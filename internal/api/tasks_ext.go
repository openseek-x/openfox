package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 任务接口：列表筛选与计数、创建（幂等）、更新（含排序/父子/多处理人）、软删除、按编号查找。
// 最近删除与恢复见 tasks_trash.go。

const taskBase = "/v1/organizations/{organizationID}/spaces/{spaceID}/tasks"

func (h *Handler) registerTaskRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+taskBase, h.listTasks)
	mux.HandleFunc("POST "+taskBase, h.createTask)
	mux.HandleFunc("GET "+taskBase+"/counts", h.countTasks)
	mux.HandleFunc("POST "+taskBase+"/batch-update", h.batchUpdateTasks)
	h.registerTaskStatsFavoriteRoutes(mux)
	mux.HandleFunc("GET "+taskBase+"/{taskID}", h.getTask)
	mux.HandleFunc("PATCH "+taskBase+"/{taskID}", h.updateTask)
	mux.HandleFunc("DELETE "+taskBase+"/{taskID}", h.deleteTask)
	mux.HandleFunc("GET "+taskBase+"/{taskID}/activities", h.listTaskActivities)
	mux.HandleFunc("POST "+taskBase+"/{taskID}/move-preview", h.previewTaskMove)
	mux.HandleFunc("POST "+taskBase+"/{taskID}/move", h.moveTask)
	h.registerTaskTrashRoutes(mux)
	h.registerTaskAgentRunRoutes(mux)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/tasks/by-display-id/{displayID}", h.getTaskByDisplayID)
}

// taskCall 汇集任务类接口共同的前置步骤：认证、CSRF、数据库就绪与组织选择。
type taskCall struct {
	organizationID string
	selected       collaborationAuth
}

func (h *Handler) beginTaskCall(w http.ResponseWriter, r *http.Request, mutation bool) (taskCall, bool) {
	auth, ok := h.authenticateCollaboration(w, r, mutation)
	if !ok {
		return taskCall{}, false
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return taskCall{}, false
	}
	organizationID := r.PathValue("organizationID")
	selected, err := h.selectCollaboration(r.Context(), auth, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return taskCall{}, false
	}
	return taskCall{organizationID: organizationID, selected: selected}, true
}

// run 在租户事务里执行 fn。
func (c taskCall) run(h *Handler, r *http.Request, fn func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error) error {
	return h.pool.RunForOrganization(r.Context(), c.organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(c.selected.Selection), func(ctx context.Context, tx pgx.Tx) error {
			return fn(ctx, tx, collaboration.Service{})
		})
}

// authorizeSpace 读取空间访问信息并按策略检查，拒绝时返回 decisionError。
func (c taskCall) authorizeSpace(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string, action policy.Action, objectID string) error {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return err
	}
	return c.checkSpaceAccess(access, action, objectID)
}

func (c taskCall) checkSpaceAccess(access collaboration.SpaceAccess, action policy.Action, objectID string) error {
	decision := collaborationSpaceDecision(action, c.organizationID, c.selected, access, objectID)
	if !decision.Allowed() {
		return decisionError{decision: decision}
	}
	// 停用空间只读：对象写入统一在这里拒绝。
	if collaboration.IsObjectWrite(action) {
		return access.RequireWritable()
	}
	return nil
}

type taskRequest struct {
	TypeID          string                           `json:"type_id"`
	StatusID        string                           `json:"status_id"`
	SubmitToTriage  bool                             `json:"submit_to_triage"`
	Title           string                           `json:"title"`
	Description     string                           `json:"description"`
	Acceptance      string                           `json:"acceptance"`
	AssigneeUserID  string                           `json:"assignee_user_id"`
	ExecutorAgentID string                           `json:"executor_agent_id"`
	Priority        string                           `json:"priority"`
	Labels          []string                         `json:"labels"`
	StartDate       string                           `json:"start_date"`
	DueDate         string                           `json:"due_date"`
	ParentTaskID    string                           `json:"parent_task_id"`
	Assignees       *[]collaboration.TaskAssigneeRef `json:"assignees"`
	LabelIDs        *[]string                        `json:"label_ids"`
	FieldValues     map[string]json.RawMessage       `json:"field_values"`
	AfterTaskID     string                           `json:"after_task_id"`
	Estimate        *float64                         `json:"estimate"`
	IterationID     string                           `json:"iteration_id"`
	ProjectID       string                           `json:"project_id"`
	SourceComment   *struct {
		SpaceID string `json:"space_id"`
		TaskID  string `json:"task_id"`
		ID      string `json:"id"`
	} `json:"source_comment,omitempty"`
}

