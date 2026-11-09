package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) bindProviderToken(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.ProviderTokenInput
	if err := decodeJSON(w, r, &in); err != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	provider := r.PathValue("provider")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.integrations.CheckProviderToken(ctx, tx, call.organizationID, call.selected.Actor, provider, in)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	id, name, err := integrations.VerifyProviderToken(r.Context(), provider, in)
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var conn integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var saveErr error
		conn, saveErr = h.integrations.SaveProviderToken(ctx, tx, call.organizationID, call.selected.Actor, provider, in, id, name)
		if saveErr != nil {
			return saveErr
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_created", "integration_connection", conn.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": provider, "result": conn.Scope})
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, conn)
}
