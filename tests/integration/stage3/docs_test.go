package stage3

// 文档库集成测试：文档集、文档树（我的文档 / 文档集）、移动、搜索、上传下载与附件。
// 复用本目录的夹具（真实 HTTP 处理器、登录会话、租户事务）。
// 需要存储的用例用 withAvatarStorage 把存储指向 t.TempDir()；大文件用流式 multipart，避免整包读入内存。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/api"
	"openfox/internal/docs"
)

type docSetView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	IconKind *string `json:"icon_kind"`
	Icon     *string `json:"icon"`
	IconURL  *string `json:"icon_url"`
	Position int     `json:"position"`
	Version  int     `json:"version"`
	CanEdit  bool    `json:"can_edit"`
}

type docCrumb struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type docView struct {
	ID            string     `json:"id"`
	SpaceID       string     `json:"space_id"`
	Container     docContain `json:"container"`
	ParentID      *string    `json:"parent_id"`
	Kind          string     `json:"kind"`
	Title         string     `json:"title"`
	Position      int        `json:"position"`
	SizeBytes     *int64     `json:"size_bytes"`
	ContentType   *string    `json:"content_type"`
	UpdatedBy     string     `json:"updated_by"`
	UpdatedByName string     `json:"updated_by_name"`
	Version       int        `json:"version"`
	HasChildren   bool       `json:"has_children"`
	CanEdit       bool       `json:"can_edit"`
	Body          *string    `json:"body"`
	Breadcrumb    []docCrumb `json:"breadcrumb"`
}

type docContain struct {
	Kind  string  `json:"kind"`
	SetID *string `json:"set_id"`
}

type docAttachmentView struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	URL         string `json:"url"`
}

// docPersonal 与 docInSet 是容器参数。
func docPersonal() map[string]any { return map[string]any{"kind": "personal"} }

func docInSet(setID string) map[string]any { return map[string]any{"kind": "set", "set_id": setID} }

func docSpacePath(spaceID string) string { return "spaces/" + spaceID }

// newDocSet 以 client 身份新建文档集，要求返回 201。
func (e *s3Env) newDocSet(client *s3Client, spaceID, name string) docSetView {
	e.t.Helper()
	var set docSetView
	client.must(http.StatusCreated, http.MethodPost, docSpacePath(spaceID)+"/doc-sets", map[string]any{"name": name}, &set)
	return set
}

// newDocNode 以 client 身份新建节点，要求返回 201。
func (e *s3Env) newDocNode(client *s3Client, spaceID string, body map[string]any) docView {
	e.t.Helper()
	var node docView
	client.must(http.StatusCreated, http.MethodPost, docSpacePath(spaceID)+"/docs", body, &node)
	return node
}

// docNodeIn 在容器的父节点下新建节点（kind 与 title），返回详情。
func (e *s3Env) docNodeIn(client *s3Client, spaceID string, container map[string]any, parentID, kind, title string) docView {
	e.t.Helper()
	body := map[string]any{"container": container, "kind": kind, "title": title}
	if parentID != "" {
		body["parent_id"] = parentID
	}
	return e.newDocNode(client, spaceID, body)
}

// listDocs 列出容器内某个父节点下的子节点（parentID 为空表示容器根）。
func (e *s3Env) listDocs(client *s3Client, spaceID, query string) []docView {
	e.t.Helper()
	var out struct {
		Nodes []docView `json:"nodes"`
	}
	client.must(http.StatusOK, http.MethodGet, docSpacePath(spaceID)+"/docs?"+query, nil, &out)
	return out.Nodes
}

// getDoc 读取节点详情，要求 200。
func (e *s3Env) getDoc(client *s3Client, spaceID, nodeID string) docView {
	e.t.Helper()
	var node docView
	client.must(http.StatusOK, http.MethodGet, docSpacePath(spaceID)+"/docs/"+nodeID, nil, &node)
	return node
}

// searchDocs 搜索文档（term 为原始搜索词），返回节点列表。
func (e *s3Env) searchDocs(client *s3Client, spaceID, term string) []docView {
	e.t.Helper()
	var out struct {
		Nodes []docView `json:"nodes"`
	}
	client.must(http.StatusOK, http.MethodGet, docSpacePath(spaceID)+"/docs/search?q="+neturl.QueryEscape(term), nil, &out)
	return out.Nodes
}

// keyOf 读取一个存储键（文件路径相对存储根）。
func (e *s3Env) keyOf(query string, args ...any) string {
	e.t.Helper()
	var key string
	e.scalar(&key, query, args...)
	return key
}

// raw 发送原始请求体（multipart 或二进制）；写请求带 CSRF 头。
func (c *s3Client) raw(method, path string, body io.Reader, contentType string) s3Response {
	c.env.t.Helper()
	if !strings.HasPrefix(path, "/") {
		path = "/v1/organizations/" + c.env.org + "/" + path
	}
	request, err := http.NewRequestWithContext(c.env.ctx, method, c.server.URL+path, body)
	if err != nil {
		c.env.t.Fatal(err)
	}
	request.Header.Set("Content-Type", contentType)
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.http.Do(request)
	if err != nil {
		c.env.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		c.env.t.Fatal(err)
	}
	return s3Response{Status: response.StatusCode, Header: response.Header, Body: payload}
}

