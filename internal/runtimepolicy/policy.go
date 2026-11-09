// Package runtimepolicy 是「对话级 Runtime / 模型 / 权限模式 / 思考深度覆盖」的叶子包：
// 取值常量、有效配置的计算（一处实现，调度与接口共用）以及心跳 capabilities 的解析。
// 它不依赖任何其他业务包，因此 conversations、dispatch、devices 都可以引用。
package runtimepolicy

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// 权限模式四档。FOLLOW_RUNTIME 等于各适配器写死的现有行为；其余三档由适配器显式映射。
const (
	FollowRuntime      = "FOLLOW_RUNTIME"
	ReadOnly           = "READ_ONLY"
	WorkspaceWrite     = "WORKSPACE_WRITE"
	FullAccess         = "FULL_ACCESS"
	ReasoningDefault   = "DEFAULT"
	ReasoningLow       = "LOW"
	ReasoningMedium    = "MEDIUM"
	ReasoningHigh      = "HIGH"
	ReasoningExtraHigh = "EXTRA_HIGH"
	ReasoningMax       = "MAX"
	ReasoningUltra     = "ULTRA"
)

// DefaultModel 表示「用该 Runtime 的默认模型」。
const DefaultModel = "default"

// MaxModelLength 与 agent_config_versions.model 的长度上限一致。
const MaxModelLength = 128

// ApprovalModes 按从严到宽排列的四档权限模式。
var ApprovalModes = []string{FollowRuntime, ReadOnly, WorkspaceWrite, FullAccess}

// ReasoningEfforts 是模型菜单的思考深度档位。
var ReasoningEfforts = []string{ReasoningDefault, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningExtraHigh, ReasoningMax, ReasoningUltra}

// ValidApprovalMode 报告 mode 是否是四档之一（区分大小写，调用方先规范化）。
func ValidApprovalMode(mode string) bool {
	for _, item := range ApprovalModes {
		if item == mode {
			return true
		}
	}
	return false
}

func ValidReasoningEffort(value string) bool {
	for _, item := range ReasoningEfforts {
		if item == value {
			return true
		}
	}
	return false
}

func NormalizeReasoningEffort(value string) string {
	if value == "" {
		return ReasoningDefault
	}
	return value
}

func NormalizeDefaultReasoningEffort(value string, supported []string) string {
	if value == ReasoningDefault || !ValidReasoningEffort(value) {
		return ""
	}
	for _, effort := range supported {
		if effort == value {
			return value
		}
	}
	return ""
}

func NormalizeReasoningEfforts(reported []string) []string {
	result := []string{ReasoningDefault}
	for _, item := range ReasoningEfforts[1:] {
		for _, value := range reported {
			if value == item {
				result = append(result, item)
				break
			}
		}
	}
	return result
}

// NormalizeApprovalModes 把设备上报的 approval_modes 规范化为按严格程度排序、去重、只含四档、一定包含 FOLLOW_RUNTIME
// 的列表（FOLLOW_RUNTIME 是各适配器现有的行为，永远可用；旧版后台服务没有该字段时结果就是只有它）。
func NormalizeApprovalModes(reported []string) []string {
	result := []string{FollowRuntime}
	for _, mode := range ApprovalModes[1:] {
		for _, item := range reported {
			if item == mode {
				result = append(result, mode)
				break
			}
		}
	}
	return result
}

// Providers 是已知的 Runtime 名称（与 devices.ValidRuntimeProvider、适配器名称一致）。
var Providers = []string{"codex", "claude-code", "opencode", "pi"}

// ValidProvider 报告 provider 是否是已知的 Runtime 名称。
func ValidProvider(provider string) bool {
	for _, item := range Providers {
		if item == provider {
			return true
		}
	}
	return false
}

// ValidModel 校验模型名格式：非空、不超过 128 字节、不含控制字符、首尾无空白。
func ValidModel(model string) bool {
	return model != "" && len(model) <= MaxModelLength && model == strings.TrimSpace(model) &&
		strings.IndexFunc(model, unicode.IsControl) < 0
}

// Config 是一次运行最终采用的三项配置。
type Config struct {
	Runtime         string
	Model           string
	ApprovalMode    string
	ReasoningEffort string
}

// Overrides 是对话上的覆盖值；nil 表示没有覆盖，沿用 Agent 配置。
type Overrides struct {
	Runtime         *string
	Model           *string
	ApprovalMode    *string
	ReasoningEffort *string
	Version         int64
}

// Effective 计算有效配置：
//   - Runtime：有覆盖用覆盖，否则用 Agent 配置；
//   - 模型：有模型覆盖用覆盖；只覆盖了 Runtime（且与 Agent 配置的不同）时用 default，
//     因为 Agent 配置的模型属于另一个 Runtime；其他情况用 Agent 配置的模型；
//   - 权限模式：有覆盖用覆盖，否则用 Agent 配置（空串按 FOLLOW_RUNTIME）。
func Effective(agent Config, overrides Overrides) Config {
	result := agent
	if overrides.Runtime != nil {
		result.Runtime = *overrides.Runtime
		if overrides.Model == nil && result.Runtime != agent.Runtime {
			result.Model = DefaultModel
		}
	}
	if overrides.Model != nil {
		result.Model = *overrides.Model
	}
	if overrides.ApprovalMode != nil {
		result.ApprovalMode = *overrides.ApprovalMode
	}
	if overrides.ReasoningEffort != nil {
		result.ReasoningEffort = *overrides.ReasoningEffort
	}
	if result.ApprovalMode == "" {
		result.ApprovalMode = FollowRuntime
	}
	if result.ReasoningEffort == "" {
		result.ReasoningEffort = ReasoningDefault
	}
	return result
}

