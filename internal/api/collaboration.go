package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/identity"
	"openfox/internal/membership"
	"openfox/internal/platform/apierror"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/requestid"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func (h *Handler) registerCollaborationRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/spaces/{spaceID}/task-types", h.listTaskTypes)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/spaces/{spaceID}/task-types", h.createTaskType)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/spaces/{spaceID}/task-types/{typeID}", h.updateTaskType)
}

type collaborationAuth struct {
	Auth      authContext
	Selection tenancy.OrganizationSelection
	Actor     collaboration.Actor
}

func (h *Handler) authenticateCollaboration(w http.ResponseWriter, r *http.Request, mutation bool) (collaborationAuth, bool) {
	result, err := h.authenticate(r)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return collaborationAuth{}, false
	}
	if mutation && result.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return collaborationAuth{}, false
	}
	return collaborationAuth{Auth: result}, true
}

func (h *Handler) selectCollaboration(ctx context.Context, auth collaborationAuth, organizationID string) (collaborationAuth, error) {
	selection, role, err := h.auth.SelectOrganization(ctx, auth.Auth.principal.UserID, organizationID)
	if err != nil {
		return collaborationAuth{}, err
	}
	actorRole := organizationRole(role)
	if selection.ActorID != auth.Auth.principal.UserID || actorRole == "" || selection.AuthorizationVersion == 0 {
		return collaborationAuth{}, tenancy.ErrOrganizationSelection
	}
	auth.Selection = selection
	auth.Actor = collaboration.Actor{
		UserID:               auth.Auth.principal.UserID,
		Role:                 actorRole,
		AuthorizationVersion: selection.AuthorizationVersion,
	}
	return auth, nil
}

func staticCollaborationSelector(selection tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}

func collaborationSpaceDecision(action policy.Action, organizationID string, auth collaborationAuth, access collaboration.SpaceAccess, objectID string) policy.Decision {
	request := policy.Request{
		Action: action,
		Actor: policy.Actor{
			OrganizationID: organizationID,
			ActorID:        auth.Actor.UserID,
			Role:           auth.Actor.Role,
			Membership:     policy.MembershipActive,
		},
		Space: &policy.Space{
			OrganizationID: organizationID,
			ID:             access.Space.ID,
			Visibility:     spaceVisibility(access.Space.Visibility),
		},
	}
	if access.Membership != nil {
		membershipState := policy.MembershipActive
		if strings.ToUpper(access.Membership.Status) != "ACTIVE" {
			membershipState = policy.MembershipRevoked
		}
		request.SpaceMembership = &policy.SpaceMembership{
			OrganizationID: organizationID,
			SpaceID:        access.Space.ID,
			ActorID:        access.Membership.UserID,
			Role:           spaceRole(access.Membership.Role),
			Membership:     membershipState,
		}
	}
	if objectID != "" {
		request.Object = &policy.Object{OrganizationID: organizationID, SpaceID: access.Space.ID, ID: objectID}
	}
	return policy.Authorize(request)
}

type taskTypeRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Icon            string   `json:"icon"`
	Enabled         *bool    `json:"enabled"`
	Default         bool     `json:"is_default"`
	StatusIDs       []string `json:"status_ids"`
	DefaultStatusID string   `json:"default_status_id"`
}

type taskTypePatchRequest struct {
	Name            *string   `json:"name"`
	Description     *string   `json:"description"`
	Icon            *string   `json:"icon"`
	Enabled         *bool     `json:"enabled"`
	Default         *bool     `json:"is_default"`
	Archived        *bool     `json:"archived"`
	StatusIDs       *[]string `json:"status_ids"`
	DefaultStatusID *string   `json:"default_status_id"`
	Version         uint64    `json:"version"`
}

