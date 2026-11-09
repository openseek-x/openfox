package integrations

// 「我的飞书账号」OAuth 绑定。复用 integration_oauth_states（provider = feishu、purpose = PERSONAL）。
// 用户令牌只用来读取身份（open_id、姓名、头像），不保存。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/feishu"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// StartFeishuAuthorization 创建授权会话并返回飞书授权页地址。所有非访客成员可用。
func (s Service) StartFeishuAuthorization(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (StartResult, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return StartResult{}, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return StartResult{}, ErrForbidden
	}
	if !s.IMConfigured() {
		return StartResult{}, ErrNotConfigured
	}
	if s.Box == nil {
		return StartResult{}, ErrSecretsUnavailable
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil {
		return StartResult{}, err
	}
	if app == nil || app.Status == FeishuStatusNeedsAttention {
		return StartResult{}, ErrFeishuAppNotConnected
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_oauth_states
		   SET status = 'EXPIRED', code_verifier_sealed = NULL, pending_sealed = NULL
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND status IN ('PENDING', 'SELECTING') AND expires_at < clock_timestamp() - interval '10 minutes'`); err != nil {
		return StartResult{}, err
	}
	state, err := randomBase64URL(32)
	if err != nil {
		return StartResult{}, err
	}
	verifier, err := randomBase64URL(32)
	if err != nil {
		return StartResult{}, err
	}
	sessionID, err := membership.NewID()
	if err != nil {
		return StartResult{}, err
	}
	sealedVerifier, _, err := s.seal([]byte(verifier), oauthAAD(organizationID, sessionID))
	if err != nil {
		return StartResult{}, err
	}
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, forProv(`
		INSERT INTO openfox.integration_oauth_states (organization_id, id, provider, purpose, user_id, state_hash, code_verifier_sealed)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'feishu', 'PERSONAL', $2::uuid, $3, $4)
		RETURNING expires_at`, s.prov()), sessionID, actor.UserID, TokenHash(state), sealedVerifier).Scan(&expiresAt); err != nil {
		return StartResult{}, err
	}
	authorizeURL := ""
	if s.prov() == ProviderDingTalk {
		authorizeURL = s.DingTalk.AuthorizeURL(app.AppID, s.FeishuRedirectURI(), state)
	} else {
		authorizeURL = s.Feishu.AuthorizeURL(app.AppID, s.FeishuRedirectURI(), state, pkceChallenge(verifier))
	}
	return StartResult{AuthorizeURL: authorizeURL, SessionID: sessionID, ExpiresAt: expiresAt}, nil
}

// FeishuCallbackTarget 是回调在事务外换取令牌需要的值。
type FeishuCallbackTarget struct {
	Verifier string
	AppID    string
	Secret   string
}

// FeishuCallbackPrepare 校验会话属于该用户且仍在等待回调，读出 PKCE verifier 与组织飞书应用的 App ID / Secret。
// 会话状态不对返回 ErrOAuthSessionState；组织已没有可用的飞书应用返回 ErrFeishuAppNotConnected。
func (s Service) FeishuCallbackPrepare(ctx context.Context, tx pgx.Tx, organizationID, userID, sessionID string) (FeishuCallbackTarget, error) {
	row, err := lockSessionFor(ctx, tx, sessionID, s.prov())
	if errors.Is(err, ErrNotFound) {
		return FeishuCallbackTarget{}, ErrOAuthSessionState
	}
	if err != nil {
		return FeishuCallbackTarget{}, err
	}
	if row.UserID != userID || row.Status != statusPending || row.Consumed || row.Expired {
		return FeishuCallbackTarget{}, ErrOAuthSessionState
	}
	verifier, err := s.open(row.VerifierSealed, oauthAAD(organizationID, sessionID))
	if err != nil {
		return FeishuCallbackTarget{}, err
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil {
		return FeishuCallbackTarget{}, err
	}
	if app == nil || app.Status == FeishuStatusNeedsAttention {
		return FeishuCallbackTarget{}, ErrFeishuAppNotConnected
	}
	secret, err := s.feishuSecret(ctx, tx, organizationID, app.ConnectionID)
	if err != nil {
		return FeishuCallbackTarget{}, err
	}
	return FeishuCallbackTarget{Verifier: string(verifier), AppID: app.AppID, Secret: secret}, nil
}

// FeishuExchange 用授权码换取用户令牌并读取身份（事务外；令牌随即丢弃）。
func (s Service) FeishuExchange(ctx context.Context, target FeishuCallbackTarget, code string) FeishuCallbackOutcome {
	if s.prov() == ProviderDingTalk {
		return s.dingTalkExchange(ctx, target, code)
	}
	if s.Feishu == nil {
		return FeishuCallbackOutcome{Failure: ReasonExchangeFailed}
	}
	token, err := s.Feishu.ExchangeCode(ctx, target.AppID, target.Secret, code, s.FeishuRedirectURI(), target.Verifier)
	if err != nil {
		return FeishuCallbackOutcome{Failure: ReasonExchangeFailed}
	}
	user, err := s.Feishu.UserInfo(ctx, token)
	if err != nil {
		return FeishuCallbackOutcome{Failure: ReasonExchangeFailed}
	}
	return FeishuCallbackOutcome{AppID: target.AppID, User: user}
}

// FeishuCallbackOutcome 是回调在事务外取得的结果：Failure 非空表示授权被拒绝或换取失败。
type FeishuCallbackOutcome struct {
	Failure string
	AppID   string
	User    feishu.User
	// Extra 是服务商特有的、写进个人连接 settings 的附加键（钉钉：staff_id）。
	Extra map[string]string
}

// FeishuFinishCallback 在组织事务里消费会话（只能用一次），建立或替换本人的个人飞书连接并开启飞书通知渠道。
func (s Service) FeishuFinishCallback(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor,
	sessionID string, out FeishuCallbackOutcome) (CallbackResult, error) {
	row, err := lockSessionFor(ctx, tx, sessionID, s.prov())
	if errors.Is(err, ErrNotFound) {
		return CallbackResult{}, ErrOAuthSessionState
	}
	if err != nil {
		return CallbackResult{}, err
	}
	if row.UserID != actor.UserID || row.Status != statusPending || row.Consumed || row.Expired {
		return CallbackResult{}, ErrOAuthSessionState
	}
	result := CallbackResult{SessionID: sessionID}
	fail := func(reason string) (CallbackResult, error) {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_oauth_states
			   SET status = 'FAILED', error_code = $2, consumed_at = clock_timestamp(), completed_at = clock_timestamp(),
			       code_verifier_sealed = NULL, pending_sealed = NULL
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, sessionID, reason); err != nil {
			return CallbackResult{}, err
		}
		result.Reason = reason
		return result, nil
	}
	if out.Failure != "" {
		return fail(out.Failure)
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), true)
	if err != nil {
		return CallbackResult{}, err
	}
	// 应用必须仍有效，且仍是发起授权时的那个应用（open_id 只在同一应用内有效）。
	if app == nil || app.Status == FeishuStatusNeedsAttention || !strings.EqualFold(app.AppID, out.AppID) || out.User.OpenID == "" {
		return fail(ReasonExchangeFailed)
	}
	var taken bool
	if err := tx.QueryRow(ctx, forProv(`
		SELECT EXISTS (SELECT 1 FROM openfox.integration_connections
		                WHERE organization_id = openfox_private.current_organization_id() AND provider = 'feishu' AND scope = 'PERSONAL'
		                  AND parent_connection_id = $1::uuid AND external_id = $2 AND owner_user_id <> $3::uuid AND status <> 'DISCONNECTED')`, s.prov()),
		app.ConnectionID, out.User.OpenID, actor.UserID).Scan(&taken); err != nil {
		return CallbackResult{}, err
	}
	if taken {
		return fail(ReasonAccountInUse)
	}
	connectionID, created, err := s.upsertFeishuPersonal(ctx, tx, app.ConnectionID, actor, out.User, out.Extra)
	if errors.Is(err, errFeishuAccountInUse) {
		return fail(ReasonAccountInUse)
	}
	if err != nil {
		return CallbackResult{}, err
	}
	// 绑定后自动开启本人的 IM 通知渠道（飞书 / Lark / 钉钉共用 feishu 这一行设置）；保留已关闭的事件。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.user_notification_channels (user_id, channel, enabled)
		VALUES ($1::uuid, 'feishu', true)
		ON CONFLICT (user_id, channel) DO UPDATE SET enabled = true, updated_at = clock_timestamp()`, actor.UserID); err != nil {
		return CallbackResult{}, err
	}
	if err := completeSession(ctx, tx, sessionID, connectionID); err != nil {
		return CallbackResult{}, err
	}
	action := "integration.connection_reauthorized"
	if created {
		action = "integration.connection_created"
	}
	result.Audit = append(result.Audit, AuditEntry{Action: action, ResourceID: connectionID,
		Metadata: map[string]string{"source": s.prov(), "result": ScopePersonal}})
	return result, nil
}

