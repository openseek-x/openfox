package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
)

// 任务快捷指令（按空间，私有 + 共享两组）。涉及共享指令的新建 / 修改 / 删除 / 排序写审计
// （只记 ID、空间与可见性，不记名称和提示词）；私有指令是本人数据，不写。

func (h *Handler) registerQuickCommandRoutes(mux *http.ServeMux) {
	const base = "/v1/organizations/{organizationID}/spaces/{spaceID}/quick-commands"
	mux.HandleFunc("GET "+base, h.listQuickCommands)
	mux.HandleFunc("POST "+base, h.createQuickCommand)
	mux.HandleFunc("PUT "+base+"/order", h.reorderQuickCommands)
	mux.HandleFunc("PATCH "+base+"/{quickCommandID}", h.updateQuickCommand)
	mux.HandleFunc("DELETE "+base+"/{quickCommandID}", h.deleteQuickCommand)
}

type quickCommandRequest struct {
	ID         string                       `json:"id"`
	Version    uint64                       `json:"version"`
	Visibility *string                      `json:"visibility"`
	Name       *string                      `json:"name"`
	IconKind   collaboration.OptionalString `json:"icon_kind"`
	Icon       *string                      `json:"icon"`
	AgentID    collaboration.OptionalString `json:"agent_id"`
	CrewID     collaboration.OptionalString `json:"crew_id"`
	Prompt     *string                      `json:"prompt"`
}

type quickCommandOrderRequest struct {
	Visibility string   `json:"visibility"`
	IDs        []string `json:"ids"`
}

func optionalValue(value collaboration.OptionalString) string {
	if value.Value == nil {
		return ""
	}
	return *value.Value
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (h *Handler) listQuickCommands(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var list conversations.QuickCommandList
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		list, err = (conversations.Service{}).ListQuickCommands(ctx, tx, selection.OrganizationID, actor, r.PathValue("spaceID"))
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) createQuickCommand(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input quickCommandRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Name == nil || input.Visibility == nil {
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
	spaceID := r.PathValue("spaceID")
	var item conversations.QuickCommand
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = (conversations.Service{}).CreateQuickCommand(ctx, tx, selection.OrganizationID, actor, spaceID, conversations.QuickCommandInput{
			ID: input.ID, Visibility: *input.Visibility, Name: *input.Name, IconKind: optionalValue(input.IconKind),
			Icon: pointerValue(input.Icon), AgentID: optionalValue(input.AgentID), CrewID: optionalValue(input.CrewID),
			Prompt: pointerValue(input.Prompt),
		})
		if err != nil {
			return err
		}
		return auditQuickCommand(ctx, tx, requestID, selection.OrganizationID, actor, "task_quick_command.created", spaceID, item.ID, item.Visibility)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateQuickCommand(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input quickCommandRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	patch := conversations.QuickCommandPatch{
		Version: input.Version, Visibility: input.Visibility, Name: input.Name, Prompt: input.Prompt,
		IconSet: input.IconKind.Set, IconKind: optionalValue(input.IconKind), Icon: pointerValue(input.Icon),
	}
	if input.AgentID.Set || input.CrewID.Set {
		agentID, crewID := optionalValue(input.AgentID), optionalValue(input.CrewID)
		patch.AgentID, patch.CrewID = &agentID, &crewID
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	spaceID := r.PathValue("spaceID")
	var change conversations.QuickCommandChange
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		change, err = (conversations.Service{}).UpdateQuickCommand(ctx, tx, selection.OrganizationID, actor, spaceID, r.PathValue("quickCommandID"), patch)
		if err != nil {
			return err
		}
		visibility := change.After.Visibility
		if change.Before.Visibility == conversations.QuickCommandShared {
			visibility = conversations.QuickCommandShared
		}
		return auditQuickCommand(ctx, tx, requestID, selection.OrganizationID, actor, "task_quick_command.updated", spaceID, change.After.ID, visibility)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, change.After)
}

func (h *Handler) deleteQuickCommand(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	spaceID := r.PathValue("spaceID")
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		item, err := (conversations.Service{}).DeleteQuickCommand(ctx, tx, selection.OrganizationID, actor, spaceID, r.PathValue("quickCommandID"))
		if err != nil {
			return err
		}
		return auditQuickCommand(ctx, tx, requestID, selection.OrganizationID, actor, "task_quick_command.deleted", spaceID, item.ID, item.Visibility)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorderQuickCommands(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input quickCommandOrderRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeConversationError(w, r, conversations.ErrInvalidRequest)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	spaceID := r.PathValue("spaceID")
	var items []conversations.QuickCommand
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = (conversations.Service{}).ReorderQuickCommands(ctx, tx, selection.OrganizationID, actor, spaceID, input.Visibility, input.IDs)
		if err != nil {
			return err
		}
		return auditQuickCommand(ctx, tx, requestID, selection.OrganizationID, actor, "task_quick_command.reordered", spaceID, spaceID, input.Visibility)
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quick_commands": items})
}

// auditQuickCommand 只为共享指令写审计；私有指令是本人数据，不写。不记名称与提示词。
func auditQuickCommand(ctx context.Context, tx pgx.Tx, requestID, organizationID string, actor conversations.Actor, action, spaceID, resourceID, visibility string) error {
	if visibility != conversations.QuickCommandShared {
		return nil
	}
	return recordConversationAudit(ctx, tx, requestID, organizationID, actor, action, "task_quick_command", resourceID, spaceID,
		map[string]string{"resource_kind": "shared_task_quick_command"})
}
