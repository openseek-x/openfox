package api

// 克隆空间到另一个组织。
// 发起人必须同时是源组织的所有者 / 管理员（并能管理该空间）和目标组织的所有者 / 管理员。
// 源组织的读取在源组织的租户事务里进行，写入在目标组织的租户事务里进行，两个事务从不嵌套或合并。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/spacebundle"
	"openfox/internal/tenancy"
)

const cloneRequestLimit = 4 << 10

func (h *Handler) registerCloneRoutes(mux *http.ServeMux) {
	space := "/v1/organizations/{organizationID}/spaces/{spaceID}/clone"
	mux.HandleFunc("GET "+space+"/targets", h.listSpaceCloneTargets)
	mux.HandleFunc("POST "+space+"/preview", h.previewSpaceClone)
	mux.HandleFunc("POST "+space, h.startSpaceClone)
	jobs := "/v1/organizations/{organizationID}/clone-jobs/{jobID}"
	mux.HandleFunc("GET "+jobs, h.getSpaceCloneJob)
	mux.HandleFunc("POST "+jobs+"/cancel", h.cancelSpaceCloneJob)
}

func writeCloneError(w http.ResponseWriter, r *http.Request, err error) {
	var blocked *imports.BlockedError
	switch {
	case errors.As(err, &blocked):
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "CLONE_BLOCKED", "clone has blocking issues", map[string]any{"blockers": blocked.Blockers})
	case errors.Is(err, imports.ErrCloneTargetInvalid):
		writeError(w, r, http.StatusBadRequest, "INVALID_CLONE_TARGET", "target organization is invalid")
	case errors.Is(err, imports.ErrCloneNameTaken):
		writeError(w, r, http.StatusConflict, "NAME_TAKEN", "a space with this name already exists in the target organization")
	case errors.Is(err, imports.ErrCloneStorage):
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
	case errors.Is(err, spacebundle.ErrSpaceNotFound):
		writeError(w, r, http.StatusNotFound, "SPACE_NOT_FOUND", "space is not available")
	default:
		writeImportError(w, r, err)
	}
}

// cloneCall 是一次克隆请求里已校验的两端。
type cloneCall struct {
	source  taskCall
	target  taskCall
	spaceID string
	req     imports.CloneRequest
}

// runTarget 在目标组织的租户事务里执行 fn（发起人身份）。
func (h *Handler) runTarget(r *http.Request, c taskCall, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return h.pool.RunForOrganization(r.Context(), c.organizationID, tenancy.SourceUserRequest,
		staticCollaborationSelector(c.selected.Selection), fn)
}

// beginClone 校验源端身份、解析请求体并选择目标组织。任何一端不是所有者 / 管理员都返回 403。
func (h *Handler) beginClone(w http.ResponseWriter, r *http.Request, mutation bool) (cloneCall, bool) {
	var c cloneCall
	source, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return c, false
	}
	c.source = source
	c.spaceID = strings.ToLower(r.PathValue("spaceID"))
	if !membership.ValidateUUID(c.spaceID) {
		writeCloneError(w, r, spacebundle.ErrSpaceNotFound)
		return c, false
	}
	if !imports.CanManage(source.selected.Actor.Role) {
		writeCloneError(w, r, imports.ErrForbidden)
		return c, false
	}
	if err := decodeJSONLimit(w, r, cloneRequestLimit, &c.req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return c, false
	}
	target := strings.ToLower(strings.TrimSpace(c.req.TargetOrganizationID))
	if !membership.ValidateUUID(target) || target == source.organizationID {
		writeCloneError(w, r, imports.ErrCloneTargetInvalid)
		return c, false
	}
	c.req.TargetOrganizationID = target
	selected, err := h.selectCollaboration(r.Context(), source.selected, target)
	if err != nil || !imports.CanManage(selected.Actor.Role) {
		// 不是目标组织成员与不是管理员一视同仁，避免探测组织是否存在。
		writeCloneError(w, r, imports.ErrForbidden)
		return c, false
	}
	c.target = taskCall{organizationID: target, selected: selected}
	return c, true
}

// authorizeSource 在源组织事务里确认发起人能管理该空间。
func (c cloneCall) authorizeSource(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
	return c.source.authorizeSpace(ctx, tx, service, c.spaceID, policy.ActionManageSpace, "")
}

