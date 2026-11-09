package api

// 全局配置接口：标签分组、标签、任务状态、关联类型、自定义字段、组织级任务类型与字段绑定。
// 权限：读 = 任意 ACTIVE 组织成员；写 = OWNER / ADMIN；空间标签与字段绑定走空间级权限。
// 创建类接口接入 Idempotency-Key；配置写操作写审计。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func (h *Handler) registerTaskConfigRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	const org = "/v1/organizations/{organizationID}"
	mux.HandleFunc("GET "+org+"/label-groups", h.listLabelGroups)
	mux.HandleFunc("POST "+org+"/label-groups", h.createLabelGroup)
	mux.HandleFunc("PATCH "+org+"/label-groups/{groupID}", h.updateLabelGroup)
	mux.HandleFunc("DELETE "+org+"/label-groups/{groupID}", h.deleteLabelGroup)

	mux.HandleFunc("GET "+org+"/labels", h.listLabels)
	mux.HandleFunc("POST "+org+"/labels", h.createLabel)
	mux.HandleFunc("POST "+org+"/labels/bulk", h.bulkLabels)
	mux.HandleFunc("PATCH "+org+"/labels/{labelID}", h.updateLabel)
	mux.HandleFunc("DELETE "+org+"/labels/{labelID}", h.deleteLabel)
	mux.HandleFunc("POST "+org+"/labels/{labelID}/merge", h.mergeLabel)

	mux.HandleFunc("GET "+org+"/task-statuses", h.listTaskStatuses)
	mux.HandleFunc("POST "+org+"/task-statuses", h.createTaskStatus)
	mux.HandleFunc("PATCH "+org+"/task-statuses/{statusID}", h.updateTaskStatus)
	mux.HandleFunc("POST "+org+"/task-statuses/{statusID}/move", h.moveTaskStatus)
	mux.HandleFunc("POST "+org+"/task-statuses/{statusID}/default", h.setDefaultTaskStatus)
	mux.HandleFunc("DELETE "+org+"/task-statuses/{statusID}", h.deleteTaskStatus)

	mux.HandleFunc("GET "+org+"/task-relation-types", h.listRelationTypes)
	mux.HandleFunc("POST "+org+"/task-relation-types", h.createRelationType)
	mux.HandleFunc("PATCH "+org+"/task-relation-types/{typeID}", h.updateRelationType)
	mux.HandleFunc("DELETE "+org+"/task-relation-types/{typeID}", h.deleteRelationType)

	mux.HandleFunc("GET "+org+"/custom-fields", h.listCustomFields)
	mux.HandleFunc("POST "+org+"/custom-fields", h.createCustomField)
	mux.HandleFunc("PATCH "+org+"/custom-fields/{fieldID}", h.updateCustomField)
	mux.HandleFunc("DELETE "+org+"/custom-fields/{fieldID}", h.deleteCustomField)
	mux.HandleFunc("POST "+org+"/custom-fields/{fieldID}/options", h.createFieldOption)
	mux.HandleFunc("PATCH "+org+"/custom-fields/{fieldID}/options/{optionID}", h.updateFieldOption)
	mux.HandleFunc("DELETE "+org+"/custom-fields/{fieldID}/options/{optionID}", h.deleteFieldOption)

	mux.HandleFunc("GET "+org+"/task-types", h.listOrganizationTaskTypes)
	mux.HandleFunc("GET "+org+"/spaces/{spaceID}/task-types/{typeID}/fields", h.listTypeFields)
	mux.HandleFunc("PUT "+org+"/spaces/{spaceID}/task-types/{typeID}/fields/{fieldID}", h.bindTypeField)
	mux.HandleFunc("DELETE "+org+"/spaces/{spaceID}/task-types/{typeID}/fields/{fieldID}", h.unbindTypeField)
}

// configRequest 是一次已认证、已选定组织的配置请求上下文。
type configRequest struct {
	Auth           collaborationAuth
	OrganizationID string
	RequestID      string
	IdempotencyKey string
}

func (cr configRequest) actor() collaboration.Actor { return cr.Auth.Actor }

