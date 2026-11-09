package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/identity"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

func (h *Handler) registerConversationRoutes(mux *http.ServeMux) {
	const offers = "/v1/organizations/{organizationID}"
	mux.HandleFunc("GET "+offers+"/conversations/{conversationID}/offer-candidates", h.conversationOffers)
	mux.HandleFunc("POST "+offers+"/conversations/{conversationID}/offers", h.conversationOffers)
	mux.HandleFunc("GET "+offers+"/conversation-offers", h.conversationOffers)
	mux.HandleFunc("GET "+offers+"/conversation-workspace-options", h.conversationWorkspaceOptions)
	mux.HandleFunc("PUT "+offers+"/conversation-offers/{offerID}", h.conversationOffers)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations", h.listConversations)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations", h.createConversation)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}", h.getConversation)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/work-directory", h.conversationWorkDirectory)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/conversations/{conversationID}", h.updateConversation)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/conversations/{conversationID}/overrides", h.updateConversationOverrides)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/override-options", h.getConversationOverrideOptions)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/conversations/{conversationID}/pin", h.pinConversation)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/conversations/{conversationID}/pin", h.unpinConversation)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/read", h.readConversation)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/task", h.bindConversationTask)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/convert-to-task", h.convertConversationToTask)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/conversations/{conversationID}/messages", h.listMessages)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/messages", h.postMessage)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/conversations/{conversationID}/members/{userID}", h.addConversationMember)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/conversations/{conversationID}/members/{userID}", h.removeConversationMember)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/complete", h.completeConversation)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/reopen", h.reopenConversation)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/handoff", h.handoffConversation)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/spaces/{spaceID}/tasks/{taskID}/comments", h.listTaskComments)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/spaces/{spaceID}/tasks/{taskID}/comments", h.postTaskComment)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/notifications", h.listNotifications)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/notifications/{notificationID}/read", h.readNotification)
}

type conversationRequest struct {
	WorkspaceSelection *conversations.WorkspaceSelection `json:"workspace_selection"`
	ID                 string                            `json:"id"`
	SpaceID            string                            `json:"space_id"`
	AgentID            string                            `json:"agent_id"`
	TaskID             string                            `json:"task_id"`
	CrewID             string                            `json:"crew_id"`
	Visibility         string                            `json:"visibility"`
}

type messageRequest struct {
	AssistantAction string                  `json:"assistant_action"`
	ID              string                  `json:"id"`
	Body            string                  `json:"body"`
	ClientMessageID string                  `json:"client_message_id"`
	ReplyToID       string                  `json:"reply_to_id"`
	Mentions        []conversations.Mention `json:"mentions"`
	AttachmentIDs   []string                `json:"attachment_ids"`
}

type commentRequest struct {
	ID              string                  `json:"id"`
	Body            string                  `json:"body"`
	ClientCommentID string                  `json:"client_comment_id"`
	ReplyToID       string                  `json:"reply_to_id"`
	Mentions        []conversations.Mention `json:"mentions"`
}

type conversationMemberRequest struct {
	AccessLevel string `json:"access_level"`
}

type conversationVersionRequest struct {
	Version      uint64 `json:"version"`
	CompleteTask bool   `json:"complete_task"`
}

type handoffRequest struct {
	Version uint64 `json:"version"`
	Handoff string `json:"handoff"`
}

