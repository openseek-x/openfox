package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

const maxIconBytes = 1 << 20

var iconTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

func (h *Handler) registerViewRoutes(mux *http.ServeMux) {
	org := "/v1/organizations/{organizationID}"
	mux.HandleFunc("GET "+org+"/spaces/{spaceID}/views", h.listViews)
	mux.HandleFunc("POST "+org+"/spaces/{spaceID}/views", h.createView)
	mux.HandleFunc("PUT "+org+"/spaces/{spaceID}/views/reorder-mine", h.reorderMyViews)
	mux.HandleFunc("GET "+org+"/views/{viewID}", h.getView)
	mux.HandleFunc("PATCH "+org+"/views/{viewID}", h.updateView)
	mux.HandleFunc("DELETE "+org+"/views/{viewID}", h.deleteView)
	mux.HandleFunc("PUT "+org+"/views/{viewID}/pin-mine", h.pinMyView)
	mux.HandleFunc("PUT "+org+"/views/{viewID}/view-mode-mine", h.setMyViewMode)
	mux.HandleFunc("POST "+org+"/icons", h.uploadIcon)
	mux.HandleFunc("GET "+org+"/icons/{iconID}", h.getIcon)
}

// viewManager 校验空间可读并返回当前用户是否空间管理者；write 时停用空间返回 409。
func (c taskCall) viewManager(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID string, write bool) (bool, error) {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return false, err
	}
	if decision := collaborationSpaceDecision(policy.ActionReadSpace, c.organizationID, c.selected, access, ""); !decision.Allowed() {
		return false, decisionError{decision: decision}
	}
	if write {
		if err := access.RequireWritable(); err != nil {
			return false, err
		}
	}
	return collaborationSpaceDecision(policy.ActionManageSpace, c.organizationID, c.selected, access, "").Allowed(), nil
}

// viewCall 先按视图找到所在空间（他人的 personal 视图由 RLS 隐藏 → 404），再做空间级检查。
func (h *Handler) viewCall(w http.ResponseWriter, r *http.Request, mutation, write bool, fn func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, viewID string, manager bool) (any, error)) {
	call, ok := h.beginTaskCall(w, r, mutation)
	if !ok {
		return
	}
	viewID := r.PathValue("viewID")
	var body any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		spaceID, err := service.ViewSpaceID(ctx, tx, viewID)
		if err != nil {
			return err
		}
		manager, err := call.viewManager(ctx, tx, service, spaceID, write)
		if err != nil {
			return err
		}
		body, err = fn(ctx, tx, service, call, viewID, manager)
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

func (h *Handler) listViews(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var items []collaboration.View
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		manager, err := call.viewManager(ctx, tx, service, spaceID, false)
		if err != nil {
			return err
		}
		items, err = service.ListViews(ctx, tx, call.organizationID, call.selected.Actor, spaceID, manager)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": items})
}

func (h *Handler) createView(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Visibility  string        `json:"visibility"`
		IconKind    string        `json:"icon_kind"`
		Icon        string        `json:"icon"`
		Filters     rawJSONObject `json:"filters"`
		Display     rawJSONObject `json:"display"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	viewID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var item collaboration.View
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionCreateObject, viewID); err != nil {
			return err
		}
		manager, err := call.viewManager(ctx, tx, service, spaceID, true)
		if err != nil {
			return err
		}
		item, err = service.CreateView(ctx, tx, call.organizationID, call.selected.Actor, spaceID, collaboration.ViewInput{
			Name: input.Name, Description: input.Description, Visibility: input.Visibility, IconKind: input.IconKind,
			Icon: input.Icon, Filters: input.Filters.raw, Display: input.Display.raw,
		}, manager)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) getView(w http.ResponseWriter, r *http.Request) {
	h.viewCall(w, r, false, false, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, viewID string, manager bool) (any, error) {
		return service.GetView(ctx, tx, call.organizationID, call.selected.Actor, viewID, manager)
	})
}

func (h *Handler) updateView(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version     uint64                       `json:"version"`
		Name        *string                      `json:"name"`
		Description *string                      `json:"description"`
		Visibility  *string                      `json:"visibility"`
		IconKind    collaboration.OptionalString `json:"icon_kind"`
		Icon        string                       `json:"icon"`
		Filters     rawJSONObject                `json:"filters"`
		Display     rawJSONObject                `json:"display"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	patch := collaboration.ViewPatch{Version: input.Version, Name: input.Name, Description: input.Description,
		Visibility: input.Visibility, Icon: input.Icon, Filters: input.Filters.raw, Display: input.Display.raw}
	if input.IconKind.Set {
		kind := ""
		if input.IconKind.Value != nil {
			kind = *input.IconKind.Value
		}
		patch.IconKind = &kind
	}
	h.viewCall(w, r, true, true, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, viewID string, manager bool) (any, error) {
		return service.UpdateView(ctx, tx, call.organizationID, call.selected.Actor, viewID, patch, manager)
	})
}