// docMultipart 把普通字段与一个 file 字段编码为 multipart 请求体。
func docMultipart(t *testing.T, fields map[string]string, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

// uploadDocFile 上传到文档库（POST docs/upload）。
func uploadDocFile(t *testing.T, client *s3Client, spaceID string, fields map[string]string, filename string, data []byte) s3Response {
	t.Helper()
	body, contentType := docMultipart(t, fields, filename, data)
	return client.raw(http.MethodPost, docSpacePath(spaceID)+"/docs/upload", body, contentType)
}

// uploadDocAttachment 上传附件到节点。
func uploadDocAttachment(t *testing.T, client *s3Client, spaceID, nodeID, filename string, data []byte) s3Response {
	t.Helper()
	body, contentType := docMultipart(t, nil, filename, data)
	return client.raw(http.MethodPost, docSpacePath(spaceID)+"/docs/"+nodeID+"/attachments", body, contentType)
}

// zeroReader 无限输出 0 字节，用于流式构造超大上传。
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// streamUpload 以流式 multipart 发送 size 字节的 file（不在内存中构造整个请求体）。
func streamUpload(t *testing.T, client *s3Client, path string, fields map[string]string, filename string, size int64) s3Response {
	t.Helper()
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		var err error
		for key, value := range fields {
			if err = form.WriteField(key, value); err != nil {
				break
			}
		}
		var part io.Writer
		if err == nil {
			part, err = form.CreateFormFile("file", filename)
		}
		if err == nil {
			_, err = io.CopyN(part, zeroReader{}, size)
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	return client.raw(http.MethodPost, path, reader, form.FormDataContentType())
}

// downloadHeaders 断言下载响应的安全头。
func downloadHeaders(t *testing.T, response s3Response) {
	t.Helper()
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff: %v", response.Header)
	}
}

func TestDocSetsPermissionsAndValidation(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")

	set := env.newDocSet(manager, space, "  产品文档  ")
	if set.Name != "产品文档" || !set.CanEdit || set.Version != 1 {
		t.Fatalf("created set = %+v", set)
	}
	env.newDocSet(env.member(), space, "成员创建")

	var list struct {
		DocSets []docSetView `json:"doc_sets"`
	}
	env.member().must(http.StatusOK, http.MethodGet, docSpacePath(space)+"/doc-sets", nil, &list)
	if len(list.DocSets) != 2 || !list.DocSets[0].CanEdit {
		t.Fatalf("member list = %+v", list.DocSets)
	}
	env.other().must(http.StatusOK, http.MethodGet, docSpacePath(space)+"/doc-sets", nil, &list)
	if len(list.DocSets) != 2 || list.DocSets[0].CanEdit {
		t.Fatalf("viewer can read but must not edit: %+v", list.DocSets)
	}
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": "只读"})
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, docSpacePath(space)+"/doc-sets", nil)
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": "跨租户"})

	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": "   "})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": strings.Repeat("名", docs.MaxSetNameRunes+1)})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": "图标", "icon": map[string]any{"kind": "upload", "value": "00000000-0000-0000-0000-000000000000"}})

	var emoji docSetView
	manager.must(http.StatusOK, http.MethodPatch, fmt.Sprintf("%s/doc-sets/%s", docSpacePath(space), set.ID),
		map[string]any{"name": "产品", "icon": map[string]any{"kind": "emoji", "value": "📄"}, "version": set.Version}, &emoji)
	if emoji.Version != 2 || emoji.IconKind == nil || *emoji.IconKind != "emoji" || emoji.Name != "产品" {
		t.Fatalf("updated set = %+v", emoji)
	}
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, fmt.Sprintf("%s/doc-sets/%s", docSpacePath(space), set.ID),
		map[string]any{"name": "旧版本", "version": set.Version})
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, fmt.Sprintf("%s/doc-sets/%s", docSpacePath(space), set.ID),
		map[string]any{"name": "只读不能改", "version": 2})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, docSpacePath(space)+"/doc-sets/"+s3NewID(t),
		map[string]any{"name": "不存在", "version": 1})

	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodDelete,
		fmt.Sprintf("%s/doc-sets/%s?version=2", docSpacePath(space), set.ID), nil)
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete,
		fmt.Sprintf("%s/doc-sets/%s?version=1", docSpacePath(space), set.ID), nil)
	manager.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s/doc-sets/%s?version=2", docSpacePath(space), set.ID), nil, nil)
	if n := env.count(`SELECT count(*) FROM openfox.doc_sets WHERE id = $1::uuid`, set.ID); n != 0 {
		t.Fatalf("deleted set rows = %d", n)
	}
}

func TestDocSetLimitPerSpace(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.exec(`INSERT INTO openfox.doc_sets (organization_id, space_id, name, position, created_by, version)
		SELECT $1::uuid, $2::uuid, 'bulk ' || g, g, $3::uuid, 1 FROM generate_series(1, 199) AS g`, env.org, space, s3ManagerID)
	env.newDocSet(manager, space, "第 200 个")
	manager.expectError(http.StatusConflict, "DOC_SET_LIMIT", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": "第 201 个"})
}

func TestDocRetiredSpaceIsReadOnly(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	set := env.newDocSet(manager, space, "停用前")
	personal := env.docNodeIn(manager, space, docPersonal(), "", docs.KindDoc, "我的文档")

	current := env.getSpace(manager, space)
	manager.must(http.StatusOK, http.MethodPost, docSpacePath(space)+"/retire",
		map[string]any{"version": current.Version, "open_tasks": "cancel"}, nil)

	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, docSpacePath(space)+"/doc-sets",
		map[string]any{"name": "停用后"})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": docInSet(set.ID), "kind": "doc", "title": "停用后"})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": docPersonal(), "kind": "doc", "title": "停用后"})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPatch,
		fmt.Sprintf("%s/docs/%s", docSpacePath(space), personal.ID), map[string]any{"version": personal.Version, "title": "改名"})

	var sets struct {
		DocSets []docSetView `json:"doc_sets"`
	}
	manager.must(http.StatusOK, http.MethodGet, docSpacePath(space)+"/doc-sets", nil, &sets)
	if len(sets.DocSets) != 1 || sets.DocSets[0].CanEdit {
		t.Fatalf("retired space sets = %+v", sets.DocSets)
	}
	if got := env.getDoc(manager, space, personal.ID); got.CanEdit {
		t.Fatalf("personal can_edit in retired space = true")
	}
}