// requireOrganization 校验组织级权限（读 = ReadOrganization，写 = ManageOrganization）。
func (cr configRequest) requireOrganization(action policy.Action) error {
	decision := policy.Authorize(policy.Request{Action: action, Actor: collaborationActorPolicy(cr.OrganizationID, cr.Auth.Actor)})
	if !decision.Allowed() {
		return decisionError{decision: decision}
	}
	return nil
}

// requireSpace 校验空间级权限；objectID 仅对象类动作（创建 / 更新 / 删除对象）需要。
func (cr configRequest) requireSpace(ctx context.Context, tx pgx.Tx, spaceID string, action policy.Action, objectID string) error {
	access, err := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, cr.OrganizationID, spaceID, cr.Auth.Actor.UserID)
	if err != nil {
		return err
	}
	decision := collaborationSpaceDecision(action, cr.OrganizationID, cr.Auth, access, objectID)
	if !decision.Allowed() {
		return decisionError{decision: decision}
	}
	if collaboration.IsObjectWrite(action) {
		return access.RequireWritable()
	}
	return nil
}

func (cr configRequest) audit(ctx context.Context, tx pgx.Tx, action, kind, id, spaceID string, metadata map[string]string) error {
	return recordCollaborationAudit(ctx, tx, cr.RequestID, cr.OrganizationID, cr.Auth.Actor,
		action, kind, id, spaceID, cr.Auth.Actor.AuthorizationVersion, metadata)
}

// runConfig 完成认证、请求体解析、组织选择和租户事务；成功返回 true，失败时已写好错误响应。
func (h *Handler) runConfig(w http.ResponseWriter, r *http.Request, mutation bool, body any,
	fn func(ctx context.Context, tx pgx.Tx, cr configRequest) error) bool {
	auth, ok := h.authenticateCollaboration(w, r, mutation)
	if !ok {
		return false
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return false
	}
	if body != nil {
		if err := decodeJSON(w, r, body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
			return false
		}
	}
	organizationID := r.PathValue("organizationID")
	selected, err := h.selectCollaboration(r.Context(), auth, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return false
	}
	cr := configRequest{
		Auth: selected, OrganizationID: organizationID, RequestID: collaborationRequestID(w, r),
		IdempotencyKey: strings.TrimSpace(r.Header.Get(idempotency.HeaderName)),
	}
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(selected.Selection), func(ctx context.Context, tx pgx.Tx) error {
			return fn(ctx, tx, cr)
		})
	if err != nil {
		writeCollaborationError(w, r, err)
		return false
	}
	return true
}

// writeCreated 首次创建返回 201；幂等重放返回 200 并带 Idempotent-Replayed: true。
func writeCreated(w http.ResponseWriter, replayed bool, value any) {
	if replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
		writeJSON(w, http.StatusOK, value)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func versionMetadata(version uint64) map[string]string {
	return map[string]string{"version": fmt.Sprint(version)}
}

// ---- 标签分组 ----

// requireLabelScope 校验标签 / 分组范围的权限：spaceID 为空 = 组织级（orgAction），否则为空间级（spaceAction）。
func (cr configRequest) requireLabelScope(ctx context.Context, tx pgx.Tx, spaceID string, orgAction, spaceAction policy.Action) error {
	if spaceID == "" {
		return cr.requireOrganization(orgAction)
	}
	return cr.requireSpace(ctx, tx, spaceID, spaceAction, "")
}

// requireLabelGroupWrite 按分组所属范围判定写权限：组织级需组织管理权限，空间分组需该空间管理权限。
func (cr configRequest) requireLabelGroupWrite(ctx context.Context, tx pgx.Tx, groupID string) (string, error) {
	spaceID, err := (collaboration.Service{}).LabelGroupSpaceID(ctx, tx, cr.OrganizationID, groupID, cr.actor())
	if err != nil {
		return "", err
	}
	if spaceID == nil {
		return "", cr.requireOrganization(policy.ActionManageOrganization)
	}
	return *spaceID, cr.requireSpace(ctx, tx, *spaceID, policy.ActionManageSpace, "")
}

func (h *Handler) listLabelGroups(w http.ResponseWriter, r *http.Request) {
	spaceID := strings.TrimSpace(r.URL.Query().Get("space_id"))
	var items []collaboration.LabelGroup
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireLabelScope(ctx, tx, spaceID, policy.ActionReadOrganization, policy.ActionReadSpace); err != nil {
			return err
		}
		var err error
		items, err = (collaboration.Service{}).ListLabelGroups(ctx, tx, cr.OrganizationID, cr.actor(), spaceID)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"label_groups": items})
	}
}