// ReasonAccountInUse 表示这个飞书账号已绑定到本组织的其他成员。
const ReasonAccountInUse = "account_in_use"

var errFeishuAccountInUse = errors.New("integrations: feishu account is bound to another member")

// upsertFeishuPersonal 建立或替换本人在该应用下的个人连接。写入放在保存点里：并发绑定同一个 open_id 时
// 唯一索引会拒绝其中一个，保存点回滚后事务仍可继续写失败结果。
func (s Service) upsertFeishuPersonal(ctx context.Context, tx pgx.Tx, appConnectionID string, actor collaboration.Actor, user feishu.User, extra map[string]string) (string, bool, error) {
	name := strings.TrimSpace(user.Name)
	if name == "" {
		name = user.OpenID
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	// union_id / tenant_key 供飞书机器人识别发消息的人：不同飞书应用下同一用户的 open_id 不同，union_id 相同。
	settingsMap := map[string]string{"avatar_url": user.AvatarURL, "union_id": user.UnionID, "tenant_key": user.TenantKey}
	for key, value := range extra {
		settingsMap[key] = value
	}
	settings, _ := json.Marshal(settingsMap)
	var existing string
	err := tx.QueryRow(ctx, forProv(`
		SELECT id::text FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'feishu' AND scope = 'PERSONAL'
		   AND owner_user_id = $1::uuid AND parent_connection_id = $2::uuid AND status <> 'DISCONNECTED'
		 FOR UPDATE`, s.prov()), actor.UserID, appConnectionID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	connectionID, created := existing, existing == ""
	if created {
		if connectionID, err = membership.NewID(); err != nil {
			_ = sp.Rollback(ctx)
			return "", false, err
		}
		_, err = sp.Exec(ctx, forProv(`
			INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, parent_connection_id, owner_user_id, external_id,
			       display_name, account_kind, status, settings, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'feishu', 'PERSONAL', $2::uuid, $3::uuid, $4, $5, 'user', 'ACTIVE', $6::jsonb, $3::uuid)`, s.prov()),
			connectionID, appConnectionID, actor.UserID, user.OpenID, name, settings)
	} else {
		_, err = sp.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET external_id = $2, display_name = $3, settings = $4::jsonb, status = 'ACTIVE', status_reason = '',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			connectionID, user.OpenID, name, settings)
	}
	if err != nil {
		_ = sp.Rollback(ctx)
		if isUniqueViolation(err) {
			return "", false, errFeishuAccountInUse
		}
		return "", false, err
	}
	if err := sp.Commit(ctx); err != nil {
		return "", false, err
	}
	return connectionID, created, nil
}
