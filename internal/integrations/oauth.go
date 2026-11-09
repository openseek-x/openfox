package integrations

// GitHub OAuth 授权流程（发起、回调、会话读取、勾选组织完成）。
// 令牌换取与 GitHub API 调用都在事务外完成，服务只在组织事务里读写会话与连接。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"openfox/internal/gitlabproxy"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const (
	PurposeOrganization = "organization"
	PurposePersonal     = "personal"
	PurposeReauthorize  = "reauthorize"

	// 授权失败原因，与前端完成页的文案一一对应。
	ReasonAccessDenied    = "access_denied"
	ReasonExchangeFailed  = "exchange_failed"
	ReasonAccountMismatch = "account_mismatch"
	ReasonInvalidState    = "invalid_state"
	ReasonAlreadyBound    = "already_connected"

	statusPending   = "PENDING"
	statusSelecting = "SELECTING"
	statusCompleted = "COMPLETED"
	statusFailed    = "FAILED"
	statusExpired   = "EXPIRED"

	maxCompleteAccounts = 50
)

// RedirectURI 是当前服务商 OAuth 应用的 Authorization callback URL（GitHub 为 /integrations/github/callback）。
func (s Service) RedirectURI() string { return s.RedirectURIFor(s.git()) }

type StartInput struct {
	Purpose      string
	ConnectionID string
	// 以下三项只在 GitLab 系组织绑定（purpose = organization）时使用：实例地址与管理员在实例上创建的 OAuth 应用凭据。
	InstanceURL       string
	ApplicationID     string
	ApplicationSecret string
}

// startSecret 是授权会话暂存的 PKCE verifier 与 GitLab 实例信息（整体加密落库）。
// GitHub 会话仍直接存 verifier 原文，保持 6b 行为不变。
type startSecret struct {
	Verifier          string `json:"v"`
	InstanceURL       string `json:"u,omitempty"`
	ApplicationID     string `json:"i,omitempty"`
	ApplicationSecret string `json:"s,omitempty"`
}

func (s Service) openStartSecret(provider, organizationID, sessionID string, sealed []byte) (startSecret, error) {
	plain, err := s.open(sealed, oauthAAD(organizationID, sessionID))
	if err != nil {
		return startSecret{}, err
	}
	if provider == ProviderGitHub {
		return startSecret{Verifier: string(plain)}, nil
	}
	var out startSecret
	if err := json.Unmarshal(plain, &out); err != nil || out.Verifier == "" {
		return startSecret{}, ErrOAuthSessionState
	}
	return out, nil
}

func (ss startSecret) instance() GitInstance {
	return GitInstance{BaseURL: ss.InstanceURL, ClientID: ss.ApplicationID, ClientSecret: ss.ApplicationSecret}
}

// connectionInstance 读取已有组织连接的实例地址与应用凭据（重新授权、个人绑定沿用组织连接的 OAuth 应用）。
func (s Service) connectionInstance(ctx context.Context, tx pgx.Tx, organizationID, connectionID, provider string) (GitInstance, bool, error) {
	var settingsRaw, sealed []byte
	err := tx.QueryRow(ctx, `
		SELECT settings, credentials_sealed FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND provider = $2 AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED'`, connectionID, provider).Scan(&settingsRaw, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitInstance{}, false, nil
	}
	if err != nil {
		return GitInstance{}, false, err
	}
	return s.instanceFrom(organizationID, connectionID, settingsRaw, sealed)
}

