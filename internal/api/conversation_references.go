package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
)

func (h *Handler) postConversationReference(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var request struct {
		Type            string          `json:"type"`
		TaskID          json.RawMessage `json:"task_id"`
		DocID           json.RawMessage `json:"doc_id"`
		ClientMessageID string          `json:"client_message_id"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	input := conversations.ReferenceInput{Type: request.Type, ClientMessageID: request.ClientMessageID}
	valid := false
	switch request.Type {
	case "TASK":
		valid = len(request.DocID) == 0 && json.Unmarshal(request.TaskID, &input.TaskID) == nil
	case "DOC":
		valid = len(request.TaskID) == 0 && json.Unmarshal(request.DocID, &input.DocID) == nil
	}
	if !valid {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	var err error
	input.ID, err = newUUID()
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	var result conversations.Message
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err = h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var created bool
		result, created, err = (conversations.Service{}).PostReference(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, input)
		if err != nil || !created {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.message_created", "message", result.ID, "", nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
