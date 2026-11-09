package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// 任务写路径：创建、更新（含排序键、父子、多处理人、标签集合）与软删除。

var (
	errAssigneeNotAllowed   = NewRuleError(400, "ASSIGNEE_NOT_ALLOWED", "assignee is not available for this task")
	errAgentNotAssignable   = NewRuleError(400, "AGENT_NOT_ASSIGNABLE", "Agent does not accept direct assignment")
	errTooManyAssignees     = NewRuleError(400, "TOO_MANY_ASSIGNEES", "a task supports at most 10 assignees")
	errInvalidParent        = NewRuleError(400, "INVALID_PARENT", "parent task is not available")
	errHierarchyTooDeep     = NewRuleError(400, "TASK_HIERARCHY_TOO_DEEP", "task hierarchy is limited to 10 levels")
	errHierarchyCycle       = NewRuleError(409, "TASK_HIERARCHY_CYCLE", "parent task would create a cycle")
	errInvalidDate          = NewRuleError(400, "INVALID_DATE", "date must be YYYY-MM-DD")
	errInvalidDateRange     = NewRuleError(400, "INVALID_DATE_RANGE", "start date must not be after due date")
	errInvalidPosition      = NewRuleError(400, "INVALID_POSITION", "position neighbors are not available")
	errInvalidLabelSet      = NewRuleError(400, "INVALID_LABEL", "label is not available for this task")
	errTaskLabelGroupSingle = NewRuleError(400, "LABEL_GROUP_EXCLUSIVE", "a task may have only one label from a single-choice group")
	errTooManyLabels        = NewRuleError(400, "TOO_MANY_LABELS", "a task supports at most 20 labels")
	errRunActive            = NewRuleError(409, "RUN_ACTIVE", "the task has an active run")
)

const (
	maxTaskAssignees = 10
	maxTaskLabels    = 20
	maxTaskDepth     = 10
)

type TaskAssigneeRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type TaskAssignee struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TaskLabelItem struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Color   *string `json:"color"`
	GroupID *string `json:"group_id"`
	SpaceID *string `json:"space_id"`
}

type TaskParent struct {
	ID        string `json:"id"`
	DisplayID string `json:"display_id"`
	Title     string `json:"title"`
}

// optionalText 返回 OptionalString 的取值（去空白），null 或未传返回空串。
func optionalText(o OptionalString) string {
	if o.Value == nil {
		return ""
	}
	return strings.TrimSpace(*o.Value)
}

// TaskPositionInput 描述拖动后的位置：两个邻居都为空表示移到末尾。
type TaskPositionInput struct {
	BeforeTaskID string
	AfterTaskID  string
}

type TaskInput struct {
	DeferAssignedAgent bool // An assistant draft can assign an Agent without starting its work.
	SubmitToTriage     bool
	integrationSource  bool // trusted live integration, never taken from the HTTP body
	SourceCommentID    string
	ID                 string
	TypeID             string
	StatusID           string
	Title              string
	Description        string
	Acceptance         string
	AssigneeUserID     string
	ExecutorAgentID    string
	Priority           string
	Labels             []string
	StartDate          string
	DueDate            string
	ParentTaskID       string
	Assignees          *[]TaskAssigneeRef
	LabelIDs           *[]string
	FieldValues        map[string]json.RawMessage
	AfterTaskID        string
	// Estimate 为 nil 表示不估时。
	Estimate    *float64
	IterationID string
	ProjectID   string
	RequestID   string
	// triageCarry 非空表示任务由分拣规则从别的空间移入：不再重跑规则，并带上已命中的规则记录。
	triageCarry *triageEntry
}

type TaskUpdateInput struct {
	TypeID          *string
	StatusID        *string
	Title           *string
	Description     *string
	Acceptance      *string
	AssigneeUserID  *string
	ExecutorAgentID *string
	Priority        *string
	Labels          *[]string
	StartDate       OptionalString
	DueDate         OptionalString
	ParentTaskID    OptionalString
	Assignees       *[]TaskAssigneeRef
	LabelIDs        *[]string
	Position        *TaskPositionInput
	// Estimate：未传不变；null 清空；数值须符合空间估时刻度。
	Estimate    OptionalFloat
	IterationID OptionalString
	ProjectID   OptionalString
	Version     uint64
	RequestID   string
	// AssignmentConversation：本次新增 Agent 处理人时用哪个运行对话。
	// 空串或 reuse 沿用现有指派会话；new 先把现有的打开状态指派会话降级为普通会话，worker 会新建一个。其它情况忽略。
	AssignmentConversation string
}

// activityWriter 在一次请求里按序号生成唯一的 source_id（<requestID>:<序号>）。
type activityWriter struct {
	discard   bool // 为 true 时不写动态（创建任务时 task.created 已覆盖）
	tx        pgx.Tx
	spaceID   string
	taskID    string
	actorID   string
	requestID string
	next      int
}

func newActivityWriter(tx pgx.Tx, spaceID, taskID, actorID, requestID string) *activityWriter {
	if strings.TrimSpace(requestID) == "" {
		requestID, _ = membership.NewID()
	}
	return &activityWriter{tx: tx, spaceID: spaceID, taskID: taskID, actorID: actorID, requestID: requestID}
}