func (s Service) instanceFrom(organizationID, connectionID string, settingsRaw, sealed []byte) (GitInstance, bool, error) {
	var settings struct {
		ProxyDevice   string `json:"proxy_device_id"`
		ProxyOwner    string `json:"proxy_owner_id"`
		AuthMethod    string `json:"auth_method"`
		InstanceURL   string `json:"instance_url"`
		ApplicationID string `json:"application_id"`
	}
	_ = json.Unmarshal(settingsRaw, &settings)
	creds, err := s.openCredentials(organizationID, connectionID, sealed)
	if err != nil {
		return GitInstance{}, true, err
	}
	return GitInstance{Proxy: gitlabproxy.Binding{Organization: organizationID, Device: settings.ProxyDevice, Owner: settings.ProxyOwner}, PersonalToken: settings.AuthMethod == "personal_token", BaseURL: settings.InstanceURL, ClientID: settings.ApplicationID, ClientSecret: creds.GitAppSecret}, true, nil
}

type StartResult struct {
	AuthorizeURL string    `json:"authorize_url"`
	SessionID    string    `json:"session_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// OAuthAccount 是授权后可以绑定的 GitHub 组织或授权者本人。
type OAuthAccount struct {
	Login        string  `json:"login"`
	Kind         string  `json:"kind"`
	AvatarURL    string  `json:"avatar_url"`
	ConnectionID *string `json:"connection_id"`
}

// OAuthSession 对应契约 IntegrationOAuthSession，不含任何令牌。
type OAuthSession struct {
	ID           string         `json:"id"`
	Provider     string         `json:"provider"`
	Purpose      string         `json:"purpose"`
	Status       string         `json:"status"`
	ErrorCode    string         `json:"error_code"`
	Accounts     []OAuthAccount `json:"accounts"`
	ConnectionID *string        `json:"connection_id"`
	ExpiresAt    time.Time      `json:"expires_at"`
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// StartAuthorization 创建授权会话并返回授权页地址。
// organization / reauthorize 只有所有者 / 管理员；personal 允许所有非访客成员。
func (s Service) StartAuthorization(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, in StartInput) (StartResult, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return StartResult{}, err
	}
	switch in.Purpose {
	case PurposeOrganization, PurposeReauthorize:
		if !CanManage(actor.Role) {
			return StartResult{}, ErrForbidden
		}
	case PurposePersonal:
		if actor.Role == policy.OrganizationRoleGuest {
			return StartResult{}, ErrForbidden
		}
	default:
		return StartResult{}, ErrInvalidRequest
	}
	if in.Purpose == PurposeReauthorize && !membership.ValidateUUID(in.ConnectionID) {
		return StartResult{}, ErrInvalidRequest
	}
	provider := s.git()
	family := IsGitLabFamily(provider)
	if family && in.Purpose == PurposePersonal && !membership.ValidateUUID(in.ConnectionID) {
		// GitLab 的个人绑定沿用某个组织连接的实例与 OAuth 应用，必须指明是哪一个。
		return StartResult{}, ErrInvalidRequest
	}
	if !s.GitProviderConfigured(provider) {
		return StartResult{}, ErrNotConfigured
	}
	if s.Box == nil {
		return StartResult{}, ErrSecretsUnavailable
	}
	connectionID := ""
	if in.Purpose == PurposeReauthorize {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.integration_connections
			                WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
			                  AND provider = $2 AND scope = 'ORGANIZATION')`, in.ConnectionID, provider).Scan(&exists); err != nil {
			return StartResult{}, err
		}
		if !exists {
			return StartResult{}, ErrNotFound
		}
		connectionID = in.ConnectionID
	}
	secret := startSecret{}
	var inst GitInstance
	if family {
		switch in.Purpose {
		case PurposeOrganization:
			base := strings.TrimSpace(in.InstanceURL)
			if base == "" {
				base = DefaultGitInstanceURL(provider)
			}
			id, appSecret := strings.TrimSpace(in.ApplicationID), strings.TrimSpace(in.ApplicationSecret)
			if base == "" || id == "" || appSecret == "" || len(id) > 255 || len(appSecret) > 1024 || strings.ContainsAny(id+appSecret, " \t\r\n") {
				return StartResult{}, ErrInvalidRequest
			}
			if s.ValidateInstance == nil {
				return StartResult{}, ErrInvalidInstance
			}
			normalized, err := s.ValidateInstance(ctx, base)
			if err != nil {
				return StartResult{}, ErrInvalidInstance
			}
			inst = GitInstance{BaseURL: normalized, ClientID: id, ClientSecret: appSecret}
		default:
			// 重新授权 / 个人绑定：沿用组织连接保存的实例与应用凭据。
			parent := in.ConnectionID
			found := false
			var err error
			if inst, found, err = s.connectionInstance(ctx, tx, organizationID, parent, provider); err != nil {
				return StartResult{}, err
			}
			if !found {
				return StartResult{}, ErrNotFound
			}
			if in.Purpose == PurposePersonal {
				connectionID = parent
			}
		}
		secret = startSecret{InstanceURL: inst.BaseURL, ApplicationID: inst.ClientID, ApplicationSecret: inst.ClientSecret}
	}
	client, err := s.gitClient(provider, inst)
	if err != nil {
		return StartResult{}, err
	}
	// 顺手清理本组织已过期十分钟以上的未完成会话，并清空暂存的密文。
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
	plain := []byte(verifier)
	if provider != ProviderGitHub {
		secret.Verifier = verifier
		plain, _ = json.Marshal(secret)
	}
	sealedVerifier, _, err := s.seal(plain, oauthAAD(organizationID, sessionID))
	if err != nil {
		return StartResult{}, err
	}
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO openfox.integration_oauth_states (organization_id, id, provider, purpose, user_id, state_hash, code_verifier_sealed, connection_id)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $6, $2, $3::uuid, $4, $5, NULLIF($7, '')::uuid)
		RETURNING expires_at`, sessionID, strings.ToUpper(in.Purpose), actor.UserID, TokenHash(state), sealedVerifier, provider, connectionID).Scan(&expiresAt); err != nil {
		return StartResult{}, err
	}
	return StartResult{AuthorizeURL: client.AuthorizeURL(state, pkceChallenge(verifier), s.RedirectURI()),
		SessionID: sessionID, ExpiresAt: expiresAt}, nil
}

type sessionRow struct {
	Provider       string
	Purpose        string
	UserID         string
	Status         string
	ConnectionID   *string
	Expired        bool
	Consumed       bool
	VerifierSealed []byte
	PendingSealed  []byte
	Accounts       []OAuthAccount
}

// lockSession 锁住当前服务商的会话行；不存在返回 ErrNotFound。
func (s Service) lockSession(ctx context.Context, tx pgx.Tx, sessionID string) (sessionRow, error) {
	return lockSessionFor(ctx, tx, sessionID, s.git())
}

// lockSessionFor 锁住指定服务商的会话；provider 为空表示不限服务商（读取会话状态用）。
func lockSessionFor(ctx context.Context, tx pgx.Tx, sessionID, provider string) (sessionRow, error) {
	var row sessionRow
	var accounts []byte
	err := tx.QueryRow(ctx, `
		SELECT provider, purpose, user_id::text, status, connection_id::text, expires_at <= clock_timestamp(), consumed_at IS NOT NULL,
		       code_verifier_sealed, pending_sealed, pending_accounts
		  FROM openfox.integration_oauth_states
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND ($2 = '' OR provider = $2)
		 FOR UPDATE`, sessionID, provider).Scan(&row.Provider, &row.Purpose, &row.UserID, &row.Status, &row.ConnectionID, &row.Expired, &row.Consumed,
		&row.VerifierSealed, &row.PendingSealed, &accounts)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if err := json.Unmarshal(accounts, &row.Accounts); err != nil {
		return row, err
	}
	return row, nil
}

// CallbackTarget 是回调在事务外换取令牌需要的信息：PKCE verifier 与（GitLab 系）实例地址、OAuth 应用凭据。
type CallbackTarget struct {
	Verifier string
	Instance GitInstance
}

// CallbackVerifier 返回会话的 PKCE verifier 与实例信息（回调在事务外换取令牌时使用）。
// 会话不属于该用户、不是等待回调的状态或已过期时返回 ErrOAuthSessionState。
func (s Service) CallbackVerifier(ctx context.Context, tx pgx.Tx, organizationID, userID, sessionID string) (CallbackTarget, error) {
	row, err := s.lockSession(ctx, tx, sessionID)
	if errors.Is(err, ErrNotFound) {
		return CallbackTarget{}, ErrOAuthSessionState
	}
	if err != nil {
		return CallbackTarget{}, err
	}
	if row.UserID != userID || row.Status != statusPending || row.Consumed || row.Expired {
		return CallbackTarget{}, ErrOAuthSessionState
	}
	secret, err := s.openStartSecret(s.git(), organizationID, sessionID, row.VerifierSealed)
	if err != nil {
		return CallbackTarget{}, err
	}
	return CallbackTarget{Verifier: secret.Verifier, Instance: secret.instance()}, nil
}

// Client 返回回调换取令牌用的客户端。
func (s Service) CallbackClient(target CallbackTarget) (gitapi.Client, error) {
	return s.gitClient(s.git(), target.Instance)
}

// CallbackOutcome 是回调在事务外取得的结果：Failure 非空表示授权被拒绝或令牌换取失败。
type CallbackOutcome struct {
	Failure   string
	Token     string
	Scope     string
	Refresh   string
	ExpiresAt time.Time
	User      gitapi.User
	Orgs      []gitapi.Org
}

// CallbackResult 带回要跳转的结果与需要写入的审计。Reason 为空表示成功。
type CallbackResult struct {
	SessionID string
	Reason    string
	Audit     []AuditEntry
}

type pendingSecret struct {
	AccessToken       string    `json:"access_token"`
	Scope             string    `json:"scope"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	ExpiresAt         time.Time `json:"expires_at,omitempty"`
	InstanceURL       string    `json:"instance_url,omitempty"`
	ApplicationID     string    `json:"application_id,omitempty"`
	ApplicationSecret string    `json:"application_secret,omitempty"`
}

