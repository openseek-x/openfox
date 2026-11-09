package collaboration

// 分拣台：设置、责任成员、规则、新任务进入分拣台，以及接受 / 拒绝 / 标记重复 / 稍后处理。
// 接受、拒绝、标记重复都经 UpdateTask 改状态（乐观锁、动态、离开分拣时清空分拣字段都在那里完成）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
)

var (
	ErrTriageStatusMissing   = NewRuleError(409, "TRIAGE_STATUS_MISSING", "the organization has no triage status")
	errStatusCategoryMissing = NewRuleError(409, "STATUS_CATEGORY_MISSING", "the organization has no status in the required category")
	errNotInTriage           = NewRuleError(409, "NOT_IN_TRIAGE", "the task is not in triage")
	errInvalidAcceptStatus   = NewRuleError(400, "INVALID_STATUS", "choose a workflow status outside triage")
	errPriorityRequired      = NewRuleError(400, "PRIORITY_REQUIRED", "a priority is required to accept this task")
	errInvalidSnooze         = NewRuleError(400, "INVALID_SNOOZE", "snooze time must be in the future and within one year")
	errInvalidDuplicate      = NewRuleError(400, "INVALID_DUPLICATE", "duplicate target is not available")
	errInvalidTriageMember   = NewRuleError(400, "INVALID_TRIAGE_MEMBER", "triage member is not available")
	errInvalidTriageAction   = NewRuleError(400, "INVALID_REQUEST", "triage action is invalid")
	errInvalidTriageRule     = NewRuleError(400, "INVALID_TRIAGE_RULE", "triage rule is invalid")
	errTriageRuleNotFound    = NewRuleError(404, "NOT_FOUND", "triage rule not found")
	errTriageRuleLimit       = NewRuleError(409, "LIMIT_REACHED", "too many triage rules")
)

const (
	maxTriageMembers = 20
	maxTriageRules   = 50
)

// ---- 规则 ----

type TriageCondition struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

type TriageConditionGroup struct {
	Conditions []TriageCondition `json:"conditions"`
}

// TriageAction.Value：assign / add_labels 是 id 列表，其余动作是单个字符串。
type TriageAction struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type TriageRule struct {
	ID         string                 `json:"id"`
	SpaceID    string                 `json:"space_id"`
	Name       string                 `json:"name"`
	Position   int                    `json:"position"`
	Enabled    bool                   `json:"enabled"`
	Conditions []TriageConditionGroup `json:"conditions"`
	Actions    []TriageAction         `json:"actions"`
	LastUsedAt *time.Time             `json:"last_used_at"`
	Version    uint64                 `json:"version"`
}

// TriageRuleInput 用于新建与修改；修改时为空 / nil 的字段沿用原值。
type TriageRuleInput struct {
	Name       string
	Enabled    *bool
	Conditions []TriageConditionGroup
	Actions    []TriageAction
	Version    uint64
}

// 条件字段与任务筛选对齐（另加来源）；新建任务此时还没有关联，所以不提供关联条件。
var triageFieldOps = map[string][]string{
	"source": {"is", "is_not"}, "priority": {"is", "is_not"}, "type": {"is", "is_not"},
	"creator": {"is", "is_not"}, "assignee": {"is", "is_not"}, "crew": {"is", "is_not"},
	"project": {"is", "is_not"}, "iteration": {"is", "is_not"},
	"label":    {"has_any", "has_all", "has_none"},
	"content":  {"contains", "not_contains", "starts_with", "ends_with", "exactly"},
	"due_date": {"before", "after", "between", "none"},
}

var triageSources = []string{"member", "guest", "agent", "integration"}

// normalizeTriageValue 校验并规范化一个条件值；content 保留原样（匹配时不区分大小写）。
func normalizeTriageValue(field, value string) (string, bool) {
	if field == "content" {
		return value, strings.TrimSpace(value) != "" && len([]rune(value)) <= 200
	}
	value = strings.ToLower(strings.TrimSpace(value))
	switch field {
	case "source":
		return value, slices.Contains(triageSources, value)
	case "priority":
		priority, err := ParsePriority(value)
		return priority, err == nil
	case "due_date":
		_, err := time.Parse("2006-01-02", value)
		return value, err == nil
	case "project", "iteration":
		return value, value == "none" || membership.ValidateUUID(value)
	default:
		return value, membership.ValidateUUID(value)
	}
}

func normalizeTriageCondition(c *TriageCondition) bool {
	if !slices.Contains(triageFieldOps[c.Field], c.Op) || len(c.Values) > 20 {
		return false
	}
	n := len(c.Values)
	switch {
	case c.Field == "due_date" && c.Op == "none":
		if n != 0 {
			return false
		}
	case c.Field == "due_date" && c.Op == "between":
		if n != 2 {
			return false
		}
	case c.Field == "due_date" || c.Field == "content":
		if n != 1 {
			return false
		}
	case n == 0:
		return false
	}
	for i, value := range c.Values {
		normalized, ok := normalizeTriageValue(c.Field, value)
		if !ok {
			return false
		}
		c.Values[i] = normalized
	}
	return true
}

// triageActionIDs 读出动作的值：单值动作返回一个元素；数据库读回的列表是 []any。
func triageActionIDs(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		ids := make([]string, 0, len(v))
		for _, item := range v {
			if id, ok := item.(string); ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == len(v) {
			return ids
		}
	}
	return nil
}

