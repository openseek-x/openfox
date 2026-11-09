package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/collaboration"
	"openfox/internal/docs"
)

// 文档上传（multipart）：文件流式写入临时文件（边读边算 sha256，超限即停止），再决定节点类型。
// 存储写入在数据库事务之外进行；事务失败时删除刚写入的存储文件。

const (
	// docMultipartOverhead 为 multipart 的边界与表单字段预留的空间。
	docMultipartOverhead = 1 << 20
	// docFieldLimit 限制非文件字段的大小（容器、父节点等）。
	docFieldLimit = 64 << 10
	// docHeadBytes 是内容类型判断使用的文件头长度（与 http.DetectContentType 一致）。
	docHeadBytes = 512
)

// spooledFile 是流式写入临时文件的结果。调用方必须调用 close。
type spooledFile struct {
	file   *os.File
	size   int64
	sha256 string
	head   []byte
}

func (s spooledFile) close() {
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
}

// spoolFile 把 part 流式写入临时文件，最多读取 limit+1 字节以判断超限；超限或读取失败时清理临时文件。
func spoolFile(part io.Reader, limit int64, tooLarge error) (spooledFile, error) {
	temporary, err := os.CreateTemp("", "openfox-doc-*")
	if err != nil {
		return spooledFile{}, err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(part, limit+1))
	// 请求体超过整体上限（MaxBytesReader）时同样按超限处理，而不是 500。
	// 当前常量下由 LimitReader 先触发（读到 limit+1 字节即返回 EOF），这里只兜底：
	// 例如 file 之前的字段或 multipart 头部占满了 overhead 时，MaxBytesReader 会先于 LimitReader 触发。
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) || (err == nil && size > limit) {
		err = tooLarge
	}
	if err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return spooledFile{}, err
	}
	head := make([]byte, min(size, docHeadBytes))
	if _, err := temporary.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return spooledFile{}, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return spooledFile{}, err
	}
	return spooledFile{file: temporary, size: size, sha256: hex.EncodeToString(hash.Sum(nil)), head: head}, nil
}

// readDocUploadFields 读取 multipart 中 file 之前的普通字段，遇到 file 部分时停止并返回它（调用方负责消费）。
func readDocUploadFields(reader *multipart.Reader) (map[string]string, *multipart.Part, error) {
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, nil, err
		}
		if part.FormName() == "file" {
			return fields, part, nil
		}
		value, err := io.ReadAll(io.LimitReader(part, docFieldLimit))
		if err != nil {
			return nil, nil, err
		}
		fields[part.FormName()] = strings.TrimSpace(string(value))
	}
}

// docStorageKey 返回文档存储键：docs/<组织>/<节点或附件所属节点>/<sha256>。
func docStorageKey(organizationID, nodeID, digest string) string {
	return "docs/" + organizationID + "/" + nodeID + "/" + digest
}

// writeStoreError 把存储写入的错误映射为接口错误：存储层超限（artifacts.ErrTooLarge）为 413，其余为 500。
func writeStoreError(w http.ResponseWriter, r *http.Request, err, tooLarge error) {
	if errors.Is(err, artifacts.ErrTooLarge) {
		writeCollaborationError(w, r, tooLarge)
		return
	}
	writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
}

