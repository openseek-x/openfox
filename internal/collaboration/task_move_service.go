package collaboration

// 单任务「移动到空间」的服务层（预览、准备、执行、幂等重放）。接口层负责鉴权（源空间删除权限、
// 目标空间创建权限）与取消运行；本文件负责加锁、校验与改写。
//
// 锁序：层级事务锁（两个空间按 id 升序）→ 空间行（按 id 升序；源空间 FOR KEY SHARE、目标空间 FOR UPDATE）
// → Run（dispatch.CancelTaskRuns）→ 任务（FOR UPDATE，按 id）。
//   - 层级锁在前，与「改父任务」（UpdateTask：层级锁 → 空间 → 任务）一致；
//   - 源空间只要 FOR KEY SHARE：挡住停用 / 删除（FOR UPDATE），但不挡建任务（递增序号是 NO KEY UPDATE），
//     避免与「建子任务」（先锁父任务、再递增序号）互等；
//   - 停用空间的移动（PrepareRetire）也按 id 升序锁两个空间行，反方向的移动与停用不会死锁。

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// ErrMoveNeedsConfirmation：任务（含子任务）指派了 Agent / 小队或有活动运行，请求没有带 cancel_runs: true（D3）。
var ErrMoveNeedsConfirmation = NewRuleError(http.StatusConflict, "TASK_HAS_ACTIVE_RUNS",
	"the task has Agent assignees or active runs; confirm with cancel_runs to cancel them")

// MoveTaskInput 是 POST …/move 的输入。
type MoveTaskInput struct {
	TargetSpaceID string
	Version       uint64
	CancelRuns    bool
	RequestID     string
}

// MoveTaskPlan 是 PrepareMoveTask 的结果：空间已锁、版本已校验。调用方随后取消运行，再调用 ApplyMoveTask。
type MoveTaskPlan struct {
	SourceID, TargetID, TaskID string
	Version                    uint64
	Tasks                      []movingTask
	// AgentAssignees：子树上 Agent / 小队处理人的数量；ActiveRuns：QUEUED / PREPARING / RUNNING 运行数。
	AgentAssignees, ActiveRuns int
}

// TaskIDs 返回移动集合的任务 ID（根任务在前）。
func (p MoveTaskPlan) TaskIDs() []string {
	ids := make([]string, len(p.Tasks))
	for index, task := range p.Tasks {
		ids[index] = task.ID
	}
	return ids
}

// NeedsConfirmation 表示需要 cancel_runs: true 才能执行。
func (p MoveTaskPlan) NeedsConfirmation() bool { return p.AgentAssignees > 0 || p.ActiveRuns > 0 }

// normalizeMoveIDs 校验并规整源空间、任务、目标空间 ID。目标等于源空间、格式不对一律 INVALID_TARGET_SPACE。
func normalizeMoveIDs(spaceID, taskID, targetID string) (string, string, string, error) {
	spaceID, taskID = strings.ToLower(spaceID), strings.ToLower(taskID)
	targetID = strings.ToLower(strings.TrimSpace(targetID))
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return "", "", "", ErrTaskNotFound
	}
	if !membership.ValidateUUID(targetID) || targetID == spaceID {
		return "", "", "", ErrInvalidTarget
	}
	return spaceID, taskID, targetID, nil
}

