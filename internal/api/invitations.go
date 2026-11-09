package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"openfox/internal/identity"
	"openfox/internal/invitations"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

type createInvitationRequest struct {
	Email    string   `json:"email"`
	Role     string   `json:"role"`
	SpaceIDs []string `json:"space_ids"`
}

type acceptInvitationRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type invitationCreateResponse struct {
	ID             string             `json:"id"`
	OrganizationID string             `json:"organization_id"`
	Email          string             `json:"email"`
	Role           string             `json:"role"`
	Status         invitations.Status `json:"status"`
	IssuedAt       time.Time          `json:"issued_at"`
	ExpiresAt      time.Time          `json:"expires_at"`
	SpaceIDs       []string           `json:"space_ids"`
	DeliveryStatus string             `json:"delivery_status"`
	LastSentAt     *time.Time         `json:"last_sent_at"`
}

func (h *Handler) registerInviteLinkRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	mux.HandleFunc("POST "+org+"/invites/{inviteID}/resend", h.resendInvitation)
	mux.HandleFunc("GET "+org+"/invite-link", h.getInviteLink)
	mux.HandleFunc("PUT "+org+"/invite-link", h.putInviteLink)
	mux.HandleFunc("POST "+org+"/invite-link/reset", h.resetInviteLink)
	// 令牌放在请求体里，不进入访问日志的路径。
	mux.HandleFunc("POST /v1/invite-links/preview", h.previewInviteLink)
	mux.HandleFunc("POST /v1/invite-links/join", h.joinInviteLink)
}

// beginInvitationCall 是组织内邀请接口共同的前置步骤：认证、CSRF、数据库就绪与组织选择。
func (h *Handler) beginInvitationCall(w http.ResponseWriter, r *http.Request, mutation bool) (tenancy.OrganizationSelection, string, bool) {
	authContext, err := h.authenticate(r)
	if err != nil {
		writeInvitationAuthError(w, r, err)
		return tenancy.OrganizationSelection{}, "", false
	}
	if mutation && authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return tenancy.OrganizationSelection{}, "", false
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return tenancy.OrganizationSelection{}, "", false
	}
	selection, actorRole, err := h.auth.SelectOrganization(r.Context(), authContext.principal.UserID, r.PathValue("organizationID"))
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return tenancy.OrganizationSelection{}, "", false
	}
	return selection, actorRole, true
}

func toInvitationResponse(invit invitations.Invitation) invitationCreateResponse {
	return invitationCreateResponse{
		ID: invit.ID, OrganizationID: invit.OrganizationID, Email: invit.Email, Role: invit.Role,
		Status: invit.Status, IssuedAt: invit.IssuedAt, ExpiresAt: invit.ExpiresAt,
		SpaceIDs: invit.SpaceIDs, DeliveryStatus: invit.DeliveryStatus, LastSentAt: invit.LastSentAt,
	}
}

func (h *Handler) createInvitation(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, true)
	if !ok {
		return
	}
	var input createInvitationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	service := invitations.Service{Pool: h.pool}
	invit, delivery, err := service.Create(r.Context(), selection, actorRole, invitations.CreateInput{
		Email: input.Email, Role: input.Role, SpaceIDs: input.SpaceIDs,
	})
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	h.deliverInvitation(r.Context(), service, selection, &invit, delivery)
	writeJSON(w, http.StatusCreated, toInvitationResponse(invit))
}

func (h *Handler) resendInvitation(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, true)
	if !ok {
		return
	}
	service := invitations.Service{Pool: h.pool}
	invit, delivery, err := service.Resend(r.Context(), selection, actorRole, r.PathValue("inviteID"))
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	h.deliverInvitation(r.Context(), service, selection, &invit, delivery)
	writeJSON(w, http.StatusOK, toInvitationResponse(invit))
}

// deliverInvitation 在邀请事务提交之后发送邮件并记录结果。没有配置 SMTP 或发送失败都记为 FAILED，
// 界面显示「发送失败，可重试」；记录失败不影响已创建的邀请。明文令牌只出现在邮件正文里。
func (h *Handler) deliverInvitation(ctx context.Context, service invitations.Service, selection tenancy.OrganizationSelection,
	invit *invitations.Invitation, delivery invitations.Delivery) {
	sent := false
	if h.auth.EmailSender != nil {
		link := strings.TrimRight(h.publicURL, "/") + "/join?invite=" + url.QueryEscape(invit.ID) +
			"&token=" + url.QueryEscape(delivery.Token)
		subject, body := invitationEmail(delivery, link)
		sent = h.auth.EmailSender.Send(ctx, invit.Email, subject, body) == nil
	}
	_ = service.RecordDelivery(ctx, selection, invit, sent)
}

