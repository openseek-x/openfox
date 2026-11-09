package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

// 任务收藏（个人偏好）与统计视图聚合。

func (h *Handler) registerTaskStatsFavoriteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT "+taskBase+"/{taskID}/favorite", h.favoriteTask)
	mux.HandleFunc("DELETE "+taskBase+"/{taskID}/favorite", h.unfavoriteTask)
	mux.HandleFunc("GET "+taskBase+"/stats", h.statsTasks)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/my-tasks/stats", h.statsMyTasks)
}

func (h *Handler) favoriteTask(w http.ResponseWriter, r *http.Request) { h.setTaskFavorite(w, r, true) }
func (h *Handler) unfavoriteTask(w http.ResponseWriter, r *http.Request) {
	h.setTaskFavorite(w, r, false)
}

// setTaskFavorite 幂等：重复收藏 / 取消都返回 204。读不到该任务的人拿到与不存在相同的 404。
func (h *Handler) setTaskFavorite(w http.ResponseWriter, r *http.Request, favorite bool) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadObject, taskID); err != nil {
			return err
		}
		return service.SetTaskFavorite(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor, favorite)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseStatsOptions(r *http.Request) (collaboration.TaskStatsOptions, error) {
	values := r.URL.Query()
	options := collaboration.TaskStatsOptions{TimeZone: values.Get("tz")}
	if raw := strings.TrimSpace(values.Get("trend_days")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 1 {
			return options, collaboration.ErrInvalidTask
		}
		options.TrendDays = days
	}
	return options, nil
}

func statsResponse(stats collaboration.TaskStats) map[string]any {
	return map[string]any{
		"total": stats.Total, "completed": stats.Completed, "open": stats.Open, "overdue": stats.Overdue,
		"by_status": stats.ByStatus, "by_type": stats.ByType, "by_priority": stats.ByPriority,
		"by_assignee": stats.ByAssignee, "trend": stats.Trend,
	}
}

func (h *Handler) statsTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	filter, err := collaboration.ParseActorTaskFilter(r.URL.Query(), call.selected.Actor)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	options, err := parseStatsOptions(r)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var stats collaboration.TaskStats
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		if err := applyViewFilter(ctx, tx, service, spaceID, r.URL.Query().Get("view_id"), &filter); err != nil {
			return err
		}
		stats, err = service.StatsTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor, filter, options)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, statsResponse(stats))
}

func (h *Handler) statsMyTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query, err := parseMyTasksQuery(r, call.selected.Actor)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	options, err := parseStatsOptions(r)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var stats collaboration.TaskStats
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		stats, err = service.StatsMyTasks(ctx, tx, call.organizationID, call.selected.Actor, query, options)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, statsResponse(stats))
}