// storeFile 把临时文件写入存储。同键已存在时（内容相同）直接复用，返回 created = false。
func (h *Handler) storeFile(r *http.Request, key string, spool spooledFile, limit int64) (bool, error) {
	if _, err := spool.file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := h.delivery.Storage.Put(r.Context(), key, spool.file, limit); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (h *Handler) uploadDocFile(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, docs.MaxUploadBytes+docMultipartOverhead)
	reader, err := r.MultipartReader()
	var fields map[string]string
	var part *multipart.Part
	if err == nil {
		fields, part, err = readDocUploadFields(reader)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart body with a file field is required")
		return
	}
	defer part.Close()
	container, err := uploadContainer(fields)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	parentID, err := docOptionalUUID(fields["parent_id"], docs.ErrParentInvalid)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	name, err := docs.CleanFileName(part.FileName())
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	title, ext := docs.SplitExtension(name)
	spaceID := r.PathValue("spaceID")
	nodeID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	// 先做一次授权，避免无权限的请求也流式写入临时文件。
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		return access.RequireWrite(container)
	}); err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spool, err := spoolFile(part, docs.MaxUploadBytes, docs.ErrFileTooLarge)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	defer spool.close()

	input := docs.NodeInput{ID: nodeID, Container: container, ParentID: parentID, Title: title}
	var storedKey string
	created := false
	if kind := docs.TextKind(ext); kind != "" && spool.size <= docs.MaxBodyBytes {
		content, err := io.ReadAll(spool.file)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
			return
		}
		if docs.IsTextBody(content) {
			text := string(content)
			input.Kind = kind
			input.Body = &text
		}
	}
	if input.Kind == "" {
		storedKey = docStorageKey(call.organizationID, nodeID, spool.sha256)
		if created, err = h.storeFile(r, storedKey, spool, docs.MaxUploadBytes); err != nil {
			writeStoreError(w, r, err, docs.ErrFileTooLarge)
			return
		}
		input.Kind = docs.KindFile
		input.File = &docs.FileInfo{Key: storedKey, Ext: docs.StoredExt(ext), ContentType: http.DetectContentType(spool.head), SizeBytes: spool.size, SHA256: spool.sha256}
	}
	var node docs.Node
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireWrite(container); err != nil {
			return err
		}
		node, err = docs.CreateNode(ctx, tx, spaceID, call.selected.Actor.UserID, input)
		return err
	})
	if err != nil {
		if created {
			_ = h.delivery.Storage.Delete(r.Context(), storedKey)
		}
		writeCollaborationError(w, r, err)
		return
	}
	node.CanEdit = true
	writeJSON(w, http.StatusCreated, node)
}

// uploadContainer 由上传表单的 container 与 set_id 字段得到容器。
func uploadContainer(fields map[string]string) (docs.Container, error) {
	c := docs.Container{Kind: fields["container"]}
	if setID := fields["set_id"]; setID != "" {
		c.SetID = &setID
	}
	return docContainer(c)
}

func (h *Handler) uploadDocAttachment(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	r.Body = http.MaxBytesReader(w, r.Body, docs.MaxAttachmentBytes+docMultipartOverhead)
	reader, err := r.MultipartReader()
	var part *multipart.Part
	if err == nil {
		_, part, err = readDocUploadFields(reader)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "multipart body with a file field is required")
		return
	}
	defer part.Close()
	name, err := docs.CleanFileName(part.FileName())
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	// 授权：节点必须可写且是文档或表格。
	if err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		_, err := attachmentTarget(ctx, tx, call, service, spaceID, nodeID)
		return err
	}); err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spool, err := spoolFile(part, docs.MaxAttachmentBytes, docs.ErrAttachmentLarge)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	defer spool.close()
	attachmentID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	storedKey := docStorageKey(call.organizationID, nodeID, spool.sha256)
	created, err := h.storeFile(r, storedKey, spool, docs.MaxAttachmentBytes)
	if err != nil {
		writeStoreError(w, r, err, docs.ErrAttachmentLarge)
		return
	}
	info := docs.FileInfo{Key: storedKey, ContentType: http.DetectContentType(spool.head), SizeBytes: spool.size, SHA256: spool.sha256}
	var attachment docs.Attachment
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if _, err := attachmentTarget(ctx, tx, call, service, spaceID, nodeID); err != nil {
			return err
		}
		attachment, err = docs.InsertAttachment(ctx, tx, spaceID, nodeID, call.selected.Actor.UserID, attachmentID, name, info)
		return err
	})
	if err != nil {
		if created {
			_ = h.delivery.Storage.Delete(r.Context(), storedKey)
		}
		writeCollaborationError(w, r, err)
		return
	}
	attachment.URL = fmt.Sprintf("/api/v1/organizations/%s/spaces/%s/doc-attachments/%s", call.organizationID, spaceID, attachment.ID)
	writeJSON(w, http.StatusCreated, attachment)
}

// attachmentTarget 校验附件目标：节点在该空间内、调用者可写、且为文档或表格。
func attachmentTarget(ctx context.Context, tx pgx.Tx, call taskCall, service collaboration.Service, spaceID, nodeID string) (docs.Ref, error) {
	access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
	if err != nil {
		return docs.Ref{}, err
	}
	ref, err := docs.LoadRef(ctx, tx, spaceID, nodeID)
	if err != nil {
		return docs.Ref{}, err
	}
	if err := access.RequireWrite(ref.Container); err != nil {
		return docs.Ref{}, err
	}
	if ref.Kind != docs.KindDoc && ref.Kind != docs.KindSheet {
		return docs.Ref{}, docs.ErrAttachmentKind
	}
	return ref, nil
}
