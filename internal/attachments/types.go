// Package attachments 提供任务附件与对话附件：类型白名单与内容嗅探、元数据读写。
// 文件内容由 internal/artifacts 存储；权限（读 / 下载 / 删除）由 API 层按任务或会话判断，这里只做数据与规则。
package attachments

import (
	"net/http"
	"time"

	"openfox/internal/collaboration"
)

const (
	OwnerTask         = "TASK"
	OwnerConversation = "CONVERSATION"

	// MaxBytes 与文档库一致。
	MaxBytes = 20 << 20
	// MaxPerTask 是每个任务的附件上限。
	MaxPerTask = 100
	// MaxPendingPerUser 是同一会话里一个人尚未随消息发送的附件上限，防止反复上传占满存储。
	MaxPendingPerUser = 20
	// MaxPerMessage 是一条消息最多携带的附件数。
	MaxPerMessage = 10
	MaxNameRunes  = 255
	// HeadBytes 是内容嗅探使用的文件头长度（与 http.DetectContentType 一致）。
	HeadBytes = 512
)

var (
	ErrNotFound     = collaboration.NewRuleError(http.StatusNotFound, "NOT_FOUND", "attachment is not available")
	ErrTooLarge     = collaboration.NewRuleError(http.StatusRequestEntityTooLarge, "ATTACHMENT_TOO_LARGE", "attachment must not exceed 20 MB")
	ErrTypeRejected = collaboration.NewRuleError(http.StatusUnsupportedMediaType, "ATTACHMENT_TYPE_NOT_ALLOWED", "this file type is not allowed as an attachment")
	ErrTaskLimit    = collaboration.NewRuleError(http.StatusConflict, "ATTACHMENT_LIMIT", "a task can have at most 100 attachments")
	ErrPendingLimit = collaboration.NewRuleError(http.StatusConflict, "ATTACHMENT_PENDING_LIMIT", "too many attachments are waiting to be sent")
	ErrBadName      = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "attachment file name is invalid")
	ErrBadMessage   = collaboration.NewRuleError(http.StatusBadRequest, "INVALID_REQUEST", "attachment_ids are invalid or already used")
)

// FileInfo 是已写入存储的文件事实。
type FileInfo struct {
	Key         string
	ContentType string
	SizeBytes   int64
	SHA256      string
}

// Attachment 是对外形态（契约 Attachment）；存储键不输出。
type Attachment struct {
	ID             string    `json:"id"`
	OwnerKind      string    `json:"owner_kind"`
	TaskID         string    `json:"task_id,omitempty"`
	ConversationID string    `json:"conversation_id,omitempty"`
	MessageID      string    `json:"message_id,omitempty"`
	Name           string    `json:"name"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	UploadedBy     string    `json:"uploaded_by"`
	UploaderName   string    `json:"uploader_name"`
	CreatedAt      time.Time `json:"created_at"`
	// Previewable 为 true 表示可以用 ?disposition=inline 预览（仅 PNG / JPEG / GIF / WebP）。
	Previewable bool `json:"previewable"`
	// CanDelete 由 API 层按调用者填写。
	CanDelete bool   `json:"can_delete"`
	SpaceID   string `json:"-"`
	FileKey   string `json:"-"`
}
