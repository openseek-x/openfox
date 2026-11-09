package integrations

// 扫码创建飞书应用（设备码流程）的会话管理。
// 对飞书的请求都在事务之外：先短事务校验权限与冲突，再调用飞书 init + begin，最后短事务保存会话；
// 轮询与落库由 IMWorker 完成（见 imworker.go）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/feishu"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	PurposeTaskAssistant = "TASK_ASSISTANT"
	PurposeAgent         = "AGENT"

	// 扫码链接最长有效 10 分钟（飞书给的 expire_in 更长时取较小者）。
	registrationMaxSeconds = 600
	maxExtraPrompt         = 4000

	RegErrSuperseded       = "superseded"
	RegErrSessionExpired   = "session_expired"
	RegErrExpired          = "expired"
	RegErrAccessDenied     = "access_denied"
	RegErrRejected         = "rejected"
	RegErrAgentConnected   = "agent_connected"
	RegErrAssistantExists  = "assistant_exists"
	RegErrSaveFailed       = "save_failed"
	registrationStatusPend = "PENDING"
)

// FeishuAssistantRuntime 对应契约 FeishuAssistantRuntime。
type FeishuAssistantRuntime struct {
	ComputerID string `json:"computer_id"`
	GrantID    string `json:"grant_id"`
	Runtime    string `json:"runtime"`
	Model      string `json:"model"`
}

// RegistrationInput 是 startFeishuRegistration 的请求体。
type RegistrationInput struct {
	Purpose     string                  `json:"purpose"`
	SpaceID     string                  `json:"space_id"`
	AgentID     string                  `json:"agent_id"`
	Runtime     *FeishuAssistantRuntime `json:"runtime"`
	ExtraPrompt string                  `json:"extra_prompt"`
}

// FeishuRegistration 对应契约 FeishuRegistration。
type FeishuRegistration struct {
	ID              string    `json:"id"`
	Purpose         string    `json:"purpose"`
	SpaceID         string    `json:"space_id,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	Status          string    `json:"status"`
	ErrorCode       string    `json:"error_code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	PollInterval    int       `json:"poll_interval"`
	BotID           *string   `json:"bot_id"`
}

// RegistrationPlan 是第一步（短事务）校验通过后的结果。
type RegistrationPlan struct {
	Purpose     string
	SpaceID     string
	AgentID     string
	Config      json.RawMessage
	ExtraPrompt string
}

func agentActor(actor collaboration.Actor) agents.Actor {
	return agents.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}
}

// requireFeishuMember 要求非访客的有效成员。
func requireFeishuMember(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	return nil
}

// requireFeishuAppActive 要求组织已绑定飞书应用且连接有效（成员要先在应用下绑定个人账号，机器人才能认出发消息的人）。
func requireFeishuAppActive(ctx context.Context, tx pgx.Tx, provider string) error {
	app, err := loadFeishuApp(ctx, tx, provider, false)
	if err != nil {
		return err
	}
	if app == nil || app.Status == FeishuStatusNeedsAttention {
		return ErrFeishuAppNotConnected
	}
	return nil
}

func isAdminRole(role policy.OrganizationRole) bool { return CanManage(role) }

