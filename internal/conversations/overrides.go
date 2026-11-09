package conversations

// 对话级 Runtime / 模型 / 权限模式覆盖。
// 覆盖只改「下一次运行」：派发在创建 Run 时把有效配置写进 runs 的快照列（见 dispatch.evaluate），
// 进行中的 Run 不受影响。只能换同一台电脑上的其他 Runtime：本接口没有电脑 / 授权字段，
// Runtime 校验只看 Agent 当前配置绑定的那台电脑最近一次心跳。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/runtimepolicy"
)

var (
	// ErrCrewConversation：小队消息按 @ 分派给不同成员，会话级覆盖没有明确含义。
	ErrCrewConversation = errors.New("conversations: overrides are not available for crew conversations")
	// ErrOverrideAgentUnavailable：Agent 已归档或没有当前配置，无法确定默认值与电脑。
	ErrOverrideAgentUnavailable = errors.New("conversations: the agent has no usable configuration")
	// ErrRuntimeNotAvailable：覆盖的 Runtime 没有出现在 Agent 绑定电脑最近一次心跳的 runtimes 里。
	ErrRuntimeNotAvailable = errors.New("conversations: runtime is not reported by the agent's computer")
	// ErrModelNotAvailable：覆盖的模型不是 default，也不在该 Runtime 上报的模型列表里。
	ErrModelNotAvailable = errors.New("conversations: model is not available on the runtime")
	// ErrReasoningEffortNotAvailable：覆盖的思考深度不是设备上的 Runtime 所支持的档位。
	ErrReasoningEffortNotAvailable = errors.New("conversations: reasoning effort is not available on the runtime")
)

// OptionalString 区分 JSON 里「字段缺省」（Set=false，不改）、「null」（Set=true、Value=nil，清除覆盖）和具体取值。
type OptionalString struct {
	Set   bool
	Value *string
}

// UnmarshalJSON 只在字段出现时被调用。
func (o *OptionalString) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

// OverridesPatch 是覆盖接口的请求体（expected_version 必填，由 API 层校验存在）。
type OverridesPatch struct {
	Runtime         OptionalString `json:"runtime"`
	Model           OptionalString `json:"model"`
	ApprovalMode    OptionalString `json:"approval_mode"`
	ReasoningEffort OptionalString `json:"reasoning_effort"`
	ExpectedVersion *int64         `json:"expected_version"`
}

// Overrides 是会话上的覆盖设置；null 表示沿用 Agent 配置。Version 是覆盖设置的乐观锁。
type Overrides struct {
	Runtime         *string `json:"runtime"`
	Model           *string `json:"model"`
	ApprovalMode    *string `json:"approval_mode"`
	ReasoningEffort *string `json:"reasoning_effort"`
	Version         int64   `json:"version"`
}