func (w *activityWriter) add(ctx context.Context, kind string, diff map[string]any) error {
	if w.discard {
		return nil
	}
	if diff == nil {
		diff = map[string]any{}
	}
	payload, err := json.Marshal(diff)
	if err != nil {
		return ErrInvalidTask
	}
	activityID, err := membership.NewID()
	if err != nil {
		return err
	}
	w.next++
	_, err = w.tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (
			organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid,
		          $4::uuid, $5, $6::jsonb, $7)`,
		w.spaceID, w.taskID, activityID, w.actorID, kind, payload, fmt.Sprintf("%s:%d", w.requestID, w.next))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func parseDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", errInvalidDate
	}
	return value, nil
}

func checkDateRange(start, due string) error {
	if start != "" && due != "" && start > due {
		return errInvalidDateRange
	}
	return nil
}

// normalizeAssignees 规范化并去重处理人列表。
func normalizeAssignees(refs []TaskAssigneeRef) ([]TaskAssigneeRef, error) {
	if len(refs) > maxTaskAssignees {
		return nil, errTooManyAssignees
	}
	seen := make(map[string]bool, len(refs))
	result := make([]TaskAssigneeRef, 0, len(refs))
	for _, ref := range refs {
		kind := strings.ToUpper(strings.TrimSpace(ref.Kind))
		id := strings.ToLower(strings.TrimSpace(ref.ID))
		if (kind != "HUMAN" && kind != "AGENT" && kind != "CREW" && kind != "INVITATION") || !membership.ValidateUUID(id) {
			return nil, errAssigneeNotAllowed
		}
		if seen[kind+":"+id] {
			continue
		}
		seen[kind+":"+id] = true
		result = append(result, TaskAssigneeRef{Kind: kind, ID: id})
	}
	return result, nil
}

func legacyAssignees(userID, agentID string) []TaskAssigneeRef {
	var refs []TaskAssigneeRef
	if strings.TrimSpace(userID) != "" {
		refs = append(refs, TaskAssigneeRef{Kind: "HUMAN", ID: userID})
	}
	if strings.TrimSpace(agentID) != "" {
		refs = append(refs, TaskAssigneeRef{Kind: "AGENT", ID: agentID})
	}
	return refs
}

// validateAssigneeRefs 校验新增处理人对任务所在空间和操作人是否可用，每种类型一次批量查询。
//   - HUMAN：组织内 ACTIVE 成员，且按空间读权限规则能读该空间（与 SpaceReadableSQL 一致，用目标成员自己的组织角色）。
//   - AGENT：ACTIVE、未归档、属于任务所在空间，并且 SHARED 或归操作人所有。
//   - CREW：未归档、属于任务所在空间，并且 SHARED 或归操作人所有。
func (s Service) validateAssigneeRefs(ctx context.Context, tx pgx.Tx, spaceID string, actor Actor, refs []TaskAssigneeRef) error {
	var humans, agents, crews []string
	for _, ref := range refs {
		switch ref.Kind {
		case "HUMAN":
			humans = append(humans, ref.ID)
		case "AGENT":
			agents = append(agents, ref.ID)
		case "CREW":
			crews = append(crews, ref.ID)
		}
	}
	count := func(query string, ids []string, extra ...any) (int, error) {
		if len(ids) == 0 {
			return 0, nil
		}
		var matched int
		args := append([]any{ids, spaceID}, extra...)
		err := tx.QueryRow(ctx, query, args...).Scan(&matched)
		return matched, err
	}
	matched, err := count(`
		SELECT count(*) FROM openfox.organization_members om
		 JOIN openfox.spaces s ON s.organization_id = om.organization_id AND s.id = $2::uuid
		 WHERE om.organization_id = openfox_private.current_organization_id()
		   AND om.user_id = ANY($1::uuid[]) AND om.status = 'ACTIVE'
		   AND (EXISTS (SELECT 1 FROM openfox.space_members sm
		                 WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
		                   AND sm.user_id = om.user_id AND sm.status = 'ACTIVE')
		        OR (s.visibility = 'SHARED' AND om.role <> 'GUEST' AND NOT EXISTS (
		                 SELECT 1 FROM openfox.space_members sm
		                  WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
		                    AND sm.user_id = om.user_id AND sm.status <> 'ACTIVE')))`, humans)
	if err != nil {
		return err
	}
	if matched != len(humans) {
		return errAssigneeNotAllowed
	}
	matched, err = count(`
		SELECT count(*) FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND space_id = $2::uuid AND status = 'ACTIVE' AND archived_at IS NULL
		   AND builtin_kind IS DISTINCT FROM 'TASK_ASSISTANT'
		   AND (visibility = 'SHARED' OR owner_user_id = $3::uuid)`, agents, actor.UserID)
	if err != nil {
		return err
	}
	if matched != len(agents) {
		return errAssigneeNotAllowed
	}
	matched, err = count(`
		SELECT count(*) FROM openfox.agent_crews
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND space_id = $2::uuid AND archived_at IS NULL
		   AND (visibility = 'SHARED' OR owner_user_id = $3::uuid)`, crews, actor.UserID)
	if err != nil {
		return err
	}
	if matched != len(crews) {
		return errAssigneeNotAllowed
	}
	return nil
}

// validateAssignableAgents：新增的 AGENT 处理人必须允许被指派（allow_assign），
// 且不是任何小队的专属成员；指派小队（CREW）不受影响。已有处理人不重新校验。
func validateAssignableAgents(ctx context.Context, tx pgx.Tx, refs []TaskAssigneeRef) error {
	var agentIDs []string
	for _, ref := range refs {
		if ref.Kind == "AGENT" {
			agentIDs = append(agentIDs, ref.ID)
		}
	}
	if len(agentIDs) == 0 {
		return nil
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM openfox.agents a
		     WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = ANY($1::uuid[])
		       AND (NOT a.allow_assign OR EXISTS (
		            SELECT 1 FROM openfox.agent_crew_members m
		             WHERE m.organization_id = a.organization_id AND m.agent_id = a.id AND m.exclusive)))`,
		agentIDs).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return errAgentNotAssignable
	}
	return nil
}

func assigneeKey(kind, id string) string { return kind + ":" + id }

// firstOfKind 取列表里第一个指定类型的目标。
func firstOfKind(refs []TaskAssigneeRef, kind string) string {
	for _, ref := range refs {
		if ref.Kind == kind {
			return ref.ID
		}
	}
	return ""
}

