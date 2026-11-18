package api

import (
	"errors"
	"net/http"
	"time"

	"openfox/internal/identity"
)

type meResponse struct {
	ID         string  `json:"id"`
	Email      string  `json:"email"`
	FullName   string  `json:"full_name"`
	Bio        string  `json:"bio"`
	AvatarURL  *string `json:"avatar_url"`
	AccessTier string  `json:"access_tier"`
}

func newMeResponse(user identity.User) meResponse {
	return meResponse{
		ID:         user.ID,
		Email:      user.Email,
		FullName:   user.FullName,
		Bio:        user.Bio,
		AvatarURL:  avatarURL(user.ID, user.AvatarUpdatedAt),
		AccessTier: user.AccessTier,
	}
}

func (h *Handler) registerSessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me", h.getMe)
	mux.HandleFunc("POST /v1/auth/logout", h.logout)
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request) {
	auth, err := h.authenticate(r)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return
	}
	user, err := h.auth.CurrentUser(r.Context(), auth.principal.UserID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			// 会话有效但账号已不存在：按未登录处理，让客户端回到登录页
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, newMeResponse(user))
}

// logout 吊销当前会话并清除 cookie。会话已失效或不存在时同样返回 204，
// 但通过 cookie 认证时必须带 CSRF 头，防止第三方页面强制用户注销。
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	token, viaCookie := bearerToken(r), false
	if token == "" {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			token, viaCookie = cookie.Value, true
		}
	}
	// 个人访问令牌不对应会话：注销时不吊销任何会话，直接返回 204。
	if !viaCookie && identity.IsPersonalAccessToken(token) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return
	}
	if err := h.auth.Logout(r.Context(), token); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	clearCookie(w, sessionCookieName, true, h.secureCookies)
	clearCookie(w, csrfCookieName, false, h.secureCookies)
	w.WriteHeader(http.StatusNoContent)
}

func clearCookie(w http.ResponseWriter, name string, httpOnly, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