// Model 是心跳里某个 Runtime 上报的一个可选模型。
type Model struct {
	ID                     string   `json:"id"`
	DisplayName            string   `json:"display_name"`
	IsDefault              bool     `json:"is_default"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

// Capability 是心跳 capabilities.runtimes.<provider> 中服务端关心的部分。
type Capability struct {
	Provider string
	Runnable bool
	Models   []string
	// ModelInfo 是 Models 的完整信息（显示名、是否默认），顺序一致；显示名缺省时回退为 ID。
	ModelInfo   []Model
	ModelsError bool
	// ApprovalModes 是适配器上报真正支持的权限档位；旧版后台服务没有该字段（nil）。
	ApprovalModes []string
	// ReasoningEfforts 是适配器上报真正支持的思考深度；旧版后台服务没有该字段。
	ReasoningEfforts []string
	// CollaborationModes 是适配器上报真正支持的协作模式；旧版后台服务没有该字段（nil）。
	CollaborationModes []string
}

// SupportsApprovalMode 报告该 Runtime 能否按 mode 执行。FOLLOW_RUNTIME 永远支持（它就是现有行为）；
// 其余档位必须出现在设备上报的 approval_modes 里——旧版后台服务没有该字段，一律视为不支持。
func (c Capability) SupportsApprovalMode(mode string) bool {
	if mode == "" || mode == FollowRuntime {
		return true
	}
	for _, item := range c.ApprovalModes {
		if item == mode {
			return true
		}
	}
	return false
}

func (c Capability) SupportsReasoningEffort(value string) bool {
	if value == "" || value == ReasoningDefault {
		return true
	}
	for _, item := range c.ReasoningEfforts {
		if item == value {
			return true
		}
	}
	return false
}

// HasModel 报告模型是否可选：default 永远可选；模型列表为空或上报时带 models_error 时无法核对，只要格式合法就放行。
func (c Capability) SupportsModelReasoningEffort(model, value string) bool {
	if value == "" || value == ReasoningDefault {
		return true
	}
	if !c.SupportsReasoningEffort(value) || c.ModelsError {
		return false
	}
	for _, item := range c.ModelInfo {
		if item.ID != model && !(model == DefaultModel && item.IsDefault) {
			continue
		}
		for _, effort := range item.ReasoningEfforts {
			if effort == value {
				return true
			}
		}
	}
	return false
}

func (c Capability) HasModel(model string) bool {
	if model == DefaultModel || len(c.Models) == 0 || c.ModelsError {
		return true
	}
	for _, item := range c.Models {
		if item == model {
			return true
		}
	}
	return false
}

// ParseCapabilities 解析心跳 capabilities.runtimes，按 provider 排序。
// capabilities 是设备上报的不透明 JSON，格式不符的条目直接跳过；approval_modes 只保留四档内的值。
func ParseCapabilities(raw json.RawMessage) []Capability {
	var document struct {
		Runtimes map[string]json.RawMessage `json:"runtimes"`
	}
	result := make([]Capability, 0)
	if len(raw) == 0 || json.Unmarshal(raw, &document) != nil {
		return result
	}
	providers := make([]string, 0, len(document.Runtimes))
	for provider := range document.Runtimes {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		var item struct {
			Runnable         bool     `json:"runnable"`
			Models           []Model  `json:"models"`
			ModelsError      string   `json:"models_error"`
			ApprovalModes    []string `json:"approval_modes"`
			ReasoningEfforts []string `json:"reasoning_efforts"`
			// 协作模式，只保留三个取值内的值。
			CollaborationModes []string `json:"collaboration_modes"`
		}
		if json.Unmarshal(document.Runtimes[provider], &item) != nil {
			continue
		}
		capability := Capability{Provider: provider, Runnable: item.Runnable, ModelsError: item.ModelsError != ""}
		for _, model := range item.Models {
			model.ReasoningEfforts = NormalizeReasoningEfforts(model.ReasoningEfforts)
			model.DefaultReasoningEffort = NormalizeDefaultReasoningEffort(model.DefaultReasoningEffort, model.ReasoningEfforts)
			capability.Models = append(capability.Models, model.ID)
			if model.DisplayName == "" {
				model.DisplayName = model.ID
			}
			capability.ModelInfo = append(capability.ModelInfo, model)
		}
		for _, mode := range item.ApprovalModes {
			if ValidApprovalMode(mode) {
				capability.ApprovalModes = append(capability.ApprovalModes, mode)
			}
		}
		capability.ReasoningEfforts = NormalizeReasoningEfforts(item.ReasoningEfforts)
		for _, mode := range item.CollaborationModes {
			if ValidCollaborationMode(mode) {
				capability.CollaborationModes = append(capability.CollaborationModes, mode)
			}
		}
		result = append(result, capability)
	}
	return result
}

// Find 在解析结果里按 provider 查找。
func Find(capabilities []Capability, provider string) (Capability, bool) {
	for _, item := range capabilities {
		if item.Provider == provider {
			return item, true
		}
	}
	return Capability{}, false
}