func (h *Handler) listConversations(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	limit := parseConversationLimit(r)
	values := r.URL.Query()
	pinned := strings.TrimSpace(values.Get("pinned"))
	if limit == 0 || (pinned != "" && pinned != "only") {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	query := conversations.ListQuery{
		Limit: limit, Cursor: values.Get("cursor"), AgentID: values.Get("agent_id"), TaskID: values.Get("task_id"),
		Archived: values.Get("archived"), PinnedOnly: pinned == "only", Q: values.Get("q"),
	}
	var page conversations.SummaryPage
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = (conversations.Service{}).ListSummaries(ctx, tx, selection.OrganizationID, actor, query)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	var next any
	if page.NextCursor != "" {
		next = page.NextCursor
	}
	body := map[string]any{"conversations": page.Items, "next_cursor": next}
	if page.TaskStats != nil {
		body["task_stats"] = page.TaskStats
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *Handler) createConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	if input.ID == "" {
		var err error
		input.ID, err = newUUID()
		if err != nil {
			writeConversationError(w, r, err)
			return
		}
	}
	var result conversations.Conversation
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).Create(ctx, tx, selection.OrganizationID, actor, conversations.CreateInput{
			WorkspaceSelection: input.WorkspaceSelection, ID: input.ID, SpaceID: input.SpaceID, AgentID: input.AgentID, TaskID: input.TaskID, CrewID: input.CrewID, Visibility: input.Visibility,
		})
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.created", "conversation", result.ID, result.SpaceID, nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) conversationWorkspaceOptions(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	var result conversations.WorkspaceOptions
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).WorkspaceOptions(ctx, tx, selection.OrganizationID, conversationActor(auth, role, selection.AuthorizationVersion), r.URL.Query().Get("agent_id"), r.URL.Query().Get("source_conversation_id"))
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	var result conversations.Summary
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).GetSummary(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	query := conversations.MessageQuery{Limit: parseConversationLimit(r), Around: strings.TrimSpace(values.Get("around"))}
	switch strings.TrimSpace(values.Get("order")) {
	case "", "asc":
	case "desc":
		query.Backward = true
	default:
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	for name, target := range map[string]*int64{"after": &query.After, "before": &query.Before} {
		if raw := strings.TrimSpace(values.Get(name)); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeConversationError(w, r, conversations.ErrInvalidRequest)
				return
			}
			*target = value
		}
	}
	var page conversations.Page[conversations.Message]
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = (conversations.Service{}).ListMessagePage(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, query)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": page.Items, "next_cursor": page.NextCursor})
}

func (h *Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input messageRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	if input.ID == "" {
		var err error
		input.ID, err = newUUID()
		if err != nil {
			writeConversationError(w, r, err)
			return
		}
	}
	var result conversations.Message
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).PostMessage(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, conversations.MessageInput{
			ID: input.ID, Body: input.Body, ClientMessageID: input.ClientMessageID, ReplyToID: input.ReplyToID, Mentions: input.Mentions,
			AttachmentIDs: input.AttachmentIDs, AssistantAction: input.AssistantAction,
			AuthenticatedUserInput: auth.principal.SessionID != "" && auth.principal.TokenID == "",
		})
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.message_created", "message", result.ID, "", nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// updateConversationOverrides 修改对话的 Runtime / 模型 / 权限模式覆盖。
// 鉴权与发消息相同（conversations.Service.AuthorizeWrite）；请求体没有电脑 / 授权字段，未知字段一律 400。
func (h *Handler) updateConversationOverrides(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversations.OverridesPatch
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	var result conversations.Summary
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).UpdateOverrides(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, input)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.overrides_changed", "conversation", result.ID, result.SpaceID,
			map[string]string{"version": strconv.FormatInt(result.Overrides.Version, 10)})
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// getConversationOverrideOptions 返回对话输入框可选的 Runtime / 模型 / 权限档位，只读。
// 鉴权与修改覆盖相同（AuthorizeWrite）：能聊天但看不到 Agent 绑定电脑的成员也能拿到可选项。
func (h *Handler) getConversationOverrideOptions(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	var result conversations.OverrideOptions
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).OverrideOptions(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) addConversationMember(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationMemberRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := (conversations.Service{}).AddMember(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, r.PathValue("userID"), conversations.MemberInput{AccessLevel: strings.ToUpper(strings.TrimSpace(input.AccessLevel))}); err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.member_added", "conversation_member", r.PathValue("userID"), "", nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeConversationMember(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := (conversations.Service{}).RemoveMember(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, r.PathValue("userID")); err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.member_removed", "conversation_member", r.PathValue("userID"), "", nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) completeConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationVersionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	var result conversations.Conversation
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).Complete(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, input.Version, input.CompleteTask)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.completed", "conversation", result.ID, result.SpaceID,
			map[string]string{"status": result.Lifecycle, "version": strconv.FormatUint(result.Version, 10)})
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) reopenConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationVersionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	var result conversations.Conversation
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).Reopen(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, input.Version)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.reopened", "conversation", result.ID, result.SpaceID,
			map[string]string{"status": result.Lifecycle, "version": strconv.FormatUint(result.Version, 10)})
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handoffConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input handoffRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	var result conversations.Conversation
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).SetHandoff(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, input.Handoff, input.Version)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.handoff_changed", "conversation", result.ID, result.SpaceID,
			map[string]string{"status": result.Handoff, "version": strconv.FormatUint(result.Version, 10)})
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listTaskComments(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	limit := parseConversationLimit(r)
	var after time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		nanos, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeConversationError(w, r, conversations.ErrInvalidRequest)
			return
		}
		after = time.Unix(0, nanos).UTC()
	}
	var page conversations.Page[conversations.Comment]
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = (conversations.Service{}).ListComments(ctx, tx, selection.OrganizationID, r.PathValue("spaceID"), r.PathValue("taskID"), actor, limit, after)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": page.Items, "next_cursor": page.NextCursor})
}

