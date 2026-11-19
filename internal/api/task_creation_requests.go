package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

func validTaskCreationRequest(input conversations.TaskCreationRequest) bool {
	if input.Brief == "" || utf8.RuneCountInString(input.Brief) > 10000 || (input.AgentID == "") == (input.CrewID == "") {
		return false
	}
	for _, id := range []string{input.AgentID, input.CrewID, input.ProjectID, input.TypeID, input.StatusID, input.ParentTaskID, input.IterationID} {
		if id != "" && !membership.ValidateUUID(id) {
			return false
		}
	}
	if input.DueDate != "" {
		if _, err := time.Parse(time.DateOnly, input.DueDate); err != nil {
			return false
		}
	}
	if input.Priority != "" {
		if _, err := collaboration.ParsePriority(input.Priority); err != nil {
			return false
		}
	}
	return true
}

func (h *Handler) createTaskWithAgent(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversations.TaskCreationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	input.Brief = strings.TrimSpace(input.Brief)
	spaceID := r.PathValue("spaceID")
	if !membership.ValidateUUID(spaceID) || !validTaskCreationRequest(input) {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if key == "" || !idempotency.ValidKey(key) {
		writeCollaborationError(w, r, idempotency.ErrInvalidKey)
		return
	}
	org := selection.OrganizationID
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var requestID, deviceID string
	var replay bool
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		service := conversations.Service{}
		if err := service.CheckActor(ctx, tx, org, actor); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "task.create_with_agent", key, struct {
			Space   string
			Request conversations.TaskCreationRequest
		}{spaceID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.task_drafts WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND requester_id=$2 AND space_id=$3`, claim.ResourceID, actor.UserID, spaceID).Scan(&requestID)
			if errors.Is(err, pgx.ErrNoRows) {
				return conversations.ErrNotFound
			}
			replay = true
			return err
		}
		requestID, err = collaboration.NewID()
		if err != nil {
			return err
		}
		deviceID, err = dispatch.EnqueueTaskDraft(ctx, tx, org, spaceID, requestID, actor, input)
		if err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, requestID)
	})
	if errors.Is(err, idempotency.ErrConflict) {
		writeCollaborationError(w, r, err)
		return
	}
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	if !replay && h.dispatchHub != nil {
		h.dispatchHub.PublishCommand(deviceID, org, requestID, dispatch.TaskDraftCommand)
	}
	writeCreated(w, replay, map[string]string{"request_id": requestID})
}