func TestDocPersonalIsPrivateToOwner(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")

	folder := env.docNodeIn(manager, space, docPersonal(), "", docs.KindFolder, "我的文件夹")
	secret := env.docNodeIn(manager, space, docPersonal(), folder.ID, docs.KindDoc, "私人笔记")

	if n := len(env.listDocs(manager, space, "container=personal")); n != 1 {
		t.Fatalf("owner root personal = %d", n)
	}
	if n := len(env.listDocs(env.other(), space, "container=personal")); n != 0 {
		t.Fatalf("viewer sees %d personal roots, want 0", n)
	}
	if n := len(env.listDocs(env.member(), space, "container=personal&parent_id="+folder.ID)); n != 0 {
		t.Fatalf("member sees owner children")
	}
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, docSpacePath(space)+"/docs/"+secret.ID, nil)
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, docSpacePath(space)+"/docs/"+secret.ID,
		map[string]any{"version": secret.Version, "title": "抢改"})
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete,
		fmt.Sprintf("%s/docs/%s?version=%d", docSpacePath(space), secret.ID, secret.Version), nil)

	if hits := env.searchDocs(env.member(), space, "私人"); len(hits) != 0 {
		t.Fatalf("member search leaked personal docs: %+v", hits)
	}
	if hits := env.searchDocs(manager, space, "私人"); len(hits) != 1 {
		t.Fatalf("owner search = %+v", hits)
	}

	// 只要空间可读即可在「我的文档」里创建（只读成员也可以）。
	if own := env.docNodeIn(env.other(), space, docPersonal(), "", docs.KindDoc, "只读成员的笔记"); !own.CanEdit {
		t.Fatalf("viewer personal can_edit = false")
	}
}

func TestDocNodeCreateRules(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	set := env.newDocSet(manager, space, "规则")
	setBox := docInSet(set.ID)

	folder := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "目录")
	second := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "目录二")
	if folder.Position != 0 || second.Position != 1 {
		t.Fatalf("positions = %d, %d; want 0, 1 (appended last)", folder.Position, second.Position)
	}
	if folder.Container.Kind != "set" || folder.Container.SetID == nil || *folder.Container.SetID != set.ID {
		t.Fatalf("container = %+v", folder.Container)
	}

	doc := env.newDocNode(manager, space, map[string]any{"container": setBox, "parent_id": folder.ID, "kind": "doc", "title": "正文", "body": "# 标题\n内容"})
	detail := env.getDoc(manager, space, doc.ID)
	if detail.Body == nil || *detail.Body != "# 标题\n内容" || len(detail.Breadcrumb) != 1 || detail.Breadcrumb[0].ID != folder.ID {
		t.Fatalf("detail = %+v", detail)
	}
	empty := env.newDocNode(manager, space, map[string]any{"container": setBox, "kind": "sheet", "title": "   "})
	if empty.Title != "" || empty.Kind != "sheet" {
		t.Fatalf("empty-title sheet = %+v", empty)
	}

	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "folder", "title": "  "})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "folder", "title": "带正文", "body": "x"})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "doc", "title": strings.Repeat("x", docs.MaxTitleRunes+1)})
	manager.expectError(http.StatusBadRequest, "DOC_BODY_TOO_LARGE", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "doc", "title": "大正文二", "body": strings.Repeat("a", docs.MaxBodyBytes+1)})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "file", "title": "文件"})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": map[string]any{"kind": "set"}, "kind": "doc", "title": "缺集合"})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": map[string]any{"kind": "personal", "set_id": set.ID}, "kind": "doc", "title": "个人带集合"})

	// 父节点必须是同一容器里的文件夹：文档不行、其他容器的文件夹不行、不存在的也不行。
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "parent_id": doc.ID, "kind": "doc", "title": "挂在文档下"})
	personalFolder := env.docNodeIn(manager, space, docPersonal(), "", docs.KindFolder, "个人目录")
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "parent_id": personalFolder.ID, "kind": "doc", "title": "跨容器"})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "parent_id": s3NewID(t), "kind": "doc", "title": "不存在的父节点"})

	// 树深 10 层：第 11 层返回 400。
	parent := folder.ID
	for depth := 2; depth <= docs.MaxDepth; depth++ {
		parent = env.docNodeIn(manager, space, setBox, parent, docs.KindFolder, fmt.Sprintf("第 %d 层", depth)).ID
	}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "parent_id": parent, "kind": "doc", "title": "第 11 层"})

	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "doc", "title": "只读不能写"})
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "doc", "title": "跨租户"})
}

func TestDocNodeUpdateBodyAndVersion(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	setBox := docInSet(env.newDocSet(manager, space, "正文").ID)
	folder := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "目录")
	doc := env.newDocNode(manager, space, map[string]any{"container": setBox, "parent_id": folder.ID, "kind": "doc", "title": "草稿"})
	path := fmt.Sprintf("%s/docs/%s", docSpacePath(space), doc.ID)

	var saved docView
	manager.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": doc.Version, "body": "第一版"}, &saved)
	if saved.Version != doc.Version+1 || saved.Body == nil || *saved.Body != "第一版" || saved.UpdatedByName == "" || !saved.CanEdit {
		t.Fatalf("saved = %+v", saved)
	}
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, path,
		map[string]any{"version": doc.Version, "body": "旧版本覆盖"})
	var renamed docView
	manager.must(http.StatusOK, http.MethodPatch, path, map[string]any{"version": saved.Version, "title": "  正式标题  "}, &renamed)
	if renamed.Title != "正式标题" || renamed.Body == nil || *renamed.Body != "第一版" {
		t.Fatalf("rename kept body = %+v", renamed)
	}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch,
		fmt.Sprintf("%s/docs/%s", docSpacePath(space), folder.ID), map[string]any{"version": folder.Version, "body": "文件夹没有正文"})
	manager.expectError(http.StatusBadRequest, "DOC_BODY_TOO_LARGE", http.MethodPatch, path,
		map[string]any{"version": renamed.Version, "body": strings.Repeat("b", docs.MaxBodyBytes+1)})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, docSpacePath(space)+"/docs/"+s3NewID(t),
		map[string]any{"version": 1, "title": "不存在"})
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, path,
		map[string]any{"version": renamed.Version, "title": "只读"})
}