func normalizeTriageAction(a *TriageAction) bool {
	_, isText := a.Value.(string)
	values := triageActionIDs(a.Value)
	switch a.Type {
	case "assign", "add_labels":
		if isText || len(values) == 0 || len(values) > 20 {
			return false
		}
	case "move_space", "set_status", "set_project", "set_priority":
		if !isText {
			return false
		}
	default:
		return false
	}
	for i, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if a.Type == "set_priority" {
			priority, err := ParsePriority(value)
			if err != nil {
				return false
			}
			value = priority
		} else if !membership.ValidateUUID(value) {
			return false
		}
		values[i] = value
	}
	if isText {
		a.Value = values[0]
	} else {
		a.Value = values
	}
	return true
}

func (r TriageRuleInput) encode() (name string, conditions, actions []byte, err error) {
	name = strings.TrimSpace(r.Name)
	if name == "" || len([]rune(name)) > 80 || len(r.Conditions) > 10 || len(r.Actions) == 0 || len(r.Actions) > 20 {
		return "", nil, nil, errInvalidTriageRule
	}
	for _, group := range r.Conditions {
		if len(group.Conditions) > 20 {
			return "", nil, nil, errInvalidTriageRule
		}
		for i := range group.Conditions {
			if !normalizeTriageCondition(&group.Conditions[i]) {
				return "", nil, nil, errInvalidTriageRule
			}
		}
	}
	for i := range r.Actions {
		if !normalizeTriageAction(&r.Actions[i]) {
			return "", nil, nil, errInvalidTriageRule
		}
	}
	if conditions, err = json.Marshal(r.Conditions); err == nil {
		actions, err = json.Marshal(r.Actions)
	}
	return name, conditions, actions, err
}

const triageRuleColumns = `id::text, space_id::text, name, position, enabled, conditions, actions, last_used_at, version`

func scanTriageRule(row pgx.Row) (TriageRule, error) {
	var rule TriageRule
	var conditions, actions []byte
	var version int64
	if err := row.Scan(&rule.ID, &rule.SpaceID, &rule.Name, &rule.Position, &rule.Enabled, &conditions, &actions, &rule.LastUsedAt, &version); err != nil {
		return TriageRule{}, err
	}
	rule.Version = uint64(version)
	if err := json.Unmarshal(conditions, &rule.Conditions); err != nil {
		return TriageRule{}, err
	}
	return rule, json.Unmarshal(actions, &rule.Actions)
}

func listTriageRules(ctx context.Context, tx pgx.Tx, spaceID string, enabledOnly bool) ([]TriageRule, error) {
	rows, err := tx.Query(ctx, `SELECT `+triageRuleColumns+` FROM openfox.triage_rules
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND (enabled OR NOT $2)
		ORDER BY position, created_at, id`, spaceID, enabledOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []TriageRule{}
	for rows.Next() {
		rule, err := scanTriageRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s Service) ListTriageRules(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) ([]TriageRule, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	return listTriageRules(ctx, tx, spaceID, false)
}

// lockTriageSpace 锁空间行，让同一空间的规则新建 / 删除 / 重排串行执行（条数上限与位置不会因并发出错）。
func lockTriageSpace(ctx context.Context, tx pgx.Tx, spaceID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM openfox.spaces
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL
		FOR UPDATE`, spaceID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceNotFound
	}
	return err
}

// triageRuleMissing 区分写入没有命中的原因：规则不存在 → 404，版本不对 → 409。
func triageRuleMissing(ctx context.Context, tx pgx.Tx, spaceID, ruleID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.triage_rules
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid)`,
		spaceID, ruleID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrVersionConflict
	}
	return errTriageRuleNotFound
}

func (s Service) CreateTriageRule(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input TriageRuleInput) (TriageRule, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TriageRule{}, err
	}
	name, conditions, actions, err := input.encode()
	if err != nil {
		return TriageRule{}, err
	}
	if err := lockTriageSpace(ctx, tx, spaceID); err != nil {
		return TriageRule{}, err
	}
	enabled := input.Enabled == nil || *input.Enabled
	rule, err := scanTriageRule(tx.QueryRow(ctx, `
		INSERT INTO openfox.triage_rules (organization_id, space_id, name, position, enabled, conditions, actions, created_by, updated_by)
		SELECT openfox_private.current_organization_id(), $1::uuid, $2, count(*), $3, $4::jsonb, $5::jsonb, $6::uuid, $6::uuid
		  FROM openfox.triage_rules
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		HAVING count(*) < $7
		RETURNING `+triageRuleColumns, spaceID, name, enabled, conditions, actions, actor.UserID, maxTriageRules))
	if errors.Is(err, pgx.ErrNoRows) {
		return TriageRule{}, errTriageRuleLimit
	}
	return rule, mapDatabaseError(err)
}

func (s Service) UpdateTriageRule(ctx context.Context, tx pgx.Tx, organizationID, spaceID, ruleID string, actor Actor, input TriageRuleInput) (TriageRule, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TriageRule{}, err
	}
	if !membership.ValidateUUID(ruleID) {
		return TriageRule{}, errTriageRuleNotFound
	}
	current, err := scanTriageRule(tx.QueryRow(ctx, `SELECT `+triageRuleColumns+` FROM openfox.triage_rules
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`, spaceID, ruleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TriageRule{}, errTriageRuleNotFound
	} else if err != nil {
		return TriageRule{}, err
	}
	if current.Version != input.Version {
		return TriageRule{}, ErrVersionConflict
	}
	if input.Name == "" {
		input.Name = current.Name
	}
	if input.Conditions == nil {
		input.Conditions = current.Conditions
	}
	if input.Actions == nil {
		input.Actions = current.Actions
	}
	if input.Enabled == nil {
		input.Enabled = &current.Enabled
	}
	name, conditions, actions, err := input.encode()
	if err != nil {
		return TriageRule{}, err
	}
	rule, err := scanTriageRule(tx.QueryRow(ctx, `
		UPDATE openfox.triage_rules
		   SET name = $4, enabled = $5, conditions = $6::jsonb, actions = $7::jsonb, updated_by = $8::uuid,
		       updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid AND version = $3
		RETURNING `+triageRuleColumns, spaceID, ruleID, int64(input.Version), name, *input.Enabled, conditions, actions, actor.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TriageRule{}, ErrVersionConflict
	}
	return rule, mapDatabaseError(err)
}

func (s Service) DeleteTriageRule(ctx context.Context, tx pgx.Tx, organizationID, spaceID, ruleID string, actor Actor, version uint64) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(ruleID) {
		return errTriageRuleNotFound
	}
	if err := lockTriageSpace(ctx, tx, spaceID); err != nil {
		return err
	}
	var position int
	err := tx.QueryRow(ctx, `DELETE FROM openfox.triage_rules
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid AND version = $3
		RETURNING position`, spaceID, ruleID, int64(version)).Scan(&position)
	if errors.Is(err, pgx.ErrNoRows) {
		return triageRuleMissing(ctx, tx, spaceID, ruleID)
	} else if err != nil {
		return err
	}
	// 后面的规则前移一位，位置保持 0..n-1，新建时直接取条数作为位置。
	_, err = tx.Exec(ctx, `UPDATE openfox.triage_rules SET position = position - 1
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND position > $2`, spaceID, position)
	return err
}

