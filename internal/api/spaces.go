package api

// 「空间设置与生命周期」接口：列表、新建、详情、资料与开关、标识符、影响预估、停用 / 恢复、软删除、个人排序。
// 权限一律在服务端判断：读 = ActionReadSpace，管理 = ActionManageSpace；不可读、跨租户、已删除 → 404。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
)

// registerSpaceRoutes 注册空间详情、设置与生命周期接口。
func (h *Handler) registerSpaceRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	org := "/v1/organizations/{organizationID}"
	space := org + "/spaces/{spaceID}"
	mux.HandleFunc("GET "+org+"/spaces", h.listSpaces)
	mux.HandleFunc("POST "+org+"/spaces", h.createSpace)
	mux.HandleFunc("GET "+space, h.getSpace)
	mux.HandleFunc("PATCH "+space, h.updateSpace)
	mux.HandleFunc("DELETE "+space, h.deleteSpace)
	mux.HandleFunc("POST "+space+"/identifier", h.changeSpaceIdentifier)
	mux.HandleFunc("GET "+org+"/space-identifiers/{prefix}", h.checkSpaceIdentifier)
	mux.HandleFunc("GET "+space+"/impact", h.getSpaceImpact)
	mux.HandleFunc("POST "+space+"/retire", h.retireSpace)
	mux.HandleFunc("POST "+space+"/restore", h.restoreSpace)
	mux.HandleFunc("PUT "+org+"/space-order", h.putSpaceOrder)
}

// spaceDetailResponse 对应契约 SpaceDetail。
type spaceDetailResponse struct {
	spaceResponse
	Icon            *string                       `json:"icon"`
	Purpose         string                        `json:"purpose"`
	Features        collaboration.SpaceFeatures   `json:"features"`
	MultiAssignee   bool                          `json:"multi_assignee"`
	Estimation      collaboration.SpaceEstimation `json:"estimation"`
	Status          string                        `json:"status"`
	RetiredAt       *time.Time                    `json:"retired_at"`
	Position        *int                          `json:"position"`
	MyRole          *string                       `json:"my_role"`
	TriageFavorited bool                          `json:"triage_favorited"`
	CanManage       bool                          `json:"can_manage"`
	MemberCount     int                           `json:"member_count"`
	TaskTypeCount   int                           `json:"task_type_count"`
	LabelCount      int                           `json:"label_count"`
	TaskCount       int                           `json:"task_count"`
	UpdatedAt       time.Time                     `json:"updated_at"`
}

func toSpaceDetailResponse(detail collaboration.SpaceDetail) spaceDetailResponse {
	status := "active"
	if strings.EqualFold(detail.Status, "ARCHIVED") {
		status = "retired"
	}
	return spaceDetailResponse{
		spaceResponse: toSpaceResponse(detail.Space), Icon: detail.Icon, Purpose: detail.Purpose,
		Features: detail.Features, MultiAssignee: detail.MultiAssignee, Estimation: detail.Estimation,
		Status: status, RetiredAt: detail.RetiredAt, Position: detail.Position, MyRole: detail.MyRole,
		TriageFavorited: detail.TriageFavorited, CanManage: detail.CanManage, MemberCount: detail.MemberCount, TaskTypeCount: detail.TaskTypeCount,
		LabelCount: detail.LabelCount, TaskCount: detail.TaskCount, UpdatedAt: detail.UpdatedAt,
	}
}

func (c taskCall) audit(ctx context.Context, tx pgx.Tx, requestID, action, spaceID string, metadata map[string]string) error {
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["space_id"] = spaceID
	return recordCollaborationAudit(ctx, tx, requestID, c.organizationID, c.selected.Actor, action, "space", spaceID,
		spaceID, c.selected.Actor.AuthorizationVersion, metadata)
}

func (h *Handler) listSpaces(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	var items []collaboration.SpaceDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		items, err = service.ListSpaceDetails(ctx, tx, call.organizationID, call.selected.Actor, status)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaces := make([]spaceDetailResponse, 0, len(items))
	for _, item := range items {
		spaces = append(spaces, toSpaceDetailResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces})
}

type createSpaceRequest struct {
	Name            string                            `json:"name"`
	Prefix          string                            `json:"prefix"`
	Visibility      string                            `json:"visibility"`
	Purpose         string                            `json:"purpose"`
	Icon            string                            `json:"icon"`
	Features        *collaboration.SpaceFeaturesPatch `json:"features"`
	MultiAssignee   *bool                             `json:"multi_assignee"`
	CopyFromSpaceID string                            `json:"copy_from_space_id"`
	TemplateKeys    []string                          `json:"template_keys"`
}