func TestDocDeleteCascadesNodesAndAttachments(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	manager := env.manager()
	space := env.space.ID
	setID := env.newDocSet(manager, space, "级联").ID
	setBox := docInSet(setID)
	folder := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "目录")
	doc := env.newDocNode(manager, space, map[string]any{"container": setBox, "parent_id": folder.ID, "kind": "doc", "title": "正文"})
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	attach := uploadDocAttachment(t, manager, space, doc.ID, "pic.png", png)
	if attach.Status != http.StatusCreated {
		t.Fatalf("attachment = %d %s", attach.Status, attach.Body)
	}
	fileResp := uploadDocFile(t, manager, space, map[string]string{"container": "set", "set_id": setID, "parent_id": folder.ID}, "photo.png", png)
	if fileResp.Status != http.StatusCreated {
		t.Fatalf("file = %d %s", fileResp.Status, fileResp.Body)
	}
	var fileNode docView
	fileResp.decode(t, &fileNode)
	var attachment docAttachmentView
	attach.decode(t, &attachment)
	attachmentFile := filepath.Join(root, filepath.FromSlash(env.keyOf(`SELECT file_key FROM openfox.doc_attachments WHERE id = $1::uuid`, attachment.ID)))
	fileFile := filepath.Join(root, filepath.FromSlash(env.keyOf(`SELECT file_key FROM openfox.doc_nodes WHERE id = $1::uuid`, fileNode.ID)))
	if _, err := os.Stat(attachmentFile); err != nil {
		t.Fatalf("attachment file missing before delete: %v", err)
	}

	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete,
		fmt.Sprintf("%s/docs/%s?version=%d", docSpacePath(space), folder.ID, folder.Version+5), nil)
	manager.must(http.StatusNoContent, http.MethodDelete,
		fmt.Sprintf("%s/docs/%s?version=%d", docSpacePath(space), folder.ID, folder.Version), nil, nil)
	if n := env.count(`SELECT count(*) FROM openfox.doc_nodes WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, folder.ID, doc.ID, fileNode.ID); n != 0 {
		t.Fatalf("subtree nodes left = %d", n)
	}
	if n := env.count(`SELECT count(*) FROM openfox.doc_attachments WHERE id = $1::uuid`, attachment.ID); n != 0 {
		t.Fatalf("attachment rows left = %d", n)
	}
	for _, path := range []string{attachmentFile, fileFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stored file %s still exists after delete: %v", path, err)
		}
	}

	// 删除文档集级联删除其中的全部文档。
	manager.must(http.StatusCreated, http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "doc", "title": "集里的文档"}, nil)
	manager.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s/doc-sets/%s?version=1", docSpacePath(space), setID), nil, nil)
	if n := env.count(`SELECT count(*) FROM openfox.doc_nodes WHERE set_id = $1::uuid`, setID); n != 0 {
		t.Fatalf("set nodes left after set delete = %d", n)
	}
}

func TestDocMoveReordersAndRejectsCycles(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	setBox := docInSet(env.newDocSet(manager, space, "移动").ID)
	a := env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "A")
	b := env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "B")
	c := env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "C")

	movePath := func(node docView) string { return fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), node.ID) }
	var moved docView
	manager.must(http.StatusOK, http.MethodPost, movePath(c), map[string]any{
		"version": c.Version, "target_space_id": space, "container": setBox, "before_id": a.ID,
	}, &moved)
	if moved.Position != 0 {
		t.Fatalf("moved position = %d, want 0", moved.Position)
	}
	order := env.listDocs(manager, space, "container=set&set_id="+setBox["set_id"].(string))
	if len(order) != 3 || order[0].ID != c.ID || order[1].ID != a.ID || order[2].ID != b.ID {
		t.Fatalf("order after move = %+v", order)
	}

	folder := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "父")
	child := env.docNodeIn(manager, space, setBox, folder.ID, docs.KindFolder, "子")
	grandchild := env.docNodeIn(manager, space, setBox, child.ID, docs.KindDoc, "孙")
	manager.expectError(http.StatusBadRequest, "DOC_MOVE_INTO_SELF", http.MethodPost, movePath(folder), map[string]any{
		"version": folder.Version, "target_space_id": space, "container": setBox, "parent_id": grandchild.ID,
	})
	manager.expectError(http.StatusBadRequest, "DOC_MOVE_INTO_SELF", http.MethodPost, movePath(folder), map[string]any{
		"version": folder.Version, "target_space_id": space, "container": setBox, "parent_id": folder.ID,
	})
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, movePath(a), map[string]any{
		"version": a.Version, "target_space_id": space, "container": setBox, "parent_id": b.ID,
	})
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, movePath(a), map[string]any{
		"version": a.Version + 9, "target_space_id": space, "container": setBox,
	})
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, movePath(a), map[string]any{
		"version": a.Version, "target_space_id": space, "container": setBox,
	})

	// 并发：两个节点互相移入对方，只能成功一个，且不能形成环。
	x := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "X")
	y := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "Y")
	statuses := parallel(2, func(index int) int {
		source, target := x, y
		if index == 1 {
			source, target = y, x
		}
		response := manager.do(http.MethodPost, movePath(source), map[string]any{
			"version": source.Version, "target_space_id": space, "container": setBox, "parent_id": target.ID,
		})
		return response.Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusBadRequest) != 1 {
		t.Fatalf("concurrent moves = %v, want one 200 and one 400", statuses)
	}
	var cycles int
	env.scalar(&cycles, `SELECT count(*) FROM openfox.doc_nodes p JOIN openfox.doc_nodes q ON p.parent_id = q.id AND q.parent_id = p.id
		WHERE p.id IN ($1::uuid, $2::uuid)`, x.ID, y.ID)
	if cycles != 0 {
		t.Fatalf("cycle formed between X and Y")
	}
}

func TestDocMoveAcrossContainersAndSpaces(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	setID := env.newDocSet(manager, space, "团队").ID
	setBox := docInSet(setID)

	// 我的文档 → 文档集：子树的 set_id、owner 一起变化。
	folder := env.docNodeIn(manager, space, docPersonal(), "", docs.KindFolder, "我的目录")
	child := env.docNodeIn(manager, space, docPersonal(), folder.ID, docs.KindDoc, "我的文档")
	var moved docView
	manager.must(http.StatusOK, http.MethodPost, fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), folder.ID), map[string]any{
		"version": folder.Version, "target_space_id": space, "container": setBox,
	}, &moved)
	if moved.Container.Kind != "set" || moved.Container.SetID == nil || *moved.Container.SetID != setID {
		t.Fatalf("moved container = %+v", moved.Container)
	}
	if got := env.getDoc(manager, space, child.ID); got.Container.Kind != "set" || got.Container.SetID == nil || *got.Container.SetID != setID {
		t.Fatalf("child did not follow subtree: %+v", got.Container)
	}
	if n := len(env.listDocs(manager, space, "container=personal")); n != 0 {
		t.Fatalf("personal root still lists %d nodes after move", n)
	}

	// 文档集 → 我的文档。
	back := env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "回家")
	manager.must(http.StatusOK, http.MethodPost, fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), back.ID), map[string]any{
		"version": back.Version, "target_space_id": space, "container": docPersonal(),
	}, nil)
	if n := len(env.listDocs(manager, space, "container=personal")); n != 1 {
		t.Fatalf("personal root after move back = %d", n)
	}

	// 跨空间：目标空间的文档集需要写权限，个人容器只需可读；目标空间不可读返回 404。
	otherSpace := env.createSpace("private")
	env.addSpaceMember(otherSpace.ID, s3OtherID, "VIEWER")
	otherSet := env.newDocSet(env.owner(), otherSpace.ID, "其他空间")
	note := env.docNodeIn(env.other(), space, docPersonal(), "", docs.KindDoc, "只读成员的笔记")
	env.other().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), note.ID), map[string]any{
		"version": note.Version, "target_space_id": otherSpace.ID, "container": docInSet(otherSet.ID),
	})
	env.other().must(http.StatusOK, http.MethodPost, fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), note.ID), map[string]any{
		"version": note.Version, "target_space_id": otherSpace.ID, "container": docPersonal(),
	}, nil)
	if got := env.getDoc(env.other(), otherSpace.ID, note.ID); got.Container.Kind != "personal" {
		t.Fatalf("note after cross-space move = %+v", got.Container)
	}
	hidden := env.docNodeIn(manager, space, docPersonal(), "", docs.KindDoc, "无权限空间")
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), hidden.ID), map[string]any{
		"version": hidden.Version, "target_space_id": otherSpace.ID, "container": docPersonal(),
	})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), hidden.ID), map[string]any{
		"version": hidden.Version, "target_space_id": s3NewID(t), "container": docPersonal(),
	})
}

func TestDocSearchEscapesAndScopes(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setBox := docInSet(env.newDocSet(manager, space, "搜索").ID)
	env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "100% 完成报告")
	env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "100X 完成报告")
	env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "under_score")
	env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "underXscore")
	env.docNodeIn(manager, space, docPersonal(), "", docs.KindDoc, "Report 私人")

	if hits := env.searchDocs(manager, space, "%"); len(hits) != 1 || hits[0].Title != "100% 完成报告" {
		t.Fatalf("search percent = %+v", hits)
	}
	if hits := env.searchDocs(manager, space, "_"); len(hits) != 1 || hits[0].Title != "under_score" {
		t.Fatalf("search underscore = %+v", hits)
	}
	if hits := env.searchDocs(manager, space, "report"); len(hits) != 1 {
		t.Fatalf("case-insensitive search = %+v", hits)
	}
	if hits := env.searchDocs(env.member(), space, "report"); len(hits) != 0 {
		t.Fatalf("member search leaked owner docs: %+v", hits)
	}
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, docSpacePath(space)+"/docs/search?q=%20%20", nil)
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet,
		docSpacePath(space)+"/docs/search?q="+strings.Repeat("a", docs.MaxSearchRunes+1), nil)
}

func TestDocUploadClassifiesAndCleansNames(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	setID := env.newDocSet(manager, space, "上传").ID
	fields := map[string]string{"container": "set", "set_id": setID}

	md := uploadDocFile(t, manager, space, fields, "需求说明.md", []byte("# 需求\n正文"))
	if md.Status != http.StatusCreated {
		t.Fatalf("md upload = %d %s", md.Status, md.Body)
	}
	var doc docView
	md.decode(t, &doc)
	if doc.Kind != docs.KindDoc || doc.Title != "需求说明" {
		t.Fatalf("md node = %+v", doc)
	}
	if body := env.getDoc(manager, space, doc.ID).Body; body == nil || *body != "# 需求\n正文" {
		t.Fatalf("md body = %v", body)
	}
	csv := uploadDocFile(t, manager, space, fields, "data.CSV", []byte("a,b\n1,2"))
	var sheet docView
	csv.decode(t, &sheet)
	if sheet.Kind != docs.KindSheet || sheet.Title != "data" {
		t.Fatalf("csv node = %+v", sheet)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 128)...)
	image := uploadDocFile(t, manager, space, fields, "photo.png", png)
	var file docView
	image.decode(t, &file)
	if file.Kind != docs.KindFile || file.Title != "photo" || file.ContentType == nil || *file.ContentType != "image/png" ||
		file.SizeBytes == nil || *file.SizeBytes != int64(len(png)) || file.Body != nil {
		t.Fatalf("png node = %+v", file)
	}
	// 非法 UTF-8 的 .md 与超过 2 MB 的 .md 都按 file 处理。
	if invalid := uploadDocFile(t, manager, space, fields, "bad.md", []byte{0xff, 0xfe, 'x'}); invalid.Status != http.StatusCreated {
		t.Fatalf("invalid utf-8 upload = %d", invalid.Status)
	} else {
		var node docView
		invalid.decode(t, &node)
		if node.Kind != docs.KindFile {
			t.Fatalf("invalid utf-8 kind = %s", node.Kind)
		}
	}
	big := uploadDocFile(t, manager, space, fields, "big.md", bytes.Repeat([]byte("a"), docs.MaxBodyBytes+1))
	var bigNode docView
	big.decode(t, &bigNode)
	if bigNode.Kind != docs.KindFile {
		t.Fatalf("oversized markdown kind = %s", bigNode.Kind)
	}
	// 路径成分被去掉。
	traversal := uploadDocFile(t, manager, space, fields, "../../secret/evil.md", []byte("x"))
	var evil docView
	traversal.decode(t, &evil)
	if evil.Title != "evil" || evil.Kind != docs.KindDoc {
		t.Fatalf("path not cleaned: %+v", evil)
	}
	uploadDocFile(t, manager, space, fields, "bad\x01name.md", []byte("x")).expectStatus(t, http.StatusBadRequest)
	uploadDocFile(t, manager, space, map[string]string{"container": "set"}, "a.md", []byte("x")).expectStatus(t, http.StatusBadRequest)
	uploadDocFile(t, env.other(), space, fields, "a.md", []byte("x")).expectStatus(t, http.StatusForbidden)
	personal := uploadDocFile(t, env.other(), space, map[string]string{"container": "personal"}, "只读成员.md", []byte("x"))
	personal.expectStatus(t, http.StatusCreated)

	// 下载：doc 为 .md、sheet 为 .csv，file 为原文件；都是 attachment + nosniff。
	download := manager.do(http.MethodGet, fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), doc.ID), nil)
	if download.Status != http.StatusOK || string(download.Body) != "# 需求\n正文" ||
		!strings.HasPrefix(download.Header.Get("Content-Disposition"), `attachment; filename="____.md"; filename*=UTF-8''`) {
		t.Fatalf("download doc = %d %v %s", download.Status, download.Header, download.Body)
	}
	downloadHeaders(t, download)
	if got := manager.do(http.MethodGet, fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), sheet.ID), nil); !strings.Contains(got.Header.Get("Content-Disposition"), "data.csv") {
		t.Fatalf("download sheet disposition = %s", got.Header.Get("Content-Disposition"))
	}
	fileDownload := manager.do(http.MethodGet, fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), file.ID), nil)
	if fileDownload.Status != http.StatusOK || !bytes.Equal(fileDownload.Body, png) || fileDownload.Header.Get("Content-Type") != "image/png" ||
		!strings.Contains(fileDownload.Header.Get("Content-Disposition"), `filename="photo.png"`) {
		t.Fatalf("download file = %d %v", fileDownload.Status, fileDownload.Header)
	}
	// 没有扩展名的文件：file_ext 为空，下载名由 content_type 推断。
	noExt := uploadDocFile(t, manager, space, fields, "README", png)
	var noExtNode docView
	noExt.decode(t, &noExtNode)
	noExtDownload := manager.do(http.MethodGet, fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), noExtNode.ID), nil)
	if !strings.Contains(noExtDownload.Header.Get("Content-Disposition"), `filename="README.png"`) {
		t.Fatalf("no-extension download = %v", noExtDownload.Header)
	}
	// 上传时保存的原扩展名（大写会被规范为小写）用于下载文件名。
	upperExt := uploadDocFile(t, manager, space, fields, "Scan.JPEG", png)
	var upper docView
	upperExt.decode(t, &upper)
	upperDownload := manager.do(http.MethodGet, fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), upper.ID), nil)
	if !strings.Contains(upperDownload.Header.Get("Content-Disposition"), `filename="Scan.jpeg"`) {
		t.Fatalf("upper-case extension download = %v", upperDownload.Header)
	}
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet,
		fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), setID), nil)
	folder := env.docNodeIn(manager, space, docInSet(setID), "", docs.KindFolder, "目录")
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet,
		fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), folder.ID), nil)
}

func (r s3Response) expectStatus(t *testing.T, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status = %d %s, want %d", r.Status, r.Body, want)
	}
}

func TestDocUploadStorageUnavailableAndTooLarge(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setID := env.newDocSet(manager, space, "无存储").ID
	fields := map[string]string{"container": "set", "set_id": setID}
	uploadDocFile(t, manager, space, fields, "a.png", []byte("\x89PNG\r\n\x1a\n")).
		expectStatus(t, http.StatusServiceUnavailable)
	uploadDocAttachment(t, manager, space, s3NewID(t), "a.png", []byte("x")).expectStatus(t, http.StatusServiceUnavailable)

	withAvatarStorage(env, t.TempDir())
	manager = env.manager()
	tooLarge := streamUpload(t, manager, docSpacePath(space)+"/docs/upload", fields, "huge.bin", docs.MaxUploadBytes+1)
	if tooLarge.Status != http.StatusRequestEntityTooLarge || tooLarge.code() != "DOC_FILE_TOO_LARGE" {
		t.Fatalf("oversized upload = %d %s", tooLarge.Status, tooLarge.Body)
	}
	// 请求体远超整体上限（触发 MaxBytesReader）时仍然是 413。
	farTooLarge := streamUpload(t, manager, docSpacePath(space)+"/docs/upload", fields, "far.bin", 2*docs.MaxUploadBytes)
	if farTooLarge.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("far oversized upload = %d %s", farTooLarge.Status, farTooLarge.Body)
	}
}

func TestDocAttachmentsReadWriteAndAccess(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	manager := env.manager()
	space := env.space.ID
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	setBox := docInSet(env.newDocSet(manager, space, "附件").ID)
	doc := env.docNodeIn(manager, space, setBox, "", docs.KindDoc, "带图")
	personalDoc := env.docNodeIn(manager, space, docPersonal(), "", docs.KindDoc, "私人带图")
	folder := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "目录")

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...)
	upload := uploadDocAttachment(t, manager, space, doc.ID, "pic.png", png)
	if upload.Status != http.StatusCreated {
		t.Fatalf("attachment upload = %d %s", upload.Status, upload.Body)
	}
	var attachment docAttachmentView
	upload.decode(t, &attachment)
	wantURL := fmt.Sprintf("/api/v1/organizations/%s/spaces/%s/doc-attachments/%s", env.org, space, attachment.ID)
	if attachment.URL != wantURL || attachment.ContentType != "image/png" || attachment.SizeBytes != int64(len(png)) {
		t.Fatalf("attachment = %+v", attachment)
	}
	readPath := fmt.Sprintf("%s/doc-attachments/%s", docSpacePath(space), attachment.ID)
	read := env.other().must(http.StatusOK, http.MethodGet, readPath, nil, nil)
	if !bytes.Equal(read.Body, png) || read.Header.Get("Content-Disposition") != `inline; filename="pic.png"; filename*=UTF-8''pic.png` ||
		read.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("read attachment headers = %v", read.Header)
	}

	// 可执行内容一律 attachment，不 inline。
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`)
	svgUpload := uploadDocAttachment(t, manager, space, doc.ID, "logo.svg", svg)
	var svgAttachment docAttachmentView
	svgUpload.decode(t, &svgAttachment)
	svgRead := manager.must(http.StatusOK, http.MethodGet, fmt.Sprintf("%s/doc-attachments/%s", docSpacePath(space), svgAttachment.ID), nil, nil)
	if strings.HasPrefix(svgRead.Header.Get("Content-Disposition"), "inline") || svgRead.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("svg must not be inline: %v", svgRead.Header)
	}
	html := uploadDocAttachment(t, manager, space, doc.ID, "page.html", []byte("<html><script>alert(1)</script></html>"))
	var htmlAttachment docAttachmentView
	html.decode(t, &htmlAttachment)
	htmlRead := manager.must(http.StatusOK, http.MethodGet, fmt.Sprintf("%s/doc-attachments/%s", docSpacePath(space), htmlAttachment.ID), nil, nil)
	if strings.HasPrefix(htmlRead.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("html must not be inline: %v", htmlRead.Header)
	}

	// 个人文档的附件只有本人可读；文件夹与 file 节点不能挂附件。
	personalUpload := uploadDocAttachment(t, manager, space, personalDoc.ID, "private.png", png)
	var privateAttachment docAttachmentView
	personalUpload.decode(t, &privateAttachment)
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet,
		fmt.Sprintf("%s/doc-attachments/%s", docSpacePath(space), privateAttachment.ID), nil)
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet,
		fmt.Sprintf("%s/doc-attachments/%s", docSpacePath(space), privateAttachment.ID), nil)
	uploadDocAttachment(t, manager, space, folder.ID, "a.png", png).expectStatus(t, http.StatusBadRequest)
	uploadDocAttachment(t, env.other(), space, doc.ID, "a.png", png).expectStatus(t, http.StatusForbidden)
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, readPath, nil)

	streamed := streamUpload(t, manager, fmt.Sprintf("%s/docs/%s/attachments", docSpacePath(space), doc.ID), nil, "huge.bin", docs.MaxAttachmentBytes+1)
	if streamed.Status != http.StatusRequestEntityTooLarge || streamed.code() != "DOC_ATTACHMENT_TOO_LARGE" {
		t.Fatalf("oversized attachment = %d %s", streamed.Status, streamed.Body)
	}
	// 远超上限（触发 MaxBytesReader）时仍是 413。
	farAttachment := streamUpload(t, manager, fmt.Sprintf("%s/docs/%s/attachments", docSpacePath(space), doc.ID), nil, "far.bin", 2*docs.MaxAttachmentBytes)
	if farAttachment.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("far oversized attachment = %d %s", farAttachment.Status, farAttachment.Body)
	}
	// 只有文档与表格能挂附件：文件节点返回 400。
	fileNode := uploadDocFile(t, manager, space, map[string]string{"container": "set", "set_id": setBox["set_id"].(string)}, "pic.png", png)
	var fileNodeView docView
	fileNode.decode(t, &fileNodeView)
	uploadDocAttachment(t, manager, space, fileNodeView.ID, "a.png", png).expectStatus(t, http.StatusBadRequest)
}

