package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
)

// 快捷回复（个人 + 组织两级）。组织级的新建 / 编辑 / 删除 / 排序写审计（只记 ID 与级别，不记正文）。

func (h *Handler) registerQuickReplyRoutes(mux *http.ServeMux) {
	const base = "/v1/organizations/{organizationID}/quick-replies"
	mux.HandleFunc("GET "+base, h.listQuickReplies)
	mux.HandleFunc("POST "+base, h.createQuickReply)
	mux.HandleFunc("PUT "+base+"/order", h.reorderQuickReplies)
	mux.HandleFunc("PATCH "+base+"/{quickReplyID}", h.updateQuickReply)
	mux.HandleFunc("DELETE "+base+"/{quickReplyID}", h.deleteQuickReply)
}

type quickReplyRequest struct {
	ID      string  `json:"id"`
	Scope   string  `json:"scope"`
	Title   *string `json:"title"`
	Body    *string `json:"body"`
	Version uint64  `json:"version"`
}

type quickReplyOrderRequest struct {
	Scope string   `json:"scope"`
	IDs   []string `json:"ids"`
}

func (h *Handler) listQuickReplies(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var list conversations.QuickReplyList
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		list, err = (conversations.Service{}).ListQuickReplies(ctx, tx, selection.OrganizationID, actor)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) createQuickReply(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input quickReplyRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Title == nil || input.Body == nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	if input.ID == "" {
		id, err := newUUID()
		if err != nil {
			writeConversationError(w, r, err)
			return
		}
		input.ID = id
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var item conversations.QuickReply
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = (conversations.Service{}).CreateQuickReply(ctx, tx, selection.OrganizationID, actor, conversations.QuickReplyInput{
			ID: input.ID, Scope: input.Scope, Title: *input.Title, Body: *input.Body,
		})
		if err != nil {
			return err
		}
		return auditQuickReply(ctx, tx, requestID, selection.OrganizationID, actor, "quick_reply.created", item.Scope, item.ID)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateQuickReply(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input quickReplyRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var item conversations.QuickReply
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = (conversations.Service{}).UpdateQuickReply(ctx, tx, selection.OrganizationID, actor, r.PathValue("quickReplyID"),
			conversations.QuickReplyPatch{Version: input.Version, Title: input.Title, Body: input.Body})
		if err != nil {
			return err
		}
		return auditQuickReply(ctx, tx, requestID, selection.OrganizationID, actor, "quick_reply.updated", item.Scope, item.ID)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) deleteQuickReply(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		item, err := (conversations.Service{}).DeleteQuickReply(ctx, tx, selection.OrganizationID, actor, r.PathValue("quickReplyID"))
		if err != nil {
			return err
		}
		return auditQuickReply(ctx, tx, requestID, selection.OrganizationID, actor, "quick_reply.deleted", item.Scope, item.ID)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorderQuickReplies(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input quickReplyOrderRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var items []conversations.QuickReply
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = (conversations.Service{}).ReorderQuickReplies(ctx, tx, selection.OrganizationID, actor, input.Scope, input.IDs)
		if err != nil {
			return err
		}
		return auditQuickReply(ctx, tx, requestID, selection.OrganizationID, actor, "quick_reply.reordered", input.Scope, selection.OrganizationID)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quick_replies": items})
}

// auditQuickReply 只为组织级快捷回复写审计；个人级是本人私有数据，不写。
func auditQuickReply(ctx context.Context, tx pgx.Tx, requestID, organizationID string, actor conversations.Actor, action, scope, resourceID string) error {
	if scope != conversations.QuickReplyOrg {
		return nil
	}
	return recordConversationAudit(ctx, tx, requestID, organizationID, actor, action, "quick_reply", resourceID, "", map[string]string{"resource_kind": "org_quick_reply"})
}
