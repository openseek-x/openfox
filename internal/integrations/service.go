package integrations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/dingtalk"
	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/github"
	"openfox/internal/integrations/wechat"
	"openfox/internal/integrations/wecom"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/secrets"
)

var (
	ErrNotFound             = errors.New("integrations: not found")
	ErrForbidden            = errors.New("integrations: forbidden")
	ErrVersionConflict      = errors.New("integrations: version conflict")
	ErrConfirmationMismatch = errors.New("integrations: confirmation phrase mismatch")
	ErrInvalidRequest       = errors.New("integrations: invalid request")
	// GitHub 授权。
	ErrNotConfigured      = errors.New("integrations: provider is not configured")
	ErrSecretsUnavailable = errors.New("integrations: encryption key is not configured")
	ErrOAuthSessionState  = errors.New("integrations: oauth session is not in a completable state")
	// Issue 同步。
	ErrNeedsReauth             = errors.New("integrations: github token revoked, reauthorization required")
	ErrUpstreamUnavailable     = errors.New("integrations: github is unavailable")
	ErrInvalidRepository       = errors.New("integrations: repository is not accessible or not owned by the connection account")
	ErrSyncLinkExists          = errors.New("integrations: repository is already linked")
	ErrSyncBidirectionalExists = errors.New("integrations: space already has a bidirectional sync")
	// 飞书应用。
	ErrFeishuCredentialsInvalid = errors.New("integrations: feishu app credentials are invalid")
	ErrFeishuBotUnavailable     = errors.New("integrations: feishu bot is unavailable")
	ErrFeishuUnavailable        = errors.New("integrations: feishu is unavailable")
	ErrFeishuAppInUse           = errors.New("integrations: feishu app is bound to another organization")
	ErrFeishuAppMismatch        = errors.New("integrations: feishu app id differs from the connected app")
	ErrFeishuAppNotConnected    = errors.New("integrations: no active feishu app in this organization")
	// 任务助手与 Agent 机器人。
	ErrFeishuAssistantExists = errors.New("integrations: the space already has a feishu task assistant")
	ErrFeishuAgentConnected  = errors.New("integrations: the agent is already connected to feishu")
)

const (
	StatusActive         = "ACTIVE"
	StatusNeedsAttention = "NEEDS_ATTENTION"
	StatusDisconnected   = "DISCONNECTED"

	ScopeOrganization = "ORGANIZATION"
	ScopePersonal     = "PERSONAL"

	// 单次详情最多返回的连接与同步关联数，防止无界读取。
	maxDetailRows = 500
)

// Service 不持有状态，除了服务端配置判断。tx 必须是已设置租户上下文的组织事务。
type Service struct {
	// GitHubConfigured 表示服务端是否同时配置了 GitHub OAuth App 的 client id 与 secret。
	GitHubConfigured bool
	// GitHub 是 GitHub 客户端；未配置时为 nil，授权接口返回 ErrNotConfigured。
	GitHub *github.Client
	// GiteeConfigured 表示服务端是否同时配置了 Gitee 第三方应用的 Client ID 与 Secret；Gitee 客户端经 GitFactory（WithGitee）提供。
	GiteeConfigured bool
	// Git 是当前绑定的 Git 服务商（github / gitlab / gitlab_cn / gitee），空值按 github 处理；用 ForGit 取副本。
	Git string
	// GitFactory 按实例信息构造 GitLab / 极狐客户端；为 nil 时这两类服务商不可用。
	GitFactory GitClientFactory
	// ValidateInstance 校验并规范化 GitLab 实例地址（SSRF 防护）；为 nil 时拒绝一切 GitLab 实例地址。
	ValidateInstance func(ctx context.Context, rawURL string) (string, error)
	// Feishu 是飞书客户端；为 nil 时飞书应用校验与授权返回 ErrNotConfigured。
	Feishu *feishu.Client
	// DingTalk 是钉钉客户端；为 nil 时钉钉应用校验与授权返回 ErrNotConfigured。
	DingTalk *dingtalk.Client
	// WeChat 是腾讯 ClawBot（iLink）客户端；为 nil 时微信扫码绑定返回 ErrNotConfigured。
	WeChat *wechat.Client
	WeCom  *wecom.Client
	// IM 是当前绑定的 IM 服务商（feishu / lark / dingtalk），空值按 feishu 处理；用 For 取副本。
	IM string
	// IMClients 是飞书家族各服务商的客户端（feishu、lark），键为服务商标识。
	IMClients map[string]*feishu.Client
	// Box 加密落库的令牌与 Webhook 密钥；为 nil 时相关接口返回 ErrSecretsUnavailable。
	Box *secrets.Box
	// PublicURL 是中心对外地址（OPENFOX_SERVER_PUBLIC_URL），用于回调、Webhook 地址与回链。
	PublicURL string
}

