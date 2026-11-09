package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/platform/apierror"
	"openfox/internal/platform/requestid"
	"openfox/internal/policy"
)

// 任务批量修改接口：POST /spaces/{spaceID}/tasks/batch-update。
// 鉴权与单条 PATCH 相同（每个任务按 object.update 判定，停用空间只读）；单个租户事务，任一任务失败整批回滚。

type taskBatchRequest struct {
	Tasks []collaboration.BatchTaskRef `json:"tasks"`
	Patch collaboration.BatchTaskPatch `json:"patch"`
}

func (h *Handler) batchUpdateTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input taskBatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := strings.ToLower(strings.TrimSpace(r.PathValue("spaceID")))
	requestID := collaborationRequestID(w, r)
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	var tasks []collaboration.Task
	replayed := false
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeBatch(ctx, tx, service, spaceID, input.Tasks); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "task.batch_update", key, struct {
			SpaceID string           `json:"space_id"`
			Body    taskBatchRequest `json:"body"`
		}{spaceID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			// 同键同请求：返回这些任务的当前状态（与 task.create 的重放语义一致），不再写任何数据。
			replayed = true
			tasks = make([]collaboration.Task, 0, len(input.Tasks))
			for _, ref := range input.Tasks {
				task, err := service.GetTask(ctx, tx, call.organizationID, spaceID, strings.ToLower(strings.TrimSpace(ref.ID)), call.selected.Actor)
				if err != nil {
					return err
				}
				tasks = append(tasks, task)
			}
			return nil
		}
		tasks, err = service.BatchUpdateTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor, collaboration.BatchTaskUpdateInput{
			Tasks: input.Tasks, Patch: input.Patch, RequestID: requestID,
		})
		if err != nil {
			return err
		}
		// resource_id 只用来标记「首次请求已完成」，批量没有单一资源，记一个新的批次 id。
		batchID, err := collaboration.NewID()
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, batchID)
	})
	if err != nil {
		writeBatchTaskError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	items := make([]taskResponse, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, toTaskResponse(task))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

// authorizeBatch 与单条 PATCH 的 authorizeSpace 等价：空间访问只读一次，再对每个任务按 object.update 判定；
// 被拒绝的任务 id 放进 details.task_ids。最后检查停用空间只读。
func (c taskCall) authorizeBatch(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string, refs []collaboration.BatchTaskRef) error {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return err
	}
	var denied []string
	var first policy.Decision
	for _, ref := range refs {
		id := strings.ToLower(strings.TrimSpace(ref.ID))
		decision := collaborationSpaceDecision(policy.ActionUpdateObject, c.organizationID, c.selected, access, id)
		if !decision.Allowed() {
			if len(denied) == 0 {
				first = decision
			}
			denied = append(denied, id)
		}
	}
	if len(denied) > 0 {
		return &collaboration.BatchTaskError{TaskIDs: denied, Err: decisionError{decision: first}}
	}
	return access.RequireWritable()
}

// writeBatchTaskError 按单条接口的错误映射输出，并在 details.task_ids 里带出问题任务。
func writeBatchTaskError(w http.ResponseWriter, r *http.Request, err error) {
	var batch *collaboration.BatchTaskError
	if !errors.As(err, &batch) || len(batch.TaskIDs) == 0 {
		writeCollaborationError(w, r, err)
		return
	}
	status, code, message, details := collaborationErrorParts(batch.Err)
	merged := make(map[string]any, len(details)+1)
	for name, value := range details {
		merged[name] = value
	}
	merged["task_ids"] = batch.TaskIDs
	requestID := requestid.FromContext(r.Context())
	if requestID == "" {
		requestID = r.Header.Get(requestid.Header)
	}
	apierror.WriteDetails(w, status, code, message, requestID, merged)
}
