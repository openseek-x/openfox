package api

// 空间成员、Git 仓库、任务类型删除与推荐模板接口。

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

// registerSpaceAdminRoutes 注册空间仓库、成员、任务类型删除与模板接口。
func (h *Handler) registerSpaceAdminRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	org := "/v1/organizations/{organizationID}"
	space := org + "/spaces/{spaceID}"
	mux.HandleFunc("GET "+space+"/members", h.listSpaceMembers)
	mux.HandleFunc("PUT "+space+"/members/{userID}", h.upsertSpaceMember)
	mux.HandleFunc("DELETE "+space+"/members/{userID}", h.removeSpaceMember)
	mux.HandleFunc("GET "+space+"/repos", h.listSpaceRepos)
	mux.HandleFunc("POST "+space+"/repos", h.createSpaceRepo)
	mux.HandleFunc("PATCH "+space+"/repos/{repoID}", h.updateSpaceRepo)
	mux.HandleFunc("DELETE "+space+"/repos/{repoID}", h.deleteSpaceRepo)
	mux.HandleFunc("DELETE "+space+"/task-types/{typeID}", h.deleteTaskType)
	mux.HandleFunc("GET "+org+"/task-type-templates", h.listTaskTypeTemplates)
}

// spaceAccess 读取空间访问信息并按策略检查（不做停用检查：成员与设置管理在停用空间也允许）。
func (c taskCall) spaceAccess(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string, action policy.Action) (collaboration.SpaceAccess, error) {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return collaboration.SpaceAccess{}, err
	}
	decision := collaborationSpaceDecision(action, c.organizationID, c.selected, access, "")
	if !decision.Allowed() {
		return collaboration.SpaceAccess{}, decisionError{decision: decision}
	}
	return access, nil
}

func (h *Handler) listSpaceMembers(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var members []collaboration.SpaceMemberDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		members, err = service.ListSpaceMembersDetail(ctx, tx, call.organizationID, spaceID, call.selected.Actor,
			r.URL.Query().Get("q"), r.URL.Query().Get("role"))
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (h *Handler) upsertSpaceMember(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Role   string `json:"role"`
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, userID := r.PathValue("spaceID"), r.PathValue("userID")
	requestID := collaborationRequestID(w, r)
	var member collaboration.SpaceMemberDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.spaceAccess(ctx, tx, service, spaceID, policy.ActionReadSpace)
		if err != nil {
			return err
		}
		action := "space_member.updated"
		if collaboration.SelfJoinAllowed(access, call.selected.Actor, userID, input.Role, input.Status) {
			action = "space_member.joined"
		} else if _, err := call.spaceAccess(ctx, tx, service, spaceID, policy.ActionManageSpaceMembers); err != nil {
			return err
		}
		member, err = service.UpsertSpaceMemberDetail(ctx, tx, call.organizationID, spaceID, userID, call.selected.Actor,
			collaboration.SpaceMemberInput{Role: input.Role, Status: input.Status})
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			action, "space_member", member.UserID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID, "status": member.Status, "result": "role=" + member.Role})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, member)
}

func (h *Handler) removeSpaceMember(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, userID := r.PathValue("spaceID"), strings.ToLower(r.PathValue("userID"))
	requestID := collaborationRequestID(w, r)
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if _, err := call.spaceAccess(ctx, tx, service, spaceID, policy.ActionReadSpace); err != nil {
			return err
		}
		action := "space_member.left"
		if userID != strings.ToLower(call.selected.Actor.UserID) {
			action = "space_member.removed"
			if _, err := call.spaceAccess(ctx, tx, service, spaceID, policy.ActionManageSpaceMembers); err != nil {
				return err
			}
		}
		if err := service.RemoveSpaceMember(ctx, tx, call.organizationID, spaceID, userID, call.selected.Actor); err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			action, "space_member", userID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Git 仓库 ----

func (h *Handler) listSpaceRepos(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var repos []collaboration.SpaceRepo
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		repos, err = service.ListSpaceRepos(ctx, tx, call.organizationID, spaceID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": repos})
}

func (h *Handler) createSpaceRepo(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ConnectionID string `json:"connection_id"`
		URL          string `json:"url"`
		Description  string `json:"description"`
		BaseBranch   string `json:"base_branch"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var repo collaboration.SpaceRepo
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		repo, err = service.CreateSpaceRepo(ctx, tx, call.organizationID, spaceID, call.selected.Actor,
			collaboration.SpaceRepoInput{ConnectionID: input.ConnectionID, URL: input.URL, Description: input.Description, BaseBranch: input.BaseBranch})
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"space_repo.created", "space_repo", repo.ID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID, "version": fmt.Sprint(repo.Version)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

func (h *Handler) updateSpaceRepo(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ConnectionID *string `json:"connection_id"`
		URL          *string `json:"url"`
		Description  *string `json:"description"`
		BaseBranch   *string `json:"base_branch"`
		Position     *int    `json:"position"`
		Version      uint64  `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID, repoID := r.PathValue("spaceID"), r.PathValue("repoID")
	requestID := collaborationRequestID(w, r)
	var repo collaboration.SpaceRepo
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		repo, err = service.UpdateSpaceRepo(ctx, tx, call.organizationID, spaceID, repoID, call.selected.Actor,
			collaboration.SpaceRepoPatch{ConnectionID: input.ConnectionID, URL: input.URL, Description: input.Description, BaseBranch: input.BaseBranch,
				Position: input.Position, Version: input.Version})
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"space_repo.updated", "space_repo", repo.ID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID, "version": fmt.Sprint(repo.Version)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (h *Handler) deleteSpaceRepo(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("version")), 10, 64)
	if err != nil || version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version is required")
		return
	}
	spaceID, repoID := r.PathValue("spaceID"), r.PathValue("repoID")
	requestID := collaborationRequestID(w, r)
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		if err := service.DeleteSpaceRepo(ctx, tx, call.organizationID, spaceID, repoID, call.selected.Actor, version); err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"space_repo.deleted", "space_repo", repoID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- 任务类型 ----

func (h *Handler) deleteTaskType(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	version, err := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("version")), 10, 64)
	if err != nil || version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version is required")
		return
	}
	spaceID, typeID := r.PathValue("spaceID"), r.PathValue("typeID")
	requestID := collaborationRequestID(w, r)
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		removed, err := service.DeleteTaskType(ctx, tx, call.organizationID, spaceID, typeID, call.selected.Actor, version)
		if err != nil {
			return err
		}
		result := "archived"
		if removed {
			result = "deleted"
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"task_type.deleted", "task_type", typeID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID, "result": result})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTaskTypeTemplates(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	purpose := strings.TrimSpace(r.URL.Query().Get("purpose"))
	templates, valid := collaboration.TemplatesForPurpose(purpose)
	if !valid {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "purpose is invalid")
		return
	}
	// 只需要组织成员身份：在租户事务里校验一次。
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		return service.CheckActor(ctx, tx, call.organizationID, call.selected.Actor)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
}