type ProviderSummary struct {
	Connections int    `json:"connections"`
	Status      string `json:"status"`
}

// ProviderView 对应契约 IntegrationProvider。
type ProviderView struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Category     string          `json:"category"`
	Availability string          `json:"availability"`
	Template     string          `json:"template"`
	Capabilities []string        `json:"capabilities"`
	Configured   bool            `json:"configured"`
	Bindable     bool            `json:"bindable"`
	Summary      ProviderSummary `json:"summary"`
	// OAuthRedirectURI 是 Git 服务商 OAuth 应用需要登记的回调地址；其他服务商为空。
	OAuthRedirectURI string `json:"oauth_redirect_uri"`
	// DefaultInstanceURL 是 GitLab 系服务商的默认实例地址（极狐 SaaS）；其他为空。
	DefaultInstanceURL string `json:"default_instance_url"`
}

type Overview struct {
	Providers []ProviderView `json:"providers"`
	CanManage bool           `json:"can_manage"`
}

// Connection 对应契约 IntegrationConnection，刻意不含任何凭据字段。
type Connection struct {
	ID                 string     `json:"id"`
	Provider           string     `json:"provider"`
	Scope              string     `json:"scope"`
	ParentConnectionID *string    `json:"parent_connection_id,omitempty"`
	OwnerUserID        *string    `json:"owner_user_id,omitempty"`
	ExternalID         string     `json:"external_id"`
	DisplayName        string     `json:"display_name"`
	AccountKind        string     `json:"account_kind"`
	Status             string     `json:"status"`
	StatusReason       string     `json:"status_reason"`
	CredentialsHint    string     `json:"credentials_hint"`
	CreatedBy          string     `json:"created_by"`
	CreatedByEmail     string     `json:"created_by_email"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	DisconnectedAt     *time.Time `json:"disconnected_at"`
	Version            uint64     `json:"version"`
	// AvatarURL 取自 settings.avatar_url（GitHub 组织 / 账号头像）。
	AvatarURL string `json:"avatar_url"`
	// WebhookLastDeliveryAt 为空表示 Webhook 尚未接通。
	WebhookLastDeliveryAt *time.Time `json:"webhook_last_delivery_at"`
	// InstanceURL 是 GitLab / 极狐连接所属实例的规范化地址（scheme://host[:port][/path]，不含查询串与凭据；个人连接取所属组织连接的）。
	// 其他服务商为空串。
	InstanceURL string `json:"instance_url"`
}

type SyncLink struct {
	ID                  string    `json:"id"`
	ConnectionID        string    `json:"connection_id"`
	SpaceID             string    `json:"space_id"`
	SpaceName           string    `json:"space_name"`
	ExternalProjectID   string    `json:"external_project_id"`
	ExternalProjectName string    `json:"external_project_name"`
	Direction           string    `json:"direction"`
	Status              string    `json:"status"`
	StatusReason        string    `json:"status_reason"`
	CreatedAt           time.Time `json:"created_at"`
	Version             uint64    `json:"version"`
	// Issue 同步。
	RepositoryURL     string       `json:"repository_url"`
	RepositoryPrivate bool         `json:"repository_private"`
	LastSyncedAt      *time.Time   `json:"last_synced_at"`
	Backfill          SyncBackfill `json:"backfill"`
	Outbound          SyncOutbound `json:"outbound"`
}

type Detail struct {
	Provider            ProviderView `json:"provider"`
	CanManage           bool         `json:"can_manage"`
	Connections         []Connection `json:"connections"`
	PersonalConnections []Connection `json:"personal_connections"`
	SyncLinks           []SyncLink   `json:"sync_links"`
}

// CanManage 只有组织所有者与管理员可以绑定 / 断开组织连接。
func CanManage(role policy.OrganizationRole) bool {
	return role == policy.OrganizationRoleOwner || role == policy.OrganizationRoleAdmin
}

func (s Service) providerView(p Provider, summary ProviderSummary) ProviderView {
	configured, bindable := true, p.Bindable
	if p.ID == "github" {
		configured = s.GitHubConfigured
		bindable = configured
	}
	redirect, defaultInstance := "", ""
	if IsGitProvider(p.ID) {
		redirect = s.RedirectURIFor(p.ID)
		defaultInstance = DefaultGitInstanceURL(p.ID)
		if p.ID != "github" {
			configured = s.GitProviderConfigured(p.ID)
			bindable = configured
		}
	}
	if p.ID == "tapd" || p.ID == "pingcode" || p.ID == "cnb" || p.ID == "shortcut" {
		configured = s.Box != nil
		bindable = configured
	}
	if p.ID == ProviderWeChat {
		configured = s.WeChatConfigured()
		bindable = configured
	}
	return ProviderView{ID: p.ID, Name: p.Name, Category: p.Category, Availability: p.Availability, Template: p.Template,
		Capabilities: p.Capabilities, Configured: configured, Bindable: bindable, Summary: summary,
		OAuthRedirectURI: redirect, DefaultInstanceURL: defaultInstance}
}

func summaryStatus(attention, active bool) string {
	switch {
	case attention:
		return "attention"
	case active:
		return "ok"
	default:
		return "none"
	}
}

// Overview 返回目录与每个服务商未断开的组织连接数 / 状态汇总。所有成员可读。
func (s Service) Overview(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (Overview, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Overview{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT provider, count(*), bool_or(status = 'NEEDS_ATTENTION'), bool_or(status = 'ACTIVE')
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED'
		   AND account_kind NOT IN ('task_assistant', 'agent')
		 GROUP BY provider`)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	summaries := map[string]ProviderSummary{}
	for rows.Next() {
		var provider string
		var count int
		var attention, active bool
		if err := rows.Scan(&provider, &count, &attention, &active); err != nil {
			return Overview{}, err
		}
		summaries[provider] = ProviderSummary{Connections: count, Status: summaryStatus(attention, active)}
	}
	if err := rows.Err(); err != nil {
		return Overview{}, err
	}
	result := Overview{Providers: make([]ProviderView, 0, len(catalog)), CanManage: CanManage(actor.Role)}
	for _, p := range Catalog() {
		summary, ok := summaries[p.ID]
		if !ok {
			summary = ProviderSummary{Status: "none"}
		}
		result.Providers = append(result.Providers, s.providerView(p, summary))
	}
	return result, nil
}