func TestDocTreeListingPaginationAndBreadcrumbs(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setBox := docInSet(env.newDocSet(manager, space, "列表").ID)
	parent := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "父")
	inner := env.docNodeIn(manager, space, setBox, parent.ID, docs.KindFolder, "内")
	env.docNodeIn(manager, space, setBox, inner.ID, docs.KindDoc, "叶")

	children := env.listDocs(manager, space, "container=set&set_id="+setBox["set_id"].(string)+"&parent_id="+parent.ID)
	if len(children) != 1 || children[0].ID != inner.ID || !children[0].HasChildren || !children[0].CanEdit {
		t.Fatalf("children = %+v", children)
	}
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet,
		docSpacePath(space)+"/docs?container=set&set_id="+s3NewID(t), nil)
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, docSpacePath(space)+"/docs?container=bogus", nil)
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet,
		docSpacePath(space)+"/docs?container=set&set_id="+setBox["set_id"].(string)+"&parent_id=not-a-uuid", nil)

	leaf := env.listDocs(manager, space, "container=set&set_id="+setBox["set_id"].(string)+"&parent_id="+inner.ID)
	if len(leaf) != 1 {
		t.Fatalf("leaf list = %+v", leaf)
	}
	detail := env.getDoc(manager, space, leaf[0].ID)
	if len(detail.Breadcrumb) != 2 || detail.Breadcrumb[0].ID != parent.ID || detail.Breadcrumb[1].ID != inner.ID {
		t.Fatalf("breadcrumb = %+v", detail.Breadcrumb)
	}
	var out map[string]json.RawMessage
	manager.must(http.StatusOK, http.MethodGet, docSpacePath(space)+"/docs/"+parent.ID, nil, &out)
	if _, ok := out["breadcrumb"]; !ok {
		t.Fatalf("detail missing breadcrumb key: %v", out)
	}
}

