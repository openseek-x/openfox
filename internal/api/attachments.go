package api

import (
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/docs"
	"openfox/internal/policy"
)

// 任务附件与对话附件。文件流式写入临时文件（边读边算 sha256，超限即停止），按扩展名白名单 + 文件头嗅探判断类型，
// 再写入存储；存储写入在数据库事务之外进行，事务失败时删除刚写入的对象（每个附件独占一个对象）。

func (h *Handler) registerAttachmentRoutes(mux *http.ServeMux) {
	const org = "/v1/organizations/{organizationID}"
	mux.HandleFunc("GET "+org+"/spaces/{spaceID}/tasks/{taskID}/attachments", h.listTaskAttachments)
	mux.HandleFunc("POST "+org+"/spaces/{spaceID}/tasks/{taskID}/attachments", h.uploadTaskAttachment)
	mux.HandleFunc("POST "+org+"/conversations/{conversationID}/attachments", h.uploadConversationAttachment)
	mux.HandleFunc("GET "+org+"/attachments/{attachmentID}", h.getAttachment)
	mux.HandleFunc("DELETE "+org+"/attachments/{attachmentID}", h.deleteAttachment)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/runs/{runID}/attachments/{attachmentID}", h.getDeviceRunAttachment)
}

// writeAttachmentError 把会话域错误与协作域错误映射为接口错误。
func writeAttachmentError(w http.ResponseWriter, r *http.Request, err error) {
	for _, target := range []error{conversations.ErrNotFound, conversations.ErrForbidden, conversations.ErrInvalidRequest,
		conversations.ErrConversationClosed, conversations.ErrConflict} {
		if errors.Is(err, target) {
			writeConversationError(w, r, err)
			return
		}
	}
	writeCollaborationError(w, r, err)
}

func conversationActorOf(actor collaboration.Actor) conversations.Actor {
	return conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}
}

// readUpload 读取 multipart 里的 file 部分并流式写入临时文件，返回清理后的文件名与嗅探后的内容类型。
// authorize 在读取文件内容前调用一次，避免无权限的请求也写入临时文件。
func (h *Handler) readUpload(w http.ResponseWriter, r *http.Request, authorize func() error) (spooledFile, string, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, attachments.MaxBytes+docMultipartOverhead)
	reader, err := r.MultipartReader()
	var part *multipart.Part
	if err == nil {
		_, part, err = readDocUploadFields(reader)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart body with a file field is required")
		return spooledFile{}, "", "", false
	}
	defer part.Close()
	name, err := docs.CleanFileName(part.FileName())
	if err != nil {
		writeAttachmentError(w, r, attachments.ErrBadName)
		return spooledFile{}, "", "", false
	}
	if err := authorize(); err != nil {
		writeAttachmentError(w, r, err)
		return spooledFile{}, "", "", false
	}
	spool, err := spoolFile(part, attachments.MaxBytes, attachments.ErrTooLarge)
	if err != nil {
		writeAttachmentError(w, r, err)
		return spooledFile{}, "", "", false
	}
	contentType, err := attachments.Classify(name, spool.head)
	if err != nil {
		spool.close()
		writeAttachmentError(w, r, err)
		return spooledFile{}, "", "", false
	}
	return spool, name, contentType, true
}