func (h *Handler) createSpace(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input createSpaceRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	requestID := collaborationRequestID(w, r)
	var item collaboration.SpaceDetail
	var replayed bool
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if decision := policy.Authorize(policy.Request{Action: policy.ActionManageOrganization,
			Actor: collaborationActorPolicy(call.organizationID, call.selected.Actor)}); !decision.Allowed() {
			return decisionError{decision: decision}
		}
		var err error
		item, replayed, err = service.CreateSpaceDetail(ctx, tx, call.organizationID, call.selected.Actor, collaboration.SpaceCreateInput{
			Name: input.Name, Prefix: input.Prefix, Visibility: input.Visibility, Purpose: input.Purpose, Icon: input.Icon,
			Features: input.Features, MultiAssignee: input.MultiAssignee, CopyFromSpaceID: input.CopyFromSpaceID,
			TemplateKeys: input.TemplateKeys, IdempotencyKey: strings.TrimSpace(r.Header.Get(idempotency.HeaderName)),
		})
		if err != nil || replayed {
			return err
		}
		metadata := map[string]string{"version": fmt.Sprint(item.Version)}
		if input.CopyFromSpaceID != "" {
			metadata["result"] = "copied_from=" + strings.ToLower(input.CopyFromSpaceID)
		}
		return call.audit(ctx, tx, requestID, "space.created", item.ID, metadata)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeCreated(w, replayed, toSpaceDetailResponse(item))
}

func (h *Handler) getSpace(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var item collaboration.SpaceDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.GetSpaceDetail(ctx, tx, call.organizationID, spaceID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDetailResponse(item))
}

type updateSpaceRequest struct {
	Version       uint64                            `json:"version"`
	Name          *string                           `json:"name"`
	Icon          collaboration.OptionalString      `json:"icon"`
	Visibility    *string                           `json:"visibility"`
	Purpose       *string                           `json:"purpose"`
	Features      *collaboration.SpaceFeaturesPatch `json:"features"`
	MultiAssignee *bool                             `json:"multi_assignee"`
}