// withoutStorage 换用没有存储的服务器（上传与下载都应返回 503）。
func withoutStorage(env *s3Env) {
	env.t.Helper()
	env.server = httptestServer(env, api.NewWithDispatch(env.auth, env.database, false, nil,
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	env.clients = map[string]*s3Client{}
}

func TestDocDownloadWithoutStorage(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	manager := env.manager()
	space := env.space.ID
	setBox := docInSet(env.newDocSet(manager, space, "下载").ID)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	file := uploadDocFile(t, manager, space, map[string]string{"container": "set", "set_id": setBox["set_id"].(string)}, "a.png", png)
	var node docView
	file.decode(t, &node)

	withoutStorage(env)
	manager = env.manager()
	response := manager.do(http.MethodGet, fmt.Sprintf("%s/docs/%s/download", docSpacePath(space), node.ID), nil)
	if response.Status != http.StatusServiceUnavailable || response.code() != "STORAGE_UNAVAILABLE" {
		t.Fatalf("download without storage = %d %s", response.Status, response.Body)
	}
	if response.Header.Get("Content-Disposition") != "" {
		t.Fatalf("error response carries download headers: %v", response.Header)
	}
}

func TestDocRetiredSpaceRejectsAttachment(t *testing.T) {
	env := newS3Env(t)
	withAvatarStorage(env, t.TempDir())
	manager := env.manager()
	space := env.space.ID
	doc := env.docNodeIn(manager, space, docInSet(env.newDocSet(manager, space, "停用附件").ID), "", docs.KindDoc, "正文")
	current := env.getSpace(manager, space)
	manager.must(http.StatusOK, http.MethodPost, docSpacePath(space)+"/retire",
		map[string]any{"version": current.Version, "open_tasks": "cancel"}, nil)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	uploadDocAttachment(t, manager, space, doc.ID, "a.png", png).expectStatus(t, http.StatusConflict)
}

func TestDocNodeLimitPerContainer(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setID := env.newDocSet(manager, space, "上限").ID
	setBox := docInSet(setID)
	env.exec(`INSERT INTO openfox.doc_nodes (organization_id, id, space_id, set_id, kind, title, body, position, created_by, updated_by)
		SELECT $1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 'doc', 'bulk ' || g, '', g, $4::uuid, $4::uuid
		  FROM generate_series(1, 4999) AS g`, env.org, space, setID, s3ManagerID)
	env.newDocNode(manager, space, map[string]any{"container": setBox, "kind": "doc", "title": "第 5000 个"})
	manager.expectError(http.StatusConflict, "DOC_NODE_LIMIT", http.MethodPost, docSpacePath(space)+"/docs",
		map[string]any{"container": setBox, "kind": "doc", "title": "第 5001 个"})
}

func TestDocListTruncatesAt500(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setID := env.newDocSet(manager, space, "截断").ID
	env.exec(`INSERT INTO openfox.doc_nodes (organization_id, id, space_id, set_id, kind, title, body, position, created_by, updated_by)
		SELECT $1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 'doc', 'row ' || g, '', g, $4::uuid, $4::uuid
		  FROM generate_series(1, 501) AS g`, env.org, space, setID, s3ManagerID)
	nodes := env.listDocs(manager, space, "container=set&set_id="+setID)
	if len(nodes) != docs.MaxListNodes {
		t.Fatalf("list returned %d nodes, want %d", len(nodes), docs.MaxListNodes)
	}
}

func TestDocSearchTruncatesAt50(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setID := env.newDocSet(manager, space, "搜索截断").ID
	env.exec(`INSERT INTO openfox.doc_nodes (organization_id, id, space_id, set_id, kind, title, body, position, created_by, updated_by)
		SELECT $1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, 'doc', 'truncword ' || g, '', g, $4::uuid, $4::uuid
		  FROM generate_series(1, 60) AS g`, env.org, space, setID, s3ManagerID)
	if hits := env.searchDocs(manager, space, "truncword"); len(hits) != docs.MaxSearchNodes {
		t.Fatalf("search returned %d hits, want %d", len(hits), docs.MaxSearchNodes)
	}
}

func TestDocMoveDepthLimit(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	space := env.space.ID
	setBox := docInSet(env.newDocSet(manager, space, "深度").ID)
	// 目标链：9 层文件夹（最深一层为第 9 层）。
	var chain []docView
	parent := ""
	for depth := 1; depth <= 9; depth++ {
		node := env.docNodeIn(manager, space, setBox, parent, docs.KindFolder, fmt.Sprintf("链 %d", depth))
		chain = append(chain, node)
		parent = node.ID
	}
	// 源子树：根与一个子文件夹，高度为 2。
	source := env.docNodeIn(manager, space, setBox, "", docs.KindFolder, "源")
	env.docNodeIn(manager, space, setBox, source.ID, docs.KindFolder, "源子")
	movePath := fmt.Sprintf("%s/docs/%s/move", docSpacePath(space), source.ID)

	// 挂到第 9 层下面：9 + 2 = 11 > 10，拒绝。
	manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, movePath, map[string]any{
		"version": source.Version, "target_space_id": space, "container": setBox, "parent_id": chain[8].ID,
	})
	// 挂到第 8 层下面：8 + 2 = 10，允许。
	manager.must(http.StatusOK, http.MethodPost, movePath, map[string]any{
		"version": source.Version, "target_space_id": space, "container": setBox, "parent_id": chain[7].ID,
	}, nil)
}