// EffectiveConfig 是下一次运行会采用的 Runtime / 模型 / 权限模式。
type EffectiveConfig struct {
	Runtime         string `json:"runtime"`
	Model           string `json:"model"`
	ApprovalMode    string `json:"approval_mode"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func (o Overrides) policy() runtimepolicy.Overrides {
	return runtimepolicy.Overrides{Runtime: o.Runtime, Model: o.Model, ApprovalMode: o.ApprovalMode, ReasoningEffort: o.ReasoningEffort, Version: o.Version}
}

// effectiveConfigs 由一处实现（runtimepolicy.Effective）计算，摘要接口与调度共用。
func effectiveConfigs(agent runtimepolicy.Config, overrides Overrides) (effective, defaults *EffectiveConfig) {
	if agent.Runtime == "" {
		return nil, nil
	}
	if agent.ApprovalMode == "" {
		agent.ApprovalMode = runtimepolicy.FollowRuntime
	}
	if agent.ReasoningEffort == "" {
		agent.ReasoningEffort = runtimepolicy.ReasoningDefault
	}
	merged := runtimepolicy.Effective(agent, overrides.policy())
	return &EffectiveConfig{Runtime: merged.Runtime, Model: merged.Model, ApprovalMode: merged.ApprovalMode, ReasoningEffort: merged.ReasoningEffort},
		&EffectiveConfig{Runtime: agent.Runtime, Model: agent.Model, ApprovalMode: agent.ApprovalMode, ReasoningEffort: agent.ReasoningEffort}
}

func sameOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// UpdateOverrides 修改会话的覆盖设置，成功后返回最新摘要。
//   - 鉴权复用发消息的 AuthorizeWrite：能在该会话发消息的人才能修改（只读访客、非成员、跨组织都被拒；会话已关闭 409）；
//   - 字段缺省 = 不改，null = 清除该项覆盖；最终状态与当前相同则不改版本、不留记录（重试幂等）；
//   - expected_version 与 overrides_version 不一致时 ErrVersionConflict；
//   - 成功后 overrides_version + 1，并在会话时间线留一条系统消息。
func (s Service) UpdateOverrides(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, patch OverridesPatch) (Summary, error) {
	if !patch.Runtime.Set && !patch.Model.Set && !patch.ApprovalMode.Set && !patch.ReasoningEffort.Set {
		return Summary{}, ErrInvalidRequest
	}
	if patch.ExpectedVersion == nil || *patch.ExpectedVersion < 0 {
		return Summary{}, ErrInvalidRequest
	}
	conversation, err := s.loadVisible(ctx, tx, organizationID, conversationID, actor, true, true)
	if err != nil {
		return Summary{}, err
	}
	if conversation.Lifecycle != LifecycleOpen {
		return Summary{}, ErrConversationClosed
	}
	var privateAssistant bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.agents
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND builtin_kind='ASSISTANT' AND owner_user_id<>$2)`, conversation.AgentID, actor.UserID).Scan(&privateAssistant); err != nil {
		return Summary{}, err
	}
	if privateAssistant {
		return Summary{}, ErrForbidden
	}
	if conversation.CrewID != "" {
		return Summary{}, ErrCrewConversation
	}
	// 与派发的物化互斥：派发对同一行 FOR UPDATE，这里也锁住，读到的就是将被采用的值。
	var current Overrides
	if err := tx.QueryRow(ctx, `
		SELECT runtime_override, model_override, approval_mode_override, reasoning_effort_override, overrides_version
		  FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, conversationID).Scan(&current.Runtime, &current.Model, &current.ApprovalMode, &current.ReasoningEffort, &current.Version); err != nil {
		return Summary{}, err
	}
	next := current
	if patch.Runtime.Set {
		next.Runtime = patch.Runtime.Value
	}
	if patch.Model.Set {
		next.Model = patch.Model.Value
	}
	if patch.ApprovalMode.Set {
		next.ApprovalMode = patch.ApprovalMode.Value
	}
	if patch.ReasoningEffort.Set {
		next.ReasoningEffort = patch.ReasoningEffort.Value
	}
	if next.Runtime != nil && !runtimepolicy.ValidProvider(*next.Runtime) ||
		next.Model != nil && !runtimepolicy.ValidModel(*next.Model) ||
		next.ApprovalMode != nil && !runtimepolicy.ValidApprovalMode(*next.ApprovalMode) ||
		next.ReasoningEffort != nil && !runtimepolicy.ValidReasoningEffort(*next.ReasoningEffort) {
		return Summary{}, ErrInvalidRequest
	}
	if sameOptional(current.Runtime, next.Runtime) && sameOptional(current.Model, next.Model) &&
		sameOptional(current.ApprovalMode, next.ApprovalMode) &&
		sameOptional(current.ReasoningEffort, next.ReasoningEffort) {
		return loadSummary(ctx, tx, conversationID)
	}
	if *patch.ExpectedVersion != current.Version {
		return Summary{}, ErrVersionConflict
	}
	if err := s.validateOverrideCapabilities(ctx, tx, conversation, patch, next); err != nil {
		return Summary{}, err
	}
	var seq int64
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.conversations
		   SET runtime_override = $2, model_override = $3, approval_mode_override = $4, reasoning_effort_override = $5,
		       overrides_version = overrides_version + 1,
		       next_message_seq = next_message_seq + 1, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND overrides_version = $6
		RETURNING next_message_seq - 1`,
		conversationID, next.Runtime, next.Model, next.ApprovalMode, next.ReasoningEffort, current.Version).Scan(&seq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Summary{}, ErrVersionConflict
		}
		return Summary{}, mapError(err)
	}
	if err := insertOverridesMessage(ctx, tx, conversationID, seq, actor.UserID, current, next); err != nil {
		return Summary{}, err
	}
	return loadSummary(ctx, tx, conversationID)
}