func invitationEmail(delivery invitations.Delivery, link string) (string, string) {
	if delivery.Locale == "en-US" {
		return fmt.Sprintf("%s invited you to join %s on OpenFox", delivery.InviterEmail, delivery.OrganizationName),
			fmt.Sprintf("%s invited you to join %s on OpenFox.\n\nAccept the invitation: %s\n\nThe link expires in 7 days.",
				delivery.InviterEmail, delivery.OrganizationName, link)
	}
	return fmt.Sprintf("%s 邀请你加入 OpenFox 上的 %s", delivery.InviterEmail, delivery.OrganizationName),
		fmt.Sprintf("%s 邀请你加入 OpenFox 上的 %s。\n\n接受邀请：%s\n\n链接 7 天内有效。",
			delivery.InviterEmail, delivery.OrganizationName, link)
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, false)
	if !ok {
		return
	}
	items, err := (invitations.Service{Pool: h.pool}).List(r.Context(), selection, actorRole)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": items})
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, true)
	if !ok {
		return
	}
	invit, err := (invitations.Service{Pool: h.pool}).Revoke(r.Context(), selection, actorRole, r.PathValue("inviteID"))
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invit)
}

func (h *Handler) getInviteLink(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, false)
	if !ok {
		return
	}
	link, err := (invitations.Service{Pool: h.pool}).GetLink(r.Context(), selection, actorRole, h.secrets)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *Handler) putInviteLink(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Enabled  bool     `json:"enabled"`
		Role     string   `json:"role"`
		SpaceIDs []string `json:"space_ids"`
		Version  uint64   `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	link, err := (invitations.Service{Pool: h.pool}).PutLink(r.Context(), selection, actorRole, h.secrets,
		invitations.LinkInput{Enabled: input.Enabled, Role: input.Role, SpaceIDs: input.SpaceIDs, Version: input.Version})
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *Handler) resetInviteLink(w http.ResponseWriter, r *http.Request) {
	selection, actorRole, ok := h.beginInvitationCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	link, err := (invitations.Service{Pool: h.pool}).ResetLink(r.Context(), selection, actorRole, h.secrets, input.Version)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (h *Handler) previewInviteLink(w http.ResponseWriter, r *http.Request) {
	h.inviteLinkByToken(w, r, false)
}

func (h *Handler) joinInviteLink(w http.ResponseWriter, r *http.Request) {
	h.inviteLinkByToken(w, r, true)
}

// inviteLinkByToken 处理加入页的预览与加入：需要登录（任何账号都可以凭链接加入），不需要组织上下文。
func (h *Handler) inviteLinkByToken(w http.ResponseWriter, r *http.Request, join bool) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	service := invitations.Service{Pool: h.pool}
	var result any
	var err error
	if join {
		result, err = service.JoinLink(r.Context(), auth.principal.UserID, input.Token)
	} else {
		result, err = service.PreviewLink(r.Context(), auth.principal.UserID, input.Token)
	}
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var actorID *string
	authContext, authErr := h.authenticate(r)
	if authErr == nil {
		if authContext.viaCookie && !validCSRF(r) {
			writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
			return
		}
		actorID = &authContext.principal.UserID
	} else if !errors.Is(authErr, identity.ErrSessionNotFound) {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var input acceptInvitationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}

	service := invitations.Service{Pool: h.pool, PasswordHasher: h.auth.PasswordHasher}
	accepted, err := service.Accept(r.Context(), r.PathValue("inviteID"), input.Token, actorID, input.Password)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, accepted)
}

func writeInvitationAuthError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, identity.ErrSessionNotFound) {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		return
	}
	writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
}

func writeInvitationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, invitations.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invitation request is invalid")
	case errors.Is(err, invitations.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "invitation is not available")
	case errors.Is(err, invitations.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "ACTION_FORBIDDEN", "action is not allowed")
	case errors.Is(err, invitations.ErrAuthenticationRequired):
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
	case errors.Is(err, invitations.ErrPasswordRequired):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "password is required for a new account")
	case errors.Is(err, invitations.ErrAlreadyMember):
		writeError(w, r, http.StatusConflict, "ALREADY_MEMBER", "the email already belongs to a member")
	case errors.Is(err, invitations.ErrCooldown):
		writeError(w, r, http.StatusTooManyRequests, "RESEND_COOLDOWN", "the invitation was sent recently")
	case errors.Is(err, invitations.ErrSecretsUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "invite links require a secrets key")
	case errors.Is(err, invitations.ErrConflict),
		errors.Is(err, invitations.ErrEmailMismatch),
		errors.Is(err, invitations.ErrParameterConflict):
		writeError(w, r, http.StatusConflict, "INVITATION_CONFLICT", "invitation cannot be used in its current state")
	case errors.Is(err, postgres.ErrNilPool):
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}