// ReorderTriageRules 按给定顺序重排；rule_ids 必须恰好是该空间的全部规则。
// 位置不属于规则内容，不递增 version，正在编辑规则的人不会因此冲突。
func (s Service) ReorderTriageRules(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, ruleIDs []string) ([]TriageRule, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if err := lockTriageSpace(ctx, tx, spaceID); err != nil {
		return nil, err
	}
	rules, err := listTriageRules(ctx, tx, spaceID, false)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(ruleIDs))
	for i, id := range ruleIDs {
		ids[i] = strings.ToLower(strings.TrimSpace(id))
	}
	existing := make([]string, len(rules))
	for i, rule := range rules {
		existing[i] = rule.ID
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	slices.Sort(existing)
	if !slices.Equal(sorted, existing) {
		return nil, errInvalidTriageRule
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.triage_rules r SET position = x.n - 1
		  FROM unnest($2::uuid[]) WITH ORDINALITY AS x(id, n)
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.space_id = $1::uuid AND r.id = x.id`,
		spaceID, ids); err != nil {
		return nil, err
	}
	return listTriageRules(ctx, tx, spaceID, false)
}

// triageCandidate 是规则匹配时看到的新任务（任务行尚未写入）。
type triageCandidate struct {
	source, priority, typeID, creatorID      string
	assigneeIDs, crewIDs, labelIDs           []string
	projectID, iterationID, title, desc, due string
}

func triageConditionSQL(q *taskQuery, c TriageCondition) string {
	var expr string
	switch c.Field {
	case "source", "priority":
		expr = "c." + c.Field + " = ANY(" + q.arg(c.Values) + "::text[])"
	case "type", "creator":
		expr = "c." + c.Field + "_id::text = ANY(" + q.arg(c.Values) + "::text[])"
	case "project", "iteration":
		list, column := q.arg(c.Values), "c."+c.Field+"_id"
		expr = "(" + column + " IS NULL AND 'none' = ANY(" + list + "::text[])) OR " + column + "::text = ANY(" + list + "::text[])"
	case "assignee", "crew":
		expr = "c." + c.Field + "_ids && " + q.arg(c.Values) + "::uuid[]"
	case "label":
		operator := "&&"
		if c.Op == "has_all" {
			operator = "@>"
		}
		expr = "c.label_ids " + operator + " " + q.arg(c.Values) + "::uuid[]"
	case "content":
		// ILIKE 的 % _ \ 已转义，用户输入只作为字面量匹配。
		value := escapeLike(c.Values[0])
		pattern := map[string]string{"contains": "%" + value + "%", "not_contains": "%" + value + "%",
			"starts_with": value + "%", "ends_with": "%" + value, "exactly": value}[c.Op]
		p := q.arg(pattern)
		expr = "c.title ILIKE " + p + " OR c.description ILIKE " + p
	case "due_date":
		switch c.Op {
		case "none":
			expr = "c.due_date IS NULL"
		case "before":
			expr = "c.due_date < " + q.arg(c.Values[0]) + "::date"
		case "after":
			expr = "c.due_date > " + q.arg(c.Values[0]) + "::date"
		case "between":
			expr = "c.due_date BETWEEN " + q.arg(c.Values[0]) + "::date AND " + q.arg(c.Values[1]) + "::date"
		}
	}
	if c.Op == "is_not" || c.Op == "has_none" || c.Op == "not_contains" {
		return "NOT COALESCE((" + expr + "), false)"
	}
	return "COALESCE((" + expr + "), false)"
}

// matchTriageRules 用一条查询判断每条规则是否命中：组内条件都满足（且），任一组满足即命中（或）；没有条件匹配所有任务。
func matchTriageRules(ctx context.Context, tx pgx.Tx, rules []TriageRule, c triageCandidate) ([]bool, error) {
	q := &taskQuery{}
	candidate := `SELECT ` + q.arg(c.source) + `::text AS source, ` + q.arg(c.priority) + `::text AS priority, ` +
		q.arg(c.typeID) + `::uuid AS type_id, ` + q.arg(c.creatorID) + `::uuid AS creator_id, ` +
		q.arg(c.assigneeIDs) + `::uuid[] AS assignee_ids, ` + q.arg(c.crewIDs) + `::uuid[] AS crew_ids, ` +
		q.arg(c.labelIDs) + `::uuid[] AS label_ids, NULLIF(` + q.arg(c.projectID) + `, '')::uuid AS project_id, NULLIF(` +
		q.arg(c.iterationID) + `, '')::uuid AS iteration_id, ` + q.arg(c.title) + `::text AS title, ` +
		q.arg(c.desc) + `::text AS description, NULLIF(` + q.arg(c.due) + `, '')::date AS due_date`
	matches := make([]string, len(rules))
	for i, rule := range rules {
		groups := []string{}
		for _, group := range rule.Conditions {
			conditions := []string{}
			for _, condition := range group.Conditions {
				conditions = append(conditions, triageConditionSQL(q, condition))
			}
			if len(conditions) > 0 {
				groups = append(groups, "("+strings.Join(conditions, " AND ")+")")
			}
		}
		matches[i] = "TRUE"
		if len(groups) > 0 {
			matches[i] = strings.Join(groups, " OR ")
		}
	}
	var matched []bool
	err := tx.QueryRow(ctx, `SELECT ARRAY[`+strings.Join(matches, ", ")+`]::boolean[] FROM (`+candidate+`) c`, q.args...).Scan(&matched)
	return matched, err
}

// ---- 设置与责任成员 ----

type TriageMember struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type TriageSettings struct {
	Enabled         bool           `json:"enabled"`
	RequirePriority bool           `json:"require_priority"`
	Action          string         `json:"action"`
	Members         []TriageMember `json:"members"`
	Rules           []TriageRule   `json:"rules"`
	CanManage       bool           `json:"can_manage"`
	Version         uint64         `json:"version"`
}

type TriagePatch struct {
	Version         uint64
	Enabled         *bool
	RequirePriority *bool
	Action          *string
}

type triageConfig struct {
	Enabled, RequirePriority bool
	Action                   string
	Version                  uint64
}

func loadTriageConfig(ctx context.Context, tx pgx.Tx, spaceID string) (triageConfig, error) {
	var cfg triageConfig
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT triage_enabled, triage_require_priority, triage_action, version FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`,
		spaceID).Scan(&cfg.Enabled, &cfg.RequirePriority, &cfg.Action, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, ErrSpaceNotFound
	}
	cfg.Version = uint64(version)
	return cfg, err
}

