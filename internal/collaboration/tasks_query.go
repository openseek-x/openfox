package collaboration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 任务列表的筛选、排序、keyset 游标与计数，空间任务列表和「我的任务」共用同一套构造器。

var (
	errInvalidFilter = NewRuleError(400, "INVALID_FILTER", "task filter is invalid")
	errInvalidCursor = NewRuleError(400, "INVALID_CURSOR", "cursor is invalid or does not match the sort")
)

const (
	maxFilterValues = 50

	// 任务所属专项的名称，无专项时为空串；专项排序与列表扫描共用。
	projectNameSQL = `COALESCE((SELECT pr.name FROM openfox.projects pr WHERE pr.organization_id = t.organization_id AND pr.id = t.project_id), '')`

	// 有效排序键：没有 rank 的旧任务按 sequence 补零排序，与 tasks_board_idx 的表达式一致。
	effectiveRankSQL = `(COALESCE(t.rank, 'h' || lpad(t.sequence::text, 12, '0')) COLLATE "C")`

	// 任务列表通用的列与来源，scanTask 按这个顺序读取。
	taskSelectColumns = `t.organization_id::text, t.space_id::text, s.prefix, t.id::text,
		       t.sequence, t.type_id::text, tt.name, t.status_id::text,
		       ts.name, ts.category, t.title, t.description, t.acceptance,
		       COALESCE(t.assignee_id::text, ''), COALESCE(t.executor_agent_id::text, ''),
		       t.priority, t.version, t.created_at, t.updated_at,
		       COALESCE(to_char(t.start_date, 'YYYY-MM-DD'), ''), COALESCE(to_char(t.due_date, 'YYYY-MM-DD'), ''),
			COALESCE(p.id::text, ''), COALESCE(p.title, ''), COALESCE(p.sequence, 0),
			` + effectiveRankSQL + `, COALESCE(tt.icon, ''), COALESCE(ts.color, ''), t.estimate::float8,
			COALESCE(t.iteration_id::text, ''), COALESCE((SELECT i.number FROM openfox.iterations i WHERE i.organization_id = t.organization_id AND i.id = t.iteration_id), 0),
			COALESCE(t.project_id::text, ''), ` + projectNameSQL + `,
			t.triage_entered_at, COALESCE(t.triage_source, ''), t.triage_snoozed_until, COALESCE(t.duplicate_of_task_id::text, '')`
	taskSelectFrom = `FROM openfox.tasks t
		JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		JOIN openfox.task_types tt ON tt.organization_id = t.organization_id AND tt.id = t.type_id
		JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
		LEFT JOIN openfox.tasks p
		  ON p.organization_id = t.organization_id AND p.id = t.parent_task_id AND p.deleted_at IS NULL`
)

// TaskFilter 是列表与计数共用的筛选条件。同一字段多个取值之间是「任一」，「不是」表示都不匹配。
type TaskFilter struct {
	triageFacets              triageFacets
	StatusIDs, StatusIDsNot   []string
	TypeIDs, TypeIDsNot       []string
	Priorities, PrioritiesNot []string
	Assignees, AssigneesNot   []string // user:<uuid> / agent:<uuid> / crew:<uuid> / none
	LabelIDs, LabelIDsNot     []string
	CreatorIDs, CreatorIDsNot []string
	Categories                []string
	Query, QueryNot           string
	StartFrom, StartTo        string
	DueFrom, DueTo            string
	// CreatedViaAgent 是 Agent 通过空间工具创建的任务（task.via_agent 动态 tool=space_task_create 的 agent_id）。
	// task.created 的操作人是 Run 发起人，所以按 Agent 查「已创建」不能用 CreatorIDs。
	CreatedViaAgent []string
	// StartNot / DueNot 把对应日期区间取反：日期为空或落在区间外的任务。
	StartNot, DueNot bool
	ParentTaskID     string
	ExcludeSubtasks  bool
	HideCompleted    bool
	// EstimateMin / EstimateMax 为估时区间（含端点）；EstimateNone 只看未估时任务，与区间同时给出时取并集。
	EstimateMin, EstimateMax      *float64
	EstimateNone                  bool
	IterationIDs, IterationIDsNot []string
	ProjectIDs, ProjectIDsNot     []string
	// TriageQueue 只看分拣台里的任务；IncludeSnoozed 为 false 时隐藏稍后处理未到期的。
	TriageQueue, IncludeSnoozed bool
	// Also 是打开视图时附加的视图筛选，与本筛选取交集（AND）。
	Also        *TaskFilter
	Sort, Order string
}

// ParseTaskFilter 解析查询参数。旧参数 assignee_id 视为 assignee=user:<id>，priority 可重复。
func ParseTaskFilter(values url.Values) (TaskFilter, error) {
	return parseTaskFilter(values, false)
}