// lockMoveSpaces 按锁序取层级锁与两个空间行（见文件头），并校验源空间未停用、目标空间可用。
func lockMoveSpaces(ctx context.Context, tx pgx.Tx, sourceID, targetID string) error {
	ordered := []string{sourceID, targetID}
	sort.Strings(ordered)
	for _, id := range ordered {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('openfox.task_hierarchy:' || $1, 0))`, id); err != nil {
			return err
		}
	}
	for _, id := range ordered {
		lock := "FOR UPDATE"
		if id == sourceID {
			lock = "FOR KEY SHARE"
		}
		var status string
		err := tx.QueryRow(ctx, `
			SELECT status FROM openfox.spaces
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL
			 `+lock, id).Scan(&status)
		switch {
		case errors.Is(err, pgx.ErrNoRows) && id == sourceID:
			return ErrSpaceNotFound
		case errors.Is(err, pgx.ErrNoRows):
			return ErrInvalidTarget
		case err != nil:
			return err
		case status != "ACTIVE" && id == sourceID:
			return ErrSpaceRetired
		case status != "ACTIVE":
			return ErrInvalidTarget
		}
	}
	return nil
}

// ConfirmationError 返回带数量的 409。
func (p MoveTaskPlan) ConfirmationError() error {
	return ErrMoveNeedsConfirmation.WithDetails(map[string]any{
		"active_runs": p.ActiveRuns, "agent_assignees": p.AgentAssignees, "tasks_count": len(p.Tasks),
	})
}

// PrepareMoveTask 加锁并校验：任务存在且属于源空间、版本一致、两个空间可用；统计 Agent 处理人与活动运行。
// 移动集合 = 根任务 + 未删除的全部后代（D2）；根任务有父任务时与父任务解除关系。
func (s Service) PrepareMoveTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, input MoveTaskInput) (MoveTaskPlan, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return MoveTaskPlan{}, err
	}
	if input.Version == 0 {
		return MoveTaskPlan{}, ErrInvalidVersion
	}
	spaceID, taskID, targetID, err := normalizeMoveIDs(spaceID, taskID, input.TargetSpaceID)
	if err != nil {
		return MoveTaskPlan{}, err
	}
	if err := lockMoveSpaces(ctx, tx, spaceID, targetID); err != nil {
		return MoveTaskPlan{}, err
	}
	tasks, err := loadMovingSubtree(ctx, tx, spaceID, taskID)
	if err != nil {
		return MoveTaskPlan{}, err
	}
	if len(tasks) == 0 {
		return MoveTaskPlan{}, ErrTaskNotFound
	}
	if uint64(tasks[0].Version) != input.Version {
		return MoveTaskPlan{}, ErrVersionConflict
	}
	plan := MoveTaskPlan{SourceID: spaceID, TargetID: targetID, TaskID: taskID, Version: input.Version, Tasks: tasks}
	err = tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM openfox.task_assignees
		         WHERE organization_id = openfox_private.current_organization_id()
		           AND task_id = ANY($1::uuid[]) AND kind <> 'HUMAN')::int,
		       (SELECT count(*) FROM openfox.runs
		         WHERE organization_id = openfox_private.current_organization_id()
		           AND task_id = ANY($1::uuid[]) AND state IN ('QUEUED', 'PREPARING', 'RUNNING'))::int`,
		plan.TaskIDs()).Scan(&plan.AgentAssignees, &plan.ActiveRuns)
	return plan, err
}

// MoveTaskResult 是移动结果（任务对象由调用方按 SpaceID 重新读取）。
type MoveTaskResult struct {
	SpaceID, DisplayID, PreviousDisplayID string
	MovedCount, CanceledRuns              int
}

