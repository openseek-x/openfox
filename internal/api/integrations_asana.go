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
	"openfox/internal/integrations/asana"
)

func (h *Handler) registerAsanaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/imports/{importID}/asana/types", h.asanaSyncTypes)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/imports/{importID}/asana/types", h.asanaSyncTypes)
	base := "/v1/organizations/{organizationID}/integrations/asana/oauth"
	mux.HandleFunc("POST "+base+"/start", h.startAsana)
	mux.HandleFunc("GET "+base+"/sessions/{sessionID}", h.getAsanaSession)
	mux.HandleFunc("POST "+base+"/sessions/{sessionID}/complete", h.completeAsana)
	mux.HandleFunc("GET /v1/integrations/asana/callback", func(w http.ResponseWriter, r *http.Request) { h.asanaCallback(w, r, false) })
	mux.HandleFunc("GET /v1/integrations/asana/identity/callback", func(w http.ResponseWriter, r *http.Request) { h.asanaCallback(w, r, true) })
}
func (h *Handler) asanaRuntime() integrations.AsanaRuntime {
	return integrations.AsanaRuntime{Pool: h.pool, Service: h.integrations}
}
func writeAsanaError(w http.ResponseWriter, r *http.Request, err error) {
	var upstream *asana.Error
	var retry *integrations.AsanaRetry
	switch {
	case errors.Is(err, integrations.ErrAsanaAppChanged):
		writeError(w, r, 409, "ASANA_APP_CHANGED", "Asana application changed; disconnect this workspace locally and create a new connection")
	case errors.As(err, &retry):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.After.Seconds()))))
		writeError(w, r, 429, "ASANA_RETRY_LATER", "Asana authorization is busy or rate limited; retry later")
	case errors.Is(err, integrations.ErrAsanaAlreadyConnected):
		writeError(w, r, 409, "ASANA_ALREADY_CONNECTED", "this workspace is already connected; reauthorize its existing connection")
	case errors.As(err, &upstream):
		switch upstream.HTTPStatus {
		case 401:
			writeError(w, r, 409, "ASANA_REAUTHORIZE", "Asana authorization must be renewed")
		case 403:
			writeError(w, r, 403, "ASANA_PERMISSION_DENIED", "the delegating account cannot read this Asana resource")
		case 404:
			writeError(w, r, 404, "ASANA_RESOURCE_UNAVAILABLE", "the Asana resource is unavailable to this connection")
		case 429:
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(upstream.RetryAfter.Seconds()))))
			writeError(w, r, 429, "ASANA_RATE_LIMITED", "Asana request limit reached; retry later")
		default:
			writeError(w, r, 502, "ASANA_UPSTREAM_FAILED", "Asana did not return a valid response")
		}
	default:
		writePersonalBindingError(w, r, err)
	}
}
func (h *Handler) startAsana(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.AsanaStartInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeAsanaError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var result integrations.StartResult
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.StartAsana(ctx, tx, call.organizationID, call.selected.Actor, in)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.oauth_started", "integration_oauth_session", result.SessionID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "asana", "result": in.Purpose})
	})
	if err != nil {
		writeAsanaError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) getAsanaSession(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result integrations.AsanaSession
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		result, err = h.integrations.GetAsanaSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		return err
	})
	if err != nil {
		writeAsanaError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) completeAsana(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.AsanaCompleteInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeAsanaError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var view integrations.AsanaSession
	var target integrations.AsanaCompleteTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		view, e = h.integrations.GetAsanaSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		if e != nil {
			return e
		}
		if view.Purpose != integrations.PurposePersonal {
			target, e = h.integrations.PrepareAsanaComplete(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"), in)
		}
		return e
	})
	if err != nil {
		writeAsanaError(w, r, err)
		return
	}
	if view.Purpose != integrations.PurposePersonal {
		if err = h.integrations.VerifyAsanaComplete(r.Context(), target); err != nil {
			writeAsanaError(w, r, err)
			return
		}
	}
	var result integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		var changed bool
		if view.Purpose == integrations.PurposePersonal {
			result, changed, e = h.integrations.CompleteAsanaPersonal(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		} else {
			result, changed, e = h.integrations.CompleteAsanaOrganization(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"), target)
		}
		if e != nil || !changed {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_bound", "integration_connection", result.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "asana", "result": view.Purpose})
	})
	if err != nil {
		writeAsanaError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) asanaCallback(w http.ResponseWriter, r *http.Request, personal bool) {
	redirect := func(org, id, reason string) {
		query := url.Values{"provider": {"asana"}, "session": {id}, "organization": {org}, "status": {"ready"}}
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
	if err != nil || user != auth.principal.UserID || provider != "asana" {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	selected, err := h.selectCollaboration(r.Context(), collaborationAuth{Auth: auth}, org)
	if err != nil {
		redirect("", "", integrations.ReasonInvalidState)
		return
	}
	call := taskCall{organizationID: org, selected: selected}
	var target integrations.AsanaCallbackTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.ConsumeAsanaCallback(ctx, tx, org, selected.Actor, id, personal)
		return e
	})
	if err != nil {
		redirect(org, id, integrations.PersonalFailure(err))
		return
	}
	failure := ""
	var result integrations.AsanaCallbackResult
	if r.URL.Query().Get("error") != "" {
		failure = integrations.ReasonAccessDenied
	} else if code == "" || len(code) > 4096 {
		failure = integrations.ReasonExchangeFailed
	} else {
		result, err = h.integrations.VerifyAsanaCallback(r.Context(), h.asanaRuntime(), org, target, code)
		if err != nil {
			failure = integrations.PersonalFailure(err)
		}
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if e := h.integrations.FinishAsanaCallback(ctx, tx, org, selected.Actor, id, result, failure); e != nil {
			return e
		}
		view, e := h.integrations.GetAsanaSession(ctx, tx, org, selected.Actor, id)
		failure = view.ErrorCode
		return e
	})
	if err != nil {
		redirect(org, id, integrations.PersonalFailure(err))
		return
	}
	redirect(org, id, failure)
}