func (h *Handler) createLabelGroup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Color       string   `json:"color"`
		SpaceID     string   `json:"space_id"`
		LabelIDs    []string `json:"label_ids"`
	}
	var item collaboration.LabelGroup
	var replayed bool
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireLabelScope(ctx, tx, input.SpaceID, policy.ActionManageOrganization, policy.ActionManageSpace); err != nil {
			return err
		}
		var err error
		item, replayed, err = (collaboration.Service{}).CreateLabelGroup(ctx, tx, cr.OrganizationID, cr.actor(),
			collaboration.LabelGroupInput{SpaceID: input.SpaceID, Name: input.Name, Description: input.Description,
				Color: input.Color, LabelIDs: input.LabelIDs, IdempotencyKey: cr.IdempotencyKey})
		if err != nil || replayed {
			return err
		}
		return cr.audit(ctx, tx, "label_group.created", "label_group", item.ID, input.SpaceID, versionMetadata(item.Version))
	}) {
		writeCreated(w, replayed, item)
	}
}

func (h *Handler) updateLabelGroup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Color       *string `json:"color"`
		Version     uint64  `json:"version"`
	}
	var item collaboration.LabelGroup
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		spaceID, err := cr.requireLabelGroupWrite(ctx, tx, r.PathValue("groupID"))
		if err != nil {
			return err
		}
		item, err = (collaboration.Service{}).UpdateLabelGroup(ctx, tx, cr.OrganizationID, r.PathValue("groupID"), cr.actor(),
			collaboration.LabelGroupUpdateInput{Name: input.Name, Description: input.Description, Color: input.Color, Version: input.Version})
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "label_group.updated", "label_group", item.ID, spaceID, versionMetadata(item.Version))
	}) {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h *Handler) deleteLabelGroup(w http.ResponseWriter, r *http.Request) {
	var removed []string
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		groupID := r.PathValue("groupID")
		spaceID, err := cr.requireLabelGroupWrite(ctx, tx, groupID)
		if err != nil {
			return err
		}
		removed, err = (collaboration.Service{}).DeleteLabelGroup(ctx, tx, cr.OrganizationID, groupID, cr.actor())
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "label_group.deleted", "label_group", groupID, spaceID,
			map[string]string{"result": fmt.Sprintf("released=%d", len(removed))})
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"removed_label_ids": removed})
	}
}

// ---- 标签 ----

func (h *Handler) listLabels(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = collaboration.LabelScopeOrganization
	}
	spaceID := r.URL.Query().Get("space_id")
	var items []collaboration.Label
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if scope == collaboration.LabelScopePicker || scope == collaboration.LabelScopeSpace || scope == collaboration.LabelScopeSpaceArchived {
			if spaceID == "" {
				return collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "space_id is required for this scope")
			}
			if err := cr.requireSpace(ctx, tx, spaceID, policy.ActionReadSpace, ""); err != nil {
				return err
			}
		} else if err := cr.requireOrganization(policy.ActionReadOrganization); err != nil {
			return err
		}
		var err error
		items, err = (collaboration.Service{}).ListLabels(ctx, tx, cr.OrganizationID, cr.actor(), scope, spaceID)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"labels": items})
	}
}

