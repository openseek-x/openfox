package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"openfox/internal/identity"
)

// maxAvatarBytes 是头像文件的上限（5 MiB）。
const maxAvatarBytes = 5 << 20

var avatarTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

type updateMeRequest struct {
	FullName *string `json:"full_name"`
	Bio      *string `json:"bio"`
}

type emailChallengeResponse struct {
	ChallengeID string    `json:"challenge_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	ResendAfter time.Time `json:"resend_after"`
}

type verifyCurrentEmailRequest struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

type requestNewEmailRequest struct {
	ChallengeID string `json:"challenge_id"`
	Email       string `json:"email"`
}

type confirmEmailChangeRequest struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

func (h *Handler) registerProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PATCH /v1/me", h.updateMe)
	mux.HandleFunc("PUT /v1/me/avatar", h.uploadMyAvatar)
	mux.HandleFunc("GET /v1/users/{userID}/avatar", h.getUserAvatar)
	mux.HandleFunc("POST /v1/me/email-change/start", h.startEmailChange)
	mux.HandleFunc("POST /v1/me/email-change/verify-current", h.verifyCurrentEmail)
	mux.HandleFunc("POST /v1/me/email-change/request-new", h.requestNewEmail)
	mux.HandleFunc("POST /v1/me/email-change/confirm", h.confirmEmailChange)
}

// avatarURL 返回头像地址；版本参数取头像更新时间的 Unix 秒，换图后地址随之变化。没有头像时返回 nil。
func avatarURL(userID string, updatedAt *time.Time) *string {
	if updatedAt == nil {
		return nil
	}
	url := "/api/v1/users/" + userID + "/avatar?v=" + strconv.FormatInt(updatedAt.Unix(), 10)
	return &url
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var input updateMeRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	user, err := h.auth.UpdateProfile(r.Context(), auth.principal.UserID, identity.ProfilePatch{FullName: input.FullName, Bio: input.Bio})
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrInvalidProfile):
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "full name must be 1-80 characters and bio at most 280")
		case errors.Is(err, identity.ErrUserNotFound):
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		default:
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return
	}
	writeJSON(w, http.StatusOK, newMeResponse(user))
}

// uploadMyAvatar 按文件头判断格式（不信任扩展名与客户端 Content-Type），内容哈希作为存储键的一部分。
// 新文件在数据库提交失败时删除；旧文件在提交后尽力删除，删除失败忽略。
func (h *Handler) uploadMyAvatar(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	storage := h.delivery.Storage
	if storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	reader, err := r.MultipartReader()
	var data []byte
	if err == nil {
		data, err = readAvatarFile(reader)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AVATAR", "multipart body with a file field is required")
		return
	}
	if len(data) > maxAvatarBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "AVATAR_TOO_LARGE", "avatar must not exceed 5 MiB")
		return
	}
	contentType := http.DetectContentType(data)
	if len(data) == 0 || !avatarTypes[contentType] {
		writeError(w, r, http.StatusBadRequest, "INVALID_AVATAR", "avatar must be a png, jpeg or webp image")
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	userID := auth.principal.UserID
	key := identity.AvatarKey(userID, digest)
	// 同内容的文件已存在时（同一键）直接复用，且它可能正被当前头像引用，所以失败时不能删除。
	created := true
	if _, err := storage.Put(r.Context(), key, bytes.NewReader(data), maxAvatarBytes); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		created = false
	}
	user, previousKey, err := h.auth.ReplaceAvatar(r.Context(), userID, key, contentType, digest)
	if err != nil {
		if created {
			_ = storage.Delete(r.Context(), key)
		}
		if errors.Is(err, identity.ErrUserNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	if previousKey != "" && previousKey != key {
		_ = storage.Delete(r.Context(), previousKey)
	}
	writeJSON(w, http.StatusOK, newMeResponse(user))
}

// readAvatarFile 读取 multipart 中的 file 字段，最多读取 maxAvatarBytes+1 字节，用于判断是否超限。
func readAvatarFile(reader *multipart.Reader) ([]byte, error) {
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, err
		}
		if part.FormName() == "file" {
			return io.ReadAll(io.LimitReader(part, maxAvatarBytes+1))
		}
	}
}

// getUserAvatar 只有本人或与对方同在一个活跃组织的用户才能读取；其余情况与没有头像一样返回 404。
func (h *Handler) getUserAvatar(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, false)
	if !ok {
		return
	}
	avatar, err := h.auth.AvatarForViewer(r.Context(), auth.principal.UserID, r.PathValue("userID"))
	if errors.Is(err, identity.ErrAvatarNotFound) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "avatar is not available")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	storage := h.delivery.Storage
	if storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	file, err := storage.Open(r.Context(), avatar.Key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "avatar is not available")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", avatar.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, file)
}

func (h *Handler) startEmailChange(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	challenge, err := h.auth.StartEmailChange(r.Context(), auth.principal.UserID)
	if err != nil {
		writeEmailChangeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmailChallengeResponse(challenge))
}

func (h *Handler) verifyCurrentEmail(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	var input verifyCurrentEmailRequest
	if err := decodeJSON(w, r, &input); err != nil || input.ChallengeID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if err := h.auth.VerifyCurrentEmail(r.Context(), auth.principal.UserID, input.ChallengeID, input.Code); err != nil {
		writeEmailChangeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requestNewEmail(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	var input requestNewEmailRequest
	if err := decodeJSON(w, r, &input); err != nil || input.ChallengeID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	challenge, err := h.auth.RequestNewEmail(r.Context(), auth.principal.UserID, input.ChallengeID, input.Email)
	if err != nil {
		writeEmailChangeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmailChallengeResponse(challenge))
}

// confirmEmailChange 改邮箱并吊销本人其他会话；当前会话（auth.principal.SessionID）保持有效。
func (h *Handler) confirmEmailChange(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok || rejectTokenAuth(w, r, auth) {
		return
	}
	var input confirmEmailChangeRequest
	if err := decodeJSON(w, r, &input); err != nil || input.ChallengeID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	user, err := h.auth.ConfirmEmailChange(r.Context(), auth.principal.UserID, auth.principal.SessionID, input.ChallengeID, input.Code)
	if err != nil {
		writeEmailChangeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newMeResponse(user))
}

// writeEmailChangeError 把修改邮箱的错误映射为接口错误码：验证码错误 400 CODE_INVALID，限频 429 EMAIL_RATE_LIMITED。
func writeEmailChangeError(w http.ResponseWriter, r *http.Request, err error) {
	var limited identity.EmailCodeLimitError
	var cooldown identity.EmailCodeCooldownError
	switch {
	case errors.As(err, &limited):
		writeRetryAfter(w, limited.RetryAfter)
		writeError(w, r, http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", "too many requests, please retry later")
	case errors.As(err, &cooldown):
		writeRetryAfter(w, cooldown.RetryAfter)
		writeError(w, r, http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", "please wait before requesting another code")
	case errors.Is(err, identity.ErrEmailCodeInvalid):
		writeError(w, r, http.StatusBadRequest, "CODE_INVALID", "verification code is invalid or expired")
	case errors.Is(err, identity.ErrInvalidEmail), errors.Is(err, identity.ErrEmailUnchanged):
		writeError(w, r, http.StatusBadRequest, "INVALID_EMAIL", "new email address is invalid or unchanged")
	case errors.Is(err, identity.ErrEmailTaken):
		writeError(w, r, http.StatusConflict, "EMAIL_TAKEN", "email address is already in use")
	case errors.Is(err, identity.ErrEmailLoginNotConfigured):
		writeError(w, r, http.StatusServiceUnavailable, "EMAIL_LOGIN_UNAVAILABLE", "email verification is not configured")
	case errors.Is(err, identity.ErrEmailDelivery):
		writeError(w, r, http.StatusServiceUnavailable, "EMAIL_DELIVERY_FAILED", "verification email could not be sent")
	case errors.Is(err, identity.ErrUserNotFound):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

func toEmailChallengeResponse(challenge identity.EmailChallenge) emailChallengeResponse {
	return emailChallengeResponse{ChallengeID: challenge.ID, ExpiresAt: challenge.ExpiresAt, ResendAfter: challenge.ResendAfter}
}
