package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// 首页「我的任务」接口：只读，跨当前成员可读的空间汇总；改状态和新建仍走空间下的任务接口。
// 筛选、排序、游标与空间任务列表一致，另有 scope 与 space_id（可重复）。

type myTaskResponse struct {
	taskResponse
	SpaceName         string `json:"space_name"`
	ExecutorAgentName string `json:"executor_agent_name,omitempty"`
	AssignedToMe      bool   `json:"assigned_to_me"`
	CreatedByMe       bool   `json:"created_by_me"`
}

func (h *Handler) registerMyTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/my-tasks", h.listMyTasks)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/my-tasks/counts", h.countMyTasks)
}

func parseMyTasksQuery(r *http.Request, actor collaboration.Actor) (collaboration.MyTasksQuery, error) {
	values := r.URL.Query()
	filter, err := collaboration.ParseActorTaskFilter(values, actor)
	if err != nil {
		return collaboration.MyTasksQuery{}, err
	}
	query := collaboration.MyTasksQuery{
		Scope:  collaboration.MyTaskScope(values.Get("scope")),
		Cursor: strings.TrimSpace(values.Get("cursor")),
		Filter: filter,
	}
	for _, id := range values["space_id"] {
		if id = strings.TrimSpace(id); id != "" {
			query.SpaceIDs = append(query.SpaceIDs, strings.ToLower(id))
		}
	}
	query.Limit = 100
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		if query.Limit, err = strconv.Atoi(raw); err != nil {
			return collaboration.MyTasksQuery{}, collaboration.ErrInvalidPageSize
		}
	}
	return query, nil
}

func (h *Handler) listMyTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query, err := parseMyTasksQuery(r, call.selected.Actor)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var page collaboration.MyTasksPage
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		page, err = service.ListMyTasks(ctx, tx, call.organizationID, call.selected.Actor, query)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	items := make([]myTaskResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, myTaskResponse{
			taskResponse: toTaskResponse(item.Task), SpaceName: item.SpaceName,
			ExecutorAgentName: item.ExecutorAgentName, AssignedToMe: item.AssignedToMe, CreatedByMe: item.CreatedByMe,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items, "next_cursor": page.NextCursor})
}

func (h *Handler) countMyTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query, err := parseMyTasksQuery(r, call.selected.Actor)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var counts collaboration.TaskCounts
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		counts, err = service.CountMyTasks(ctx, tx, call.organizationID, call.selected.Actor, query)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": counts.Total, "by_status": counts.ByStatus})
}