// nullableTime 把零值转成 NULL（token_expires_at 为空表示令牌不会过期）。
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// FinishCallback 在组织事务里消费会话（只能用一次）并按用途处理。
func (s Service) FinishCallback(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, sessionID string, out CallbackOutcome) (CallbackResult, error) {
	row, err := s.lockSession(ctx, tx, sessionID)
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
	start, err := s.openStartSecret(s.git(), organizationID, sessionID, row.VerifierSealed)
	if err != nil {
		return CallbackResult{}, err
	}
	source := s.git()
	switch strings.ToLower(row.Purpose) {
	case PurposeOrganization:
		accounts, err := s.pendingAccounts(ctx, tx, out)
		if err != nil {
			return CallbackResult{}, err
		}
		plain, _ := json.Marshal(pendingSecret{AccessToken: out.Token, Scope: out.Scope, RefreshToken: out.Refresh, ExpiresAt: out.ExpiresAt,
			InstanceURL: start.InstanceURL, ApplicationID: start.ApplicationID, ApplicationSecret: start.ApplicationSecret})
		sealed, _, err := s.seal(plain, oauthAAD(organizationID, sessionID))
		if err != nil {
			return CallbackResult{}, err
		}
		accountsJSON, _ := json.Marshal(accounts)
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_oauth_states
			   SET status = 'SELECTING', consumed_at = clock_timestamp(), code_verifier_sealed = NULL,
			       pending_sealed = $2, pending_accounts = $3::jsonb
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			sessionID, sealed, accountsJSON); err != nil {
			return CallbackResult{}, err
		}
	case PurposePersonal:
		parentID := ""
		if row.ConnectionID != nil {
			parentID = *row.ConnectionID
		}
		connectionID, created, err := s.upsertPersonal(ctx, tx, organizationID, actor, out, parentID)
		if err != nil {
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
			Metadata: map[string]string{"source": source, "result": ScopePersonal}})
	case PurposeReauthorize:
		if row.ConnectionID == nil {
			return fail(ReasonInvalidState)
		}
		reason, err := s.reauthorize(ctx, tx, organizationID, *row.ConnectionID, out)
		if err != nil {
			if isUniqueViolation(err) {
				return fail(ReasonAlreadyBound)
			}
			return CallbackResult{}, err
		}
		if reason != "" {
			return fail(reason)
		}
		if err := completeSession(ctx, tx, sessionID, *row.ConnectionID); err != nil {
			return CallbackResult{}, err
		}
		result.Audit = append(result.Audit, AuditEntry{Action: "integration.connection_reauthorized", ResourceID: *row.ConnectionID,
			Metadata: map[string]string{"source": source, "result": ScopeOrganization}})
	default:
		return fail(ReasonInvalidState)
	}
	return result, nil
}

