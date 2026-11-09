package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/membership"
)

// 组织设置、离开与删除组织。

const organizationDeletedRunMessage = "The Agent run was canceled because its organization was deleted."

func (h *Handler) registerOrganizationSettingsRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	for _, method := range []string{"GET", "PUT"} {
		mux.HandleFunc(method+" "+org+"/git-settings", h.gitSettings)
		mux.HandleFunc(method+" "+org+"/spaces/{spaceID}/git-settings", h.gitSettings)
	}
	mux.HandleFunc("GET "+org+"/settings", h.getOrganizationSettings)
	mux.HandleFunc("PATCH "+org+"/settings", h.updateOrganizationSettings)
	mux.HandleFunc("GET "+org+"/settings/lowest-priority-count", h.getLowestPriorityCount)
	mux.HandleFunc("POST "+org+"/leave", h.leaveOrganization)
	mux.HandleFunc("DELETE "+org, h.deleteOrganization)
}

func (h *Handler) getOrganizationSettings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var item collaboration.OrganizationSettings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		item, err = service.GetOrganizationSettings(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type organizationSettingsPatch struct {
	Version        uint64          `json:"version"`
	Name           *string         `json:"name"`
	Slug           *string         `json:"slug"`
	Description    *string         `json:"description"`
	Icon           json.RawMessage `json:"icon"`
	DefaultLocale  *string         `json:"default_locale"`
	PriorityLevels *int            `json:"priority_levels"`
}

func (h *Handler) updateOrganizationSettings(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input organizationSettingsPatch
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	patch := collaboration.OrganizationPatch{
		Version: input.Version, Name: input.Name, Slug: input.Slug, Description: input.Description,
		DefaultLocale: input.DefaultLocale, PriorityLevels: input.PriorityLevels,
	}
	// icon：缺省 = 不改；null = 清除（首字母头像）；{kind, value} = 替换。
	if len(input.Icon) > 0 {
		patch.SetIcon = true
		if !bytes.Equal(bytes.TrimSpace(input.Icon), []byte("null")) {
			var icon struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal(input.Icon, &icon); err != nil || icon.Kind == "" {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "icon is invalid")
				return
			}
			patch.IconKind, patch.Icon = icon.Kind, icon.Value
		}
	}
	requestID := collaborationRequestID(w, r)
	var item collaboration.OrganizationSettings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var downgraded int
		var err error
		item, downgraded, err = service.UpdateOrganizationSettings(ctx, tx, call.organizationID, call.selected.Actor, patch)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"organization.updated", "organization", call.organizationID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"result": fmt.Sprintf("version=%d,downgraded_tasks=%d", item.Version, downgraded)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) getLowestPriorityCount(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var count int
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		count, err = service.LowestPriorityCount(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

func (h *Handler) leaveOrganization(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	requestID := ensureMemberRequestID(w, r)
	actor := membership.ActorSnapshot(call.selected.Actor)
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		result, err := (membership.Service{}).Leave(ctx, tx, call.organizationID, actor)
		if err != nil {
			return err
		}
		return recordMemberAudit(ctx, tx, requestID, call.organizationID, actor, result)
	})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, membership.ErrSoleMember):
		writeError(w, r, http.StatusConflict, "SOLE_MEMBER", "the only member must delete the organization instead")
	case errors.Is(err, membership.ErrLastOwner):
		writeError(w, r, http.StatusConflict, "OWNER_REQUIRED", "organization requires another active owner")
	default:
		writeMemberError(w, r, err)
	}
}

func (h *Handler) deleteOrganization(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ConfirmName string `json:"confirm_name"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var result collaboration.OrganizationDeletion
	var runs dispatch.AgentRunCancellation
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		// 一个事务：锁组织行 → 取消全部活跃 Run → 归档 Agent → 吊销电脑授权 → 软删除。任一步失败整体回滚。
		if err := service.PrepareOrganizationDelete(ctx, tx, call.organizationID, call.selected.Actor, input.ConfirmName); err != nil {
			return err
		}
		var err error
		if runs, err = dispatch.CancelOrganizationRuns(ctx, tx, call.organizationID, "organization deleted",
			organizationDeletedRunMessage); err != nil {
			return err
		}
		if result, err = service.ApplyOrganizationDelete(ctx, tx, call.selected.Actor); err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"organization.deleted", "organization", call.organizationID, "", call.selected.Actor.AuthorizationVersion,
			map[string]string{"result": fmt.Sprintf("archived_agents=%d,canceled_runs=%d,revoked_grants=%d",
				result.ArchivedAgents, runs.Canceled, result.RevokedGrants)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.publishRunCancels(runs)
	w.WriteHeader(http.StatusNoContent)
}
