package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/docs"
	"openfox/internal/membership"
)

// 全局搜索（命令面板）：GET /v1/organizations/{organizationID}/search。
// 每一类结果各一条有界查询，可见性复用各模块现有规则（见 collaboration/search.go 等），不另写更宽的判断。

const (
	searchDefaultLimit = 5
	searchMaxLimit     = 20
	// searchMaxTotal 是旧版自动补全响应的条数上限；类别按固定顺序消耗额度，额度用完后不再查询后面的类别。
	searchMaxTotal = 100
)

// searchTypes 是支持的类型，同时也是响应里各组的固定顺序。
var legacySearchTypes = []string{"task", "project", "view", "agent", "person", "space", "doc"}

var searchTypes = append(slices.Clone(legacySearchTypes), "conversation", "message")

type searchItem struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Subtitle         string     `json:"subtitle"`
	SpaceID          string     `json:"space_id,omitempty"`
	DisplayID        string     `json:"display_id,omitempty"`
	ConversationID   string     `json:"conversation_id,omitempty"`
	ConversationKind string     `json:"conversation_kind,omitempty"`
	TaskDisplayID    string     `json:"task_display_id,omitempty"`
	MessageID        string     `json:"message_id,omitempty"`
	MessageSeq       int64      `json:"message_seq,omitempty"`
	OccurredAt       *time.Time `json:"occurred_at,omitempty"`
	SenderKind       string     `json:"sender_kind,omitempty"`
	SenderID         string     `json:"sender_id,omitempty"`
	SenderName       string     `json:"sender_name,omitempty"`
	total            int
}

type searchGroup struct {
	Type  string       `json:"type"`
	Items []searchItem `json:"items"`
	Total *int         `json:"total,omitempty"`
}

func (h *Handler) registerSearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/search", h.globalSearch)
}

// parseSearchTypes 解析 types（逗号分隔，可重复）；为空表示旧版七类；有未知类型返回 false。结果按固定顺序去重。
func parseSearchTypes(values []string) ([]string, bool) {
	wanted := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !slices.Contains(searchTypes, part) {
				return nil, false
			}
			wanted[part] = true
		}
	}
	if len(wanted) == 0 {
		return legacySearchTypes, true
	}
	ordered := make([]string, 0, len(wanted))
	for _, name := range searchTypes {
		if wanted[name] {
			ordered = append(ordered, name)
		}
	}
	return ordered, true
}

