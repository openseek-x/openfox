package integrations

// 飞书机器人（任务助手 / 绑定 Agents）的列表、编辑、移除与重试。

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
	BotKindTaskAssistant = "TASK_ASSISTANT"
	BotKindAgent         = "AGENT"

	BotConnecting = "CONNECTING"
	BotConnected  = "CONNECTED"
	BotFailed     = "FAILED"

	maxBotSpaces = 200
)

type FeishuBotRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type FeishuBotRuntime struct {
	ComputerID   string `json:"computer_id"`
	ComputerName string `json:"computer_name"`
	GrantID      string `json:"grant_id"`
	Runtime      string `json:"runtime"`
	Model        string `json:"model"`
}

// FeishuBot 对应契约 FeishuBot。
type FeishuBot struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	ConnectionID string            `json:"connection_id"`
	AppID        string            `json:"app_id"`
	BotName      string            `json:"bot_name"`
	BotAvatarURL string            `json:"bot_avatar_url"`
	Status       string            `json:"status"`
	StatusReason string            `json:"status_reason"`
	Agent        FeishuBotRef      `json:"agent"`
	Space        *FeishuBotRef     `json:"space"`
	ExtraPrompt  string            `json:"extra_prompt"`
	Runtime      *FeishuBotRuntime `json:"runtime"`
	CanManage    bool              `json:"can_manage"`
	CreatedAt    time.Time         `json:"created_at"`
	Version      uint64            `json:"version"`
}

type FeishuAssistantRow struct {
	SpaceID               string     `json:"space_id"`
	SpaceName             string     `json:"space_name"`
	CanManage             bool       `json:"can_manage"`
	Bot                   *FeishuBot `json:"bot"`
	PendingRegistrationID *string    `json:"pending_registration_id"`
	// BoundProvider 非空表示该空间的任务助手已经绑定在别的服务商（飞书 / Lark / 钉钉）：每个空间只能有一个任务助手
	// （im_bots_space_assistant_unique 不分服务商），所以当前服务商不能再绑定。没有时整个字段不出现，飞书现有响应不变。
	BoundProvider string `json:"bound_provider,omitempty"`
}

// FeishuBotsView 对应契约 FeishuBotsView。
type FeishuBotsView struct {
	AppConnected  bool                 `json:"app_connected"`
	Assistants    []FeishuAssistantRow `json:"assistants"`
	Agents        []FeishuBot          `json:"agents"`
	CanBindAgent  bool                 `json:"can_bind_agent"`
	BuiltinPrompt string               `json:"builtin_prompt"`
}

type botRow struct {
	bot        FeishuBot
	agentOwner string
	spaceID    string
}

const botSelect = `
	SELECT b.id::text, b.kind, b.connection_id::text, c.external_id, c.display_name, COALESCE(c.settings->>'bot_avatar_url', ''),
	       c.status, c.status_reason, COALESCE(st.state, ''), COALESCE(st.last_error, ''),
	       a.id::text, a.name, COALESCE(a.owner_user_id::text, ''), COALESCE(b.space_id::text, ''), COALESCE(s.name, ''),
	       b.extra_prompt, b.created_at, b.version
	  FROM openfox.im_bots b
	  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
	  JOIN openfox.agents a ON a.organization_id = b.organization_id AND a.id = b.agent_id
	  LEFT JOIN openfox.spaces s ON s.organization_id = b.organization_id AND s.id = b.space_id
	  LEFT JOIN openfox.im_connection_states st ON st.organization_id = c.organization_id AND st.connection_id = c.id
	 WHERE b.organization_id = openfox_private.current_organization_id() AND b.removed_at IS NULL`

