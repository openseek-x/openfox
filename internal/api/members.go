package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/identity"
	"openfox/internal/membership"
	"openfox/internal/platform/requestid"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// registerMemberRoutes is intentionally separate from New so the owning API
// wiring can choose when to mount this vertical slice.
func (h *Handler) registerMemberRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/members", h.listMembers)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/members", h.addMember)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/members/{userID}", h.changeMember)
}

type addMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

type changeMemberRequest struct {
	Role   *string `json:"role"`
	Status *string `json:"status"`
}

type memberResponse struct {
	UserID               string    `json:"user_id"`
	Email                string    `json:"email"`
	Role                 string    `json:"role"`
	Status               string    `json:"status"`
	AuthorizationVersion uint64    `json:"authorization_version"`
	CreatedAt            time.Time `json:"created_at"`
	FullName             string    `json:"full_name"`
	AvatarURL            *string   `json:"avatar_url"`
	// Spaces 只在列表接口返回：成员显式加入且当前用户能读到的活跃空间。
	Spaces []collaboration.MemberSpace `json:"spaces,omitempty"`
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	authContext, ok := h.authenticateMemberRequest(w, r, false)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	organizationID := strings.TrimSpace(r.PathValue("organizationID"))
	selection, role, err := h.selectMemberOrganization(r.Context(), authContext, organizationID)
	if err != nil {
		writeMemberSelectionError(w, r, err)
		return
	}
	limit, err := membership.ParsePageSize(r.URL.Query().Get("limit"))
	if err != nil {
		writeMemberError(w, r, err)
		return
	}
	var cursor *membership.Cursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		parsed, err := membership.ParseCursor(raw)
		if err != nil {
			writeMemberError(w, r, err)
			return
		}
		cursor = &parsed
	}

	actor := membership.ActorSnapshot{
		UserID:               authContext.principal.UserID,
		Role:                 role,
		AuthorizationVersion: selection.AuthorizationVersion,
	}
	var page membership.Page
	var spaces map[string][]collaboration.MemberSpace
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticMemberSelector(selection), func(ctx context.Context, tx pgx.Tx) error {
			decision := authorizeMemberRead(organizationID, actor)
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			var err error
			if page, err = (membership.Service{}).List(ctx, tx, organizationID, actor, limit, cursor); err != nil {
				return err
			}
			userIDs := make([]string, 0, len(page.Members))
			for _, member := range page.Members {
				userIDs = append(userIDs, member.UserID)
			}
			spaces, err = collaboration.MemberSpaces(ctx, tx, collaboration.Actor(actor), userIDs)
			return err
		})
	if err != nil {
		writeMemberError(w, r, err)
		return
	}
	response := make([]memberResponse, 0, len(page.Members))
	for _, member := range page.Members {
		item := toMemberResponse(member)
		item.Spaces = spaces[member.UserID]
		if item.Spaces == nil {
			item.Spaces = []collaboration.MemberSpace{}
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"members":     response,
		"next_cursor": page.NextCursor,
	})
}

