// Package docs 实现文档库：文档集、文档树（我的文档 / 文档集）与附件。
// 对外数据形态与契约 DocSet*、DocNode*、DocContainer、DocAttachment 逐字段对齐。
// 授权由 API 层完成（空间读写判断见 Access），这里只负责业务规则、并发控制与数据读写。
package docs

import (
	"net/http"
	"time"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

const (
	KindFolder = "folder"
	KindDoc    = "doc"
	KindSheet  = "sheet"
	KindFile   = "file"

	ContainerPersonal = "personal"
	ContainerSet      = "set"
)

const (
	MaxSetsPerSpace        = 200
	MaxNodesPerContainer   = 5000
	MaxDepth               = 10
	MaxBodyBytes           = 2 << 20
	MaxUploadBytes         = 20 << 20
	MaxAttachmentBytes     = 20 << 20
	MaxListNodes           = 500
	MaxSearchNodes         = 50
	MaxSetNameRunes        = 64
	MaxTitleRunes          = 200
	MaxAttachmentNameRunes = 255
	MaxSearchRunes         = 100
	// untitledLabel 只用于下载文件名（界面的「未命名文档」）。
	untitledLabel = "未命名文档"
)

var (
	ErrNotFound         = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "document is not available")
	ErrVersionConflict  = collaboration.ErrVersionConflict
	ErrSetLimit         = collaboration.NewRuleError(http.StatusConflict, "DOC_SET_LIMIT", "each space can have at most 200 document sets")
	ErrNodeLimit        = collaboration.NewRuleError(http.StatusConflict, "DOC_NODE_LIMIT", "each container can have at most 5000 nodes")
	ErrDepthExceeded    = invalidf("the tree can be at most 10 levels deep")
	ErrMoveIntoSelf     = collaboration.NewRuleError(http.StatusBadRequest, "DOC_MOVE_INTO_SELF", "a document cannot be moved into itself or its descendants")
	ErrParentInvalid    = invalidf("parent must be a folder in the same container")
	ErrBeforeInvalid    = invalidf("before_id must be a sibling in the target folder")
	ErrBodyTooLarge     = collaboration.NewRuleError(http.StatusBadRequest, "DOC_BODY_TOO_LARGE", "body must not exceed 2 MB")
	ErrBodyNotAllowed   = invalidf("body is only allowed for doc and sheet")
	ErrFolderTitle      = invalidf("folder title must not be empty")
	ErrFolderDownload   = invalidf("folders cannot be downloaded")
	ErrKindInvalid      = invalidf("kind is invalid")
	ErrTitleInvalid     = invalidf("title must be at most 200 characters")
	ErrSetNameInvalid   = invalidf("document set name must be 1-64 characters")
	ErrIconInvalid      = invalidf("icon is invalid")
	ErrQueryInvalid     = invalidf("search query must be 1-100 characters")
	ErrFileName         = invalidf("file name is invalid")
	ErrFileTooLarge     = collaboration.NewRuleError(http.StatusRequestEntityTooLarge, "DOC_FILE_TOO_LARGE", "file must not exceed 20 MB")
	ErrAttachmentLarge  = collaboration.NewRuleError(http.StatusRequestEntityTooLarge, "DOC_ATTACHMENT_TOO_LARGE", "attachment must not exceed 20 MB")
	ErrAttachmentKind   = invalidf("attachments can only be added to doc or sheet nodes")
	ErrContainerInvalid = invalidf("container is invalid")
	ErrSetIDRequired    = invalidf("set_id is required for a document set container")
	ErrSetIDNotAllowed  = invalidf("set_id is only allowed for a document set container")
)

// invalidf 返回 400 INVALID_REQUEST，消息保留具体原因。
func invalidf(message string) error {
	return collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", message)
}

// deniedError 把策略决定转换为接口错误（隐藏 → 404，拒绝 → 403）。
func deniedError(decision policy.Decision) error {
	return collaboration.NewRuleError(decision.HTTPStatus(), decision.ErrorCode(), "action is not allowed")
}

// Container 是 DocContainer：personal 为当前用户在该空间的「我的文档」，set 为文档集。
type Container struct {
	Kind  string  `json:"kind"`
	SetID *string `json:"set_id,omitempty"`
}

// IsPersonal 判断容器是否为「我的文档」。
func (c Container) IsPersonal() bool { return c.Kind == ContainerPersonal }