func completeSession(ctx context.Context, tx pgx.Tx, sessionID, connectionID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.integration_oauth_states
		   SET status = 'COMPLETED', consumed_at = clock_timestamp(), completed_at = clock_timestamp(),
		       code_verifier_sealed = NULL, pending_sealed = NULL, connection_id = $2::uuid
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, sessionID, connectionID)
	return err
}

// pendingAccounts 组出可绑定的账号：授权者所属的各个组织，加上授权者本人；已有未断开连接的带上连接 ID。
func (s Service) pendingAccounts(ctx context.Context, tx pgx.Tx, out CallbackOutcome) ([]OAuthAccount, error) {
	accounts := make([]OAuthAccount, 0, len(out.Orgs)+1)
	for _, org := range out.Orgs {
		accounts = append(accounts, OAuthAccount{Login: org.Login, Kind: "organization", AvatarURL: org.AvatarURL})
	}
	accounts = append(accounts, OAuthAccount{Login: out.User.Login, Kind: "user", AvatarURL: out.User.AvatarURL})
	for i := range accounts {
		var id *string
		err := tx.QueryRow(ctx, s.gq(`
			SELECT id::text FROM openfox.integration_connections
			 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'ORGANIZATION'
			   AND lower(external_id) = lower($1) AND status <> 'DISCONNECTED'`), accounts[i].Login).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		accounts[i].ConnectionID = id
	}
	return accounts, nil
}

