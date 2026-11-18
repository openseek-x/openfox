package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"openfox/internal/integrations/slack"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) registerSlackRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/slack"
	mux.HandleFunc("POST /v1/integrations/slack/events/{appID}", h.receiveSlackEvent)
	mux.HandleFunc("POST "+base+"/apps/operations/{appID}/{action}", h.manageSlackApp)
	mux.HandleFunc("POST "+base+"/installations/{appID}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("action", "disconnect")
		h.manageSlackApp(w, r)
	})
	mux.HandleFunc("POST "+base+"/installations/{appID}/reauthorize", h.startSlackOAuth)
	mux.HandleFunc("GET "+base+"/configuration", h.slackConfiguration)
	// These public paths overlap in ServeMux's wildcard specificity. A single
	// two-segment dispatcher preserves both contracts without ambiguous patterns.
	mux.HandleFunc("GET "+base+"/apps/{slackFirst}/{slackSecond}", func(w http.ResponseWriter, r *http.Request) {
		first, second := r.PathValue("slackFirst"), r.PathValue("slackSecond")
		if first == "operations" {
			r.SetPathValue("appID", second)
			h.getSlackApp(w, r)
			return
		}
		if second == "deliveries" {
			r.SetPathValue("appID", first)
			h.slackDeliveries(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST "+base+"/apps/{appID}/deliveries/{deliveryID}/resolve", h.resolveSlackDelivery)
	mux.HandleFunc("GET "+base+"/bots/{botID}/channels", h.slackChannels)
	mux.HandleFunc("PATCH "+base+"/bots/{botID}/channels/{channelID}", h.patchSlackChannel)
	mux.HandleFunc("POST "+base+"/oauth/start", h.startSlackOAuth)
	mux.HandleFunc("GET "+base+"/oauth/sessions/{sessionID}", h.getSlackOAuth)
	mux.HandleFunc("POST "+base+"/apps/operations", h.createSlackApp)
	mux.HandleFunc("DELETE "+base+"/personal/{identityID}", h.disconnectSlackIdentity)
	mux.HandleFunc("GET /v1/integrations/slack/callback", func(w http.ResponseWriter, r *http.Request) { h.slackCallback(w, r, false) })
	mux.HandleFunc("GET /v1/integrations/slack/identity/callback", func(w http.ResponseWriter, r *http.Request) { h.slackCallback(w, r, true) })
}
func slackNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func (h *Handler) slackConfiguration(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.SlackConfiguration
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.SlackConfiguration(ctx, tx, call.organizationID, call.selected.Actor)
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) startSlackOAuth(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in integrations.SlackOAuthInput
	if decodeJSON(w, r, &in) != nil {
		writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	if pathID := r.PathValue("appID"); pathID != "" && in.AppID != pathID {
		writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.SlackOAuthSession
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.StartSlackAuthorization(ctx, tx, call.organizationID, call.selected.Actor, in)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.oauth_started", "integration_oauth_session", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "slack", "result": in.Purpose})
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 201, out)
}
func (h *Handler) getSlackOAuth(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.SlackOAuthSession
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GetSlackOAuthSession(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"))
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) getSlackApp(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.SlackApp
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GetSlackApp(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("appID"))
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) createSlackApp(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in integrations.SlackCreateAppInput
	if decodeJSON(w, r, &in) != nil {
		writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	defer func() { in = integrations.SlackCreateAppInput{} }()
	var out integrations.SlackApp
	var fresh bool
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, fresh, e = h.integrations.PrepareSlackApp(ctx, tx, call.organizationID, call.selected.Actor, in)
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	if fresh {
		proof := h.integrations.RotateSlackConfiguration(r.Context(), in.ConfigurationRefreshToken)
		in.ConfigurationRefreshToken = ""
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.FinishSlackProof(ctx, tx, call.organizationID, call.selected.Actor, out.ID, out.Version, proof)
			if e != nil {
				return e
			}
			return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.slack_app_prepared", "integration_slack_app", out.ID, out.SpaceID, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "slack", "result": out.State})
		})
		if e != nil {
			writeSlackIntegrationError(w, r, e)
			return
		}
	}
	writeJSON(w, 202, out)
}
func (h *Handler) disconnectSlackIdentity(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in integrations.SlackVersionInput
	if decodeJSON(w, r, &in) != nil {
		writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if e := h.integrations.DisconnectSlackIdentity(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("identityID"), in.Version); e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.slack_identity_disconnected", "integration_slack_identity", r.PathValue("identityID"), "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "slack"})
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) slackCallback(w http.ResponseWriter, r *http.Request, personal bool) {
	slackNoStore(w)
	redirect := func(org, id, reason string) {
		q := url.Values{"provider": {"slack"}, "session": {id}, "organization": {org}, "status": {"ready"}}
		if reason != "" {
			q.Set("status", "error")
			q.Set("reason", reason)
		}
		http.Redirect(w, r, h.centerURL()+"/integrations/oauth/complete?"+q.Encode(), http.StatusFound)
	}
	state := r.URL.Query().Get("state")
	if len(state) < 16 || len(state) > 256 || h.pool == nil {
		redirect("", "", "invalid_state")
		return
	}
	var callbackUser string
	if personal {
		auth, err := h.authenticate(r)
		if err != nil {
			redirect("", "", "authorization_unavailable")
			return
		}
		callbackUser = auth.principal.UserID
	}
	var org, id string
	e := h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,session_id::text FROM openfox_private.slack_oauth_route($1)`, integrations.TokenHash(state)).Scan(&org, &id)
	})
	if e != nil {
		redirect("", "", "invalid_state")
		return
	}
	var target integrations.SlackCallbackTarget
	e = h.runAsWorker(r.Context(), org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = h.integrations.ClaimSlackCallback(ctx, tx, org, id, state, personal, callbackUser)
		return e
	})
	if e != nil {
		if personal {
			redirect("", "", "authorization_unavailable")
		} else {
			redirect(org, id, "authorization_unavailable")
		}
		return
	}
	outcome := integrations.SlackCallbackOutcome{Failure: "access_denied"}
	if r.URL.Query().Get("error") == "" {
		outcome = h.integrations.ExchangeSlackCallback(r.Context(), target, r.URL.Query().Get("code"))
	}
	verified := false
	if target.App.Kind == "CENTRAL" && !personal {
		e = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox_private.slack_deployment_verifications WHERE fingerprint=$1)`, h.integrations.SlackDeploymentFingerprint()).Scan(&verified)
		})
		if e != nil {
			redirect(org, id, "verification_unavailable")
			return
		}
	}
	var result integrations.SlackOAuthSession
	e = h.runAsWorker(r.Context(), org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		result, e = h.integrations.FinishSlackCallback(ctx, tx, org, target, outcome, verified)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), org, target.Actor, "integration.oauth_completed", "integration_oauth_session", id, "", target.Actor.AuthorizationVersion, map[string]string{"source": "slack", "result": result.Status})
	})
	if e != nil {
		redirect(org, id, "authorization_unavailable")
		return
	}
	redirect(org, id, result.StatusReason)
}

