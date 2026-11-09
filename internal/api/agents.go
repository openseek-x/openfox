package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/requestid"
	"openfox/internal/tenancy"
)

func (h *Handler) registerAgentRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/spaces/{spaceID}/agents", h.listAgents)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/spaces/{spaceID}/agents", h.createAgent)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}", h.getAgent)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}", h.updateAgent)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}/config-versions", h.listAgentConfigs)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}/config-versions", h.createAgentConfig)
}

type agentCreateRequest struct {
	Name            string             `json:"name"`
	Description     string             `json:"description"`
	Kind            agents.Kind        `json:"kind"`
	Visibility      agents.Visibility  `json:"visibility"`
	Config          agents.ConfigInput `json:"config"`
	Title           string             `json:"title"`
	Avatar          json.RawMessage    `json:"avatar"`
	AllowAssign     *bool              `json:"allow_assign"`
	AllowDirectChat *bool              `json:"allow_direct_chat"`
}

type agentUpdateRequest struct {
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Kind        *agents.Kind       `json:"kind"`
	Visibility  *agents.Visibility `json:"visibility"`
	Archived    *bool              `json:"archived"`
	Version     uint64             `json:"version"`
}

type agentConfigRequest struct {
	agents.ConfigInput
	ExpectedAgentVersion uint64 `json:"expected_agent_version"`
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, false)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	includeArchived, err := parseBoolQuery(r, "include_archived")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "include_archived is invalid")
		return
	}
	items, err := (agents.PostgresStore{Pool: h.pool}).List(r.Context(), auth.Selection, agents.Actor{
		UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion,
	}, r.PathValue("spaceID"), agents.ListOptions{IncludeArchived: includeArchived})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": items})
}

func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input agentCreateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).Create(r.Context(), auth.Selection, agents.Actor{
		UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}, r.PathValue("spaceID"), agents.CreateInput{
		Name: input.Name, Description: input.Description, Kind: input.Kind,
		Visibility: input.Visibility, Config: input.Config, Title: input.Title,
		Avatar: nullJSON(input.Avatar), AllowAssign: input.AllowAssign, AllowDirectChat: input.AllowDirectChat,
	})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, false)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).Get(r.Context(), auth.Selection, agents.Actor{
		UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}, r.PathValue("spaceID"), r.PathValue("agentID"))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updateAgent(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input agentUpdateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).Update(r.Context(), auth.Selection, agents.Actor{
		UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}, r.PathValue("spaceID"), r.PathValue("agentID"), agents.UpdateInput{
		Name: input.Name, Description: input.Description, Kind: input.Kind,
		Visibility: input.Visibility, Archived: input.Archived, Version: input.Version,
	})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) listAgentConfigs(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, false)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	items, err := (agents.PostgresStore{Pool: h.pool}).ListConfigs(r.Context(), auth.Selection, agents.Actor{
		UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}, r.PathValue("spaceID"), r.PathValue("agentID"))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config_versions": items})
}

func (h *Handler) createAgentConfig(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input agentConfigRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().CreateConfig(r.Context(), auth.Selection, agents.Actor{
		UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}, r.PathValue("spaceID"), r.PathValue("agentID"), input.ExpectedAgentVersion, input.ConfigInput)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type agentAuth struct {
	Auth      authContext
	Selection tenancy.OrganizationSelection
	Actor     collaboration.Actor
}

func (h *Handler) authenticateAgent(w http.ResponseWriter, r *http.Request, mutation bool) (agentAuth, bool) {
	result, err := h.authenticate(r)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return agentAuth{}, false
	}
	if mutation && result.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return agentAuth{}, false
	}
	organizationID := r.PathValue("organizationID")
	selection, role, err := h.auth.SelectOrganization(r.Context(), result.principal.UserID, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return agentAuth{}, false
	}
	actorRole := organizationRole(role)
	if actorRole == "" || selection.ActorID != result.principal.UserID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization is not available")
		return agentAuth{}, false
	}
	return agentAuth{Auth: result, Selection: selection, Actor: collaboration.Actor{
		UserID: result.principal.UserID, Role: actorRole, AuthorizationVersion: selection.AuthorizationVersion,
	}}, true
}

func parseBoolQuery(r *http.Request, name string) (bool, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	return parsed, err
}

func writeAgentError(w http.ResponseWriter, r *http.Request, err error) {
	var rule *collaboration.RuleError
	if errors.As(err, &rule) {
		writeError(w, r, rule.Status, rule.Code, rule.Message)
		return
	}
	switch {
	case errors.Is(err, idempotency.ErrInvalidKey):
		writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid")
	case errors.Is(err, idempotency.ErrConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was used for a different request")
	case errors.Is(err, agents.ErrNotFound), errors.Is(err, postgres.ErrOrganizationNotFound), errors.Is(err, tenancy.ErrOrganizationSelection):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Agent is not available")
	case errors.Is(err, agents.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "ACTION_FORBIDDEN", "action is not allowed")
	case errors.Is(err, agents.ErrVersionConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "Agent version is stale")
	case errors.Is(err, agents.ErrNameConflict):
		writeError(w, r, http.StatusConflict, "NAME_CONFLICT", "Agent name is already in use")
	case errors.Is(err, agents.ErrArchived):
		writeError(w, r, http.StatusConflict, "ARCHIVED", "archived Agent cannot be configured")
	case errors.Is(err, agents.ErrInvalidRequest), errors.Is(err, agents.ErrInvalidConfig), errors.Is(err, agents.ErrImmutableConfig):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Agent request is invalid")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

// nullJSON 把 JSON null 视为未传。
func nullJSON(value json.RawMessage) json.RawMessage {
	if string(value) == "null" {
		return nil
	}
	return value
}

func agentRequestID(r *http.Request) string {
	if value := requestid.FromContext(r.Context()); value != "" {
		return value
	}
	return strings.TrimSpace(r.Header.Get(requestid.Header))
}
