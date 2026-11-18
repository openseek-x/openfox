package api

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/telegram"
)

type telegramVersionInput struct {
	Version uint64 `json:"version"`
}
type telegramAPIAction func(context.Context, pgx.Tx, string, collaboration.Actor) (any, error)

// Called once from Handler.Routes by the integration owner. Public webhook does
// not share authenticated organization/CSRF handling.
func (h *Handler) registerTelegramRoutes(m *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/telegram"
	m.HandleFunc("POST /v1/hooks/telegram/{routeToken}", h.receiveTelegramWebhook)
	m.HandleFunc("POST "+base+"/preparations", h.prepareTelegramConnection)
	m.HandleFunc("POST "+base+"/bots/{botID}/groups", h.saveTelegramGroup)
	m.HandleFunc("GET "+base+"/bots", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TelegramBots(ctx, tx, org, a)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetTelegramBot(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("GET "+base+"/preparations/{preparationID}", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetTelegramPreparation(ctx, tx, org, a, r.PathValue("preparationID"))
		})
	})
	m.HandleFunc("POST "+base+"/preparations/{preparationID}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.TelegramConfirmInput
		h.telegramJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.ConfirmTelegramPreparation(ctx, tx, org, a, r.PathValue("preparationID"), in)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.TelegramDisconnectInput
		h.telegramJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.DisconnectTelegramBot(ctx, tx, org, a, r.PathValue("botID"), in)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var in telegramVersionInput
		h.telegramJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.ReconcileTelegramWebhook(ctx, tx, org, a, r.PathValue("botID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/personal-targets", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TelegramPersonalTargets(ctx, tx, org, a)
		})
	})
	m.HandleFunc("GET "+base+"/personal", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TelegramPersonal(ctx, tx, org, a)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/binding-sessions", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.IssueTelegramBinding(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("GET "+base+"/binding-sessions/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetTelegramBindingSession(ctx, tx, org, a, r.PathValue("sessionID"))
		})
	})
	m.HandleFunc("POST "+base+"/binding-sessions/{sessionID}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var in telegramVersionInput
		h.telegramJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.ConfirmTelegramBinding(ctx, tx, org, a, r.PathValue("sessionID"), in.Version)
		})
	})
	m.HandleFunc("DELETE "+base+"/personal/{identityID}", func(w http.ResponseWriter, r *http.Request) {
		var in telegramVersionInput
		h.telegramJSON(w, r, true, &in, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.UnbindTelegram(ctx, tx, org, a, r.PathValue("identityID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/group-discoveries", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TelegramGroupDiscoveries(ctx, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/groups", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TelegramGroups(ctx, tx, org, a, r.PathValue("botID"), r.URL.Query().Get("after"))
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		h.telegramJSON(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.TelegramDeliveryDiagnostics(ctx, tx, org, a, r.PathValue("botID"), r.URL.Query().Get("after"))
		})
	})
}
func telegramResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func (h *Handler) telegramJSON(w http.ResponseWriter, r *http.Request, mutation bool, in any, action telegramAPIAction) {
	telegramResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return
	}
	if in != nil && decodeJSON(w, r, in) != nil {
		writeTelegramError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out any
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = action(ctx, tx, call.organizationID, call.selected.Actor)
		return e
	})
	if e != nil {
		writeTelegramError(w, r, e)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) prepareTelegramConnection(w http.ResponseWriter, r *http.Request) {
	telegramResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.TelegramConnectInput
	if decodeJSON(w, r, &in) != nil {
		writeTelegramError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.TelegramConnectTarget
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		plan, e = h.integrations.PrepareTelegramConnection(ctx, tx, call.organizationID, call.selected.Actor, in)
		return e
	})
	var proof integrations.TelegramConnectProof
	if e == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		proof, e = h.integrations.VerifyTelegramConnection(ctx, plan)
		cancel()
	}
	var out integrations.TelegramPreparation
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveTelegramPreparation(ctx, tx, call.organizationID, call.selected.Actor, plan, proof)
			return e
		})
	}
	if e != nil {
		writeTelegramError(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) saveTelegramGroup(w http.ResponseWriter, r *http.Request) {
	telegramResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.TelegramGroupInput
	if decodeJSON(w, r, &in) != nil {
		writeTelegramError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.TelegramGroupTarget
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		plan, e = h.integrations.PrepareTelegramGroup(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), in)
		return e
	})
	if e == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		plan, e = h.integrations.VerifyTelegramGroup(ctx, plan)
		cancel()
	}
	var out integrations.TelegramGroup
	if e == nil {
		e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveTelegramGroup(ctx, tx, call.organizationID, call.selected.Actor, plan)
			return e
		})
	}
	if e != nil {
		writeTelegramError(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func writeTelegramError(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 500, "INTERNAL_ERROR", "request could not be completed"
	switch {
	case errors.Is(e, integrations.ErrInvalidRequest), errors.Is(e, telegram.ErrInvalid):
		status, code, message = 400, "INVALID_REQUEST", "request is invalid"
	case errors.Is(e, integrations.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "resource not found"
	case errors.Is(e, integrations.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "permission denied"
	case errors.Is(e, integrations.ErrVersionConflict):
		status, code, message = 409, "VERSION_CONFLICT", "configuration changed; refresh before retrying"
	case errors.Is(e, integrations.ErrTelegramBotOccupied), errors.Is(e, integrations.ErrFeishuAgentConnected), errors.Is(e, integrations.ErrFeishuAssistantExists):
		status, code, message = 409, "TELEGRAM_BOT_OCCUPIED", "bot or target is reserved"
	case errors.Is(e, integrations.ErrTelegramWebhookConflict):
		status, code, message = 409, "TELEGRAM_WEBHOOK_CONFLICT", "another webhook is configured"
	case errors.Is(e, integrations.ErrTelegramSessionState):
		status, code, message = 409, "TELEGRAM_SESSION_STATE", "session is expired or no longer in the required state"
	case errors.Is(e, integrations.ErrTelegramUnsupported), errors.Is(e, telegram.ErrUnsupported):
		status, code, message = 400, "TELEGRAM_UNSUPPORTED", "configuration is not supported"
	case errors.Is(e, integrations.ErrPersonalIdentity):
		status, code, message = 409, "TELEGRAM_IDENTITY_MISMATCH", "identity does not match"
	case errors.Is(e, integrations.ErrSecretsUnavailable), errors.Is(e, integrations.ErrNotConfigured):
		status, code, message = 503, "TELEGRAM_CONFIGURATION_UNAVAILABLE", "configuration is unavailable"
	case errors.Is(e, telegram.ErrSignature):
		status, code, message = 401, "TELEGRAM_WEBHOOK_UNAUTHORIZED", "webhook authentication failed"
	case errors.Is(e, telegram.ErrAuth):
		status, code, message = 422, "TELEGRAM_VERIFICATION_FAILED", "bot credentials could not be verified"
	case errors.Is(e, telegram.ErrForbidden):
		status, code, message = 403, "TELEGRAM_TARGET_FORBIDDEN", "bot cannot access the target"
	case errors.Is(e, telegram.ErrRejected):
		status, code, message = 422, "TELEGRAM_REQUEST_REJECTED", "Telegram rejected the request"
	case errors.Is(e, integrations.ErrTelegramQueueFull), errors.Is(e, telegram.ErrRateLimited):
		status, code, message = 429, "TELEGRAM_RATE_LIMITED", "retry later"
		delay := time.Minute
		var upstream *telegram.Error
		if errors.As(e, &upstream) && upstream.RetryAfter > 0 {
			delay = upstream.RetryAfter
		}
		if delay > 24*time.Hour {
			delay = 24 * time.Hour
		}
		w.Header().Set("Retry-After", strconv.FormatInt(int64((delay+time.Second-1)/time.Second), 10))
	case errors.Is(e, telegram.ErrUnknown), errors.Is(e, telegram.ErrIncomplete), errors.Is(e, telegram.ErrUnavailable), errors.Is(e, context.DeadlineExceeded):
		status, code, message = 502, "TELEGRAM_UNAVAILABLE", "Telegram could not complete verification"
	}
	writeError(w, r, status, code, message)
}
func (h *Handler) receiveTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	telegramResponseHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = r.Body.Close(); _ = controller.SetReadDeadline(time.Time{}) }()
	route := r.PathValue("routeToken")
	decoded, e := base64.RawURLEncoding.DecodeString(route)
	if e != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != route {
		writeTelegramError(w, r, integrations.ErrNotFound)
		return
	}
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		writeTelegramError(w, r, integrations.ErrInvalidRequest)
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "UNAVAILABLE", "webhook is unavailable")
		return
	}
	var lookup integrations.TelegramWebhookLookup
	e = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,bot_id::text FROM openfox_private.resolve_telegram_webhook($1::bytea)`, integrations.TokenHash(route)).Scan(&lookup.OrganizationID, &lookup.BotID)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		writeTelegramError(w, r, integrations.ErrNotFound)
		return
	}
	if e != nil {
		writeError(w, r, 503, "UNAVAILABLE", "webhook is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, telegram.MaxBodyBytes)
	raw, e := io.ReadAll(r.Body)
	if e != nil {
		var large *http.MaxBytesError
		if errors.As(e, &large) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "update is too large")
		} else {
			writeTelegramError(w, r, integrations.ErrInvalidRequest)
		}
		return
	}
	var out integrations.TelegramReceiveResult
	e = h.runAsWorker(ctx, lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='2500ms'`); e != nil {
			return e
		}
		var e error
		out, e = h.integrations.ReceiveTelegram(ctx, tx, lookup, route, r.Header, raw)
		return e
	})
	if errors.Is(e, integrations.ErrTelegramQueueFull) {
		w.Header().Set("Retry-After", "60")
		writeError(w, r, 503, "TELEGRAM_QUEUE_FULL", "retry later")
		return
	}
	if e != nil {
		writeTelegramError(w, r, e)
		return
	}
	// Durable receipt is committed. Runtime work and replies are asynchronous.
	writeJSON(w, http.StatusOK, out)
}