// categoryStatusID 返回分类中 position 最小的未归档状态；allowed 非空时只在其中（某个工作流）选。没有时返回空串。
func categoryStatusID(ctx context.Context, tx pgx.Tx, category string, allowed map[string]bool) (string, error) {
	var ids []string
	for id := range allowed {
		ids = append(ids, id)
	}
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND category = $1 AND archived_at IS NULL
		   AND ($2::uuid[] IS NULL OR id = ANY($2::uuid[]))
		 ORDER BY position, id LIMIT 1`, category, ids).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// activeTriageMembers 只返回仍是组织有效成员的责任成员，按加入顺序。
func activeTriageMembers(ctx context.Context, tx pgx.Tx, spaceID string) ([]TriageMember, error) {
	rows, err := tx.Query(ctx, `
		SELECT m.user_id::text, u.email FROM openfox.triage_members m
		  JOIN openfox.users u ON u.id = m.user_id
		  JOIN openfox.organization_members om ON om.organization_id = m.organization_id AND om.user_id = m.user_id
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.space_id = $1::uuid
		   AND om.status = 'ACTIVE'
		 ORDER BY m.created_at, m.user_id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []TriageMember{}
	for rows.Next() {
		var member TriageMember
		if err := rows.Scan(&member.ID, &member.Email); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// GetTriageSettings 由调用方完成空间读权限检查；canManage 来自同一次授权判断。
func (s Service) GetTriageSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, canManage bool) (TriageSettings, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TriageSettings{}, err
	}
	cfg, err := loadTriageConfig(ctx, tx, spaceID)
	if err != nil {
		return TriageSettings{}, err
	}
	members, err := activeTriageMembers(ctx, tx, spaceID)
	if err != nil {
		return TriageSettings{}, err
	}
	rules, err := listTriageRules(ctx, tx, spaceID, false)
	if err != nil {
		return TriageSettings{}, err
	}
	return TriageSettings{Enabled: cfg.Enabled, RequirePriority: cfg.RequirePriority, Action: cfg.Action,
		Members: members, Rules: rules, CanManage: canManage, Version: cfg.Version}, nil
}

