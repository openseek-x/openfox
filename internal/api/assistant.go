package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 首页「小助手」接口：PUT /agents/assistant、会话选择器、草稿卡确认 / 取消。
func (h *Handler) registerAssistantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/assistant/work-directory", h.assistantDirectory)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/assistant/work-directory", h.assistantDirectory)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/conversation-workspace-recents", h.forgetWorkspaceRecent)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/assistant", h.getAssistant)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/assistant", h.ensureAssistant)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/assistant/sessions", h.listAssistantSessions)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/conversations/{conversationID}/session", h.updateAssistantSession)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/runs/{runID}/retry", h.retryAssistantRun)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/messages/{messageID}/confirm", h.confirmProposal)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/conversations/{conversationID}/messages/{messageID}/proposal", h.cancelProposal)
}

func (h *Handler) retryAssistantRun(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		return (conversations.Service{}).RetryAssistantRun(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("runID"), actor)
	})
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type assistantSessionRequest struct {
	Title    *string `json:"title"`
	Archived *bool   `json:"archived"`
}

// proposalConfirmRequest 是用户在草稿卡上编辑后的字段；按草稿的 action 取用对应字段。
type proposalConfirmRequest struct {
	SpaceID     string `json:"space_id"`
	Description string `json:"description"`
	// create_agent
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	ComputerID   string `json:"computer_id"`
	GrantID      string `json:"grant_id"`
	Runtime      string `json:"runtime"`
	Model        string `json:"model"`
	Visibility   string `json:"visibility"`
	// create_task
	StartWorking    *bool  `json:"start_working"`
	Title           string `json:"title"`
	AssigneeUserID  string `json:"assignee_user_id"`
	ExecutorAgentID string `json:"executor_agent_id"`
}

func (h *Handler) getAssistant(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, false)
	if !ok {
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).GetAssistant(r.Context(), auth.Selection, h.agentActor(auth, r))
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assistant": item})
}

func (h *Handler) ensureAssistant(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	var input agents.AssistantInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := (agents.PostgresStore{Pool: h.pool}).EnsureAssistant(r.Context(), auth.Selection, h.agentActor(auth, r), input)
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assistant": item})
}

func (h *Handler) listAssistantSessions(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var items []conversations.AssistantSession
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = (conversations.Service{}).ListAssistantSessions(ctx, tx, selection.OrganizationID, actor)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (h *Handler) updateAssistantSession(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input assistantSessionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	conversationID := r.PathValue("conversationID")
	var item conversations.AssistantSession
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = (conversations.Service{}).UpdateAssistantSession(ctx, tx, selection.OrganizationID, conversationID, actor,
			conversations.SessionUpdate{Title: input.Title, Archived: input.Archived})
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.session_updated", "conversation", item.ID, item.SpaceID, nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// confirmProposal 按用户编辑后的字段执行草稿：创建 Agent 或任务、标记草稿已确认、追加执行结果，
// 全部在一个事务里，用当前成员自己的权限。重复确认返回 409 PROPOSAL_RESOLVED。
func (h *Handler) confirmProposal(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.authenticateAgent(w, r, true)
	if !ok {
		return
	}
	var input proposalConfirmRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	organizationID := auth.Selection.OrganizationID
	conversationActorValue := conversations.Actor{UserID: auth.Actor.UserID, Role: auth.Actor.Role, AuthorizationVersion: auth.Actor.AuthorizationVersion}
	agentActorValue := h.agentActor(auth, r)
	collab := collaborationAuth{Auth: auth.Auth, Selection: auth.Selection, Actor: auth.Actor}
	requestID := collaborationRequestID(w, r)
	service := conversations.Service{}
	var proposalMessage, executedMessage conversations.Message
	err := h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(auth.Selection), func(ctx context.Context, tx pgx.Tx) error {
			proposal, err := service.LockProposal(ctx, tx, organizationID, r.PathValue("conversationID"), r.PathValue("messageID"), conversationActorValue)
			if err != nil {
				return err
			}
			executed := conversations.ExecutedPayload{Action: proposal.Payload.Action, ProposalID: proposal.MessageID, SpaceID: input.SpaceID}
			switch proposal.Payload.Action {
			case conversations.ActionCreateAgent:
				visibility := agents.Visibility(input.Visibility)
				if visibility == "" {
					visibility = agents.VisibilityPrivate
				}
				created, err := agents.CreateInTx(ctx, tx, auth.Selection, agentActorValue, input.SpaceID, agents.CreateInput{
					Name: input.Name, Description: input.Description, Kind: agents.KindCode, Visibility: visibility,
					Config: agents.ConfigInput{
						Instructions: input.Instructions, ComputerID: input.ComputerID, GrantID: input.GrantID,
						Runtime: defaultValue(input.Runtime, "codex"), Model: defaultValue(input.Model, "default"),
					},
				})
				if err != nil {
					return err
				}
				proposal.Payload.Agent = &conversations.AgentDraft{Name: created.Name, Description: created.Description, Instructions: input.Instructions}
				executed.SpaceID, executed.AgentID, executed.Name = created.SpaceID, created.ID, created.Name
			case conversations.ActionCreateTask:
				taskID, err := collaboration.NewID()
				if err != nil {
					return err
				}
				collabService := collaboration.Service{}
				access, err := collabService.LoadSpaceAccess(ctx, tx, organizationID, input.SpaceID, auth.Actor.UserID)
				if err != nil {
					return err
				}
				decision := collaborationSpaceDecision(policy.ActionCreateObject, organizationID, collab, access, taskID)
				if !decision.Allowed() {
					return decisionError{decision: decision}
				}
				task, err := collabService.CreateTask(ctx, tx, organizationID, input.SpaceID, auth.Actor, collaboration.TaskInput{
					ID: taskID, Title: input.Title, Description: input.Description, RequestID: requestID,
					DeferAssignedAgent: input.StartWorking != nil && !*input.StartWorking,
					AssigneeUserID:     input.AssigneeUserID, ExecutorAgentID: input.ExecutorAgentID,
				})
				if err != nil {
					return err
				}
				proposal.Payload.Task = &conversations.TaskDraft{Title: task.Title, Description: task.Description}
				executed.SpaceID, executed.TaskID, executed.Identifier, executed.Name = task.SpaceID, task.ID, task.DisplayID, task.Title
			default:
				return conversations.ErrProposalNotFound
			}
			executedMessage, err = service.ResolveProposal(ctx, tx, proposal, conversations.ProposalConfirmed, &executed)
			if err != nil {
				return err
			}
			proposalMessage, err = service.LoadMessage(ctx, tx, proposal.MessageID)
			if err != nil {
				return err
			}
			return recordConversationAudit(ctx, tx, requestID, organizationID, conversationActorValue,
				"conversation.proposal_confirmed", "message", proposal.MessageID, "", map[string]string{"resource_kind": proposalResourceKind(proposal.Payload.Action)})
		})
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": proposalMessage, "executed": executedMessage})
}

func (h *Handler) cancelProposal(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	service := conversations.Service{}
	var result conversations.Message
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		proposal, err := service.LockProposal(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), r.PathValue("messageID"), actor)
		if err != nil {
			return err
		}
		result, err = service.ResolveProposal(ctx, tx, proposal, conversations.ProposalCancelled, nil)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.proposal_cancelled", "message", proposal.MessageID, "", map[string]string{"resource_kind": proposalResourceKind(proposal.Payload.Action)})
	})
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposal": result})
}

