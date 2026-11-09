package integrations

// 组织飞书应用（App ID + App Secret）的查看、绑定、更新凭证、重新验证与设置。
// 对飞书的请求都在数据库事务之外：API 层先用短事务读状态与权限，再调用 FeishuValidate，最后用短事务保存。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/wecom"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const (
	feishuProvider = "feishu"

	FeishuStatusPending        = "PENDING"
	FeishuStatusConnected      = "CONNECTED"
	FeishuStatusNeedsAttention = "NEEDS_ATTENTION"

	ReasonCredentialsInvalid = "credentials_invalid"
	ReasonBotUnavailable     = "bot_unavailable"
	ReasonConnectionLimit    = "connection_limit"

	defaultFeishuOpenPlatform = "https://open.feishu.cn"
	defaultLarkOpenPlatform   = "https://open.larksuite.com"
)

// FeishuApp 对应契约 FeishuApp，刻意不含任何凭据。
type FeishuApp struct {
	ConnectionID       string     `json:"connection_id"`
	AppID              string     `json:"app_id"`
	BotName            string     `json:"bot_name"`
	BotAvatarURL       string     `json:"bot_avatar_url"`
	Status             string     `json:"status"`
	StatusReason       string     `json:"status_reason"`
	LastConnectedAt    *time.Time `json:"last_connected_at"`
	LastEventAt        *time.Time `json:"last_event_at"`
	LastError          string     `json:"last_error"`
	LinkPreviewEnabled bool       `json:"link_preview_enabled"`
	CredentialsHint    string     `json:"credentials_hint"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	Version            uint64     `json:"version"`
}

// FeishuPersonalBinding 对应契约 FeishuPersonalBinding。
type FeishuPersonalBinding struct {
	ConnectionID string    `json:"connection_id"`
	DisplayName  string    `json:"display_name"`
	AvatarURL    string    `json:"avatar_url"`
	CreatedAt    time.Time `json:"created_at"`
	Version      uint64    `json:"version"`
}

// FeishuGuide 是配置指引需要的值。
type FeishuGuide struct {
	OpenPlatformURL    string   `json:"open_platform_url"`
	RedirectURI        string   `json:"redirect_uri"`
	LinkPreviewPattern string   `json:"link_preview_pattern"`
	ScopesJSON         string   `json:"scopes_json"`
	Events             []string `json:"events"`
	Callbacks          []string `json:"callbacks"`
}

// FeishuAppView 对应契约 FeishuAppView。
type FeishuAppView struct {
	CanManage bool                   `json:"can_manage"`
	App       *FeishuApp             `json:"app"`
	Personal  *FeishuPersonalBinding `json:"personal"`
	Guide     FeishuGuide            `json:"guide"`
}

// FeishuRedirectURI 是飞书应用「安全设置 → 重定向 URL」要填的地址。
func (s Service) FeishuRedirectURI() string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/integrations/" + s.prov() + "/callback"
}

const feishuScopesJSON = `{
  "scopes": {
    "tenant": [
      "im:message:send_as_bot",
      "im:message.p2p_msg:readonly",
      "im:message.group_at_msg:readonly",
      "im:message.group_msg",
      "im:chat:readonly"
    ],
    "user": []
  }
}`

func (s Service) feishuGuide() FeishuGuide {
	if s.prov() == ProviderDingTalk {
		return s.dingTalkGuide()
	}
	platform := defaultFeishuOpenPlatform
	if s.prov() == ProviderLark {
		platform = defaultLarkOpenPlatform
	}
	if s.Feishu != nil && s.Feishu.BaseURL != "" {
		platform = s.Feishu.BaseURL
	}
	return FeishuGuide{
		OpenPlatformURL:    platform + "/app",
		RedirectURI:        s.FeishuRedirectURI(),
		LinkPreviewPattern: strings.TrimRight(s.PublicURL, "/") + "/o/*",
		ScopesJSON:         feishuScopesJSON,
		Events:             []string{"im.message.receive_v1"},
		Callbacks:          []string{"url.preview.get"},
	}
}

const feishuAppSelect = `
	SELECT c.id::text, c.external_id, c.display_name, COALESCE(c.settings->>'bot_avatar_url', ''), c.status, c.status_reason,
	       st.last_connected_at, st.last_event_at, COALESCE(st.last_error, ''), COALESCE(st.state, ''),
	       COALESCE((c.settings->>'link_preview_enabled')::boolean, true), c.credentials_hint, c.created_at, c.updated_at, c.version
	  FROM openfox.integration_connections c
	  LEFT JOIN openfox.im_connection_states st
	    ON st.organization_id = c.organization_id AND st.connection_id = c.id
	 WHERE c.organization_id = openfox_private.current_organization_id() AND c.provider = 'feishu'
	   AND c.scope = 'ORGANIZATION' AND c.account_kind = 'app' AND c.status <> 'DISCONNECTED'
	 ORDER BY c.created_at, c.id LIMIT 1`

// loadFeishuApp 返回本组织未断开的飞书应用；没有返回 (nil, nil)。lock 为 true 时锁住连接行。
func loadFeishuApp(ctx context.Context, tx pgx.Tx, provider string, lock bool) (*FeishuApp, error) {
	query := forProv(feishuAppSelect, provider)
	if lock {
		query += " FOR UPDATE OF c"
	}
	var app FeishuApp
	var connStatus, state string
	var version int64
	err := tx.QueryRow(ctx, query).Scan(&app.ConnectionID, &app.AppID, &app.BotName, &app.BotAvatarURL, &connStatus, &app.StatusReason,
		&app.LastConnectedAt, &app.LastEventAt, &app.LastError, &state, &app.LinkPreviewEnabled, &app.CredentialsHint,
		&app.CreatedAt, &app.UpdatedAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	app.Version = uint64(version)
	switch {
	case connStatus == StatusNeedsAttention:
		app.Status = FeishuStatusNeedsAttention
	case state == "CONNECTED":
		app.Status = FeishuStatusConnected
	default:
		app.Status = FeishuStatusPending
	}
	return &app, nil
}

// loadFeishuPersonal 返回成员在指定应用下未断开的个人绑定。
func loadFeishuPersonal(ctx context.Context, tx pgx.Tx, provider, appConnectionID, userID string) (*FeishuPersonalBinding, error) {
	var item FeishuPersonalBinding
	var version int64
	err := tx.QueryRow(ctx, forProv(`
		SELECT id::text, display_name, COALESCE(settings->>'avatar_url', ''), created_at, version
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'feishu' AND scope = 'PERSONAL'
		   AND owner_user_id = $1::uuid AND parent_connection_id = $2::uuid AND status <> 'DISCONNECTED'`, provider),
		userID, appConnectionID).Scan(&item.ConnectionID, &item.DisplayName, &item.AvatarURL, &item.CreatedAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.Version = uint64(version)
	return &item, nil
}

// FeishuView 返回飞书详情。所有非访客成员可读。
func (s Service) FeishuView(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (FeishuAppView, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return FeishuAppView{}, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return FeishuAppView{}, ErrForbidden
	}
	view := FeishuAppView{CanManage: CanManage(actor.Role), Guide: s.feishuGuide()}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil {
		return FeishuAppView{}, err
	}
	view.App = app
	if app != nil {
		if view.Personal, err = loadFeishuPersonal(ctx, tx, s.prov(), app.ConnectionID, actor.UserID); err != nil {
			return FeishuAppView{}, err
		}
	}
	return view, nil
}

// FeishuPlan 是第一步（短事务）读到的现状：ConnectionID 为空表示尚未绑定。
type FeishuPlan struct {
	ConnectionID string
	AppID        string
	Version      uint64
}

func requireFeishuManager(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !CanManage(actor.Role) {
		return ErrForbidden
	}
	return nil
}

// FeishuPrepare 校验权限、版本与 App ID 是否一致，不访问飞书。
func (s Service) FeishuPrepare(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, appID string, version uint64) (FeishuPlan, error) {
	if err := requireFeishuManager(ctx, tx, organizationID, actor); err != nil {
		return FeishuPlan{}, err
	}
	if s.Box == nil {
		return FeishuPlan{}, ErrSecretsUnavailable
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil {
		return FeishuPlan{}, err
	}
	if app == nil {
		return FeishuPlan{}, nil
	}
	if version == 0 {
		return FeishuPlan{}, ErrInvalidRequest
	}
	if version != app.Version {
		return FeishuPlan{}, ErrVersionConflict
	}
	if !strings.EqualFold(app.AppID, appID) {
		return FeishuPlan{}, ErrFeishuAppMismatch
	}
	return FeishuPlan{ConnectionID: app.ConnectionID, AppID: app.AppID, Version: app.Version}, nil
}

// FeishuValidate 用 App ID / Secret 换取 tenant_access_token 并读取机器人信息（不碰数据库）。
func (s Service) FeishuValidate(ctx context.Context, appID, secret string) (feishu.Bot, error) {
	if s.prov() == ProviderDingTalk {
		return s.dingTalkValidate(ctx, appID, secret)
	}
	if s.prov() == ProviderWeCom {
		if s.WeCom == nil {
			return feishu.Bot{}, ErrNotConfigured
		}
		if err := s.WeCom.Validate(ctx, appID, secret); err != nil {
			if wecom.IsPermanent(err) {
				return feishu.Bot{}, ErrFeishuCredentialsInvalid
			}
			return feishu.Bot{}, ErrFeishuUnavailable
		}
		return feishu.Bot{}, nil
	}
	if s.Feishu == nil {
		return feishu.Bot{}, ErrNotConfigured
	}
	// 校验必须真的问一次飞书：丢弃缓存的 token，否则凭证已被停用时仍会用旧 token 通过。
	s.Feishu.ClearToken(appID, secret)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		token, err := s.Feishu.TenantToken(ctx, appID, secret)
		if err != nil {
			return feishu.Bot{}, mapFeishuValidateError(err)
		}
		bot, err := s.Feishu.BotInfo(ctx, token)
		if err == nil {
			return bot, nil
		}
		if errors.Is(err, feishu.ErrTokenExpired) {
			s.Feishu.ClearToken(appID, secret)
			lastErr = err
			continue
		}
		return feishu.Bot{}, mapFeishuValidateError(err)
	}
	return feishu.Bot{}, mapFeishuValidateError(lastErr)
}

func mapFeishuValidateError(err error) error {
	switch {
	case errors.Is(err, feishu.ErrInvalidCredentials):
		return ErrFeishuCredentialsInvalid
	case errors.Is(err, feishu.ErrBotUnavailable):
		return ErrFeishuBotUnavailable
	default:
		return ErrFeishuUnavailable
	}
}

func botSettings(bot feishu.Bot) map[string]any {
	return map[string]any{"bot_open_id": bot.OpenID, "bot_avatar_url": bot.AvatarURL}
}

func botDisplayName(bot feishu.Bot, appID string) string {
	name := strings.TrimSpace(bot.AppName)
	if name == "" {
		return appID
	}
	if r := []rune(name); len(r) > 200 {
		return string(r[:200])
	}
	return name
}

// FeishuSave 保存校验通过的应用：新绑定插入连接与长连接状态；更新凭证重置状态让持有者重连。
func (s Service) FeishuSave(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, plan FeishuPlan,
	appID, secret string, bot feishu.Bot) (FeishuApp, []AuditEntry, error) {
	if err := requireFeishuManager(ctx, tx, organizationID, actor); err != nil {
		return FeishuApp{}, nil, err
	}
	current, err := loadFeishuApp(ctx, tx, s.prov(), true)
	if err != nil {
		return FeishuApp{}, nil, err
	}
	var audit AuditEntry
	if plan.ConnectionID == "" {
		if current != nil {
			return FeishuApp{}, nil, ErrVersionConflict
		}
		connectionID, err := membership.NewID()
		if err != nil {
			return FeishuApp{}, nil, err
		}
		blob, keyID, err := s.sealCredentials(organizationID, connectionID, connectionCredentials{AppSecret: secret})
		if err != nil {
			return FeishuApp{}, nil, err
		}
		settings := botSettings(bot)
		settings["link_preview_enabled"] = true
		settingsJSON, _ := json.Marshal(settings)
		if _, err := tx.Exec(ctx, forProv(`
			INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, external_id, display_name, account_kind, status,
			       credentials_sealed, credentials_key_id, credentials_hint, settings, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'feishu', 'ORGANIZATION', $2, $3, 'app', 'ACTIVE',
			        $4, $5, $6, $7::jsonb, $8::uuid)`, s.prov()),
			connectionID, appID, botDisplayName(bot, appID), blob, keyID, tokenHint(secret), settingsJSON, actor.UserID); err != nil {
			if isUniqueViolation(err) {
				return FeishuApp{}, nil, ErrFeishuAppInUse
			}
			return FeishuApp{}, nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.im_connection_states (organization_id, connection_id, state)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'PENDING')`, connectionID); err != nil {
			return FeishuApp{}, nil, err
		}
		audit = AuditEntry{Action: "integration.connection_created", ResourceID: connectionID,
			Metadata: map[string]string{"source": s.prov(), "result": ScopeOrganization}}
	} else {
		if current == nil || current.ConnectionID != plan.ConnectionID || current.Version != plan.Version {
			return FeishuApp{}, nil, ErrVersionConflict
		}
		blob, keyID, err := s.sealCredentials(organizationID, plan.ConnectionID, connectionCredentials{AppSecret: secret})
		if err != nil {
			return FeishuApp{}, nil, err
		}
		patch, _ := json.Marshal(botSettings(bot))
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET display_name = $3, credentials_sealed = $4, credentials_key_id = $5, credentials_hint = $6,
			       settings = settings || $7::jsonb, status = 'ACTIVE', status_reason = '',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
			plan.ConnectionID, int64(plan.Version), botDisplayName(bot, appID), blob, keyID, tokenHint(secret), patch)
		if err != nil {
			return FeishuApp{}, nil, err
		}
		if tag.RowsAffected() != 1 {
			return FeishuApp{}, nil, ErrVersionConflict
		}
		if err := resetFeishuState(ctx, tx, plan.ConnectionID); err != nil {
			return FeishuApp{}, nil, err
		}
		audit = AuditEntry{Action: "integration.connection_reauthorized", ResourceID: plan.ConnectionID,
			Metadata: map[string]string{"source": s.prov(), "result": ScopeOrganization}}
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil || app == nil {
		return FeishuApp{}, nil, errors.Join(err, errors.New("integrations: feishu app missing after save"))
	}
	return *app, []AuditEntry{audit}, nil
}