type taskPositionRequest struct {
	BeforeTaskID string `json:"before_task_id"`
	AfterTaskID  string `json:"after_task_id"`
}

type taskPatchRequest struct {
	TypeID          *string                          `json:"type_id"`
	StatusID        *string                          `json:"status_id"`
	Title           *string                          `json:"title"`
	Description     *string                          `json:"description"`
	Acceptance      *string                          `json:"acceptance"`
	AssigneeUserID  *string                          `json:"assignee_user_id"`
	ExecutorAgentID *string                          `json:"executor_agent_id"`
	Priority        *string                          `json:"priority"`
	Labels          *[]string                        `json:"labels"`
	StartDate       collaboration.OptionalString     `json:"start_date"`
	DueDate         collaboration.OptionalString     `json:"due_date"`
	ParentTaskID    collaboration.OptionalString     `json:"parent_task_id"`
	Assignees       *[]collaboration.TaskAssigneeRef `json:"assignees"`
	LabelIDs        *[]string                        `json:"label_ids"`
	Position        *taskPositionRequest             `json:"position"`
	Estimate        collaboration.OptionalFloat      `json:"estimate"`
	IterationID     collaboration.OptionalString     `json:"iteration_id"`
	ProjectID       collaboration.OptionalString     `json:"project_id"`
	Version         uint64                           `json:"version"`
	// 新增 Agent 处理人时用哪个运行对话（reuse / new，默认 reuse）。
	AssignmentConversation *string `json:"assignment_conversation"`
}

