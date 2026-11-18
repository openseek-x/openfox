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
	"openfox/internal/conversations"
	"openfox/internal/policy"
)

// triageCall 在同一个事务里完成空间授权与业务操作。taskID 路径参数存在时按任务做对象级授权；
// 写入还要求空间未停用。fn 返回 nil 时回 204，否则以 status 返回 JSON。
func (h *Handler) triageCall(w http.ResponseWriter, r *http.Request, action policy.Action, status int,
	fn func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, manager bool) (any, error)) {
	call, ok := h.beginTaskCall(w, r, action != policy.ActionReadSpace)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var body any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := service.LoadSpaceAccess(ctx, tx, call.organizationID, spaceID, call.selected.Actor.UserID)
		if err != nil {
			return err
		}
		if decision := collaborationSpaceDecision(action, call.organizationID, call.selected, access, r.PathValue("taskID")); !decision.Allowed() {
			return decisionError{decision: decision}
		}
		if action != policy.ActionReadSpace {
			if err := access.RequireWritable(); err != nil {
				return err
			}
		}
		manager := collaborationSpaceDecision(policy.ActionManageSpace, call.organizationID, call.selected, access, "").Allowed()
		body, err = fn(ctx, tx, service, call, manager)
		return err
	})
	switch {
	case err != nil:
		writeCollaborationError(w, r, err)
	case body == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, status, body)
	}
}

// decodeTriage 解析请求体，失败时写 400 并返回 false。
func decodeTriage(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := decodeJSON(w, r, target); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return false
	}
	return true
}

func (h *Handler) setTriageFavorite(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		stmt := `DELETE FROM openfox.triage_favorites WHERE organization_id = openfox_private.current_organization_id()
		         AND user_id = openfox_private.current_actor_id() AND space_id = $1::uuid`
		if r.Method == http.MethodPut {
			stmt = `INSERT INTO openfox.triage_favorites (organization_id,user_id,space_id)
		    VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid) ON CONFLICT DO NOTHING`
		}
		_, err := tx.Exec(ctx, stmt, spaceID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getTriageSettings(w http.ResponseWriter, r *http.Request) {
	h.triageCall(w, r, policy.ActionReadSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, manager bool) (any, error) {
		return service.GetTriageSettings(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor, manager)
	})
}

func (h *Handler) updateTriageSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version         uint64  `json:"version"`
		Enabled         *bool   `json:"enabled"`
		RequirePriority *bool   `json:"require_priority"`
		Action          *string `json:"action"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		return service.UpdateTriageSettings(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor,
			collaboration.TriagePatch{Version: input.Version, Enabled: input.Enabled, RequirePriority: input.RequirePriority, Action: input.Action})
	})
}

func (h *Handler) replaceTriageMembers(w http.ResponseWriter, r *http.Request) {
	var input struct {
		UserIDs []string `json:"user_ids"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		members, err := service.SetTriageMembers(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor, input.UserIDs)
		return map[string]any{"members": members}, err
	})
}

func (h *Handler) listTriageRules(w http.ResponseWriter, r *http.Request) {
	h.triageCall(w, r, policy.ActionReadSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		rules, err := service.ListTriageRules(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor)
		return map[string]any{"rules": rules}, err
	})
}

type triageRuleRequest struct {
	Version    uint64                                `json:"version"`
	Name       string                                `json:"name"`
	Enabled    *bool                                 `json:"enabled"`
	Conditions *[]collaboration.TriageConditionGroup `json:"conditions"`
	Actions    *[]collaboration.TriageAction         `json:"actions"`
}

// input 把请求转成服务层输入；新建时缺省的条件按「匹配所有」处理，动作缺省会被校验拒绝。
func (req triageRuleRequest) input() collaboration.TriageRuleInput {
	input := collaboration.TriageRuleInput{Name: req.Name, Enabled: req.Enabled, Version: req.Version}
	if req.Conditions != nil {
		input.Conditions = *req.Conditions
	}
	if req.Actions != nil {
		input.Actions = *req.Actions
	}
	return input
}

