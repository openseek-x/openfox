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
)

// 阶段 12e：组织级审计查询（K-01）。只读；仅组织 Owner/Admin。
// Run 生命周期不在审计表里（只进 Run 事件），这里不做合并。

func (h *Handler) registerAuditEventRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/audit-events", h.listAuditEvents)
}

type auditEventsResponse struct {
	Events []audit.ListedEvent `json:"events"`
	// NextCursor 为 null 表示没有更多。
	NextCursor *string `json:"next_cursor"`
}

func (h *Handler) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	_, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if normalized := strings.ToUpper(strings.TrimSpace(role)); normalized != "OWNER" && normalized != "ADMIN" {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization administration is required")
		return
	}
	query, err := parseAuditQuery(r, selection.OrganizationID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "audit query is invalid")
		return
	}
	var page audit.Page
	err = h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		page, err = audit.List(ctx, tx, query)
		return err
	})
	if errors.Is(err, audit.ErrInvalidQuery) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "audit query is invalid")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	response := auditEventsResponse{Events: page.Events}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, response)
}

// parseAuditQuery 解析查询参数。action 以 "*" 结尾表示前缀匹配（例如 device.*）；
// from / to 为 RFC 3339 时间，范围左闭右开。
func parseAuditQuery(r *http.Request, organizationID string) (audit.Query, error) {
	values := r.URL.Query()
	query := audit.Query{
		OrganizationID: organizationID,
		ActorID:        strings.TrimSpace(values.Get("actor_id")),
		ResourceKind:   strings.TrimSpace(values.Get("resource_kind")),
		Cursor:         strings.TrimSpace(values.Get("cursor")),
	}
	if action := strings.TrimSpace(values.Get("action")); strings.HasSuffix(action, "*") {
		query.ActionPrefix = strings.TrimSuffix(action, "*")
		if query.ActionPrefix == "" {
			return audit.Query{}, audit.ErrInvalidQuery
		}
	} else {
		query.Action = action
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return audit.Query{}, audit.ErrInvalidQuery
		}
		query.Limit = limit
	}
	var err error
	if query.From, err = parseAuditTime(values.Get("from")); err != nil {
		return audit.Query{}, err
	}
	if query.To, err = parseAuditTime(values.Get("to")); err != nil {
		return audit.Query{}, err
	}
	if err := query.Validate(); err != nil {
		return audit.Query{}, err
	}
	return query, nil
}

func parseAuditTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, audit.ErrInvalidQuery
	}
	return value.UTC(), nil
}