// writeAssignees 用 desired 整体替换 task_assignees（顺序即展示顺序），返回新增的处理人。
// 调用方负责在 tasks 行上写入投影列。
func (s Service) writeAssignees(ctx context.Context, tx pgx.Tx, w *activityWriter, spaceID, taskID string, actor Actor, current []TaskAssignee, desired []TaskAssigneeRef, startAddedAgents bool) ([]TaskAssigneeRef, error) {
	current, desired, err := syncInvitationAssignees(ctx, tx, spaceID, taskID, actor, current, desired)
	if err != nil {
		return nil, err
	}
	currentSet := make(map[string]bool, len(current))
	for _, item := range current {
		currentSet[assigneeKey(item.Kind, item.ID)] = true
	}
	desiredSet := make(map[string]bool, len(desired))
	var added []TaskAssigneeRef
	for _, ref := range desired {
		desiredSet[assigneeKey(ref.Kind, ref.ID)] = true
		if !currentSet[assigneeKey(ref.Kind, ref.ID)] {
			added = append(added, ref)
		}
	}
	sameOrder := len(current) == len(desired)
	for index := 0; sameOrder && index < len(desired); index++ {
		sameOrder = current[index].Kind == desired[index].Kind && current[index].ID == desired[index].ID
	}
	if sameOrder {
		return nil, nil
	}
	if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, added); err != nil {
		return nil, err
	}
	if err := validateAssignableAgents(ctx, tx, added); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_assignees
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid`, taskID); err != nil {
		return nil, err
	}
	for _, ref := range desired {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_assignees (
				organization_id, space_id, task_id, kind, user_id, agent_id, crew_id, created_by
			) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3,
			          CASE WHEN $3 = 'HUMAN' THEN $4::uuid END,
			          CASE WHEN $3 = 'AGENT' THEN $4::uuid END,
			          CASE WHEN $3 = 'CREW' THEN $4::uuid END,
			          $5::uuid)`, spaceID, taskID, ref.Kind, ref.ID, actor.UserID); err != nil {
			return nil, mapDatabaseError(err)
		}
	}
	for _, item := range current {
		if !desiredSet[assigneeKey(item.Kind, item.ID)] {
			if err := w.add(ctx, "task.assignee_removed", map[string]any{"kind": item.Kind, "id": item.ID}); err != nil {
				return nil, err
			}
			if item.Kind == "AGENT" {
				if err := enqueueTaskAgentEvent(ctx, tx, "UNASSIGNED", spaceID, taskID, item.ID, actor.UserID); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, ref := range added {
		if err := w.add(ctx, "task.assignee_added", map[string]any{"kind": ref.Kind, "id": ref.ID}); err != nil {
			return nil, err
		}
		if ref.Kind == "AGENT" && startAddedAgents {
			if err := enqueueTaskAgentEvent(ctx, tx, "ASSIGNED", spaceID, taskID, ref.ID, actor.UserID); err != nil {
				return nil, err
			}
		}
	}
	return added, nil
}

// enqueueTaskAgentEvent 在任务写事务里记录新增 / 移除 Agent 处理人，由 dispatch worker 异步处理：
// ASSIGNED 打开指派会话让 Agent 开始执行，UNASSIGNED 取消它在该任务上的活跃运行（见 000044）。
func enqueueTaskAgentEvent(ctx context.Context, tx pgx.Tx, kind, spaceID, taskID, agentID, actorID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_agent_events (organization_id, kind, space_id, task_id, agent_id, actor_user_id)
		VALUES (openfox_private.current_organization_id(), $1, $2::uuid, $3::uuid, $4::uuid, $5::uuid)`,
		kind, spaceID, taskID, agentID, actorID)
	return err
}

// addFollower 让成员自动成为任务关注者；已关注时不做任何事。
func addHumanFollower(ctx context.Context, tx pgx.Tx, spaceID, taskID, userID, createdBy string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $4::uuid)
		ON CONFLICT DO NOTHING`, spaceID, taskID, userID, createdBy)
	return err
}

// notifyAssignee 在任务被分配给他人时写一条 TASK_ASSIGNED 通知；
// 以任务 ID + 被分配人 + 任务版本去重，同一次写入重试不会重复通知。
func notifyAssignee(ctx context.Context, tx pgx.Tx, taskID, assigneeUserID, actorUserID string, version int64) error {
	if assigneeUserID == "" || assigneeUserID == actorUserID {
		return nil
	}
	_, err := tx.Exec(ctx, `
		SELECT openfox_private.enqueue_notification(
			$1::uuid, 'TASK_ASSIGNED', NULL::uuid, NULL::uuid, $2::uuid, NULL::uuid,
			$3, 'A task was assigned to you.')`,
		assigneeUserID, taskID, fmt.Sprintf("assign:%s:%s:%d", taskID, assigneeUserID, version))
	return err
}

// afterAssigneesAdded 处理新增处理人的副作用：成员自动关注并通知。
func (s Service) afterAssigneesAdded(ctx context.Context, tx pgx.Tx, spaceID, taskID string, actor Actor, added []TaskAssigneeRef, version int64) error {
	for _, ref := range added {
		if ref.Kind != "HUMAN" {
			continue
		}
		if err := addHumanFollower(ctx, tx, spaceID, taskID, ref.ID, actor.UserID); err != nil {
			return err
		}
		if err := notifyAssignee(ctx, tx, taskID, ref.ID, actor.UserID, version); err != nil {
			return err
		}
	}
	return nil
}

// resolveLabelNames 把旧接口的标签名称解析为标签 ID：优先匹配未归档的组织级标签，
// 否则取（必要时创建）空间级标签。
func (s Service) resolveLabelNames(ctx context.Context, tx pgx.Tx, spaceID string, names []string) ([]string, error) {
	if len(names) > maxTaskLabels {
		return nil, errTooManyLabels
	}
	seen := make(map[string]bool)
	ids := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || len([]rune(name)) > 50 || seen[strings.ToLower(name)] {
			return nil, ErrInvalidTask
		}
		seen[strings.ToLower(name)] = true
		var id string
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM openfox.labels
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND space_id IS NULL AND archived_at IS NULL AND lower(name) = lower($1)`, name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			labelID, idErr := membership.NewID()
			if idErr != nil {
				return nil, idErr
			}
			err = tx.QueryRow(ctx, `
				INSERT INTO openfox.labels (organization_id, space_id, id, name)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3)
				ON CONFLICT (organization_id, space_id, lower(name)) DO UPDATE SET name = openfox.labels.name
				RETURNING id::text`, spaceID, labelID, name).Scan(&id)
		}
		if err != nil {
			return nil, mapDatabaseError(err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// validateTaskLabelSet 校验任务的目标标签集合：标签存在、属于组织或该空间；新增的标签必须未归档；
// 同一单选分组最多一个；至多 20 个。
func (s Service) validateTaskLabelSet(ctx context.Context, tx pgx.Tx, spaceID string, desired []string, existing map[string]bool) error {
	if len(desired) > maxTaskLabels {
		return errTooManyLabels
	}
	if len(desired) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(space_id::text, ''), COALESCE(group_id::text, ''), archived_at IS NOT NULL
		  FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, desired)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := 0
	groups := make(map[string]bool)
	for rows.Next() {
		var id, labelSpace, group string
		var archived bool
		if err := rows.Scan(&id, &labelSpace, &group, &archived); err != nil {
			return err
		}
		found++
		if (labelSpace != "" && labelSpace != spaceID) || (archived && !existing[id]) {
			return errInvalidLabelSet
		}
		if group != "" {
			if groups[group] {
				return errTaskLabelGroupSingle
			}
			groups[group] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found != len(desired) {
		return errInvalidLabelSet
	}
	return nil
}

func normalizeLabelIDs(ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	result := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		if !membership.ValidateUUID(id) {
			return nil, errInvalidLabelSet
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	if len(result) > maxTaskLabels {
		return nil, errTooManyLabels
	}
	return result, nil
}

// writeLabelSet 把任务标签替换为 desired，只写有差异的行并记录字段级动态。
func (s Service) writeLabelSet(ctx context.Context, tx pgx.Tx, w *activityWriter, spaceID, taskID string, current []TaskLabelItem, desired []string) error {
	existing := make(map[string]bool, len(current))
	for _, item := range current {
		existing[item.ID] = true
	}
	if err := s.validateTaskLabelSet(ctx, tx, spaceID, desired, existing); err != nil {
		return err
	}
	desiredSet := make(map[string]bool, len(desired))
	var added, removed []string
	for _, id := range desired {
		desiredSet[id] = true
		if !existing[id] {
			added = append(added, id)
		}
	}
	for _, item := range current {
		if !desiredSet[item.ID] {
			removed = append(removed, item.ID)
		}
	}
	if len(removed) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.task_labels
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND task_id = $1::uuid AND label_id = ANY($2::uuid[])`, taskID, removed); err != nil {
			return err
		}
	}
	for _, id := range added {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_labels (organization_id, space_id, task_id, label_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid)
			ON CONFLICT DO NOTHING`, spaceID, taskID, id); err != nil {
			return mapDatabaseError(err)
		}
	}
	for _, id := range removed {
		if err := w.add(ctx, "task.label_removed", map[string]any{"label_id": id}); err != nil {
			return err
		}
	}
	for _, id := range added {
		if err := w.add(ctx, "task.label_added", map[string]any{"label_id": id}); err != nil {
			return err
		}
	}
	return nil
}

// checkParent 校验新父任务并防止成环与过深。调用方已按 id 顺序锁住自身与父任务。
// selfID 为空表示创建新任务（还没有后代，不会成环）。
func (s Service) checkParent(ctx context.Context, tx pgx.Tx, spaceID, selfID, parentID string) error {
	var ancestors int
	var cycle bool
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE anc AS (
			SELECT t.id, t.parent_task_id, 1 AS depth
			  FROM openfox.tasks t
			 WHERE t.organization_id = openfox_private.current_organization_id()
			   AND t.id = $1::uuid AND t.space_id = $2::uuid AND t.deleted_at IS NULL
			UNION ALL
			SELECT t.id, t.parent_task_id, a.depth + 1
			  FROM openfox.tasks t
			  JOIN anc a ON t.id = a.parent_task_id
			 WHERE t.organization_id = openfox_private.current_organization_id()
			   AND a.depth < 20 AND t.deleted_at IS NULL
		)
		SELECT count(*), COALESCE(bool_or(id = NULLIF($3, '')::uuid), false) FROM anc`,
		parentID, spaceID, selfID).Scan(&ancestors, &cycle)
	if err != nil {
		return err
	}
	if ancestors == 0 {
		return errInvalidParent
	}
	if cycle {
		return errHierarchyCycle
	}
	height := 0
	if selfID != "" {
		if err := tx.QueryRow(ctx, `
			WITH RECURSIVE d AS (
				SELECT id, 0 AS depth FROM openfox.tasks
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
				UNION ALL
				SELECT c.id, d.depth + 1 FROM openfox.tasks c JOIN d ON c.parent_task_id = d.id
				 WHERE c.organization_id = openfox_private.current_organization_id()
				   AND d.depth < 20 AND c.deleted_at IS NULL
			)
			SELECT COALESCE(max(depth), 0) FROM d`, selfID).Scan(&height); err != nil {
			return err
		}
	}
	if ancestors+1+height > maxTaskDepth {
		return errHierarchyTooDeep
	}
	return nil
}

