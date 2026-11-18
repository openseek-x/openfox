package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/zohobugtracker"
)

func (h *Handler) registerZohoRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/zoho_bugtracker/oauth"
	mux.HandleFunc("POST "+base+"/start", h.startZoho)
	mux.HandleFunc("GET "+base+"/sessions/{sessionID}", h.getZohoSession)
	mux.HandleFunc("POST "+base+"/sessions/{sessionID}/complete", h.completeZoho)
	mux.HandleFunc("GET /v1/integrations/zoho_bugtracker/callback", h.zohoCallback)
}
func (h *Handler) zohoRuntime() integrations.ZohoRuntime {
	return integrations.ZohoRuntime{Pool: h.pool, Service: h.integrations}
}
func writeZohoError(w http.ResponseWriter, r *http.Request, err error) {
	var upstream *zohobugtracker.Error
	var retry *integrations.ZohoRetry
	switch {
	case errors.Is(err, imports.ErrSpaceNotFound), errors.Is(err, imports.ErrForbidden), errors.Is(err, imports.ErrInvalidRequest), errors.Is(err, imports.ErrImportActive):
		writeImportError(w, r, err)
	case errors.Is(err, integrations.ErrZohoAppChanged):
		writeError(w, r, 409, "ZOHO_APP_CHANGED", "Zoho application changed; disconnect this portal locally and create a new connection")
	case errors.As(err, &retry):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.After.Seconds()))))
		writeError(w, r, 429, "ZOHO_RETRY_LATER", "Zoho authorization is busy or rate limited; retry later")
	case errors.Is(err, integrations.ErrZohoGrantInUse):
		writeError(w, r, 409, "ZOHO_GRANT_IN_USE", "this authorization is already in use; create a separate authorization")
	case errors.Is(err, integrations.ErrZohoLimit):
		writeError(w, r, 422, "ZOHO_DIRECTORY_LIMIT", "Zoho directory exceeds supported verification limits")
	case errors.Is(err, integrations.ErrZohoAlreadyConnected):
		writeError(w, r, 409, "ZOHO_ALREADY_CONNECTED", "this portal is already connected; reauthorize its existing connection")
	case errors.As(err, &upstream):
		switch upstream.HTTPStatus {
		case 401:
			writeError(w, r, 409, "ZOHO_REAUTHORIZE", "Zoho authorization must be renewed")
		case 403:
			writeError(w, r, 403, "ZOHO_PERMISSION_DENIED", "the delegating account cannot read this Zoho resource")
		case 404:
			writeError(w, r, 404, "ZOHO_RESOURCE_UNAVAILABLE", "the Zoho resource is unavailable to this connection")
		case 429:
			if upstream.RetryAfterValid {
				w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(upstream.RetryAfter.Seconds())), 10))
			}
			writeError(w, r, 429, "ZOHO_RATE_LIMITED", "Zoho request limit reached; retry later")
		default:
			writeError(w, r, 502, "ZOHO_UPSTREAM_FAILED", "Zoho did not return a valid response")
		}
	default:
		writePersonalBindingError(w, r, err)
	}
}
func (h *Handler) startZoho(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.ZohoStartInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeZohoError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.StartZoho(ctx, tx, call.organizationID, call.selected.Actor, in)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "zoho_bugtracker", "result": in.Purpose})
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) getZohoSession(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result integrations.ZohoSession
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.GetZohoSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		return err
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) completeZoho(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.ZohoCompleteInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeZohoError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var target integrations.ZohoCompleteTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.PrepareZohoComplete(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"), in)
		return e
	})
	if err == nil {
		target, err = h.integrations.VerifyZohoComplete(r.Context(), h.zohoRuntime(), call.organizationID, target)
	}
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	var result integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		var changed bool
		result, changed, e = h.integrations.CompleteZoho(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"), target)
		if e != nil || !changed {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_bound", "integration_connection", result.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "zoho_bugtracker"})
	})
	if err != nil {
		writeZohoError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) zohoCallback(w http.ResponseWriter, r *http.Request) {
	redirect := func(org, id, reason string) {
		query := url.Values{"provider": {"zoho_bugtracker"}, "session": {id}, "organization": {org}, "status": {"ready"}}
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
	if err != nil || len(state) < 1 || len(state) > 128 || h.pool == nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	var org, id, user, provider string
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,state_id::text,user_id::text,provider FROM openfox_private.resolve_integration_oauth_state($1)`, integrations.TokenHash(state)).Scan(&org, &id, &user, &provider)
	})
	if err != nil || user != auth.principal.UserID || provider != "zoho_bugtracker" {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, org)
	if err != nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: org, selected: selected}
	var target integrations.ZohoCallbackTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.ConsumeZohoCallback(ctx, tx, org, selected.Actor, id)
		return e
	})
	if err != nil {
		redirect(org, id, integrations.PersonalFailure(err))
		return
	}
	failure := ""
	var result integrations.ZohoCallbackResult
	if r.URL.Query().Get("error") != "" {
		failure = integrations.ReasonAccessDenied
	} else if code == "" || len(code) > 4096 {
		failure = integrations.ReasonExchangeFailed
	} else {
		result, err = h.integrations.VerifyZohoCallback(r.Context(), h.zohoRuntime(), org, target, code)
		if err != nil {
			failure = integrations.PersonalFailure(err)
		}
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if e := h.integrations.FinishZohoCallback(ctx, tx, org, selected.Actor, id, result, failure); e != nil {
			return e
		}
		view, e := h.integrations.GetZohoSession(ctx, tx, org, selected.Actor, id)
		failure = view.ErrorCode
		return e
	})
	if err != nil {
		redirect(org, id, integrations.PersonalFailure(err))
		return
	}
	redirect(org, id, failure)
}