func (h *Handler) createTriageRule(w http.ResponseWriter, r *http.Request) {
	var input triageRuleRequest
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		return service.CreateTriageRule(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor, input.input())
	})
}

func (h *Handler) updateTriageRule(w http.ResponseWriter, r *http.Request) {
	var input triageRuleRequest
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		return service.UpdateTriageRule(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("ruleID"), call.selected.Actor, input.input())
	})
}

func (h *Handler) setTriageRuleEnabled(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version uint64 `json:"version"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		return service.UpdateTriageRule(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("ruleID"), call.selected.Actor,
			collaboration.TriageRuleInput{Enabled: &input.Enabled, Version: input.Version})
	})
}

func (h *Handler) deleteTriageRule(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseUint(r.URL.Query().Get("version"), 10, 64)
	if err != nil || version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version is required")
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		return nil, service.DeleteTriageRule(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("ruleID"), call.selected.Actor, version)
	})
}

func (h *Handler) reorderTriageRules(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RuleIDs []string `json:"rule_ids"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionManageSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		rules, err := service.ReorderTriageRules(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor, input.RuleIDs)
		return map[string]any{"rules": rules}, err
	})
}

func (h *Handler) listTriageTasks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := collaboration.ParsePageSize(query.Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.triageCall(w, r, policy.ActionReadSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		// 筛选参数与任务列表相同（复用同一个解析器）；排序默认按进入分拣台时间倒序。
		filter, err := collaboration.ParseActorTaskFilter(query, call.selected.Actor)
		if err != nil {
			return nil, err
		}
		if filter.Sort == "" || filter.Sort == "entered" {
			filter.Sort = "triage_entered"
		}
		if filter.Order == "" {
			filter.Order = "desc"
		}
		filter.TriageQueue, filter.IncludeSnoozed = true, query.Get("include_snoozed") == "true"
		spaceID := r.PathValue("spaceID")
		if err := filter.SetTriageFacets(query, call.selected.Actor); err != nil {
			return nil, err
		}
		page, err := service.ListTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor,
			collaboration.TaskListOptions{WorkbenchOrder: query.Get("sort_mode") == "workbench", Limit: limit, Cursor: query.Get("cursor"), Filter: filter})
		if err != nil {
			return nil, err
		}
		counts, err := service.CountTasks(ctx, tx, call.organizationID, spaceID, call.selected.Actor, filter)
		items := make([]taskResponse, len(page.Items))
		for i, task := range page.Items {
			items[i] = toTaskResponse(task)
		}
		return map[string]any{"tasks": items, "next_cursor": page.NextCursor, "total": counts.Total}, err
	})
}

func (h *Handler) getTriageCandidates(w http.ResponseWriter, r *http.Request) {
	h.triageCall(w, r, policy.ActionReadSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		v := r.URL.Query()
		f, err := collaboration.ParseActorTaskFilter(v, call.selected.Actor)
		if err != nil {
			return nil, err
		}
		if err := f.SetTriageFacets(v, call.selected.Actor); err != nil {
			return nil, err
		}
		f.TriageQueue, f.IncludeSnoozed = true, v.Get("include_snoozed") == "true"
		return service.TriageCandidates(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor, v.Get("field"), f)
	})
}

func (h *Handler) getTriageCount(w http.ResponseWriter, r *http.Request) {
	h.triageCall(w, r, policy.ActionReadSpace, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		total, snoozed, next, err := service.TriageCount(ctx, tx, call.organizationID, r.PathValue("spaceID"), call.selected.Actor)
		return map[string]any{"total": total, "snoozed": snoozed, "next_snooze_at": next}, err
	})
}

