package api

import (
	"context"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/membership"
)

func (h *Handler) shortcutHookSettings(w http.ResponseWriter, r *http.Request) {
	write := r.Method == http.MethodPut
	call, ok := h.beginTaskCall(w, r, write)
	if !ok {
		return
	}
	var in struct {
		Version uint64  `json:"version"`
		Secret  *string `json:"secret"`
	}
	if write && (decodeJSONLimit(w, r, 2048, &in) != nil || in.Secret == nil || in.Version == 0) {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var v integrations.ShortcutHook
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		id := r.PathValue("connectionID")
		v, err = h.integrations.ShortcutHookSettings(ctx, tx, call.organizationID, id, call.selected.Actor, in.Version, in.Secret)
		if err == nil && write {
			return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.settings_updated", "integration_connection", id, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "shortcut", "result": "hook_updated"})
		}
		return err
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, v)
}

func (h *Handler) receiveShortcutHook(w http.ResponseWriter, r *http.Request) {
	org, id, sig := r.PathValue("organizationID"), r.PathValue("connectionID"), r.Header.Get("Payload-Signature")
	if !membership.ValidateUUID(org) || !membership.ValidateUUID(id) || len(sig) != 64 {
		writeError(w, r, 403, "FORBIDDEN", "invalid webhook")
		return
	}
	if !h.allowKey(w, r, h.webhookLimiter, id) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "invalid webhook body")
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "NOT_READY", "database not ready")
		return
	}
	err = h.runAsWorker(r.Context(), org, func(ctx context.Context, tx pgx.Tx) error {
		return h.integrations.ReceiveShortcutHook(ctx, tx, org, id, sig, raw)
	})
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