// effectiveRankOf 返回同空间未删除任务的有效排序键；找不到返回 errInvalidPosition。
func effectiveRankOf(ctx context.Context, tx pgx.Tx, spaceID, taskID string) (string, error) {
	var rank string
	err := tx.QueryRow(ctx, `
		SELECT `+effectiveRankSQL+` FROM openfox.tasks t
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.space_id = $1::uuid AND t.id = $2::uuid AND t.deleted_at IS NULL`, spaceID, taskID).Scan(&rank)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errInvalidPosition
	}
	return rank, err
}

// neighborRank 取同空间排除 selfID 后，大于（next=true）或小于 rank 的最近一个有效键，没有则为空串。
func neighborRank(ctx context.Context, tx pgx.Tx, spaceID, selfID, rank string, next bool) (string, error) {
	comparison, direction := "<", "DESC"
	if next {
		comparison, direction = ">", "ASC"
	}
	var neighbor string
	err := tx.QueryRow(ctx, `
		SELECT `+effectiveRankSQL+` FROM openfox.tasks t
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.space_id = $1::uuid AND t.deleted_at IS NULL AND t.id IS DISTINCT FROM NULLIF($2, '')::uuid
		   AND `+effectiveRankSQL+` `+comparison+` $3::text COLLATE "C"
		 ORDER BY `+effectiveRankSQL+` `+direction+`, t.id `+direction+` LIMIT 1`, spaceID, selfID, rank).Scan(&neighbor)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return neighbor, err
}

func maxRank(ctx context.Context, tx pgx.Tx, spaceID, selfID string) (string, error) {
	var rank *string
	err := tx.QueryRow(ctx, `
		SELECT max(`+effectiveRankSQL+`) FROM openfox.tasks t
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.space_id = $1::uuid AND t.deleted_at IS NULL AND t.id IS DISTINCT FROM NULLIF($2, '')::uuid`, spaceID, selfID).Scan(&rank)
	if err != nil || rank == nil {
		return "", err
	}
	return *rank, nil
}

// resolveRank 计算任务放到新位置后的排序键。after/before 为空表示没有该侧邻居；都为空放到末尾。
func resolveRank(ctx context.Context, tx pgx.Tx, spaceID, selfID, afterID, beforeID string) (string, error) {
	var lower, upper string
	var err error
	switch {
	case afterID != "" && beforeID != "":
		if lower, err = effectiveRankOf(ctx, tx, spaceID, afterID); err != nil {
			return "", err
		}
		if upper, err = effectiveRankOf(ctx, tx, spaceID, beforeID); err != nil {
			return "", err
		}
		if lower >= upper {
			return "", errInvalidPosition
		}
	case afterID != "":
		if lower, err = effectiveRankOf(ctx, tx, spaceID, afterID); err != nil {
			return "", err
		}
		if upper, err = neighborRank(ctx, tx, spaceID, selfID, lower, true); err != nil {
			return "", err
		}
	case beforeID != "":
		if upper, err = effectiveRankOf(ctx, tx, spaceID, beforeID); err != nil {
			return "", err
		}
		if lower, err = neighborRank(ctx, tx, spaceID, selfID, upper, false); err != nil {
			return "", err
		}
	default:
		if lower, err = maxRank(ctx, tx, spaceID, selfID); err != nil {
			return "", err
		}
	}
	rank, err := RankBetween(lower, upper)
	if err != nil {
		return "", errInvalidPosition
	}
	return rank, nil
}