// switchTriage 在启用 / 关闭分拣台前做检查与迁移；分拣设置页与空间功能开关都经过这里。
// 关闭时把空间里分拣中的任务改成各自工作流里的 BACKLOG 状态并写状态变更动态；有工作流缺 BACKLOG 状态时整体拒绝。
func switchTriage(ctx context.Context, tx pgx.Tx, spaceID string, enable bool, actor Actor) error {
	if enable {
		id, err := categoryStatusID(ctx, tx, "TRIAGE", nil)
		if err == nil && id == "" {
			err = ErrTriageStatusMissing
		}
		return err
	}
	const backlog = `(SELECT ws.status_id FROM openfox.workflow_statuses ws
		  JOIN openfox.task_statuses bs ON bs.organization_id = ws.organization_id AND bs.id = ws.status_id
		 WHERE ws.organization_id = k.organization_id AND ws.space_id = k.space_id AND ws.task_type_id = k.type_id
		   AND bs.category = 'BACKLOG' AND bs.archived_at IS NULL
		 ORDER BY bs.position, bs.id LIMIT 1)`
	const inTriage = `k.organization_id = openfox_private.current_organization_id() AND k.space_id = $1::uuid
		AND k.triage_entered_at IS NOT NULL AND k.deleted_at IS NULL`
	var missing bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.tasks k WHERE `+inTriage+` AND `+backlog+` IS NULL)`,
		spaceID).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return errStatusCategoryMissing
	}
	_, err := tx.Exec(ctx, `
		WITH moved AS (
			UPDATE openfox.tasks t
			   SET status_id = x.target, triage_entered_at = NULL, triage_source = NULL, triage_snoozed_until = NULL,
			       version = t.version + 1, updated_at = clock_timestamp()
			  FROM (SELECT k.id, k.status_id AS old_status, `+backlog+` AS target FROM openfox.tasks k WHERE `+inTriage+`) x
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = x.id
			RETURNING t.id, x.old_status, t.status_id, t.version)
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		SELECT openfox_private.current_organization_id(), $1::uuid, id, gen_random_uuid(), $2::uuid, 'task.status_changed',
		       jsonb_build_object('from_status_id', old_status::text, 'to_status_id', status_id::text),
		       'triage-off:' || id::text || ':' || version::text
		  FROM moved`, spaceID, actor.UserID)
	return mapDatabaseError(err)
}

func (s Service) UpdateTriageSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, patch TriagePatch) (TriageSettings, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TriageSettings{}, err
	}
	if _, _, err := lockLiveSpace(ctx, tx, spaceID, patch.Version); err != nil {
		return TriageSettings{}, err
	}
	cfg, err := loadTriageConfig(ctx, tx, spaceID)
	if err != nil {
		return TriageSettings{}, err
	}
	if patch.Enabled != nil && *patch.Enabled != cfg.Enabled {
		if err := switchTriage(ctx, tx, spaceID, *patch.Enabled, actor); err != nil {
			return TriageSettings{}, err
		}
		cfg.Enabled = *patch.Enabled
	}
	if patch.RequirePriority != nil {
		cfg.RequirePriority = *patch.RequirePriority
	}
	if patch.Action != nil {
		if *patch.Action != "none" && *patch.Action != "notify" && *patch.Action != "assign" {
			return TriageSettings{}, errInvalidTriageAction
		}
		cfg.Action = *patch.Action
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.spaces SET triage_enabled = $2, triage_require_priority = $3, triage_action = $4,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		spaceID, cfg.Enabled, cfg.RequirePriority, cfg.Action); err != nil {
		return TriageSettings{}, mapDatabaseError(err)
	}
	// 不再通知 / 指派时责任成员没有意义，一并清空（9.4）。
	if cfg.Action == "none" {
		if _, err := tx.Exec(ctx, `DELETE FROM openfox.triage_members
			WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID); err != nil {
			return TriageSettings{}, err
		}
	}
	return s.GetTriageSettings(ctx, tx, organizationID, spaceID, actor, true)
}

// SetTriageMembers 整体替换责任成员：必须是能被指派到该空间的非访客有效组织成员。
func (s Service) SetTriageMembers(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, userIDs []string) ([]TriageMember, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if len(userIDs) > maxTriageMembers {
		return nil, errInvalidTriageMember
	}
	ids := make([]string, len(userIDs))
	refs := make([]TaskAssigneeRef, len(userIDs))
	for i, raw := range userIDs {
		ids[i] = strings.ToLower(strings.TrimSpace(raw))
		if !membership.ValidateUUID(ids[i]) || slices.Contains(ids[:i], ids[i]) {
			return nil, errInvalidTriageMember
		}
		refs[i] = TaskAssigneeRef{Kind: "HUMAN", ID: ids[i]}
	}
	var guest bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.organization_members
		WHERE organization_id = openfox_private.current_organization_id() AND user_id = ANY($1::uuid[]) AND role = 'GUEST')`,
		ids).Scan(&guest); err != nil {
		return nil, err
	}
	if guest {
		return nil, errInvalidTriageMember
	}
	if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, refs); errors.Is(err, errAssigneeNotAllowed) {
		return nil, errInvalidTriageMember
	} else if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.triage_members
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID); err != nil {
		return nil, err
	}
	// clock_timestamp() 逐行递增，created_at 保留给定顺序（「指派」取第一个可指派的成员）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.triage_members (organization_id, space_id, user_id, created_at)
		SELECT openfox_private.current_organization_id(), $1::uuid, x.id, clock_timestamp()
		  FROM unnest($2::uuid[]) WITH ORDINALITY AS x(id, n) ORDER BY x.n`, spaceID, ids); err != nil {
		return nil, mapDatabaseError(err)
	}
	return activeTriageMembers(ctx, tx, spaceID)
}