func (h *Handler) deleteView(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseUint(r.URL.Query().Get("version"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "version query parameter is required")
		return
	}
	h.viewCall(w, r, true, true, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, call taskCall, viewID string, manager bool) (any, error) {
		return nil, service.DeleteView(ctx, tx, call.organizationID, call.selected.Actor, viewID, version, manager)
	})
}

func (h *Handler) pinMyView(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Pinned *bool `json:"pinned"`
	}
	if err := decodeJSON(w, r, &input); err != nil || input.Pinned == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	h.viewCall(w, r, true, false, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, _ taskCall, viewID string, _ bool) (any, error) {
		return nil, service.SetViewPrefs(ctx, tx, viewID, input.Pinned, nil)
	})
}

func (h *Handler) setMyViewMode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ViewMode collaboration.OptionalString `json:"view_mode"`
	}
	if err := decodeJSON(w, r, &input); err != nil || !input.ViewMode.Set {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	mode := ""
	if input.ViewMode.Value != nil {
		mode = *input.ViewMode.Value
	}
	h.viewCall(w, r, true, false, func(ctx context.Context, tx pgx.Tx, service collaboration.Service, _ taskCall, viewID string, _ bool) (any, error) {
		return nil, service.SetViewPrefs(ctx, tx, viewID, nil, &mode)
	})
}

func (h *Handler) reorderMyViews(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ViewIDs []string `json:"view_ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if _, err := call.viewManager(ctx, tx, service, spaceID, false); err != nil {
			return err
		}
		return service.ReorderViews(ctx, tx, spaceID, input.ViewIDs)
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) uploadIcon(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	storage := h.delivery.Storage
	if storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ICON", "multipart body with a file field is required")
		return
	}
	var data []byte
	for {
		part, err := reader.NextPart()
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ICON", "multipart body with a file field is required")
			return
		}
		if part.FormName() != "file" {
			continue
		}
		data, err = io.ReadAll(io.LimitReader(part, maxIconBytes+1))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ICON", "file could not be read")
			return
		}
		break
	}
	if len(data) > maxIconBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "ICON_TOO_LARGE", "icon must not exceed 1 MiB")
		return
	}
	// 按文件头判断类型，不信任扩展名与客户端 Content-Type；SVG 嗅探为文本，直接被拒。
	contentType := http.DetectContentType(data)
	if len(data) == 0 || !iconTypes[contentType] {
		writeError(w, r, http.StatusBadRequest, "INVALID_ICON", "icon must be a png, jpeg, webp or gif image")
		return
	}
	iconID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	key := "icons/" + call.organizationID + "/" + iconID
	sum := sha256.Sum256(data)
	if _, err := storage.Put(r.Context(), key, bytes.NewReader(data), maxIconBytes); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := service.CheckActor(ctx, tx, call.organizationID, call.selected.Actor); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO openfox.uploaded_icons (organization_id, id, storage_key, content_type, size_bytes, sha256, created_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6::uuid)`,
			iconID, key, contentType, len(data), hex.EncodeToString(sum[:]), call.selected.Actor.UserID)
		return err
	})
	if err != nil {
		_ = storage.Delete(r.Context(), key)
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": iconID, "url": collaboration.IconURL(call.organizationID, iconID),
		"content_type": contentType, "size_bytes": len(data)})
}

func (h *Handler) getIcon(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var key, contentType string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := service.CheckActor(ctx, tx, call.organizationID, call.selected.Actor); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT storage_key, content_type FROM openfox.uploaded_icons
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, r.PathValue("iconID")).Scan(&key, &contentType)
		if errors.Is(err, pgx.ErrNoRows) {
			return collaboration.ErrViewNotFound
		}
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	file, err := h.delivery.Storage.Open(r.Context(), key)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, artifacts.ErrInvalidKey) {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = io.Copy(w, file)
}

// rawJSONObject 保留请求里 filters / display 的原始 JSON；未传时 raw 为空。
type rawJSONObject struct{ raw []byte }

func (o *rawJSONObject) UnmarshalJSON(data []byte) error {
	o.raw = append([]byte(nil), data...)
	return nil
}
