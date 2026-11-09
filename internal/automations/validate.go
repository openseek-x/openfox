package automations

import (
	"strings"
	"unicode/utf8"

	"openfox/internal/membership"
)

// validateFields 做与数据库无关的字段校验：标题、提示词、指派类型、通知人员格式与触发器结构。
// 指派是否可用、通知人员是否能读空间、状态是否属于工作流等依赖数据库的检查在服务层完成。
func validateFields(title, prompt, assigneeKind, assigneeID string, notifyUserIDs []string, triggers []TriggerInput) error {
	if err := validateHead(title, prompt, assigneeKind, assigneeID, notifyUserIDs); err != nil {
		return err
	}
	return validateTriggerList(triggers)
}

// validateHead 校验标题、提示词、指派与通知人员的格式。
func validateHead(title, prompt, assigneeKind, assigneeID string, notifyUserIDs []string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(title)); n < 1 || n > maxTitleRunes {
		return invalidf("标题长度必须在 1-%d 之间", maxTitleRunes)
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(prompt)); n < 1 || n > maxPromptRunes {
		return invalidf("提示词长度必须在 1-%d 之间", maxPromptRunes)
	}
	if assigneeKind != "AGENT" && assigneeKind != "CREW" {
		return invalidf("指派类型必须是 AGENT 或 CREW")
	}
	if !membership.ValidateUUID(assigneeID) {
		return invalidf("指派对象 ID 无效")
	}
	if len(notifyUserIDs) > maxNotifyUsers {
		return invalidf("通知人员最多 %d 人", maxNotifyUsers)
	}
	for _, id := range notifyUserIDs {
		if !membership.ValidateUUID(id) {
			return invalidf("通知人员 ID 无效")
		}
	}
	return nil
}

// validateTriggerList 校验触发器数量与每个触发器的结构。
func validateTriggerList(triggers []TriggerInput) error {
	if len(triggers) < 1 || len(triggers) > maxTriggers {
		return invalidf("触发器数量必须在 1-%d 之间", maxTriggers)
	}
	for _, t := range triggers {
		if err := validateTrigger(t); err != nil {
			return err
		}
	}
	return nil
}

// validateTrigger 校验单个触发器的结构：每种 kind 只能带自己的配置，webhook 的签名与白名单只对 webhook 生效。
func validateTrigger(t TriggerInput) error {
	if t.ID != "" && !membership.ValidateUUID(t.ID) {
		return invalidf("触发器 ID 无效")
	}
	if t.Kind != "webhook" && (t.SignatureRequired != nil || len(t.IPAllowlist) > 0) {
		return invalidf("只有 Webhook 触发器可以设置签名与 IP 白名单")
	}
	if err := ValidateIPAllowlist(t.IPAllowlist); err != nil {
		return err
	}
	c := t.Config
	switch t.Kind {
	case "schedule":
		if c.Schedule == nil || len(c.StatusIDs) > 0 || len(c.Filters) > 0 || c.Contains != "" {
			return invalidf("定时触发器只能有 schedule 配置")
		}
		_, err := ValidateSchedule(*c.Schedule)
		return err
	case "task_status":
		if c.Schedule != nil || c.Contains != "" {
			return invalidf("任务状态触发器只能有 status_ids 与 filters")
		}
		if err := validateStatusIDs(c.StatusIDs); err != nil {
			return err
		}
		return ValidateTaskFilters(c.Filters)
	case "comment":
		if c.Schedule != nil || len(c.StatusIDs) > 0 || len(c.Filters) > 0 {
			return invalidf("评论触发器只能有 contains")
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(c.Contains)); n < 1 || n > maxContainsRunes {
			return invalidf("评论关键词长度必须在 1-%d 之间", maxContainsRunes)
		}
		return nil
	case "webhook":
		if c.Schedule != nil || len(c.StatusIDs) > 0 || len(c.Filters) > 0 || c.Contains != "" {
			return invalidf("Webhook 触发器的配置必须为空对象")
		}
		return nil
	default:
		return invalidf("未知的触发器类型")
	}
}

// validateStatusIDs 要求至少一个且不重复的合法状态 ID；存在性在服务层按工作流检查。
func validateStatusIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > maxStatusIDs {
		return invalidf("任务状态触发器需要 1-%d 个状态", maxStatusIDs)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !membership.ValidateUUID(id) || seen[id] {
			return invalidf("状态 ID 无效或重复")
		}
		seen[id] = true
	}
	return nil
}