func scanBotRow(row pgx.Row) (botRow, error) {
	var r botRow
	var connStatus, reason, state, lastError, spaceName string
	var version int64
	err := row.Scan(&r.bot.ID, &r.bot.Kind, &r.bot.ConnectionID, &r.bot.AppID, &r.bot.BotName, &r.bot.BotAvatarURL,
		&connStatus, &reason, &state, &lastError, &r.bot.Agent.ID, &r.bot.Agent.Name, &r.agentOwner, &r.spaceID, &spaceName,
		&r.bot.ExtraPrompt, &r.bot.CreatedAt, &version)
	if err != nil {
		return botRow{}, err
	}
	r.bot.Version = uint64(version)
	switch {
	case connStatus == StatusNeedsAttention || state == "FAILED":
		r.bot.Status = BotFailed
		r.bot.StatusReason = reason
		if r.bot.StatusReason == "" {
			r.bot.StatusReason = truncateNote(lastError)
		}
	case state == "CONNECTED":
		r.bot.Status = BotConnected
	default:
		r.bot.Status = BotConnecting
	}
	if r.spaceID != "" {
		r.bot.Space = &FeishuBotRef{ID: r.spaceID, Name: spaceName}
	}
	return r, nil
}

// canManageBot 判断成员能否管理机器人：任务助手 → 能管理空间；Agent → 组织所有者 / 管理员或 Agent 所有者。
func canManageBot(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, r botRow, spaceCache map[string]bool) (bool, error) {
	if r.bot.Kind == BotKindAgent {
		return isAdminRole(actor.Role) || r.agentOwner == actor.UserID, nil
	}
	if isAdminRole(actor.Role) {
		return true, nil
	}
	if can, ok := spaceCache[r.spaceID]; ok {
		return can, nil
	}
	can, err := agents.CanManageSpace(ctx, tx, organizationID, r.spaceID, agentActor(actor))
	if err != nil {
		return false, err
	}
	spaceCache[r.spaceID] = can
	return can, nil
}

// botSelectFor 在 botSelect 上加服务商过滤；provider 为空表示不限（后台 worker 按机器人 ID 工作）。
func botSelectFor(provider string) string {
	if provider == "" {
		return botSelect
	}
	return forProv(botSelect+` AND c.provider = 'feishu'`, provider)
}

func loadBotRow(ctx context.Context, tx pgx.Tx, provider, botID string, lock bool) (botRow, error) {
	if !membership.ValidateUUID(botID) {
		return botRow{}, ErrNotFound
	}
	query := botSelectFor(provider) + ` AND b.id = $1::uuid`
	if lock {
		query += ` FOR UPDATE OF b`
	}
	r, err := scanBotRow(tx.QueryRow(ctx, query, botID))
	if errors.Is(err, pgx.ErrNoRows) {
		return botRow{}, ErrNotFound
	}
	return r, err
}

// fillBotDetails 为能管理的人补上附加提示词与 Runtime；其他人为空。
func (s Service) fillBotDetails(ctx context.Context, tx pgx.Tx, organizationID string, r *botRow, canManage bool) error {
	r.bot.CanManage = canManage
	if !canManage || r.bot.Kind != BotKindTaskAssistant {
		r.bot.ExtraPrompt = ""
		if !canManage {
			r.bot.Runtime = nil
		}
		return nil
	}
	runtime, err := agents.LoadTaskAssistantRuntime(ctx, tx, organizationID, r.bot.Agent.ID)
	if err != nil {
		return err
	}
	if runtime != nil {
		r.bot.Runtime = &FeishuBotRuntime{ComputerID: runtime.ComputerID, ComputerName: runtime.ComputerName, GrantID: runtime.GrantID,
			Runtime: runtime.Runtime, Model: runtime.Model}
	}
	return nil
}

