package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) bindPingCodePersonal(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.PingCodePersonalInput
	if err := decodeJSONLimit(w, r, 8<<10, &input); err != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var grant integrations.PingCodePersonalGrant
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		grant, err = h.integrations.PreparePingCodePersonal(ctx, tx, call.organizationID, call.selected.Actor, input)
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	grant, err = integrations.VerifyPingCodePersonal(ctx, grant)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var connection integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		connection, err = h.integrations.SavePingCodePersonal(ctx, tx, call.organizationID, call.selected.Actor, input, grant)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.personal_comment_bound", "integration_connection", connection.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "pingcode"})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, connection)
}
