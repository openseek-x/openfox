package api

import (
	"net/http"

	"openfox/internal/agents"
)

// 首页「全部 Agent 和小队」：组织级目录、个人置顶，以及空间内小队的创建与修改（详情与成员见 agents_ext.go）。
func (h *Handler) registerAgentDirectoryRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/agent-directory", h.listAgentDirectory)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/agent-directory/pins", h.setAgentDirectoryPin)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/spaces/{spaceID}/crews", h.createCrew)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/spaces/{spaceID}/crews/{crewID}", h.updateCrew)
}

type agentPinRequest struct {
	AgentID string `json:"agent_id"`
	CrewID  string `json:"crew_id"`
	Pinned  *bool  `json:"pinned"`
}

type crewCreateRequest struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Visibility    agents.Visibility `json:"visibility"`
	LeaderAgentID string            `json:"leader_agent_id"`
	AgentIDs      []string          `json:"agent_ids"`
	UserIDs       []string          `json:"user_ids"`
}

type crewUpdateRequest struct {
	Name          *string            `json:"name"`
	Description   *string            `json:"description"`
	Visibility    *agents.Visibility `json:"visibility"`
	SpaceID       *string            `json:"space_id"`
	OwnerUserID   *string            `json:"owner_user_id"`
	Instructions  *string            `json:"instructions"`
	SharedTaskDir *bool              `json:"shared_task_dir"`
	LeaderAgentID *string            `json:"leader_agent_id"`
	Archived      *bool              `json:"archived"`
	Version       uint64             `json:"version"`
}

func (h *Handler) agentActor(auth agentAuth, r *http.Request) agents.Actor {
	return agents.Actor{
		AuthenticatedSession: auth.Auth.principal.SessionID != "" && auth.Auth.principal.TokenID == "",
		UserID:               auth.Actor.UserID, Role: auth.Actor.Role,
		AuthorizationVersion: auth.Actor.AuthorizationVersion, RequestID: agentRequestID(r),
	}
}

func (h *Handler) listAgentDirectory(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, false)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	archived, err := parseBoolQuery(r, "archived")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "archived is invalid")
		return
	}
	result, err := (agents.PostgresStore{Pool: h.pool}).Directory(r.Context(), auth.Selection, h.agentActor(auth, r),
		agents.DirectoryOptions{Archived: archived})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) setAgentDirectoryPin(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input agentPinRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Pinned == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if err := (agents.PostgresStore{Pool: h.pool}).SetPin(r.Context(), auth.Selection, h.agentActor(auth, r),
		agents.PinTarget{AgentID: input.AgentID, CrewID: input.CrewID}, *input.Pinned); err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pinned": *input.Pinned})
}

func (h *Handler) createCrew(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input crewCreateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).CreateCrew(r.Context(), auth.Selection, h.agentActor(auth, r),
		r.PathValue("spaceID"), agents.CrewInput{
			Name: input.Name, Description: input.Description, Visibility: input.Visibility,
			LeaderAgentID: input.LeaderAgentID, AgentIDs: input.AgentIDs, UserIDs: input.UserIDs,
		})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateCrew(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	var input crewUpdateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).UpdateCrew(r.Context(), auth.Selection, h.agentActor(auth, r),
		r.PathValue("spaceID"), r.PathValue("crewID"), agents.CrewPatch{
			Name: input.Name, Description: input.Description, Visibility: input.Visibility, SpaceID: input.SpaceID,
			OwnerUserID: input.OwnerUserID, Instructions: input.Instructions, SharedTaskDir: input.SharedTaskDir,
			LeaderAgentID: input.LeaderAgentID, Archived: input.Archived, Version: input.Version,
		})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