func (h *Handler) postTaskComment(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input commentRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	if input.ID == "" {
		var err error
		input.ID, err = newUUID()
		if err != nil {
			writeConversationError(w, r, err)
			return
		}
	}
	var result conversations.Comment
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).PostComment(ctx, tx, selection.OrganizationID, r.PathValue("spaceID"), r.PathValue("taskID"), actor, conversations.CommentInput{
			ID: input.ID, Body: input.Body, ClientCommentID: input.ClientCommentID, ReplyToID: input.ReplyToID, Mentions: input.Mentions,
		})
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"task.comment_created", "task_comment", result.ID, result.SpaceID, nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) listNotifications(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	limit := parseConversationLimit(r)
	var items []conversations.Notification
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = (conversations.Service{}).ListNotifications(ctx, tx, selection.OrganizationID, actor, limit)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
}

func (h *Handler) readNotification(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := (conversations.Service{}).MarkNotificationRead(ctx, tx, selection.OrganizationID, r.PathValue("notificationID"), actor); err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"notification.read", "notification", r.PathValue("notificationID"), "", nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) conversationAuth(w http.ResponseWriter, r *http.Request) (authContext, tenancy.OrganizationSelection, string, bool) {
	auth, err := h.authenticate(r)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return authContext{}, tenancy.OrganizationSelection{}, "", false
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return authContext{}, tenancy.OrganizationSelection{}, "", false
	}
	selection, role, err := h.auth.SelectOrganization(r.Context(), auth.principal.UserID, r.PathValue("organizationID"))
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return authContext{}, tenancy.OrganizationSelection{}, "", false
	}
	return auth, selection, role, true
}

func (h *Handler) runConversationTransaction(r *http.Request, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx) error) error {
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return h.pool.RunForOrganization(r.Context(), selection.OrganizationID, tenancy.SourceUserRequest, selector, fn)
}

func conversationActor(auth authContext, role string, authorizationVersion uint64) conversations.Actor {
	return conversations.Actor{UserID: auth.principal.UserID, Role: organizationRole(role), AuthorizationVersion: authorizationVersion}
}

func requireConversationCSRF(w http.ResponseWriter, r *http.Request, auth authContext) bool {
	if auth.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return false
	}
	return true
}

func recordConversationAudit(ctx context.Context, tx pgx.Tx, requestID, organizationID string, actor conversations.Actor,
	action, resourceKind, resourceID, spaceID string, metadata map[string]string) error {
	eventID, err := newUUID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actor.UserID,
		Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: resourceKind, OrganizationID: organizationID, SpaceID: spaceID, ID: resourceID},
		RequestID: requestID, AuthorizationVersion: actor.AuthorizationVersion,
		SafeMetadata: metadata, CreatedAt: time.Now().UTC(),
	})
}

func parseConversationLimit(r *http.Request) int {
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return 50
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > conversations.MaxPageSize {
		return 0
	}
	return limit
}