func (h *Handler) manageSlackApp(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	action, id := r.PathValue("action"), r.PathValue("appID")
	var out integrations.SlackApp
	var e error
	audit := func(ctx context.Context, tx pgx.Tx) error {
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.slack_app_changed", "integration_slack_app", out.ID, out.SpaceID, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "slack", "result": action + ":" + out.State})
	}
	switch action {
	case "cancel":
		var in integrations.SlackCancelInput
		if decodeJSON(w, r, &in) != nil {
			writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var err error
			out, err = h.integrations.CancelSlackApp(ctx, tx, call.organizationID, call.selected.Actor, id, in)
			if err != nil {
				return err
			}
			return audit(ctx, tx)
		})
	case "disconnect":
		var in integrations.SlackVersionInput
		if decodeJSON(w, r, &in) != nil {
			writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var err error
			out, err = h.integrations.DisconnectSlackApp(ctx, tx, call.organizationID, call.selected.Actor, id, in.Version)
			if err != nil {
				return err
			}
			return audit(ctx, tx)
		})
	case "recover", "configure":
		var in integrations.SlackRecoverAppInput
		if action == "recover" {
			e = decodeJSON(w, r, &in)
		} else {
			var config integrations.SlackConfigureInput
			e = decodeJSON(w, r, &config)
			in.Version = config.Version
			in.ConfigurationRefreshToken = config.ConfigurationRefreshToken
			config = integrations.SlackConfigureInput{}
		}
		if e != nil {
			writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
		defer func() { in = integrations.SlackRecoverAppInput{} }()
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var err error
			out, err = h.integrations.PrepareSlackReconfiguration(ctx, tx, call.organizationID, call.selected.Actor, id, in, action == "recover")
			return err
		})
		if e == nil {
			var proof integrations.SlackProofResult
			if action == "recover" {
				proof = h.integrations.VerifySlackRecovery(r.Context(), in)
			} else {
				proof = h.integrations.RotateSlackConfiguration(r.Context(), in.ConfigurationRefreshToken)
			}
			in.ConfigurationRefreshToken = ""
			e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
				var err error
				if action == "recover" {
					out, err = h.integrations.FinishSlackRecovery(ctx, tx, call.organizationID, call.selected.Actor, id, out.Version, in, proof)
				} else {
					out, err = h.integrations.FinishSlackProof(ctx, tx, call.organizationID, call.selected.Actor, id, out.Version, proof)
				}
				if err != nil {
					return err
				}
				return audit(ctx, tx)
			})
		}
	default:
		e = integrations.ErrInvalidRequest
	}
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	status := 200
	if action == "recover" || action == "configure" {
		status = 202
	}
	writeJSON(w, status, out)
}

