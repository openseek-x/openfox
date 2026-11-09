package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/skills"
)

// 从 Runtime 导入（用户侧）与设备侧接口（下载 Run 的技能、上传导入的技能）。

// skillDeviceBodyLimit 是设备上传一个技能的请求体上限（文件内容 base64，契约 ≤ 8 MB）。
const skillDeviceBodyLimit = 8 << 20

var (
	importNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	importCodePattern = regexp.MustCompile(`^[A-Z0-9_]{0,64}$`)
)

func (h *Handler) createSkillRuntimeImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		DeviceID string   `json:"device_id"`
		Names    []string `json:"names"`
	}
	if err := decodeJSON(w, r, &input); err != nil || !membership.ValidateUUID(input.DeviceID) ||
		len(input.Names) == 0 || len(input.Names) > skills.MaxImportNames {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "device_id and 1-20 names are required")
		return
	}
	for _, name := range input.Names {
		if !importNamePattern.MatchString(name) {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "a skill name is invalid")
			return
		}
	}
	spaceID := r.PathValue("spaceID")
	params, err := json.Marshal(devices.SkillImportParams{SpaceID: spaceID, Names: input.Names})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var progress skills.RuntimeImport
	var command devices.Command
	created := false
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireSetWrite(); err != nil {
			return err
		}
		command, created, err = devices.CreateCommandInTx(ctx, tx, call.selected.Selection, input.DeviceID, devices.CommandSkillImport, params)
		if err != nil {
			return err
		}
		if created {
			err = skills.InsertImportItems(ctx, tx, command.ID, spaceID, input.Names)
		} else if !devices.SameSkillImportParams(command.Params, params) {
			// 同一台电脑同时只能有一个未完成的导入；参数不同的不复用。
			err = skills.ErrImportBusy
		}
		if err != nil {
			return err
		}
		progress, err = skills.LoadImport(ctx, tx, call.selected.Actor.UserID, spaceID, command.ID)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	// 提交后唤醒电脑；复用的 PENDING 命令也再推一次（电脑可能错过了上一次），重复唤醒无害。
	if h.dispatchHub != nil && command.Status == devices.CommandPending {
		h.dispatchHub.PublishCommand(command.DeviceID, call.organizationID, command.ID, string(command.Kind))
	}
	writeJSON(w, http.StatusAccepted, progress)
}

func (h *Handler) getSkillRuntimeImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, commandID := r.PathValue("spaceID"), r.PathValue("commandID")
	var progress skills.RuntimeImport
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		progress, err = skills.LoadImport(ctx, tx, call.selected.Actor.UserID, spaceID, commandID)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (h *Handler) cancelSkillRuntimeImport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	spaceID, commandID := r.PathValue("spaceID"), r.PathValue("commandID")
	var progress skills.RuntimeImport
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		progress, err = skills.CancelImport(ctx, tx, call.selected.Actor.UserID, spaceID, commandID)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

// writeDeviceSkillError 输出设备侧技能接口的错误：技能规则错误原样输出，其余沿用派发接口的映射。
func writeDeviceSkillError(w http.ResponseWriter, r *http.Request, err error) {
	var rule *collaboration.RuleError
	var tooLarge *http.MaxBytesError
	if errors.As(err, &rule) || errors.As(err, &tooLarge) {
		writeSkillError(w, r, err)
		return
	}
	writeDispatchError(w, r, err)
}

func (h *Handler) getDeviceRunSkill(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	generation, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("generation")), 10, 64)
	if err != nil || generation < 1 {
		writeDispatchError(w, r, dispatch.ErrInvalidRequest)
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	skillID := r.PathValue("skillID")
	var entries []skills.Entry
	var name string
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		// 授权与 Context 相同：租约有效、授权未撤销；技能还必须装配在该 Run 的 Agent 上。
		if err := h.dispatch.AuthorizeRunSkill(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), generation, skillID); err != nil {
			return err
		}
		var err error
		entries, name, err = skills.Entries(ctx, tx, skillID)
		return err
	})
	if err != nil {
		writeDeviceSkillError(w, r, err)
		return
	}
	// 文件内容在事务之外读取。
	bundle, err := skills.ReadBundle(r.Context(), h.delivery.Storage, name, entries)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "the skill files could not be read")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, bundle)
}

