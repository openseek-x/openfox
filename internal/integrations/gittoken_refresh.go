package integrations

// Git 服务商 OAuth 令牌续期。GitLab 访问令牌 2 小时过期、Gitee 1 天过期，刷新令牌每次使用后换新（旧的立即失效）。
// 后台任务在令牌到期前（Lead，默认 15 分钟）主动续期，其余代码照常读取令牌，不需要关心过期。
// 网络请求都在事务外。多实例同时续期时，读取阶段先「认领」连接：行锁下把 token_expires_at 推到扫描窗口之外
// （只有续期任务读这个列），其他实例因此跳过它，不会拿已经作废的旧刷新令牌去换、再把连接误标为需要重新授权。
// 换取失败（网络 / 5xx）时把 token_expires_at 恢复原值；进程中途退出时认领在 tokenRefreshClaim 后自然失效。
// 回写时仍比对刷新令牌，防止认领期间管理员重新授权。

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations/gitapi"
	"openfox/internal/tenancy"
)

const (
	tokenRefreshInterval = 30 * time.Second
	tokenRefreshLead     = 15 * time.Minute
	tokenRefreshBatch    = 100
	tokenRefreshMaxPass  = 500
	// tokenRefreshClaim 是认领的有效期，需大于一次换取请求的超时。
	tokenRefreshClaim = 2 * time.Minute
)

// TokenRefresher 周期性续期即将过期的 OAuth 令牌。
type TokenRefresher struct {
	Pool     SyncRunner
	Logger   *slog.Logger
	Service  Service
	Interval time.Duration
	Lead     time.Duration
}

func (r *TokenRefresher) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Run 循环直到 ctx 取消。
func (r *TokenRefresher) Run(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = tokenRefreshInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger().Warn("git_token_refresh_pass_failed", "error", err.Error())
		}
		timer.Reset(interval)
	}
}

type refreshTarget struct{ OrganizationID, ConnectionID string }