type taskResponse struct {
	OrganizationID   string                              `json:"organization_id"`
	SpaceID          string                              `json:"space_id"`
	ID               string                              `json:"id"`
	DisplayID        string                              `json:"display_id"`
	Sequence         int64                               `json:"sequence"`
	TypeID           string                              `json:"type_id"`
	TypeName         string                              `json:"type_name"`
	StatusID         string                              `json:"status_id"`
	StatusName       string                              `json:"status_name"`
	StatusCategory   string                              `json:"status_category"`
	Title            string                              `json:"title"`
	Description      string                              `json:"description"`
	Acceptance       string                              `json:"acceptance"`
	AssigneeUserID   string                              `json:"assignee_user_id,omitempty"`
	ExecutorAgentID  string                              `json:"executor_agent_id,omitempty"`
	Priority         string                              `json:"priority"`
	Version          uint64                              `json:"version"`
	CreatedAt        time.Time                           `json:"created_at"`
	UpdatedAt        time.Time                           `json:"updated_at"`
	Labels           []string                            `json:"labels"`
	StartDate        *string                             `json:"start_date"`
	DueDate          *string                             `json:"due_date"`
	ParentTaskID     *string                             `json:"parent_task_id"`
	Parent           *collaboration.TaskParent           `json:"parent"`
	Rank             string                              `json:"rank"`
	Assignees        []collaboration.TaskAssignee        `json:"assignees"`
	LabelItems       []collaboration.TaskLabelItem       `json:"label_items"`
	SubtaskCount     int                                 `json:"subtask_count"`
	SubtaskDoneCount int                                 `json:"subtask_done_count"`
	CreatedBy        *string                             `json:"created_by"`
	TypeIcon         *string                             `json:"type_icon"`
	StatusColor      *string                             `json:"status_color"`
	Deleted          bool                                `json:"deleted"`
	Estimate         *float64                            `json:"estimate"`
	IterationID      *string                             `json:"iteration_id"`
	ProjectID        *string                             `json:"project_id"`
	Triage           *taskTriageResponse                 `json:"triage,omitempty"`
	DuplicateOf      *collaboration.TaskParent           `json:"duplicate_of,omitempty"`
	ExternalAssignee *collaboration.TaskExternalAssignee `json:"external_assignee,omitempty"`
	Favorited        bool                                `json:"favorited"`
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

type taskTriageCreator struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type taskTriageResponse struct {
	EnteredAt    time.Time         `json:"entered_at"`
	Source       string            `json:"source"`
	SnoozedUntil *time.Time        `json:"snoozed_until"`
	Creator      taskTriageCreator `json:"creator"`
}

func toTaskResponse(task collaboration.Task) taskResponse {
	response := taskResponse{
		OrganizationID: task.OrganizationID, SpaceID: task.SpaceID, ID: task.ID, DisplayID: task.DisplayID,
		Sequence: task.Sequence, TypeID: task.TypeID, TypeName: task.TypeName, StatusID: task.StatusID,
		StatusName: task.StatusName, StatusCategory: task.StatusCategory, Title: task.Title,
		Description: task.Description, Acceptance: task.Acceptance, AssigneeUserID: task.AssigneeUserID,
		ExecutorAgentID: task.ExecutorAgentID, Priority: task.Priority, Version: task.Version,
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, Labels: task.Labels,
		StartDate: nullableString(task.StartDate), DueDate: nullableString(task.DueDate),
		ParentTaskID: nullableString(task.ParentTaskID), Parent: task.Parent, Rank: task.Rank,
		Assignees: task.Assignees, LabelItems: task.LabelItems,
		SubtaskCount: task.SubtaskCount, SubtaskDoneCount: task.SubtaskDoneCount,
		CreatedBy: nullableString(task.CreatedBy), TypeIcon: nullableString(task.TypeIcon),
		StatusColor: nullableString(task.StatusColor), Estimate: task.Estimate, IterationID: nullableString(task.IterationID),
		ProjectID: nullableString(task.ProjectID), DuplicateOf: task.DuplicateOf, ExternalAssignee: task.ExternalAssignee, Favorited: task.Favorited,
	}
	if task.TriageEnteredAt != nil {
		response.Triage = &taskTriageResponse{EnteredAt: *task.TriageEnteredAt, Source: task.TriageSource, SnoozedUntil: task.TriageSnoozedUntil,
			Creator: taskTriageCreator{ID: task.CreatedBy, Email: task.CreatedByEmail}}
	}
	return response
}

func (h *Handler) registerTriageRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	space := org + "/spaces/{spaceID}"
	task := space + "/tasks/{taskID}"
	mux.HandleFunc("GET "+space+"/triage", h.listTriageTasks)
	mux.HandleFunc("PUT "+space+"/triage/favorite", h.setTriageFavorite)
	mux.HandleFunc("DELETE "+space+"/triage/favorite", h.setTriageFavorite)
	mux.HandleFunc("GET "+space+"/triage/candidates", h.getTriageCandidates)
	mux.HandleFunc("GET "+space+"/triage/count", h.getTriageCount)
	mux.HandleFunc("GET "+space+"/triage-settings", h.getTriageSettings)
	mux.HandleFunc("PATCH "+space+"/triage-settings", h.updateTriageSettings)
	mux.HandleFunc("PUT "+space+"/triage-members", h.replaceTriageMembers)
	mux.HandleFunc("GET "+space+"/triage-rules", h.listTriageRules)
	mux.HandleFunc("POST "+space+"/triage-rules", h.createTriageRule)
	mux.HandleFunc("PUT "+space+"/triage-rules/reorder", h.reorderTriageRules)
	mux.HandleFunc("PUT "+space+"/triage-rules/{ruleID}", h.updateTriageRule)
	mux.HandleFunc("DELETE "+space+"/triage-rules/{ruleID}", h.deleteTriageRule)
	mux.HandleFunc("PUT "+space+"/triage-rules/{ruleID}/enabled", h.setTriageRuleEnabled)
	mux.HandleFunc("POST "+task+"/triage/accept", h.acceptTriageTask)
	mux.HandleFunc("POST "+task+"/triage/decline", h.declineTriageTask)
	mux.HandleFunc("POST "+task+"/triage/duplicate", h.duplicateTriageTask)
	mux.HandleFunc("PUT "+task+"/triage-snooze", h.snoozeTriageTask)
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	limit, err := collaboration.ParsePageSize(r.URL.Query().Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	filter, err := collaboration.ParseActorTaskFilter(r.URL.Query(), call.selected.Actor)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	options := collaboration.TaskListOptions{Limit: limit, Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")), Filter: filter}
	var page collaboration.Page[collaboration.Task]
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		if err := applyViewFilter(ctx, tx, service, spaceID, r.URL.Query().Get("view_id"), &options.Filter); err != nil {
			return err
		}
		page, err = service.ListTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor, options)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	items := make([]taskResponse, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toTaskResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items, "next_cursor": page.NextCursor})
}

