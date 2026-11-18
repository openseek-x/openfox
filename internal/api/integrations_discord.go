package api

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/discord"
	"strconv"
	"time"
)

type discordVersionInput struct {
	Version uint64 `json:"version"`
}
type discordAPIAction func(context.Context, pgx.Tx, string, collaboration.Actor) (any, error)

func (h *Handler) registerDiscordRoutes(m *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/discord"
	m.HandleFunc("POST "+base+"/preparations", h.prepareDiscordConnection)
	m.HandleFunc("POST "+base+"/bots/{botID}/channels", h.enableDiscordChannel)
	m.HandleFunc("GET "+base+"/bots/{botID}/guilds", h.browseDiscord)
	m.HandleFunc("GET "+base+"/bots/{botID}/guilds/{guildID}/channels", h.browseDiscord)
	m.HandleFunc("GET "+base+"/bots", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.DiscordBots(c, tx, org, a)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetDiscordBot(c, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("GET "+base+"/preparations/{preparationID}", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetDiscordPreparation(c, tx, org, a, r.PathValue("preparationID"))
		})
	})
	m.HandleFunc("POST "+base+"/preparations/{preparationID}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var in integrations.DiscordConfirmInput
		h.discordJSON(w, r, true, &in, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.ConfirmDiscordPreparation(c, tx, org, a, r.PathValue("preparationID"), in)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var in discordVersionInput
		h.discordJSON(w, r, true, &in, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.DisconnectDiscordBot(c, tx, org, a, r.PathValue("botID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/personal-targets", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.DiscordPersonalTargets(c, tx, org, a)
		})
	})
	m.HandleFunc("GET "+base+"/personal", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.DiscordPersonal(c, tx, org, a)
		})
	})
	m.HandleFunc("POST "+base+"/bots/{botID}/binding-sessions", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, true, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.IssueDiscordBinding(c, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("GET "+base+"/binding-sessions/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.GetDiscordBindingSession(c, tx, org, a, r.PathValue("sessionID"))
		})
	})
	m.HandleFunc("POST "+base+"/binding-sessions/{sessionID}/confirm", func(w http.ResponseWriter, r *http.Request) {
		var in discordVersionInput
		h.discordJSON(w, r, true, &in, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.ConfirmDiscordBinding(c, tx, org, a, r.PathValue("sessionID"), in.Version)
		})
	})
	m.HandleFunc("DELETE "+base+"/personal/{identityID}", func(w http.ResponseWriter, r *http.Request) {
		var in discordVersionInput
		h.discordJSON(w, r, true, &in, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.UnbindDiscord(c, tx, org, a, r.PathValue("identityID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/channels", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.DiscordChannels(c, tx, org, a, r.PathValue("botID"))
		})
	})
	m.HandleFunc("DELETE "+base+"/bots/{botID}/channels/{channelID}", func(w http.ResponseWriter, r *http.Request) {
		var in discordVersionInput
		h.discordJSON(w, r, true, &in, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return nil, h.integrations.DisableDiscordChannel(c, tx, org, a, r.PathValue("botID"), r.PathValue("channelID"), in.Version)
		})
	})
	m.HandleFunc("GET "+base+"/bots/{botID}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		h.discordJSON(w, r, false, nil, func(c context.Context, tx pgx.Tx, org string, a collaboration.Actor) (any, error) {
			return h.integrations.DiscordDeliveries(c, tx, org, a, r.PathValue("botID"), r.URL.Query().Get("after"))
		})
	})
}
func discordResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func (h *Handler) discordJSON(w http.ResponseWriter, r *http.Request, mutation bool, in any, action discordAPIAction) {
	discordResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return
	}
	if in != nil && decodeJSON(w, r, in) != nil {
		writeDiscordError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out any
	e := call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = action(c, tx, call.organizationID, call.selected.Actor)
		return e
	})
	if e != nil {
		writeDiscordError(w, r, e)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) prepareDiscordConnection(w http.ResponseWriter, r *http.Request) {
	discordResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.DiscordConnectInput
	if decodeJSON(w, r, &in) != nil {
		writeDiscordError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var target integrations.DiscordConnectTarget
	e := call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.PrepareDiscordConnection(c, tx, call.organizationID, call.selected.Actor, in)
		return e
	})
	var proof integrations.DiscordConnectProof
	if e == nil {
		proof, e = h.integrations.VerifyDiscordConnection(r.Context(), target)
	}
	var out integrations.DiscordPreparation
	if e == nil {
		e = call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveDiscordPreparation(c, tx, call.organizationID, call.selected.Actor, target, proof)
			return e
		})
	}
	if e != nil {
		writeDiscordError(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) enableDiscordChannel(w http.ResponseWriter, r *http.Request) {
	discordResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.DiscordChannelInput
	if decodeJSON(w, r, &in) != nil {
		writeDiscordError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var target integrations.DiscordChannelTarget
	e := call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.PrepareDiscordChannel(c, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"), in)
		return e
	})
	var proof integrations.DiscordChannelProof
	if e == nil {
		proof, e = h.integrations.VerifyDiscordChannel(r.Context(), target)
	}
	var out integrations.DiscordChannel
	if e == nil {
		e = call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.EnableDiscordChannel(c, tx, call.organizationID, call.selected.Actor, target, proof)
			return e
		})
	}
	if e != nil {
		writeDiscordError(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) browseDiscord(w http.ResponseWriter, r *http.Request) {
	discordResponseHeaders(w)
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var target integrations.DiscordChannelTarget
	e := call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.PrepareDiscordBrowse(c, tx, call.organizationID, call.selected.Actor, r.PathValue("botID"))
		return e
	})
	var out any
	if e == nil {
		if guild := r.PathValue("guildID"); guild != "" {
			out, e = h.integrations.DiscordRemoteChannels(r.Context(), target, guild)
		} else {
			out, e = h.integrations.DiscordGuilds(r.Context(), target, r.URL.Query().Get("after"))
		}
	}
	if e == nil {
		e = call.run(h, r, func(c context.Context, tx pgx.Tx, _ collaboration.Service) error {
			return h.integrations.CheckDiscordBrowse(c, tx, call.organizationID, call.selected.Actor, target)
		})
	}
	if e != nil {
		writeDiscordError(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func writeDiscordError(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 500, "INTERNAL_ERROR", "request could not be completed"
	switch {
	case errors.Is(e, integrations.ErrInvalidRequest), errors.Is(e, discord.ErrInvalid):
		status, code, message = 400, "INVALID_REQUEST", "request is invalid"
	case errors.Is(e, integrations.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "resource not found"
	case errors.Is(e, integrations.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "permission denied"
	case errors.Is(e, integrations.ErrVersionConflict):
		status, code, message = 409, "VERSION_CONFLICT", "configuration changed; refresh before retrying"
	case errors.Is(e, integrations.ErrDiscordBotOccupied), errors.Is(e, agents.ErrTaskAssistantExists):
		status, code, message = 409, "DISCORD_BOT_OCCUPIED", "bot or target is reserved"
	case errors.Is(e, integrations.ErrDiscordSessionState):
		status, code, message = 409, "DISCORD_SESSION_STATE", "session expired or changed"
	case errors.Is(e, integrations.ErrDiscordUnsupported), errors.Is(e, discord.ErrUnsupported):
		status, code, message = 400, "DISCORD_UNSUPPORTED", "channel or capability is not supported"
	case errors.Is(e, integrations.ErrPersonalIdentity), errors.Is(e, integrations.ErrPersonalInUse):
		status, code, message = 409, "DISCORD_IDENTITY_MISMATCH", "identity unavailable or does not match"
	case errors.Is(e, integrations.ErrSecretsUnavailable):
		status, code, message = 503, "DISCORD_CONFIGURATION_UNAVAILABLE", "configuration is unavailable"
	case errors.Is(e, discord.ErrAuth):
		status, code, message = 422, "DISCORD_VERIFICATION_FAILED", "bot credentials could not be verified"
	case errors.Is(e, discord.ErrForbidden):
		status, code, message = 403, "DISCORD_TARGET_FORBIDDEN", "Discord permissions are insufficient"
	case errors.Is(e, discord.ErrNotFound):
		status, code, message = 404, "DISCORD_TARGET_MISSING", "Discord target is unavailable"
	case errors.Is(e, discord.ErrRateLimited):
		status, code, message = 429, "DISCORD_RATE_LIMITED", "retry later"
		delay := time.Minute
		var de *discord.Error
		if errors.As(e, &de) && de.RetryAfter > 0 {
			delay = de.RetryAfter
		}
		seconds := delay / time.Second
		if delay%time.Second > 0 {
			seconds++
		}
		w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	case errors.Is(e, discord.ErrUnknown), errors.Is(e, discord.ErrResponse), errors.Is(e, discord.ErrUnavailable), errors.Is(e, context.DeadlineExceeded):
		status, code, message = 502, "DISCORD_UNAVAILABLE", "Discord verification is unavailable"
	}
	writeError(w, r, status, code, message)
}