func (s Service) CreateTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input TaskInput) (Task, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Task{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return Task{}, ErrSpaceNotFound
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || len([]rune(input.Title)) > 500 {
		return Task{}, ErrInvalidTask
	}
	if len([]rune(input.Description)) > 10000 || len([]rune(input.Acceptance)) > 10000 {
		return Task{}, ErrInvalidTask
	}
	if strings.TrimSpace(input.Priority) == "" {
		input.Priority = "NONE"
	}
	priority, err := ParsePriority(input.Priority)
	if err != nil {
		return Task{}, err
	}
	startDate, err := parseDate(input.StartDate)
	if err != nil {
		return Task{}, err
	}
	dueDate, err := parseDate(input.DueDate)
	if err != nil {
		return Task{}, err
	}
	if err := checkDateRange(startDate, dueDate); err != nil {
		return Task{}, err
	}
	if (input.Assignees != nil) && (input.AssigneeUserID != "" || input.ExecutorAgentID != "") {
		return Task{}, ErrInvalidTask
	}
	if input.LabelIDs != nil && len(input.Labels) > 0 {
		return Task{}, ErrInvalidTask
	}
	var desiredAssignees []TaskAssigneeRef
	if input.Assignees != nil {
		desiredAssignees, err = normalizeAssignees(*input.Assignees)
	} else {
		desiredAssignees, err = normalizeAssignees(legacyAssignees(input.AssigneeUserID, input.ExecutorAgentID))
	}
	if err != nil {
		return Task{}, err
	}
	for _, ref := range desiredAssignees {
		if ref.Kind == "INVITATION" && !canManageOrganization(actor.Role) {
			return Task{}, ErrOrganizationForbidden
		}
	}
	space, err := s.loadSpace(ctx, tx, organizationID, spaceID, actor.UserID, true)
	if err != nil {
		return Task{}, err
	}
	if input.IterationID != "" {
		if err := validateTaskIteration(ctx, tx, spaceID, input.IterationID); err != nil {
			return Task{}, err
		}
	}
	if input.ProjectID != "" {
		if err := validateTaskProject(ctx, tx, spaceID, input.ProjectID); err != nil {
			return Task{}, err
		}
	}
	if input.Estimate != nil {
		settings, err := loadSpaceEstimation(ctx, tx, spaceID, "")
		if err != nil {
			return Task{}, err
		}
		if err := ValidateEstimate(settings, *input.Estimate); err != nil {
			return Task{}, err
		}
	}
	typeID := input.TypeID
	if typeID == "" {
		typeID, err = defaultTypeID(ctx, tx, organizationID, spaceID)
		if err != nil {
			return Task{}, err
		}
	}
	if !membership.ValidateUUID(typeID) {
		return Task{}, ErrTaskTypeNotFound
	}
	flow, err := loadWorkflow(ctx, tx, organizationID, spaceID, typeID)
	if err != nil {
		return Task{}, err
	}
	statusID := input.StatusID
	if statusID == "" {
		statusID = flow.defaultStatusID
		if orgDefault, err := OrganizationDefaultStatusID(ctx, tx); err != nil && !errors.Is(err, ErrStatusNotFound) {
			return Task{}, err
		} else if err == nil && flow.contains(orgDefault) {
			statusID = orgDefault
		}
	}
	if !membership.ValidateUUID(statusID) || !flow.contains(statusID) {
		return Task{}, ErrInvalidWorkflow
	}
	var labelIDs []string
	if input.LabelIDs != nil {
		labelIDs, err = normalizeLabelIDs(*input.LabelIDs)
	} else if len(input.Labels) > 0 {
		labelIDs, err = s.resolveLabelNames(ctx, tx, spaceID, input.Labels)
	}
	if err != nil {
		return Task{}, err
	}
	draft := triageDraft{typeID: typeID, statusID: statusID, priority: priority, assignees: desiredAssignees, labelIDs: labelIDs,
		projectID: input.ProjectID, iterationID: input.IterationID, title: input.Title, description: input.Description, dueDate: dueDate, flow: flow}
	entry, moved, err := s.prepareTriage(ctx, tx, organizationID, spaceID, actor, input, &draft)
	if err != nil {
		return Task{}, err
	}
	if moved != nil {
		return *moved, nil
	}
	statusID, priority, desiredAssignees, labelIDs, input.ProjectID = draft.statusID, draft.priority, draft.assignees, draft.labelIDs, draft.projectID
	triageSource := ""
	if entry != nil {
		triageSource = entry.source
	}
	parentID := strings.ToLower(strings.TrimSpace(input.ParentTaskID))
	if parentID != "" {
		if !membership.ValidateUUID(parentID) {
			return Task{}, errInvalidParent
		}
		if _, err := tx.Exec(ctx, `
			SELECT 1 FROM openfox.tasks WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND deleted_at IS NULL FOR UPDATE`, parentID); err != nil {
			return Task{}, err
		}
		if err := s.checkParent(ctx, tx, spaceID, "", parentID); err != nil {
			return Task{}, err
		}
	}
	afterID := strings.ToLower(strings.TrimSpace(input.AfterTaskID))
	if afterID != "" && !membership.ValidateUUID(afterID) {
		return Task{}, errInvalidPosition
	}
	rank, err := resolveRank(ctx, tx, spaceID, "", afterID, "")
	if err != nil {
		return Task{}, err
	}
	sequence, err := nextTaskSequence(ctx, tx, organizationID, spaceID)
	if err != nil {
		return Task{}, err
	}
	taskID := input.ID
	if taskID == "" {
		if taskID, err = membership.NewID(); err != nil {
			return Task{}, err
		}
	}
	if !membership.ValidateUUID(taskID) {
		return Task{}, ErrInvalidTask
	}
	var version int64
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.tasks (
			organization_id, id, space_id, sequence, type_id, status_id, title,
			description, acceptance, assignee_id, executor_agent_id, priority,
			start_date, due_date, parent_task_id, rank, estimate, iteration_id, project_id,
			triage_entered_at, triage_source
		) VALUES (
			openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3,
			$4::uuid, $5::uuid, $6, $7, $8, NULLIF($9, '')::uuid,
			NULLIF($10, '')::uuid, $11, NULLIF($12, '')::date, NULLIF($13, '')::date,
			NULLIF($14, '')::uuid, $15, round($16::float8::numeric, 2), NULLIF($17, '')::uuid, NULLIF($18, '')::uuid,
			CASE WHEN $19 <> '' THEN clock_timestamp() END, NULLIF($19, '')
		) RETURNING version`, taskID, spaceID, sequence, typeID, statusID, input.Title, input.Description,
		input.Acceptance, firstOfKind(desiredAssignees, "HUMAN"), firstOfKind(desiredAssignees, "AGENT"),
		priority, startDate, dueDate, parentID, rank, input.Estimate, input.IterationID, input.ProjectID, triageSource).Scan(&version)
	if err != nil {
		return Task{}, mapDatabaseError(err)
	}
	writer := newActivityWriter(tx, spaceID, taskID, actor.UserID, input.RequestID)
	created := map[string]any{"title": truncateRunes(input.Title, 200)}
	if input.SourceCommentID != "" {
		created["source_comment_id"] = input.SourceCommentID
	}
	if err := writer.add(ctx, "task.created", created); err != nil {
		return Task{}, err
	}
	if _, err := s.writeAssignees(ctx, tx, newDiscardWriter(), spaceID, taskID, actor, nil, desiredAssignees, !input.DeferAssignedAgent); err != nil {
		return Task{}, err
	}
	if err := addHumanFollower(ctx, tx, spaceID, taskID, actor.UserID, actor.UserID); err != nil {
		return Task{}, err
	}
	if err := s.afterAssigneesAdded(ctx, tx, spaceID, taskID, actor, desiredAssignees, version); err != nil {
		return Task{}, err
	}
	if err := s.writeLabelSet(ctx, tx, newDiscardWriter(), spaceID, taskID, nil, labelIDs); err != nil {
		return Task{}, err
	}
	if err := s.finishTriage(ctx, tx, taskID, writer, entry); err != nil {
		return Task{}, err
	}
	if len(input.FieldValues) > 0 {
		if err := s.writeBoundFieldValues(ctx, tx, newDiscardWriter(), organizationID, spaceID, taskID, typeID, actor, input.FieldValues); err != nil {
			return Task{}, err
		}
	}
	_ = space
	return s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
}

// newDiscardWriter 用于创建任务时不重复记录 assignee_added/label_added（task.created 已覆盖）。
func newDiscardWriter() *activityWriter { return &activityWriter{discard: true} }

func (s Service) UpdateTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, input TaskUpdateInput) (Task, error) {
	if input.Version == 0 {
		return Task{}, ErrInvalidVersion
	}
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Task{}, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return Task{}, ErrTaskNotFound
	}
	if (input.Assignees != nil) && (input.AssigneeUserID != nil || input.ExecutorAgentID != nil) {
		return Task{}, ErrInvalidTask
	}
	if input.LabelIDs != nil && input.Labels != nil {
		return Task{}, ErrInvalidTask
	}
	assignmentConversation, err := NormalizeAssignmentConversation(input.AssignmentConversation)
	if err != nil {
		return Task{}, err
	}
	newParent := ""
	if input.ParentTaskID.Set {
		newParent = strings.ToLower(optionalText(input.ParentTaskID))
		if newParent != "" && (!membership.ValidateUUID(newParent) || newParent == taskID) {
			return Task{}, errInvalidParent
		}
	}
	if (input.TypeID != nil && !membership.ValidateUUID(*input.TypeID)) || (input.StatusID != nil && !membership.ValidateUUID(*input.StatusID)) {
		return Task{}, ErrInvalidWorkflow
	}
	// 改父任务时先取空间级事务锁，让同一空间内的层级变更串行执行：
	// 只锁自身与新父任务只能挡住「互设父任务」，挡不住 A→B、C→D 并发闭合成更长的环。
	if newParent != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('openfox.task_hierarchy:' || $1, 0))`, spaceID); err != nil {
			return Task{}, err
		}
	}
	// 设置非空估时时先以 FOR KEY SHARE 锁空间行再锁任务：与修改估时设置（先 FOR UPDATE 空间、再改任务）锁序一致，
	// 保证校验用的刻度在本事务提交前不会被并发修改。
	var estimation SpaceEstimation
	if input.Estimate.Set && input.Estimate.Value != nil {
		var err error
		if estimation, err = loadSpaceEstimation(ctx, tx, spaceID, "FOR KEY SHARE"); err != nil {
			if errors.Is(err, ErrSpaceNotFound) {
				return Task{}, ErrTaskNotFound
			}
			return Task{}, err
		}
		if err := ValidateEstimate(estimation, *input.Estimate.Value); err != nil {
			return Task{}, err
		}
	}
	// 按 id 排序锁住自身与新父任务，并发互设父任务时第二个事务会在锁释放后看到已提交的父子关系。
	lockIDs := []string{taskID}
	if newParent != "" {
		lockIDs = append(lockIDs, newParent)
	}
	lockedRows, err := tx.Query(ctx, `
		SELECT id::text, space_id::text, version FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND deleted_at IS NULL
		 ORDER BY id FOR UPDATE`, lockIDs)
	if err != nil {
		return Task{}, err
	}
	locked := make(map[string][2]string)
	var lockedVersion = make(map[string]int64)
	for lockedRows.Next() {
		var id, taskSpace string
		var version int64
		if err := lockedRows.Scan(&id, &taskSpace, &version); err != nil {
			lockedRows.Close()
			return Task{}, err
		}
		locked[id] = [2]string{id, taskSpace}
		lockedVersion[id] = version
	}
	lockedRows.Close()
	if err := lockedRows.Err(); err != nil {
		return Task{}, err
	}
	if self, ok := locked[taskID]; !ok || self[1] != spaceID {
		return Task{}, ErrTaskNotFound
	}
	if uint64(lockedVersion[taskID]) != input.Version {
		return Task{}, ErrVersionConflict
	}
	if newParent != "" {
		if parent, ok := locked[newParent]; !ok || parent[1] != spaceID {
			return Task{}, errInvalidParent
		}
	}
	current, err := s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
	if err != nil {
		return Task{}, err
	}
	typeID := current.TypeID
	if input.TypeID != nil {
		typeID = *input.TypeID
	}
	statusID := current.StatusID
	if input.StatusID != nil {
		statusID = *input.StatusID
	}
	flow, err := loadWorkflow(ctx, tx, organizationID, spaceID, typeID)
	if err != nil {
		return Task{}, err
	}
	if !flow.contains(statusID) {
		return Task{}, ErrInvalidWorkflow
	}
	title := current.Title
	if input.Title != nil {
		title = strings.TrimSpace(*input.Title)
		if title == "" || len([]rune(title)) > 500 {
			return Task{}, ErrInvalidTask
		}
	}
	description := current.Description
	if input.Description != nil {
		description = *input.Description
		if len([]rune(description)) > 10000 {
			return Task{}, ErrInvalidTask
		}
	}
	acceptance := current.Acceptance
	if input.Acceptance != nil {
		acceptance = *input.Acceptance
		if len([]rune(acceptance)) > 10000 {
			return Task{}, ErrInvalidTask
		}
	}
	priority := current.Priority
	if input.Priority != nil {
		if priority, err = ParsePriority(*input.Priority); err != nil {
			return Task{}, err
		}
	}
	startDate, dueDate := current.StartDate, current.DueDate
	if input.StartDate.Set {
		if startDate, err = parseDate(optionalText(input.StartDate)); err != nil {
			return Task{}, err
		}
	}
	if input.DueDate.Set {
		if dueDate, err = parseDate(optionalText(input.DueDate)); err != nil {
			return Task{}, err
		}
	}
	if err := checkDateRange(startDate, dueDate); err != nil {
		return Task{}, err
	}
	parentID := current.ParentTaskID
	if input.ParentTaskID.Set {
		parentID = newParent
		if newParent != "" && newParent != current.ParentTaskID {
			if err := s.checkParent(ctx, tx, spaceID, taskID, newParent); err != nil {
				return Task{}, err
			}
		}
	}
	// 目标处理人列表：显式 assignees，或把旧接口的单人字段映射为对第一个同类处理人的替换。
	desired := make([]TaskAssigneeRef, 0, len(current.Assignees)+2)
	for _, item := range current.Assignees {
		desired = append(desired, TaskAssigneeRef{Kind: item.Kind, ID: item.ID})
	}
	assigneesChanged := false
	if input.Assignees != nil {
		if desired, err = normalizeAssignees(*input.Assignees); err != nil {
			return Task{}, err
		}
		assigneesChanged = true
	} else {
		if input.AssigneeUserID != nil {
			desired, assigneesChanged = replaceFirstOfKind(desired, "HUMAN", strings.ToLower(strings.TrimSpace(*input.AssigneeUserID)), assigneesChanged)
		}
		if input.ExecutorAgentID != nil {
			desired, assigneesChanged = replaceFirstOfKind(desired, "AGENT", strings.ToLower(strings.TrimSpace(*input.ExecutorAgentID)), assigneesChanged)
		}
		if desired, err = normalizeAssignees(desired); err != nil {
			return Task{}, err
		}
	}
	rank := ""
	if input.Position != nil {
		beforeID := strings.ToLower(strings.TrimSpace(input.Position.BeforeTaskID))
		afterID := strings.ToLower(strings.TrimSpace(input.Position.AfterTaskID))
		for _, id := range []string{beforeID, afterID} {
			if id != "" && (!membership.ValidateUUID(id) || id == taskID) {
				return Task{}, errInvalidPosition
			}
		}
		if rank, err = resolveRank(ctx, tx, spaceID, taskID, afterID, beforeID); err != nil {
			return Task{}, err
		}
	}
	var labelIDs []string
	labelsChanged := false
	if input.LabelIDs != nil {
		if labelIDs, err = normalizeLabelIDs(*input.LabelIDs); err != nil {
			return Task{}, err
		}
		labelsChanged = true
	} else if input.Labels != nil {
		if labelIDs, err = s.resolveLabelNames(ctx, tx, spaceID, *input.Labels); err != nil {
			return Task{}, err
		}
		labelsChanged = true
	}
	writer := newActivityWriter(tx, spaceID, taskID, actor.UserID, input.RequestID)
	var added []TaskAssigneeRef
	if assigneesChanged {
		if added, err = s.writeAssignees(ctx, tx, writer, spaceID, taskID, actor, current.Assignees, desired, true); err != nil {
			return Task{}, err
		}
		// 新开对话运行：与 ASSIGNED 事件同一事务降级旧指派会话。
		if assignmentConversation == AssignmentConversationNew {
			for _, ref := range added {
				if ref.Kind != "AGENT" {
					continue
				}
				if err := demoteAssignmentConversation(ctx, tx, taskID, ref.ID); err != nil {
					return Task{}, err
				}
			}
		}
	}
	if labelsChanged {
		if err := s.writeLabelSet(ctx, tx, writer, spaceID, taskID, current.LabelItems, labelIDs); err != nil {
			return Task{}, err
		}
	}
	iterationID := current.IterationID
	if input.IterationID.Set {
		iterationID = optionalText(input.IterationID)
		if iterationID != "" {
			if err := validateTaskIteration(ctx, tx, spaceID, iterationID); err != nil {
				return Task{}, err
			}
		}
	}
	if !input.IterationID.Set && iterationID == "" && statusID != current.StatusID {
		if iterationID, err = autoAddIteration(ctx, tx, spaceID, statusID); err != nil {
			return Task{}, err
		}
	}
	projectID := current.ProjectID
	if input.ProjectID.Set {
		projectID = optionalText(input.ProjectID)
		if projectID != "" && projectID != current.ProjectID {
			if err := validateTaskProject(ctx, tx, spaceID, projectID); err != nil {
				return Task{}, err
			}
		}
	}
	estimate := current.Estimate
	if input.Estimate.Set {
		estimate = input.Estimate.Value
	}
	assigneeProjection, agentProjection := current.AssigneeUserID, current.ExecutorAgentID
	if assigneesChanged {
		assigneeProjection, agentProjection = firstOfKind(desired, "HUMAN"), firstOfKind(desired, "AGENT")
	}
	var version int64
	err = tx.QueryRow(ctx, `
		UPDATE openfox.tasks
		   SET type_id = $1::uuid, status_id = $2::uuid, title = $3, description = $4, acceptance = $5,
		       assignee_id = NULLIF($6, '')::uuid, executor_agent_id = NULLIF($7, '')::uuid,
		       priority = $8, start_date = NULLIF($9, '')::date, due_date = NULLIF($10, '')::date,
		       parent_task_id = NULLIF($11, '')::uuid, rank = COALESCE(NULLIF($12, ''), rank),
		       estimate = round($16::float8::numeric, 2), iteration_id = NULLIF($17, '')::uuid, project_id = NULLIF($18, '')::uuid,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $13::uuid AND id = $14::uuid AND version = $15 AND deleted_at IS NULL
		RETURNING version`, typeID, statusID, title, description, acceptance, assigneeProjection, agentProjection,
		priority, startDate, dueDate, parentID, rank, spaceID, taskID, int64(input.Version), estimate, iterationID, projectID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrVersionConflict
	}
	if err != nil {
		return Task{}, mapDatabaseError(err)
	}
	if err := s.afterAssigneesAdded(ctx, tx, spaceID, taskID, actor, added, version); err != nil {
		return Task{}, err
	}
	if statusID != current.StatusID {
		if err := syncTriageAfterStatusChange(ctx, tx, taskID); err != nil {
			return Task{}, err
		}
	}
	changes := []struct {
		kind    string
		changed bool
		diff    map[string]any
	}{
		{"task.title_changed", title != current.Title, map[string]any{"from": truncateRunes(current.Title, 200), "to": truncateRunes(title, 200)}},
		{"task.description_changed", description != current.Description, nil},
		{"task.status_changed", statusID != current.StatusID, map[string]any{"from_status_id": current.StatusID, "to_status_id": statusID}},
		{"task.priority_changed", priority != current.Priority, map[string]any{"from": current.Priority, "to": priority}},
		{"task.type_changed", typeID != current.TypeID, map[string]any{"from": current.TypeID, "to": typeID}},
		{"task.start_date_changed", startDate != current.StartDate, map[string]any{"from": nullIfEmpty(current.StartDate), "to": nullIfEmpty(startDate)}},
		{"task.due_date_changed", dueDate != current.DueDate, map[string]any{"from": nullIfEmpty(current.DueDate), "to": nullIfEmpty(dueDate)}},
		{"task.parent_changed", parentID != current.ParentTaskID, map[string]any{"from": nullIfEmpty(current.ParentTaskID), "to": nullIfEmpty(parentID)}},
		{"task.iteration_changed", iterationID != current.IterationID, map[string]any{"from_iteration_id": nullIfEmpty(current.IterationID), "to_iteration_id": nullIfEmpty(iterationID)}},
		{"task.project_changed", projectID != current.ProjectID, map[string]any{"from_project_id": nullIfEmpty(current.ProjectID), "to_project_id": nullIfEmpty(projectID)}},
		{"task.estimate_changed", !sameEstimate(estimate, current.Estimate), map[string]any{"from": current.Estimate, "to": estimate}},
	}
	for _, change := range changes {
		if change.changed {
			if err := writer.add(ctx, change.kind, change.diff); err != nil {
				return Task{}, err
			}
		}
	}
	return s.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
}