// TriageCount 返回分拣台里的任务数与其中稍后处理未到期的数量。
func (s Service) TriageCount(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) (total, snoozed int, next *time.Time, err error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, 0, nil, err
	}
	err = tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE triage_snoozed_until > $2),min(triage_snoozed_until) FILTER (WHERE triage_snoozed_until>$2) FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND triage_entered_at IS NOT NULL AND deleted_at IS NULL`, spaceID, s.now()).Scan(&total, &snoozed, &next)
	return total, snoozed, next, err
}

// ---- 新任务进入分拣台 ----

type triageApplied struct{ RuleID, Name string }

// triageEntry 是新任务进入分拣台的结果；source 为空表示任务不在分拣台里（规则改了状态，或目标空间未启用分拣台）。
// 规则把任务移到别的空间时，同一个 entry 作为 TaskInput.triageCarry 带过去，不在目标空间重跑规则。
type triageEntry struct {
	source  string
	applied []triageApplied
	notify  []string
}

// triageDraft 是建任务过程中规则可以改写的字段，以及匹配规则时只读的字段。
type triageDraft struct {
	typeID, statusID, priority  string
	assignees                   []TaskAssigneeRef
	labelIDs                    []string
	projectID, iterationID      string
	title, description, dueDate string
	flow                        workflow
	leftTriage                  bool
}

func triageSourceOf(actor Actor) string {
	if actor.Role == policy.OrganizationRoleGuest {
		return "guest"
	}
	return "member"
}

// prepareTriage 在空间启用分拣台时决定新任务的状态，执行规则与责任动作。
// 规则把任务移到别的空间时，直接在目标空间创建并返回 moved。
func (s Service) prepareTriage(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input TaskInput, d *triageDraft) (*triageEntry, *Task, error) {
	cfg, err := loadTriageConfig(ctx, tx, spaceID)
	if err != nil {
		return nil, nil, err
	}
	if !cfg.Enabled {
		if input.triageCarry != nil {
			// 移入的空间没有分拣台：照常建任务，只保留已命中规则的动态。
			return &triageEntry{applied: input.triageCarry.applied}, nil, nil
		}
		return nil, nil, nil
	}
	triageStatus, err := categoryStatusID(ctx, tx, "TRIAGE", d.flow.statusIDs)
	if err != nil {
		return nil, nil, err
	}
	if !input.SubmitToTriage && input.triageCarry == nil && !input.integrationSource && actor.Role != policy.OrganizationRoleGuest && d.statusID != triageStatus {
		return nil, nil, nil
	}
	if triageStatus == "" {
		return nil, nil, ErrTriageStatusMissing
	}
	d.statusID = triageStatus
	entry := input.triageCarry
	if entry == nil {
		entry = &triageEntry{source: triageSourceOf(actor)}
		if input.integrationSource {
			entry.source = "integration"
		}
		moveTo, err := s.applyTriageRules(ctx, tx, spaceID, actor, entry, d)
		if err != nil {
			return nil, nil, err
		}
		if moveTo != "" {
			moved, err := s.moveTriageTask(ctx, tx, organizationID, moveTo, actor, input, entry, d)
			return nil, moved, err
		}
	}
	if d.leftTriage {
		entry.source = ""
		return entry, nil, nil
	}
	if cfg.Action == "none" {
		return entry, nil, nil
	}
	members, err := activeTriageMembers(ctx, tx, spaceID)
	if err != nil {
		return nil, nil, err
	}
	for _, member := range members {
		if cfg.Action == "notify" && member.ID != actor.UserID {
			entry.notify = append(entry.notify, member.ID)
		}
		if cfg.Action == "assign" && len(d.assignees) == 0 {
			refs := []TaskAssigneeRef{{Kind: "HUMAN", ID: member.ID}}
			if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, refs); err == nil {
				d.assignees = refs
			} else if !errors.Is(err, errAssigneeNotAllowed) {
				return nil, nil, err
			}
		}
	}
	return entry, nil, nil
}

// applyTriageRules 按顺序执行所有命中的启用规则：同一字段以后执行的为准，标签取并集。
// 规则引用的对象失效（标签被删、空间停用、人员无权限等）时跳过该动作并记日志，不让建任务失败。返回要移入的空间。
func (s Service) applyTriageRules(ctx context.Context, tx pgx.Tx, spaceID string, actor Actor, entry *triageEntry, d *triageDraft) (string, error) {
	rules, err := listTriageRules(ctx, tx, spaceID, true)
	if err != nil || len(rules) == 0 {
		return "", err
	}
	candidate := triageCandidate{source: entry.source, priority: d.priority, typeID: d.typeID, creatorID: actor.UserID,
		assigneeIDs: []string{}, crewIDs: []string{}, labelIDs: append([]string{}, d.labelIDs...),
		projectID: d.projectID, iterationID: d.iterationID, title: d.title, desc: d.description, due: d.dueDate}
	for _, ref := range d.assignees {
		if ref.Kind == "CREW" {
			candidate.crewIDs = append(candidate.crewIDs, ref.ID)
		} else {
			candidate.assigneeIDs = append(candidate.assigneeIDs, ref.ID)
		}
	}
	matched, err := matchTriageRules(ctx, tx, rules, candidate)
	if err != nil {
		return "", err
	}
	moveTo := ""
	usedIDs := []string{}
	for i, rule := range rules {
		if !matched[i] {
			continue
		}
		for _, action := range rule.Actions {
			// A live Issue link is scoped to its configured space. Do not forward repository data to another space.
			if entry.source == "integration" && action.Type == "move_space" {
				slog.Warn("triage_rule_action_skipped", "rule_id", rule.ID, "action", action.Type, "reason", "issue_link_space")
				continue
			}
			ok, err := s.applyTriageAction(ctx, tx, spaceID, actor, action, d, &moveTo)
			if err != nil {
				return "", err
			}
			if !ok {
				slog.Warn("triage_rule_action_skipped", "rule_id", rule.ID, "action", action.Type)
			}
		}
		entry.applied = append(entry.applied, triageApplied{RuleID: rule.ID, Name: rule.Name})
		usedIDs = append(usedIDs, rule.ID)
	}
	if len(usedIDs) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE openfox.triage_rules SET last_used_at = clock_timestamp()
			WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, usedIDs); err != nil {
			return "", err
		}
	}
	return moveTo, nil
}

// applyTriageAction 执行一个动作；返回 false 表示引用失效、动作被跳过。
func (s Service) applyTriageAction(ctx context.Context, tx pgx.Tx, spaceID string, actor Actor, action TriageAction, d *triageDraft, moveTo *string) (bool, error) {
	ids := triageActionIDs(action.Value)
	if len(ids) == 0 {
		return false, nil
	}
	switch action.Type {
	case "set_priority":
		if ids[0] == "LOWEST" {
			var levels int
			if err := tx.QueryRow(ctx, `SELECT priority_levels FROM openfox.organizations
				WHERE id = openfox_private.current_organization_id()`).Scan(&levels); err != nil {
				return false, err
			}
			if levels != 5 {
				return false, nil
			}
		}
		d.priority = ids[0]
	case "set_status":
		if !d.flow.statusIDs[ids[0]] {
			return false, nil
		}
		var category string
		if err := tx.QueryRow(ctx, `SELECT category FROM openfox.task_statuses
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND archived_at IS NULL`,
			ids[0]).Scan(&category); errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		d.statusID, d.leftTriage = ids[0], category != "TRIAGE"
	case "assign":
		refs := make([]TaskAssigneeRef, len(ids))
		for i, id := range ids {
			refs[i] = TaskAssigneeRef{Kind: "HUMAN", ID: id}
		}
		if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, refs); errors.Is(err, errAssigneeNotAllowed) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		d.assignees = refs
	case "add_labels":
		labels, err := usableLabelIDs(ctx, tx, spaceID, ids)
		if err != nil || len(labels) < len(ids) {
			return false, err
		}
		for _, id := range labels {
			if !slices.Contains(d.labelIDs, id) {
				d.labelIDs = append(d.labelIDs, id)
			}
		}
	case "set_project":
		if validateTaskProject(ctx, tx, spaceID, ids[0]) != nil {
			return false, nil
		}
		d.projectID = ids[0]
	case "move_space":
		// 目标须是创建者能写任务的另一个活跃空间：可读，且不是该空间的只读成员。
		var writable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.spaces s
			WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $2::uuid AND s.id <> $3::uuid
			  AND s.status = 'ACTIVE' AND `+SpaceReadableSQL+`
			  AND NOT EXISTS (SELECT 1 FROM openfox.space_members sm
			       WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
			         AND sm.user_id = openfox_private.current_actor_id() AND sm.role = 'VIEWER'))`,
			strings.ToUpper(string(actor.Role)), ids[0], spaceID).Scan(&writable); err != nil || !writable {
			return false, err
		}
		*moveTo = ids[0]
	}
	return true, nil
}