// upsertPersonal 建立或更新当前成员的个人连接。GitHub / Gitee：同一成员只有一个未断开的个人连接；
// GitLab 系：每个组织连接（实例）一个，parentID 是所属组织连接。
func (s Service) upsertPersonal(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, out CallbackOutcome, parentID string) (string, bool, error) {
	settings, _ := json.Marshal(map[string]string{"login": out.User.Login, "avatar_url": out.User.AvatarURL})
	externalID := formatID(out.User.ID)
	var existing string
	err := tx.QueryRow(ctx, s.gq(`
		SELECT id::text FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'PERSONAL'
		   AND owner_user_id = $1::uuid AND parent_connection_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid AND status <> 'DISCONNECTED'
		 FOR UPDATE`), actor.UserID, parentID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	connectionID := existing
	created := existing == ""
	if created {
		if connectionID, err = membership.NewID(); err != nil {
			return "", false, err
		}
	}
	blob, keyID, err := s.sealCredentials(organizationID, connectionID, connectionCredentials{AccessToken: out.Token, Scope: out.Scope, RefreshToken: out.Refresh})
	if err != nil {
		return "", false, err
	}
	if created {
		_, err = tx.Exec(ctx, s.gq(`
			INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, owner_user_id, parent_connection_id, external_id, display_name,
			       account_kind, status, credentials_sealed, credentials_key_id, credentials_hint, settings, created_by, token_expires_at)
			VALUES (openfox_private.current_organization_id(), $1::uuid, 'github', 'PERSONAL', $2::uuid, NULLIF($9, '')::uuid, $3, $4,
			        'user', 'ACTIVE', $5, $6, $7, $8::jsonb, $2::uuid, $10)`),
			connectionID, actor.UserID, externalID, out.User.Login, blob, keyID, tokenHint(out.Token), settings, parentID, nullableTime(out.ExpiresAt))
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET external_id = $2, display_name = $3, status = 'ACTIVE', status_reason = '', credentials_sealed = $4,
			       credentials_key_id = $5, credentials_hint = $6, settings = $7::jsonb, token_expires_at = $8,
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			connectionID, externalID, out.User.Login, blob, keyID, tokenHint(out.Token), settings, nullableTime(out.ExpiresAt))
	}
	if err != nil {
		return "", false, err
	}
	return connectionID, created, nil
}

