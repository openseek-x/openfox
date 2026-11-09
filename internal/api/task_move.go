package api

// 任务「移动到空间」接口（复测报告 D09）。
//   POST …/spaces/{spaceID}/tasks/{taskID}/move-preview：只读预览；
//   POST …/spaces/{spaceID}/tasks/{taskID}/move：执行，Idempotency-Key 必填。
// 权限：源空间删除权限（任务从源空间消失），目标空间创建权限；目标看不到、已停用、跨组织一律 400 INVALID_TARGET_SPACE。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
)

const taskMovedAuditAction = "task.moved"

type taskMoveRequest struct {
	TargetSpaceID string `json:"target_space_id"`
	Version       uint64 `json:"version"`
	CancelRuns    bool   `json:"cancel_runs"`
}

type taskMoveResponse struct {
	Task              taskResponse `json:"task"`
	DisplayID         string       `json:"display_id"`
	PreviousDisplayID string       `json:"previous_display_id"`
	MovedCount        int          `json:"moved_count"`
	CanceledRuns      int          `json:"canceled_runs"`
}

// authorizeMove 校验源空间删除权限与目标空间创建权限。目标不可写一律映射为 INVALID_TARGET_SPACE，不泄露存在性；
// 源空间的拒绝（含已停用 409 SPACE_RETIRED）原样返回。
func (c taskCall) authorizeMove(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID, taskID, targetID string) error {
	if err := c.authorizeSpace(ctx, tx, service, spaceID, policy.ActionDeleteObject, taskID); err != nil {
		return err
	}
	objectID, err := collaboration.NewID()
	if err != nil {
		return err
	}
	if err := c.authorizeSpace(ctx, tx, service, targetID, policy.ActionCreateObject, objectID); err != nil {
		var denied decisionError
		var rule *collaboration.RuleError
		if errors.As(err, &denied) || errors.As(err, &rule) || errors.Is(err, collaboration.ErrSpaceNotFound) {
			return collaboration.ErrInvalidTarget
		}
		return err
	}
	return nil
}

func (h *Handler) previewTaskMove(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		TargetSpaceID string `json:"target_space_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	targetID := strings.ToLower(strings.TrimSpace(input.TargetSpaceID))
	var preview collaboration.MovePreview
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeMove(ctx, tx, service, spaceID, taskID, targetID); err != nil {
			return err
		}
		var err error
		preview, err = service.PreviewMoveTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor, targetID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) moveTask(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input taskMoveRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if key == "" || !idempotency.ValidKey(key) {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "a valid Idempotency-Key header is required")
		return
	}
	input.TargetSpaceID = strings.ToLower(strings.TrimSpace(input.TargetSpaceID))
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	requestID := collaborationRequestID(w, r)
	var result collaboration.MoveTaskResult
	var task collaboration.Task
	var canceled dispatch.AgentRunCancellation
	replayed := false
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeMove(ctx, tx, service, spaceID, taskID, input.TargetSpaceID); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "task.move", key, struct {
			SpaceID string          `json:"space_id"`
			TaskID  string          `json:"task_id"`
			Body    taskMoveRequest `json:"body"`
		}{strings.ToLower(spaceID), strings.ToLower(taskID), input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			return h.replayTaskMove(ctx, tx, call, service, claim.ResourceID, &result, &task)
		}
		result, canceled, err = h.applyTaskMove(ctx, tx, call, service, spaceID, taskID, input, requestID)
		if err != nil {
			return err
		}
		if task, err = service.GetTask(ctx, tx, call.organizationID, result.SpaceID, taskID, call.selected.Actor); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, task.ID)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.publishRunCancels(canceled)
	if replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	writeJSON(w, http.StatusOK, taskMoveResponse{Task: toTaskResponse(task), DisplayID: result.DisplayID,
		PreviousDisplayID: result.PreviousDisplayID, MovedCount: result.MovedCount, CanceledRuns: result.CanceledRuns})
}

// applyTaskMove 按锁序执行：空间行（PrepareMoveTask）→ Run（取消）→ 任务（ApplyMoveTask），再写审计。
func (h *Handler) applyTaskMove(ctx context.Context, tx pgx.Tx, call taskCall, service collaboration.Service,
	spaceID, taskID string, input taskMoveRequest, requestID string) (collaboration.MoveTaskResult, dispatch.AgentRunCancellation, error) {
	var canceled dispatch.AgentRunCancellation
	plan, err := service.PrepareMoveTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor, collaboration.MoveTaskInput{
		TargetSpaceID: input.TargetSpaceID, Version: input.Version, CancelRuns: input.CancelRuns, RequestID: requestID,
	})
	if err != nil {
		return collaboration.MoveTaskResult{}, canceled, err
	}
	if plan.NeedsConfirmation() && !input.CancelRuns {
		return collaboration.MoveTaskResult{}, canceled, plan.ConfirmationError()
	}
	if canceled, err = dispatch.CancelTaskRuns(ctx, tx, call.organizationID, plan.TaskIDs()); err != nil {
		return collaboration.MoveTaskResult{}, canceled, err
	}
	result, err := service.ApplyMoveTask(ctx, tx, call.selected.Actor, plan, canceled.Canceled, requestID)
	if err != nil {
		return collaboration.MoveTaskResult{}, canceled, err
	}
	err = recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor, taskMovedAuditAction, "task",
		plan.TaskID, plan.TargetID, call.selected.Actor.AuthorizationVersion, map[string]string{
			"source_space_id": plan.SourceID, "target_space_id": plan.TargetID,
			"previous_display_id": result.PreviousDisplayID, "display_id": result.DisplayID,
			"moved_tasks": strconv.Itoa(result.MovedCount), "canceled_runs": strconv.Itoa(result.CanceledRuns),
		})
	return result, canceled, err
}

// replayTaskMove 返回首次执行的结果（任务按当前所在空间读取，需要读权限）。
func (h *Handler) replayTaskMove(ctx context.Context, tx pgx.Tx, call taskCall, service collaboration.Service, taskID string,
	result *collaboration.MoveTaskResult, task *collaboration.Task) error {
	replay, err := service.MoveReplayResult(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if err := call.authorizeSpace(ctx, tx, service, replay.SpaceID, policy.ActionReadObject, taskID); err != nil {
		return err
	}
	*result = replay
	*task, err = service.GetTask(ctx, tx, call.organizationID, replay.SpaceID, taskID, call.selected.Actor)
	return err
}
