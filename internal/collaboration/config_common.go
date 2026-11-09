package collaboration

// 全局配置（标签、分组、状态、关联类型、自定义字段）共用的错误、上限与校验。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/membership"
)

// 全局配置类列表的上限；超过时创建返回 409 LIMIT_REACHED。
const (
	MaxLabels        = 2000
	MaxLabelGroups   = 200
	MaxStatuses      = 200
	MaxRelationTypes = 200
	MaxCustomFields  = 500
	MaxFieldOptions  = 200
	// MaxStatusMoveTasks 是跨类别移动 / 删除状态时允许同事务改写的任务数上限。
	MaxStatusMoveTasks = 5000
	// MaxBulkLabels 是标签批量接口一次最多处理的标签数。
	MaxBulkLabels = 100
)

var (
	ErrNameTaken    = NewRuleError(http.StatusConflict, "NAME_TAKEN", "name is already in use")
	ErrLimitReached = NewRuleError(http.StatusConflict, "LIMIT_REACHED", "configuration limit reached")

	ErrStatusDefaultCategory = NewRuleError(http.StatusConflict, "STATUS_DEFAULT_CATEGORY",
		"the default status must stay in the BACKLOG or TODO category and cannot be deleted or archived")
	ErrStatusIsWorkflowDefault = NewRuleError(http.StatusConflict, "STATUS_IS_WORKFLOW_DEFAULT",
		"the status is the default status of a task type workflow")
	ErrStatusReplacementRequired = NewRuleError(http.StatusConflict, "STATUS_REPLACEMENT_REQUIRED",
		"replacement_status_id is required because tasks still use this status")
	ErrStatusReplacementNotInWorkflow = NewRuleError(http.StatusConflict, "STATUS_REPLACEMENT_NOT_IN_WORKFLOW",
		"the replacement status is missing from the workflow of a task type that uses this status")
	ErrStatusMoveTooLarge = NewRuleError(http.StatusConflict, "STATUS_MOVE_TOO_LARGE",
		"too many tasks use this status to rewrite in one request")

	ErrRelationTypeBuiltin = NewRuleError(http.StatusConflict, "RELATION_TYPE_BUILTIN",
		"built-in relation types cannot be changed or deleted")
	ErrCustomFieldInUse = NewRuleError(http.StatusConflict, "CUSTOM_FIELD_IN_USE",
		"the custom field is bound to a task type")
)

var (
	colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	// 状态类别的固定展示顺序，用于列表排序和重排。
	statusCategoryOrderSQL = `CASE ts.category WHEN 'TRIAGE' THEN 0 WHEN 'BACKLOG' THEN 1 WHEN 'TODO' THEN 2
		WHEN 'IN_PROGRESS' THEN 3 WHEN 'DONE' THEN 4 WHEN 'CANCELLED' THEN 5 ELSE 6 END`
)

func invalidInput(message string) error {
	return NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", message)
}

func configNotFound(message string) error {
	return NewRuleError(http.StatusNotFound, "NOT_FOUND", message)
}

// OptionalString 区分 JSON 中「未传」「null」「字符串」三种情况。
type OptionalString struct {
	Set   bool
	Value *string
}

func (o *OptionalString) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
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

// OptionalRaw 区分 JSON 中「未传」「null」「任意值」三种情况；null 时 Value 为 nil。
type OptionalRaw struct {
	Set   bool
	Value json.RawMessage
}

func (o *OptionalRaw) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	o.Value = append(json.RawMessage(nil), data...)
	return nil
}

// normalizeConfigName 去掉首尾空白并校验长度（按字符数）与控制字符。
func normalizeConfigName(value string, maxRunes int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > maxRunes {
		return "", invalidInput("name length is out of range")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", invalidInput("name contains control characters")
		}
	}
	return value, nil
}

func validateDescription(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 1000 {
		return "", invalidInput("description is too long")
	}
	return value, nil
}

func validateColor(value string) error {
	if !colorPattern.MatchString(value) {
		return invalidInput("color must look like #RRGGBB")
	}
	return nil
}

// uniqueUUIDs 校验并去重（保持首次出现的顺序）。
func uniqueUUIDs(values []string, max int) ([]string, error) {
	if len(values) > max {
		return nil, invalidInput("too many ids")
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !membership.ValidateUUID(value) {
			return nil, invalidInput("id is not a valid uuid")
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

// configDatabaseError 把唯一约束冲突映射为 NAME_TAKEN，外键 / 检查约束违反映射为 400。
func configDatabaseError(err error) error {
	if IsPriorityLevelViolation(err) {
		return ErrPriorityLevelUnavailable
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrNameTaken
		case "23503", "23514":
			return invalidInput("request references an unavailable resource or violates a constraint")
		}
	}
	return err
}

// rowExists 判断当前组织下某表是否存在指定 id 的行；table 只允许传入本包的常量。
func rowExists(ctx context.Context, tx pgx.Tx, table, id string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM `+table+`
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid)`, id).Scan(&exists)
	return exists, err
}

// versionOrNotFound 在乐观锁更新 0 行时区分「不存在」与「版本冲突」。
func versionOrNotFound(ctx context.Context, tx pgx.Tx, table, id, notFoundMessage string) error {
	exists, err := rowExists(ctx, tx, table, id)
	if err != nil {
		return err
	}
	if !exists {
		return configNotFound(notFoundMessage)
	}
	return ErrVersionConflict
}