// reauthorize 更新组织连接的访问令牌并恢复 ACTIVE（保留 Webhook 密钥与地址）。
// 授权的 GitHub 账号既不是该连接绑定的账号、也不属于该组织时返回 account_mismatch。
func (s Service) reauthorize(ctx context.Context, tx pgx.Tx, organizationID, connectionID string, out CallbackOutcome) (string, error) {
	var externalID string
	var sealed []byte
	err := tx.QueryRow(ctx, s.gq(`
		SELECT external_id, credentials_sealed FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND provider = 'github' AND scope = 'ORGANIZATION'
		 FOR UPDATE`), connectionID).Scan(&externalID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReasonInvalidState, nil
	}
	if err != nil {
		return "", err
	}
	if !canAccessAccount(externalID, out) {
		return ReasonAccountMismatch, nil
	}
	creds, openErr := s.openCredentials(organizationID, connectionID, sealed)
	regenerate := openErr != nil || creds.WebhookToken == "" || creds.WebhookSecret == ""
	if regenerate {
		// 旧密文无法解密（例如主密钥已更换）：重新生成 Webhook 令牌与密钥，管理员需要在 GitHub 里同步更新。
		if creds.WebhookToken, err = randomBase64URL(32); err != nil {
			return "", err
		}
		if creds.WebhookSecret, err = randomHex(32); err != nil {
			return "", err
		}
	}
	creds.AccessToken, creds.Scope, creds.RefreshToken = out.Token, out.Scope, out.Refresh
	blob, keyID, err := s.sealCredentials(organizationID, connectionID, creds)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET status = 'ACTIVE', status_reason = '', disconnected_at = NULL, credentials_sealed = $2, credentials_key_id = $3,
		       credentials_hint = $4, webhook_token_hash = CASE WHEN $5::boolean THEN $6 ELSE webhook_token_hash END,
		       token_expires_at = $7, updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		connectionID, blob, keyID, tokenHint(out.Token), regenerate, TokenHash(creds.WebhookToken), nullableTime(out.ExpiresAt))
	return "", err
}

func canAccessAccount(externalID string, out CallbackOutcome) bool {
	if strings.EqualFold(out.User.Login, externalID) {
		return true
	}
	for _, org := range out.Orgs {
		if strings.EqualFold(org.Login, externalID) {
			return true
		}
	}
	return false
}

