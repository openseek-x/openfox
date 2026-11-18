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

func (h *Handler) registerIterationRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	space := org + "/spaces/{spaceID}"
	iteration := space + "/iterations/{iterationID}"
	mux.HandleFunc("GET "+space+"/iteration-settings", h.getIterationSettings)
	mux.HandleFunc("PATCH "+space+"/iteration-settings", h.updateIterationSettings)
	mux.HandleFunc("GET "+space+"/iterations", h.listIterations)
	mux.HandleFunc("POST "+space+"/iterations", h.createIteration)
	mux.HandleFunc("GET "+iteration, h.getIteration)
	mux.HandleFunc("PATCH "+iteration, h.updateIteration)
	mux.HandleFunc("DELETE "+iteration, h.deleteIteration)
	mux.HandleFunc("POST "+iteration+"/dates", h.updateIterationDates)
	mux.HandleFunc("POST "+iteration+"/start", h.startIteration)
	mux.HandleFunc("POST "+iteration+"/complete", h.completeIteration)
	mux.HandleFunc("POST "+iteration+"/tasks", h.updateIterationTasks)
	mux.HandleFunc("POST "+iteration+"/links", h.createIterationLink)
	mux.HandleFunc("DELETE "+iteration+"/links/{linkID}", h.deleteIterationLink)
}

type iterationSettingsPatchRequest struct {
	Version          uint64  `json:"version"`
	Enabled          *bool   `json:"enabled"`
	Mode             *string `json:"mode"`
	DurationWeeks    *int    `json:"duration_weeks"`
	CooldownWeeks    *int    `json:"cooldown_weeks"`
	StartWeekday     *int    `json:"start_weekday"`
	Timezone         *string `json:"timezone"`
	UpcomingCount    *int    `json:"upcoming_count"`
	AutoAddStarted   *bool   `json:"auto_add_started"`
	AutoAddCompleted *bool   `json:"auto_add_completed"`
	NextStartDate    *string `json:"next_start_date"`
}

type iterationCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	StartDate   string `json:"start_date"`
	EndDate     string `json:"end_date"`
}
type iterationPatchRequest struct {
	Version     uint64  `json:"version"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}
type iterationDatesRequest struct {
	Version   uint64  `json:"version"`
	StartDate *string `json:"start_date"`
	EndDate   *string `json:"end_date"`
}
type iterationVersionRequest struct {
	Version uint64 `json:"version"`
}
type iterationCompleteRequest struct {
	Version           uint64 `json:"version"`
	MoveUnfinishedTo  string `json:"move_unfinished_to"`
	TargetIterationID string `json:"target_iteration_id"`
}
type iterationTaskBatchRequest struct {
	TaskIDs []string `json:"task_ids"`
	Action  string   `json:"action"`
}
type iterationLinkRequest struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Position int    `json:"position"`
}

func (h *Handler) iterationCall(w http.ResponseWriter, r *http.Request, mutation bool) (taskCall, bool) {
	return h.beginTaskCall(w, r, mutation)
}
func (c taskCall) iterationAccess(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string, action policy.Action, objectID string) error {
	if err := c.authorizeSpace(ctx, tx, service, spaceID, action, objectID); err != nil || action == policy.ActionReadSpace {
		return err
	}
	// 停用空间只读：设置与迭代写入（管理类动作）同样拒绝，对象写入已在 authorizeSpace 里拒绝。
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return err
	}
	return access.RequireWritable()
}
func (h *Handler) getIterationSettings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var item collaboration.IterationSettings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.GetIterationSettings(ctx, tx, call.organizationID, spaceID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) updateIterationSettings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationSettingsPatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	var item collaboration.IterationSettings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		patch := collaboration.IterationSettingsPatch{Version: input.Version, Enabled: input.Enabled, Mode: input.Mode, DurationWeeks: input.DurationWeeks, CooldownWeeks: input.CooldownWeeks, StartWeekday: input.StartWeekday, Timezone: input.Timezone, UpcomingCount: input.UpcomingCount, AutoAddStarted: input.AutoAddStarted, AutoAddCompleted: input.AutoAddCompleted, NextStartDate: input.NextStartDate}
		var err error
		item, err = service.UpdateIterationSettings(ctx, tx, call.organizationID, spaceID, patch)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func parseIterationStatuses(raw string) []string {
	var out []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(strings.ToLower(value)); value != "" {
			out = append(out, value)
		}
	}
	return out
}
func (h *Handler) listIterations(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, false)
	if !ok {
		return
	}
	limit, err := collaboration.ParsePageSize(r.URL.Query().Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var page collaboration.Page[collaboration.Iteration]
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		page, err = service.ListIterations(ctx, tx, call.organizationID, spaceID, collaboration.IterationListOptions{Status: parseIterationStatuses(r.URL.Query().Get("status")), Limit: limit, Cursor: strings.TrimSpace(r.URL.Query().Get("cursor"))})
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"iterations": page.Items, "next_cursor": page.NextCursor})
}
func (h *Handler) createIteration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationCreateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	var item collaboration.Iteration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.CreateIteration(ctx, tx, call.organizationID, spaceID, call.selected.Actor, collaboration.IterationCreateInput{Name: input.Name, Description: input.Description, StartDate: input.StartDate, EndDate: input.EndDate})
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (h *Handler) getIteration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, false)
	if !ok {
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var item collaboration.Iteration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.GetIteration(ctx, tx, call.organizationID, spaceID, iterationID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) updateIteration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationPatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var item collaboration.Iteration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.UpdateIteration(ctx, tx, call.organizationID, spaceID, iterationID, collaboration.IterationPatchInput{Version: input.Version, Name: input.Name, Description: input.Description})
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) deleteIteration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	// DELETE 不带请求体，乐观锁版本走查询参数。
	version, err := strconv.ParseUint(r.URL.Query().Get("version"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version query parameter is required")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		return service.DeleteIteration(ctx, tx, call.organizationID, spaceID, iterationID, version)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) updateIterationDates(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationDatesRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var item collaboration.Iteration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.UpdateIterationDates(ctx, tx, call.organizationID, spaceID, iterationID, collaboration.IterationDatesInput{Version: input.Version, StartDate: input.StartDate, EndDate: input.EndDate})
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) startIteration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationVersionRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var item collaboration.Iteration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.StartIteration(ctx, tx, call.organizationID, spaceID, iterationID, input.Version)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) completeIteration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationCompleteRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var item collaboration.Iteration
	target := input.MoveUnfinishedTo
	if input.TargetIterationID != "" {
		target = input.TargetIterationID
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.CompleteIteration(ctx, tx, call.organizationID, spaceID, iterationID, call.selected.Actor, input.Version, target)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) updateIterationTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationTaskBatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var updated int
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionUpdateObject, iterationID); err != nil {
			return err
		}
		var err error
		updated, err = service.UpdateIterationTasks(ctx, tx, call.organizationID, spaceID, iterationID, collaboration.IterationTaskBatchInput{TaskIDs: input.TaskIDs, Action: input.Action})
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}
func (h *Handler) createIterationLink(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	var input iterationLinkRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, iterationID := r.PathValue("spaceID"), r.PathValue("iterationID")
	var item collaboration.IterationLink
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.CreateIterationLink(ctx, tx, call.organizationID, spaceID, iterationID, call.selected.Actor, collaboration.IterationLinkInput{Title: input.Title, URL: input.URL, Position: input.Position})
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (h *Handler) deleteIterationLink(w http.ResponseWriter, r *http.Request) {
	call, ok := h.iterationCall(w, r, true)
	if !ok {
		return
	}
	spaceID, iterationID, linkID := r.PathValue("spaceID"), r.PathValue("iterationID"), r.PathValue("linkID")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.iterationAccess(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		return service.DeleteIterationLink(ctx, tx, call.organizationID, spaceID, iterationID, linkID)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var _ = strconv.Itoa