// BotsView 返回飞书机器人总览。所有非访客成员可读。
func (s Service) BotsView(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) (FeishuBotsView, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return FeishuBotsView{}, err
	}
	view := FeishuBotsView{Assistants: []FeishuAssistantRow{}, Agents: []FeishuBot{}, BuiltinPrompt: agents.TaskAssistantPrompt}
	app, err := loadFeishuApp(ctx, tx, s.prov(), false)
	if err != nil {
		return FeishuBotsView{}, err
	}
	view.AppConnected = app != nil && app.Status != FeishuStatusNeedsAttention

	rows, err := tx.Query(ctx, botSelectFor(s.prov())+` ORDER BY b.created_at, b.id`)
	if err != nil {
		return FeishuBotsView{}, err
	}
	var all []botRow
	for rows.Next() {
		r, err := scanBotRow(rows)
		if err != nil {
			rows.Close()
			return FeishuBotsView{}, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FeishuBotsView{}, err
	}
	spaceCache := map[string]bool{}
	assistants := map[string]*FeishuBot{}
	for i := range all {
		r := all[i]
		can, err := canManageBot(ctx, tx, organizationID, actor, r, spaceCache)
		if err != nil {
			return FeishuBotsView{}, err
		}
		if err := s.fillBotDetails(ctx, tx, organizationID, &r, can); err != nil {
			return FeishuBotsView{}, err
		}
		if r.bot.Kind == BotKindAgent {
			view.Agents = append(view.Agents, r.bot)
			continue
		}
		bot := r.bot
		assistants[r.spaceID] = &bot
	}

	// 当前成员可读的空间（每个空间一行）。
	spaceRows, err := tx.Query(ctx, `
		SELECT s.id::text, s.name FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.status = 'ACTIVE' AND `+collaboration.SpaceReadableSQL+`
		 ORDER BY lower(s.name), s.id LIMIT $2`, strings.ToUpper(string(actor.Role)), maxBotSpaces)
	if err != nil {
		return FeishuBotsView{}, err
	}
	var spaces []FeishuAssistantRow
	for spaceRows.Next() {
		var row FeishuAssistantRow
		if err := spaceRows.Scan(&row.SpaceID, &row.SpaceName); err != nil {
			spaceRows.Close()
			return FeishuBotsView{}, err
		}
		spaces = append(spaces, row)
	}
	spaceRows.Close()
	if err := spaceRows.Err(); err != nil {
		return FeishuBotsView{}, err
	}
	pending := map[string]string{}
	pendingRows, err := tx.Query(ctx, `
		SELECT space_id::text, id::text FROM openfox.im_app_registrations
		 WHERE organization_id = openfox_private.current_organization_id() AND status = 'PENDING' AND purpose = 'TASK_ASSISTANT'
		   AND requested_by = $1::uuid AND expires_at > clock_timestamp()`, actor.UserID)
	if err != nil {
		return FeishuBotsView{}, err
	}
	for pendingRows.Next() {
		var spaceID, id string
		if err := pendingRows.Scan(&spaceID, &id); err != nil {
			pendingRows.Close()
			return FeishuBotsView{}, err
		}
		pending[spaceID] = id
	}
	pendingRows.Close()
	if err := pendingRows.Err(); err != nil {
		return FeishuBotsView{}, err
	}
	// 其他服务商已绑定的任务助手：空间 → 服务商。当前服务商的助手已经在 assistants 里，这里只取别的服务商的。
	elsewhere := map[string]string{}
	elsewhereRows, err := tx.Query(ctx, `
		SELECT b.space_id::text, c.provider
		  FROM openfox.im_bots b
		  JOIN openfox.integration_connections c ON c.organization_id = b.organization_id AND c.id = b.connection_id
		 WHERE b.organization_id = openfox_private.current_organization_id() AND b.kind = 'TASK_ASSISTANT' AND b.removed_at IS NULL
		   AND b.space_id IS NOT NULL AND c.provider <> $1 AND c.status <> 'DISCONNECTED'
		 LIMIT $2`, s.prov(), maxBotSpaces*5)
	if err != nil {
		return FeishuBotsView{}, err
	}
	for elsewhereRows.Next() {
		var spaceID, provider string
		if err := elsewhereRows.Scan(&spaceID, &provider); err != nil {
			elsewhereRows.Close()
			return FeishuBotsView{}, err
		}
		elsewhere[spaceID] = provider
	}
	elsewhereRows.Close()
	if err := elsewhereRows.Err(); err != nil {
		return FeishuBotsView{}, err
	}
	for _, row := range spaces {
		can := isAdminRole(actor.Role)
		if !can {
			if known, ok := spaceCache[row.SpaceID]; ok {
				can = known
			} else if can, err = agents.CanManageSpace(ctx, tx, organizationID, row.SpaceID, agentActor(actor)); err != nil {
				return FeishuBotsView{}, err
			}
		}
		row.CanManage = can
		row.Bot = assistants[row.SpaceID]
		if row.Bot == nil {
			row.BoundProvider = elsewhere[row.SpaceID]
		}
		if id, ok := pending[row.SpaceID]; ok {
			idCopy := id
			row.PendingRegistrationID = &idCopy
		}
		view.Assistants = append(view.Assistants, row)
	}

	// 能否绑定 Agent：所有者 / 管理员，或拥有至少一个可绑定的 Agent。
	if isAdminRole(actor.Role) {
		view.CanBindAgent = true
	} else if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.agents a
		                WHERE a.organization_id = openfox_private.current_organization_id() AND a.owner_user_id = $1::uuid
		                  AND a.archived_at IS NULL AND a.builtin_kind IS NULL AND a.space_id IS NOT NULL
		                  AND NOT EXISTS (SELECT 1 FROM openfox.im_bots b
		                                   WHERE b.organization_id = a.organization_id AND b.agent_id = a.id AND b.removed_at IS NULL))`,
		actor.UserID).Scan(&view.CanBindAgent); err != nil {
		return FeishuBotsView{}, err
	}
	return view, nil
}

func (s Service) loadBot(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string) (FeishuBot, error) {
	r, err := loadBotRow(ctx, tx, s.prov(), botID, false)
	if err != nil {
		return FeishuBot{}, err
	}
	can, err := canManageBot(ctx, tx, organizationID, actor, r, map[string]bool{})
	if err != nil {
		return FeishuBot{}, err
	}
	if err := s.fillBotDetails(ctx, tx, organizationID, &r, can); err != nil {
		return FeishuBot{}, err
	}
	return r.bot, nil
}

// lockManagedBot 锁住机器人并要求调用方能管理它：不能管理 → ErrForbidden。
func lockManagedBot(ctx context.Context, tx pgx.Tx, provider, organizationID string, actor collaboration.Actor, botID string) (botRow, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return botRow{}, err
	}
	r, err := loadBotRow(ctx, tx, provider, botID, true)
	if err != nil {
		return botRow{}, err
	}
	can, err := canManageBot(ctx, tx, organizationID, actor, r, map[string]bool{})
	if err != nil {
		return botRow{}, err
	}
	if !can {
		return botRow{}, ErrForbidden
	}
	return r, nil
}

// BotUpdateInput 是 updateFeishuBot 的请求体；Runtime 与 ExtraPrompt 至少给一个。
type BotUpdateInput struct {
	Runtime     *FeishuAssistantRuntime
	ExtraPrompt *string
	Version     uint64
}

// UpdateBot 编辑任务助手：更换 Runtime、修改附加提示词；内置 Agent 追加配置版本。
func (s Service) UpdateBot(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string, in BotUpdateInput) (FeishuBot, []AuditEntry, error) {
	r, err := lockManagedBot(ctx, tx, s.prov(), organizationID, actor, botID)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	if r.bot.Kind != BotKindTaskAssistant || in.Version == 0 || (in.Runtime == nil && in.ExtraPrompt == nil) {
		return FeishuBot{}, nil, ErrInvalidRequest
	}
	if r.bot.Version != in.Version {
		return FeishuBot{}, nil, ErrVersionConflict
	}
	extra := r.bot.ExtraPrompt
	if in.ExtraPrompt != nil {
		extra = strings.TrimSpace(*in.ExtraPrompt)
		if len([]rune(extra)) > maxExtraPrompt {
			return FeishuBot{}, nil, ErrInvalidRequest
		}
	}
	current, err := agents.LoadTaskAssistantRuntime(ctx, tx, organizationID, r.bot.Agent.ID)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	input := agents.AssistantInput{}
	if current != nil {
		input = agents.AssistantInput{ComputerID: current.ComputerID, GrantID: current.GrantID, Runtime: current.Runtime, Model: current.Model}
	}
	if in.Runtime != nil {
		input = agents.AssistantInput{ComputerID: in.Runtime.ComputerID, GrantID: in.Runtime.GrantID, Runtime: in.Runtime.Runtime, Model: in.Runtime.Model}
	}
	config, err := agents.TaskAssistantConfig(input, extra)
	if err != nil || !membership.ValidateUUID(config.ComputerID) || !membership.ValidateUUID(config.GrantID) {
		return FeishuBot{}, nil, ErrInvalidRequest
	}
	if err := agents.CheckRuntimeTarget(ctx, tx, organizationID, config); err != nil {
		return FeishuBot{}, nil, ErrInvalidRequest
	}
	selection := selectionFor(organizationID, actor)
	if err := agents.AppendTaskAssistantConfig(ctx, tx, selection, agentActor(actor), r.bot.Agent.ID, config); err != nil {
		return FeishuBot{}, nil, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.im_bots SET extra_prompt = $3, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
		botID, int64(in.Version), extra)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	if tag.RowsAffected() != 1 {
		return FeishuBot{}, nil, ErrVersionConflict
	}
	bot, err := s.loadBot(ctx, tx, organizationID, actor, botID)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	return bot, []AuditEntry{{Action: "integration.settings_updated", ResourceID: r.bot.ConnectionID,
		Metadata: map[string]string{"source": s.prov(), "result": "task_assistant"}}}, nil
}

// RemoveBot 移除任务助手 / 断开 Agent 机器人：停止长连接、连接改为 DISCONNECTED、待发回复 SKIPPED、待处理入站消息 IGNORED；
// 任务助手的内置 Agent 归档。已有的 OpenFox 会话与任务保留。
func (s Service) RemoveBot(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string, version uint64) ([]AuditEntry, error) {
	r, err := lockManagedBot(ctx, tx, s.prov(), organizationID, actor, botID)
	if err != nil {
		return nil, err
	}
	if version == 0 || r.bot.Version != version {
		return nil, ErrVersionConflict
	}
	if err := retireBot(ctx, tx, organizationID, selectionFor(organizationID, actor), agentActor(actor), r, "disconnected by user"); err != nil {
		return nil, err
	}
	return []AuditEntry{{Action: "integration.connection_disconnected", ResourceID: r.bot.ConnectionID,
		Metadata: map[string]string{"source": s.prov(), "result": strings.ToLower(r.bot.Kind)}}}, nil
}

// retireBot 是移除机器人的收尾（也用于 Agent 被归档时）。
func retireBot(ctx context.Context, tx pgx.Tx, organizationID string, selection tenancy.OrganizationSelection, actor agents.Actor, r botRow, reason string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.wecom_notification_targets
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND personal_connection_id IN (
		       SELECT id FROM openfox.integration_connections
		        WHERE organization_id = openfox_private.current_organization_id()
		          AND provider = 'wecom' AND parent_connection_id = $1::uuid)`, r.bot.ConnectionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.wecom_binding_codes
		 WHERE organization_id = openfox_private.current_organization_id() AND bot_id = $1::uuid`, r.bot.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET status = 'DISCONNECTED', disconnected_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND provider = 'wecom'
		   AND scope = 'PERSONAL' AND parent_connection_id = $1::uuid AND status <> 'DISCONNECTED'`, r.bot.ConnectionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_notification_outbox
		   SET status = 'SKIPPED', last_error = 'bot_removed', lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND app_connection_id = $1::uuid
		   AND status IN ('PENDING', 'SENDING')`, r.bot.ConnectionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_bots SET removed_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND removed_at IS NULL`, r.bot.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET status = 'DISCONNECTED', status_reason = $2, disconnected_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status <> 'DISCONNECTED'`,
		r.bot.ConnectionID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.im_connection_states (organization_id, connection_id, state)
		VALUES (openfox_private.current_organization_id(), $1::uuid, 'STOPPED')
		ON CONFLICT (organization_id, connection_id) DO UPDATE
		   SET state = 'STOPPED', lease_owner = '', lease_expires_at = NULL, updated_at = clock_timestamp()`, r.bot.ConnectionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_reply_outbox
		   SET status = 'SKIPPED', last_error = 'bot_removed', lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND bot_id = $1::uuid AND status IN ('PENDING', 'SENDING')`, r.bot.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.im_inbound_messages
		   SET status = 'IGNORED', result = 'bot_removed', lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND bot_id = $1::uuid AND status IN ('PENDING', 'PROCESSING')`, r.bot.ID); err != nil {
		return err
	}
	if r.bot.Kind == BotKindTaskAssistant {
		return agents.ArchiveTaskAssistant(ctx, tx, selection, actor, r.bot.Agent.ID)
	}
	return nil
}

