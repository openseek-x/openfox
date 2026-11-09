package collaboration

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 任务批量修改：同一空间内 1–100 个任务、一次只改一类字段，单个事务内要么全部成功要么全部失败。
//
// 每个任务都走 UpdateTask（校验、字段级动态、通知、Agent 指派事件、自动化 / Issue 同步 / 实时事件触发器全部复用），
// 这里只负责：请求规范化、按任务 id 排序后一次性加锁并预检（不存在 / 版本 / 工作流），以及把单任务补丁换算成
// UpdateTask 的输入。失败时返回 *BatchTaskError，接口层据此在 error.details.task_ids 里带出问题任务。

// MaxBatchTasks 是一次批量修改的任务数上限。
const MaxBatchTasks = 100

// BatchTaskRef 是批量请求里的一个任务及调用方看到的版本。
type BatchTaskRef struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

// BatchAssigneePatch 替换某一类处理人：同一类只保留一个；ID 为 null 表示清空该类。
type BatchAssigneePatch struct {
	Kind string         `json:"kind"`
	ID   OptionalString `json:"id"`
}

// BatchLabelPatch 在每个任务现有标签上增删；同一个 id 不能同时出现在两边。
type BatchLabelPatch struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// BatchTaskPatch 必须恰好给出一个字段。
type BatchTaskPatch struct {
	StatusID *string             `json:"status_id,omitempty"`
	Priority *string             `json:"priority,omitempty"`
	Assignee *BatchAssigneePatch `json:"assignee,omitempty"`
	Labels   *BatchLabelPatch    `json:"labels,omitempty"`
}

// BatchTaskUpdateInput 是批量修改的输入；RequestID 用于派生每个任务的动态 source_id。
type BatchTaskUpdateInput struct {
	Tasks     []BatchTaskRef
	Patch     BatchTaskPatch
	RequestID string
}

// BatchTaskError 带出导致整批失败的任务 id；Err 是原始错误（决定 HTTP 状态与错误码）。
type BatchTaskError struct {
	TaskIDs []string
	Err     error
}

func (e *BatchTaskError) Error() string { return "collaboration: batch task update: " + e.Err.Error() }
func (e *BatchTaskError) Unwrap() error { return e.Err }

func batchError(err error, ids ...string) error {
	return &BatchTaskError{TaskIDs: ids, Err: err}
}

// errInvalidBatch 是请求形状不合法（数量、重复、补丁字段个数、标签增删冲突等）；details.reason 说明原因。
var errInvalidBatch = NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "batch request is invalid")

func invalidBatch(reason string, ids ...string) error {
	details := map[string]any{"reason": reason}
	if len(ids) > 0 {
		details["task_ids"] = ids
	}
	return errInvalidBatch.WithDetails(details)
}