const connectionSelect = `
	SELECT c.id::text, c.provider, c.scope, c.parent_connection_id::text, c.owner_user_id::text, c.external_id,
	       c.display_name, c.account_kind, c.status, c.status_reason, c.credentials_hint,
	       c.created_by::text, COALESCE(u.email, ''), c.created_at, c.updated_at, c.disconnected_at, c.version,
	       COALESCE(c.settings->>'avatar_url', ''), c.webhook_last_delivery_at,
	       CASE WHEN c.provider IN ('gitlab', 'gitlab_cn')
	            THEN COALESCE(c.settings->>'instance_url', pc.settings->>'instance_url', '') ELSE '' END
	  FROM openfox.integration_connections c
	  LEFT JOIN openfox.users u ON u.id = c.created_by
	  LEFT JOIN openfox.integration_connections pc ON pc.organization_id = c.organization_id AND pc.id = c.parent_connection_id`

func scanConnection(row pgx.Row) (Connection, error) {
	var item Connection
	var version int64
	err := row.Scan(&item.ID, &item.Provider, &item.Scope, &item.ParentConnectionID, &item.OwnerUserID, &item.ExternalID,
		&item.DisplayName, &item.AccountKind, &item.Status, &item.StatusReason, &item.CredentialsHint,
		&item.CreatedBy, &item.CreatedByEmail, &item.CreatedAt, &item.UpdatedAt, &item.DisconnectedAt, &version,
		&item.AvatarURL, &item.WebhookLastDeliveryAt, &item.InstanceURL)
	item.Version = uint64(version)
	return item, err
}