// agentBindCheck 校验成员能否把 Agent 绑定为飞书机器人（读取 Agent 并锁不住行，调用方在需要时自行加锁）。
func agentBindCheck(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, agentID string) error {
	var owner *string
	var archived bool
	var builtin, visibility string
	err := tx.QueryRow(ctx, `
		SELECT owner_user_id::text, archived_at IS NOT NULL, COALESCE(builtin_kind, ''), visibility
		  FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, agentID).
		Scan(&owner, &archived, &builtin, &visibility)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	isOwner := owner != nil && *owner == actor.UserID
	if !isOwner && !isAdminRole(actor.Role) {
		if visibility == "PRIVATE" {
			return ErrNotFound
		}
		return ErrForbidden
	}
	if archived {
		return ErrNotFound
	}
	if builtin != "" {
		return ErrInvalidRequest
	}
	return nil
}

// RegistrationPrepare 校验权限与冲突，不访问飞书。
func (s Service) RegistrationPrepare(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, in RegistrationInput) (RegistrationPlan, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return RegistrationPlan{}, err
	}
	if s.Box == nil {
		return RegistrationPlan{}, ErrSecretsUnavailable
	}
	if !s.IMConfigured() {
		return RegistrationPlan{}, ErrNotConfigured
	}
	// 飞书 / Lark 的 Agent 扫码会创建独立应用，扫码人在该机器人内
	// 作为发起人识别；任务助手和凭据直连仍需要组织账号绑定。
	if s.prov() != ProviderWeCom && (!IsFeishuFamily(s.prov()) || !strings.EqualFold(strings.TrimSpace(in.Purpose), "agent")) {
		if err := requireFeishuAppActive(ctx, tx, s.prov()); err != nil {
			return RegistrationPlan{}, err
		}
	}
	if len([]rune(in.ExtraPrompt)) > maxExtraPrompt {
		return RegistrationPlan{}, ErrInvalidRequest
	}
	plan := RegistrationPlan{ExtraPrompt: strings.TrimSpace(in.ExtraPrompt)}
	switch strings.ToLower(strings.TrimSpace(in.Purpose)) {
	case "task_assistant":
		plan.Purpose, plan.SpaceID = PurposeTaskAssistant, strings.ToLower(strings.TrimSpace(in.SpaceID))
		if !membership.ValidateUUID(plan.SpaceID) || in.Runtime == nil || in.AgentID != "" {
			return RegistrationPlan{}, ErrInvalidRequest
		}
		can, err := agents.CanManageSpace(ctx, tx, organizationID, plan.SpaceID, agentActor(actor))
		if err != nil {
			return RegistrationPlan{}, err
		}
		if !can {
			return RegistrationPlan{}, ErrForbidden
		}
		config, err := agents.TaskAssistantConfig(agents.AssistantInput{ComputerID: in.Runtime.ComputerID, GrantID: in.Runtime.GrantID,
			Runtime: in.Runtime.Runtime, Model: in.Runtime.Model}, plan.ExtraPrompt)
		if err != nil || !membership.ValidateUUID(config.ComputerID) || !membership.ValidateUUID(config.GrantID) {
			return RegistrationPlan{}, ErrInvalidRequest
		}
		if err := agents.CheckRuntimeTarget(ctx, tx, organizationID, config); err != nil {
			return RegistrationPlan{}, ErrInvalidRequest
		}
		runtime, _ := json.Marshal(FeishuAssistantRuntime{ComputerID: config.ComputerID, GrantID: config.GrantID, Runtime: config.Runtime, Model: config.Model})
		plan.Config = runtime
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.im_bots
			                WHERE organization_id = openfox_private.current_organization_id()
			                  AND kind = 'TASK_ASSISTANT' AND space_id = $1::uuid AND removed_at IS NULL)`, plan.SpaceID).Scan(&exists); err != nil {
			return RegistrationPlan{}, err
		}
		if exists {
			return RegistrationPlan{}, ErrFeishuAssistantExists
		}
	case "agent":
		plan.Purpose, plan.AgentID = PurposeAgent, strings.ToLower(strings.TrimSpace(in.AgentID))
		if !membership.ValidateUUID(plan.AgentID) || in.SpaceID != "" || in.Runtime != nil {
			return RegistrationPlan{}, ErrInvalidRequest
		}
		if err := agentBindCheck(ctx, tx, actor, plan.AgentID); err != nil {
			return RegistrationPlan{}, err
		}
		plan.Config = json.RawMessage(`{}`)
		plan.ExtraPrompt = ""
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.im_bots
			                WHERE organization_id = openfox_private.current_organization_id()
			                  AND agent_id = $1::uuid AND removed_at IS NULL)`, plan.AgentID).Scan(&exists); err != nil {
			return RegistrationPlan{}, err
		}
		if exists {
			return RegistrationPlan{}, ErrFeishuAgentConnected
		}
	default:
		return RegistrationPlan{}, ErrInvalidRequest
	}
	return plan, nil
}

// RegistrationBegin 访问飞书开始设备码流程（事务外）。
func (s Service) RegistrationBegin(ctx context.Context) (feishu.RegistrationBegin, error) {
	if s.Feishu == nil {
		return feishu.RegistrationBegin{}, ErrNotConfigured
	}
	if err := s.Feishu.RegistrationInit(ctx); err != nil {
		return feishu.RegistrationBegin{}, ErrFeishuUnavailable
	}
	begin, err := s.Feishu.RegistrationBeginFlow(ctx)
	if err != nil {
		return feishu.RegistrationBegin{}, ErrFeishuUnavailable
	}
	return begin, nil
}

const registrationColumns = `id::text, purpose, COALESCE(space_id::text, ''), COALESCE(agent_id::text, ''), status, error_code, verification_url,
	expires_at, poll_interval, bot_id::text, requested_by::text`

func scanRegistration(row pgx.Row) (FeishuRegistration, string, error) {
	var item FeishuRegistration
	var requester string
	if err := row.Scan(&item.ID, &item.Purpose, &item.SpaceID, &item.AgentID, &item.Status, &item.ErrorCode, &item.VerificationURL,
		&item.ExpiresAt, &item.PollInterval, &item.BotID, &requester); err != nil {
		return FeishuRegistration{}, "", err
	}
	item.Purpose = strings.ToLower(item.Purpose)
	if item.Status != registrationStatusPend {
		item.VerificationURL = ""
	}
	return item, requester, nil
}

// RegistrationSave 保存会话：同目标等待中的旧会话改为 CANCELLED（superseded），插入新会话（device_code 加密）。
func (s Service) RegistrationSave(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, plan RegistrationPlan,
	begin feishu.RegistrationBegin) (FeishuRegistration, []AuditEntry, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return FeishuRegistration{}, nil, err
	}
	id, err := membership.NewID()
	if err != nil {
		return FeishuRegistration{}, nil, err
	}
	sealed, keyID, err := s.seal([]byte(begin.DeviceCode), registrationAAD(organizationID, id))
	if err != nil {
		return FeishuRegistration{}, nil, err
	}
	// 同一目标同时只有一个等待中的会话。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_app_registrations
		   SET status = 'CANCELLED', error_code = $3, device_code_sealed = NULL, lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND status = 'PENDING' AND purpose = $1
		   AND (($1 = 'TASK_ASSISTANT' AND space_id = NULLIF($2, '')::uuid) OR ($1 = 'AGENT' AND agent_id = NULLIF($2, '')::uuid))`,
		plan.Purpose, firstNonEmpty(plan.SpaceID, plan.AgentID), RegErrSuperseded); err != nil {
		return FeishuRegistration{}, nil, err
	}
	ttl := begin.ExpireIn
	if ttl <= 0 || ttl > registrationMaxSeconds {
		ttl = registrationMaxSeconds
	}
	item, _, err := scanRegistration(tx.QueryRow(ctx, `
		INSERT INTO openfox.im_app_registrations (organization_id, id, purpose, space_id, agent_id, requested_by, config,
		       device_code_sealed, device_code_key_id, verification_url, poll_interval, expires_at, next_poll_at, provider)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5::uuid,
		        jsonb_build_object('runtime', $6::jsonb, 'extra_prompt', $7::text),
		        $8, $9, $10, $11, clock_timestamp() + make_interval(secs => $12::float8), clock_timestamp() + make_interval(secs => $13::float8), $14)
		RETURNING `+registrationColumns,
		id, plan.Purpose, plan.SpaceID, plan.AgentID, actor.UserID, string(plan.Config), plan.ExtraPrompt,
		sealed, keyID, begin.VerificationURIComplete, begin.Interval, float64(ttl), float64(begin.Interval), s.prov()))
	if err != nil {
		if isUniqueViolation(err) {
			return FeishuRegistration{}, nil, ErrVersionConflict
		}
		return FeishuRegistration{}, nil, err
	}
	return item, []AuditEntry{{Action: "integration.oauth_started", ResourceID: id,
		Metadata: map[string]string{"source": s.prov(), "result": strings.ToLower(plan.Purpose)}}}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// RegistrationGet 返回扫码会话；只有发起人可读。等待中的会话已过期时直接落库为 EXPIRED。