// usableLabelIDs 返回在空间里可用（该空间或组织级、未归档）的标签，保持给定顺序。
func usableLabelIDs(ctx context.Context, tx pgx.Tx, spaceID string, ids []string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.id::text FROM unnest($2::uuid[]) WITH ORDINALITY AS x(id, n)
		  JOIN openfox.labels l ON l.organization_id = openfox_private.current_organization_id() AND l.id = x.id
		 WHERE (l.space_id IS NULL OR l.space_id = $1::uuid) AND l.archived_at IS NULL
		 ORDER BY x.n`, spaceID, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// moveTriageTask 按规则把任务建到目标空间：原空间专属的类型、状态、父任务、字段值不带过去，
// 处理人、标签、专项在目标空间不可用时丢弃。
func (s Service) moveTriageTask(ctx context.Context, tx pgx.Tx, organizationID, targetID string, actor Actor, input TaskInput, entry *triageEntry, d *triageDraft) (*Task, error) {
	moved := TaskInput{
		ID: input.ID, Title: input.Title, Description: input.Description, Acceptance: input.Acceptance,
		Priority: d.priority, StartDate: input.StartDate, DueDate: input.DueDate, Estimate: input.Estimate,
		RequestID: input.RequestID, triageCarry: entry,
	}
	if len(d.assignees) > 0 {
		if err := s.validateAssigneeRefs(ctx, tx, targetID, actor, d.assignees); err == nil {
			moved.Assignees = &d.assignees
		} else if !errors.Is(err, errAssigneeNotAllowed) {
			return nil, err
		}
	}
	if len(d.labelIDs) > 0 {
		labels, err := usableLabelIDs(ctx, tx, targetID, d.labelIDs)
		if err != nil {
			return nil, err
		}
		moved.LabelIDs = &labels
	}
	if d.projectID != "" && validateTaskProject(ctx, tx, targetID, d.projectID) == nil {
		moved.ProjectID = d.projectID
	}
	task, err := s.CreateTask(ctx, tx, organizationID, targetID, actor, moved)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// finishTriage 在任务行写入后记录命中的规则，并通知责任成员。
func (s Service) finishTriage(ctx context.Context, tx pgx.Tx, taskID string, w *activityWriter, entry *triageEntry) error {
	if entry == nil {
		return nil
	}
	for _, rule := range entry.applied {
		if err := w.add(ctx, "triage.rule_applied", map[string]any{"rule_id": rule.RuleID, "name": rule.Name}); err != nil {
			return err
		}
	}
	for _, userID := range entry.notify {
		if _, err := tx.Exec(ctx, `
			SELECT openfox_private.enqueue_notification(
				$1::uuid, 'TRIAGE_NEW', NULL::uuid, NULL::uuid, $2::uuid, NULL::uuid,
				$3, 'A new task entered triage.')`,
			userID, taskID, fmt.Sprintf("triage:%s:%s", taskID, userID)); err != nil {
			return err
		}
	}
	return nil
}

// ---- 分拣动作 ----

type TriageAcceptInput struct {
	FieldValues map[string]json.RawMessage
	Assignees   *[]TaskAssigneeRef
	ProjectID   OptionalString
	IterationID OptionalString
	Estimate    OptionalFloat
	StartDate   OptionalString
	DueDate     OptionalString
	Version     uint64
	StatusID    *string
	AssigneeIDs *[]string
	Priority    *string
	LabelIDs    *[]string
	RequestID   string
}

// triageTask 读取任务并确认它在分拣台里。
func (s Service) triageTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor) (Task, error) {
	task, err := s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
	if err == nil && task.TriageEnteredAt == nil {
		err = errNotInTriage
	}
	return task, err
}

func (s Service) AcceptTriage(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, in TriageAcceptInput) (Task, error) {
	task, err := s.triageTask(ctx, tx, organizationID, spaceID, taskID, actor)
	if err != nil {
		return Task{}, err
	}
	statusID := ""
	if in.StatusID != nil {
		statusID = strings.ToLower(strings.TrimSpace(*in.StatusID))
	}
	if statusID == "" {
		if statusID, err = OrganizationDefaultStatusID(ctx, tx); errors.Is(err, ErrStatusNotFound) {
			return Task{}, errStatusCategoryMissing
		} else if err != nil {
			return Task{}, err
		}
	} else {
		var category string
		if !membership.ValidateUUID(statusID) {
			return Task{}, errInvalidAcceptStatus
		}
		err := tx.QueryRow(ctx, `SELECT category FROM openfox.task_statuses
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, statusID).Scan(&category)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && category == "TRIAGE") {
			return Task{}, errInvalidAcceptStatus
		} else if err != nil {
			return Task{}, err
		}
	}
	priority := task.Priority
	if in.Priority != nil {
		if priority, err = ParsePriority(*in.Priority); err != nil {
			return Task{}, err
		}
	}
	cfg, err := loadTriageConfig(ctx, tx, spaceID)
	if err != nil {
		return Task{}, err
	}
	if cfg.RequirePriority && priority == "NONE" {
		return Task{}, errPriorityRequired
	}
	update := TaskUpdateInput{StatusID: &statusID, Priority: in.Priority, LabelIDs: in.LabelIDs, Version: in.Version, RequestID: in.RequestID,
		Assignees: in.Assignees, ProjectID: in.ProjectID, IterationID: in.IterationID, Estimate: in.Estimate, StartDate: in.StartDate, DueDate: in.DueDate}
	if in.Assignees != nil && in.AssigneeIDs != nil {
		return Task{}, invalidInput("specify only one assignee format")
	}
	if in.AssigneeIDs != nil {
		refs := make([]TaskAssigneeRef, len(*in.AssigneeIDs))
		for i, id := range *in.AssigneeIDs {
			refs[i] = TaskAssigneeRef{Kind: "HUMAN", ID: id}
		}
		update.Assignees = &refs
	}
	updated, err := s.UpdateTask(ctx, tx, organizationID, spaceID, taskID, actor, update)
	if err == nil && len(in.FieldValues) != 0 {
		w := newActivityWriter(tx, spaceID, taskID, actor.UserID, in.RequestID+":fields")
		err = s.writeBoundFieldValues(ctx, tx, w, organizationID, spaceID, taskID, task.TypeID, actor, in.FieldValues)
	}
	return updated, err
}