// RunOnce 执行一轮：列出即将过期的连接并逐个续期（测试的单轮入口）。
func (r *TokenRefresher) RunOnce(ctx context.Context) error {
	if r.Pool == nil {
		return errors.New("integrations: nil token refresher pool")
	}
	lead := r.Lead
	if lead <= 0 {
		lead = tokenRefreshLead
	}
	before := time.Now().Add(lead)
	var targets []refreshTarget
	var afterOrg, afterConn any
	for len(targets) < tokenRefreshMaxPass {
		var page []refreshTarget
		err := r.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT organization_id::text, connection_id::text FROM openfox_private.list_git_token_refresh($1, $2::uuid, $3::uuid, $4)`,
				before, afterOrg, afterConn, tokenRefreshBatch)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var item refreshTarget
				if err := rows.Scan(&item.OrganizationID, &item.ConnectionID); err != nil {
					return err
				}
				page = append(page, item)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		targets = append(targets, page...)
		if len(page) < tokenRefreshBatch {
			break
		}
		afterOrg, afterConn = page[len(page)-1].OrganizationID, page[len(page)-1].ConnectionID
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.refresh(ctx, target, before); err != nil {
			r.logger().Warn("git_token_refresh_failed", "organization_id", target.OrganizationID, "connection_id", target.ConnectionID, "error", err.Error())
		}
	}
	return nil
}

func (r *TokenRefresher) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: syncWorkerActorID, AuthorizationVersion: 1}
	return r.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

// refresh 续期一个连接：读取并认领（短事务）→ 换取新令牌（事务外）→ 回写（短事务，带刷新令牌比对）。
func (r *TokenRefresher) refresh(ctx context.Context, target refreshTarget, before time.Time) error {
	var provider, oldRefresh string
	var inst GitInstance
	var expiresAt time.Time
	claimUntil := before.Add(tokenRefreshClaim).Truncate(time.Microsecond)
	err := r.inOrganization(ctx, target.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var sealed, settingsRaw []byte
		var parent *string
		var expires *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT provider, credentials_sealed, settings, parent_connection_id::text, token_expires_at FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'
			 FOR UPDATE`, target.ConnectionID).
			Scan(&provider, &sealed, &settingsRaw, &parent, &expires); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errSkipRefresh
			}
			return err
		}
		// 已被别的实例认领或刚续期过：不在窗口内就跳过。
		if expires == nil || expires.After(before) {
			return errSkipRefresh
		}
		expiresAt = *expires
		creds, err := r.Service.openCredentials(target.OrganizationID, target.ConnectionID, sealed)
		if err != nil {
			return err
		}
		if creds.RefreshToken == "" {
			return errSkipRefresh
		}
		oldRefresh = creds.RefreshToken
		if IsGitLabFamily(provider) {
			// 个人连接沿用所属组织连接的实例与应用凭据。
			source, sourceID := settingsRaw, target.ConnectionID
			sourceSealed := sealed
			if parent != nil {
				sourceID = *parent
				if err := tx.QueryRow(ctx, `
					SELECT settings, credentials_sealed FROM openfox.integration_connections
					 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, sourceID).Scan(&source, &sourceSealed); err != nil {
					return err
				}
			}
			var found bool
			if inst, found, err = r.Service.instanceFrom(target.OrganizationID, sourceID, source, sourceSealed); err != nil {
				return err
			}
			if !found {
				return errSkipRefresh
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE openfox.integration_connections SET token_expires_at = $2
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, target.ConnectionID, claimUntil)
		return err
	})
	if errors.Is(err, errSkipRefresh) {
		return nil
	}
	if err != nil {
		return err
	}
	client, err := r.Service.gitClient(provider, inst)
	if err != nil {
		return errors.Join(err, r.release(ctx, target, claimUntil, expiresAt))
	}
	token, refreshErr := client.Refresh(ctx, oldRefresh)
	if refreshErr != nil && !errors.Is(refreshErr, gitapi.ErrTokenRevoked) {
		// 网络错误 / 5xx：释放认领，下一轮再试。
		return errors.Join(refreshErr, r.release(ctx, target, claimUntil, expiresAt))
	}
	return r.inOrganization(ctx, target.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var sealed []byte
		if err := tx.QueryRow(ctx, `
			SELECT credentials_sealed FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'
			 FOR UPDATE`, target.ConnectionID).Scan(&sealed); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		creds, err := r.Service.openCredentials(target.OrganizationID, target.ConnectionID, sealed)
		if err != nil {
			return err
		}
		if creds.RefreshToken != oldRefresh {
			return nil // 别的实例已经续期过了
		}
		if refreshErr != nil {
			return r.Service.MarkTokenRevoked(ctx, tx, target.ConnectionID)
		}
		creds.AccessToken = token.Access
		if token.Refresh != "" {
			creds.RefreshToken = token.Refresh
		}
		blob, keyID, err := r.Service.sealCredentials(target.OrganizationID, target.ConnectionID, creds)
		if err != nil {
			return err
		}
		// 续期不改 version：它不是用户可见的配置变更，不能让页面上的乐观锁凭空失效。
		_, err = tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET credentials_sealed = $2, credentials_key_id = $3, credentials_hint = $4, token_expires_at = $5, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			target.ConnectionID, blob, keyID, tokenHint(creds.AccessToken), nullableTime(token.ExpiresAt))
		return err
	})
}

// release 把认领时改掉的 token_expires_at 恢复原值；期间被重新授权或续期改过就不动。
func (r *TokenRefresher) release(ctx context.Context, target refreshTarget, claimUntil, expiresAt time.Time) error {
	return r.inOrganization(ctx, target.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.integration_connections SET token_expires_at = $3
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND token_expires_at = $2`,
			target.ConnectionID, claimUntil, expiresAt)
		return err
	})
}

var errSkipRefresh = errors.New("integrations: skip token refresh")
