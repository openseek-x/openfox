package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
)

// 「Agent 与小队」接口：Agent 资料、环境变量、仓库、复制、统计、运行历史、收藏，
// 小队详情与成员，成员页签。路由前缀 /v1/organizations/{organizationID}。
func (h *Handler) registerAgentExtRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	const agentPath = "/v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}"
	mux.HandleFunc("PATCH "+agentPath+"/profile", h.updateAgentProfile)
	mux.HandleFunc("POST "+agentPath+"/copy", h.copyAgent)
	mux.HandleFunc("GET "+agentPath+"/stats", h.getAgentStats)
	mux.HandleFunc("GET "+agentPath+"/runs", h.listAgentRuns)
	mux.HandleFunc("GET "+agentPath+"/env-vars", h.listAgentEnvVars)
	mux.HandleFunc("PUT "+agentPath+"/env-vars/{name}", h.putAgentEnvVar)
	mux.HandleFunc("DELETE "+agentPath+"/env-vars/{name}", h.deleteAgentEnvVar)
	mux.HandleFunc("GET "+agentPath+"/repos", h.listAgentRepos)
	mux.HandleFunc("POST "+agentPath+"/repos", h.createAgentRepo)
	mux.HandleFunc("PATCH "+agentPath+"/repos/{repoID}", h.updateAgentRepo)
	mux.HandleFunc("DELETE "+agentPath+"/repos/{repoID}", h.deleteAgentRepo)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/agent-favorites", h.addAgentFavorite)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/agent-favorites", h.removeAgentFavorite)
	const crewPath = "/v1/organizations/{organizationID}/spaces/{spaceID}/crews/{crewID}"
	mux.HandleFunc("GET "+crewPath, h.getCrew)
	mux.HandleFunc("POST "+crewPath+"/members", h.addCrewMembers)
	mux.HandleFunc("PATCH "+crewPath+"/members/agents/{agentID}", h.updateCrewAgentMember)
	mux.HandleFunc("DELETE "+crewPath+"/members/{kind}/{memberID}", h.removeCrewMember)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/contacts", h.listContacts)
}

// agentStore 带上主密钥（环境变量的加解密、复制与 env_digest 需要）。
func (h *Handler) agentStore() agents.PostgresStore {
	return agents.PostgresStore{Pool: h.pool, Secrets: h.secrets}
}

// beginAgentRequest 统一认证、CSRF 与数据库就绪检查。
func (h *Handler) beginAgentRequest(w http.ResponseWriter, r *http.Request, mutation bool) (agentAuth, bool) {
	auth, ok := h.authenticateAgent(w, r, mutation)
	if !ok {
		return agentAuth{}, false
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return agentAuth{}, false
	}
	return auth, true
}

type agentProfileRequest struct {
	Name            *string                   `json:"name"`
	Title           *string                   `json:"title"`
	Description     *string                   `json:"description"`
	Avatar          collaboration.OptionalRaw `json:"avatar"`
	AllowAssign     *bool                     `json:"allow_assign"`
	AllowDirectChat *bool                     `json:"allow_direct_chat"`
	TriggerCrew     *bool                     `json:"trigger_crew"`
	TriggerMention  *bool                     `json:"trigger_mention"`
	TriggerWorkflow *bool                     `json:"trigger_workflow"`
	SpaceID         *string                   `json:"space_id"`
	Visibility      *agents.Visibility        `json:"visibility"`
	OwnerUserID     *string                   `json:"owner_user_id"`
	ProfileVersion  uint64                    `json:"profile_version"`
}