func (h *Handler) addMember(w http.ResponseWriter, r *http.Request) {
	authContext, ok := h.authenticateMemberRequest(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input addMemberRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	input.UserID = strings.TrimSpace(input.UserID)
	if !membership.ValidateUUID(input.UserID) {
		writeMemberError(w, r, membership.ErrInvalidUserID)
		return
	}
	desiredRole, err := membership.ParseRole(input.Role)
	if err != nil {
		writeMemberError(w, r, err)
		return
	}
	organizationID := strings.TrimSpace(r.PathValue("organizationID"))
	selection, actorRole, err := h.selectMemberOrganization(r.Context(), authContext, organizationID)
	if err != nil {
		writeMemberSelectionError(w, r, err)
		return
	}
	requestID := ensureMemberRequestID(w, r)
	actor := membership.ActorSnapshot{
		UserID:               authContext.principal.UserID,
		Role:                 actorRole,
		AuthorizationVersion: selection.AuthorizationVersion,
	}
	var result membership.MutationResult
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticMemberSelector(selection), func(ctx context.Context, tx pgx.Tx) error {
			decision := authorizeMemberManagement(organizationID, actor)
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			var err error
			result, err = (membership.Service{}).Add(ctx, tx, organizationID, actor, input.UserID, desiredRole)
			if err != nil {
				return err
			}
			return recordMemberAudit(ctx, tx, requestID, organizationID, actor, result)
		})
	if err != nil {
		writeMemberError(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Operation != "created" {
		status = http.StatusOK
	}
	writeJSON(w, status, toMemberResponse(result.Member))
}

func (h *Handler) changeMember(w http.ResponseWriter, r *http.Request) {
	authContext, ok := h.authenticateMemberRequest(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input changeMemberRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if input.Role == nil && input.Status == nil {
		writeMemberError(w, r, membership.ErrInvalidTransition)
		return
	}
	var desiredRole *membership.Role
	if input.Role != nil {
		parsed, err := membership.ParseRole(*input.Role)
		if err != nil {
			writeMemberError(w, r, err)
			return
		}
		desiredRole = &parsed
	}
	var desiredStatus *membership.Status
	if input.Status != nil {
		parsed, err := membership.ParseStatus(*input.Status)
		if err != nil || parsed == membership.StatusInvited {
			if err == nil {
				err = membership.ErrInvalidStatus
			}
			writeMemberError(w, r, err)
			return
		}
		desiredStatus = &parsed
	}
	organizationID := strings.TrimSpace(r.PathValue("organizationID"))
	userID := strings.TrimSpace(r.PathValue("userID"))
	if !membership.ValidateUUID(userID) {
		writeMemberError(w, r, membership.ErrInvalidUserID)
		return
	}
	selection, actorRole, err := h.selectMemberOrganization(r.Context(), authContext, organizationID)
	if err != nil {
		writeMemberSelectionError(w, r, err)
		return
	}
	requestID := ensureMemberRequestID(w, r)
	actor := membership.ActorSnapshot{
		UserID:               authContext.principal.UserID,
		Role:                 actorRole,
		AuthorizationVersion: selection.AuthorizationVersion,
	}
	var result membership.MutationResult
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticMemberSelector(selection), func(ctx context.Context, tx pgx.Tx) error {
			decision := authorizeMemberManagement(organizationID, actor)
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			var err error
			result, err = (membership.Service{}).Change(ctx, tx, organizationID, actor, userID, desiredRole, desiredStatus)
			if err != nil {
				return err
			}
			return recordMemberAudit(ctx, tx, requestID, organizationID, actor, result)
		})
	if err != nil {
		writeMemberError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMemberResponse(result.Member))
}

func (h *Handler) authenticateMemberRequest(w http.ResponseWriter, r *http.Request, mutation bool) (authContext, bool) {
	result, err := h.authenticate(r)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return authContext{}, false
	}
	if mutation && result.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return authContext{}, false
	}
	return result, true
}

func (h *Handler) selectMemberOrganization(ctx context.Context, authContext authContext, organizationID string) (tenancy.OrganizationSelection, membership.Role, error) {
	if !membership.ValidateUUID(organizationID) {
		return tenancy.OrganizationSelection{}, "", tenancy.ErrInvalidOrganizationID
	}
	selection, roleValue, err := h.auth.SelectOrganization(ctx, authContext.principal.UserID, organizationID)
	if err != nil {
		return tenancy.OrganizationSelection{}, "", err
	}
	if !strings.EqualFold(selection.OrganizationID, organizationID) || selection.ActorID != authContext.principal.UserID {
		return tenancy.OrganizationSelection{}, "", tenancy.ErrCrossOrganizationContext
	}
	role, err := membership.ParseRole(roleValue)
	if err != nil || selection.AuthorizationVersion == 0 {
		return tenancy.OrganizationSelection{}, "", tenancy.ErrOrganizationSelection
	}
	return selection, role, nil
}

func staticMemberSelector(selection tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}

func authorizeMemberRead(organizationID string, actor membership.ActorSnapshot) policy.Decision {
	return policy.Authorize(policy.Request{
		Action: policy.ActionReadOrganization,
		Actor: policy.Actor{
			OrganizationID: organizationID,
			ActorID:        actor.UserID,
			Role:           actor.Role,
			Membership:     policy.MembershipActive,
		},
	})
}

func authorizeMemberManagement(organizationID string, actor membership.ActorSnapshot) policy.Decision {
	return policy.Authorize(policy.Request{
		Action: policy.ActionManageOrganization,
		Actor: policy.Actor{
			OrganizationID: organizationID,
			ActorID:        actor.UserID,
			Role:           actor.Role,
			Membership:     policy.MembershipActive,
		},
	})
}