func (h *Handler) updateSpace(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input updateSpaceRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var item collaboration.SpaceDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.UpdateSpaceSettings(ctx, tx, call.organizationID, spaceID, call.selected.Actor, collaboration.SpacePatch{
			Version: input.Version, Name: input.Name, Icon: input.Icon, Visibility: input.Visibility,
			Purpose: input.Purpose, Features: input.Features, MultiAssignee: input.MultiAssignee,
		})
		if err != nil {
			return err
		}
		return call.audit(ctx, tx, requestID, "space.updated", spaceID, map[string]string{"version": fmt.Sprint(item.Version)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDetailResponse(item))
}

func (h *Handler) changeSpaceIdentifier(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version uint64 `json:"version"`
		Prefix  string `json:"prefix"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var item collaboration.SpaceDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.ChangeIdentifier(ctx, tx, call.organizationID, spaceID, call.selected.Actor, input.Version, input.Prefix)
		if err != nil {
			return err
		}
		return call.audit(ctx, tx, requestID, "space.identifier_changed", spaceID,
			map[string]string{"version": fmt.Sprint(item.Version), "result": "prefix=" + item.Prefix})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDetailResponse(item))
}

func (h *Handler) checkSpaceIdentifier(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var result collaboration.IdentifierCheck
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		spaceID := strings.TrimSpace(r.URL.Query().Get("space_id"))
		if spaceID != "" {
			// 本空间的历史值视为可用：只有能管理该空间的人才需要（也才可以）这样预检。
			if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
				return err
			}
		}
		var err error
		result, err = service.CheckIdentifier(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("prefix"), spaceID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) getSpaceImpact(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var impact collaboration.SpaceImpact
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		impact, err = service.GetSpaceImpact(ctx, tx, call.organizationID, spaceID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, impact)
}

const (
	spaceRetiredRunMessage = "The Agent run was canceled because its space was retired."
	spaceDeletedRunMessage = "The Agent run was canceled because its space was deleted."
)

func (h *Handler) retireSpace(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version       uint64 `json:"version"`
		OpenTasks     string `json:"open_tasks"`
		TargetSpaceID string `json:"target_space_id"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var item collaboration.SpaceDetail
	var result collaboration.RetireResult
	var canceled dispatch.AgentRunCancellation
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		openTasks := strings.ToLower(strings.TrimSpace(input.OpenTasks))
		if openTasks == collaboration.RetireMove {
			// 目标空间须对操作人可写且未停用；不可读的目标与不存在一样报 INVALID_TARGET_SPACE，不泄露存在性。
			objectID, err := collaboration.NewID()
			if err != nil {
				return err
			}
			if err := call.authorizeSpace(ctx, tx, service, input.TargetSpaceID, policy.ActionCreateObject, objectID); err != nil {
				return collaboration.ErrInvalidTarget
			}
		}
		// 锁序：空间行 → Run（容量函数）→ 任务 / 空间改写。
		plan, err := service.PrepareRetire(ctx, tx, call.organizationID, spaceID, call.selected.Actor, collaboration.RetireInput{
			Version: input.Version, OpenTasks: openTasks, TargetSpaceID: input.TargetSpaceID, RequestID: requestID,
		})
		if err != nil {
			return err
		}
		canceled, err = dispatch.CancelSpaceRuns(ctx, tx, call.organizationID, spaceID, "space retired", spaceRetiredRunMessage)
		if err != nil {
			return err
		}
		result, err = service.ApplyRetire(ctx, tx, call.organizationID, call.selected.Actor, plan)
		if err != nil {
			return err
		}
		item, err = service.GetSpaceDetail(ctx, tx, call.organizationID, spaceID, call.selected.Actor)
		if err != nil {
			return err
		}
		return call.audit(ctx, tx, requestID, "space.retired", spaceID, map[string]string{
			"version": fmt.Sprint(item.Version), "status": openTasks,
			"result": fmt.Sprintf("canceled_tasks=%d,moved_tasks=%d,canceled_runs=%d", result.CanceledTasks, result.MovedTasks, canceled.Canceled),
		})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.publishRunCancels(canceled)
	writeJSON(w, http.StatusOK, map[string]any{
		"space": toSpaceDetailResponse(item), "canceled_tasks": result.CanceledTasks,
		"moved_tasks": result.MovedTasks, "canceled_runs": canceled.Canceled,
	})
}

func (h *Handler) restoreSpace(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var item collaboration.SpaceDetail
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		item, err = service.RestoreSpace(ctx, tx, call.organizationID, spaceID, call.selected.Actor, input.Version)
		if err != nil {
			return err
		}
		return call.audit(ctx, tx, requestID, "space.restored", spaceID, map[string]string{"version": fmt.Sprint(item.Version)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSpaceDetailResponse(item))
}

func (h *Handler) deleteSpace(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ConfirmName string `json:"confirm_name"`
		Version     uint64 `json:"version"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var result collaboration.DeleteResult
	var runs dispatch.AgentRunCancellation
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		// 一个事务：锁空间 → 取消空间的活动 Run（含其 Agent 的 Run）→ 归档 Agent → 吊销 SPACE 授权 → 软删除。任一步失败整体回滚。
		if err := service.PrepareDelete(ctx, tx, call.organizationID, spaceID, call.selected.Actor, input.Version, input.ConfirmName); err != nil {
			return err
		}
		agentIDs, err := collaboration.ListLiveAgentIDs(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		devices, err := collaboration.SpaceGrantDevices(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		spaceRuns, err := dispatch.CancelSpaceRuns(ctx, tx, call.organizationID, spaceID, "space deleted", spaceDeletedRunMessage)
		if err != nil {
			return err
		}
		agentRuns, err := dispatch.CancelAgentRuns(ctx, tx, call.organizationID, agentIDs, spaceDeletedRunMessage)
		if err != nil {
			return err
		}
		runs = dispatch.AgentRunCancellation{Canceled: spaceRuns.Canceled + agentRuns.Canceled,
			Notices: append(spaceRuns.Notices, agentRuns.Notices...)}
		result, err = service.ApplyDelete(ctx, tx, call.organizationID, spaceID, call.selected.Actor, input.Version, agentIDs)
		if err != nil {
			return err
		}
		for _, deviceID := range devices {
			if err := recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
				"device.grant_revoked", "device", deviceID, spaceID, call.selected.Actor.AuthorizationVersion,
				map[string]string{"status": "REVOKED", "result": "space_deleted"}); err != nil {
				return err
			}
		}
		return call.audit(ctx, tx, requestID, "space.deleted", spaceID, map[string]string{
			"result": fmt.Sprintf("archived_agents=%d,canceled_runs=%d,revoked_grants=%d",
				result.ArchivedAgents, runs.Canceled, result.RevokedGrants),
		})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.publishRunCancels(runs)
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": true, "archived_agents": result.ArchivedAgents, "canceled_runs": runs.Canceled,
		"revoked_grants": result.RevokedGrants,
	})
}

func (h *Handler) putSpaceOrder(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		SpaceIDs []string `json:"space_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.SpaceIDs == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		return service.PutSpaceOrder(ctx, tx, call.organizationID, call.selected.Actor, input.SpaceIDs)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publishRunCancels 在事务提交后唤醒电脑处理取消；投递失败不影响结果，电脑下一次续租会看到 CANCEL_REQUESTED。
func (h *Handler) publishRunCancels(result dispatch.AgentRunCancellation) {
	if h.dispatchHub == nil {
		return
	}
	for _, notice := range result.Notices {
		h.dispatchHub.PublishCancel(notice.DeviceID, notice.OrganizationID, notice.RunID, notice.Generation)
	}
}
