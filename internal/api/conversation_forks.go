package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/idempotency"
)

func (h *Handler) forkConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var request struct {
		SourceMessageID    string          `json:"source_message_id"`
		AgentID            string          `json:"agent_id"`
		WorkspaceSelection json.RawMessage `json:"workspace_selection"`
	}
	if err := decodeJSON(w, r, &request); err != nil || len(request.WorkspaceSelection) == 0 {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	input := conversations.ForkInput{SourceMessageID: request.SourceMessageID, AgentID: request.AgentID}
	if err := json.Unmarshal(request.WorkspaceSelection, &input.WorkspaceSelection); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	// The outer and nested request are both closed, including server-only binding fields.
	if input.WorkspaceSelection != nil {
		decoder := json.NewDecoder(strings.NewReader(string(request.WorkspaceSelection)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input.WorkspaceSelection); err != nil {
			writeConversationError(w, r, conversations.ErrInvalidRequest)
			return
		}
	}
	input = conversations.NormalizeForkInput(input)
	sourceID := strings.ToLower(strings.TrimSpace(r.PathValue("conversationID")))
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if key == "" || !idempotency.ValidKey(key) {
		writeCollaborationError(w, r, idempotency.ErrInvalidKey)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var result conversations.Conversation
	replayed := false
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		service := conversations.Service{}
		if err := service.CheckActor(ctx, tx, selection.OrganizationID, actor); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "conversation.fork", key, struct {
			Source string
			Input  conversations.ForkInput
		}{sourceID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			result, err = service.Get(ctx, tx, selection.OrganizationID, claim.ResourceID, actor)
			if err == nil && result.OwnerUserID != actor.UserID {
				return conversations.ErrNotFound
			}
			return err
		}
		result, err = service.Fork(ctx, tx, selection.OrganizationID, sourceID, actor, input)
		if err != nil {
			return err
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.messages WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid`, result.ID).Scan(&count); err != nil {
			return err
		}
		if err = recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor, "conversation.forked", "conversation", result.ID, result.SpaceID, map[string]string{"message_count": strconv.Itoa(count)}); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, result.ID)
	})
	if errors.Is(err, conversations.ErrForkContextTooLarge) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "FORK_CONTEXT_TOO_LARGE", "fork history exceeds 500 messages or 200 KiB")
		return
	}
	if errors.Is(err, idempotency.ErrConflict) || errors.Is(err, idempotency.ErrInvalidKey) {
		writeCollaborationError(w, r, err)
		return
	}
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeCreated(w, replayed, result)
}