func toMemberResponse(member membership.Member) memberResponse {
	return memberResponse{
		UserID:               member.UserID,
		Email:                member.Email,
		Role:                 strings.ToUpper(string(member.Role)),
		Status:               string(member.Status),
		AuthorizationVersion: member.AuthorizationVersion,
		CreatedAt:            member.CreatedAt,
		FullName:             member.FullName,
		AvatarURL:            avatarURL(member.UserID, member.AvatarUpdatedAt),
	}
}

func recordMemberAudit(ctx context.Context, tx pgx.Tx, requestID, organizationID string, actor membership.ActorSnapshot, result membership.MutationResult) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	resultValue := result.Operation
	if result.PreviousRole != "" {
		resultValue = fmt.Sprintf("%s:%s/%s->%s/%s", result.Operation,
			strings.ToUpper(string(result.PreviousRole)), result.PreviousStatus,
			strings.ToUpper(string(result.Member.Role)), result.Member.Status)
	}
	metadata := map[string]string{
		"result":        resultValue,
		"status":        string(result.Member.Status),
		"version":       strconv.FormatUint(result.Member.AuthorizationVersion, 10),
		"grant_scope":   "role:" + strings.ToUpper(string(result.Member.Role)),
		"resource_kind": "member",
	}
	return membership.RecordAudit(ctx, tx, audit.Event{
		ID:                   eventID,
		OrganizationID:       organizationID,
		ActorID:              actor.UserID,
		Action:               memberAuditAction(result.Operation),
		Decision:             "allow",
		Reason:               "allowed",
		Resource:             audit.ResourceRef{Kind: "member", OrganizationID: organizationID, ID: result.Member.UserID},
		RequestID:            requestID,
		AuthorizationVersion: actor.AuthorizationVersion,
		SafeMetadata:         metadata,
		CreatedAt:            time.Now().UTC(),
	})
}

func memberAuditAction(operation string) string {
	switch operation {
	case "created":
		return "membership.created"
	case "activated", "activated_role_changed":
		return "membership.activated"
	case "role_changed":
		return "membership.role_changed"
	case "revoked":
		return "membership.revoked"
	default:
		return "membership.updated"
	}
}

func ensureMemberRequestID(w http.ResponseWriter, r *http.Request) string {
	id := requestid.FromContext(r.Context())
	if id == "" {
		id = strings.TrimSpace(r.Header.Get(requestid.Header))
	}
	if id == "" {
		id = requestid.New()
		w.Header().Set(requestid.Header, id)
	}
	return id
}

func writeMemberSelectionError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, tenancy.ErrInvalidOrganizationID) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "organization id is invalid")
		return
	}
	writeOrganizationSelectionError(w, r, err)
}

func writeMemberError(w http.ResponseWriter, r *http.Request, err error) {
	var denied decisionError
	if errors.As(err, &denied) {
		writeError(w, r, denied.decision.HTTPStatus(), denied.decision.ErrorCode(), "action is not allowed")
		return
	}
	switch {
	case errors.Is(err, membership.ErrInvalidCursor), errors.Is(err, membership.ErrInvalidPageSize),
		errors.Is(err, membership.ErrInvalidRole), errors.Is(err, membership.ErrInvalidStatus),
		errors.Is(err, membership.ErrInvalidTransition), errors.Is(err, membership.ErrInvalidUserID):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "member request is invalid")
	case errors.Is(err, membership.ErrActionForbidden):
		writeError(w, r, http.StatusForbidden, "ACTION_FORBIDDEN", "action is not allowed")
	case errors.Is(err, membership.ErrMemberNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "member is not available")
	case errors.Is(err, membership.ErrLastOwner):
		writeError(w, r, http.StatusConflict, "OWNER_REQUIRED", "organization requires an active owner")
	case errors.Is(err, membership.ErrAuthorizationStale):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "organization authorization changed")
	case errors.Is(err, membership.ErrMemberConflict):
		writeError(w, r, http.StatusConflict, "MEMBER_CONFLICT", "member state conflicts with the request")
	case errors.Is(err, tenancy.ErrCrossOrganizationContext):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "member is not available")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}
