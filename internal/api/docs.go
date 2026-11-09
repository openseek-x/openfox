package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/docs"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// 文档库接口：文档集 CRUD、文档树（列表 / 详情 / 新建 / 保存 / 删除 / 移动）、搜索、下载与附件读取。
// 上传（multipart）见 docs_upload.go。权限一律在服务端判断，同一请求只加载一次空间授权。

const (
	docBase = "/v1/organizations/{organizationID}/spaces/{spaceID}"
	// docJSONLimit 容纳 2 MB 正文（多字节字符与转义后的 JSON）。
	docJSONLimit = 4 << 20
)

func (h *Handler) registerDocRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+docBase+"/doc-sets", h.listDocSets)
	mux.HandleFunc("POST "+docBase+"/doc-sets", h.createDocSet)
	mux.HandleFunc("PATCH "+docBase+"/doc-sets/{docSetID}", h.updateDocSet)
	mux.HandleFunc("DELETE "+docBase+"/doc-sets/{docSetID}", h.deleteDocSet)
	mux.HandleFunc("GET "+docBase+"/docs", h.listDocNodes)
	mux.HandleFunc("POST "+docBase+"/docs", h.createDocNode)
	mux.HandleFunc("POST "+docBase+"/docs/upload", h.uploadDocFile)
	mux.HandleFunc("GET "+docBase+"/docs/search", h.searchDocs)
	mux.HandleFunc("GET "+docBase+"/docs/{nodeID}", h.getDocNode)
	mux.HandleFunc("PATCH "+docBase+"/docs/{nodeID}", h.updateDocNode)
	mux.HandleFunc("GET "+docBase+"/docs/{nodeID}/revisions", h.listDocRevisions)
	mux.HandleFunc("GET "+docBase+"/docs/{nodeID}/revisions/{revision}", h.listDocRevisions)
	mux.HandleFunc("POST "+docBase+"/docs/{nodeID}/revisions/{revision}/restore", h.restoreDocRevision)
	mux.HandleFunc("DELETE "+docBase+"/docs/{nodeID}", h.deleteDocNode)
	mux.HandleFunc("POST "+docBase+"/docs/{nodeID}/move", h.moveDocNode)
	mux.HandleFunc("GET "+docBase+"/docs/{nodeID}/download", h.downloadDocNode)
	mux.HandleFunc("POST "+docBase+"/docs/{nodeID}/attachments", h.uploadDocAttachment)
	mux.HandleFunc("GET "+docBase+"/doc-attachments/{attachmentID}", h.getDocAttachment)
}

// docAccess 读取空间授权并一次性算出读、写文档集与停用三种结果；列表的 can_edit 与写入判断共用它。
// 对象类策略要求非空对象 ID：写入时传入目标对象（新建时为新生成的 ID）；只读与列表传空间 ID 作占位，写判断与具体对象无关。
func (c taskCall) docAccess(ctx context.Context, tx pgx.Tx, service collaboration.Service, spaceID, objectID string) (docs.Access, error) {
	access, err := service.LoadSpaceAccess(ctx, tx, c.organizationID, spaceID, c.selected.Actor.UserID)
	if err != nil {
		return docs.Access{}, err
	}
	read := collaborationSpaceDecision(policy.ActionReadSpace, c.organizationID, c.selected, access, "")
	write := collaborationSpaceDecision(policy.ActionUpdateObject, c.organizationID, c.selected, access, objectID)
	return docs.Access{Read: docs.DecisionError(read), Write: docs.DecisionError(write), Retired: access.RequireWritable()}, nil
}

// decodeDocJSON 与 decodeJSON 一样拒绝未知字段，但请求体上限为 4 MB（正文上限 2 MB）。
func decodeDocJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, docJSONLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// docPathID 读取路径里的 UUID；格式不对时与不存在一样返回 404。
func docPathID(r *http.Request, name string) (string, error) {
	value := r.PathValue(name)
	if !membership.ValidateUUID(value) {
		return "", docs.ErrNotFound
	}
	return value, nil
}

