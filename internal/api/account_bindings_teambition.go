package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) registerTeambitionBindingRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/me/account-bindings/teambition"
	mux.HandleFunc("POST "+base+"/sessions", h.startTeambitionBinding)
	mux.HandleFunc("GET "+base+"/sessions/{sessionID}", h.getTeambitionBinding)
	mux.HandleFunc("POST "+base+"/sessions/{sessionID}/complete", h.completeTeambitionBinding)
	mux.HandleFunc("GET /v1/integrations/teambition/callback", h.teambitionBindingCallback)
}

func writePersonalBindingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, integrations.ErrPersonalIdentity):
		writeError(w, r, 422, "PERSONAL_IDENTITY_MISMATCH", "the authorized identity must match your organization email")
	case errors.Is(err, integrations.ErrPersonalInUse):
		writeError(w, r, 409, "PERSONAL_IDENTITY_IN_USE", "this identity is already bound")
	case errors.Is(err, integrations.ErrPersonalCapability):
		writeError(w, r, 403, "PERSONAL_CAPABILITY_DENIED", "the application cannot verify your enterprise membership")
	case errors.Is(err, integrations.ErrPersonalRateLimited):
		writeError(w, r, 429, "PERSONAL_RATE_LIMITED", "identity service rate limited")
	default:
		writeIntegrationError(w, r, err)
	}
}

func (h *Handler) startTeambitionBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.TeambitionStartInput
	if err := decodeJSONLimit(w, r, 8<<10, &input); err != nil {
		writeError(w, r, 400, "INVALID_REQUEST", "invalid request")
		return
	}
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.StartTeambitionPersonal(ctx, tx, call.organizationID, call.selected.Actor, input)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "teambition", "result": "PERSONAL"})
	})
	if err != nil {
		writePersonalBindingError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (h *Handler) getTeambitionBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result integrations.TeambitionSession
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.GetTeambitionSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		return err
	})
	if err != nil {
		writePersonalBindingError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

func (h *Handler) completeTeambitionBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var result integrations.Connection
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var changed bool
		var err error
		result, changed, err = h.integrations.CompleteTeambitionPersonal(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		if err != nil || !changed {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_bound", "integration_connection", result.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "teambition", "result": "PERSONAL"})
	})
	if err != nil {
		writePersonalBindingError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *Handler) teambitionBindingCallback(w http.ResponseWriter, r *http.Request) {
	redirect := func(org, id, reason string) {
		query := url.Values{"provider": {"teambition"}, "session": {id}, "organization": {org}, "status": {"ready"}}
		if reason != "" {
			query.Set("status", "error")
			query.Set("reason", reason)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, h.centerURL()+"/integrations/oauth/complete?"+query.Encode(), http.StatusFound)
	}
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	auth, err := h.authenticate(r)
	if err != nil || state == "" || len(state) > 128 || h.pool == nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	var org, id, user, provider string
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,state_id::text,user_id::text,provider FROM openfox_private.resolve_integration_oauth_state($1)`, integrations.TokenHash(state)).Scan(&org, &id, &user, &provider)
	})
	if err != nil || user != auth.principal.UserID || provider != "teambition" {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, org)
	if err != nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: org, selected: selected}
	var target integrations.TeambitionCallbackTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.ConsumeTeambitionCallback(ctx, tx, org, selected.Actor, id)
		return e
	})
	if err != nil {
		redirect(org, id, integrations.PersonalFailure(err))
		return
	}
	failure := ""
	var identity integrations.TeambitionIdentity
	if r.URL.Query().Get("error") != "" {
		failure = integrations.ReasonAccessDenied
	} else if code == "" || len(code) > 4096 {
		failure = integrations.ReasonExchangeFailed
	} else {
		identity, err = h.integrations.VerifyTeambitionCallback(r.Context(), target, code)
		if err != nil {
			failure = integrations.PersonalFailure(err)
		}
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if err := h.integrations.FinishTeambitionCallback(ctx, tx, org, selected.Actor, id, identity, failure); err != nil {
			return err
		}
		view, err := h.integrations.GetTeambitionSession(ctx, tx, org, selected.Actor, id)
		failure = view.ErrorCode
		return err
	})
	if err != nil {
		redirect(org, id, integrations.PersonalFailure(err))
		return
	}
	redirect(org, id, failure)
}