func (h *Handler) updateAgentProfile(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input agentProfileRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().UpdateProfile(r.Context(), auth.Selection, h.agentActor(auth, r),
		r.PathValue("spaceID"), r.PathValue("agentID"), agents.ProfileInput{
			Name: input.Name, Title: input.Title, Description: input.Description, Avatar: input.Avatar,
			AllowAssign: input.AllowAssign, AllowDirectChat: input.AllowDirectChat, TriggerCrew: input.TriggerCrew,
			TriggerMention: input.TriggerMention, TriggerWorkflow: input.TriggerWorkflow, SpaceID: input.SpaceID,
			Visibility: input.Visibility, OwnerUserID: input.OwnerUserID, ProfileVersion: input.ProfileVersion,
		})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) copyAgent(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Name *string `json:"name"`
	}
	// 请求体可省略（空请求体按默认名称复制）。
	if err := decodeJSON(w, r, &input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if !idempotency.ValidKey(key) {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid")
		return
	}
	item, replay, err := h.agentStore().CopyAgent(r.Context(), auth.Selection, h.agentActor(auth, r),
		r.PathValue("spaceID"), r.PathValue("agentID"), input.Name, key)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeCreated(w, replay, item)
}

func (h *Handler) getAgentStats(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	item, err := h.agentStore().Stats(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("agentID"))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) listAgentRuns(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit is invalid")
			return
		}
		limit = parsed
	}
	page, err := h.agentStore().Runs(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("agentID"), limit, strings.TrimSpace(r.URL.Query().Get("cursor")))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) listAgentEnvVars(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	result, err := h.agentStore().ListEnvVars(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("agentID"))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) putAgentEnvVar(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Value   *string `json:"value"`
		Version *uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Value == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().PutEnvVar(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("agentID"), r.PathValue("name"), *input.Value, input.Version)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) deleteAgentEnvVar(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	if err := h.agentStore().DeleteEnvVar(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("agentID"), r.PathValue("name")); err != nil {
		writeAgentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listAgentRepos(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	items, err := h.agentStore().ListRepos(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("agentID"))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": items})
}

func (h *Handler) createAgentRepo(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input agents.RepoInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if !idempotency.ValidKey(key) {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid")
		return
	}
	item, replay, err := h.agentStore().CreateRepo(r.Context(), auth.Selection, h.agentActor(auth, r),
		r.PathValue("spaceID"), r.PathValue("agentID"), input, key)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeCreated(w, replay, item)
}

func (h *Handler) updateAgentRepo(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ConnectionID *string `json:"connection_id"`
		URL          *string `json:"url"`
		BaseBranch   *string `json:"base_branch"`
		Position     *int    `json:"position"`
		Version      uint64  `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().UpdateRepo(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("agentID"), r.PathValue("repoID"), agents.RepoPatch{
			ConnectionID: input.ConnectionID, URL: input.URL, BaseBranch: input.BaseBranch, Position: input.Position, Version: input.Version,
		})
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) deleteAgentRepo(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	if err := h.agentStore().DeleteRepo(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("agentID"), r.PathValue("repoID")); err != nil {
		writeAgentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) addAgentFavorite(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		AgentID string `json:"agent_id"`
		CrewID  string `json:"crew_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if err := h.agentStore().AddFavorite(r.Context(), auth.Selection, h.agentActor(auth, r),
		agents.PinTarget{AgentID: input.AgentID, CrewID: input.CrewID}); err != nil {
		writeAgentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeAgentFavorite(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	query := r.URL.Query()
	if err := h.agentStore().RemoveFavorite(r.Context(), auth.Selection, h.agentActor(auth, r),
		agents.PinTarget{AgentID: query.Get("agent_id"), CrewID: query.Get("crew_id")}); err != nil {
		writeAgentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getCrew(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	item, err := h.agentStore().GetCrew(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("crewID"))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) addCrewMembers(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		AgentIDs []string `json:"agent_ids"`
		UserIDs  []string `json:"user_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().AddCrewMembers(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("crewID"), input.AgentIDs, input.UserIDs)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) updateCrewAgentMember(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Exclusive *bool `json:"exclusive"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Exclusive == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().SetCrewMemberExclusive(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("crewID"), r.PathValue("agentID"), *input.Exclusive)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) removeCrewMember(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	if err := h.agentStore().RemoveCrewMember(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"),
		r.PathValue("crewID"), r.PathValue("kind"), r.PathValue("memberID")); err != nil {
		writeAgentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, false)
	if !ok {
		return
	}
	result, err := h.agentStore().Contacts(r.Context(), auth.Selection, h.agentActor(auth, r))
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
