package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations"
	"openfox/internal/integrations/qq"
)

type qqVersionInput struct {
	Version uint64 `json:"version"`
}

type qqAPIAction func(context.Context, pgx.Tx, string, collaboration.Actor) (any, error)

func (h *Handler) registerQQRoutes(m *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/qq"
	m.HandleFunc("POST /v1/hooks/qq/{routeToken}", h.receiveQQWebhook)
	m.HandleFunc("GET "+base+"/configuration", func(w http.ResponseWriter, r *http.Request) {
		h.qqJSON(w, r, false, http.StatusOK, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.QQConfiguration(ctx, tx, org, a)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/release", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.QQQRActionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.ReleaseQQBot(ctx, tx, org, a, r.PathValue("botID"), in)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/qr", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.QQQRStartInput
		h.qqJSON(w, r, true, http.StatusAccepted, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.StartQQQRSession(ctx, tx, org, a, r.PathValue("botID"), in)
		})
	})
	m.HandleFunc("GET "+base+"/qr/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		h.qqJSON(w, r, false, http.StatusOK, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetQQQRSession(ctx, tx, org, a, r.PathValue("sessionID"))
		})
	})
	m.HandleFunc("POST "+base+"/qr/{sessionID}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.QQQRActionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.CancelQQQRSession(ctx, tx, org, a, r.PathValue("sessionID"), in)
		})
	})
	m.HandleFunc("POST "+base+"/qr/{sessionID}/recover", h.recoverQQCandidate)
	m.HandleFunc("POST "+base+"/binding-sessions/{sessionID}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var in qqVersionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.CancelQQBindingSession(ctx, tx, org, a, r.PathValue("sessionID"), in.Version)
		})
	})

	m.HandleFunc("POST "+base+"/bots", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.QQCreateBotInput
		h.qqJSON(w, r, true, http.StatusCreated, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.CreateQQBot(ctx, tx, org, a, in)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}", func(w http.ResponseWriter, r *http.Request) {
		h.qqJSON(w, r, false, http.StatusOK, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetQQBotDetail(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/credentials", h.connectQQBot)
	m.HandleFunc("POST "+base+"/bots/{botID}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var in qqVersionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.DisconnectQQBot(ctx, tx, org, a, r.PathValue("botID"), in.Version)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/webhook", func(w http.ResponseWriter, r *http.Request) {
		var in qqVersionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			url, err := h.integrations.RevealQQWebhook(ctx, tx, org, a, r.PathValue("botID"), in.Version)
			return map[string]string{"url": url}, err
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/binding-sessions", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.QQStartBindingInput
		h.qqJSON(w, r, true, http.StatusCreated, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.StartQQBinding(ctx, tx, org, a, r.PathValue("botID"), in)
		})
	})
	m.HandleFunc("GET "+base+"/binding-sessions/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		h.qqJSON(w, r, false, http.StatusOK, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetQQBindingSession(ctx, tx, org, a, r.PathValue("sessionID"))
		})
	})
	m.HandleFunc("POST "+base+"/binding-sessions/{sessionID}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.QQConfirmBindingInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.ConfirmQQBinding(ctx, tx, org, a, r.PathValue("sessionID"), in)
		})
	})
	m.HandleFunc("POST "+base+"/bindings/{bindingID}/revoke", func(w http.ResponseWriter, r *http.Request) {
		var in qqVersionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.RevokeQQBinding(ctx, tx, org, a, r.PathValue("bindingID"), in.Version)
		})
	})
	m.HandleFunc("POST "+base+"/groups/{groupID}/disable", func(w http.ResponseWriter, r *http.Request) {
		var in qqVersionInput
		h.qqJSON(w, r, true, http.StatusOK, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.DisableQQGroup(ctx, tx, org, a, r.PathValue("groupID"), in.Version)
		})
	})
}

func qqResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (h *Handler) qqJSON(w http.ResponseWriter, r *http.Request, mutation bool, status int, in any, action qqAPIAction) {
	qqResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return
	}
	if in != nil && decodeJSON(w, r, in) != nil {
		writeQQError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = action(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeQQError(w, r, err)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, status, out)
}

