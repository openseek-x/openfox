package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/integrations/gitapi"
)

func (h *Handler) registerBitbucketOAuthRoutes(mux *http.ServeMux) {
	h.registerBitbucketRepositoryRoutes(mux)
	base := "/v1/organizations/{organizationID}/integrations/bitbucket/oauth"
	mux.HandleFunc("POST "+base+"/start", h.startBitbucketOAuth)
	mux.HandleFunc("GET "+base+"/sessions/{sessionID}", h.getBitbucketOAuthSession)
	mux.HandleFunc("POST "+base+"/sessions/{sessionID}/complete", h.completeBitbucketOAuth)
	mux.HandleFunc("GET /v1/integrations/bitbucket/callback", h.bitbucketOAuthCallback)
}

// Public failures are fixed codes/messages; never return upstream bodies, URLs,
// OAuth codes or consumer credentials to the browser.
func writeBitbucketError(w http.ResponseWriter, r *http.Request, err error) {
	var retry *gitapi.ErrRateLimited
	var lease *integrations.BitbucketRetry
	switch {
	case errors.Is(err, integrations.ErrPersonalIdentity):
		writeError(w, r, 422, "BITBUCKET_IDENTITY_MISMATCH", "the authorized identity is not valid for this confirmation")
	case errors.Is(err, integrations.ErrBitbucketAppChanged):
		writeError(w, r, 409, "BITBUCKET_APP_CHANGED", "Bitbucket consumer configuration changed; start authorization again")
	case errors.Is(err, integrations.ErrBitbucketAlreadyConnected):
		writeError(w, r, 409, "BITBUCKET_ALREADY_CONNECTED", "this organization already has a Bitbucket workspace")
	case errors.Is(err, bitbucket.ErrNotConfigured):
		writeError(w, r, 409, "INTEGRATION_NOT_CONFIGURED", "Bitbucket OAuth consumer is not configured")
	case errors.Is(err, bitbucket.ErrScope):
		writeError(w, r, 403, "BITBUCKET_SCOPE_NOT_ALLOWED", "Bitbucket consumer permissions do not match the authorization purpose")
	case errors.Is(err, bitbucket.ErrForbidden):
		writeError(w, r, 403, "BITBUCKET_PERMISSION_DENIED", "the authorized account must administer the selected workspace")
	case errors.Is(err, bitbucket.ErrInvalidGrant), errors.Is(err, gitapi.ErrTokenRevoked):
		writeError(w, r, 409, "BITBUCKET_REAUTHORIZE", "Bitbucket authorization must be renewed")
	case errors.As(err, &lease):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, min(3600, int(lease.After.Seconds())))))
		writeError(w, r, 429, "BITBUCKET_RATE_LIMITED", "Bitbucket authorization is busy; retry later")
	case errors.As(err, &retry):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, min(3600, int(time.Until(retry.ResetAt).Seconds())))))
		writeError(w, r, 429, "BITBUCKET_RATE_LIMITED", "Bitbucket request limit reached; retry later")
	case errors.Is(err, bitbucket.ErrIncomplete), errors.Is(err, bitbucket.ErrInvalid), errors.Is(err, gitapi.ErrUnknownResult), errors.Is(err, gitapi.ErrRequestFailed), errors.Is(err, gitapi.ErrExchangeFailed), errors.Is(err, gitapi.ErrNotFound):
		writeError(w, r, 502, "BITBUCKET_UPSTREAM_FAILED", "Bitbucket did not return a complete valid response")
	default:
		writePersonalBindingError(w, r, err)
	}
}
func bitbucketCallbackFailure(err error) string {
	switch {
	case errors.Is(err, integrations.ErrBitbucketAppChanged):
		return "configuration_changed"
	case errors.Is(err, integrations.ErrVersionConflict), errors.Is(err, integrations.ErrNeedsReauth):
		return "connection_changed"
	case errors.Is(err, integrations.ErrForbidden), errors.Is(err, bitbucket.ErrForbidden):
		return "permission_denied"
	case errors.Is(err, bitbucket.ErrScope):
		return "scope_denied"
	case errors.Is(err, integrations.ErrPersonalIdentity):
		return "account_mismatch"
	case errors.Is(err, integrations.ErrOAuthSessionState), errors.Is(err, integrations.ErrNotFound):
		return integrations.ReasonInvalidState
	default:
		return integrations.ReasonExchangeFailed
	}
}
func (h *Handler) startBitbucketOAuth(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BitbucketStartInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		result, e = h.integrations.StartBitbucket(ctx, tx, call.organizationID, call.selected.Actor, in)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "bitbucket", "result": in.Purpose})
	})
	if err != nil {
		writeBitbucketError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) getBitbucketOAuthSession(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result integrations.BitbucketSession
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		result, e = h.integrations.GetBitbucketSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		return e
	})
	if err != nil {
		writeBitbucketError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) completeBitbucketOAuth(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BitbucketCompleteInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	id := r.PathValue("sessionID")
	var view integrations.BitbucketSession
	// Commit an expiry transition independently. Do not hold a session lock and
	// then acquire grant/parent locks during a separate completion preparation.
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		view, e = h.integrations.GetBitbucketSession(ctx, tx, call.organizationID, call.selected.Actor, id)
		return e
	})
	if err != nil {
		writeBitbucketError(w, r, err)
		return
	}
	if view.Status != "SELECTING" && view.Status != "COMPLETED" {
		writeBitbucketError(w, r, integrations.ErrOAuthSessionState)
		return
	}
	personal := view.Purpose == integrations.PurposePersonal
	if personal && in.WorkspaceID != "" {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var target integrations.BitbucketCompleteTarget
	if !personal {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			target, e = h.integrations.PrepareBitbucketComplete(ctx, tx, call.organizationID, call.selected.Actor, id, in)
			return e
		})
		if err != nil {
			writeBitbucketError(w, r, err)
			return
		}
		target, err = h.integrations.VerifyBitbucketComplete(r.Context(), target)
		if err != nil {
			writeBitbucketError(w, r, err)
			return
		}
	}
	var result integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var changed bool
		var e error
		if personal {
			result, changed, e = h.integrations.CompleteBitbucketPersonal(ctx, tx, call.organizationID, call.selected.Actor, id)
		} else {
			result, changed, e = h.integrations.CompleteBitbucketOrganization(ctx, tx, call.organizationID, call.selected.Actor, id, target)
		}
		if e != nil || !changed {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_bound", "integration_connection", result.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "bitbucket", "result": view.Purpose})
	})
	if err != nil {
		writeBitbucketError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) bitbucketOAuthCallback(w http.ResponseWriter, r *http.Request) {
	redirect := func(org, id, reason string) {
		q := url.Values{"provider": {"bitbucket"}, "session": {id}, "organization": {org}, "status": {"ready"}}
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
	if err != nil || user != auth.principal.UserID || provider != integrations.ProviderBitbucket {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, org)
	if err != nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: org, selected: selected}
	var target integrations.BitbucketCallbackTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.ConsumeBitbucketCallback(ctx, tx, org, selected.Actor, id)
		return e
	})
	if err != nil {
		redirect(org, id, bitbucketCallbackFailure(err))
		return
	}
	failure := ""
	var result integrations.BitbucketCallbackResult
	if r.URL.Query().Get("error") != "" {
		failure = integrations.ReasonAccessDenied
	} else if code == "" || len(code) > 8192 {
		failure = integrations.ReasonExchangeFailed
	} else {
		result, err = h.integrations.VerifyBitbucketCallback(r.Context(), target, code)
		if err != nil {
			failure = bitbucketCallbackFailure(err)
		}
	}
	// Finish returns nil after persisting FAILED, so the failure isn't rolled
	// back by propagating the external error through the transaction callback.
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.integrations.FinishBitbucketCallback(ctx, tx, org, selected.Actor, id, result, failure)
	})
	if err != nil {
		redirect(org, id, bitbucketCallbackFailure(err))
		return
	}
	var view integrations.BitbucketSession
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		view, e = h.integrations.GetBitbucketSession(ctx, tx, org, selected.Actor, id)
		return e
	})
	if err != nil {
		redirect(org, id, bitbucketCallbackFailure(err))
		return
	}
	redirect(org, id, view.ErrorCode)
}
