package runtimepolicy

// 协作模式：Agent 级设置，没有对话级覆盖。
//   - FOLLOW_RUNTIME：跟随 Runtime，不向 Runtime 传任何协作模式参数（与引入该设置之前的行为逐字一致）；
//   - DEFAULT：明确要求普通执行模式（对有「计划模式」概念的 Runtime，即「不是计划模式」）；
//   - PLAN：计划模式，只做计划、不改动工作区；比任何权限档位都严格，与权限档位同时设置时以 PLAN 为准。
//
// 历史值 FOLLOW_AGENT（迁移 000028 的列默认值）按 FOLLOW_RUNTIME 读取和执行，不做数据迁移。
const (
	CollaborationFollowRuntime = FollowRuntime
	CollaborationDefault       = "DEFAULT"
	CollaborationPlan          = "PLAN"
	// CollaborationLegacyFollowAgent 是迁移 000028 的列默认值，只用于读取兼容，新保存时规范化为 FOLLOW_RUNTIME。
	CollaborationLegacyFollowAgent = "FOLLOW_AGENT"
)

// CollaborationModes 是协作模式的三个取值，按界面顺序排列。
var CollaborationModes = []string{CollaborationFollowRuntime, CollaborationDefault, CollaborationPlan}

// ValidCollaborationMode 报告 mode 是否是三个取值之一（区分大小写；FOLLOW_AGENT 不算，调用方先规范化）。
func ValidCollaborationMode(mode string) bool {
	for _, item := range CollaborationModes {
		if item == mode {
			return true
		}
	}
	return false
}

// NormalizeCollaborationMode 把空串与历史值 FOLLOW_AGENT 规范化为 FOLLOW_RUNTIME；其他值原样返回
// （未知值由调用方按「不支持」处理：保存时拒绝，派发时失败，绝不当作 FOLLOW_RUNTIME 悄悄执行）。
func NormalizeCollaborationMode(mode string) string {
	if mode == "" || mode == CollaborationLegacyFollowAgent {
		return CollaborationFollowRuntime
	}
	return mode
}

// NormalizeCollaborationModes 把设备上报的 collaboration_modes 规范化为按界面顺序、去重、只含三个取值、
// 一定包含 FOLLOW_RUNTIME 的列表；旧版后台服务没有该字段时结果就是只有 FOLLOW_RUNTIME。
func NormalizeCollaborationModes(reported []string) []string {
	result := []string{CollaborationFollowRuntime}
	for _, mode := range CollaborationModes[1:] {
		for _, item := range reported {
			if item == mode {
				result = append(result, mode)
				break
			}
		}
	}
	return result
}

// SupportsCollaborationMode 报告该 Runtime 能否按协作模式 mode 执行。FOLLOW_RUNTIME（及空串、FOLLOW_AGENT）永远支持；
// 其余取值必须出现在设备上报的 collaboration_modes 里——旧版后台服务没有该字段，一律视为不支持。
func (c Capability) SupportsCollaborationMode(mode string) bool {
	mode = NormalizeCollaborationMode(mode)
	if mode == CollaborationFollowRuntime {
		return true
	}
	for _, item := range c.CollaborationModes {
		if item == mode {
			return true
		}
	}
	return false
}