func queryConnections(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]Connection, error) {
	rows, err := tx.Query(ctx, connectionSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Connection, 0)
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Detail 返回某服务商的组织连接（含已断开）、当前成员的个人连接（不含已断开）与当前成员可读空间的同步关联。
func (s Service) Detail(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, providerID string) (Detail, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Detail{}, err
	}
	provider, ok := Lookup(providerID)
	if !ok {
		return Detail{}, ErrNotFound
	}
	orgConnections, err := queryConnections(ctx, tx, `
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.provider = $1 AND c.scope = 'ORGANIZATION'
		   AND c.account_kind NOT IN ('task_assistant', 'agent')
		 ORDER BY c.created_at, c.id LIMIT $2`, providerID, maxDetailRows)
	if err != nil {
		return Detail{}, err
	}
	personal, err := queryConnections(ctx, tx, `
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.provider = $1 AND c.scope = 'PERSONAL'
		   AND c.owner_user_id = $2::uuid AND c.status <> 'DISCONNECTED'
		 ORDER BY c.created_at, c.id LIMIT $3`, providerID, actor.UserID, maxDetailRows)
	if err != nil {
		return Detail{}, err
	}
	links, err := s.syncLinks(ctx, tx, actor, providerID)
	if err != nil {
		return Detail{}, err
	}
	summary := ProviderSummary{Status: "none"}
	var attention, active bool
	for _, item := range orgConnections {
		if item.Status == StatusDisconnected {
			continue
		}
		summary.Connections++
		attention = attention || item.Status == StatusNeedsAttention
		active = active || item.Status == StatusActive
	}
	summary.Status = summaryStatus(attention, active)
	return Detail{Provider: s.providerView(provider, summary), CanManage: CanManage(actor.Role),
		Connections: orgConnections, PersonalConnections: personal, SyncLinks: links}, nil
}