// applyViewFilter 打开视图：把视图保存的筛选与请求里的筛选取交集（AND）。
func applyViewFilter(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID, viewID string, filter *collaboration.TaskFilter) error {
	if viewID == "" {
		return nil
	}
	view, err := service.ViewTaskFilter(ctx, tx, spaceID, strings.ToLower(strings.TrimSpace(viewID)))
	if err != nil {
		return err
	}
	filter.Also = &view
	return nil
}

func (h *Handler) countTasks(w http.ResponseWriter, r *http.Request) {
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
	var counts collaboration.TaskCounts
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		if err := applyViewFilter(ctx, tx, service, spaceID, r.URL.Query().Get("view_id"), &filter); err != nil {
			return err
		}
		counts, err = service.CountTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor, filter)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": counts.Total, "by_status": counts.ByStatus})
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input taskRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	taskID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	requestID := collaborationRequestID(w, r)
	idempotencyKey := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	var task collaboration.Task
	replayed := false
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionCreateObject, taskID); err != nil {
			return err
		}
		claim, err := idempotency.Begin(ctx, tx, "task.create", idempotencyKey, struct {
			SpaceID string      `json:"space_id"`
			Body    taskRequest `json:"body"`
		}{spaceID, input})
		if err != nil {
			return err
		}
		if claim.Replay {
			replayed = true
			task, err = service.GetTask(ctx, tx, call.organizationID, spaceID, claim.ResourceID, call.selected.Actor)
			return err
		}
		var sourceID string
		if source := input.SourceComment; source != nil {
			if input.ParentTaskID != "" && input.ParentTaskID != source.TaskID {
				return collaboration.NewRuleError(400, "INVALID_REQUEST", "source task must be the parent")
			}
			if err := call.authorizeSpace(ctx, tx, service, source.SpaceID, policy.ActionUpdateObject, source.TaskID); err != nil {
				return err
			}
			if _, err := service.GetTask(ctx, tx, call.organizationID, source.SpaceID, source.TaskID, call.selected.Actor); err != nil {
				return err
			}
			if _, err := collaboration.LockCommentThread(ctx, tx, source.SpaceID, source.TaskID, source.ID); err != nil {
				return err
			}
			sourceID = source.ID
		}
		task, err = service.CreateTask(ctx, tx, call.organizationID, spaceID, call.selected.Actor, collaboration.TaskInput{
			ID: taskID, TypeID: input.TypeID, StatusID: input.StatusID, Title: input.Title, SubmitToTriage: input.SubmitToTriage,
			Description: input.Description, Acceptance: input.Acceptance, AssigneeUserID: input.AssigneeUserID,
			ExecutorAgentID: input.ExecutorAgentID, Priority: input.Priority, Labels: input.Labels,
			StartDate: input.StartDate, DueDate: input.DueDate, ParentTaskID: input.ParentTaskID,
			Assignees: input.Assignees, LabelIDs: input.LabelIDs, FieldValues: input.FieldValues,
			AfterTaskID: input.AfterTaskID, Estimate: input.Estimate, IterationID: input.IterationID, ProjectID: input.ProjectID, SourceCommentID: sourceID, RequestID: requestID,
		})
		if err != nil {
			return err
		}
		if source := input.SourceComment; source != nil && input.ParentTaskID == "" {
			if _, err := service.CreateRelation(ctx, tx, call.organizationID, source.SpaceID, source.TaskID, call.selected.Actor, "builtin:RELATES", task.ID, "", requestID+"-relation"); err != nil {
				return err
			}
		}
		return idempotency.Complete(ctx, tx, claim, task.ID)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
		writeJSON(w, http.StatusOK, toTaskResponse(task))
		return
	}
	writeJSON(w, http.StatusCreated, toTaskResponse(task))
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	var response taskDetailResponse
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadObject, taskID); err != nil {
			return err
		}
		task, err := service.GetTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor)
		if err != nil {
			return err
		}
		response.taskResponse = toTaskResponse(task)
		response.DescriptionReactions, err = service.ReactionSummary(ctx, tx, taskID, "")
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type taskDetailResponse struct {
	taskResponse
	DescriptionReactions []collaboration.ReactionSummary `json:"description_reactions"`
}