func (h *Handler) createLabel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Color       string `json:"color"`
		GroupID     string `json:"group_id"`
		SpaceID     string `json:"space_id"`
	}
	var item collaboration.Label
	var replayed bool
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if input.SpaceID == "" {
			if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
				return err
			}
		} else {
			objectID, err := collaboration.NewID()
			if err != nil {
				return err
			}
			if err := cr.requireSpace(ctx, tx, input.SpaceID, policy.ActionCreateObject, objectID); err != nil {
				return err
			}
		}
		var err error
		item, replayed, err = (collaboration.Service{}).CreateLabel(ctx, tx, cr.OrganizationID, cr.actor(),
			collaboration.LabelInput{Name: input.Name, Description: input.Description, Color: input.Color,
				GroupID: input.GroupID, SpaceID: input.SpaceID, IdempotencyKey: cr.IdempotencyKey})
		if err != nil || replayed {
			return err
		}
		spaceID := ""
		if item.SpaceID != nil {
			spaceID = *item.SpaceID
		}
		return cr.audit(ctx, tx, "label.created", "label", item.ID, spaceID, versionMetadata(item.Version))
	}) {
		writeCreated(w, replayed, item)
	}
}

// requireLabelWrite 按标签所属范围判定写权限：组织级需管理权限，空间标签需该空间的对象权限。
func (cr configRequest) requireLabelWrite(ctx context.Context, tx pgx.Tx, labelID string, action policy.Action) (string, error) {
	spaceID, err := (collaboration.Service{}).LabelSpaceID(ctx, tx, cr.OrganizationID, labelID, cr.actor())
	if err != nil {
		return "", err
	}
	if spaceID == nil {
		return "", cr.requireOrganization(policy.ActionManageOrganization)
	}
	return *spaceID, cr.requireSpace(ctx, tx, *spaceID, action, labelID)
}

func (h *Handler) updateLabel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        *string                      `json:"name"`
		Description *string                      `json:"description"`
		Color       *string                      `json:"color"`
		GroupID     collaboration.OptionalString `json:"group_id"`
		Archived    *bool                        `json:"archived"`
		Version     uint64                       `json:"version"`
	}
	var item collaboration.Label
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		labelID := r.PathValue("labelID")
		spaceID, err := cr.requireLabelWrite(ctx, tx, labelID, policy.ActionUpdateObject)
		if err != nil {
			return err
		}
		// 空间标签的归档与分组属于空间标签管理，需空间管理权限；改名称 / 颜色 / 描述沿用对象写权限。
		if spaceID != "" && (input.Archived != nil || input.GroupID.Set) {
			if err := cr.requireSpace(ctx, tx, spaceID, policy.ActionManageSpace, ""); err != nil {
				return err
			}
		}
		item, err = (collaboration.Service{}).UpdateLabel(ctx, tx, cr.OrganizationID, labelID, cr.actor(),
			collaboration.LabelUpdateInput{Name: input.Name, Description: input.Description, Color: input.Color,
				GroupID: input.GroupID, Archived: input.Archived, Version: input.Version})
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "label.updated", "label", item.ID, spaceID, versionMetadata(item.Version))
	}) {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h *Handler) deleteLabel(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		labelID := r.PathValue("labelID")
		spaceID, err := cr.requireLabelWrite(ctx, tx, labelID, policy.ActionDeleteObject)
		if err != nil {
			return err
		}
		if err := (collaboration.Service{}).DeleteLabel(ctx, tx, cr.OrganizationID, labelID, cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "label.deleted", "label", labelID, spaceID, nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) mergeLabel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TargetLabelID string `json:"target_label_id"`
	}
	var item collaboration.Label
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		sourceID := r.PathValue("labelID")
		spaceID, err := cr.requireLabelWrite(ctx, tx, sourceID, policy.ActionUpdateObject)
		if err != nil {
			return err
		}
		item, err = (collaboration.Service{}).MergeLabel(ctx, tx, cr.OrganizationID, sourceID, input.TargetLabelID, cr.actor())
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "label.merged", "label", sourceID, spaceID, map[string]string{"result": "merged_into=" + item.ID})
	}) {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h *Handler) bulkLabels(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action        string   `json:"action"`
		SpaceID       string   `json:"space_id"`
		LabelIDs      []string `json:"label_ids"`
		TargetLabelID string   `json:"target_label_id"`
	}
	var affected int64
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireLabelScope(ctx, tx, input.SpaceID, policy.ActionManageOrganization, policy.ActionManageSpace); err != nil {
			return err
		}
		var err error
		action := strings.ToUpper(strings.TrimSpace(input.Action))
		affected, err = (collaboration.Service{}).BulkLabels(ctx, tx, cr.OrganizationID, cr.actor(), action, input.LabelIDs,
			input.TargetLabelID, input.SpaceID)
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "label.bulk", "label", input.LabelIDs[0], input.SpaceID,
			map[string]string{"status": action, "result": fmt.Sprintf("affected=%d", affected)})
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"affected": affected})
	}
}

