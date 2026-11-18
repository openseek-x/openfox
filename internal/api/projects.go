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

func (h *Handler) registerProjectRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	project := org + "/projects/{projectID}"
	mux.HandleFunc("GET "+org+"/projects", h.listProjects)
	mux.HandleFunc("POST "+org+"/projects", h.createProject)
	mux.HandleFunc("GET "+project, h.getProject)
	mux.HandleFunc("PATCH "+project, h.updateProject)
	mux.HandleFunc("DELETE "+project, h.deleteProject)
	mux.HandleFunc("GET "+project+"/delete-impact", h.getProjectDeleteImpact)
	mux.HandleFunc("GET "+project+"/spaces", h.listProjectSpaces)
	mux.HandleFunc("POST "+project+"/spaces", h.addProjectSpace)
	mux.HandleFunc("DELETE "+project+"/spaces/{spaceID}", h.removeProjectSpace)
	mux.HandleFunc("PUT "+project+"/spaces/{spaceID}/primary", h.setProjectPrimarySpace)
	mux.HandleFunc("PUT "+project+"/labels", h.setProjectLabels)
}

// projectAccess 级别：读 = 能读任一关联空间；写 = 主空间管理权限或任一关联空间的任务写权限；删除 = 主空间管理权限。
type projectAccess int

const (
	projectRead projectAccess = iota
	projectWrite
	projectDelete
)

// authorizeProject 先判可读（不可读一律 404，不泄露存在），再判写 / 删除权限（403），最后检查主空间是否停用（409）。
func (c taskCall) authorizeProject(ctx context.Context, tx pgx.Tx, service collaboration.Service, projectID string, level projectAccess) error {
	refs, err := service.ProjectSpaceRefs(ctx, tx, projectID)
	if err != nil {
		return err
	}
	readable, allowed := false, false
	var primary *collaboration.SpaceAccess
	for _, ref := range refs {
		access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, ref.SpaceID, c.selected.Actor.UserID)
		if err != nil {
			return err
		}
		// 主空间停用与否与当前用户能否读它无关：只读规则对所有写入者生效。
		if ref.IsPrimary {
			primary = &access
		}
		if !collaborationSpaceDecision(policy.ActionReadSpace, c.organizationID, c.selected, access, "").Allowed() {
			continue
		}
		readable = true
		manage := ref.IsPrimary && collaborationSpaceDecision(policy.ActionManageSpace, c.organizationID, c.selected, access, "").Allowed()
		edit := level == projectWrite && collaborationSpaceDecision(policy.ActionUpdateObject, c.organizationID, c.selected, access, projectID).Allowed()
		allowed = allowed || manage || edit
	}
	if !readable {
		return collaboration.ErrProjectNotFound
	}
	if level == projectRead {
		return nil
	}
	if !allowed {
		return decisionError{decision: policy.Deny(policy.ReasonActionForbidden)}
	}
	if primary != nil {
		return primary.RequireWritable()
	}
	return nil
}

// projectFunc 返回响应体；nil 表示 204。响应在事务提交后才写出。
type projectFunc func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error)

func (h *Handler) projectRead(w http.ResponseWriter, r *http.Request, fn projectFunc) {
	h.projectCall(w, r, false, projectRead, fn)
}