func (s Service) RegistrationGet(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (FeishuRegistration, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return FeishuRegistration{}, err
	}
	if !membership.ValidateUUID(id) {
		return FeishuRegistration{}, ErrNotFound
	}
	item, requester, err := scanRegistration(tx.QueryRow(ctx, `
		SELECT `+registrationColumns+` FROM openfox.im_app_registrations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return FeishuRegistration{}, ErrNotFound
	}
	if err != nil {
		return FeishuRegistration{}, err
	}
	if requester != actor.UserID {
		return FeishuRegistration{}, ErrForbidden
	}
	if item.Status == registrationStatusPend && !item.ExpiresAt.After(time.Now()) {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.im_app_registrations
			   SET status = 'EXPIRED', error_code = $2, device_code_sealed = NULL, lease_owner = '', lease_expires_at = NULL,
			       finished_at = clock_timestamp(), updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'PENDING'`, id, RegErrSessionExpired); err != nil {
			return FeishuRegistration{}, err
		}
		item.Status, item.ErrorCode, item.VerificationURL = "EXPIRED", RegErrSessionExpired, ""
	}
	return item, nil
}

// RegistrationCancel 取消等待中的会话；已结束的幂等返回当前状态。
func (s Service) RegistrationCancel(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (FeishuRegistration, error) {
	item, err := s.RegistrationGet(ctx, tx, organizationID, actor, id)
	if err != nil || item.Status != registrationStatusPend {
		return item, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_app_registrations
		   SET status = 'CANCELLED', device_code_sealed = NULL, lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'PENDING'`, id); err != nil {
		return FeishuRegistration{}, err
	}
	item.Status, item.VerificationURL = "CANCELLED", ""
	return item, nil
}

// selectionFor 是写审计 / 配置版本需要的组织选择（成员与授权版本）。
func selectionFor(organizationID string, actor collaboration.Actor) tenancy.OrganizationSelection {
	return tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: actor.UserID, AuthorizationVersion: actor.AuthorizationVersion}
}
