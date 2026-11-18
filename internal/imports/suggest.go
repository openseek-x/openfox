package imports

import "strings"

// NormalizeHeader 规范化表头：去首尾空白、转小写、折叠连续空白。
func NormalizeHeader(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

var headerFields = map[string]string{
	"需求名称": FieldTitle, "需求标题": FieldTitle, "缺陷标题": FieldTitle, "需求id": FieldSourceID, "缺陷id": FieldSourceID, "父需求id": FieldParent, "处理人": FieldAssignee, "任务名称": FieldTitle, "工作项标题": FieldTitle, "任务标题": FieldTitle,
	"工作项id": FieldSourceID, "任务id": FieldSourceID, "id": FieldSourceID,
	"任务状态": FieldStatus, "负责人": FieldAssignee, "执行者": FieldAssignee,
	"截止时间": FieldDueDate, "截止日期": FieldDueDate, "任务描述": FieldDescription,
	"epic link": FieldProject, "custom field (epic link)": FieldProject, "专项": FieldProject, "所属专项": FieldProject, "所属项目": FieldProject, "项目名称": FieldProject, "epic name": FieldProject, "project name": FieldProject, "source project": FieldProject,
	"summary": FieldTitle, "摘要": FieldTitle, "标题": FieldTitle,
	"issue key": FieldSourceKey, "问题关键字": FieldSourceKey,
	"issue id": FieldSourceID, "问题 id": FieldSourceID, "问题id": FieldSourceID,
	"issue type": FieldType, "问题类型": FieldType,
	"status": FieldStatus, "状态": FieldStatus,
	"priority": FieldPriority, "优先级": FieldPriority,
	"assignee": FieldAssignee, "经办人": FieldAssignee,
	"reporter": FieldReporter, "报告人": FieldReporter,
	"creator": FieldCreator, "创建者": FieldCreator,
	"description": FieldDescription, "描述": FieldDescription,
	"parent id": FieldParent, "parent": FieldParent,
	"labels": FieldLabels, "标签": FieldLabels,
	"comment": FieldComment, "注释": FieldComment, "评论": FieldComment,
	"created": FieldCreatedAt, "创建日期": FieldCreatedAt, "创建时间": FieldCreatedAt,
	"updated": FieldUpdatedAt, "已更新": FieldUpdatedAt, "更新日期": FieldUpdatedAt,
	"due date": FieldDueDate, "到期日": FieldDueDate,
	"start date": FieldStartDate, "custom field (start date)": FieldStartDate,
	"custom field (story point estimate)": FieldStoryPoints, "custom field (story points)": FieldStoryPoints,
	"sprint": FieldSprint, "custom field (sprint)": FieldSprint,
	"resolution": FieldResolution,
	"watchers":   FieldWatchers,
	"attachment": FieldAttachment, "附件": FieldAttachment,
	"original estimate": FieldOriginalEstimate,
}

// suggestOne 返回单个表头建议的字段；没有匹配返回 ignore。
func suggestOne(header string) string {
	key := NormalizeHeader(header)
	if field, ok := headerFields[key]; ok {
		return field
	}
	if strings.HasPrefix(key, "inward issue link (") || strings.HasPrefix(key, "outward issue link (") {
		return FieldRelations
	}
	return FieldIgnore
}

// SuggestFields 按表头给每一列建议字段（大小写不敏感）。
// labels / comment 允许多列；其余字段只建议第一列，重复的同名列建议 ignore，避免预填配置就违反「单值字段只映射一列」。
// 同时存在「Parent id」与「Parent」时优先 Parent id。
func SuggestFields(headers []string) []string {
	result := make([]string, len(headers))
	hasParentID := false
	for _, h := range headers {
		if NormalizeHeader(h) == "parent id" {
			hasParentID = true
			break
		}
	}
	used := map[string]bool{}
	for i, h := range headers {
		field := suggestOne(h)
		if field == FieldParent && hasParentID && NormalizeHeader(h) != "parent id" {
			field = FieldIgnore
		}
		if field != FieldIgnore && field != FieldLabels && field != FieldComment {
			if used[field] {
				field = FieldIgnore
			} else {
				used[field] = true
			}
		}
		result[i] = field
	}
	return result
}

// SuggestedConfig 用建议字段预填列映射，并给出默认选项（导入未完成 / 已完成任务，其余关闭）。
func SuggestedConfig(headers []string) Config {
	fields := SuggestFields(headers)
	columns := make([]ColumnMapping, len(fields))
	for i, f := range fields {
		columns[i] = ColumnMapping{Index: i, Field: f}
	}
	return Config{
		Columns:  columns,
		Statuses: []StatusMapping{},
		Users:    []UserMapping{},
		Options:  Options{ImportOpen: true, ImportDone: true},
	}
}

func suggestFileCustomFields(headers []string, columns []ColumnMapping) {
	for i, h := range headers {
		key := NormalizeHeader(h)
		if columns[i].Field != FieldIgnore || suggestOne(h) != FieldIgnore || key == "" || strings.HasSuffix(key, " id") {
			continue
		}
		name := strings.TrimSpace(h)
		if strings.HasPrefix(key, "custom field (") && strings.HasSuffix(key, ")") {
			name = strings.TrimSpace(name[strings.Index(name, "(")+1 : len(name)-1])
		}
		columns[i].Field, columns[i].CustomName = FieldCustom, name
	}
}