func parseTaskFilter(values url.Values, allowInvitation bool) (TaskFilter, error) {
	var filter TaskFilter
	var err error
	uuids := func(key string) []string {
		if err != nil {
			return nil
		}
		var list []string
		list, err = uuidList(values[key])
		return list
	}
	filter.StatusIDs, filter.StatusIDsNot = uuids("status_id"), uuids("status_id_not")
	filter.TypeIDs, filter.TypeIDsNot = uuids("type_id"), uuids("type_id_not")
	keywords := func(key string, allowed ...string) []string {
		if err != nil {
			return nil
		}
		var list []string
		list, err = keywordList(values[key], allowed...)
		return list
	}
	filter.IterationIDs, filter.IterationIDsNot = keywords("iteration_id", "none", "current"), keywords("iteration_id_not", "none", "current")
	filter.ProjectIDs, filter.ProjectIDsNot = keywords("project_id", "none"), keywords("project_id_not", "none")
	filter.LabelIDs, filter.LabelIDsNot = uuids("label_id"), uuids("label_id_not")
	filter.CreatorIDs, filter.CreatorIDsNot = uuids("creator_id"), uuids("creator_id_not")
	filter.CreatedViaAgent = uuids("created_via_agent")
	if err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	if filter.Priorities, err = priorityList(values["priority"]); err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	if filter.PrioritiesNot, err = priorityList(values["priority_not"]); err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	assignees := values["assignee"]
	if legacy := strings.TrimSpace(values.Get("assignee_id")); legacy != "" {
		assignees = append(append([]string(nil), assignees...), "user:"+legacy)
	}
	if filter.Assignees, err = assigneeList(assignees, allowInvitation); err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	if filter.AssigneesNot, err = assigneeList(values["assignee_not"], allowInvitation); err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	for _, raw := range values["category"] {
		category, parseErr := ParseCategory(raw)
		if parseErr != nil {
			return TaskFilter{}, errInvalidFilter
		}
		filter.Categories = append(filter.Categories, category)
	}
	filter.Query = strings.TrimSpace(values.Get("q"))
	filter.QueryNot = strings.TrimSpace(values.Get("q_not"))
	if len([]rune(filter.Query)) > 100 || len([]rune(filter.QueryNot)) > 100 {
		return TaskFilter{}, errInvalidFilter
	}
	for key, target := range map[string]*bool{"start_not": &filter.StartNot, "due_not": &filter.DueNot, "estimate_none": &filter.EstimateNone} {
		switch strings.ToLower(strings.TrimSpace(values.Get(key))) {
		case "", "false":
		case "true":
			*target = true
		default:
			return TaskFilter{}, errInvalidFilter
		}
	}
	for key, target := range map[string]*string{
		"start_from": &filter.StartFrom, "start_to": &filter.StartTo,
		"due_from": &filter.DueFrom, "due_to": &filter.DueTo,
	} {
		value := strings.TrimSpace(values.Get(key))
		if value != "" {
			if _, parseErr := time.Parse("2006-01-02", value); parseErr != nil {
				return TaskFilter{}, errInvalidFilter
			}
		}
		*target = value
	}
	if filter.EstimateMin, err = parseEstimateBound(values.Get("estimate_min")); err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	if filter.EstimateMax, err = parseEstimateBound(values.Get("estimate_max")); err != nil {
		return TaskFilter{}, errInvalidFilter
	}
	if filter.ParentTaskID = strings.TrimSpace(values.Get("parent_task_id")); filter.ParentTaskID != "" && !membership.ValidateUUID(filter.ParentTaskID) {
		return TaskFilter{}, errInvalidFilter
	}
	switch strings.ToLower(strings.TrimSpace(values.Get("include_subtasks"))) {
	case "", "true":
	case "false":
		filter.ExcludeSubtasks = true
	default:
		return TaskFilter{}, errInvalidFilter
	}
	switch strings.ToLower(strings.TrimSpace(values.Get("completed"))) {
	case "", "all":
	case "hide":
		filter.HideCompleted = true
	default:
		return TaskFilter{}, errInvalidFilter
	}
	filter.Sort = strings.ToLower(strings.TrimSpace(values.Get("sort")))
	filter.Order = strings.ToLower(strings.TrimSpace(values.Get("order")))
	return filter, nil
}

// keywordList 解析「UUID 或固定关键字（none / current）」的多值参数。
func keywordList(raw []string, allowed ...string) ([]string, error) {
	if len(raw) > maxFilterValues {
		return nil, errInvalidFilter
	}
	list := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.ToLower(strings.TrimSpace(value))
		if !membership.ValidateUUID(value) && !slices.Contains(allowed, value) {
			return nil, errInvalidFilter
		}
		list = append(list, value)
	}
	return list, nil
}

func uuidList(raw []string) ([]string, error) {
	if len(raw) > maxFilterValues {
		return nil, errInvalidFilter
	}
	list := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.ToLower(strings.TrimSpace(value))
		if !membership.ValidateUUID(value) {
			return nil, errInvalidFilter
		}
		list = append(list, value)
	}
	return list, nil
}

func priorityList(raw []string) ([]string, error) {
	if len(raw) > maxFilterValues {
		return nil, errInvalidFilter
	}
	list := make([]string, 0, len(raw))
	for _, value := range raw {
		parsed, err := ParsePriority(value)
		if err != nil {
			return nil, errInvalidFilter
		}
		list = append(list, parsed)
	}
	return list, nil
}

func assigneeList(raw []string, allowInvitation bool) ([]string, error) {
	if len(raw) > maxFilterValues {
		return nil, errInvalidFilter
	}
	list := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "none" {
			list = append(list, value)
			continue
		}
		kind, id, found := strings.Cut(value, ":")
		if !found || (kind != "user" && kind != "agent" && kind != "crew" && !(allowInvitation && kind == "invitation")) || !membership.ValidateUUID(id) {
			return nil, errInvalidFilter
		}
		list = append(list, value)
	}
	return list, nil
}

type taskQuery struct {
	args []any
}

func (q *taskQuery) arg(value any) string {
	q.args = append(q.args, value)
	return "$" + strconv.Itoa(len(q.args))
}