func writeConversationError(w http.ResponseWriter, r *http.Request, err error) {
	var rule *collaboration.RuleError
	if errors.As(err, &rule) {
		writeError(w, r, rule.Status, rule.Code, rule.Message)
		return
	}
	if errors.Is(err, membership.ErrAuthorizationStale) {
		writeError(w, r, http.StatusUnauthorized, "AUTHORIZATION_STALE", "authorization must be refreshed")
		return
	}
	switch {
	case errors.Is(err, conversations.ErrAssistantTransferNotReady):
		writeError(w, r, http.StatusConflict, "ASSISTANT_TRANSFER_NOT_READY", "the recipient must configure their own assistant with an active device grant covering this conversation space")
	case errors.Is(err, conversations.ErrAssistantTransferBusy):
		writeError(w, r, http.StatusConflict, "ASSISTANT_TRANSFER_BUSY", "finish pending inputs, runs and reconciliation before accepting the transfer")
	case errors.Is(err, conversations.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "conversation resource is not available")
	case errors.Is(err, conversations.ErrInviteRequired):
		writeError(w, r, http.StatusConflict, "CONVERSATION_INVITE_REQUIRED", "send an invitation to add a new task conversation member; the recipient must accept it")
	case errors.Is(err, conversations.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "ACTION_FORBIDDEN", "action is not allowed")
	case errors.Is(err, conversations.ErrInvalidRequest), errors.Is(err, conversations.ErrInvalidMention):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "conversation request is invalid")
	case errors.Is(err, conversations.ErrVersionConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "conversation version is stale")
	case errors.Is(err, conversations.ErrRunActive):
		writeError(w, r, http.StatusConflict, "RUN_ACTIVE", "conversation has an active run")
	case errors.Is(err, conversations.ErrTaskBound):
		writeError(w, r, http.StatusConflict, "CONVERSATION_TASK_BOUND", "conversation is already bound to a task")
	case errors.Is(err, conversations.ErrTaskSpaceMismatch):
		writeError(w, r, http.StatusBadRequest, "TASK_SPACE_MISMATCH", "task is outside the conversation space")
	case errors.Is(err, conversations.ErrQuickReplyLimit):
		writeError(w, r, http.StatusConflict, "QUICK_REPLY_LIMIT", "quick reply limit reached")
	case errors.Is(err, conversations.ErrQuickCommandLimit):
		writeError(w, r, http.StatusConflict, "QUICK_COMMAND_LIMIT", "quick command limit reached")
	case errors.Is(err, conversations.ErrQuickCommandTarget):
		writeError(w, r, http.StatusBadRequest, "INVALID_QUICK_COMMAND_TARGET", "the agent or crew is not available in this space")
	case errors.Is(err, conversations.ErrCrewConversation):
		writeError(w, r, http.StatusConflict, "CREW_CONVERSATION_UNSUPPORTED", "overrides are not available for crew conversations")
	case errors.Is(err, conversations.ErrWorkspaceUnavailable):
		writeError(w, r, http.StatusConflict, "WORKSPACE_UNAVAILABLE", "the working directory is unavailable for this conversation")
	case errors.Is(err, conversations.ErrOverrideAgentUnavailable):
		writeError(w, r, http.StatusConflict, "AGENT_UNAVAILABLE", "the agent has no usable configuration")
	case errors.Is(err, conversations.ErrRuntimeNotAvailable):
		writeError(w, r, http.StatusUnprocessableEntity, "RUNTIME_NOT_AVAILABLE", "the runtime is not reported by the agent's computer")
	case errors.Is(err, conversations.ErrModelNotAvailable):
		writeError(w, r, http.StatusUnprocessableEntity, "MODEL_NOT_AVAILABLE", "the model is not available on the runtime")
	case errors.Is(err, conversations.ErrReasoningEffortNotAvailable):
		writeError(w, r, http.StatusUnprocessableEntity, "REASONING_EFFORT_NOT_AVAILABLE", "the reasoning depth is not available on the runtime")
	case errors.Is(err, conversations.ErrConversationClosed):
		writeError(w, r, http.StatusConflict, "CONVERSATION_CLOSED", "conversation is not open")
	case errors.Is(err, conversations.ErrDuplicateMessage), errors.Is(err, conversations.ErrConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "the client id was used with different content")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}