func (h *Handler) globalSearch(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	values := r.URL.Query()
	types, valid := parseSearchTypes(values["types"])
	if !valid {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "types contains an unsupported value")
		return
	}
	actor := call.selected.Actor
	scope := collaboration.SearchScope{Role: strings.ToUpper(string(actor.Role)), WithTotal: values.Has("offset")}
	if scope.WithTotal {
		offset, err := strconv.Atoi(values.Get("offset"))
		if err != nil || offset < 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "offset is invalid")
			return
		}
		scope.Offset = offset
	}
	scope.Query, valid = collaboration.SearchQueryValid(values.Get("q"))
	if !valid && !(scope.WithTotal && scope.Query == "" && len(types) == 1 && (types[0] == "task" || types[0] == "project")) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "q must be 1-100 characters")
		return
	}
	limit := searchDefaultLimit
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit is invalid")
			return
		}
		maximum := searchMaxLimit
		if scope.WithTotal {
			maximum = 50
		}
		limit = min(parsed, maximum)
	}
	legacySpace := strings.ToLower(strings.TrimSpace(values.Get("space_id")))
	if legacySpace != "" {
		if !membership.ValidateUUID(legacySpace) {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "space_id is invalid")
			return
		}
		scope.SpaceIDs = []string{legacySpace}
	}
	for _, list := range values["space_ids"] {
		for _, id := range strings.Split(list, ",") {
			id = strings.ToLower(strings.TrimSpace(id))
			if !membership.ValidateUUID(id) || len(scope.SpaceIDs) >= collaboration.MaxSpaceOrder {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "space_ids is invalid")
				return
			}
			if !slices.Contains(scope.SpaceIDs, id) {
				scope.SpaceIDs = append(scope.SpaceIDs, id)
			}
		}
	}
	if slices.Contains(types, "task") && scope.WithTotal {
		filter, err := collaboration.ParseSearchTaskFilter(values, actor)
		if err != nil {
			writeCollaborationError(w, r, err)
			return
		}
		scope.TaskFilter = filter
	}
	groups := make([]searchGroup, 0, len(types))
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := service.CheckActor(ctx, tx, call.organizationID, actor); err != nil {
			return err
		}
		remaining := searchMaxTotal
		for _, name := range types {
			groupScope := scope
			groupScope.Limit = limit
			if !scope.WithTotal {
				groupScope.Limit = min(limit, remaining)
			}
			if name == "person" || name == "conversation" || name == "message" {
				groupScope.SpaceIDs = nil
				if legacySpace != "" {
					groupScope.SpaceIDs = []string{legacySpace}
				}
			}
			var items []searchItem
			if groupScope.Limit > 0 {
				var err error
				if items, err = searchGroupItems(ctx, tx, service, name, groupScope, call, actor); err != nil {
					return err
				}
			}
			remaining -= len(items)
			if items == nil {
				items = []searchItem{}
			}
			group := searchGroup{Type: name, Items: items}
			if scope.WithTotal {
				count := 0
				if len(items) > 0 {
					count = items[0].total
				} else if scope.Offset > 0 {
					groupScope.Offset, groupScope.Limit = 0, 1
					head, err := searchGroupItems(ctx, tx, service, name, groupScope, call, actor)
					if err != nil {
						return err
					}
					if len(head) > 0 {
						count = head[0].total
					}
				}
				group.Total = &count
			}
			groups = append(groups, group)
		}
		return nil
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func searchGroupItems(ctx context.Context, tx pgx.Tx, service collaboration.Service, name string,
	scope collaboration.SearchScope, call taskCall, actor collaboration.Actor) ([]searchItem, error) {
	var items []searchItem
	fromHits := func(hits []collaboration.SearchHit, err error) ([]searchItem, error) {
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			items = append(items, searchItem{
				ID: hit.ID, Title: hit.Title, Subtitle: hit.Subtitle, SpaceID: hit.SpaceID,
				DisplayID: hit.DisplayID, TaskDisplayID: hit.TaskDisplayID,
				ConversationID: hit.ConversationID, ConversationKind: hit.ConversationKind,
				MessageID: hit.MessageID, MessageSeq: hit.MessageSeq, total: hit.Total,
				OccurredAt: hit.OccurredAt, SenderKind: hit.SenderKind, SenderID: hit.SenderID, SenderName: hit.SenderName,
			})
		}
		return items, nil
	}
	switch name {
	case "task":
		return fromHits(service.SearchTasks(ctx, tx, scope))
	case "project":
		return fromHits(service.SearchProjects(ctx, tx, scope))
	case "view":
		return fromHits(service.SearchViews(ctx, tx, scope))
	case "space":
		return fromHits(service.SearchSpaces(ctx, tx, scope))
	case "conversation", "message":
		return fromHits(conversations.SearchChats(ctx, tx, scope, name == "message"))
	case "person":
		// 与 GET /members 同一读权限；无权时这一组为空，不报错。
		if !authorizeMemberRead(call.organizationID, membership.ActorSnapshot(actor)).Allowed() {
			return nil, nil
		}
		return fromHits(service.SearchPeople(ctx, tx, scope))
	case "agent":
		hits, err := agents.SearchAgents(ctx, tx, scope)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			items = append(items, searchItem{ID: hit.ID, Title: hit.Name, Subtitle: hit.SpaceName, SpaceID: hit.SpaceID, total: hit.Total})
		}
		return items, nil
	case "doc":
		hits, err := docs.SearchAcrossSpaces(ctx, tx, scope)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			items = append(items, searchItem{ID: hit.ID, Title: hit.Title, Subtitle: hit.SpaceName, SpaceID: hit.SpaceID, total: hit.Total})
		}
		return items, nil
	}
	return nil, nil
}