// GetSession 只有发起授权的本人可读（他人按不存在处理）；过期的会话在读取时改为 EXPIRED。
func (s Service) GetSession(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, sessionID string) (OAuthSession, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return OAuthSession{}, err
	}
	if !membership.ValidateUUID(sessionID) {
		return OAuthSession{}, ErrNotFound
	}
	row, err := lockSessionFor(ctx, tx, sessionID, "")
	if err != nil {
		return OAuthSession{}, err
	}
	if row.UserID != actor.UserID {
		return OAuthSession{}, ErrNotFound
	}
	var view OAuthSession
	view.ID, view.Provider, view.Purpose, view.Status = sessionID, row.Provider, strings.ToLower(row.Purpose), row.Status
	view.ConnectionID = row.ConnectionID
	if err := tx.QueryRow(ctx, `
		SELECT error_code, expires_at FROM openfox.integration_oauth_states
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, sessionID).Scan(&view.ErrorCode, &view.ExpiresAt); err != nil {
		return OAuthSession{}, err
	}
	if row.Expired && (row.Status == statusPending || row.Status == statusSelecting) {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_oauth_states SET status = 'EXPIRED', code_verifier_sealed = NULL, pending_sealed = NULL
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, sessionID); err != nil {
			return OAuthSession{}, err
		}
		view.Status = statusExpired
	}
	view.Accounts = []OAuthAccount{}
	if view.Status == statusSelecting {
		view.Accounts = row.Accounts
	}
	return view, nil
}

// Complete 勾选要绑定的账号并建立组织连接。只有发起授权的所有者 / 管理员。
func (s Service) Complete(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, sessionID string, logins []string) ([]Connection, []AuditEntry, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, nil, err
	}
	if !CanManage(actor.Role) {
		return nil, nil, ErrForbidden
	}
	if !membership.ValidateUUID(sessionID) {
		return nil, nil, ErrNotFound
	}
	if len(logins) == 0 || len(logins) > maxCompleteAccounts {
		return nil, nil, ErrInvalidRequest
	}
	row, err := lockSessionFor(ctx, tx, sessionID, "")
	if err != nil {
		return nil, nil, err
	}
	if row.UserID != actor.UserID {
		return nil, nil, ErrNotFound
	}
	// 路由里没有服务商：以会话自己的服务商为准（GitHub / GitLab / 极狐）。
	if row.Provider != s.git() {
		if !IsGitProvider(row.Provider) {
			return nil, nil, ErrNotFound
		}
		return s.ForGit(row.Provider).Complete(ctx, tx, organizationID, actor, sessionID, logins)
	}
	if strings.ToLower(row.Purpose) != PurposeOrganization || row.Status != statusSelecting || row.Expired {
		return nil, nil, ErrOAuthSessionState
	}
	byLogin := map[string]OAuthAccount{}
	for _, account := range row.Accounts {
		byLogin[strings.ToLower(account.Login)] = account
	}
	picked := make([]OAuthAccount, 0, len(logins))
	seen := map[string]bool{}
	for _, login := range logins {
		key := strings.ToLower(strings.TrimSpace(login))
		account, ok := byLogin[key]
		if !ok {
			return nil, nil, ErrInvalidRequest
		}
		if !seen[key] {
			seen[key] = true
			picked = append(picked, account)
		}
	}
	plain, err := s.open(row.PendingSealed, oauthAAD(organizationID, sessionID))
	if err != nil {
		return nil, nil, err
	}
	var pending pendingSecret
	if err := json.Unmarshal(plain, &pending); err != nil || pending.AccessToken == "" {
		return nil, nil, ErrOAuthSessionState
	}
	connections := make([]Connection, 0, len(picked))
	audits := make([]AuditEntry, 0, len(picked))
	for _, account := range picked {
		connectionID, created, err := s.bindOrganization(ctx, tx, organizationID, actor, account, pending)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, nil, ErrVersionConflict
			}
			return nil, nil, err
		}
		item, err := loadConnection(ctx, tx, connectionID)
		if err != nil {
			return nil, nil, err
		}
		connections = append(connections, item)
		action := "integration.connection_reauthorized"
		if created {
			action = "integration.connection_created"
		}
		audits = append(audits, AuditEntry{Action: action, ResourceID: connectionID,
			Metadata: map[string]string{"source": s.git(), "result": account.Kind}})
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_oauth_states
		   SET status = 'COMPLETED', completed_at = clock_timestamp(), pending_sealed = NULL, code_verifier_sealed = NULL
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, sessionID); err != nil {
		return nil, nil, err
	}
	return connections, audits, nil
}

// bindOrganization 新建组织连接，或在已有未断开的同一账号连接时只更新令牌。
func (s Service) bindOrganization(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, account OAuthAccount, pending pendingSecret) (string, bool, error) {
	var existing string
	var sealed []byte
	err := tx.QueryRow(ctx, s.gq(`
		SELECT id::text, credentials_sealed FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'github' AND scope = 'ORGANIZATION'
		   AND lower(external_id) = lower($1) AND status <> 'DISCONNECTED'
		 FOR UPDATE`), account.Login).Scan(&existing, &sealed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	if existing != "" {
		creds, openErr := s.openCredentials(organizationID, existing, sealed)
		regenerate := openErr != nil || creds.WebhookToken == "" || creds.WebhookSecret == ""
		if regenerate {
			if creds.WebhookToken, err = randomBase64URL(32); err != nil {
				return "", false, err
			}
			if creds.WebhookSecret, err = randomHex(32); err != nil {
				return "", false, err
			}
		}
		creds.AccessToken, creds.Scope, creds.RefreshToken = pending.AccessToken, pending.Scope, pending.RefreshToken
		if pending.ApplicationSecret != "" {
			creds.GitAppSecret = pending.ApplicationSecret
		}
		blob, keyID, err := s.sealCredentials(organizationID, existing, creds)
		if err != nil {
			return "", false, err
		}
		_, err = tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET status = 'ACTIVE', status_reason = '', credentials_sealed = $2, credentials_key_id = $3, credentials_hint = $4,
			       webhook_token_hash = CASE WHEN $5::boolean THEN $6 ELSE webhook_token_hash END,
			       settings = settings || $7::jsonb, token_expires_at = $8,
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			existing, blob, keyID, tokenHint(pending.AccessToken), regenerate, TokenHash(creds.WebhookToken),
			instanceSettings(pending), nullableTime(pending.ExpiresAt))
		return existing, false, err
	}
	connectionID, err := membership.NewID()
	if err != nil {
		return "", false, err
	}
	creds := connectionCredentials{AccessToken: pending.AccessToken, Scope: pending.Scope, RefreshToken: pending.RefreshToken,
		GitAppSecret: pending.ApplicationSecret}
	if creds.WebhookToken, err = randomBase64URL(32); err != nil {
		return "", false, err
	}
	if creds.WebhookSecret, err = randomHex(32); err != nil {
		return "", false, err
	}
	blob, keyID, err := s.sealCredentials(organizationID, connectionID, creds)
	if err != nil {
		return "", false, err
	}
	kind := "organization"
	if account.Kind == "user" {
		kind = "user"
	}
	settings, _ := json.Marshal(map[string]string{"avatar_url": account.AvatarURL})
	if inst := instanceSettings(pending); string(inst) != "{}" {
		merged := map[string]string{"avatar_url": account.AvatarURL}
		_ = json.Unmarshal(inst, &merged)
		merged["avatar_url"] = account.AvatarURL
		settings, _ = json.Marshal(merged)
	}
	_, err = tx.Exec(ctx, s.gq(`
		INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, external_id, display_name, account_kind, status,
		       credentials_sealed, credentials_key_id, credentials_hint, settings, created_by, webhook_token_hash, token_expires_at)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'github', 'ORGANIZATION', $2, $3, $4, 'ACTIVE',
		        $5, $6, $7, $8::jsonb, $9::uuid, $10, $11)`),
		connectionID, strings.ToLower(account.Login), account.Login, kind, blob, keyID, tokenHint(pending.AccessToken), settings,
		actor.UserID, TokenHash(creds.WebhookToken), nullableTime(pending.ExpiresAt))
	return connectionID, true, err
}

// instanceSettings 是写入连接 settings 的实例信息（只有 GitLab 系有；不含任何密钥）。
func instanceSettings(p pendingSecret) []byte {
	m := map[string]string{}
	if p.InstanceURL != "" {
		m["instance_url"] = p.InstanceURL
	}
	if p.ApplicationID != "" {
		m["application_id"] = p.ApplicationID
	}
	raw, _ := json.Marshal(m)
	return raw
}