func (s Service) syncLinks(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, providerID string) ([]SyncLink, error) {
	// SpaceReadableSQL 使用 $1（组织角色）与别名 s，与空间列表的可见性一致。
	rows, err := tx.Query(ctx, `SELECT`+syncLinkColumns+syncLinkFrom+`
		 WHERE l.organization_id = openfox_private.current_organization_id() AND l.deleted_at IS NULL
		   AND c.provider = $2 AND `+collaboration.SpaceReadableSQL+`
		 ORDER BY l.created_at, l.id LIMIT $3`, strings.ToUpper(string(actor.Role)), providerID, maxDetailRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SyncLink, 0)
	for rows.Next() {
		item, err := scanSyncLink(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DisconnectResult 带回是否发生状态变化或清除了残留凭据，调用方据此决定是否写审计。
type DisconnectResult struct {
	Connection Connection
	Changed    bool
}

// ConfirmPhrase 返回断开组织连接需要输入的确认短语。
func ConfirmPhrase(providerName string) string { return "断开 " + providerName }

// Disconnect 断开连接：锁行 + 版本 CAS。组织连接需要所有者 / 管理员与确认短语，个人连接需要本人。
// 断开组织连接时同一事务内联动其下个人连接（DISCONNECTED）与同步关联（PAUSED）。站点导入与评论凭据同时清除。
func (s Service) Disconnect(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor,
	connectionID string, version uint64, confirmPhrase string) (DisconnectResult, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return DisconnectResult{}, err
	}
	if !membership.ValidateUUID(connectionID) || version == 0 {
		return DisconnectResult{}, ErrInvalidRequest
	}
	var providerID, scope, status string
	var owner *string
	var currentVersion int64
	err := tx.QueryRow(ctx, `
		SELECT provider, scope, owner_user_id::text, status, version
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, connectionID).Scan(&providerID, &scope, &owner, &status, &currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return DisconnectResult{}, ErrNotFound
	}
	if err != nil {
		return DisconnectResult{}, err
	}
	switch scope {
	case ScopeOrganization:
		if !CanManage(actor.Role) {
			return DisconnectResult{}, ErrForbidden
		}
	default:
		// 他人的个人连接对当前成员不可见，按不存在处理。
		if owner == nil || *owner != actor.UserID {
			return DisconnectResult{}, ErrNotFound
		}
	}
	if status == StatusDisconnected {
		tag, err := tx.Exec(ctx, `UPDATE openfox.integration_connections
		 SET credentials_sealed=NULL,credentials_key_id='',updated_at=clock_timestamp(),version=version+1
		 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
		 AND settings->>'auth_method' IN ('import_site','site_comments','pingcode_personal')
		 AND (credentials_sealed IS NOT NULL OR credentials_key_id<>'')`, connectionID)
		if err != nil {
			return DisconnectResult{}, err
		}
		item, err := loadConnection(ctx, tx, connectionID)
		return DisconnectResult{Connection: item, Changed: tag.RowsAffected() == 1}, err
	}
	if uint64(currentVersion) != version {
		return DisconnectResult{}, ErrVersionConflict
	}
	if scope == ScopeOrganization {
		provider, ok := Lookup(providerID)
		if !ok || strings.TrimSpace(confirmPhrase) != ConfirmPhrase(provider.Name) {
			return DisconnectResult{}, ErrConfirmationMismatch
		}
	}
	if scope == ScopeOrganization {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET status = 'DISCONNECTED', status_reason = 'parent connection disconnected',
			       credentials_sealed=CASE WHEN settings->>'auth_method' IN ('site_comments','pingcode_personal') THEN NULL ELSE credentials_sealed END,
			       credentials_key_id=CASE WHEN settings->>'auth_method' IN ('site_comments','pingcode_personal') THEN '' ELSE credentials_key_id END,
			       disconnected_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND parent_connection_id = $1::uuid AND (status <> 'DISCONNECTED'
			     OR (settings->>'auth_method' IN ('site_comments','pingcode_personal') AND (credentials_sealed IS NOT NULL OR credentials_key_id<>'')))`, connectionID); err != nil {
			return DisconnectResult{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET state='DISCONNECTED',version=version+1,lease=NULL,error_code=''
         WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state<>'DISCONNECTED'`, connectionID); err != nil {
			return DisconnectResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_sync_links
			   SET status = 'PAUSED', status_reason = 'connection disconnected', updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND deleted_at IS NULL
			   AND status IN ('ACTIVE', 'NEEDS_ATTENTION')
			   AND (connection_id = $1::uuid OR connection_id IN (
			        SELECT id FROM openfox.integration_connections
			         WHERE organization_id = openfox_private.current_organization_id() AND parent_connection_id = $1::uuid))`,
			connectionID); err != nil {
			return DisconnectResult{}, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
			SET status = 'DISCONNECTED', status_reason = 'disconnected by user',
			       credentials_sealed=CASE WHEN settings->>'auth_method' IN ('import_site','site_comments','pingcode_personal') THEN NULL ELSE credentials_sealed END,
			       credentials_key_id=CASE WHEN settings->>'auth_method' IN ('import_site','site_comments','pingcode_personal') THEN '' ELSE credentials_key_id END,
		       disconnected_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
		connectionID, int64(version))
	if err != nil {
		return DisconnectResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return DisconnectResult{}, ErrVersionConflict
	}
	if IsIMProvider(providerID) {
		if err := disconnectFeishu(ctx, tx, scope, connectionID); err != nil {
			return DisconnectResult{}, err
		}
	}
	item, err := loadConnection(ctx, tx, connectionID)
	return DisconnectResult{Connection: item, Changed: true}, err
}

func loadConnection(ctx context.Context, tx pgx.Tx, id string) (Connection, error) {
	return scanConnection(tx.QueryRow(ctx, connectionSelect+`
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`, id))
}
