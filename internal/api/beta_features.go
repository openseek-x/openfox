package api

// 内测功能开关。只有 access_tier 为 beta / internal 的账号可用。

import (
	"errors"
	"net/http"

	"openfox/internal/identity"
)

type betaFeatureResponse struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

type updateBetaFeatureRequest struct {
	Enabled *bool `json:"enabled"`
}

func (h *Handler) registerBetaFeatureRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/beta-features", h.listMyBetaFeatures)
	mux.HandleFunc("PUT /v1/me/beta-features/{key}", h.putMyBetaFeature)
}

func (h *Handler) listMyBetaFeatures(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, false)
	if !ok {
		return
	}
	items, err := h.auth.BetaFeatures(r.Context(), auth.principal.UserID)
	if err != nil {
		writeBetaFeatureError(w, r, err)
		return
	}
	features := make([]betaFeatureResponse, 0, len(items))
	for _, item := range items {
		features = append(features, betaFeatureResponse{Key: item.Key, Enabled: item.Enabled})
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": features})
}

func (h *Handler) putMyBetaFeature(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var input updateBetaFeatureRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.auth.SetBetaFeature(r.Context(), auth.principal.UserID, r.PathValue("key"), *input.Enabled)
	if err != nil {
		writeBetaFeatureError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, betaFeatureResponse{Key: item.Key, Enabled: item.Enabled})
}

func writeBetaFeatureError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrBetaOnly):
		writeError(w, r, http.StatusForbidden, "BETA_ONLY", "beta features are not available for this account")
	case errors.Is(err, identity.ErrBetaFeatureNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "beta feature is not available")
	case errors.Is(err, identity.ErrUserNotFound):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}