func (h *Handler) getTaskByDisplayID(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	displayID := r.PathValue("displayID")
	var response struct {
		taskDetailResponse
		CanonicalDisplayID string `json:"canonical_display_id"`
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		spaceID, taskID, err := service.FindTaskByDisplayID(ctx, tx, call.organizationID, displayID, call.selected.Actor)
		if err != nil {
			return err
		}
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadObject, taskID); err != nil {
			return err
		}
		task, err := service.GetTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor)
		if err != nil {
			return err
		}
		response.taskResponse = toTaskResponse(task)
		// 按旧编号（历史标识符）命中时与请求值不同，前端据此替换地址。
		response.CanonicalDisplayID = task.DisplayID
		response.DescriptionReactions, err = service.ReactionSummary(ctx, tx, taskID, "")
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) updateTask(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input taskPatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	requestID := collaborationRequestID(w, r)
	update := collaboration.TaskUpdateInput{
		TypeID: input.TypeID, StatusID: input.StatusID, Title: input.Title, Description: input.Description,
		Acceptance: input.Acceptance, AssigneeUserID: input.AssigneeUserID, ExecutorAgentID: input.ExecutorAgentID,
		Priority: input.Priority, Labels: input.Labels, StartDate: input.StartDate, DueDate: input.DueDate,
		ParentTaskID: input.ParentTaskID, Assignees: input.Assignees, LabelIDs: input.LabelIDs,
		Estimate: input.Estimate, IterationID: input.IterationID, ProjectID: input.ProjectID, Version: input.Version, RequestID: requestID,
	}
	if input.AssignmentConversation != nil {
		if _, err := collaboration.NormalizeAssignmentConversation(*input.AssignmentConversation); err != nil || *input.AssignmentConversation == "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "assignment_conversation must be reuse or new")
			return
		}
		update.AssignmentConversation = *input.AssignmentConversation
	}
	if input.Position != nil {
		update.Position = &collaboration.TaskPositionInput{
			BeforeTaskID: input.Position.BeforeTaskID, AfterTaskID: input.Position.AfterTaskID,
		}
	}
	var task collaboration.Task
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, taskID); err != nil {
			return err
		}
		var err error
		task, err = service.UpdateTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor, update)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(task))
}

func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	version, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("version")), 10, 64)
	if err != nil || version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version is required")
		return
	}
	requestID := collaborationRequestID(w, r)
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionDeleteObject, taskID); err != nil {
			return err
		}
		return service.DeleteTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor, version, requestID)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTaskActivities(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	limit, err := collaboration.ParsePageSize(r.URL.Query().Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var cursor *collaboration.Cursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		parsed, err := collaboration.ParseCursor(raw)
		if err != nil {
			writeCollaborationError(w, r, err)
			return
		}
		cursor = &parsed
	}
	var page collaboration.Page[collaboration.Activity]
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadObject, taskID); err != nil {
			return err
		}
		if _, err := service.GetTask(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor); err != nil {
			return err
		}
		page, err = service.ListActivities(ctx, tx, call.organizationID, taskID, call.selected.Actor, limit, cursor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activities": page.Items, "next_cursor": page.NextCursor})
}