func (s Service) DeclineTriage(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, version uint64, requestID string) (Task, error) {
	return s.leaveTriage(ctx, tx, organizationID, spaceID, taskID, actor, version, requestID, "CANCELLED", "")
}

// MarkTriageDuplicate 把分拣中的任务标记为目标任务的重复：目标须同组织、可读、未删除、不是自己。
func (s Service) MarkTriageDuplicate(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, version uint64, requestID, targetID string) (Task, error) {
	targetID = strings.ToLower(strings.TrimSpace(targetID))
	if !membership.ValidateUUID(targetID) || targetID == taskID {
		return Task{}, errInvalidDuplicate
	}
	return s.leaveTriage(ctx, tx, organizationID, spaceID, taskID, actor, version, requestID, "DUPLICATE", targetID)
}

func (s Service) leaveTriage(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, version uint64, requestID, category, duplicateOf string) (Task, error) {
	task, err := s.triageTask(ctx, tx, organizationID, spaceID, taskID, actor)
	if err != nil {
		return Task{}, err
	}
	var targetSpace string
	if duplicateOf != "" {
		err := tx.QueryRow(ctx, `
			SELECT d.space_id::text FROM openfox.tasks d
			  JOIN openfox.spaces s ON s.organization_id = d.organization_id AND s.id = d.space_id
			 WHERE d.organization_id = openfox_private.current_organization_id() AND d.id = $2::uuid
			   AND d.deleted_at IS NULL AND `+SpaceReadableSQL, strings.ToUpper(string(actor.Role)), duplicateOf).Scan(&targetSpace)
		if errors.Is(err, pgx.ErrNoRows) {
			return Task{}, errInvalidDuplicate
		} else if err != nil {
			return Task{}, err
		}
	}
	flow, err := loadWorkflow(ctx, tx, organizationID, task.SpaceID, task.TypeID)
	if err != nil {
		return Task{}, err
	}
	statusID, err := categoryStatusID(ctx, tx, category, flow.statusIDs)
	if err != nil {
		return Task{}, err
	}
	if statusID == "" {
		return Task{}, errStatusCategoryMissing
	}
	updated, err := s.UpdateTask(ctx, tx, organizationID, spaceID, taskID, actor, TaskUpdateInput{StatusID: &statusID, Version: version, RequestID: requestID})
	if err != nil || duplicateOf == "" {
		return updated, err
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.tasks SET duplicate_of_task_id = $2::uuid
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, taskID, duplicateOf); err != nil {
		return Task{}, mapDatabaseError(err)
	}
	// 同一请求已经给本任务写过动态（<requestID>:<序号>），目标任务的动态用独立前缀避免 source_id 冲突。
	if err := newActivityWriter(tx, targetSpace, duplicateOf, actor.UserID, requestID+":duplicate").add(ctx, "task.duplicate_marked",
		map[string]any{"task_id": taskID, "display_id": task.DisplayID}); err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
}

// SnoozeTriage 设置或取消（until 为空）稍后处理；到期前任务不在队列默认视图里显示。
func (s Service) SnoozeTriage(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, until *time.Time) (Task, error) {
	now := s.now()
	if until != nil && (!until.After(now) || until.After(now.AddDate(1, 0, 0))) {
		return Task{}, errInvalidSnooze
	}
	if _, err := s.triageTask(ctx, tx, organizationID, spaceID, taskID, actor); err != nil {
		return Task{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks SET triage_snoozed_until = $3, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`,
		spaceID, taskID, until); err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
}
