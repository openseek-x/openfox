package api

// 阶段 4c · 通知渠道设置（桌面端、浏览器）。

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"

	"openfox/internal/identity"
)

type notificationChannelResponse struct {
	DirectMessages *bool           `json:"direct_messages,omitempty"`
	EventOverrides map[string]bool `json:"event_overrides"`
	MutedGroups    []string        `json:"muted_groups"`
	Revision       int64           `json:"revision"`
	Channel        string          `json:"channel"`
	Enabled        bool            `json:"enabled"`
	Available      bool            `json:"available"`
	DisabledEvents []string        `json:"disabled_events"`
	Customized     bool            `json:"customized"`
}

// updateNotificationChannelRequest 的指针字段为 nil 表示缺省，两个字段都是必填的（整体替换）。
type updateNotificationChannelRequest struct {
	DirectMessages *bool     `json:"direct_messages"`
	Enabled        *bool     `json:"enabled"`
	DisabledEvents *[]string `json:"disabled_events"`
}

func (h *Handler) registerNotificationChannelRoutes(mux *http.ServeMux) {
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		mux.HandleFunc(method+" /v1/me/inbox-preferences", h.myInboxPreferences)
	}
	mux.HandleFunc("GET /v1/me/notification-channels", h.listMyNotificationChannels)
	mux.HandleFunc("PATCH /v1/me/notification-channels/{channel}", h.patchMyNotificationChannel)
	mux.HandleFunc("PUT /v1/me/notification-channels/{channel}", h.putMyNotificationChannel)
	mux.HandleFunc("DELETE /v1/me/notification-channels/{channel}", h.resetMyNotificationChannel)
}

func (h *Handler) listMyNotificationChannels(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, false)
	if !ok {
		return
	}
	items, err := h.auth.ListNotificationChannels(r.Context(), auth.principal.UserID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	slackAvailable, err := h.slackNotificationAvailable(r, auth.principal.UserID)
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	channels := make([]notificationChannelResponse, 0, len(items))
	for _, item := range items {
		if item.Channel == "slack" {
			item.Available = slackAvailable
		}
		channels = append(channels, toNotificationChannelResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

func (h *Handler) putMyNotificationChannel(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var input updateNotificationChannelRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Enabled == nil || input.DisabledEvents == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	slackAvailable := false
	if r.PathValue("channel") == "slack" {
		var err error
		slackAvailable, err = h.slackNotificationAvailable(r, auth.principal.UserID)
		if err != nil {
			writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		if *input.Enabled && !slackAvailable {
			writeError(w, r, 409, "SLACK_UNAVAILABLE", "bind an active Slack workspace identity first")
			return
		}
	}
	item, err := h.auth.SaveNotificationChannel(r.Context(), auth.principal.UserID, r.PathValue("channel"), *input.Enabled, *input.DisabledEvents, input.DirectMessages)
	if errors.Is(err, identity.ErrInvalidNotificationSettings) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "channel or notification events are invalid")
		return
	}
	if errors.Is(err, identity.ErrEmailNotificationsUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "EMAIL_UNAVAILABLE", "email notifications are not configured")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	if item.Channel == "slack" {
		item.Available = slackAvailable
	}
	writeJSON(w, http.StatusOK, toNotificationChannelResponse(item))
}

func (h *Handler) resetMyNotificationChannel(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var revision *int64
	if raw := r.URL.Query().Get("revision"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeError(w, r, 400, "INVALID_REQUEST", "invalid revision")
			return
		}
		revision = &v
	}
	item, err := h.auth.ResetNotificationChannel(r.Context(), auth.principal.UserID, r.PathValue("channel"), revision)
	if errors.Is(err, identity.ErrNotificationRevisionConflict) {
		writeError(w, r, 409, "VERSION_CONFLICT", "notification settings changed")
		return
	}
	if errors.Is(err, identity.ErrInvalidNotificationSettings) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "channel is invalid")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	if item.Channel == "slack" {
		item.Available, err = h.slackNotificationAvailable(r, auth.principal.UserID)
		if err != nil {
			writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
			return
		}
	}
	writeJSON(w, http.StatusOK, toNotificationChannelResponse(item))
}

func toNotificationChannelResponse(item identity.NotificationChannel) notificationChannelResponse {
	return notificationChannelResponse{DirectMessages: item.DirectMessages,
		EventOverrides: item.EventOverrides, MutedGroups: item.MutedGroups, Revision: item.Revision,
		Channel: item.Channel, Enabled: item.Enabled, Available: item.Available, DisabledEvents: item.DisabledEvents, Customized: item.Customized,
	}
}

func (h *Handler) patchMyNotificationChannel(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var input identity.NotificationChannelPatch
	if decodeJSON(w, r, &input) != nil {
		writeError(w, r, 400, "INVALID_REQUEST", "invalid notification settings")
		return
	}
	slackAvailable := false
	if r.PathValue("channel") == "slack" {
		var err error
		slackAvailable, err = h.slackNotificationAvailable(r, auth.principal.UserID)
		if err != nil {
			writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		if input.Enabled != nil && *input.Enabled && !slackAvailable {
			writeError(w, r, 409, "SLACK_UNAVAILABLE", "bind an active Slack workspace identity first")
			return
		}
	}
	item, err := h.auth.PatchNotificationChannel(r.Context(), auth.principal.UserID, r.PathValue("channel"), input)
	switch {
	case errors.Is(err, identity.ErrInvalidNotificationSettings):
		writeError(w, r, 400, "INVALID_REQUEST", "invalid notification settings")
	case errors.Is(err, identity.ErrNotificationRevisionConflict):
		writeError(w, r, 409, "VERSION_CONFLICT", "notification settings changed")
	case errors.Is(err, identity.ErrEmailNotificationsUnavailable):
		writeError(w, r, 503, "EMAIL_UNAVAILABLE", "email notifications are not configured")
	case err != nil:
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
	default:
		if item.Channel == "slack" {
			item.Available = slackAvailable
		}
		writeJSON(w, 200, toNotificationChannelResponse(item))
	}
}

func (h *Handler) slackNotificationAvailable(r *http.Request, user string) (bool, error) {
	if h.pool == nil || !h.integrations.SlackConfigured() {
		return false, nil
	}
	var available bool
	err := h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT openfox_private.slack_notifications_available($1::uuid)`, user).Scan(&available)
	})
	return available, err
}
