package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/policy"
)

func (h *Handler) editTaskComment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Body    string `json:"body"`
		Version int64  `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, 400, "INVALID_REQUEST", "invalid edit")
		return
	}
	h.commentAction(w, r, "comment.edit", "comment.edited", input, func(ctx context.Context, tx pgx.Tx, sub taskSub, id string) (bool, error) {
		return sub.service.EditTaskComment(ctx, tx, sub.spaceID, sub.taskID, id, sub.call.selected.Actor, input.Body, input.Version)
	})
}

func (h *Handler) setTaskCommentResolution(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Resolved *bool `json:"resolved"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Resolved == nil {
		writeError(w, r, 400, "INVALID_REQUEST", "resolved is required")
		return
	}
	action := "comment.reopened"
	if *input.Resolved {
		action = "comment.resolved"
	}
	h.commentAction(w, r, "comment.resolution", action, input, func(ctx context.Context, tx pgx.Tx, sub taskSub, id string) (bool, error) {
		return sub.service.SetCommentResolution(ctx, tx, sub.spaceID, sub.taskID, id, sub.call.selected.Actor, *input.Resolved, sub.requestID)
	})
}

func (h *Handler) commentAction(w http.ResponseWriter, r *http.Request, operation, action string, input any, apply func(context.Context, pgx.Tx, taskSub, string) (bool, error)) {
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if key == "" || !idempotency.ValidKey(key) {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "a valid Idempotency-Key header is required")
		return
	}
	replayed := false
	if h.subResource(w, r, true, policy.ActionUpdateObject, func(ctx context.Context, tx pgx.Tx, sub taskSub) error {
		if err := sub.service.CheckActor(ctx, tx, sub.call.organizationID, sub.call.selected.Actor); err != nil {
			return err
		}
		id := strings.ToLower(r.PathValue("commentID"))
		claim, err := idempotency.Begin(ctx, tx, operation, key, struct {
			Space, Task, Comment string
			Input                any
		}{sub.spaceID, sub.taskID, id, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			return nil
		}
		changed, err := apply(ctx, tx, sub, id)
		if err != nil {
			return err
		}
		if changed {
			actor := sub.call.selected.Actor
			if err := recordCollaborationAudit(ctx, tx, sub.requestID, sub.call.organizationID, actor, action, "task_comment", id, sub.spaceID, actor.AuthorizationVersion, nil); err != nil {
				return err
			}
		}
		return idempotency.Complete(ctx, tx, claim, id)
	}) {
		if replayed {
			w.Header().Set(idempotency.ReplayedHeader, "true")
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) deleteTaskComment(w http.ResponseWriter, r *http.Request) {
	h.commentAction(w, r, "comment.delete", "comment.deleted", nil, func(ctx context.Context, tx pgx.Tx, sub taskSub, id string) (bool, error) {
		return sub.service.DeleteTaskComment(ctx, tx, sub.spaceID, sub.taskID, id, sub.call.selected.Actor, sub.requestID)
	})
}