type deviceSkillUpload struct {
	Name      string              `json:"name"`
	Files     []skills.BundleFile `json:"files"`
	ErrorCode string              `json:"error_code"`
}

// uploadDeviceImportedSkill 接收电脑上传的一个技能。三步：
//  1. 事务一：锁命令与导入项，确认命令仍是 DELIVERED、名称在 names 里且该项仍是 pending；内容本身不合规时直接记为 failed。
//  2. 事务之外：把文件写入存储。
//  3. 事务二：以发起人的身份重新检查空间写权限，建立技能；重名记 skipped，其余记 failed 或 imported。
func (h *Handler) uploadDeviceImportedSkill(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	var input deviceSkillUpload
	r.Body = http.MaxBytesReader(w, r.Body, skillDeviceBodyLimit)
	if err := decodeStrictJSON(r, &input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeCollaborationError(w, r, skills.ErrTooLarge)
			return
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if !importNamePattern.MatchString(input.Name) || !importCodePattern.MatchString(input.ErrorCode) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "name or error_code is invalid")
		return
	}
	commandID := r.PathValue("commandID")

	// 内容先在内存里校验；不合规的原因码稍后写进导入项。
	var pkg skills.Package
	failure := input.ErrorCode
	if failure == "" {
		var err error
		if pkg, err = buildImportPackage(input.Files); err != nil {
			failure = importFailureCode(err)
		}
	}

	var item skills.ImportItem
	finished := false
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, finished, err = lockImportSlot(ctx, tx, principal.DeviceID, commandID, input.Name)
		if err != nil || finished {
			return err
		}
		if failure != "" {
			item, err = skills.FinishImportItem(ctx, tx, commandID, input.Name, skills.ItemFailed, failure, nil)
			finished = true
		}
		return err
	})
	if err != nil {
		writeDeviceSkillError(w, r, err)
		return
	}
	if finished {
		writeJSON(w, http.StatusOK, item)
		return
	}

	skillID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	entries, created, err := skills.StoreFiles(r.Context(), h.delivery.Storage, selection.OrganizationID, skillID, pkg.Files)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	keep := false
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var finished bool
		var active skills.ActiveImport
		var err error
		active, err = skills.LockActiveImport(ctx, tx, principal.DeviceID, commandID)
		if err != nil {
			return err
		}
		if item, finished, err = lockImportSlot(ctx, tx, principal.DeviceID, commandID, input.Name); err != nil || finished {
			return err
		}
		reason, err := h.importRequesterReason(ctx, tx, selection.OrganizationID, active.RequestedBy, active.SpaceID)
		if err != nil {
			return err
		}
		if reason != "" {
			item, err = skills.FinishImportItem(ctx, tx, commandID, input.Name, skills.ItemFailed, reason, nil)
			return err
		}
		// 保存点：重名等业务失败只回滚建技能，不影响导入项的记录。
		savepoint, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		createErr := skills.Create(ctx, savepoint, skills.CreateInput{
			ID: skillID, SpaceID: active.SpaceID, UserID: active.RequestedBy,
			Source:  skills.Source{Kind: "runtime", Label: h.deviceLabel(ctx, tx, principal.DeviceID)},
			Package: pkg, Entries: entries,
		})
		switch {
		case createErr == nil:
			if err := savepoint.Commit(ctx); err != nil {
				return err
			}
			keep = true
			item, err = skills.FinishImportItem(ctx, tx, commandID, input.Name, skills.ItemImported, "", &skillID)
			return err
		case errors.Is(createErr, skills.ErrNameTaken):
			_ = savepoint.Rollback(ctx)
			item, err = skills.FinishImportItem(ctx, tx, commandID, input.Name, skills.ItemSkipped, "SKILL_NAME_TAKEN", nil)
			return err
		case errors.Is(createErr, skills.ErrLimit):
			_ = savepoint.Rollback(ctx)
			item, err = skills.FinishImportItem(ctx, tx, commandID, input.Name, skills.ItemFailed, "SKILL_LIMIT", nil)
			return err
		default:
			_ = savepoint.Rollback(ctx)
			return createErr
		}
	})
	if !keep || err != nil {
		skills.DeleteKeys(r.Context(), h.delivery.Storage, created)
	}
	if err != nil {
		writeDeviceSkillError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// lockImportSlot 锁定命令与导入项。导入项已取消返回 ErrImportCanceled；已有终态（设备重试）返回该项并 finished = true。
func lockImportSlot(ctx context.Context, tx pgx.Tx, deviceID, commandID, name string) (skills.ImportItem, bool, error) {
	active, err := skills.LockActiveImport(ctx, tx, deviceID, commandID)
	if err != nil {
		return skills.ImportItem{}, false, err
	}
	listed := false
	for _, candidate := range active.Names {
		if candidate == name {
			listed = true
		}
	}
	if !listed {
		return skills.ImportItem{}, false, skills.ErrImportName
	}
	item, err := skills.LockImportItem(ctx, tx, commandID, name)
	if err != nil {
		return skills.ImportItem{}, false, err
	}
	switch item.Status {
	case skills.ItemPending:
		return item, false, nil
	case skills.ItemCanceled:
		return skills.ImportItem{}, false, skills.ErrImportCanceled
	}
	return item, true, nil
}

// buildImportPackage 还原并校验设备上传的文件。
func buildImportPackage(files []skills.BundleFile) (skills.Package, error) {
	decoded, err := skills.DecodeBundleFiles(files)
	if err != nil {
		return skills.Package{}, err
	}
	return skills.BuildPackage(decoded)
}

// importFailureCode 把内容校验错误转换为导入项的原因码。
func importFailureCode(err error) string {
	if errors.Is(err, skills.ErrTooLarge) {
		return "SKILL_TOO_LARGE"
	}
	return "SKILL_INVALID"
}

// deviceLabel 返回电脑名（作为技能的来源说明）。
func (h *Handler) deviceLabel(ctx context.Context, tx pgx.Tx, deviceID string) string {
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM openfox.physical_devices WHERE id = $1::uuid`, deviceID).Scan(&name); err != nil {
		return ""
	}
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	return name
}

// importRequesterReason 以发起导入的用户身份重新检查空间写权限：组织成员仍 ACTIVE、空间可读可写且未停用。
// 返回空串表示允许，否则为写进导入项的原因码。
func (h *Handler) importRequesterReason(ctx context.Context, tx pgx.Tx, organizationID, userID, spaceID string) (string, error) {
	var role, status string
	err := tx.QueryRow(ctx, `SELECT role, status FROM openfox.organization_members
		WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, userID).Scan(&role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "REQUESTER_INACTIVE", nil
	}
	if err != nil {
		return "", err
	}
	canonical, err := membership.ParseRole(role)
	if err != nil || status != "ACTIVE" {
		return "REQUESTER_INACTIVE", nil
	}
	access, err := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, organizationID, spaceID, userID)
	if errors.Is(err, collaboration.ErrSpaceNotFound) {
		return "SPACE_NOT_FOUND", nil
	}
	if err != nil {
		return "", err
	}
	actor := collaborationAuth{Actor: collaboration.Actor{UserID: userID, Role: policy.OrganizationRole(string(canonical))}}
	if !collaborationSpaceDecision(policy.ActionReadSpace, organizationID, actor, access, "").Allowed() {
		return "SPACE_NOT_FOUND", nil
	}
	if !collaborationSpaceDecision(policy.ActionUpdateObject, organizationID, actor, access, spaceID).Allowed() {
		return "FORBIDDEN", nil
	}
	if access.RequireWritable() != nil {
		return "SPACE_RETIRED", nil
	}
	return "", nil
}