// triageDecision 执行接受 / 拒绝 / 标记重复；带 comment 时在同一事务里写一条评论（拒绝原因等）。
func (h *Handler) triageDecision(w http.ResponseWriter, r *http.Request, comment string, subscribe *bool,
	fn func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, requestID string) (collaboration.Task, error)) {
	requestID := collaborationRequestID(w, r)
	h.triageCall(w, r, policy.ActionUpdateObject, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		task, err := fn(ctx, tx, service, call, requestID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(comment) != "" {
			commentID, err := newUUID()
			if err != nil {
				return nil, err
			}
			actor := call.selected.Actor
			if _, err := (conversations.Service{}).PostComment(ctx, tx, call.organizationID, task.SpaceID, task.ID,
				conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion},
				conversations.CommentInput{ID: commentID, Body: comment, ClientCommentID: requestID, SkipAutoFollow: subscribe != nil && !*subscribe}); err != nil {
				return nil, err
			}
		}
		if subscribe != nil && *subscribe && strings.TrimSpace(comment) == "" {
			actor := call.selected.Actor
			if err := service.AddFollower(ctx, tx, call.organizationID, task.SpaceID, task.ID, actor, "HUMAN", actor.UserID); err != nil {
				return nil, err
			}
		}
		return toTaskResponse(task), nil
	})
}

func (h *Handler) acceptTriageTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		FieldValues map[string]json.RawMessage       `json:"field_values"`
		Subscribe   *bool                            `json:"subscribe"`
		Version     uint64                           `json:"version"`
		StatusID    *string                          `json:"status_id"`
		Assignees   *[]collaboration.TaskAssigneeRef `json:"assignees"`
		ProjectID   collaboration.OptionalString     `json:"project_id"`
		IterationID collaboration.OptionalString     `json:"iteration_id"`
		Estimate    collaboration.OptionalFloat      `json:"estimate"`
		StartDate   collaboration.OptionalString     `json:"start_date"`
		DueDate     collaboration.OptionalString     `json:"due_date"`
		AssigneeIDs *[]string                        `json:"assignee_ids"`
		Priority    *string                          `json:"priority"`
		LabelIDs    *[]string                        `json:"label_ids"`
		Comment     string                           `json:"comment"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageDecision(w, r, input.Comment, input.Subscribe, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, requestID string) (collaboration.Task, error) {
		return service.AcceptTriage(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("taskID"), call.selected.Actor,
			collaboration.TriageAcceptInput{FieldValues: input.FieldValues, Version: input.Version, StatusID: input.StatusID, AssigneeIDs: input.AssigneeIDs,
				Priority: input.Priority, LabelIDs: input.LabelIDs, RequestID: requestID, Assignees: input.Assignees, ProjectID: input.ProjectID, IterationID: input.IterationID, Estimate: input.Estimate, StartDate: input.StartDate, DueDate: input.DueDate})
	})
}

func (h *Handler) declineTriageTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Subscribe *bool  `json:"subscribe"`
		Version   uint64 `json:"version"`
		Comment   string `json:"comment"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageDecision(w, r, input.Comment, input.Subscribe, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, requestID string) (collaboration.Task, error) {
		return service.DeclineTriage(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("taskID"), call.selected.Actor, input.Version, requestID)
	})
}

func (h *Handler) duplicateTriageTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Subscribe         *bool  `json:"subscribe"`
		Version           uint64 `json:"version"`
		DuplicateOfTaskID string `json:"duplicate_of_task_id"`
		Comment           string `json:"comment"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageDecision(w, r, input.Comment, input.Subscribe, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, requestID string) (collaboration.Task, error) {
		return service.MarkTriageDuplicate(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("taskID"), call.selected.Actor,
			input.Version, requestID, input.DuplicateOfTaskID)
	})
}

func (h *Handler) snoozeTriageTask(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Until *time.Time `json:"until"`
	}
	if !decodeTriage(w, r, &input) {
		return
	}
	h.triageCall(w, r, policy.ActionUpdateObject, http.StatusOK, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, _ bool) (any, error) {
		task, err := service.SnoozeTriage(ctx, tx, call.organizationID, r.PathValue("spaceID"), r.PathValue("taskID"), call.selected.Actor, input.Until)
		return toTaskResponse(task), err
	})
}