func (h *Handler) listSpaceCloneTargets(w http.ResponseWriter, r *http.Request) {
	source, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := strings.ToLower(r.PathValue("spaceID"))
	if !membership.ValidateUUID(spaceID) {
		writeCloneError(w, r, spacebundle.ErrSpaceNotFound)
		return
	}
	if !imports.CanManage(source.selected.Actor.Role) {
		writeCloneError(w, r, imports.ErrForbidden)
		return
	}
	if err := source.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		return source.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, "")
	}); err != nil {
		writeCloneError(w, r, err)
		return
	}
	targets := make([]map[string]string, 0)
	err := h.pool.WithTransaction(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, name, role FROM openfox_private.list_user_organizations($1::uuid) ORDER BY created_at, organization_id`,
			source.selected.Actor.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, role string
			if err := rows.Scan(&id, &name, &role); err != nil {
				return err
			}
			if id != source.organizationID && imports.CanManage(organizationRole(role)) {
				targets = append(targets, map[string]string{"id": id, "name": name})
			}
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

// buildPreview 读取源空间计数（源组织事务）并检查目标命名（目标组织事务）。
func (h *Handler) buildPreview(r *http.Request, c cloneCall) (imports.ClonePreview, imports.CloneTargetCheck, string, error) {
	var sum spacebundle.Summary
	var sourceOrgName string
	if err := c.source.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := c.authorizeSource(ctx, tx, service); err != nil {
			return err
		}
		var err error
		if sum, err = spacebundle.Summarize(ctx, tx, c.spaceID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT name FROM openfox.organizations WHERE id = openfox_private.current_organization_id()`).Scan(&sourceOrgName)
	}); err != nil {
		return imports.ClonePreview{}, imports.CloneTargetCheck{}, "", err
	}
	name, prefix, err := imports.NormalizeCloneRequest(c.req, sum.SpaceName, sum.SpacePrefix)
	if err != nil {
		return imports.ClonePreview{}, imports.CloneTargetCheck{}, "", err
	}
	var check imports.CloneTargetCheck
	if err := h.runTarget(r, c.target, func(ctx context.Context, tx pgx.Tx) error {
		if !imports.CanManage(c.target.selected.Actor.Role) {
			return imports.ErrForbidden
		}
		var err error
		check, err = imports.CheckCloneTarget(ctx, tx, name, prefix)
		return err
	}); err != nil {
		return imports.ClonePreview{}, imports.CloneTargetCheck{}, "", err
	}
	return imports.BuildClonePreview(sum, name, prefix, c.target.organizationID, check), check, sourceOrgName, nil
}

