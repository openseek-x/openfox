package api

// 个人访问令牌。令牌与本人权限相同，但令牌不能管理令牌、不能修改邮箱。

import (
	"errors"
	"net/http"
	"time"

	"openfox/internal/identity"
	"openfox/internal/membership"
)

type personalAccessTokenResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type createPersonalAccessTokenRequest struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
}

func (h *Handler) registerTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/tokens", h.listMyTokens)
	mux.HandleFunc("POST /v1/me/tokens", h.createMyToken)
	mux.HandleFunc("DELETE /v1/me/tokens/{tokenID}", h.revokeMyToken)
}

// rejectTokenAuth 用个人访问令牌认证的请求不能执行令牌管理与修改邮箱；返回 true 表示已写入 403。
func rejectTokenAuth(w http.ResponseWriter, r *http.Request, auth authContext) bool {
	if auth.principal.TokenID == "" {
		return false
	}
	writeError(w, r, http.StatusForbidden, "TOKEN_FORBIDDEN", "personal access tokens cannot perform this action")
	return true
}

func (h *Handler) listMyTokens(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, false)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	items, err := h.auth.ListPersonalAccessTokens(r.Context(), auth.principal.UserID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	tokens := make([]personalAccessTokenResponse, 0, len(items))
	for _, item := range items {
		tokens = append(tokens, toPersonalAccessTokenResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

// createMyToken 明文 secret 只在这次响应里出现；服务端只保存哈希。
func (h *Handler) createMyToken(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	var input createPersonalAccessTokenRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	token, secret, err := h.auth.CreatePersonalAccessToken(r.Context(), auth.principal.UserID, input.Name, input.ExpiresInDays)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, map[string]any{"token": toPersonalAccessTokenResponse(token), "secret": secret})
	case errors.Is(err, identity.ErrInvalidToken):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "name must be 1-80 characters and expires_in_days must be 30, 90, 365 or null")
	case errors.Is(err, identity.ErrTokenLimit):
		writeError(w, r, http.StatusConflict, "TOKEN_LIMIT", "at most 50 active tokens are allowed, revoke an unused token first")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

// revokeMyToken 只能吊销本人且未吊销的令牌；别人的、已吊销的、格式不对的 ID 一律 404。
func (h *Handler) revokeMyToken(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	tokenID := r.PathValue("tokenID")
	if !membership.ValidateUUID(tokenID) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "token is not available")
		return
	}
	err := h.auth.RevokePersonalAccessToken(r.Context(), auth.principal.UserID, tokenID)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, identity.ErrTokenNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "token is not available")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

func toPersonalAccessTokenResponse(token identity.PersonalAccessToken) personalAccessTokenResponse {
	return personalAccessTokenResponse{
		ID: token.ID, Name: token.Name, Prefix: token.Prefix, CreatedAt: token.CreatedAt,
		ExpiresAt: token.ExpiresAt, LastUsedAt: token.LastUsedAt,
	}
}
