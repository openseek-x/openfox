package imports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/platform/safehttp"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

// WorkerActorID 是组织 worker 上下文使用的系统主体（与 dispatch 一致）。
const WorkerActorID = "00000000-0000-0000-0000-000000000000"

// Runner 是 worker 需要的最小数据库边界，便于测试替换。
type Runner interface {
	RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error
	RunForOrganization(ctx context.Context, organizationID string, source tenancy.Source,
		selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error
}

const (
	pollInterval   = 2 * time.Second
	maxBackoff     = 30 * time.Second
	leaseDuration  = 60 * time.Second
	batchSize      = 50
	linkBatchSize  = 200
	maxBatchesPass = 10
	maxAttempts    = 20
	draftTTL       = 24 * time.Hour
	scanLimit      = 100
)

// Worker 周期性扫描有导入工作的组织：关联外部账号、清理过期草稿、执行导入。
// 单组织失败只记日志，不阻塞其他组织。
type Worker struct {
	Pool     Runner
	Logger   *slog.Logger
	Instance string
	// MaxBatches 为每个组织每轮最多处理的批数（默认 10），测试可调小。
	MaxBatches int
	// Interval 为轮询间隔（默认 2 秒）。
	Interval time.Duration
	// Secrets 用于解密 API 导入的凭据；为空时拉取中的 API 草稿会被丢弃（SECRETS_UNAVAILABLE）。
	Secrets *secrets.Box
	// Safe 是出站 HTTP 的防护配置，零值即生产的严格配置；测试注入放行地址与根证书。
	Safe safehttp.Options
	// FetchBackoff 是拉取失败后的退避基数（默认 30 秒），测试可调小。
	FetchBackoff time.Duration
	// Storage 是克隆空间的快照包存储；为空时克隆任务会失败（STORAGE_UNAVAILABLE）。
	Storage artifacts.Storage

	service Service
	cursor  string
}

// NewInstanceID 生成实例标识：hostname + pid + 随机串（≤ 128 字）。
func NewInstanceID() string {
	host, _ := os.Hostname()
	if len(host) > 80 {
		host = host[:80]
	}
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(buf)
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *Worker) instance() string {
	if w.Instance == "" {
		w.Instance = NewInstanceID()
	}
	return w.Instance
}

// Run 循环直到 ctx 取消；整轮失败时等待时间逐次翻倍（上限 30 秒）。
func (w *Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = pollInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			failures++
			wait := interval
			for i := 1; i < failures && wait < maxBackoff; i++ {
				wait *= 2
			}
			if wait > maxBackoff {
				wait = maxBackoff
			}
			w.logger().Warn("import_pass_failed", "error", err.Error(), "consecutive_failures", failures, "retry_in", wait.String())
			timer.Reset(wait)
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// RunOnce 执行一轮：列出有工作的组织并逐个处理（测试的单轮入口）。
func (w *Worker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("imports: nil worker pool")
	}
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_import_organizations($1::uuid, $2)`, after, scanLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.RunOrganization(ctx, id); err != nil {
			w.logger().Warn("import_organization_failed", "organization_id", id, "error", err.Error())
		}
		w.cursor = id
	}
	if len(ids) < scanLimit {
		w.cursor = ""
	}
	return nil
}

func (w *Worker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: WorkerActorID, AuthorizationVersion: 1}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

// RunOrganization 依次：关联外部账号、过期草稿、执行导入。各阶段失败互不影响，返回第一个错误。
func (w *Worker) RunOrganization(ctx context.Context, organizationID string) error {
	var first error
	record := func(name string, err error) {
		if err != nil {
			w.logger().Warn("import_phase_failed", "organization_id", organizationID, "phase", name, "error", err.Error())
			if first == nil {
				first = err
			}
		}
	}
	record("link_accounts", w.inOrganization(ctx, organizationID, w.linkExternalAccounts))
	record("expire_drafts", w.inOrganization(ctx, organizationID, w.expireDrafts))
	record("execute", w.executeImports(ctx, organizationID))
	record("project_sync", w.syncProjectImports(ctx, organizationID))
	return first
}

// expireDrafts 把超过 24 小时的草稿改为 DISCARDED 并释放原始行。
func (w *Worker) expireDrafts(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		WITH expired AS (
			UPDATE openfox.import_jobs SET status = 'DISCARDED', phase = '', credentials_sealed = NULL, credentials_key_id = '',
			       lease_owner = '', lease_expires_at = NULL, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND status = 'DRAFT'
			   AND created_at <= clock_timestamp() - make_interval(secs => $1)
			RETURNING id
		)
		UPDATE openfox.import_items i SET raw = '{}'::jsonb
		  FROM expired e
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.job_id = e.id`, draftTTL.Seconds())
	return err
}

// linkExternalAccounts 把邀请已被接受的外部账号关联到成员，并把对应任务的处理人改为该成员（不触发自动化）。
func (w *Worker) linkExternalAccounts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', 'link', true)`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, m.user_id::text, a.created_by::text
		  FROM openfox.external_accounts a
		  JOIN openfox.invitations i ON i.id = a.invitation_id AND i.status = 'ACCEPTED'
		  JOIN openfox.users u ON u.normalized_email = i.normalized_email
		  JOIN openfox.organization_members m ON m.organization_id = a.organization_id AND m.user_id = u.id AND m.status = 'ACTIVE'
		 WHERE a.organization_id = openfox_private.current_organization_id() AND a.linked_user_id IS NULL
		 ORDER BY a.created_at, a.id LIMIT $1`, scanLimit)
	if err != nil {
		return err
	}
	type link struct{ account, user, createdBy string }
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.account, &l.user, &l.createdBy); err != nil {
			rows.Close()
			return err
		}
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.external_accounts SET linked_user_id = $2::uuid, linked_at = clock_timestamp(), link_method = 'invite',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND linked_user_id IS NULL`,
			l.account, l.user); err != nil {
			return err
		}
		if _, err := reassignTasks(ctx, tx, l.account, l.user, l.createdBy); err != nil {
			return err
		}
	}
	return nil
}