func (h *Handler) previewSpaceClone(w http.ResponseWriter, r *http.Request) {
	c, ok := h.beginClone(w, r, false)
	if !ok {
		return
	}
	preview, _, _, err := h.buildPreview(r, c)
	if err != nil {
		writeCloneError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handler) startSpaceClone(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !idempotencyKeyPattern.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "a valid Idempotency-Key header is required")
		return
	}
	c, ok := h.beginClone(w, r, true)
	if !ok {
		return
	}
	if h.storage == nil {
		writeCloneError(w, r, imports.ErrCloneStorage)
		return
	}
	preview, check, sourceOrgName, err := h.buildPreview(r, c)
	if err != nil {
		writeCloneError(w, r, err)
		return
	}
	// 命名冲突是 409，其它阻塞项是 422。但同键同请求的重试要先返回首次创建的任务（副本创建后名称已被占用）。
	if check.PrefixTaken || check.NameTaken || !preview.CanStart {
		var replay imports.CloneJob
		rerr := h.runTarget(r, c.target, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			replay, err = (imports.Service{}).FindCloneReplay(ctx, tx, c.target.organizationID, c.target.selected.Actor, key,
				c.source.organizationID, c.spaceID, preview.TargetName, preview.TargetPrefix)
			return err
		})
		if rerr == nil {
			w.Header().Set("Idempotent-Replayed", "true")
			writeJSON(w, http.StatusOK, replay)
			return
		}
		if !errors.Is(rerr, imports.ErrNoReplay) {
			writeCloneError(w, r, rerr)
			return
		}
	}
	switch {
	case check.PrefixTaken:
		writeCloneError(w, r, collaboration.ErrIdentifierTaken)
		return
	case check.NameTaken:
		writeCloneError(w, r, imports.ErrCloneNameTaken)
		return
	case !preview.CanStart:
		writeCloneError(w, r, &imports.BlockedError{Blockers: preview.Blockers})
		return
	}
	jobID, err := membership.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	bundleKey := "space-clone/" + c.target.organizationID + "/" + jobID + ".jsonl.gz"
	// 在源组织事务里（发起人身份）导出快照包并写入存储；之后 worker 只读快照包。
	if err := c.source.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := c.authorizeSource(ctx, tx, service); err != nil {
			return err
		}
		return exportBundle(ctx, h.storage, tx, bundleKey, spacebundle.Org{ID: c.source.organizationID, Name: sourceOrgName}, c.spaceID)
	}); err != nil {
		if errors.Is(err, spacebundle.ErrTooLarge) || errors.Is(err, artifacts.ErrTooLarge) {
			err = &imports.BlockedError{Blockers: []imports.CloneIssue{{Code: "bundle_limit", Message: "空间内容超过快照包大小上限，无法克隆。", Count: 0}}}
		}
		writeCloneError(w, r, err)
		return
	}
	discard := func() { _ = h.storage.Delete(context.WithoutCancel(r.Context()), bundleKey) }
	var job imports.CloneJob
	var replayed bool
	err = h.runTarget(r, c.target, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		job, replayed, err = (imports.Service{}).CreateCloneJob(ctx, tx, c.target.organizationID, c.target.selected.Actor, imports.CloneStartInput{
			JobID: jobID, IdempotencyKey: key, SourceOrganizationID: c.source.organizationID, SourceOrgName: sourceOrgName,
			SourceSpaceID: c.spaceID, Preview: preview, BundleKey: bundleKey, Request: c.req,
		})
		return err
	})
	if err != nil || replayed {
		discard()
		if err != nil {
			writeCloneError(w, r, err)
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, job)
		return
	}
	// 源组织也记一条审计；失败不影响已创建的任务。
	if err := c.source.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return imports.RecordCloneSourceAudit(ctx, tx, c.source.organizationID, c.source.selected.Actor, c.spaceID, jobID)
	}); err != nil {
		slog.Default().Warn("clone_source_audit_failed", "error", err.Error())
	}
	writeJSON(w, http.StatusCreated, job)
}

// exportBundle 把源空间流式写入存储：导出在 goroutine 里写管道，存储从管道读取。
func exportBundle(ctx context.Context, storage artifacts.Storage, tx pgx.Tx, key string, org spacebundle.Org, spaceID string) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		w := spacebundle.NewWriter(pw)
		err := spacebundle.Export(ctx, tx, org, spaceID, w)
		if err == nil {
			err = w.Close()
		}
		pw.CloseWithError(err)
		done <- err
	}()
	_, putErr := storage.Put(ctx, key, pr, artifacts.MaxArtifactBytes)
	// 存储提前失败（如超过大小）时让导出端的写入立刻返回。
	_ = pr.CloseWithError(putErr)
	exportErr := <-done
	switch {
	case exportErr != nil:
		_ = storage.Delete(context.WithoutCancel(ctx), key)
		return exportErr
	case putErr != nil:
		_ = storage.Delete(context.WithoutCancel(ctx), key)
		return putErr
	}
	return nil
}

func (h *Handler) getSpaceCloneJob(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var job imports.CloneJob
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, err = (imports.Service{}).GetClone(ctx, tx, call.organizationID, call.selected.Actor, strings.ToLower(r.PathValue("jobID")))
		return err
	})
	if err != nil {
		writeCloneError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) cancelSpaceCloneJob(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var job imports.CloneJob
	var bundleKey string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, bundleKey, err = (imports.Service{}).CancelClone(ctx, tx, call.organizationID, call.selected.Actor, strings.ToLower(r.PathValue("jobID")))
		return err
	})
	if err != nil {
		writeCloneError(w, r, err)
		return
	}
	if bundleKey != "" && h.storage != nil {
		_ = h.storage.Delete(context.WithoutCancel(r.Context()), bundleKey)
	}
	writeJSON(w, http.StatusOK, job)
}