// assigneeMatchSQL 生成「处理人命中」条件：优先看 task_assignees，没有行的旧任务看投影列。
func assigneeMatchSQL(q *taskQuery, values []string) string {
	var users, agents, crews, invitations []string
	none := false
	for _, value := range values {
		kind, id, _ := strings.Cut(value, ":")
		switch kind {
		case "user":
			users = append(users, id)
		case "agent":
			agents = append(agents, id)
		case "crew":
			crews = append(crews, id)
		case "invitation":
			invitations = append(invitations, id)
		default:
			none = true
		}
	}
	hasRows := `EXISTS (SELECT 1 FROM openfox.task_assignees x WHERE x.organization_id = t.organization_id AND x.task_id = t.id)`
	var parts []string
	if len(users) > 0 {
		p := q.arg(users)
		parts = append(parts, `(EXISTS (SELECT 1 FROM openfox.task_assignees x WHERE x.organization_id = t.organization_id
		   AND x.task_id = t.id AND x.user_id = ANY(`+p+`::uuid[])) OR (t.assignee_id = ANY(`+p+`::uuid[]) AND NOT `+hasRows+`))`)
	}
	if len(agents) > 0 {
		p := q.arg(agents)
		parts = append(parts, `(EXISTS (SELECT 1 FROM openfox.task_assignees x WHERE x.organization_id = t.organization_id
		   AND x.task_id = t.id AND x.agent_id = ANY(`+p+`::uuid[])) OR (t.executor_agent_id = ANY(`+p+`::uuid[]) AND NOT `+hasRows+`))`)
	}
	if len(crews) > 0 {
		p := q.arg(crews)
		parts = append(parts, `EXISTS (SELECT 1 FROM openfox.task_assignees x WHERE x.organization_id = t.organization_id
		   AND x.task_id = t.id AND x.crew_id = ANY(`+p+`::uuid[]))`)
	}
	const pendingInvitation = `SELECT 1 FROM openfox.task_invitation_assignees a JOIN openfox.invitations i
		ON i.organization_id=a.organization_id AND i.id=a.invitation_id
		WHERE a.organization_id=t.organization_id AND a.task_id=t.id AND i.status='PENDING' AND i.expires_at>statement_timestamp()`
	if len(invitations) > 0 {
		parts = append(parts, "EXISTS ("+pendingInvitation+" AND i.id=ANY("+q.arg(invitations)+"::uuid[]))")
	}
	if none {
		parts = append(parts, `(NOT `+hasRows+` AND t.assignee_id IS NULL AND t.executor_agent_id IS NULL AND NOT EXISTS (`+pendingInvitation+`))`)
	}
	// 投影列为 NULL 时比较结果是 NULL，「不是」取反后会把这些任务也排除掉；统一折叠成 false。
	return "COALESCE((" + strings.Join(parts, " OR ") + "), false)"
}