// proposalResourceKind 是审计里记录的草稿对象类型（SafeMetadata 只允许白名单键）。
func proposalResourceKind(action string) string {
	if action == conversations.ActionCreateTask {
		return "task"
	}
	return "agent"
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// writeAssistantError 把草稿、Agent、会话、协作四类错误映射为 HTTP 响应。
func writeAssistantError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, conversations.ErrRunNotRetryable):
		writeError(w, r, http.StatusConflict, "RUN_NOT_RETRYABLE", "only failed runs with original user messages can be retried")
	case errors.Is(err, conversations.ErrProposalNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "action proposal is not available")
	case errors.Is(err, conversations.ErrProposalResolved):
		writeError(w, r, http.StatusConflict, "PROPOSAL_RESOLVED", "action proposal was already handled")
	case errors.Is(err, agents.ErrNoAssistantSpace):
		writeError(w, r, http.StatusConflict, "NO_ASSISTANT_SPACE", "no space is available for the assistant")
	case isAgentError(err):
		writeAgentError(w, r, err)
	case isConversationError(err):
		writeConversationError(w, r, err)
	default:
		writeCollaborationError(w, r, err)
	}
}

func isAgentError(err error) bool {
	for _, target := range []error{agents.ErrNotFound, agents.ErrForbidden, agents.ErrVersionConflict, agents.ErrNameConflict,
		agents.ErrArchived, agents.ErrInvalidRequest, agents.ErrInvalidConfig, agents.ErrImmutableConfig} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func isConversationError(err error) bool {
	for _, target := range []error{membership.ErrAuthorizationStale, conversations.ErrNotFound, conversations.ErrForbidden, conversations.ErrInvalidRequest,
		conversations.ErrVersionConflict, conversations.ErrConversationClosed, conversations.ErrConflict} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func (h *Handler) assistantDirectory(w http.ResponseWriter, r *http.Request) {
	update := r.Method == http.MethodPut
	auth, ok := h.authenticateAgent(w, r, update)
	if !ok {
		return
	}
	var change *agents.AssistantDirectoryInput
	if update {
		var input struct {
			Version   uint64  `json:"version"`
			DeviceID  string  `json:"device_id"`
			Directory *string `json:"directory"`
		}
		if err := decodeJSON(w, r, &input); err != nil || input.Directory == nil || input.Version == 0 || !membership.ValidateUUID(input.DeviceID) {
			writeError(w, r, 400, "INVALID_REQUEST", "invalid directory settings")
			return
		}
		change = &agents.AssistantDirectoryInput{Version: input.Version, DeviceID: input.DeviceID, Directory: *input.Directory}
	}
	result, err := (agents.PostgresStore{Pool: h.pool}).AssistantDirectory(r.Context(), auth.Selection, h.agentActor(auth, r), change)
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

func (h *Handler) forgetWorkspaceRecent(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	agent, device := r.URL.Query().Get("agent_id"), r.URL.Query().Get("device_id")
	var input struct {
		Directory string `json:"directory"`
	}
	if !membership.ValidateUUID(agent) || !membership.ValidateUUID(device) || decodeJSON(w, r, &input) != nil || len(input.Directory) == 0 || len(input.Directory) > 1024 {
		writeError(w, r, 400, "INVALID_REQUEST", "invalid recent directory")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := (conversations.Service{}).CheckActor(ctx, tx, selection.OrganizationID, actor); err != nil {
			return err
		}
		return conversations.ForgetWorkDirectory(ctx, tx, selection.OrganizationID, actor.UserID, agent, device, input.Directory)
	})
	if err != nil {
		writeAssistantError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
