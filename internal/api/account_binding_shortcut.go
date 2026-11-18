package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) putMyShortcutBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.ShortcutPersonalInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var grant integrations.ShortcutPersonalGrant
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		grant, err = h.integrations.PrepareShortcutPersonal(ctx, tx, call.organizationID, call.selected.Actor, in)
		return err
	})
	if err != nil {
		writeShortcutBindingError(w, r, err)
		return
	}
	grant, err = integrations.VerifyShortcutPersonal(r.Context(), grant)
	if err != nil {
		writeShortcutBindingError(w, r, err)
		return
	}
	var out integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.SaveShortcutPersonal(ctx, tx, call.organizationID, call.selected.Actor, in, grant)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.personal_identity_bound", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "shortcut"})
	})
	in.Token = ""
	if err != nil {
		writeShortcutBindingError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func writeShortcutBindingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, integrations.ErrShortcutAuth):
		writeError(w, r, 422, "SHORTCUT_AUTH_FAILED", "Shortcut rejected the token or the authenticated member is inactive")
	case errors.Is(err, integrations.ErrShortcutEmail):
		writeError(w, r, 422, "SHORTCUT_EMAIL_MISMATCH", "the member email must match your OpenFox email")
	case errors.Is(err, integrations.ErrShortcutWorkspace):
		writeError(w, r, 422, "SHORTCUT_WORKSPACE_MISMATCH", "the personal token must belong to the selected workspace")
	case errors.Is(err, integrations.ErrShortcutInUse):
		writeError(w, r, 409, "SHORTCUT_ACCOUNT_IN_USE", "this identity is already bound; disconnect before changing identity")
	case errors.Is(err, integrations.ErrShortcutRate):
		writeError(w, r, 429, "SHORTCUT_RATE_LIMITED", "Shortcut temporarily limited requests; retry later")
	default:
		writeIntegrationError(w, r, err)
	}
}