// BotRetryTarget 是重试第一步读到的现状与已保存的 Secret。
type BotRetryTarget struct {
	BotID        string
	ConnectionID string
	AppID        string
	Version      uint64
	Secret       string
}

// BotRetryPrepare 校验权限并解密机器人应用的 Secret。
func (s Service) BotRetryPrepare(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, botID string) (BotRetryTarget, error) {
	r, err := lockManagedBot(ctx, tx, s.prov(), organizationID, actor, botID)
	if err != nil {
		return BotRetryTarget{}, err
	}
	secret, err := s.feishuSecret(ctx, tx, organizationID, r.bot.ConnectionID)
	if err != nil {
		return BotRetryTarget{}, err
	}
	return BotRetryTarget{BotID: botID, ConnectionID: r.bot.ConnectionID, AppID: r.bot.AppID, Version: r.bot.Version, Secret: secret}, nil
}

// BotRetryFinish 回写校验结果，规则同 6d-1 的重新验证：成功 → 重置长连接状态；凭证 / 机器人问题 → 标为需要处理；飞书不可达 → 不改状态。
func (s Service) BotRetryFinish(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, target BotRetryTarget,
	bot feishu.Bot, validateErr error) (FeishuBot, []AuditEntry, error) {
	r, err := lockManagedBot(ctx, tx, s.prov(), organizationID, actor, target.BotID)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	var audits []AuditEntry
	switch {
	case validateErr == nil:
		patch, _ := json.Marshal(botSettings(bot))
		displayName := botDisplayName(bot, r.bot.AppID)
		if s.prov() == ProviderWeCom {
			displayName = r.bot.BotName
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.integration_connections
			   SET display_name = $2, settings = settings || $3::jsonb, status = 'ACTIVE', status_reason = '',
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status <> 'DISCONNECTED'`,
			r.bot.ConnectionID, displayName, patch); err != nil {
			return FeishuBot{}, nil, err
		}
		if err := resetFeishuState(ctx, tx, r.bot.ConnectionID); err != nil {
			return FeishuBot{}, nil, err
		}
		audits = append(audits, AuditEntry{Action: "integration.connection_verified", ResourceID: r.bot.ConnectionID,
			Metadata: map[string]string{"source": s.prov(), "result": "ok"}})
	case errors.Is(validateErr, ErrFeishuCredentialsInvalid), errors.Is(validateErr, ErrFeishuBotUnavailable):
		reason := ReasonCredentialsInvalid
		if errors.Is(validateErr, ErrFeishuBotUnavailable) {
			reason = ReasonBotUnavailable
		}
		if err := markFeishuNeedsAttention(ctx, tx, r.bot.ConnectionID, reason); err != nil {
			return FeishuBot{}, nil, err
		}
		audits = append(audits, AuditEntry{Action: "integration.connection_verified", ResourceID: r.bot.ConnectionID,
			Metadata: map[string]string{"source": s.prov(), "result": reason}})
	}
	view, err := s.loadBot(ctx, tx, organizationID, actor, target.BotID)
	return view, audits, err
}

// 在编译期确认 policy 被引用（isAdminRole 的角色类型）。
var _ = policy.OrganizationRoleOwner