func (f TaskFilter) conditions(q *taskQuery) []string {
	var conds []string
	inList := func(column string, include, exclude []string, cast string) {
		if len(include) > 0 {
			conds = append(conds, fmt.Sprintf("%s = ANY(%s::%s[])", column, q.arg(include), cast))
		}
		if len(exclude) > 0 {
			conds = append(conds, fmt.Sprintf("NOT (%s = ANY(%s::%s[]))", column, q.arg(exclude), cast))
		}
	}
	inList("t.status_id", f.StatusIDs, f.StatusIDsNot, "uuid")
	inList("t.type_id", f.TypeIDs, f.TypeIDsNot, "uuid")
	inList("t.priority", f.Priorities, f.PrioritiesNot, "text")
	labelExists := func(p string) string {
		return `EXISTS (SELECT 1 FROM openfox.task_labels tl WHERE tl.organization_id = t.organization_id
		   AND tl.task_id = t.id AND tl.label_id = ANY(` + p + `::uuid[]))`
	}
	if len(f.LabelIDs) > 0 {
		conds = append(conds, labelExists(q.arg(f.LabelIDs)))
	}
	if len(f.LabelIDsNot) > 0 {
		conds = append(conds, "NOT "+labelExists(q.arg(f.LabelIDsNot)))
	}
	creatorExists := func(p string) string {
		return `EXISTS (SELECT 1 FROM openfox.task_activities a WHERE a.organization_id = t.organization_id
		   AND a.task_id = t.id AND a.kind = 'task.created' AND a.actor_id = ANY(` + p + `::uuid[]))`
	}
	if len(f.CreatorIDs) > 0 {
		conds = append(conds, creatorExists(q.arg(f.CreatorIDs)))
	}
	if len(f.CreatorIDsNot) > 0 {
		conds = append(conds, "NOT "+creatorExists(q.arg(f.CreatorIDsNot)))
	}
	if len(f.CreatedViaAgent) > 0 {
		// 只算创建：space_task_update 写的 via_agent 动态不算
		conds = append(conds, `EXISTS (SELECT 1 FROM openfox.task_activities a WHERE a.organization_id = t.organization_id
		   AND a.task_id = t.id AND a.kind = 'task.via_agent' AND a.safe_diff->>'tool' = 'space_task_create'
		   AND a.safe_diff->>'agent_id' = ANY(`+q.arg(f.CreatedViaAgent)+`::text[]))`)
	}
	if len(f.Assignees) > 0 {
		conds = append(conds, assigneeMatchSQL(q, f.Assignees))
	}
	if len(f.AssigneesNot) > 0 {
		conds = append(conds, "NOT "+assigneeMatchSQL(q, f.AssigneesNot))
	}
	// 迭代与专项同形：UUID / none（未设置）/ 仅迭代支持的 current；「不是」要把未设置（NULL）的任务保留下来。
	refFilter := func(column string, include, exclude []string, current string) {
		match := func(values []string) string {
			parts := make([]string, 0, len(values))
			for _, value := range values {
				switch value {
				case "none":
					parts = append(parts, column+" IS NULL")
				case "current":
					parts = append(parts, current)
				default:
					parts = append(parts, column+" = "+q.arg(value)+"::uuid")
				}
			}
			return "(" + strings.Join(parts, " OR ") + ")"
		}
		if len(include) > 0 {
			conds = append(conds, match(include))
		}
		if len(exclude) > 0 {
			conds = append(conds, "NOT COALESCE("+match(exclude)+", FALSE)")
		}
	}
	refFilter("t.iteration_id", f.IterationIDs, f.IterationIDsNot,
		`(t.iteration_id IS NOT NULL AND EXISTS (SELECT 1 FROM openfox.iterations ci WHERE ci.organization_id = t.organization_id AND ci.id = t.iteration_id AND ci.status = 'active'))`)
	refFilter("t.project_id", f.ProjectIDs, f.ProjectIDsNot, "")
	if len(f.Categories) > 0 {
		conds = append(conds, "ts.category = ANY("+q.arg(f.Categories)+"::text[])")
	}
	if f.HideCompleted {
		conds = append(conds, "ts.category NOT IN ('DONE', 'CANCELLED', 'DUPLICATE')")
	}
	if f.Query != "" {
		p := q.arg("%" + escapeLike(f.Query) + "%")
		if prefix, sequence, ok := splitDisplayID(f.Query); ok {
			// 输入形如 ENG-12 时同时按编号命中，含空间的历史标识符。
			pp, ps := q.arg(prefix), q.arg(sequence)
			conds = append(conds, fmt.Sprintf(`(t.title ILIKE %s OR t.description ILIKE %s OR (t.sequence = %s::bigint AND (
				lower(s.prefix) = lower(%s) OR EXISTS (SELECT 1 FROM openfox.space_prefix_history h
				 WHERE h.organization_id = s.organization_id AND h.space_id = s.id AND lower(h.prefix) = lower(%s)))))`,
				p, p, ps, pp, pp))
		} else {
			conds = append(conds, fmt.Sprintf("(t.title ILIKE %s OR t.description ILIKE %s)", p, p))
		}
	}
	if f.QueryNot != "" {
		p := q.arg("%" + escapeLike(f.QueryNot) + "%")
		conds = append(conds, fmt.Sprintf("NOT (t.title ILIKE %s OR t.description ILIKE %s)", p, p))
	}
	dateRange := func(column, from, to string, negate bool) {
		var parts []string
		if from != "" {
			parts = append(parts, fmt.Sprintf("%s >= %s::date", column, q.arg(from)))
		}
		if to != "" {
			parts = append(parts, fmt.Sprintf("%s <= %s::date", column, q.arg(to)))
		}
		if len(parts) == 0 {
			return
		}
		match := "(" + strings.Join(parts, " AND ") + ")"
		if negate {
			// 日期为空时比较结果是 NULL，折叠成 false 后取反，空日期算作「不在区间内」。
			match = "NOT COALESCE(" + match + ", false)"
		}
		conds = append(conds, match)
	}
	dateRange("t.start_date", f.StartFrom, f.StartTo, f.StartNot)
	dateRange("t.due_date", f.DueFrom, f.DueTo, f.DueNot)
	var estimateParts []string
	if f.EstimateMin != nil {
		estimateParts = append(estimateParts, "t.estimate >= "+q.arg(*f.EstimateMin)+"::float8")
	}
	if f.EstimateMax != nil {
		estimateParts = append(estimateParts, "t.estimate <= "+q.arg(*f.EstimateMax)+"::float8")
	}
	switch {
	case len(estimateParts) > 0 && f.EstimateNone:
		conds = append(conds, "(t.estimate IS NULL OR ("+strings.Join(estimateParts, " AND ")+"))")
	case len(estimateParts) > 0:
		conds = append(conds, "("+strings.Join(estimateParts, " AND ")+")")
	case f.EstimateNone:
		conds = append(conds, "t.estimate IS NULL")
	}
	if f.ParentTaskID != "" {
		conds = append(conds, "t.parent_task_id = "+q.arg(f.ParentTaskID)+"::uuid")
	} else if f.ExcludeSubtasks {
		conds = append(conds, "t.parent_task_id IS NULL")
	}
	if f.TriageQueue {
		conds = append(conds, f.triageFacets.conditions(q)...)
		conds = append(conds, "t.triage_entered_at IS NOT NULL")
		if !f.IncludeSnoozed {
			conds = append(conds, "(t.triage_snoozed_until IS NULL OR t.triage_snoozed_until <= clock_timestamp())")
		}
	}
	if f.Also != nil {
		conds = append(conds, f.Also.conditions(q)...)
	}
	return conds
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

type taskSort struct {
	workbench   bool
	name, order string
	expr, cast  string
}

const priorityRankSQL = `(CASE t.priority WHEN 'URGENT' THEN 5 WHEN 'HIGH' THEN 4 WHEN 'NORMAL' THEN 3 WHEN 'LOW' THEN 2 WHEN 'LOWEST' THEN 1 ELSE 0 END)`

func resolveTaskSort(sortName, order, defaultSort, defaultOrder string) (taskSort, error) {
	if sortName == "" {
		sortName = defaultSort
		if order == "" {
			order = defaultOrder
		}
	}
	if order == "" {
		order = "asc"
	}
	if order != "asc" && order != "desc" {
		return taskSort{}, errInvalidFilter
	}
	result := taskSort{name: sortName, order: order}
	switch sortName {
	case "manual":
		if order != "asc" {
			return taskSort{}, errInvalidFilter
		}
		result.expr, result.cast = effectiveRankSQL, "text"
	case "created":
		result.expr, result.cast = "t.created_at", "timestamptz"
	case "updated":
		result.expr, result.cast = "t.updated_at", "timestamptz"
	case "triage_entered":
		result.expr, result.cast = "t.triage_entered_at", "timestamptz"
	case "priority":
		result.expr, result.cast = priorityRankSQL, "int"
	case "due":
		sentinel := "'~'"
		if order == "desc" {
			sentinel = "''"
		}
		result.expr, result.cast = `(COALESCE(to_char(t.due_date, 'YYYY-MM-DD'), `+sentinel+`) COLLATE "C")`, "text"
	case "estimate":
		// 未估时排最后：升序时用大于上限的哨兵，降序时用 -1。
		result.expr, result.cast = "COALESCE(t.estimate, "+estimateNullSentinel(order)+")", "numeric"
	case "project":
		// 无专项的任务排最后：名称按字节序比较，哨兵取最大码点（降序时取空串）。
		sentinel := "chr(1114111)"
		if order == "desc" {
			sentinel = "''"
		}
		result.expr, result.cast = `(COALESCE(NULLIF(`+projectNameSQL+`, ''), `+sentinel+`) COLLATE "C")`, "text"
	case "iteration":
		result.expr, result.cast = "COALESCE((SELECT i.number FROM openfox.iterations i WHERE i.organization_id = t.organization_id AND i.id = t.iteration_id), 2147483647)", "int"
	default:
		return taskSort{}, errInvalidFilter
	}
	return result, nil
}

// estimateNullSentinel 是估时排序中未估时任务的取值，保证空值总排在最后。
func estimateNullSentinel(order string) string {
	if order == "desc" {
		return "-1"
	}
	return "10000000000"
}

func priorityOrdinal(priority string) int {
	switch priority {
	case "URGENT":
		return 5
	case "HIGH":
		return 4
	case "NORMAL":
		return 3
	case "LOW":
		return 2
	case "LOWEST":
		return 1
	default:
		return 0
	}
}

// sortKey 是任务在该排序下的游标键，必须与 expr 的取值一致。
func (s taskSort) sortKey(task Task) string {
	switch s.name {
	case "manual":
		return task.Rank
	case "created":
		return task.CreatedAt.UTC().Format(time.RFC3339Nano)
	case "updated":
		return task.UpdatedAt.UTC().Format(time.RFC3339Nano)
	case "triage_entered":
		return task.TriageEnteredAt.UTC().Format(time.RFC3339Nano)
	case "priority":
		if s.workbench && task.Priority != "NONE" {
			return strconv.Itoa(6 - priorityOrdinal(task.Priority))
		}
		return strconv.Itoa(priorityOrdinal(task.Priority))
	case "estimate":
		if task.Estimate == nil {
			return estimateNullSentinel(s.order)
		}
		return strconv.FormatFloat(*task.Estimate, 'f', -1, 64)
	case "iteration":
		return strconv.Itoa(task.IterationNumber)
	case "project":
		if task.ProjectName == "" {
			if s.order == "desc" {
				return ""
			}
			return "\U0010FFFF"
		}
		return task.ProjectName
	default:
		if task.DueDate == "" {
			if s.order == "desc" {
				return ""
			}
			return "~"
		}
		return task.DueDate
	}
}

type taskCursor struct {
	Workbench bool   `json:"w,omitempty"`
	Version   int    `json:"v"`
	Sort      string `json:"s"`
	Order     string `json:"o"`
	Key       string `json:"k"`
	ID        string `json:"id"`
}

func (s taskSort) encodeCursor(task Task) string {
	payload, _ := json.Marshal(taskCursor{Workbench: s.workbench, Version: 2, Sort: s.name, Order: s.order, Key: s.sortKey(task), ID: task.ID})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func (s taskSort) decodeCursor(raw string) (taskCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(raw) > 1024 {
		return taskCursor{}, errInvalidCursor
	}
	var cursor taskCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != 2 ||
		cursor.Workbench != s.workbench || cursor.Sort != s.name || cursor.Order != s.order || !membership.ValidateUUID(cursor.ID) {
		return taskCursor{}, errInvalidCursor
	}
	switch s.cast {
	case "timestamptz":
		if _, err := time.Parse(time.RFC3339Nano, cursor.Key); err != nil {
			return taskCursor{}, errInvalidCursor
		}
	case "int":
		if _, err := strconv.Atoi(cursor.Key); err != nil {
			return taskCursor{}, errInvalidCursor
		}
	case "numeric":
		if _, err := parseFiniteFloat(cursor.Key); err != nil {
			return taskCursor{}, errInvalidCursor
		}
	}
	return cursor, nil
}

// keysetCondition 生成「在游标之后」的行比较条件。
func (s taskSort) keysetCondition(q *taskQuery, cursor taskCursor) string {
	op := ">"
	if s.order == "desc" {
		op = "<"
	}
	if s.workbench && s.order == "desc" {
		k, id := q.arg(cursor.Key), q.arg(cursor.ID)
		return fmt.Sprintf("(%[1]s < %[2]s::%[3]s OR (%[1]s = %[2]s::%[3]s AND t.id > %[4]s::uuid))", s.expr, k, s.cast, id)
	}
	return fmt.Sprintf("(%s, t.id) %s (%s::%s, %s::uuid)", s.expr, op, q.arg(cursor.Key), s.cast, q.arg(cursor.ID))
}

func (s taskSort) orderBy() string {
	direction := strings.ToUpper(s.order)
	tie := direction
	if s.workbench {
		tie = "ASC"
	}
	return fmt.Sprintf("ORDER BY %s %s, t.id %s", s.expr, direction, tie)
}

// TaskListOptions 描述空间任务列表请求。
type TaskListOptions struct {
	WorkbenchOrder bool
	Limit          int
	Cursor         string
	Filter         TaskFilter
}

func (s Service) ListTasks(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, options TaskListOptions) (Page[Task], error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Page[Task]{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return Page[Task]{}, ErrSpaceNotFound
	}
	if options.Limit < 1 || options.Limit > MaxPageSize {
		return Page[Task]{}, ErrInvalidPageSize
	}
	sorting, err := resolveTaskSort(options.Filter.Sort, options.Filter.Order, "manual", "asc")
	if err != nil {
		return Page[Task]{}, err
	}
	sorting.workbench = options.WorkbenchOrder && options.Filter.TriageQueue
	if sorting.workbench && sorting.name == "priority" {
		sorting.expr = "(CASE WHEN t.priority='NONE' THEN 0 ELSE 6-" + priorityRankSQL + " END)"
	}
	q := &taskQuery{}
	conds := []string{
		"t.organization_id = openfox_private.current_organization_id()",
		"t.deleted_at IS NULL",
		"t.space_id = " + q.arg(spaceID) + "::uuid",
	}
	if options.Cursor != "" {
		cursor, err := sorting.decodeCursor(options.Cursor)
		if err != nil {
			return Page[Task]{}, err
		}
		conds = append(conds, sorting.keysetCondition(q, cursor))
	}
	conds = append(conds, options.Filter.conditions(q)...)
	limitArg := q.arg(options.Limit + 1)
	rows, err := tx.Query(ctx, `SELECT `+taskSelectColumns+` `+taskSelectFrom+`
		WHERE `+strings.Join(conds, " AND ")+`
		`+sorting.orderBy()+` LIMIT `+limitArg, q.args...)
	if err != nil {
		return Page[Task]{}, err
	}
	defer rows.Close()
	tasks := make([]Task, 0, options.Limit+1)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return Page[Task]{}, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return Page[Task]{}, err
	}
	rows.Close()
	page := Page[Task]{}
	if len(tasks) > options.Limit {
		tasks = tasks[:options.Limit]
		page.NextCursor = sorting.encodeCursor(tasks[len(tasks)-1])
	}
	if err := s.enrichTasks(ctx, tx, tasks); err != nil {
		return Page[Task]{}, err
	}
	page.Items = tasks
	return page, nil
}

// TaskCounts 是列表筛选下的总数与按状态分布。
type TaskCounts struct {
	Total    int64
	ByStatus []StatusCount
}

type StatusCount struct {
	StatusID string `json:"status_id"`
	Count    int64  `json:"count"`
}

func (s Service) CountTasks(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, filter TaskFilter) (TaskCounts, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskCounts{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return TaskCounts{}, ErrSpaceNotFound
	}
	if _, err := resolveTaskSort(filter.Sort, filter.Order, "manual", "asc"); err != nil {
		return TaskCounts{}, err
	}
	q := &taskQuery{}
	conds := []string{
		"t.organization_id = openfox_private.current_organization_id()",
		"t.deleted_at IS NULL",
		"t.space_id = " + q.arg(spaceID) + "::uuid",
	}
	conds = append(conds, filter.conditions(q)...)
	return s.countByStatus(ctx, tx, conds, q.args)
}

func (s Service) countByStatus(ctx context.Context, tx pgx.Tx, conds []string, args []any) (TaskCounts, error) {
	rows, err := tx.Query(ctx, `SELECT t.status_id::text, count(*) `+taskSelectFrom+`
		WHERE `+strings.Join(conds, " AND ")+` GROUP BY t.status_id ORDER BY t.status_id`, args...)
	if err != nil {
		return TaskCounts{}, err
	}
	defer rows.Close()
	counts := TaskCounts{ByStatus: make([]StatusCount, 0)}
	for rows.Next() {
		var item StatusCount
		if err := rows.Scan(&item.StatusID, &item.Count); err != nil {
			return TaskCounts{}, err
		}
		counts.Total += item.Count
		counts.ByStatus = append(counts.ByStatus, item)
	}
	return counts, rows.Err()
}

func (s Service) GetTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor) (Task, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Task{}, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return Task{}, ErrTaskNotFound
	}
	task, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskSelectColumns+` `+taskSelectFrom+`
		WHERE t.organization_id = openfox_private.current_organization_id()
		  AND t.space_id = $1::uuid AND t.id = $2::uuid AND t.deleted_at IS NULL`, spaceID, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	items := []Task{task}
	if err := s.enrichTasks(ctx, tx, items); err != nil {
		return Task{}, err
	}
	if items[0].DuplicateOfID != "" {
		// 目标任务所在空间读不到时不返回标题。
		var ref TaskParent
		var sequence int64
		err := tx.QueryRow(ctx, `
			SELECT d.id::text, s.prefix, d.sequence, d.title FROM openfox.tasks d
			  JOIN openfox.spaces s ON s.organization_id = d.organization_id AND s.id = d.space_id
			 WHERE d.organization_id = openfox_private.current_organization_id() AND d.id = $2::uuid
			   AND `+SpaceReadableSQL, strings.ToUpper(string(actor.Role)), items[0].DuplicateOfID).Scan(&ref.ID, &ref.DisplayID, &sequence, &ref.Title)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Task{}, err
		}
		if err == nil {
			ref.DisplayID = displayID(ref.DisplayID, sequence)
			items[0].DuplicateOf = &ref
		}
	}
	return items[0], nil
}

