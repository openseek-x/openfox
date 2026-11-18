package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
)

// Registration is intentionally separate; the I12 owner wires this once in the
// handler after integrating this isolated OAuth slice.
func (h *Handler) registerAzureOAuthRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/azure_devops/oauth"
	mux.HandleFunc("POST "+base+"/start", h.startAzureOAuth)
	mux.HandleFunc("GET "+base+"/sessions/{sessionID}", h.getAzureOAuthSession)
	mux.HandleFunc("POST "+base+"/sessions/{sessionID}/complete", h.completeAzureOAuth)
	mux.HandleFunc("GET /v1/integrations/azure_devops/callback", func(w http.ResponseWriter, r *http.Request) { h.azureOAuthCallback(w, r, false) })
	mux.HandleFunc("GET /v1/integrations/azure_devops/identity/callback", func(w http.ResponseWriter, r *http.Request) { h.azureOAuthCallback(w, r, true) })
}
func (h *Handler) azureRuntime() integrations.AzureRuntime {
	return integrations.AzureRuntime{Pool: h.pool, Service: h.integrations}
}
func writeAzureError(w http.ResponseWriter, r *http.Request, err error) {
	var upstream *azuredevops.Error
	var retry *integrations.AzureRetry
	switch {
	case errors.Is(err, integrations.ErrAzureAppChanged):
		writeError(w, r, 409, "AZURE_APP_CHANGED", "Azure application changed; disconnect locally and reconnect")
	case errors.Is(err, integrations.ErrAzureAlreadyConnected):
		writeError(w, r, 409, "AZURE_ALREADY_CONNECTED", "this Azure DevOps organization is already connected")
	case errors.Is(err, integrations.ErrPersonalCapability):
		writeError(w, r, 403, "AZURE_SCOPE_NOT_ALLOWED", "Azure token scopes must match the configured least-privilege application")
	case errors.Is(err, integrations.ErrPersonalIdentity):
		writeError(w, r, 422, "AZURE_IDENTITY_MISMATCH", "the authorized identity must be an active member of this exact Azure DevOps organization")
	case errors.As(err, &retry):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.After.Seconds()))))
		writeError(w, r, 429, "AZURE_RETRY_LATER", "Azure authorization is busy or rate limited; retry later")
	case errors.As(err, &upstream):
		switch {
		case upstream.Code == "rate_limited" || upstream.HTTPStatus == 429:
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(upstream.RetryAfter.Seconds()))))
			writeError(w, r, 429, "AZURE_RATE_LIMITED", "Azure request limit reached; retry later")
		case upstream.HTTPStatus == 401 || upstream.Code == "invalid_grant":
			writeError(w, r, 409, "AZURE_REAUTHORIZE", "Azure authorization must be renewed")
		case upstream.HTTPStatus == 403:
			writeError(w, r, 403, "AZURE_PERMISSION_DENIED", "the delegating user cannot access this Azure DevOps resource")
		case upstream.HTTPStatus == 404:
			writeError(w, r, 404, "AZURE_RESOURCE_UNAVAILABLE", "the Azure DevOps resource is unavailable to this authorization")
		case upstream.Code == "not_configured" || upstream.Code == "invalid_client":
			writeError(w, r, 409, "AZURE_CONFIGURATION_REQUIRED", "Azure OAuth application configuration must be checked")
		default:
			writeError(w, r, 502, "AZURE_UPSTREAM_FAILED", "Azure DevOps did not return a valid response")
		}
	default:
		writePersonalBindingError(w, r, err)
	}
}
func (h *Handler) startAzureOAuth(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.AzureStartInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeAzureError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		result, e = h.integrations.StartAzure(ctx, tx, call.organizationID, call.selected.Actor, in)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "azure_devops", "result": in.Purpose})
	})
	if err != nil {
		writeAzureError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) getAzureOAuthSession(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result integrations.AzureSession
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		result, e = h.integrations.GetAzureSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		return e
	})
	if err != nil {
		writeAzureError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) completeAzureOAuth(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.AzureCompleteInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeAzureError(w, r, integrations.ErrInvalidRequest)
		return
	}
	id := r.PathValue("sessionID")
	var view integrations.AzureSession
	// Commit an expiry transition independently. Do not hold a session lock and
	// then acquire grant/parent locks during a separate completion preparation.
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		view, e = h.integrations.GetAzureSession(ctx, tx, call.organizationID, call.selected.Actor, id)
		return e
	})
	if err != nil {
		writeAzureError(w, r, err)
		return
	}
	if view.Status != "SELECTING" && view.Status != "COMPLETED" {
		writeAzureError(w, r, integrations.ErrOAuthSessionState)
		return
	}
	personal := view.Purpose == integrations.PurposePersonal
	if personal && in.AccountID != "" {
		writeAzureError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var target integrations.AzureCompleteTarget
	if !personal {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			target, e = h.integrations.PrepareAzureComplete(ctx, tx, call.organizationID, call.selected.Actor, id, in)
			return e
		})
		if err != nil {
			writeAzureError(w, r, err)
			return
		}
		target, err = h.integrations.VerifyAzureComplete(r.Context(), target)
		if err != nil {
			writeAzureError(w, r, err)
			return
		}
	}
	var result integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var changed bool
		var e error
		if personal {
			result, changed, e = h.integrations.CompleteAzurePersonal(ctx, tx, call.organizationID, call.selected.Actor, id)
		} else {
			result, changed, e = h.integrations.CompleteAzureOrganization(ctx, tx, call.organizationID, call.selected.Actor, id, target)
		}
		if e != nil || !changed {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_bound", "integration_connection", result.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "azure_devops", "result": view.Purpose})
	})
	if err != nil {
		writeAzureError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) azureOAuthCallback(w http.ResponseWriter, r *http.Request, personal bool) {
	redirect := func(org, id, reason string) {
		q := url.Values{"provider": {"azure_devops"}, "session": {id}, "organization": {org}, "status": {"ready"}}
		if reason != "" {
			q.Set("status", "error")
			q.Set("reason", reason)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, h.centerURL()+"/integrations/oauth/complete?"+q.Encode(), http.StatusFound)
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
	if err != nil || user != auth.principal.UserID || provider != integrations.ProviderAzure {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, org)
	if err != nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: org, selected: selected}
	var target integrations.AzureCallbackTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.ConsumeAzureCallback(ctx, tx, org, selected.Actor, id, personal)
		return e
	})
	if err != nil {
		redirect(org, id, integrations.AzureFailure(err))
		return
	}
	failure := ""
	var result integrations.AzureCallbackResult
	if r.URL.Query().Get("error") != "" {
		failure = integrations.ReasonAccessDenied
	} else if code == "" || len(code) > 32768 {
		failure = integrations.ReasonExchangeFailed
	} else {
		result, err = h.integrations.VerifyAzureCallback(r.Context(), h.azureRuntime(), org, target, code)
		if err != nil {
			failure = integrations.AzureFailure(err)
		}
	}
	// Finish returns nil after persisting FAILED, so the failure isn't rolled
	// back by propagating the external error through the transaction callback.
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.integrations.FinishAzureCallback(ctx, tx, org, selected.Actor, id, result, failure)
	})
	if err != nil {
		redirect(org, id, integrations.AzureFailure(err))
		return
	}
	var view integrations.AzureSession
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		view, e = h.integrations.GetAzureSession(ctx, tx, org, selected.Actor, id)
		return e
	})
	if err != nil {
		redirect(org, id, integrations.AzureFailure(err))
		return
	}
	redirect(org, id, view.ErrorCode)
}
