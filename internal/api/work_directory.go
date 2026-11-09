package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
)

func (h *Handler) conversationWorkDirectory(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	var result conversations.WorkDirectoryTarget
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).WorkDirectory(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), conversationActor(auth, role, selection.AuthorizationVersion))
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