func (h *Handler) receiveSlackEvent(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	if h.pool == nil {
		writeError(w, r, 503, "UNAVAILABLE", "Slack reception is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, slack.MaxEventBytes)
	raw, e := io.ReadAll(r.Body)
	if e != nil {
		writeError(w, r, 413, "BODY_TOO_LARGE", "Slack event exceeds the limit")
		return
	}
	if len(r.Header.Values("X-Slack-Request-Timestamp")) != 1 || len(r.Header.Values("X-Slack-Signature")) != 1 {
		writeError(w, r, 401, "INVALID_SIGNATURE", "Invalid Slack signature")
		return
	}
	timestamp, signature := r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature")
	ctx, cancel := context.WithTimeout(r.Context(), 2500*time.Millisecond)
	defer cancel()
	id, org := r.PathValue("appID"), ""
	if id == "central" {
		event, err := h.integrations.VerifySlackCentral(timestamp, signature, raw)
		if err != nil {
			writeError(w, r, 401, "INVALID_SIGNATURE", "Invalid Slack signature")
			return
		}
		if event.Type == "url_verification" {
			e = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO openfox_private.slack_deployment_verifications(fingerprint,verified_at) VALUES($1,clock_timestamp()) ON CONFLICT(fingerprint) DO UPDATE SET verified_at=EXCLUDED.verified_at`, h.integrations.SlackDeploymentFingerprint())
				return err
			})
			if e != nil {
				writeError(w, r, 503, "UNAVAILABLE", "Slack verification is unavailable")
				return
			}
			writeJSON(w, 200, map[string]string{"challenge": event.Challenge})
			return
		}
		e = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT organization_id::text,app_id::text FROM openfox_private.slack_central_route($1,$2)`, event.AppID, event.TeamID).Scan(&org, &id)
		})
		if errors.Is(e, pgx.ErrNoRows) {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	} else {
		e = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT organization_id::text FROM openfox_private.slack_app_route($1::uuid)`, id).Scan(&org)
		})
	}
	if e != nil {
		writeError(w, r, 404, "NOT_FOUND", "Slack application is unavailable")
		return
	}
	var challenge string
	e = h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		challenge, err = h.integrations.ReceiveSlackEvent(ctx, tx, org, id, timestamp, signature, raw)
		return err
	})
	if e != nil {
		if errors.Is(e, slack.ErrSignature) {
			writeError(w, r, 401, "INVALID_SIGNATURE", "Invalid Slack signature")
		} else if errors.Is(e, integrations.ErrInvalidRequest) || errors.Is(e, integrations.ErrVersionConflict) {
			writeError(w, r, 400, "INVALID_EVENT", "Invalid Slack event")
		} else {
			writeError(w, r, 503, "UNAVAILABLE", "Slack reception is unavailable")
		}
		return
	}
	if challenge != "" {
		writeJSON(w, 200, map[string]string{"challenge": challenge})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) slackChannels(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.SlackChannelsPage
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.SlackChannels(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), r.URL.Query().Get("cursor"))
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) patchSlackChannel(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in integrations.SlackChannelInput
	if decodeJSON(w, r, &in) != nil {
		writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.SlackChannel
	var e error
	audit := func(ctx context.Context, tx pgx.Tx) error {
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.slack_channel_changed", "integration_slack_channel", out.ID, out.SpaceID, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "slack"})
	}
	if !in.Enabled {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.DisableSlackChannel(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), r.PathValue("channelID"), in.Version)
			if e != nil {
				return e
			}
			return audit(ctx, tx)
		})
	} else {
		var target integrations.SlackChannelTarget
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			target, e = h.integrations.PrepareSlackChannel(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), r.PathValue("channelID"), in)
			return e
		})
		if e == nil {
			remote, err := h.integrations.VerifySlackChannel(r.Context(), target)
			target.Token = ""
			e = err
			if e == nil {
				e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
					var e error
					out, e = h.integrations.FinishSlackChannel(ctx, tx, call.organizationID, target, remote)
					if e != nil {
						return e
					}
					return audit(ctx, tx)
				})
			}
		}
	}
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}

func (h *Handler) slackDeliveries(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.SlackDeliveriesPage
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.SlackDeliveries(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("appID"), r.URL.Query().Get("cursor"))
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}

func writeSlackIntegrationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, slack.ErrRateLimited):
		var remote *slack.Error
		if errors.As(err, &remote) && remote.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(remote.RetryAfter/time.Second)+boolInt(remote.RetryAfter%time.Second != 0), 10))
		}
		writeError(w, r, 429, "SLACK_RATE_LIMITED", "Slack request budget is temporarily exhausted")
	case errors.Is(err, slack.ErrForbidden), errors.Is(err, integrations.ErrForbidden):
		writeError(w, r, 403, "FORBIDDEN", "Current identity or target permission is unavailable")
	case errors.Is(err, slack.ErrAuth), errors.Is(err, slack.ErrIdentity):
		writeError(w, r, 409, "NEEDS_REAUTH", "Slack authorization must be renewed")
	case errors.Is(err, slack.ErrNotFound):
		writeError(w, r, 404, "NOT_FOUND", "Slack resource is unavailable")
	case errors.Is(err, slack.ErrInvalid), errors.Is(err, slack.ErrUnsupported):
		writeError(w, r, 400, "INVALID_REQUEST", "Slack request is outside the supported scope")
	case errors.Is(err, slack.ErrIncomplete), errors.Is(err, slack.ErrUnavailable), errors.Is(err, slack.ErrUnknown):
		writeError(w, r, 502, "SLACK_PROTOCOL_ERROR", "Slack did not provide a verified result")
	default:
		writeIntegrationError(w, r, err)
	}
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func (h *Handler) resolveSlackDelivery(w http.ResponseWriter, r *http.Request) {
	slackNoStore(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in integrations.SlackResolveDeliveryInput
	if decodeJSON(w, r, &in) != nil {
		writeSlackIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var work integrations.SlackDeliveryResolution
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		work, e = h.integrations.PrepareSlackDeliveryResolution(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("appID"), r.PathValue("deliveryID"), in)
		return e
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	result := h.integrations.ExecuteSlackDeliveryResolution(r.Context(), work)
	var out integrations.SlackDelivery
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.FinishSlackDeliveryResolution(ctx, tx, call.organizationID, call.selected.Actor, work, result)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.slack_delivery_resolved", "integration_slack_delivery", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "slack", "result": in.Action + ":" + out.State})
	})
	if e != nil {
		writeSlackIntegrationError(w, r, e)
		return
	}
	writeJSON(w, 200, out)
}
