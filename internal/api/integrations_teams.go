package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/teams"
	"openfox/internal/membership"
)

type teamsAPIAction func(context.Context, pgx.Tx, string, collaboration.Actor) (any, error)

func (h *Handler) registerTeamsRoutes(m *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/teams"
	m.HandleFunc("POST /v1/hooks/teams/{botID}/activities", h.receiveTeamsActivity)
	m.HandleFunc("POST "+base+"/bots", h.saveTeamsCredentials)
	m.HandleFunc("PUT "+base+"/bots/{botID}/credentials", h.saveTeamsCredentials)
	m.HandleFunc("PUT "+base+"/bots/{botID}/scopes/{scopeID}", h.saveTeamsScope)
	m.HandleFunc("GET "+base+"/bots", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TeamsBots(ctx, tx, org, a)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetTeamsBot(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("DELETE "+base+"/bots/{botID}", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.TeamsVersionInput
		h.teamsJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.RemoveTeamsBot(ctx, tx, org, a, r.PathValue("botID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/manifest.zip", h.downloadTeamsManifest)
	m.HandleFunc("GET "+base+"/personal-targets", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TeamsPersonalTargets(ctx, tx, org, a)
		})
	})
	m.HandleFunc("GET "+base+"/personal-bindings", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TeamsPersonalBindings(ctx, tx, org, a)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/binding-challenges", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.IssueTeamsChallenge(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("DELETE "+base+"/bots/{botID}/personal-binding", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.TeamsVersionInput
		h.teamsJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.RemoveTeamsPersonalBinding(ctx, tx, org, a, r.PathValue("botID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/scopes", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TeamsScopes(ctx, tx, org, a, r.PathValue("botID"), r.URL.Query().Get("after"))
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		h.teamsJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TeamsDeliveries(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
}
func teamsResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func (h *Handler) teamsJSON(w http.ResponseWriter, r *http.Request, mutation bool, in any, action teamsAPIAction) {
	teamsResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return
	}
	if in != nil && decodeJSON(w, r, in) != nil {
		writeTeamsError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = action(ctx, tx, call.organizationID, call.selected.Actor)
		return e
	})
	if err != nil {
		writeTeamsError(w, r, err)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) saveTeamsCredentials(w http.ResponseWriter, r *http.Request) {
	teamsResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.TeamsConnectInput
	var update integrations.TeamsCredentialsInput
	var err error
	if r.Method == http.MethodPost {
		err = decodeJSON(w, r, &in)
	} else {
		err = decodeJSON(w, r, &update)
	}
	if err != nil {
		writeTeamsError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.TeamsConnectTarget
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		if r.Method == http.MethodPost {
			plan, e = h.integrations.PrepareTeamsConnection(ctx, tx, call.organizationID, call.selected.Actor, in)
		} else {
			plan, e = h.integrations.PrepareTeamsCredentials(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), update)
		}
		return e
	})
	var proof integrations.TeamsConnectProof
	if err == nil {
		proof, err = h.integrations.VerifyTeamsConnection(r.Context(), plan)
	}
	var out integrations.TeamsBot
	if err == nil {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveTeamsConnection(ctx, tx, call.organizationID, call.selected.Actor, plan, proof)
			return e
		})
	}
	if err != nil {
		writeTeamsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) saveTeamsScope(w http.ResponseWriter, r *http.Request) {
	teamsResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.TeamsScopeInput
	if decodeJSON(w, r, &in) != nil {
		writeTeamsError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.TeamsScopeTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		plan, e = h.integrations.PrepareTeamsScope(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), r.PathValue("scopeID"), in)
		return e
	})
	var proof integrations.TeamsScopeProof
	if err == nil {
		proof, err = h.integrations.VerifyTeamsScope(r.Context(), plan)
	}
	var out integrations.TeamsScope
	if err == nil {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveTeamsScope(ctx, tx, call.organizationID, call.selected.Actor, plan, proof)
			return e
		})
	}
	if err != nil {
		writeTeamsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) downloadTeamsManifest(w http.ResponseWriter, r *http.Request) {
	teamsResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var zip []byte
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		zip, e = h.integrations.TeamsManifest(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"))
		return e
	})
	if err != nil {
		writeTeamsError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="openfox-teams.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(zip)
}
func writeTeamsError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 500, "INTERNAL_ERROR", "request could not be completed"
	var upstream *teams.Error
	switch {
	case errors.Is(err, integrations.ErrInvalidRequest), errors.Is(err, teams.ErrInvalid):
		status, code, message = 400, "INVALID_REQUEST", "request is invalid"
	case errors.Is(err, integrations.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "resource not found"
	case errors.Is(err, integrations.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "permission denied"
	case errors.Is(err, integrations.ErrVersionConflict):
		status, code, message = 409, "VERSION_CONFLICT", "configuration changed; refresh before retrying"
	case errors.Is(err, integrations.ErrNotConfigured), errors.Is(err, integrations.ErrSecretsUnavailable):
		status, code, message = 503, "TEAMS_CONFIGURATION_UNAVAILABLE", "configuration is unavailable"
	case errors.Is(err, teams.ErrUnsupported), errors.Is(err, teams.ErrChannelScope):
		status, code, message = 422, "TEAMS_UNSUPPORTED_SCOPE", "the scope is not supported or cannot be verified"
	case errors.Is(err, teams.ErrUnauthorized):
		status, code, message = 401, "TEAMS_UNAUTHORIZED", "activity authentication failed"
	case errors.As(err, &upstream):
		status, code, message = 502, "TEAMS_UNAVAILABLE", "Teams could not complete the request"
		if upstream.Status == 429 {
			status, code, message = 429, "TEAMS_RATE_LIMITED", "Teams requested a later retry"
			if upstream.RetryAfterValid {
				seconds := int64(upstream.RetryAfter / time.Second)
				if upstream.RetryAfter%time.Second != 0 {
					seconds++
				}
				w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			}
		}
		if upstream.Status == 400 || upstream.Status == 401 || upstream.Status == 403 || upstream.Status == 404 {
			status, code, message = 422, "TEAMS_VERIFICATION_FAILED", "credentials or target could not be verified"
		}
	case errors.Is(err, teams.ErrUnavailable), errors.Is(err, teams.ErrIncomplete), errors.Is(err, integrations.ErrUpstreamUnavailable), errors.Is(err, context.DeadlineExceeded):
		status, code, message = 503, "TEAMS_UNAVAILABLE", "Teams could not complete the request"
	}
	writeError(w, r, status, code, message)
}
func (h *Handler) receiveTeamsActivity(w http.ResponseWriter, r *http.Request) {
	teamsResponseHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = r.Body.Close(); _ = controller.SetReadDeadline(time.Time{}) }()
	botID := r.PathValue("botID")
	if !membership.ValidateUUID(botID) {
		writeTeamsError(w, r, integrations.ErrNotFound)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeTeamsError(w, r, integrations.ErrInvalidRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, teams.MaxActivityBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "activity is too large")
		} else {
			writeTeamsError(w, r, integrations.ErrInvalidRequest)
		}
		return
	}
	if h.pool == nil {
		writeTeamsError(w, r, integrations.ErrNotConfigured)
		return
	}
	var org, tenant, app, id string
	err = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,bot_id::text,tenant_id,app_id FROM openfox_private.resolve_teams_webhook($1::uuid)`, botID).Scan(&org, &id, &tenant, &app)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeTeamsError(w, r, integrations.ErrNotFound)
		return
	}
	if err != nil {
		writeError(w, r, 503, "UNAVAILABLE", "webhook is unavailable")
		return
	}
	event, err := h.integrations.AuthenticateTeamsActivity(ctx, r.Header.Get("Authorization"), raw, tenant, app)
	if err != nil {
		writeTeamsError(w, r, err)
		return
	}
	var plan integrations.TeamsReceiveTarget
	err = h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = h.integrations.PrepareTeamsActivity(ctx, tx, org, id, event)
		return e
	})
	var proof integrations.TeamsReceiveProof
	if err == nil {
		proof, err = h.integrations.VerifyTeamsActivity(ctx, plan)
	}
	var out integrations.TeamsReceiveResult
	if err == nil {
		err = h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			out, e = h.integrations.ReceiveTeamsActivity(ctx, tx, org, plan, proof)
			return e
		})
	}
	if err != nil {
		writeTeamsError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