// normalizeBatchInput 校验请求形状并规范化 id；返回按 id 排序的任务列表（加锁顺序）。
func normalizeBatchInput(input *BatchTaskUpdateInput) ([]BatchTaskRef, error) {
	if len(input.Tasks) == 0 || len(input.Tasks) > MaxBatchTasks {
		return nil, invalidBatch("task_count")
	}
	seen := make(map[string]bool, len(input.Tasks))
	var duplicates []string
	for index := range input.Tasks {
		ref := &input.Tasks[index]
		ref.ID = strings.ToLower(strings.TrimSpace(ref.ID))
		if !membership.ValidateUUID(ref.ID) || ref.Version == 0 {
			return nil, invalidBatch("task_ref")
		}
		if seen[ref.ID] {
			duplicates = append(duplicates, ref.ID)
		}
		seen[ref.ID] = true
	}
	if len(duplicates) > 0 {
		return nil, invalidBatch("duplicate_task", duplicates...)
	}
	patch := &input.Patch
	fields := 0
	if patch.StatusID != nil {
		fields++
		value := strings.ToLower(strings.TrimSpace(*patch.StatusID))
		if !membership.ValidateUUID(value) {
			return nil, invalidBatch("status_id")
		}
		patch.StatusID = &value
	}
	if patch.Priority != nil {
		fields++
		value, err := ParsePriority(*patch.Priority)
		if err != nil {
			return nil, invalidBatch("priority")
		}
		patch.Priority = &value
	}
	if patch.Assignee != nil {
		fields++
		kind := strings.ToUpper(strings.TrimSpace(patch.Assignee.Kind))
		if (kind != "HUMAN" && kind != "AGENT" && kind != "CREW") || !patch.Assignee.ID.Set {
			return nil, invalidBatch("assignee")
		}
		patch.Assignee.Kind = kind
		if patch.Assignee.ID.Value != nil {
			id := strings.ToLower(strings.TrimSpace(*patch.Assignee.ID.Value))
			if !membership.ValidateUUID(id) {
				return nil, invalidBatch("assignee")
			}
			patch.Assignee.ID.Value = &id
		}
	}
	if patch.Labels != nil {
		fields++
		add, err := normalizeLabelIDs(patch.Labels.Add)
		if err != nil || len(patch.Labels.Add) > maxTaskLabels {
			return nil, invalidBatch("labels")
		}
		remove, err := normalizeLabelIDs(patch.Labels.Remove)
		if err != nil || len(patch.Labels.Remove) > maxTaskLabels || len(add)+len(remove) == 0 {
			return nil, invalidBatch("labels")
		}
		removing := make(map[string]bool, len(remove))
		for _, id := range remove {
			removing[id] = true
		}
		for _, id := range add {
			if removing[id] {
				return nil, invalidBatch("labels")
			}
		}
		patch.Labels.Add, patch.Labels.Remove = add, remove
	}
	if fields != 1 {
		return nil, invalidBatch("patch_fields")
	}
	sorted := append([]BatchTaskRef(nil), input.Tasks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return sorted, nil
}

// BatchUpdateTasks 在调用方的事务里批量修改任务，返回顺序与请求相同的更新后任务。
// 调用方负责鉴权（与单条 PATCH 相同）与停用空间检查；任一任务失败即返回错误，由调用方回滚整个事务。
//
// 加锁：先按任务 id 升序一次性 SELECT ... FOR UPDATE 锁住全部任务行（两批交叉时按同一顺序排队，不会互相死锁），
// 再逐个调用 UpdateTask；UpdateTask 内部对同一行再次 FOR UPDATE 不会阻塞。批量补丁不改父任务、估时，
// 因此不会触发 UpdateTask 里的空间层级 advisory 锁或空间行 FOR KEY SHARE。
func (s Service) BatchUpdateTasks(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input BatchTaskUpdateInput) ([]Task, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(spaceID) {
		return nil, ErrSpaceNotFound
	}
	sorted, err := normalizeBatchInput(&input)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(sorted))
	for index, ref := range sorted {
		ids[index] = ref.ID
	}
	type lockedTask struct {
		spaceID, typeID string
		version         int64
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, space_id::text, type_id::text, version FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND deleted_at IS NULL
		 ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	locked := make(map[string]lockedTask, len(ids))
	for rows.Next() {
		var id string
		var item lockedTask
		if err := rows.Scan(&id, &item.spaceID, &item.typeID, &item.version); err != nil {
			rows.Close()
			return nil, err
		}
		locked[id] = item
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 不存在、已删除、不在该空间：一律按不存在处理（不泄露别的空间里有没有这个任务）。
	var missing, stale []string
	for _, ref := range sorted {
		item, ok := locked[ref.ID]
		if !ok || item.spaceID != spaceID {
			missing = append(missing, ref.ID)
			continue
		}
		if uint64(item.version) != ref.Version {
			stale = append(stale, ref.ID)
		}
	}
	if len(missing) > 0 {
		return nil, batchError(ErrTaskNotFound, missing...)
	}
	if len(stale) > 0 {
		return nil, batchError(ErrVersionConflict, stale...)
	}
	// 目标状态必须属于每个任务各自类型的工作流：先整体检查，一次列出全部不合法的任务。
	if input.Patch.StatusID != nil {
		flows := make(map[string]workflow)
		var invalid []string
		for _, ref := range sorted {
			typeID := locked[ref.ID].typeID
			flow, ok := flows[typeID]
			if !ok {
				if flow, err = loadWorkflow(ctx, tx, organizationID, spaceID, typeID); err != nil && !errors.Is(err, ErrTaskTypeNotFound) {
					return nil, err
				}
				flows[typeID] = flow
			}
			if !flow.contains(*input.Patch.StatusID) {
				invalid = append(invalid, ref.ID)
			}
		}
		if len(invalid) > 0 {
			return nil, batchError(ErrInvalidWorkflow, invalid...)
		}
	}
	updated := make(map[string]Task, len(sorted))
	for _, ref := range sorted {
		update, err := s.batchUpdateInput(ctx, tx, organizationID, spaceID, actor, ref, input)
		if err != nil {
			return nil, batchError(err, ref.ID)
		}
		task, err := s.UpdateTask(ctx, tx, organizationID, spaceID, ref.ID, actor, update)
		if err != nil {
			return nil, batchError(err, ref.ID)
		}
		updated[ref.ID] = task
	}
	result := make([]Task, 0, len(input.Tasks))
	for _, ref := range input.Tasks {
		result = append(result, updated[ref.ID])
	}
	return result, nil
}

// batchUpdateInput 把批量补丁换算成单个任务的 UpdateTask 输入。
// 每个任务用独立的 RequestID（<请求 id>:<任务 id>），保证动态的 source_id 在组织内唯一。
func (s Service) batchUpdateInput(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, ref BatchTaskRef, input BatchTaskUpdateInput) (TaskUpdateInput, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		var err error
		if requestID, err = membership.NewID(); err != nil {
			return TaskUpdateInput{}, err
		}
	}
	update := TaskUpdateInput{Version: ref.Version, RequestID: requestID + ":" + ref.ID}
	patch := input.Patch
	switch {
	case patch.StatusID != nil:
		update.StatusID = patch.StatusID
	case patch.Priority != nil:
		update.Priority = patch.Priority
	case patch.Assignee != nil, patch.Labels != nil:
		current, err := s.GetTask(ctx, tx, organizationID, spaceID, ref.ID, actor)
		if err != nil {
			return TaskUpdateInput{}, err
		}
		if patch.Assignee != nil {
			id := ""
			if patch.Assignee.ID.Value != nil {
				id = *patch.Assignee.ID.Value
			}
			desired := replaceAssigneeKind(current.Assignees, patch.Assignee.Kind, id)
			update.Assignees = &desired
		} else {
			desired := applyLabelPatch(current.LabelItems, patch.Labels.Add, patch.Labels.Remove)
			update.LabelIDs = &desired
		}
	}
	return update, nil
}

// replaceAssigneeKind 让某一类处理人只剩 id（空串表示清空该类），其他类保持原样。
// 已经是目标值时原样返回（UpdateTask 据此不写动态、不发事件）；否则按 HUMAN、AGENT、CREW 稳定排序，与前端 replaceAssignee 一致。
func replaceAssigneeKind(current []TaskAssignee, kind, id string) []TaskAssigneeRef {
	refs := make([]TaskAssigneeRef, 0, len(current)+1)
	var sameKind []string
	for _, item := range current {
		refs = append(refs, TaskAssigneeRef{Kind: item.Kind, ID: item.ID})
		if item.Kind == kind {
			sameKind = append(sameKind, item.ID)
		}
	}
	if (id == "" && len(sameKind) == 0) || (id != "" && len(sameKind) == 1 && sameKind[0] == id) {
		return refs
	}
	result := make([]TaskAssigneeRef, 0, len(refs)+1)
	for _, ref := range refs {
		if ref.Kind != kind {
			result = append(result, ref)
		}
	}
	if id != "" {
		result = append(result, TaskAssigneeRef{Kind: kind, ID: id})
	}
	order := map[string]int{"HUMAN": 0, "AGENT": 1, "CREW": 2, "INVITATION": 3}
	sort.SliceStable(result, func(i, j int) bool { return order[result[i].Kind] < order[result[j].Kind] })
	return result
}

// applyLabelPatch 在现有标签上先移除再追加（已有的不重复追加），保持原有顺序。
func applyLabelPatch(current []TaskLabelItem, add, remove []string) []string {
	removing := make(map[string]bool, len(remove))
	for _, id := range remove {
		removing[id] = true
	}
	result := make([]string, 0, len(current)+len(add))
	present := make(map[string]bool, len(current)+len(add))
	for _, item := range current {
		if !removing[item.ID] {
			result = append(result, item.ID)
			present[item.ID] = true
		}
	}
	for _, id := range add {
		if !present[id] {
			result = append(result, id)
			present[id] = true
		}
	}
	return result
}