// Set 是 DocSet。
type Set struct {
	ID        string    `json:"id"`
	SpaceID   string    `json:"space_id"`
	Name      string    `json:"name"`
	IconKind  *string   `json:"icon_kind"`
	Icon      *string   `json:"icon"`
	IconURL   *string   `json:"icon_url"`
	Position  int       `json:"position"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   uint64    `json:"version"`
	CanEdit   bool      `json:"can_edit"`
}

// SetInput 是 DocSetInput；Icon 为 nil 表示清除图标（回到默认图标）。
type SetInput struct {
	Name string   `json:"name"`
	Icon *SetIcon `json:"icon"`
}

// SetIcon 是图标选择器的结果：kind 为 icon / emoji / upload，value 为内置图标名、表情或上传图标 ID。
type SetIcon struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Node 是 DocNode。SetID 与 OwnerUserID 只用于推导 container，不直接输出。
type Node struct {
	ID            string    `json:"id"`
	SpaceID       string    `json:"space_id"`
	Container     Container `json:"container"`
	ParentID      *string   `json:"parent_id"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	Position      int       `json:"position"`
	SizeBytes     *int64    `json:"size_bytes"`
	ContentType   *string   `json:"content_type"`
	CreatedBy     string    `json:"created_by"`
	UpdatedBy     string    `json:"updated_by"`
	UpdatedByName string    `json:"updated_by_name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Version       uint64    `json:"version"`
	HasChildren   bool      `json:"has_children"`
	CanEdit       bool      `json:"can_edit"`

	FileKey string `json:"-"`
	FileExt string `json:"-"`

	setID       *string
	ownerUserID *string
}

// Ref 是节点的轻量引用，用于授权与写入前的校验（不含正文）。
type Ref struct {
	ID        string
	SpaceID   string
	Container Container
	ParentID  *string
	Kind      string
	Version   uint64
}

// Crumb 是面包屑中的一项。
type Crumb struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// NodeDetail 是 DocNodeDetail：节点加正文与面包屑。
type NodeDetail struct {
	Node
	Body       *string `json:"body"`
	Breadcrumb []Crumb `json:"breadcrumb"`
}

// NodeInput 是新建节点的输入。Body 只对 doc / sheet 有效；File 只对 file 有效（上传时由 API 层填好）。
type NodeInput struct {
	ID        string
	Container Container
	ParentID  *string
	Kind      string
	Title     string
	Body      *string
	File      *FileInfo
}

// FileInfo 是 file 节点的存储信息。Ext 为小写的原扩展名（不含点号），为空表示没有可用的扩展名。
type FileInfo struct {
	Ext         string
	Key         string
	ContentType string
	SizeBytes   int64
	SHA256      string
}

// NodePatch 是 PATCH 的输入；Title / Body 为 nil 表示不修改。
type NodePatch struct {
	Version uint64
	Title   *string
	Body    *string
}

// Attachment 是 DocAttachment。Storage 键不输出。
type Attachment struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	URL         string `json:"url"`
	FileKey     string `json:"-"`
	SpaceID     string `json:"-"`
}

// Access 是一次空间授权的结果（同一次 LoadSpaceAccess 得出）。列表的 can_edit 与写入判断共用它。
type Access struct {
	// Read 为空间不可读时的错误（隐藏为 404，拒绝为 403）。
	Read error
	// Write 为文档集写入被拒绝时的错误（只读成员为 403）。
	Write error
	// Retired 为空间停用时的错误：我的文档与文档集都只读。
	Retired error
}

// RequireRead 返回读取所需的授权错误。
func (a Access) RequireRead() error { return a.Read }

// RequireSetWrite 返回写入文档集（含文档集本身）所需的授权错误：要求写权限且未停用。
func (a Access) RequireSetWrite() error {
	if a.Read != nil {
		return a.Read
	}
	if a.Write != nil {
		return a.Write
	}
	return a.Retired
}

// RequireWrite 返回写入指定容器所需的授权错误：我的文档只要求可读且未停用，文档集同 RequireSetWrite。
func (a Access) RequireWrite(container Container) error {
	if container.IsPersonal() {
		if a.Read != nil {
			return a.Read
		}
		return a.Retired
	}
	return a.RequireSetWrite()
}

// CanEdit 与 RequireWrite 使用同一判断，结果写入 can_edit。
func (a Access) CanEdit(container Container) bool { return a.RequireWrite(container) == nil }

// DecisionError 把策略决定转换为 Access 的错误值（允许时返回 nil）。
func DecisionError(decision policy.Decision) error {
	if decision.Allowed() {
		return nil
	}
	return deniedError(decision)
}