// docVersion 解析 version（必须是大于 0 的整数）。
func docVersion(raw string) (uint64, error) {
	version, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || version == 0 {
		return 0, collaboration.ErrInvalidVersion
	}
	return version, nil
}

// docContainer 校验容器：personal 不能带 set_id；set 必须带合法的 set_id。
func docContainer(c docs.Container) (docs.Container, error) {
	switch c.Kind {
	case docs.ContainerPersonal:
		if c.SetID != nil {
			return docs.Container{}, docs.ErrSetIDNotAllowed
		}
		return c, nil
	case docs.ContainerSet:
		if c.SetID == nil || *c.SetID == "" {
			return docs.Container{}, docs.ErrSetIDRequired
		}
		if !membership.ValidateUUID(*c.SetID) {
			return docs.Container{}, docs.ErrNotFound
		}
		return c, nil
	}
	return docs.Container{}, docs.ErrContainerInvalid
}

// docOptionalUUID 返回可选的 UUID 参数（空串为 nil）；格式不对返回 invalid 错误。
func docOptionalUUID(value string, invalid error) (*string, error) {
	if value == "" {
		return nil, nil
	}
	if !membership.ValidateUUID(value) {
		return nil, invalid
	}
	return &value, nil
}

// queryContainer 解析列表的 container 与 set_id 查询参数。
func queryContainer(values url.Values) (docs.Container, error) {
	c := docs.Container{Kind: values.Get("container")}
	if setID := values.Get("set_id"); setID != "" {
		c.SetID = &setID
	}
	return docContainer(c)
}

func (h *Handler) listDocSets(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var sets []docs.Set
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		sets, err = docs.ListSets(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		canEdit := access.RequireSetWrite() == nil
		for i := range sets {
			sets[i].CanEdit = canEdit
		}
		return nil
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"doc_sets": sets})
}

func (h *Handler) createDocSet(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input docs.SetInput
	if err := decodeDocJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	setID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var set docs.Set
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, setID)
		if err != nil {
			return err
		}
		if err := access.RequireSetWrite(); err != nil {
			return err
		}
		set, err = docs.CreateSet(ctx, tx, spaceID, call.selected.Actor.UserID, setID, input)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	set.CanEdit = true
	writeJSON(w, http.StatusCreated, set)
}

type docSetPatchRequest struct {
	docs.SetInput
	Version uint64 `json:"version"`
}

func (h *Handler) updateDocSet(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	setID, err := docPathID(r, "docSetID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var input docSetPatchRequest
	if err := decodeDocJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	var set docs.Set
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, setID)
		if err != nil {
			return err
		}
		if err := access.RequireSetWrite(); err != nil {
			return err
		}
		set, err = docs.UpdateSet(ctx, tx, spaceID, setID, input.SetInput, input.Version)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	set.CanEdit = true
	writeJSON(w, http.StatusOK, set)
}