// resetFeishuState 让长连接管理器重新接手：状态 PENDING、清空错误与租约。
func resetFeishuState(ctx context.Context, tx pgx.Tx, connectionID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.im_connection_states (organization_id, connection_id, state)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'PENDING')
		ON CONFLICT (organization_id, connection_id) DO UPDATE
		   SET state = 'PENDING', failure_count = 0, last_error = '', lease_owner = '', lease_expires_at = NULL,
		       updated_at = clock_timestamp()`, connectionID)
	return err
}

// FeishuVerifyTarget 是重新验证第一步读到的现状与已保存的 Secret。
type FeishuVerifyTarget struct {
	Plan   FeishuPlan
	Secret string
}

// FeishuVerifyPrepare 校验权限并解密已保存的 Secret；没有有效应用返回 ErrNotFound。
func (s Service) FeishuVerifyPrepare(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (FeishuVerifyTarget, error) {
	if err := requireFeishuManager(ctx, tx, organizationID, actor); err != nil {
		return FeishuVerifyTarget{}, err
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil {
		return FeishuVerifyTarget{}, err
	}
	if app == nil {
		return FeishuVerifyTarget{}, ErrNotFound
	}
	secret, err := s.feishuSecret(ctx, tx, organizationID, app.ConnectionID)
	if err != nil {
		return FeishuVerifyTarget{}, err
	}
	return FeishuVerifyTarget{Plan: FeishuPlan{ConnectionID: app.ConnectionID, AppID: app.AppID, Version: app.Version}, Secret: secret}, nil
}

// feishuSecret 读取并解密组织飞书应用的 App Secret。
func (s Service) feishuSecret(ctx context.Context, tx pgx.Tx, organizationID, connectionID string) (string, error) {
	var sealed []byte
	err := tx.QueryRow(ctx, forProv(`
		SELECT credentials_sealed FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND provider = 'feishu'`, s.prov()),
		connectionID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	creds, err := s.openCredentials(organizationID, connectionID, sealed)
	if err != nil {
		return "", err
	}
	if creds.AppSecret == "" {
		return "", ErrFeishuCredentialsInvalid
	}
	return creds.AppSecret, nil
}

// FeishuVerifyFinish 回写验证结果。校验失败（凭证无效 / 机器人不可用）时把连接标为 NEEDS_ATTENTION 并保存，
// 由调用方在提交后返回对应的 422；飞书不可达（ErrFeishuUnavailable）不改变任何状态。
func (s Service) FeishuVerifyFinish(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, plan FeishuPlan,
	bot feishu.Bot, validateErr error) (FeishuApp, []AuditEntry, error) {
	if err := requireFeishuManager(ctx, tx, organizationID, actor); err != nil {
		return FeishuApp{}, nil, err
	}
	current, err := loadFeishuApp(ctx, tx, s.prov(), true)
	if err != nil {
		return FeishuApp{}, nil, err
	}
	if current == nil || current.ConnectionID != plan.ConnectionID || current.Version != plan.Version {
		return FeishuApp{}, nil, ErrVersionConflict
	}
	var audits []AuditEntry
	switch {
	case validateErr == nil:
		patch, _ := json.Marshal(botSettings(bot))
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET display_name = $2, settings = settings || $3::jsonb, status = 'ACTIVE', status_reason = '',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			plan.ConnectionID, botDisplayName(bot, plan.AppID), patch); err != nil {
			return FeishuApp{}, nil, err
		}
		if err := resetFeishuState(ctx, tx, plan.ConnectionID); err != nil {
			return FeishuApp{}, nil, err
		}
		audits = append(audits, AuditEntry{Action: "integration.connection_verified", ResourceID: plan.ConnectionID,
			Metadata: map[string]string{"source": s.prov(), "result": "ok"}})
	case errors.Is(validateErr, ErrFeishuCredentialsInvalid), errors.Is(validateErr, ErrFeishuBotUnavailable):
		reason := ReasonCredentialsInvalid
		if errors.Is(validateErr, ErrFeishuBotUnavailable) {
			reason = ReasonBotUnavailable
		}
		if err := markFeishuNeedsAttention(ctx, tx, plan.ConnectionID, reason); err != nil {
			return FeishuApp{}, nil, err
		}
		audits = append(audits, AuditEntry{Action: "integration.connection_verified", ResourceID: plan.ConnectionID,
			Metadata: map[string]string{"source": s.prov(), "result": reason}})
	default:
		return *current, nil, nil
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil || app == nil {
		return FeishuApp{}, nil, errors.Join(err, errors.New("integrations: feishu app missing after verify"))
	}
	return *app, audits, nil
}

// markFeishuNeedsAttention 把飞书应用连接标为需要处理，并让长连接状态停在 FAILED（不再重连，直到重新验证）。
func markFeishuNeedsAttention(ctx context.Context, tx pgx.Tx, connectionID, reason string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET status = 'NEEDS_ATTENTION', status_reason = $2, updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'ACTIVE'`,
		connectionID, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.im_connection_states (organization_id, connection_id, state, last_error)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'FAILED', $2)
		ON CONFLICT (organization_id, connection_id) DO UPDATE
		   SET state = 'FAILED', last_error = EXCLUDED.last_error, lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()`,
		connectionID, reason)
	return err
}

// FeishuPatchSettings 修改任务链接预览开关。
func (s Service) FeishuPatchSettings(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor,
	linkPreview bool, version uint64) (FeishuApp, []AuditEntry, error) {
	if err := requireFeishuManager(ctx, tx, organizationID, actor); err != nil {
		return FeishuApp{}, nil, err
	}
	current, err := loadFeishuApp(ctx, tx, s.prov(), true)
	if err != nil {
		return FeishuApp{}, nil, err
	}
	if current == nil {
		return FeishuApp{}, nil, ErrNotFound
	}
	if current.Version != version {
		return FeishuApp{}, nil, ErrVersionConflict
	}
	patch, _ := json.Marshal(map[string]any{"link_preview_enabled": linkPreview})
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET settings = settings || $2::jsonb, updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		current.ConnectionID, patch); err != nil {
		return FeishuApp{}, nil, err
	}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil || app == nil {
		return FeishuApp{}, nil, errors.Join(err, errors.New("integrations: feishu app missing after patch"))
	}
	audit := AuditEntry{Action: "integration.settings_updated", ResourceID: current.ConnectionID,
		Metadata: map[string]string{"source": s.prov(), "result": "link_preview"}}
	return *app, []AuditEntry{audit}, nil
}

// disconnectFeishu 在断开飞书连接的同一事务里收尾：
// 组织应用 → 长连接状态 STOPPED、清租约，该应用下待发的通知改为 SKIPPED；个人绑定 → 其待发通知 SKIPPED。
func disconnectFeishu(ctx context.Context, tx pgx.Tx, scope, connectionID string) error {
	if scope == ScopeOrganization {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.im_connection_states (organization_id, connection_id, state)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'STOPPED')
			ON CONFLICT (organization_id, connection_id) DO UPDATE
			   SET state = 'STOPPED', lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()`, connectionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE openfox.im_notification_outbox
			   SET status = 'SKIPPED', last_error = 'app_disconnected', lease_owner = '', lease_expires_at = NULL,
			       finished_at = clock_timestamp(), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND app_connection_id = $1::uuid
			   AND status IN ('PENDING', 'SENDING')`, connectionID)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.im_notification_outbox
		   SET status = 'SKIPPED', last_error = 'account_unbound', lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND personal_connection_id = $1::uuid
		   AND status IN ('PENDING', 'SENDING')`, connectionID)
	return err
}
