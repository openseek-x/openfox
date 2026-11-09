package automations

import (
	"slices"
)

// maxTaskFilters 与契约 AutomationTriggerConfig.filters 的上限一致；多个筛选条件必须全部满足。
const maxTaskFilters = 10

// TaskFilter 与契约 AutomationTaskFilter 字段一致。
type TaskFilter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// TaskSnapshot 是事件处理时读取的任务属性，只用于筛选判断。
type TaskSnapshot struct {
	ProjectID       string
	TypeID          string
	AssigneeID      string
	ExecutorAgentID string
	Priority        string
	LabelIDs        []string
}

// ValidateTaskFilters 校验筛选条件的字段、运算符与组合：label 只能用 includes / excludes，其余字段只能用 is / is_not。
func ValidateTaskFilters(filters []TaskFilter) error {
	if len(filters) > maxTaskFilters {
		return invalidf("筛选条件最多 %d 个", maxTaskFilters)
	}
	for _, f := range filters {
		if f.Value == "" {
			return invalidf("筛选值不能为空")
		}
		switch f.Field {
		case "project", "task_type", "assignee", "agent", "priority":
			if f.Op != "is" && f.Op != "is_not" {
				return invalidf("%s 只能用 is / is_not", f.Field)
			}
		case "label":
			if f.Op != "includes" && f.Op != "excludes" {
				return invalidf("label 只能用 includes / excludes")
			}
		default:
			return invalidf("未知的筛选字段 %q", f.Field)
		}
	}
	return nil
}

// MatchTaskFilters 要求全部条件都满足。
func MatchTaskFilters(filters []TaskFilter, task TaskSnapshot) bool {
	for _, f := range filters {
		if !matchTaskFilter(f, task) {
			return false
		}
	}
	return true
}

func matchTaskFilter(f TaskFilter, task TaskSnapshot) bool {
	switch f.Field {
	case "project":
		return equalOp(f.Op, task.ProjectID == f.Value)
	case "task_type":
		return equalOp(f.Op, task.TypeID == f.Value)
	case "assignee":
		return equalOp(f.Op, task.AssigneeID == f.Value)
	case "agent":
		return equalOp(f.Op, task.ExecutorAgentID == f.Value)
	case "priority":
		return equalOp(f.Op, task.Priority == f.Value)
	case "label":
		has := slices.Contains(task.LabelIDs, f.Value)
		return f.Op == "includes" && has || f.Op == "excludes" && !has
	}
	return false
}

// equalOp 把 is / is_not 统一成相等判断的结果。
func equalOp(op string, equal bool) bool {
	if op == "is_not" {
		return !equal
	}
	return equal
}
