package api

// 通知渠道设置（桌面端、浏览器）。

import (
	"errors"
	"net/http"

	"openfox/internal/identity"
)

type notificationChannelResponse struct {
	DirectMessages *bool    `json:"direct_messages,omitempty"`
	Channel        string   `json:"channel"`
	Enabled        bool     `json:"enabled"`
	Available      bool     `json:"available"`
	DisabledEvents []string `json:"disabled_events"`
	Customized     bool     `json:"customized"`
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
	channels := make([]notificationChannelResponse, 0, len(items))
	for _, item := range items {
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
	writeJSON(w, http.StatusOK, toNotificationChannelResponse(item))
}

func (h *Handler) resetMyNotificationChannel(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	item, err := h.auth.ResetNotificationChannel(r.Context(), auth.principal.UserID, r.PathValue("channel"))
	if errors.Is(err, identity.ErrInvalidNotificationSettings) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "channel is invalid")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, toNotificationChannelResponse(item))
}

func toNotificationChannelResponse(item identity.NotificationChannel) notificationChannelResponse {
	return notificationChannelResponse{DirectMessages: item.DirectMessages,
		Channel: item.Channel, Enabled: item.Enabled, Available: item.Available, DisabledEvents: item.DisabledEvents, Customized: item.Customized,
	}
}
