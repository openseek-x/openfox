package api

import (
	"errors"
	"net/http"

	"openfox/internal/identity"
)

type preferencesResponse struct {
	Theme                     string            `json:"theme"`
	Density                   string            `json:"density"`
	Locale                    *string           `json:"locale"`
	OutlineColor              int               `json:"outline_color"`
	OpenSessionOnAssign       bool              `json:"open_session_on_assign"`
	OpenSessionOnQuickCommand bool              `json:"open_session_on_quick_command"`
	SendShortcut              string            `json:"send_shortcut"`
	CommandShortcuts          map[string]string `json:"command_shortcuts"`
	Version                   int64             `json:"version"`
}

// updatePreferencesRequest 的指针字段为 nil 表示未出现；CommandShortcuts 为 nil 时也表示未出现（整体替换的语义在服务层）。
type updatePreferencesRequest struct {
	Version                   *int64            `json:"version"`
	Theme                     *string           `json:"theme"`
	Density                   *string           `json:"density"`
	Locale                    *string           `json:"locale"`
	OutlineColor              *int              `json:"outline_color"`
	OpenSessionOnAssign       *bool             `json:"open_session_on_assign"`
	OpenSessionOnQuickCommand *bool             `json:"open_session_on_quick_command"`
	SendShortcut              *string           `json:"send_shortcut"`
	CommandShortcuts          map[string]string `json:"command_shortcuts"`
}

func (h *Handler) registerPreferenceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/preferences", h.getMyPreferences)
	mux.HandleFunc("PATCH /v1/me/preferences", h.updateMyPreferences)
}

func (h *Handler) getMyPreferences(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, false)
	if !ok {
		return
	}
	prefs, err := h.auth.Preferences(r.Context(), auth.principal.UserID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, toPreferencesResponse(prefs))
}

func (h *Handler) updateMyPreferences(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var input updatePreferencesRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Version == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	prefs, err := h.auth.UpdatePreferences(r.Context(), auth.principal.UserID, identity.PreferencesPatch{
		Version:                   input.Version,
		Theme:                     input.Theme,
		Density:                   input.Density,
		Locale:                    input.Locale,
		OutlineColor:              input.OutlineColor,
		OpenSessionOnAssign:       input.OpenSessionOnAssign,
		OpenSessionOnQuickCommand: input.OpenSessionOnQuickCommand,
		SendShortcut:              input.SendShortcut,
		CommandShortcuts:          input.CommandShortcuts,
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toPreferencesResponse(prefs))
	case errors.Is(err, identity.ErrPreferencesVersionConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "preferences were changed elsewhere, reload and retry")
	case errors.Is(err, identity.ErrShortcutConflict):
		writeError(w, r, http.StatusBadRequest, "SHORTCUT_CONFLICT", "command shortcuts conflict with each other")
	case errors.Is(err, identity.ErrInvalidPreferences):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "preferences are invalid")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

func toPreferencesResponse(prefs identity.Preferences) preferencesResponse {
	return preferencesResponse{
		Theme:                     prefs.Theme,
		Density:                   prefs.Density,
		Locale:                    prefs.Locale,
		OutlineColor:              prefs.OutlineColor,
		OpenSessionOnAssign:       prefs.OpenSessionOnAssign,
		OpenSessionOnQuickCommand: prefs.OpenSessionOnQuickCommand,
		SendShortcut:              prefs.SendShortcut,
		CommandShortcuts:          prefs.CommandShortcuts,
		Version:                   prefs.Version,
	}
}
