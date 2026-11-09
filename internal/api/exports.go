package api

// 导出任务数据。
// 发起导出在请求内（发起人身份）校验可读空间并登记任务，文件由后台 worker 生成；
// 下载需要登录，且只有发起人或组织所有者 / 管理员可下载，文件名固定由服务端生成。

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/exports"
)

const exportRequestLimit = 8 << 10

func (h *Handler) registerExportRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	mux.HandleFunc("GET "+org+"/export-spaces", h.listExportSpaces)
	mux.HandleFunc("POST "+org+"/exports", h.createExport)
	mux.HandleFunc("GET "+org+"/exports", h.listExports)
	mux.HandleFunc("GET "+org+"/exports/{exportID}", h.getExport)
	mux.HandleFunc("GET "+org+"/exports/{exportID}/download", h.downloadExport)
}

func writeExportError(w http.ResponseWriter, r *http.Request, err error) {
	var many *exports.TooManyError
	switch {
	case errors.As(err, &many):
		writeErrorDetails(w, r, http.StatusUnprocessableEntity, "EXPORT_TOO_LARGE", "export exceeds the limits; narrow the selection",
			map[string]any{"spaces": many.Spaces, "tasks": many.Tasks, "max_spaces": exports.MaxSpaces, "max_tasks": exports.MaxTasks})
	case errors.Is(err, exports.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "you do not have permission for this action")
	case errors.Is(err, exports.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, exports.ErrSpaceMissing):
		writeError(w, r, http.StatusNotFound, "SPACE_NOT_FOUND", "space is not available")
	case errors.Is(err, exports.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	case errors.Is(err, exports.ErrExpired):
		writeError(w, r, http.StatusGone, "EXPORT_EXPIRED", "the export file has expired")
	case errors.Is(err, exports.ErrNotReady):
		writeError(w, r, http.StatusConflict, "EXPORT_NOT_READY", "the export file is not available")
	default:
		writeCollaborationError(w, r, err)
	}
}

func (h *Handler) listExportSpaces(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var items []exports.SpaceOption
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		items, err = (exports.Service{}).Spaces(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	}); err != nil {
		writeExportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": items})
}

func (h *Handler) createExport(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !idempotencyKeyPattern.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "a valid Idempotency-Key header is required")
		return
	}
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var req exports.Request
	if err := decodeJSONLimit(w, r, exportRequestLimit, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if h.storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	var job exports.Job
	var replayed bool
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, replayed, err = (exports.Service{}).Create(ctx, tx, call.organizationID, call.selected.Actor, key, req)
		return err
	}); err != nil {
		writeExportError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, job)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (h *Handler) listExports(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var items []exports.Job
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		items, err = (exports.Service{}).List(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	}); err != nil {
		writeExportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exports": items})
}

func (h *Handler) getExport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var job exports.Job
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		job, err = (exports.Service{}).Get(ctx, tx, call.organizationID, call.selected.Actor, strings.ToLower(r.PathValue("exportID")))
		return err
	}); err != nil {
		writeExportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) downloadExport(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var info exports.Download
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		info, err = (exports.Service{}).Download(ctx, tx, call.organizationID, call.selected.Actor, strings.ToLower(r.PathValue("exportID")))
		return err
	}); err != nil {
		writeExportError(w, r, err)
		return
	}
	if h.storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	file, err := h.storage.Open(r.Context(), info.Key)
	if err != nil {
		writeError(w, r, http.StatusGone, "EXPORT_EXPIRED", "the export file is no longer available")
		return
	}
	defer file.Close()
	// 文件名由服务端生成（前缀只含字母数字），仍经 mime.FormatMediaType 转义；无法编码时退回固定名称。
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": info.FileName})
	if disposition == "" {
		disposition = `attachment; filename="export"`
	}
	w.Header().Set("Content-Type", info.ContentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(info.SizeBytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}