func (h *Handler) projectCall(w http.ResponseWriter, r *http.Request, mutation bool, level projectAccess, fn projectFunc) {
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return
	}
	projectID := r.PathValue("projectID")
	var body any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeProject(ctx, tx, service, projectID, level); err != nil {
			return err
		}
		var err error
		body, err = fn(ctx, tx, service, call, projectID)
		return err
	})
	switch {
	case err != nil:
		writeCollaborationError(w, r, err)
	case body == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, body)
	}
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query := r.URL.Query()
	limit, err := collaboration.ParsePageSize(query.Get("limit"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	options := collaboration.ProjectListOptions{SpaceID: query.Get("space_id"), Status: query.Get("status"), Limit: limit, Cursor: strings.TrimSpace(query.Get("cursor"))}
	var page collaboration.Page[collaboration.Project]
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, options.SpaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		page, err = service.ListProjects(ctx, tx, call.organizationID, call.selected.Actor, options)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": page.Items, "next_cursor": page.NextCursor})
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		SpaceID     string `json:"space_id"`
		Name        string `json:"name"`
		Icon        string `json:"icon"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
		LeadUserID  string `json:"lead_user_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	projectID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var item collaboration.Project
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, input.SpaceID, policy.ActionCreateObject, projectID); err != nil {
			return err
		}
		item, err = service.CreateProject(ctx, tx, call.organizationID, call.selected.Actor, collaboration.ProjectInput(input))
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	h.projectRead(w, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error) {
		return service.GetProject(ctx, tx, call.organizationID, call.selected.Actor, projectID)
	})
}

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version     uint64                       `json:"version"`
		Name        *string                      `json:"name"`
		Icon        collaboration.OptionalString `json:"icon"`
		Description *string                      `json:"description"`
		Status      *string                      `json:"status"`
		Priority    *string                      `json:"priority"`
		LeadUserID  collaboration.OptionalString `json:"lead_user_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	h.projectCall(w, r, true, projectWrite, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error) {
		return service.UpdateProject(ctx, tx, call.organizationID, call.selected.Actor, projectID, collaboration.ProjectPatch(input))
	})
}

func (h *Handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseUint(r.URL.Query().Get("version"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version query parameter is required")
		return
	}
	requestID := collaborationRequestID(w, r)
	h.projectCall(w, r, true, projectDelete, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error) {
		return nil, service.DeleteProject(ctx, tx, projectID, version, call.selected.Actor, requestID)
	})
}

func (h *Handler) getProjectDeleteImpact(w http.ResponseWriter, r *http.Request) {
	h.projectRead(w, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, _ taskCall, projectID string) (any, error) {
		count, err := service.ProjectDeleteImpact(ctx, tx, projectID)
		return map[string]int{"task_count": count}, err
	})
}

func (h *Handler) listProjectSpaces(w http.ResponseWriter, r *http.Request) {
	h.projectRead(w, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error) {
		spaces, err := service.ListProjectSpaces(ctx, tx, call.selected.Actor, projectID)
		return map[string]any{"spaces": spaces}, err
	})
}

func (h *Handler) addProjectSpace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SpaceID string `json:"space_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	h.projectCall(w, r, true, projectWrite, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error) {
		// 目标空间也要有任务写权限（同时拒绝停用空间）。
		if err := call.authorizeSpace(ctx, tx, service, input.SpaceID, policy.ActionCreateObject, projectID); err != nil {
			return nil, err
		}
		return nil, service.AddProjectSpace(ctx, tx, projectID, input.SpaceID)
	})
}

func (h *Handler) removeProjectSpace(w http.ResponseWriter, r *http.Request) {
	requestID := collaborationRequestID(w, r)
	h.projectCall(w, r, true, projectWrite, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, projectID string) (any, error) {
		return nil, service.RemoveProjectSpace(ctx, tx, projectID, r.PathValue("spaceID"), call.selected.Actor, requestID)
	})
}

func (h *Handler) setProjectPrimarySpace(w http.ResponseWriter, r *http.Request) {
	h.projectCall(w, r, true, projectWrite, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, _ taskCall, projectID string) (any, error) {
		return nil, service.SetProjectPrimarySpace(ctx, tx, projectID, r.PathValue("spaceID"))
	})
}

func (h *Handler) setProjectLabels(w http.ResponseWriter, r *http.Request) {
	var input struct {
		LabelIDs []string `json:"label_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	h.projectCall(w, r, true, projectWrite, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, _ taskCall, projectID string) (any, error) {
		return nil, service.SetProjectLabels(ctx, tx, projectID, input.LabelIDs)
	})
}
