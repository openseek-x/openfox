package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) putMyTAPDBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.TAPDPersonalInput
	if err := decodeJSONLimit(w, r, 8<<10, &input); err != nil {
		writeTAPDPersonalError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var grant integrations.TAPDPersonalGrant
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		grant, err = h.integrations.PrepareTAPDPersonal(ctx, tx, call.organizationID, call.selected.Actor, input)
		return err
	})
	if err != nil {
		writeTAPDPersonalError(w, r, err)
		return
	}
	grant, err = integrations.VerifyTAPDPersonal(r.Context(), grant)
	if err != nil {
		writeTAPDPersonalError(w, r, err)
		return
	}
	var out integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.SaveTAPDPersonal(ctx, tx, call.organizationID, call.selected.Actor, input, grant)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.personal_identity_bound", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "tapd"})
	})
	input.AccessToken = ""
	if err != nil {
		writeTAPDPersonalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) removeMyTAPDProject(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		BindingID string `json:"binding_id"`
		Version   uint64 `json:"version"`
	}
	if err := decodeJSONLimit(w, r, 2<<10, &input); err != nil {
		writeTAPDPersonalError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.Connection
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.RemoveTAPDPersonalProject(ctx, tx, call.organizationID, call.selected.Actor, input.BindingID, r.PathValue("projectID"), input.Version)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.personal_identity_scope_removed", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "tapd"})
	})
	if err != nil {
		writeTAPDPersonalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func writeTAPDPersonalError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, integrations.ErrTAPDPersonalAuth):
		writeError(w, r, 422, "TAPD_AUTH_FAILED", "TAPD rejected the user OAuth token or the account is inactive")
	case errors.Is(err, integrations.ErrTAPDPersonalPermission):
		writeError(w, r, 422, "TAPD_PROJECT_UNAVAILABLE", "the selected project or exact member could not be verified")
	case errors.Is(err, integrations.ErrTAPDPersonalEmail):
		writeError(w, r, 422, "TAPD_EMAIL_MISMATCH", "the project member email must match your OpenFox email")
	case errors.Is(err, integrations.ErrTAPDPersonalParent):
		writeError(w, r, 409, "TAPD_BASIC_REQUIRED", "identity verification requires a Basic organization connection")
	case errors.Is(err, integrations.ErrTAPDPersonalInUse):
		writeError(w, r, 409, "TAPD_ACCOUNT_IN_USE", "this TAPD identity is already bound; disconnect before changing identity or parent")
	case errors.Is(err, integrations.ErrTAPDPersonalLimit):
		writeError(w, r, 409, "TAPD_PROJECT_LIMIT", "an identity binding can include at most 20 projects")
	case errors.Is(err, integrations.ErrTAPDPersonalRate):
		writeError(w, r, 429, "TAPD_RATE_LIMITED", "TAPD temporarily limited requests; retry later")
	default:
		writeIntegrationError(w, r, err)
	}
}