// ---- 任务状态 ----

func (h *Handler) listTaskStatuses(w http.ResponseWriter, r *http.Request) {
	var statuses []collaboration.TaskStatus
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionReadOrganization); err != nil {
			return err
		}
		var err error
		statuses, err = (collaboration.Service{}).ListStatuses(ctx, tx, cr.OrganizationID, cr.actor())
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses})
	}
}

func (h *Handler) createTaskStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Position    *int   `json:"position"`
		Color       string `json:"color"`
		Icon        string `json:"icon"`
	}
	var status collaboration.TaskStatus
	var replayed bool
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		var err error
		status, replayed, err = (collaboration.Service{}).CreateStatus(ctx, tx, cr.OrganizationID, cr.actor(), collaboration.TaskStatusInput{
			Name: input.Name, Description: input.Description, Category: input.Category, Position: input.Position,
			Color: input.Color, Icon: input.Icon, IdempotencyKey: cr.IdempotencyKey,
		})
		if err != nil || replayed {
			return err
		}
		return cr.audit(ctx, tx, "task_status.created", "task_status", status.ID, "",
			map[string]string{"status": status.Category, "version": fmt.Sprint(status.Version)})
	}) {
		writeCreated(w, replayed, status)
	}
}

func (h *Handler) updateTaskStatus(w http.ResponseWriter, r *http.Request) {
	var input collaboration.TaskStatusUpdateInput
	var status collaboration.TaskStatus
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		var err error
		status, err = (collaboration.Service{}).UpdateStatus(ctx, tx, cr.OrganizationID, r.PathValue("statusID"), cr.actor(), input)
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "task_status.updated", "task_status", status.ID, "",
			map[string]string{"status": status.Category, "version": fmt.Sprint(status.Version)})
	}) {
		writeJSON(w, http.StatusOK, status)
	}
}

func (h *Handler) moveTaskStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Category       string  `json:"category"`
		BeforeStatusID *string `json:"before_status_id"`
		Version        uint64  `json:"version"`
	}
	var statuses []collaboration.TaskStatus
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		statusID := r.PathValue("statusID")
		var err error
		statuses, err = (collaboration.Service{}).MoveStatus(ctx, tx, cr.OrganizationID, statusID, cr.actor(),
			collaboration.StatusMoveInput{Category: input.Category, BeforeStatusID: input.BeforeStatusID, Version: input.Version})
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "task_status.moved", "task_status", statusID, "",
			map[string]string{"status": strings.ToUpper(input.Category)})
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses})
	}
}

func (h *Handler) setDefaultTaskStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version uint64 `json:"version"`
	}
	var statuses []collaboration.TaskStatus
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		statusID := r.PathValue("statusID")
		var err error
		statuses, err = (collaboration.Service{}).SetDefaultStatus(ctx, tx, cr.OrganizationID, statusID, cr.actor(), input.Version)
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "task_status.default_set", "task_status", statusID, "", nil)
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"statuses": statuses})
	}
}

func (h *Handler) deleteTaskStatus(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		statusID := r.PathValue("statusID")
		if err := (collaboration.Service{}).DeleteStatus(ctx, tx, cr.OrganizationID, statusID,
			r.URL.Query().Get("replacement_status_id"), cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "task_status.deleted", "task_status", statusID, "", nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- 关联类型 ----

func (h *Handler) listRelationTypes(w http.ResponseWriter, r *http.Request) {
	var items []collaboration.RelationType
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionReadOrganization); err != nil {
			return err
		}
		var err error
		items, err = (collaboration.Service{}).ListRelationTypes(ctx, tx, cr.OrganizationID, cr.actor())
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"relation_types": items})
	}
}

