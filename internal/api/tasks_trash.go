package api

// 任务回收站接口。
//   GET  …/spaces/{spaceID}/deleted-tasks：最近删除（空间管理员看全部，其他人只看自己删除的；空间读不到 404）；
//   POST …/spaces/{spaceID}/tasks/{taskID}/restore：恢复（写权限 + 删除人本人或空间管理员；Idempotency-Key 可选）。
// 「空间管理员」与空间设置等管理接口相同：policy.ActionManageSpace（组织所有者 / 管理员，或空间角色 administrator）。

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
)

func (h *Handler) registerTaskTrashRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/spaces/{spaceID}/deleted-tasks", h.listDeletedTasks)
	mux.HandleFunc("POST "+taskBase+"/{taskID}/restore", h.restoreTask)
}

// trashAccess 检查空间访问并返回是否为空间管理员。action 为 ActionReadSpace（列表）或 ActionUpdateObject（恢复）；
// 恢复时停用空间按 RequireWritable 拒绝（409 SPACE_RETIRED）。
func (c taskCall) trashAccess(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string, action policy.Action, objectID string) (bool, error) {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return false, err
	}
	if decision := collaborationSpaceDecision(action, c.organizationID, c.selected, access, objectID); !decision.Allowed() {
		return false, decisionError{decision: decision}
	}
	if collaboration.IsObjectWrite(action) {
		if err := access.RequireWritable(); err != nil {
			return false, err
		}
	}
	return collaborationSpaceDecision(policy.ActionManageSpace, c.organizationID, c.selected, access, "").Allowed(), nil
}

func (h *Handler) listDeletedTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	limit, err := collaboration.ParsePageSize(r.URL.Query().Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var cursor *collaboration.Cursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		parsed, err := collaboration.ParseCursor(raw)
		if err != nil {
			writeCollaborationError(w, r, err)
			return
		}
		cursor = &parsed
	}
	var page collaboration.Page[collaboration.DeletedTask]
	manager := false
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		if manager, err = call.trashAccess(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		page, err = service.ListDeletedTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor, manager, limit, cursor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	scope := "mine"
	if manager {
		scope = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": page.Items, "next_cursor": page.NextCursor, "scope": scope})
}

type taskRestoreRequest struct {
	Version uint64 `json:"version"`
}

func (h *Handler) restoreTask(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input taskRestoreRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version is required")
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	requestID := collaborationRequestID(w, r)
	idempotencyKey := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	var task collaboration.Task
	replayed := false
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		manager, err := call.trashAccess(ctx, tx, service, spaceID, policy.ActionUpdateObject, taskID)
		if err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "task.restore", idempotencyKey, struct {
			SpaceID string             `json:"space_id"`
			TaskID  string             `json:"task_id"`
			Body    taskRestoreRequest `json:"body"`
		}{spaceID, taskID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			task, err = service.GetTask(ctx, tx, call.organizationID, spaceID, claim.ResourceID, call.selected.Actor)
			return err
		}
		task, err = service.RestoreTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor, manager, input.Version, requestID)
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, task.ID)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	writeJSON(w, http.StatusOK, toTaskResponse(task))
}