// validateOverrideCapabilities 按 Agent 当前配置绑定的电脑、最近一次心跳校验本次修改涉及的取值。
// 只校验本次请求触碰的字段（以及它们依赖的另一项）：
//   - Runtime 非空：必须出现在心跳 runtimes 里（不要求 runnable，不可运行时 Run 会按等待语义排队）；
//   - 模型非空：必须是 default，或出现在有效 Runtime 的模型列表里（列表为空或带 models_error 时只校验格式）。
//     Runtime 被改而旧的模型覆盖留在原处时，也会按新 Runtime 重新校验，不匹配则整体拒绝，请求需要同时给出模型。
//
// 权限模式只校验取值；设备是否支持由派发前的核对负责（设备能力可能随升级变化）。
func (s Service) validateOverrideCapabilities(ctx context.Context, tx pgx.Tx, conversation Conversation, patch OverridesPatch, next Overrides) error {
	touchesRuntime, touchesModel, touchesReasoning := patch.Runtime.Set, patch.Model.Set, patch.ReasoningEffort.Set
	if !touchesRuntime && !touchesModel && !touchesReasoning {
		return nil
	}
	agentRuntime, agentModel, capabilities, _, err := loadAgentComputer(ctx, tx, conversation.AgentID)
	if err != nil {
		return err
	}
	parsed := runtimepolicy.ParseCapabilities(capabilities)
	effectiveRuntime := agentRuntime
	if next.Runtime != nil {
		effectiveRuntime = *next.Runtime
	}
	capability, found := runtimepolicy.Find(parsed, effectiveRuntime)
	if next.Runtime != nil && !found {
		return ErrRuntimeNotAvailable
	}
	// 没有 Runtime 覆盖时 Agent 自己的 Runtime 可能尚未上报：无从核对，只校验格式。
	if next.Model != nil && found && !capability.HasModel(*next.Model) {
		return ErrModelNotAvailable
	}
	if next.ReasoningEffort != nil && *next.ReasoningEffort != runtimepolicy.ReasoningDefault {
		model := agentModel
		if effectiveRuntime != agentRuntime {
			model = runtimepolicy.DefaultModel
		}
		if next.Model != nil {
			model = *next.Model
		}
		if !found || !capability.SupportsModelReasoningEffort(model, *next.ReasoningEffort) {
			return ErrReasoningEffortNotAvailable
		}
	}
	return nil
}