func (h *Handler) listTaskTypes(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateCollaboration(w, r, false)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	organizationID, spaceID := r.PathValue("organizationID"), r.PathValue("spaceID")
	selected, err := h.selectCollaboration(r.Context(), auth, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	var items []collaboration.TaskType
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(selected.Selection), func(ctx context.Context, tx pgx.Tx) error {
			service := collaboration.Service{}
			access, err := service.LoadSpaceAccess(ctx, tx, organizationID, spaceID, selected.Actor.UserID)
			if err != nil {
				return err
			}
			decision := collaborationSpaceDecision(policy.ActionReadSpace, organizationID, selected, access, "")
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			items, err = service.ListTaskTypes(ctx, tx, organizationID, spaceID, selected.Actor)
			return err
		})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task_types": items})
}

func (h *Handler) createTaskType(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateCollaboration(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input taskTypeRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	organizationID, spaceID := r.PathValue("organizationID"), r.PathValue("spaceID")
	selected, err := h.selectCollaboration(r.Context(), auth, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	requestID := collaborationRequestID(w, r)
	var item collaboration.TaskType
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(selected.Selection), func(ctx context.Context, tx pgx.Tx) error {
			service := collaboration.Service{}
			access, err := service.LoadSpaceAccess(ctx, tx, organizationID, spaceID, selected.Actor.UserID)
			if err != nil {
				return err
			}
			decision := collaborationSpaceDecision(policy.ActionManageSpace, organizationID, selected, access, "")
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			item, err = service.CreateTaskType(ctx, tx, organizationID, spaceID, selected.Actor, collaboration.TaskTypeInput{
				Name: input.Name, Description: input.Description, Icon: input.Icon, Enabled: input.Enabled,
				Default: input.Default, StatusIDs: input.StatusIDs, DefaultStatusID: input.DefaultStatusID,
			})
			if err != nil {
				return err
			}
			return recordCollaborationAudit(ctx, tx, requestID, organizationID, selected.Actor,
				"task_type.created", "task_type", item.ID, spaceID, selected.Actor.AuthorizationVersion, map[string]string{"space_id": spaceID, "version": fmt.Sprint(item.Version)})
		})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateTaskType(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateCollaboration(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input taskTypePatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	organizationID, spaceID, typeID := r.PathValue("organizationID"), r.PathValue("spaceID"), r.PathValue("typeID")
	selected, err := h.selectCollaboration(r.Context(), auth, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	requestID := collaborationRequestID(w, r)
	var item collaboration.TaskType
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(selected.Selection), func(ctx context.Context, tx pgx.Tx) error {
			service := collaboration.Service{}
			access, err := service.LoadSpaceAccess(ctx, tx, organizationID, spaceID, selected.Actor.UserID)
			if err != nil {
				return err
			}
			decision := collaborationSpaceDecision(policy.ActionManageSpace, organizationID, selected, access, "")
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			item, err = service.UpdateTaskType(ctx, tx, organizationID, spaceID, typeID, selected.Actor,
				collaboration.TaskTypeUpdateInput{Name: input.Name, Description: input.Description, Icon: input.Icon,
					Enabled: input.Enabled, Default: input.Default, Archived: input.Archived,
					StatusIDs: input.StatusIDs, DefaultStatusID: input.DefaultStatusID, Version: input.Version})
			if err != nil {
				return err
			}
			return recordCollaborationAudit(ctx, tx, requestID, organizationID, selected.Actor,
				"task_type.updated", "task_type", item.ID, spaceID, selected.Actor.AuthorizationVersion, map[string]string{"space_id": spaceID, "version": fmt.Sprint(item.Version)})
		})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func toSpaceResponse(space collaboration.Space) spaceResponse {
	return spaceResponse{ID: space.ID, Prefix: space.Prefix, Name: space.Name,
		Visibility: strings.ToLower(space.Visibility), Version: space.Version, CreatedAt: space.CreatedAt}
}

func collaborationActorPolicy(organizationID string, actor collaboration.Actor) policy.Actor {
	return policy.Actor{OrganizationID: organizationID, ActorID: actor.UserID, Role: actor.Role, Membership: policy.MembershipActive}
}

func recordCollaborationAudit(ctx context.Context, tx pgx.Tx, requestID, organizationID string, actor collaboration.Actor,
	action, resourceKind, resourceID, spaceID string, version uint64, metadata map[string]string) error {
	eventID, err := collaboration.NewID()
	if err != nil {
		return err
	}
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["resource_kind"] = resourceKind
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actor.UserID, Action: action,
		Decision: "allow", Reason: "allowed", Resource: audit.ResourceRef{Kind: resourceKind,
			OrganizationID: organizationID, SpaceID: spaceID, ID: resourceID}, RequestID: requestID,
		AuthorizationVersion: version, SafeMetadata: metadata, CreatedAt: time.Now().UTC(),
	})
}

func collaborationRequestID(w http.ResponseWriter, r *http.Request) string {
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

func writeCollaborationError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := collaborationErrorParts(err)
	if len(details) > 0 {
		requestID := requestid.FromContext(r.Context())
		if requestID == "" {
			requestID = r.Header.Get(requestid.Header)
		}
		apierror.WriteDetails(w, status, code, message, requestID, details)
		return
	}
	writeError(w, r, status, code, message)
}

// collaborationErrorParts 把协作类错误映射为 HTTP 状态、错误码、文案与可选的 details；
// 批量接口在此基础上追加 details.task_ids。
func collaborationErrorParts(err error) (int, string, string, map[string]any) {
	var denied decisionError
	if errors.As(err, &denied) {
		return denied.decision.HTTPStatus(), denied.decision.ErrorCode(), "action is not allowed", nil
	}
	if collaboration.IsPriorityLevelViolation(err) {
		err = collaboration.ErrPriorityLevelUnavailable
	}
	var rule *collaboration.RuleError
	if errors.As(err, &rule) {
		return rule.Status, rule.Code, rule.Message, rule.Details
	}
	switch {
	case errors.Is(err, membership.ErrAuthorizationStale):
		return http.StatusUnauthorized, "AUTHORIZATION_STALE", "authorization changed; refresh and retry", nil
	case errors.Is(err, idempotency.ErrInvalidKey):
		return http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid", nil
	case errors.Is(err, idempotency.ErrConflict):
		return http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was used for a different request", nil
	case errors.Is(err, tenancy.ErrOrganizationSelection), errors.Is(err, postgres.ErrOrganizationNotFound),
		errors.Is(err, collaboration.ErrSpaceNotFound), errors.Is(err, collaboration.ErrTaskNotFound),
		errors.Is(err, collaboration.ErrTaskTypeNotFound), errors.Is(err, collaboration.ErrStatusNotFound),
		errors.Is(err, collaboration.ErrSpaceMemberNotFound):
		return http.StatusNotFound, "NOT_FOUND", "resource is not available", nil
	case errors.Is(err, collaboration.ErrInvalidCursor), errors.Is(err, collaboration.ErrInvalidPageSize),
		errors.Is(err, collaboration.ErrInvalidPrefix), errors.Is(err, collaboration.ErrInvalidVisibility),
		errors.Is(err, collaboration.ErrInvalidCategory), errors.Is(err, collaboration.ErrInvalidPriority),
		errors.Is(err, collaboration.ErrInvalidName), errors.Is(err, collaboration.ErrInvalidTask),
		errors.Is(err, collaboration.ErrInvalidVersion), errors.Is(err, collaboration.ErrInvalidStatus),
		errors.Is(err, collaboration.ErrInvalidWorkflow):
		return http.StatusBadRequest, "INVALID_REQUEST", "collaboration request is invalid", nil
	case errors.Is(err, collaboration.ErrVersionConflict), errors.Is(err, collaboration.ErrConflict),
		errors.Is(err, collaboration.ErrStatusInUse), errors.Is(err, collaboration.ErrStatusRequired),
		errors.Is(err, collaboration.ErrActionForbidden):
		code := "VERSION_CONFLICT"
		if !errors.Is(err, collaboration.ErrVersionConflict) {
			code = "COLLABORATION_CONFLICT"
		}
		return http.StatusConflict, code, "collaboration state conflicts with the request", nil
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed", nil
	}
}