func (h *Handler) connectQQBot(w http.ResponseWriter, r *http.Request) {
	qqResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.QQConnectInput
	if decodeJSON(w, r, &in) != nil {
		writeQQError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.QQConnectWork
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = h.integrations.PrepareQQConnection(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), in)
		return err
	})
	if err != nil {
		writeQQError(w, r, err)
		return
	}
	// The provider request runs outside a transaction. Finish persists either
	// verified credentials or the failed verification fact under the same fence.
	proof := integrations.ExecuteQQConnection(r.Context(), h.integrations.QQ, plan)
	var out integrations.QQBot
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.FinishQQConnection(ctx, tx, call.organizationID, call.selected.Actor, plan, proof)
		return err
	})
	if err != nil {
		writeQQError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) recoverQQCandidate(w http.ResponseWriter, r *http.Request) {
	qqResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in qqVersionInput
	if decodeJSON(w, r, &in) != nil {
		writeQQError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.QQConnectWork
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		plan, err = h.integrations.PrepareQQQRCandidate(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("sessionID"), in.Version)
		return err
	})
	if err != nil {
		writeQQError(w, r, err)
		return
	}
	// The provider request runs outside a transaction. Finish persists either
	// verified credentials or the failed verification fact under the same fence.
	proof := integrations.ExecuteQQConnection(r.Context(), h.integrations.QQ, plan)
	var out integrations.QQBot
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		out, err = h.integrations.FinishQQConnection(ctx, tx, call.organizationID, call.selected.Actor, plan, proof)
		return err
	})
	if err != nil {
		writeQQError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeQQError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 500, "INTERNAL_ERROR", "request could not be completed"
	switch {
	case errors.Is(err, integrations.ErrInvalidRequest), errors.Is(err, qq.ErrInvalid):
		status, code, message = 400, "INVALID_REQUEST", "request is invalid"
	case errors.Is(err, integrations.ErrNotFound), errors.Is(err, conversations.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "resource not found"
	case errors.Is(err, integrations.ErrForbidden), errors.Is(err, conversations.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "permission denied"
	case errors.Is(err, integrations.ErrVersionConflict):
		status, code, message = 409, "VERSION_CONFLICT", "configuration changed; refresh before retrying"
	case errors.Is(err, integrations.ErrNeedsReauth):
		status, code, message = 409, "QQ_NEEDS_REAUTH", "bot credentials or callback require verification"
	case errors.Is(err, integrations.ErrSecretsUnavailable), errors.Is(err, integrations.ErrNotConfigured):
		status, code, message = 503, "QQ_CONFIGURATION_UNAVAILABLE", "configuration is unavailable"
	case errors.Is(err, qq.ErrSignature):
		status, code, message = 401, "QQ_WEBHOOK_UNAUTHORIZED", "webhook authentication failed"
	case errors.Is(err, qq.ErrAuthentication):
		status, code, message = 422, "QQ_VERIFICATION_FAILED", "bot credentials could not be verified"
	case errors.Is(err, qq.ErrUnsupported):
		status, code, message = 400, "QQ_UNSUPPORTED", "event is not supported"
	case errors.Is(err, qq.ErrRateLimited):
		status, code, message = 429, "QQ_RATE_LIMITED", "retry later"
		var upstream *qq.Error
		if errors.As(err, &upstream) && upstream.RetryAfter > 0 {
			seconds := upstream.RetryAfter / time.Second
			if upstream.RetryAfter%time.Second != 0 {
				seconds++
			}
			w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
		}
	case errors.Is(err, qq.ErrRejected):
		status, code, message = 422, "QQ_REQUEST_REJECTED", "QQ rejected the request"
	case errors.Is(err, qq.ErrUnknown), errors.Is(err, qq.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		status, code, message = 502, "QQ_UNAVAILABLE", "QQ could not complete verification"
	}
	writeError(w, r, status, code, message)
}

func (h *Handler) receiveQQWebhook(w http.ResponseWriter, r *http.Request) {
	qqResponseHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = r.Body.Close(); _ = controller.SetReadDeadline(time.Time{}) }()
	route := r.PathValue("routeToken")
	decoded, err := base64.RawURLEncoding.DecodeString(route)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != route {
		writeQQError(w, r, integrations.ErrNotFound)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeQQError(w, r, integrations.ErrInvalidRequest)
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "UNAVAILABLE", "webhook is unavailable")
		return
	}
	var org, bot string
	hash := integrations.TokenHash(route)
	err = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,bot_id::text FROM openfox_private.qq_webhook_route($1::bytea)`, hash).Scan(&org, &bot)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeQQError(w, r, integrations.ErrNotFound)
		return
	}
	if err != nil {
		writeError(w, r, 503, "UNAVAILABLE", "webhook is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, qq.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "event is too large")
		} else {
			writeQQError(w, r, integrations.ErrInvalidRequest)
		}
		return
	}
	var out json.RawMessage
	err = h.runAsWorker(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='2500ms'`); err != nil {
			return err
		}
		var err error
		out, err = h.integrations.ReceiveQQWebhook(ctx, tx, org, bot, hash, r.Header, raw)
		return err
	})
	if err != nil {
		// Only the service's authenticated event marker permits a retry ACK.
		// A failed commit is still unaccepted, even if its callback returned OK.
		if string(out) == `{"op":12,"d":0}` && !errors.Is(err, integrations.ErrInvalidRequest) &&
			!errors.Is(err, integrations.ErrVersionConflict) && !errors.Is(err, integrations.ErrForbidden) &&
			!errors.Is(err, integrations.ErrNotFound) && !errors.Is(err, integrations.ErrNeedsReauth) {
			writeJSON(w, http.StatusOK, json.RawMessage(`{"op":12,"d":1}`))
			return
		}
		writeQQError(w, r, err)
		return
	}
	// A challenge or success ACK is returned only after its transaction commits.
	writeJSON(w, http.StatusOK, out)
}