// loadAgentComputer 读取 Agent 当前配置的 Runtime，以及它绑定电脑最近一次心跳的 capabilities 与在线状态。
// 校验覆盖取值与「可选项」接口共用这一段，保证两者看到的是同一份数据。
func loadAgentComputer(ctx context.Context, tx pgx.Tx, agentID string) (agentRuntime, agentModel string, capabilities json.RawMessage, online bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT cfg.runtime, cfg.model, d.capabilities,
		       (d.status = 'ACTIVE' AND d.last_seen_at IS NOT NULL
		        AND d.last_seen_at >= clock_timestamp() - make_interval(secs => $2))
		  FROM openfox.agents a
		  JOIN openfox.agent_config_versions cfg
		    ON cfg.organization_id = a.organization_id AND cfg.agent_id = a.id AND cfg.config_version = a.current_config_version
		  JOIN openfox.physical_devices d ON d.id = cfg.computer_id
		 WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = $1::uuid
		   AND a.status = 'ACTIVE' AND a.archived_at IS NULL`, agentID, OnlineWindowSeconds).Scan(&agentRuntime, &agentModel, &capabilities, &online)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil, false, ErrOverrideAgentUnavailable
	}
	return agentRuntime, agentModel, capabilities, online, err
}

// OverrideOptionModel / OverrideOptionRuntime / OverrideOptions 是「可选项」接口的返回：
// 只含 Runtime、模型与权限档位，不含电脑名称、环境变量与授权信息。
type OverrideOptionModel struct {
	ID                     string   `json:"id"`
	DisplayName            string   `json:"display_name"`
	IsDefault              bool     `json:"is_default"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

type OverrideOptionRuntime struct {
	Provider         string                `json:"provider"`
	Runnable         bool                  `json:"runnable"`
	Models           []OverrideOptionModel `json:"models"`
	ApprovalModes    []string              `json:"approval_modes"`
	ReasoningEfforts []string              `json:"reasoning_efforts"`
}

type OverrideOptions struct {
	ComputerOnline bool                    `json:"computer_online"`
	Runtimes       []OverrideOptionRuntime `json:"runtimes"`
}

// OverrideOptions 返回 Agent 绑定电脑最近一次心跳里的 Runtime 列表（只读）。
// 鉴权与修改覆盖相同（AuthorizeWrite）：能聊天但看不到 Agent 绑定电脑的人也能拿到可选项；小队对话与修改接口一样 409。
func (s Service) OverrideOptions(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) (OverrideOptions, error) {
	conversation, err := s.AuthorizeWrite(ctx, tx, organizationID, conversationID, actor)
	if err != nil {
		return OverrideOptions{}, err
	}
	if conversation.CrewID != "" {
		return OverrideOptions{}, ErrCrewConversation
	}
	_, _, capabilities, online, err := loadAgentComputer(ctx, tx, conversation.AgentID)
	if err != nil {
		return OverrideOptions{}, err
	}
	result := OverrideOptions{ComputerOnline: online, Runtimes: []OverrideOptionRuntime{}}
	for _, capability := range runtimepolicy.ParseCapabilities(capabilities) {
		item := OverrideOptionRuntime{
			Provider: capability.Provider, Runnable: capability.Runnable,
			Models:           []OverrideOptionModel{},
			ApprovalModes:    runtimepolicy.NormalizeApprovalModes(capability.ApprovalModes),
			ReasoningEfforts: runtimepolicy.NormalizeReasoningEfforts(capability.ReasoningEfforts),
		}
		for _, model := range capability.ModelInfo {
			item.Models = append(item.Models, OverrideOptionModel{ID: model.ID, DisplayName: model.DisplayName, IsDefault: model.IsDefault,
				ReasoningEfforts: model.ReasoningEfforts, DefaultReasoningEffort: model.DefaultReasoningEffort})
		}
		result.Runtimes = append(result.Runtimes, item)
	}
	return result, nil
}

// insertOverridesMessage 在时间线写一条系统消息（沿用现有的 SYSTEM 文本消息，不新造消息体系）。
func insertOverridesMessage(ctx context.Context, tx pgx.Tx, conversationID string, seq int64, actorID string, before, after Overrides) error {
	var email string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(email, '') FROM openfox.users WHERE id = $1::uuid`, actorID).Scan(&email); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	name := email
	if at := strings.IndexByte(email, '@'); at > 0 {
		name = email[:at]
	}
	if name == "" {
		name = "A member"
	}
	var changes []string
	describe := func(label string, was, now *string) {
		if sameOptional(was, now) {
			return
		}
		if now == nil {
			changes = append(changes, label+" reset to the agent default")
		} else {
			changes = append(changes, label+" set to "+*now)
		}
	}
	describe("Runtime", before.Runtime, after.Runtime)
	describe("Model", before.Model, after.Model)
	describe("Permission mode", before.ApprovalMode, after.ApprovalMode)
	describe("Reasoning depth", before.ReasoningEffort, after.ReasoningEffort)
	body := name + " changed this conversation's settings: " + strings.Join(changes, "; ") +
		". The change takes effect from the next message; a run already in progress is not affected."
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, body)
		VALUES (openfox_private.current_organization_id(), gen_random_uuid(), $1::uuid, $2, 'SYSTEM', $3)`,
		conversationID, seq, body)
	return mapError(err)
}