// taskAccess 校验调用者对任务的权限，并确认任务属于该空间且未删除。
func taskAccess(ctx context.Context, tx pgx.Tx, call taskCall, service collaboration.Service, spaceID, taskID string, action policy.Action) error {
	if err := call.authorizeSpace(ctx, tx, service, spaceID, action, taskID); err != nil {
		return err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL)`, spaceID, taskID).Scan(&live); err != nil {
		return err
	}
	if !live {
		return attachments.ErrNotFound
	}
	return nil
}

func (h *Handler) uploadTaskAttachment(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	check := func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		return taskAccess(ctx, tx, call, service, spaceID, taskID, policy.ActionUpdateObject)
	}
	spool, name, contentType, ok := h.readUpload(w, r, func() error { return call.run(h, r, check) })
	if !ok {
		return
	}
	defer spool.close()
	id, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	key := attachments.StorageKey(call.organizationID, id)
	if _, err := h.storeFile(r, key, spool, attachments.MaxBytes); err != nil {
		writeStoreError(w, r, err, attachments.ErrTooLarge)
		return
	}
	info := attachments.FileInfo{Key: key, ContentType: contentType, SizeBytes: spool.size, SHA256: spool.sha256}
	var result attachments.Attachment
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := check(ctx, tx, service); err != nil {
			return err
		}
		var err error
		result, err = attachments.InsertTask(ctx, tx, spaceID, taskID, call.selected.Actor.UserID, id, name, info)
		return err
	})
	if err != nil {
		_ = h.delivery.Storage.Delete(r.Context(), key)
		writeAttachmentError(w, r, err)
		return
	}
	result.CanDelete = true
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) listTaskAttachments(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	var items []attachments.Attachment
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := taskAccess(ctx, tx, call, service, spaceID, taskID, policy.ActionReadObject); err != nil {
			return err
		}
		var err error
		if items, err = attachments.ListTask(ctx, tx, taskID); err != nil {
			return err
		}
		canManage := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, taskID) == nil
		canWrite := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, taskID) == nil
		for index := range items {
			items[index].CanDelete = canManage || (canWrite && items[index].UploadedBy == call.selected.Actor.UserID)
		}
		return nil
	})
	if err != nil {
		writeAttachmentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": items})
}

func (h *Handler) uploadConversationAttachment(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	conversationID := r.PathValue("conversationID")
	var spaceID string
	check := func(ctx context.Context, tx pgx.Tx) error {
		conversation, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, selection.OrganizationID, conversationID, actor)
		spaceID = conversation.SpaceID
		return err
	}
	spool, name, contentType, ok := h.readUpload(w, r, func() error {
		return h.runConversationTransaction(r, selection, check)
	})
	if !ok {
		return
	}
	defer spool.close()
	id, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	key := attachments.StorageKey(selection.OrganizationID, id)
	if _, err := h.storeFile(r, key, spool, attachments.MaxBytes); err != nil {
		writeStoreError(w, r, err, attachments.ErrTooLarge)
		return
	}
	info := attachments.FileInfo{Key: key, ContentType: contentType, SizeBytes: spool.size, SHA256: spool.sha256}
	var result attachments.Attachment
	err = h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := check(ctx, tx); err != nil {
			return err
		}
		var err error
		result, err = attachments.InsertConversation(ctx, tx, spaceID, conversationID, actor.UserID, id, name, info)
		return err
	})
	if err != nil {
		_ = h.delivery.Storage.Delete(r.Context(), key)
		writeAttachmentError(w, r, err)
		return
	}
	result.CanDelete = true
	writeJSON(w, http.StatusCreated, result)
}

// authorizeAttachment 按附件所属对象校验调用者权限。
// 读：任务附件需要任务读权限；对话附件需要会话可见，且未发送的附件只有上传人能看到。
// 删除：任务附件为上传人（仍需任务编辑权限）或空间管理员；对话附件只有上传人能删除尚未发送的附件。
func authorizeAttachment(ctx context.Context, tx pgx.Tx, call taskCall, service collaboration.Service, a attachments.Attachment, remove bool) error {
	actorID := call.selected.Actor.UserID
	if a.OwnerKind == attachments.OwnerTask {
		// 任务可能已移到别的空间：按任务当前所在空间鉴权，attachments.space_id 只是上传时的空间。
		spaceID, err := attachmentTaskSpace(ctx, tx, a)
		if err != nil {
			return err
		}
		if !remove {
			return taskAccess(ctx, tx, call, service, spaceID, a.TaskID, policy.ActionReadObject)
		}
		action := policy.ActionManageSpace
		if a.UploadedBy == actorID {
			action = policy.ActionUpdateObject
		}
		return taskAccess(ctx, tx, call, service, spaceID, a.TaskID, action)
	}
	actor := conversationActorOf(call.selected.Actor)
	if !remove {
		if _, err := (conversations.Service{}).Get(ctx, tx, call.organizationID, a.ConversationID, actor); err != nil {
			return err
		}
		if a.MessageID == "" && a.UploadedBy != actorID {
			return attachments.ErrNotFound
		}
		return nil
	}
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, call.organizationID, a.ConversationID, actor); err != nil {
		return err
	}
	if a.UploadedBy != actorID {
		return conversations.ErrForbidden
	}
	if a.MessageID != "" {
		return conversations.ErrForbidden
	}
	return nil
}

// attachmentTaskSpace 返回任务附件所属任务的当前空间（含已软删除的任务，由 taskAccess 统一按不存在处理）。
func attachmentTaskSpace(ctx context.Context, tx pgx.Tx, a attachments.Attachment) (string, error) {
	var spaceID string
	err := tx.QueryRow(ctx, `
		SELECT space_id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, a.TaskID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", attachments.ErrNotFound
	}
	return spaceID, err
}

func (h *Handler) getAttachment(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var item attachments.Attachment
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		if item, err = attachments.Load(ctx, tx, r.PathValue("attachmentID")); err != nil {
			return err
		}
		return authorizeAttachment(ctx, tx, call, service, item, false)
	})
	if err != nil {
		writeAttachmentError(w, r, err)
		return
	}
	// 默认 attachment；只有显式 ?disposition=inline 且是图片才 inline。其余类型（含 SVG / HTML，落库为纯文本）永远下载。
	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" && attachments.Previewable(item.ContentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", docs.ContentDisposition(disposition, item.Name))
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	h.streamStoredFile(w, r, item.FileKey, item.ContentType, item.SizeBytes)
}

func (h *Handler) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var key string
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		item, err := attachments.Load(ctx, tx, r.PathValue("attachmentID"))
		if err != nil {
			return err
		}
		// 读不到的人按不存在处理，不泄露附件存在与否。
		if err := authorizeAttachment(ctx, tx, call, service, item, false); err != nil {
			return err
		}
		if err := authorizeAttachment(ctx, tx, call, service, item, true); err != nil {
			return err
		}
		key, err = attachments.Delete(ctx, tx, item.ID)
		return err
	})
	if err != nil {
		writeAttachmentError(w, r, err)
		return
	}
	// 事务已提交，再删除存储对象；失败只留下孤立对象，不影响结果。
	if h.delivery.Storage != nil {
		_ = h.delivery.Storage.Delete(r.Context(), key)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getDeviceRunAttachment(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	generation, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("generation")), 10, 64)
	if err != nil || generation < 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "generation is required")
		return
	}
	var file dispatch.RunAttachmentFile
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		file, err = h.dispatch.AuthorizeRunAttachment(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), generation, r.PathValue("attachmentID"))
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", docs.ContentDisposition("attachment", file.Name))
	w.Header().Set("Cache-Control", "no-store")
	h.streamStoredFile(w, r, file.Key, file.ContentType, file.SizeBytes)
}