func (h *Handler) createRelationType(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name         string `json:"name"`
		OutwardLabel string `json:"outward_label"`
		InwardLabel  string `json:"inward_label"`
	}
	var item collaboration.RelationType
	var replayed bool
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		var err error
		item, replayed, err = (collaboration.Service{}).CreateRelationType(ctx, tx, cr.OrganizationID, cr.actor(),
			collaboration.RelationTypeInput{Name: input.Name, OutwardLabel: input.OutwardLabel,
				InwardLabel: input.InwardLabel, IdempotencyKey: cr.IdempotencyKey})
		if err != nil || replayed {
			return err
		}
		return cr.audit(ctx, tx, "relation_type.created", "relation_type", item.ID, "", nil)
	}) {
		writeCreated(w, replayed, item)
	}
}

func (h *Handler) updateRelationType(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name         *string `json:"name"`
		OutwardLabel *string `json:"outward_label"`
		InwardLabel  *string `json:"inward_label"`
		Version      uint64  `json:"version"`
	}
	var item collaboration.RelationType
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		var err error
		item, err = (collaboration.Service{}).UpdateRelationType(ctx, tx, cr.OrganizationID, r.PathValue("typeID"), cr.actor(),
			collaboration.RelationTypeUpdateInput{Name: input.Name, OutwardLabel: input.OutwardLabel,
				InwardLabel: input.InwardLabel, Version: input.Version})
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "relation_type.updated", "relation_type", item.ID, "", nil)
	}) {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h *Handler) deleteRelationType(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		typeID := r.PathValue("typeID")
		if err := (collaboration.Service{}).DeleteRelationType(ctx, tx, cr.OrganizationID, typeID, cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "relation_type.deleted", "relation_type", typeID, "", nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- 自定义字段 ----

func (h *Handler) listCustomFields(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	var items []collaboration.CustomField
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionReadOrganization); err != nil {
			return err
		}
		var err error
		items, err = (collaboration.Service{}).ListCustomFields(ctx, tx, cr.OrganizationID, cr.actor(), includeArchived, r.URL.Query().Get("space_id"))
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"custom_fields": items})
	}
}

func (h *Handler) createCustomField(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name         string                           `json:"name"`
		Kind         string                           `json:"kind"`
		Description  string                           `json:"description"`
		DefaultValue json.RawMessage                  `json:"default_value"`
		MemberScope  string                           `json:"member_scope"`
		Options      []collaboration.FieldOptionInput `json:"options"`
	}
	var item collaboration.CustomField
	var replayed bool
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		var err error
		item, replayed, err = (collaboration.Service{}).CreateCustomField(ctx, tx, cr.OrganizationID, cr.actor(),
			collaboration.CustomFieldInput{Name: input.Name, Kind: input.Kind, Description: input.Description,
				DefaultValue: input.DefaultValue, MemberScope: input.MemberScope, Options: input.Options,
				IdempotencyKey: cr.IdempotencyKey})
		if err != nil || replayed {
			return err
		}
		return cr.audit(ctx, tx, "custom_field.created", "custom_field", item.ID, "", versionMetadata(item.Version))
	}) {
		writeCreated(w, replayed, item)
	}
}

func (h *Handler) updateCustomField(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name         *string                   `json:"name"`
		Description  *string                   `json:"description"`
		DefaultValue collaboration.OptionalRaw `json:"default_value"`
		MemberScope  *string                   `json:"member_scope"`
		Version      uint64                    `json:"version"`
	}
	var item collaboration.CustomField
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		var err error
		item, err = (collaboration.Service{}).UpdateCustomField(ctx, tx, cr.OrganizationID, r.PathValue("fieldID"), cr.actor(),
			collaboration.CustomFieldUpdateInput{Name: input.Name, Description: input.Description,
				DefaultValue: input.DefaultValue, MemberScope: input.MemberScope, Version: input.Version})
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "custom_field.updated", "custom_field", item.ID, "", versionMetadata(item.Version))
	}) {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h *Handler) deleteCustomField(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		fieldID := r.PathValue("fieldID")
		if err := (collaboration.Service{}).DeleteCustomField(ctx, tx, cr.OrganizationID, fieldID, cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "custom_field.deleted", "custom_field", fieldID, "", nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) createFieldOption(w http.ResponseWriter, r *http.Request) {
	var input collaboration.FieldOptionInput
	var item collaboration.FieldOption
	var replayed bool
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		fieldID := r.PathValue("fieldID")
		var err error
		item, replayed, err = (collaboration.Service{}).CreateFieldOption(ctx, tx, cr.OrganizationID, fieldID, cr.actor(),
			collaboration.FieldOptionCreateInput{Label: input.Label, Color: input.Color, IdempotencyKey: cr.IdempotencyKey})
		if err != nil || replayed {
			return err
		}
		return cr.audit(ctx, tx, "custom_field_option.created", "custom_field", fieldID, "", versionMetadata(item.Version))
	}) {
		writeCreated(w, replayed, item)
	}
}

