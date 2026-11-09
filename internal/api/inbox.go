package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
)

// 首页收件箱（三分区）接口。读写都只作用于当前成员作为收件人的通知。

type inboxActionRequest struct {
	Action       string     `json:"action"`
	Section      string     `json:"section"`
	GroupIDs     []string   `json:"group_ids"`
	SnoozedUntil *time.Time `json:"snoozed_until"`
}

type inboxBulkRequest struct {
	Operation  string   `json:"operation"`
	Section    string   `json:"section"`
	Importance string   `json:"importance"`
	SpaceID    string   `json:"space_id"`
	SpaceIDs   []string `json:"space_ids"`
	SpaceMode  string   `json:"space_mode"`
	Query      string   `json:"q"`
}

func (h *Handler) registerInboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/inbox", h.listInbox)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/inbox/stats", h.inboxStats)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/inbox/actions", h.applyInboxAction)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/inbox/bulk", h.applyInboxBulk)
}

func (h *Handler) listInbox(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	section := values.Get("section")
	if section == "" {
		section = string(conversations.InboxSectionInbox)
	}
	// space_ids 可重复出现，也可用逗号分隔；旧版 space_id 继续有效。
	var spaceIDs []string
	for _, value := range values["space_ids"] {
		for _, id := range strings.Split(value, ",") {
			if id = strings.TrimSpace(id); id != "" {
				spaceIDs = append(spaceIDs, id)
			}
		}
	}
	query := conversations.InboxQuery{
		DesktopFeed: values.Get("delivery") == "banner",
		InboxScope: conversations.InboxScope{
			Section:    conversations.InboxSection(section),
			Importance: conversations.InboxImportance(values.Get("importance")),
			SpaceID:    strings.TrimSpace(values.Get("space_id")),
			SpaceIDs:   spaceIDs,
			SpaceMode:  conversations.InboxSpaceMode(values.Get("space_mode")),
			Query:      values.Get("q"),
		},
		Limit:  parseConversationLimit(r),
		Cursor: values.Get("cursor"),
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var page conversations.InboxPage
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = (conversations.Service{}).ListInbox(ctx, tx, selection.OrganizationID, actor, query)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *Handler) inboxStats(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var counts conversations.InboxCounts
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		counts, err = (conversations.Service{}).InboxStats(ctx, tx, selection.OrganizationID, actor)
		return err
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

func (h *Handler) applyInboxAction(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var request inboxActionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "inbox request is invalid")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var affected int64
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		affected, err = (conversations.Service{}).ApplyInboxAction(ctx, tx, selection.OrganizationID, actor,
			conversations.InboxAction(request.Action), conversations.InboxSection(request.Section),
			request.GroupIDs, request.SnoozedUntil)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"inbox."+request.Action, "inbox", actor.UserID, "",
			map[string]string{"status": request.Section, "result": strconv.FormatInt(affected, 10)})
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"affected": affected})
}

func (h *Handler) applyInboxBulk(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var request inboxBulkRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "inbox request is invalid")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	requestID := collaborationRequestID(w, r)
	var affected int64
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		affected, err = (conversations.Service{}).ApplyInboxBulk(ctx, tx, selection.OrganizationID, actor,
			conversations.InboxBulkOperation(request.Operation), conversations.InboxScope{
				Section:    conversations.InboxSection(request.Section),
				Importance: conversations.InboxImportance(request.Importance),
				SpaceID:    strings.TrimSpace(request.SpaceID),
				SpaceIDs:   request.SpaceIDs,
				SpaceMode:  conversations.InboxSpaceMode(request.SpaceMode),
				Query:      request.Query,
			})
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, requestID, selection.OrganizationID, actor,
			"inbox."+request.Operation, "inbox", actor.UserID, "",
			map[string]string{"status": request.Section, "result": strconv.FormatInt(affected, 10)})
	})
	if err != nil {
		writeConversationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"affected": affected})
}