// ApplyMoveTask 锁住移动集合并改写（须在 PrepareMoveTask 与取消运行之后、同一事务内调用）。
func (s Service) ApplyMoveTask(ctx context.Context, tx pgx.Tx, actor Actor, plan MoveTaskPlan, canceledRuns int, requestID string) (MoveTaskResult, error) {
	ids := plan.TaskIDs()
	rows, err := tx.Query(ctx, `
		SELECT id::text, space_id::text, version, deleted_at IS NULL FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])
		 ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return MoveTaskResult{}, err
	}
	locked := 0
	for rows.Next() {
		var id, space string
		var version int64
		var live bool
		if err := rows.Scan(&id, &space, &version, &live); err != nil {
			rows.Close()
			return MoveTaskResult{}, err
		}
		if space != plan.SourceID || !live || (id == plan.TaskID && uint64(version) != plan.Version) {
			rows.Close()
			return MoveTaskResult{}, ErrVersionConflict
		}
		locked++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return MoveTaskResult{}, err
	}
	if locked != len(ids) {
		return MoveTaskResult{}, ErrVersionConflict
	}
	moved, err := moveTasks(ctx, tx, moveTasksInput{SourceID: plan.SourceID, TargetID: plan.TargetID, Tasks: plan.Tasks,
		Single: true, ActorID: actor.UserID, RequestID: requestID, CanceledRuns: canceledRuns})
	if err != nil {
		return MoveTaskResult{}, err
	}
	if len(moved.Sequences) != len(ids) {
		return MoveTaskResult{}, ErrConflict
	}
	return s.MoveReplayResult(ctx, tx, plan.TaskID)
}

// MoveReplayResult 按根任务最近一条 task.moved 动态还原移动结果（执行后与幂等重放共用）。
// 重放时任务可能已被再次移动：SpaceID 是任务当前空间，编号与计数取最近一次移动的动态。
func (s Service) MoveReplayResult(ctx context.Context, tx pgx.Tx, taskID string) (MoveTaskResult, error) {
	var result MoveTaskResult
	var moved, canceled *int
	err := tx.QueryRow(ctx, `
		SELECT t.space_id::text, COALESCE(sp.prefix, '') || '-' || t.sequence::text,
		       COALESCE(a.safe_diff->>'from_display_id', ''),
		       (a.safe_diff->>'moved_count')::int, (a.safe_diff->>'canceled_runs')::int
		  FROM openfox.tasks t
		  JOIN openfox.spaces sp ON sp.organization_id = t.organization_id AND sp.id = t.space_id
		  LEFT JOIN LATERAL (
		        SELECT x.safe_diff FROM openfox.task_activities x
		         WHERE x.organization_id = t.organization_id AND x.task_id = t.id AND x.kind = 'task.moved'
		           AND x.safe_diff ? 'moved_count'
		         ORDER BY x.created_at DESC, x.id DESC LIMIT 1) a ON true
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid
		   AND t.deleted_at IS NULL`, taskID).Scan(&result.SpaceID, &result.DisplayID, &result.PreviousDisplayID, &moved, &canceled)
	if errors.Is(err, pgx.ErrNoRows) {
		return MoveTaskResult{}, ErrTaskNotFound
	}
	if err != nil {
		return MoveTaskResult{}, err
	}
	if moved != nil {
		result.MovedCount = *moved
	}
	if canceled != nil {
		result.CanceledRuns = *canceled
	}
	return result, nil
}

// MoveNamed 是预览里的一个「名称 + ID」值。
type MoveNamed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MovePreviewTask 是子树里一个任务的映射预览。DisplayIDHint 只是提示，真正编号在执行时分配。
type MovePreviewTask struct {
	TaskID        string      `json:"task_id"`
	Title         string      `json:"title"`
	DisplayID     string      `json:"display_id"`
	DisplayIDHint string      `json:"display_id_hint"`
	TypeFrom      MoveNamed   `json:"type_from"`
	TypeTo        MoveNamed   `json:"type_to"`
	StatusFrom    MoveNamed   `json:"status_from"`
	StatusTo      MoveNamed   `json:"status_to"`
	EstimateFrom  *float64    `json:"estimate_from"`
	EstimateTo    *float64    `json:"estimate_to"`
	Cleared       []string    `json:"cleared"`
	DroppedLabels []MoveNamed `json:"dropped_labels"`
}

// MovePreviewAssignee 是将被移除的 Agent / 小队处理人。
type MovePreviewAssignee struct {
	TaskID string `json:"task_id"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

// MovePreviewIssue 是将解除同步的 Issue 链接（远端 Issue 不动，D6）。
type MovePreviewIssue struct {
	TaskID     string `json:"task_id"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
}

// MovePreview 是 POST …/move-preview 的结果。
type MovePreview struct {
	TargetSpaceID        string                `json:"target_space_id"`
	TasksCount           int                   `json:"tasks_count"`
	Tasks                []MovePreviewTask     `json:"tasks"`
	DetachedParent       *MoveNamed            `json:"detached_parent"`
	RemovedAssignees     []MovePreviewAssignee `json:"removed_assignees"`
	ActiveRuns           int                   `json:"active_runs"`
	UnlinkedIssues       []MovePreviewIssue    `json:"unlinked_issues"`
	RequiresConfirmation bool                  `json:"requires_confirmation"`
}

// PreviewMoveTask 只读地计算移动的影响（不加锁；执行时按当时的数据重新计算）。
func (s Service) PreviewMoveTask(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, targetSpaceID string) (MovePreview, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return MovePreview{}, err
	}
	spaceID, taskID, targetID, err := normalizeMoveIDs(spaceID, taskID, targetSpaceID)
	if err != nil {
		return MovePreview{}, err
	}
	var sourcePrefix, targetPrefix string
	var next int64
	err = tx.QueryRow(ctx, `
		SELECT src.prefix, dst.prefix, dst.next_task_number
		  FROM openfox.spaces src, openfox.spaces dst
		 WHERE src.organization_id = openfox_private.current_organization_id() AND src.id = $1::uuid AND src.deleted_at IS NULL
		   AND dst.organization_id = src.organization_id AND dst.id = $2::uuid AND dst.deleted_at IS NULL
		   AND dst.status = 'ACTIVE'`, spaceID, targetID).Scan(&sourcePrefix, &targetPrefix, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return MovePreview{}, ErrInvalidTarget
	}
	if err != nil {
		return MovePreview{}, err
	}
	tasks, err := loadMovingSubtree(ctx, tx, spaceID, taskID)
	if err != nil {
		return MovePreview{}, err
	}
	if len(tasks) == 0 {
		return MovePreview{}, ErrTaskNotFound
	}
	target, err := loadMoveTarget(ctx, tx, targetID, true)
	if err != nil {
		return MovePreview{}, err
	}
	preview := MovePreview{TargetSpaceID: targetID, TasksCount: len(tasks)}
	if err := s.fillPreviewTasks(ctx, tx, &preview, tasks, target, sourcePrefix, targetPrefix, next); err != nil {
		return MovePreview{}, err
	}
	if err := fillPreviewSideEffects(ctx, tx, &preview, tasks); err != nil {
		return MovePreview{}, err
	}
	preview.RequiresConfirmation = len(preview.RemovedAssignees) > 0 || preview.ActiveRuns > 0
	return preview, nil
}

func (s Service) fillPreviewTasks(ctx context.Context, tx pgx.Tx, preview *MovePreview, tasks []movingTask, target moveTarget,
	sourcePrefix, targetPrefix string, next int64) error {
	statusNames, err := loadStatusNames(ctx, tx)
	if err != nil {
		return err
	}
	moved := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		moved[task.ID] = true
	}
	for index, task := range tasks {
		mapping := mapTask(task, target, moved, true)
		item := MovePreviewTask{
			TaskID: task.ID, Title: task.Title, DisplayID: displayID(sourcePrefix, task.Sequence),
			DisplayIDHint: displayID(targetPrefix, next+int64(index)),
			TypeFrom:      MoveNamed{ID: task.TypeID, Name: task.TypeName}, TypeTo: MoveNamed{ID: mapping.TypeID, Name: mapping.TypeName},
			StatusFrom:   MoveNamed{ID: task.StatusID, Name: statusNames[task.StatusID]},
			StatusTo:     MoveNamed{ID: mapping.StatusID, Name: statusNames[mapping.StatusID]},
			EstimateFrom: task.Estimate, EstimateTo: mapping.Estimate, Cleared: []string{}, DroppedLabels: []MoveNamed{},
		}
		if task.IterationID != "" {
			item.Cleared = append(item.Cleared, "iteration")
		}
		if task.ProjectID != "" && mapping.ProjectID == "" {
			item.Cleared = append(item.Cleared, "project")
		}
		if task.Estimate != nil && mapping.Estimate == nil {
			item.Cleared = append(item.Cleared, "estimate")
		}
		if mapping.ParentDetached {
			item.Cleared = append(item.Cleared, "parent")
		}
		if task.InTriage {
			item.Cleared = append(item.Cleared, "triage")
		}
		preview.Tasks = append(preview.Tasks, item)
	}
	if root := tasks[0]; root.ParentID != "" {
		var title string
		if err := tx.QueryRow(ctx, `SELECT title FROM openfox.tasks WHERE organization_id = openfox_private.current_organization_id()
			AND id = $1::uuid`, root.ParentID).Scan(&title); err != nil {
			return err
		}
		preview.DetachedParent = &MoveNamed{ID: root.ParentID, Name: title}
	}
	return nil
}

func loadStatusNames(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name FROM openfox.task_statuses
		WHERE organization_id = openfox_private.current_organization_id()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// fillPreviewSideEffects 补充丢弃的空间标签、移除的 Agent / 小队处理人、活动运行数与要解除的 Issue 同步。
func fillPreviewSideEffects(ctx context.Context, tx pgx.Tx, preview *MovePreview, tasks []movingTask) error {
	ids := make([]string, len(tasks))
	index := map[string]int{}
	for position, task := range tasks {
		ids[position], index[task.ID] = task.ID, position
	}
	rows, err := tx.Query(ctx, `
		SELECT tl.task_id::text, l.id::text, l.name FROM openfox.task_labels tl
		  JOIN openfox.labels l ON l.organization_id = tl.organization_id AND l.id = tl.label_id
		 WHERE tl.organization_id = openfox_private.current_organization_id() AND tl.task_id = ANY($1::uuid[])
		   AND l.space_id IS NOT NULL
		 ORDER BY l.name, l.id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID string
		var label MoveNamed
		if err := rows.Scan(&taskID, &label.ID, &label.Name); err != nil {
			rows.Close()
			return err
		}
		item := &preview.Tasks[index[taskID]]
		item.DroppedLabels = append(item.DroppedLabels, label)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return fillPreviewAssigneesAndIssues(ctx, tx, preview, ids)
}

func fillPreviewAssigneesAndIssues(ctx context.Context, tx pgx.Tx, preview *MovePreview, ids []string) error {
	preview.RemovedAssignees, preview.UnlinkedIssues = []MovePreviewAssignee{}, []MovePreviewIssue{}
	rows, err := tx.Query(ctx, `
		SELECT a.task_id::text, a.kind, COALESCE(a.agent_id, a.crew_id)::text, COALESCE(ag.name, c.name, '')
		  FROM openfox.task_assignees a
		  LEFT JOIN openfox.agents ag ON ag.organization_id = a.organization_id AND ag.id = a.agent_id
		  LEFT JOIN openfox.agent_crews c ON c.organization_id = a.organization_id AND c.id = a.crew_id
		 WHERE a.organization_id = openfox_private.current_organization_id() AND a.task_id = ANY($1::uuid[])
		   AND a.kind <> 'HUMAN'
		 ORDER BY a.task_id, a.created_at, a.id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item MovePreviewAssignee
		if err := rows.Scan(&item.TaskID, &item.Kind, &item.ID, &item.Name); err != nil {
			rows.Close()
			return err
		}
		preview.RemovedAssignees = append(preview.RemovedAssignees, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `
		SELECT task_id::text, repository, issue_number, url FROM openfox.task_issue_links
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = ANY($1::uuid[])
		   AND unlinked_at IS NULL
		 ORDER BY task_id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var item MovePreviewIssue
		if err := rows.Scan(&item.TaskID, &item.Repository, &item.Number, &item.URL); err != nil {
			rows.Close()
			return err
		}
		preview.UnlinkedIssues = append(preview.UnlinkedIssues, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `
		SELECT count(*)::int FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = ANY($1::uuid[])
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING')`, ids).Scan(&preview.ActiveRuns)
}
