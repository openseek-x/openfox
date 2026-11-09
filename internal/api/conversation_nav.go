package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/idempotency"
)

// 对话导航接口：重命名 / 归档、个人置顶与已读、绑定任务、转为任务。

type conversationUpdateRequest struct {
	// Title 缺省表示不改；null 或空串表示清除标题。
	Title      json.RawMessage `json:"title"`
	Archived   *bool           `json:"archived"`
	StatusMark *string         `json:"status_mark"`
}

type conversationReadRequest struct {
	Seq *int64 `json:"seq"`
}

type conversationTaskRequest struct {
	TaskID string `json:"task_id"`
}

type conversationConvertRequest struct {
	Title  string `json:"title"`
	TypeID string `json:"type_id"`
}

func (h *Handler) updateConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationUpdateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	update := conversations.ConversationUpdate{Archived: input.Archived, StatusMark: input.StatusMark}
	if raw := bytes.TrimSpace(input.Title); len(raw) > 0 {
		if bytes.Equal(raw, []byte("null")) {
			update.ClearTitle = true
		} else {
			var title string
			if err := json.Unmarshal(raw, &title); err != nil {
				writeConversationError(w, r, conversations.ErrInvalidRequest)
				return
			}
			update.Title = &title
		}
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var result conversations.Summary
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).UpdateConversation(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, update)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.session_updated", "conversation", result.ID, result.SpaceID, nil)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) pinConversation(w http.ResponseWriter, r *http.Request) {
	h.setConversationPinned(w, r, true)
}

func (h *Handler) unpinConversation(w http.ResponseWriter, r *http.Request) {
	h.setConversationPinned(w, r, false)
}

// setConversationPinned 是个人偏好，不写审计（与收藏 / 置顶 Agent 一致），也不改会话 version。
func (h *Handler) setConversationPinned(w http.ResponseWriter, r *http.Request, pinned bool) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		return (conversations.Service{}).SetPinned(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, pinned)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readConversation 推进个人已读位置（只增不减）。调用频繁，不写审计。
func (h *Handler) readConversation(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationReadRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Seq == nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		return (conversations.Service{}).MarkRead(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, *input.Seq)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) bindConversationTask(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationTaskRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var result conversations.Summary
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (conversations.Service{}).BindTask(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor, input.TaskID)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.task_bound", "conversation", result.ID, result.SpaceID, versionMetadata(result.Version))
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// convertConversationToTask 创建任务并绑定。可选 Idempotency-Key（操作 conversation.convert_task）：
// 同键同请求重放返回会话当前摘要（200 + Idempotent-Replayed）；不带键时重复请求得到 409 CONVERSATION_TASK_BOUND。
func (h *Handler) convertConversationToTask(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input conversationConvertRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	taskID, err := collaboration.NewID()
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	conversationID := r.PathValue("conversationID")
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	service := conversations.Service{}
	var result conversations.Summary
	replayed := false
	err = h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		claim, err := idempotency.Begin(ctx, tx, "conversation.convert_task", key, struct {
			ConversationID string                     `json:"conversation_id"`
			Body           conversationConvertRequest `json:"body"`
		}{conversationID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			result, err = service.GetSummary(ctx, tx, selection.OrganizationID, conversationID, actor)
			return err
		}
		var task collaboration.Task
		result, task, err = service.ConvertToTask(ctx, tx, selection.OrganizationID, conversationID, actor, conversations.ConvertInput{
			TaskID: taskID, Title: input.Title, TypeID: input.TypeID, RequestID: requestID,
		})
		if err != nil {
			return err
		}
		if err := recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"conversation.converted_to_task", "conversation", result.ID, result.SpaceID, versionMetadata(result.Version)); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, task.ID)
	})
	if err != nil {
		writeConversationTaskError(w, r, err)
		return
	}
	writeCreated(w, replayed, result)
}

// writeConversationTaskError：会话类错误按 writeConversationError，其余（任务创建、幂等）按协作错误映射。
func writeConversationTaskError(w http.ResponseWriter, r *http.Request, err error) {
	for _, target := range []error{conversations.ErrNotFound, conversations.ErrForbidden, conversations.ErrInvalidRequest,
		conversations.ErrRunActive, conversations.ErrTaskBound, conversations.ErrTaskSpaceMismatch, conversations.ErrConflict} {
		if errors.Is(err, target) {
			writeConversationError(w, r, err)
			return
		}
	}
	writeCollaborationError(w, r, err)
}