// syncTriageAfterStatusChange 在状态改变后同步分拣字段：状态离开 TRIAGE 分类时清空分拣字段；
// 改成 TRIAGE 时重新计入分拣台（不重跑规则）。更新任务与恢复任务共用。
func syncTriageAfterStatusChange(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.tasks t
		   SET triage_entered_at = CASE WHEN ts.category = 'TRIAGE' THEN clock_timestamp() END,
		       triage_source = CASE WHEN ts.category = 'TRIAGE' THEN 'member' END,
		       triage_snoozed_until = NULL
		  FROM openfox.task_statuses ts
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid
		   AND ts.organization_id = t.organization_id AND ts.id = t.status_id
		   AND (t.triage_entered_at IS NOT NULL OR ts.category = 'TRIAGE')`, taskID)
	return err
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// replaceFirstOfKind 把 desired 中第一个指定类型的处理人替换为 id（空串表示移除）；
// 与现状相同则原样返回。
func replaceFirstOfKind(desired []TaskAssigneeRef, kind, id string, changed bool) ([]TaskAssigneeRef, bool) {
	if firstOfKind(desired, kind) == id {
		return desired, changed
	}
	result := make([]TaskAssigneeRef, 0, len(desired)+1)
	replaced := false
	for _, ref := range desired {
		if ref.Kind == kind && !replaced {
			replaced = true
			if id != "" {
				result = append(result, TaskAssigneeRef{Kind: kind, ID: id})
			}
			continue
		}
		result = append(result, ref)
	}
	if !replaced && id != "" {
		result = append(result, TaskAssigneeRef{Kind: kind, ID: id})
	}
	return result, true
}

// DeleteTask 软删除任务：有进行中的 Run 时拒绝；子任务脱离父任务（恢复父任务时不挂回）；
// 删除后进入空间「最近删除」，由 RestoreTask 恢复。成功后任务 version = 请求的 version + 1。
func (s Service) DeleteTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, version uint64, requestID string) error {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return err
	}
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if version == 0 {
		return ErrInvalidVersion
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return ErrTaskNotFound
	}
	// 自身与直接子任务按 id 顺序加锁，避免与「改父任务」的事务互相等待。
	rows, err := tx.Query(ctx, `
		SELECT id::text, space_id::text, version FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND (id = $1::uuid OR parent_task_id = $1::uuid) AND deleted_at IS NULL
		 ORDER BY id FOR UPDATE`, taskID)
	if err != nil {
		return err
	}
	found := false
	var currentVersion int64
	for rows.Next() {
		var id, taskSpace string
		var rowVersion int64
		if err := rows.Scan(&id, &taskSpace, &rowVersion); err != nil {
			rows.Close()
			return err
		}
		if id == taskID && taskSpace == spaceID {
			found, currentVersion = true, rowVersion
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return ErrTaskNotFound
	}
	if uint64(currentVersion) != version {
		return ErrVersionConflict
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM openfox.runs
			 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
			   AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED'))`, taskID).Scan(&active); err != nil {
		return err
	}
	if active {
		return errRunActive
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks SET parent_task_id = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND parent_task_id = $1::uuid AND deleted_at IS NULL`, taskID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.tasks
		   SET deleted_at = clock_timestamp(), deleted_by = $3::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND version = $4 AND deleted_at IS NULL`,
		spaceID, taskID, actor.UserID, int64(version))
	if err != nil {
		return mapDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, "task.deleted", nil)
}

// requireLiveTask 确认任务存在、未删除且属于指定空间；子资源写接口用它统一返回 404。
func (s Service) requireLiveTask(ctx context.Context, tx pgx.Tx, spaceID, taskID string) error {
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return ErrTaskNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL)`, spaceID, taskID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrTaskNotFound
	}
	return nil
}

// sameEstimate 比较两个估时（nil = 未估时）。
func sameEstimate(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