// FindTaskSpaceByDisplayID 把 <prefix>-<n>（前缀大小写不敏感）解析为任务所在空间与任务 ID；
// 找不到返回 ErrTaskNotFound。调用方必须随后做空间级读权限检查。
func (s Service) FindTaskByDisplayID(ctx context.Context, tx pgx.Tx, organizationID, displayID string, actor Actor) (spaceID, taskID string, err error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return "", "", err
	}
	index := strings.LastIndex(displayID, "-")
	if index <= 0 || index == len(displayID)-1 {
		return "", "", ErrTaskNotFound
	}
	sequence, parseErr := strconv.ParseInt(displayID[index+1:], 10, 64)
	if parseErr != nil || sequence < 1 {
		return "", "", ErrTaskNotFound
	}
	err = tx.QueryRow(ctx, `
		SELECT t.space_id::text, t.id::text
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND lower(s.prefix) = lower($1) AND t.sequence = $2 AND t.deleted_at IS NULL AND s.deleted_at IS NULL`,
		displayID[:index], sequence).Scan(&spaceID, &taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 旧编号：按空间的历史标识符查找；任务编号由当前前缀实时拼接，调用方据此给出 canonical_display_id。
		err = tx.QueryRow(ctx, `
			SELECT t.space_id::text, t.id::text
			  FROM openfox.space_prefix_history h
			  JOIN openfox.spaces s ON s.organization_id = h.organization_id AND s.id = h.space_id
			  JOIN openfox.tasks t ON t.organization_id = s.organization_id AND t.space_id = s.id
			 WHERE h.organization_id = openfox_private.current_organization_id()
			   AND lower(h.prefix) = lower($1) AND t.sequence = $2 AND t.deleted_at IS NULL AND s.deleted_at IS NULL`,
			displayID[:index], sequence).Scan(&spaceID, &taskID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// 任务被移到别的空间后，源空间（当前或历史前缀）的旧序号经别名表解析到任务现在的位置。
		// 活任务优先：上面两步命中时不查别名。返回的空间是任务当前空间，调用方据此给出新的 canonical_display_id。
		err = tx.QueryRow(ctx, `
			SELECT t.space_id::text, t.id::text
			  FROM openfox.spaces s
			  JOIN openfox.task_display_aliases a ON a.organization_id = s.organization_id AND a.space_id = s.id
			  JOIN openfox.tasks t ON t.organization_id = a.organization_id AND t.id = a.task_id
			  JOIN openfox.spaces ts ON ts.organization_id = t.organization_id AND ts.id = t.space_id
			 WHERE s.organization_id = openfox_private.current_organization_id() AND a.sequence = $2
			   AND (lower(s.prefix) = lower($1) OR EXISTS (
			        SELECT 1 FROM openfox.space_prefix_history h
			         WHERE h.organization_id = s.organization_id AND h.space_id = s.id AND lower(h.prefix) = lower($1)))
			   AND t.deleted_at IS NULL AND ts.deleted_at IS NULL
			 ORDER BY (lower(s.prefix) = lower($1)) DESC, a.moved_at DESC
			 LIMIT 1`,
			displayID[:index], sequence).Scan(&spaceID, &taskID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrTaskNotFound
	}
	return spaceID, taskID, err
}

func scanTask(row interface{ Scan(...any) error }, extra ...any) (Task, error) {
	var task Task
	var version int64
	var parentID, parentTitle string
	var parentSequence int64
	targets := append([]any{&task.OrganizationID, &task.SpaceID, &task.SpacePrefix, &task.ID, &task.Sequence,
		&task.TypeID, &task.TypeName, &task.StatusID, &task.StatusName, &task.StatusCategory,
		&task.Title, &task.Description, &task.Acceptance, &task.AssigneeUserID, &task.ExecutorAgentID,
		&task.Priority, &version, &task.CreatedAt, &task.UpdatedAt,
		&task.StartDate, &task.DueDate, &parentID, &parentTitle, &parentSequence,
		&task.Rank, &task.TypeIcon, &task.StatusColor, &task.Estimate, &task.IterationID, &task.IterationNumber, &task.ProjectID, &task.ProjectName,
		&task.TriageEnteredAt, &task.TriageSource, &task.TriageSnoozedUntil, &task.DuplicateOfID}, extra...)
	if err := row.Scan(targets...); err != nil {
		return Task{}, err
	}
	var err error
	task.Version, err = positiveVersion(version)
	if err != nil {
		return Task{}, err
	}
	task.DisplayID = displayID(task.SpacePrefix, task.Sequence)
	task.Labels = []string{}
	task.Assignees = []TaskAssignee{}
	task.LabelItems = []TaskLabelItem{}
	if parentID != "" {
		task.ParentTaskID = parentID
		task.Parent = &TaskParent{ID: parentID, DisplayID: displayID(task.SpacePrefix, parentSequence), Title: parentTitle}
	}
	return task, nil
}

// enrichTasks 批量补充处理人、标签、子任务计数与创建人，每类数据一次 = ANY($1) 查询。
func (s Service) enrichTasks(ctx context.Context, tx pgx.Tx, tasks []Task) error {
	if len(tasks) == 0 {
		return nil
	}
	ids := make([]string, len(tasks))
	index := make(map[string]int, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
		index[task.ID] = i
	}
	rows, err := tx.Query(ctx, `
		SELECT task_id, kind, target_id, name FROM (
			SELECT ta.task_id::text AS task_id, ta.kind, COALESCE(ta.user_id, ta.agent_id, ta.crew_id)::text AS target_id,
			       COALESCE(u.email, ag.name, cr.name, '') AS name,
			       CASE ta.kind WHEN 'HUMAN' THEN 0 WHEN 'AGENT' THEN 1 ELSE 2 END AS kind_order,
			       ta.created_at AS at, ta.id::text AS row_id
			  FROM openfox.task_assignees ta
			  LEFT JOIN openfox.users u ON u.id = ta.user_id
			  LEFT JOIN openfox.agents ag ON ag.organization_id = ta.organization_id AND ag.id = ta.agent_id
			  LEFT JOIN openfox.agent_crews cr ON cr.organization_id = ta.organization_id AND cr.id = ta.crew_id
			 WHERE ta.organization_id = openfox_private.current_organization_id() AND ta.task_id = ANY($1::uuid[])
			UNION ALL
			SELECT a.task_id::text,'INVITATION',i.id::text,i.normalized_email,3,a.created_at,i.id::text
			  FROM openfox.task_invitation_assignees a JOIN openfox.invitations i ON i.organization_id=a.organization_id AND i.id=a.invitation_id
			 WHERE a.organization_id=openfox_private.current_organization_id() AND a.task_id=ANY($1::uuid[])
			   AND i.status='PENDING' AND i.expires_at>statement_timestamp()
			UNION ALL
			SELECT t.id::text, 'HUMAN', t.assignee_id::text, COALESCE(u.email, ''), 0, t.created_at, t.id::text
			  FROM openfox.tasks t
			  LEFT JOIN openfox.users u ON u.id = t.assignee_id
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = ANY($1::uuid[])
			   AND t.assignee_id IS NOT NULL
			   AND NOT EXISTS (SELECT 1 FROM openfox.task_assignees x
			                    WHERE x.organization_id = t.organization_id AND x.task_id = t.id)
			UNION ALL
			SELECT t.id::text, 'AGENT', t.executor_agent_id::text, COALESCE(ag.name, ''), 1, t.created_at, t.id::text
			  FROM openfox.tasks t
			  LEFT JOIN openfox.agents ag ON ag.organization_id = t.organization_id AND ag.id = t.executor_agent_id
			 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = ANY($1::uuid[])
			   AND t.executor_agent_id IS NOT NULL
			   AND NOT EXISTS (SELECT 1 FROM openfox.task_assignees x
			                    WHERE x.organization_id = t.organization_id AND x.task_id = t.id)
		) a ORDER BY kind_order, at, row_id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID string
		var item TaskAssignee
		if err := rows.Scan(&taskID, &item.Kind, &item.ID, &item.Name); err != nil {
			rows.Close()
			return err
		}
		tasks[index[taskID]].Assignees = append(tasks[index[taskID]].Assignees, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 外部账号处理人：一次批量查询，只命中带 external_assignee_id 的任务。
	rows, err = tx.Query(ctx, `
		SELECT t.id::text, a.id::text, a.provider, a.display_name, a.email, (a.invitation_id IS NOT NULL AND a.linked_user_id IS NULL)
		  FROM openfox.tasks t
		  JOIN openfox.external_accounts a ON a.organization_id = t.organization_id AND a.id = t.external_assignee_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID string
		var item TaskExternalAssignee
		if err := rows.Scan(&taskID, &item.ID, &item.Provider, &item.Name, &item.Email, &item.PendingInvitation); err != nil {
			rows.Close()
			return err
		}
		tasks[index[taskID]].ExternalAssignee = &item
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = tx.Query(ctx, `
		SELECT tl.task_id::text, l.id::text, l.name, l.color, l.group_id::text, l.space_id::text
		  FROM openfox.task_labels tl
		  JOIN openfox.labels l ON l.organization_id = tl.organization_id AND l.id = tl.label_id
		 WHERE tl.organization_id = openfox_private.current_organization_id() AND tl.task_id = ANY($1::uuid[])
		 ORDER BY l.name, l.id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID string
		var item TaskLabelItem
		if err := rows.Scan(&taskID, &item.ID, &item.Name, &item.Color, &item.GroupID, &item.SpaceID); err != nil {
			rows.Close()
			return err
		}
		task := &tasks[index[taskID]]
		task.LabelItems = append(task.LabelItems, item)
		task.Labels = append(task.Labels, item.Name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = tx.Query(ctx, `
		SELECT c.parent_task_id::text, count(*), count(*) FILTER (WHERE cs.category = 'DONE')
		  FROM openfox.tasks c
		  JOIN openfox.task_statuses cs ON cs.organization_id = c.organization_id AND cs.id = c.status_id
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND c.parent_task_id = ANY($1::uuid[]) AND c.deleted_at IS NULL
		 GROUP BY c.parent_task_id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID string
		var total, done int
		if err := rows.Scan(&taskID, &total, &done); err != nil {
			rows.Close()
			return err
		}
		tasks[index[taskID]].SubtaskCount, tasks[index[taskID]].SubtaskDoneCount = total, done
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = tx.Query(ctx, `
		SELECT DISTINCT ON (a.task_id) a.task_id::text, a.actor_id::text, COALESCE(u.email, '')
		  FROM openfox.task_activities a
		  LEFT JOIN openfox.users u ON u.id = a.actor_id
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.task_id = ANY($1::uuid[]) AND a.kind = 'task.created'
		 ORDER BY a.task_id, a.created_at, a.id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID, actorID, actorEmail string
		if err := rows.Scan(&taskID, &actorID, &actorEmail); err != nil {
			rows.Close()
			return err
		}
		tasks[index[taskID]].CreatedBy = actorID
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 当前用户的收藏：RLS 只放行本人的行，一次批量查询。
	rows, err = tx.Query(ctx, `
		SELECT task_id::text FROM openfox.task_favorites
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = openfox_private.current_actor_id() AND task_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			rows.Close()
			return err
		}
		tasks[index[taskID]].Favorited = true
	}
	rows.Close()
	return rows.Err()
}

// splitDisplayID 拆分形如 <前缀>-<序号> 的编号；不是编号格式时 ok = false。
func splitDisplayID(value string) (string, int64, bool) {
	index := strings.LastIndex(value, "-")
	if index <= 0 || index == len(value)-1 || len(value) > 64 {
		return "", 0, false
	}
	sequence, err := strconv.ParseInt(value[index+1:], 10, 64)
	if err != nil || sequence < 1 {
		return "", 0, false
	}
	for _, character := range value[:index] {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return "", 0, false
		}
	}
	return value[:index], sequence, true
}