func (h *Handler) deleteDocSet(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	setID, err := docPathID(r, "docSetID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	version, err := docVersion(r.URL.Query().Get("version"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var keys []string
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, setID)
		if err != nil {
			return err
		}
		if err := access.RequireSetWrite(); err != nil {
			return err
		}
		keys, err = docs.DeleteSet(ctx, tx, spaceID, setID, version)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.removeStoredFiles(r.Context(), keys)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listDocNodes(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	query := r.URL.Query()
	container, err := queryContainer(query)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	parentID, err := docOptionalUUID(query.Get("parent_id"), docs.ErrParentInvalid)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var nodes []docs.Node
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		if !container.IsPersonal() {
			if _, err := docs.LoadSet(ctx, tx, spaceID, *container.SetID); err != nil {
				return err
			}
		}
		nodes, err = docs.ListNodes(ctx, tx, spaceID, call.selected.Actor.UserID, container, parentID)
		if err != nil {
			return err
		}
		for i := range nodes {
			nodes[i].CanEdit = access.CanEdit(nodes[i].Container)
		}
		return nil
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

// docNodeCreateRequest 是新建节点的请求体（契约 DocNode 创建）。
type docNodeCreateRequest struct {
	Container docs.Container `json:"container"`
	ParentID  *string        `json:"parent_id"`
	Kind      string         `json:"kind"`
	Title     string         `json:"title"`
	Body      *string        `json:"body"`
}

func (h *Handler) createDocNode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input docNodeCreateRequest
	if err := decodeDocJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	container, err := docContainer(input.Container)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	parentID, err := optionalDocUUID(input.ParentID, docs.ErrParentInvalid)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	nodeID, err := collaboration.NewID()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	spaceID := r.PathValue("spaceID")
	var detail docs.NodeDetail
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireWrite(container); err != nil {
			return err
		}
		if _, err := docs.CreateNode(ctx, tx, spaceID, call.selected.Actor.UserID, docs.NodeInput{
			ID: nodeID, Container: container, ParentID: parentID, Kind: input.Kind, Title: input.Title, Body: input.Body,
		}); err != nil {
			return err
		}
		detail, err = docs.GetNodeDetail(ctx, tx, spaceID, nodeID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	detail.CanEdit = true
	writeJSON(w, http.StatusCreated, detail)
}

func (h *Handler) getDocNode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var detail docs.NodeDetail
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		detail, err = docs.GetNodeDetail(ctx, tx, spaceID, nodeID)
		if err != nil {
			return err
		}
		detail.CanEdit = access.CanEdit(detail.Container)
		return nil
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type docNodePatchRequest struct {
	Version uint64  `json:"version"`
	Title   *string `json:"title"`
	Body    *string `json:"body"`
}

func (h *Handler) updateDocNode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var input docNodePatchRequest
	if err := decodeDocJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	var detail docs.NodeDetail
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		ref, err := docs.LoadRef(ctx, tx, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireWrite(ref.Container); err != nil {
			return err
		}
		if err := docs.UpdateNode(ctx, tx, ref, call.selected.Actor.UserID, input.Version, input.Title, input.Body); err != nil {
			return err
		}
		detail, err = docs.GetNodeDetail(ctx, tx, spaceID, nodeID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	detail.CanEdit = true
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) deleteDocNode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	version, err := docVersion(r.URL.Query().Get("version"))
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var keys []string
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		ref, err := docs.LoadRef(ctx, tx, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireWrite(ref.Container); err != nil {
			return err
		}
		keys, err = docs.DeleteNode(ctx, tx, ref, call.selected.Actor.UserID, version)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	h.removeStoredFiles(r.Context(), keys)
	w.WriteHeader(http.StatusNoContent)
}

// docMoveRequest 是移动的请求体（契约 moveDocNode）。
type docMoveRequest struct {
	Version       uint64         `json:"version"`
	TargetSpaceID string         `json:"target_space_id"`
	Container     docs.Container `json:"container"`
	ParentID      *string        `json:"parent_id"`
	BeforeID      *string        `json:"before_id"`
}

func (h *Handler) moveDocNode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	var input docMoveRequest
	if err := decodeDocJSON(w, r, &input); err != nil || input.Version == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if !membership.ValidateUUID(input.TargetSpaceID) {
		writeCollaborationError(w, r, collaboration.ErrSpaceNotFound)
		return
	}
	container, err := docContainer(input.Container)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	parentID, err := optionalDocUUID(input.ParentID, docs.ErrParentInvalid)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	beforeID, err := optionalDocUUID(input.BeforeID, docs.ErrBeforeInvalid)
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var node docs.Node
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		ref, err := docs.LoadRef(ctx, tx, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := docs.MoveNode(ctx, tx, ref, call.selected.Actor.UserID, docs.MoveInput{
			Version: input.Version, TargetSpaceID: input.TargetSpaceID, Target: container, ParentID: parentID, BeforeID: beforeID,
		}, func(targetSpaceID string, c docs.Container) error {
			access, err := call.docAccess(ctx, tx, service, targetSpaceID, nodeID)
			if err != nil {
				return err
			}
			return access.RequireWrite(c)
		}); err != nil {
			return err
		}
		node, err = docs.GetNode(ctx, tx, input.TargetSpaceID, nodeID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	node.CanEdit = true
	writeJSON(w, http.StatusOK, node)
}

// optionalDocUUID 校验可选的 UUID 字段（nil 与空串都视为没有）。
func optionalDocUUID(value *string, invalid error) (*string, error) {
	if value == nil {
		return nil, nil
	}
	return docOptionalUUID(*value, invalid)
}

func (h *Handler) searchDocs(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || utf8.RuneCountInString(query) > docs.MaxSearchRunes {
		writeCollaborationError(w, r, docs.ErrQueryInvalid)
		return
	}
	spaceID := r.PathValue("spaceID")
	var nodes []docs.Node
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, spaceID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		nodes, err = docs.SearchNodes(ctx, tx, spaceID, call.selected.Actor.UserID, query)
		if err != nil {
			return err
		}
		for i := range nodes {
			nodes[i].CanEdit = access.CanEdit(nodes[i].Container)
		}
		return nil
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (h *Handler) downloadDocNode(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	nodeID, err := docPathID(r, "nodeID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var detail docs.NodeDetail
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, nodeID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		if detail, err = docs.GetNodeDetail(ctx, tx, spaceID, nodeID); err != nil {
			return err
		}
		if detail.Kind == docs.KindFolder {
			return docs.ErrFolderDownload
		}
		return nil
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	// 存储未配置时在写响应头之前返回 503，避免错误响应带上下载头。
	if detail.Kind == docs.KindFile && h.delivery.Storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	name := docs.DownloadName(detail.Kind, detail.Title, detail.FileExt, derefString(detail.ContentType))
	w.Header().Set("Content-Disposition", docs.ContentDisposition("attachment", name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if detail.Kind == docs.KindFile {
		h.streamStoredFile(w, r, detail.FileKey, derefString(detail.ContentType), derefInt64(detail.SizeBytes))
		return
	}
	body := ""
	if detail.Body != nil {
		body = *detail.Body
	}
	contentType := "text/markdown; charset=utf-8"
	if detail.Kind == docs.KindSheet {
		contentType = "text/csv; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = io.WriteString(w, body)
}

func (h *Handler) getDocAttachment(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	attachmentID, err := docPathID(r, "attachmentID")
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	spaceID := r.PathValue("spaceID")
	var attachment docs.Attachment
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		access, err := call.docAccess(ctx, tx, service, spaceID, attachmentID)
		if err != nil {
			return err
		}
		if err := access.RequireRead(); err != nil {
			return err
		}
		attachment, err = docs.LoadAttachment(ctx, tx, spaceID, attachmentID)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	// 只有图片以 inline 返回；SVG、HTML 等可执行内容一律 attachment，并加沙箱策略。
	disposition := "attachment"
	if docs.IsInlineType(attachment.ContentType) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", docs.ContentDisposition(disposition, attachment.Name))
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	h.streamStoredFile(w, r, attachment.FileKey, attachment.ContentType, attachment.SizeBytes)
}

// streamStoredFile 把存储中的文件写入响应。存储未配置返回 503，文件缺失返回 404。
func (h *Handler) streamStoredFile(w http.ResponseWriter, r *http.Request, key, contentType string, size int64) {
	storage := h.delivery.Storage
	if storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	file, err := storage.Open(r.Context(), key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "file is not available")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, file)
}

// removeStoredFiles 在数据库提交后尽力删除存储文件；失败忽略（孤儿文件可由清理任务回收）。
func (h *Handler) removeStoredFiles(ctx context.Context, keys []string) {
	storage := h.delivery.Storage
	if storage == nil {
		return
	}
	for _, key := range keys {
		_ = storage.Delete(ctx, key)
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