func (h *Handler) updateFieldOption(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Label    *string `json:"label"`
		Color    *string `json:"color"`
		Position *int    `json:"position"`
		Version  uint64  `json:"version"`
	}
	var item collaboration.FieldOption
	if h.runConfig(w, r, true, &input, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		fieldID := r.PathValue("fieldID")
		var err error
		item, err = (collaboration.Service{}).UpdateFieldOption(ctx, tx, cr.OrganizationID, fieldID, r.PathValue("optionID"), cr.actor(),
			collaboration.FieldOptionUpdateInput{Label: input.Label, Color: input.Color, Position: input.Position, Version: input.Version})
		if err != nil {
			return err
		}
		return cr.audit(ctx, tx, "custom_field_option.updated", "custom_field", fieldID, "", versionMetadata(item.Version))
	}) {
		writeJSON(w, http.StatusOK, item)
	}
}

func (h *Handler) deleteFieldOption(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionManageOrganization); err != nil {
			return err
		}
		fieldID := r.PathValue("fieldID")
		if err := (collaboration.Service{}).DeleteFieldOption(ctx, tx, cr.OrganizationID, fieldID, r.PathValue("optionID"), cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "custom_field_option.deleted", "custom_field", fieldID, "", nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- 组织级任务类型与字段绑定 ----

func (h *Handler) listOrganizationTaskTypes(w http.ResponseWriter, r *http.Request) {
	var spaces []collaboration.OrgTaskTypeSpace
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		if err := cr.requireOrganization(policy.ActionReadOrganization); err != nil {
			return err
		}
		var err error
		spaces, err = (collaboration.Service{}).ListOrganizationTaskTypes(ctx, tx, cr.OrganizationID, cr.actor())
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces})
	}
}

func (h *Handler) listTypeFields(w http.ResponseWriter, r *http.Request) {
	var fields []collaboration.CustomField
	if h.runConfig(w, r, false, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		spaceID := r.PathValue("spaceID")
		if err := cr.requireSpace(ctx, tx, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		fields, err = (collaboration.Service{}).ListTypeFields(ctx, tx, cr.OrganizationID, spaceID, r.PathValue("typeID"), cr.actor())
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"custom_fields": fields})
	}
}

func (h *Handler) bindTypeField(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		spaceID, typeID, fieldID := r.PathValue("spaceID"), r.PathValue("typeID"), r.PathValue("fieldID")
		if err := cr.requireSpace(ctx, tx, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		if err := (collaboration.Service{}).BindFieldToType(ctx, tx, cr.OrganizationID, spaceID, typeID, fieldID, cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "task_type_field.bound", "custom_field", fieldID, spaceID, nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Handler) unbindTypeField(w http.ResponseWriter, r *http.Request) {
	if h.runConfig(w, r, true, nil, func(ctx context.Context, tx pgx.Tx, cr configRequest) error {
		spaceID, typeID, fieldID := r.PathValue("spaceID"), r.PathValue("typeID"), r.PathValue("fieldID")
		if err := cr.requireSpace(ctx, tx, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		if err := (collaboration.Service{}).UnbindFieldFromType(ctx, tx, cr.OrganizationID, spaceID, typeID, fieldID, cr.actor()); err != nil {
			return err
		}
		return cr.audit(ctx, tx, "task_type_field.unbound", "custom_field", fieldID, spaceID, nil)
	}) {
		w.WriteHeader(http.StatusNoContent)
	}
}
