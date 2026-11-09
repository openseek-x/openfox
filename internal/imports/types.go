// Package imports 实现 Jira CSV 导入：列/状态/成员映射分析、后台执行与回滚。
package imports

import "errors"

var (
	ErrNotFound              = errors.New("imports: not found")
	ErrForbidden             = errors.New("imports: forbidden")
	ErrVersionConflict       = errors.New("imports: version conflict")
	ErrImportState           = errors.New("imports: invalid state")
	ErrImportActive          = errors.New("imports: another import is active in this space")
	ErrInvalidConfig         = errors.New("imports: invalid config")
	ErrConfigIncomplete      = errors.New("imports: config incomplete")
	ErrOverwriteConfirmation = errors.New("imports: overwrite confirmation required")
	ErrInvalidFile           = errors.New("imports: invalid file")
	ErrInvalidRequest        = errors.New("imports: invalid request")
	ErrRollbackUnavailable   = errors.New("imports: rollback unavailable")
	ErrRunActive             = errors.New("imports: agent run active")
	ErrSpaceNotFound         = errors.New("imports: space not found")
)

// 来源与状态常量。
const (
	SourceJiraCSV    = "jira_csv"
	SourceJiraCloud  = "jira_cloud"
	SourceJiraServer = "jira_server"
	SourceSystem     = "jira" // import_mappings.source_system / external_accounts.provider

	StatusDraft           = "DRAFT"
	StatusQueued          = "QUEUED"
	StatusRunning         = "RUNNING"
	StatusCancelRequested = "CANCEL_REQUESTED"
	StatusSucceeded       = "SUCCEEDED"
	StatusFailed          = "FAILED"
	StatusCancelled       = "CANCELLED"
	StatusRolledBack      = "ROLLED_BACK"
	StatusDiscarded       = "DISCARDED"
)

// 文件限制。
const (
	MaxDataRows  = 5000
	MaxColumns   = 300
	MaxCellBytes = 64 * 1024
	MaxFileBytes = 10 << 20
)

// 任务字段（对应契约 ImportField）。
const (
	FieldIgnore           = "ignore"
	FieldSourceID         = "source_id"
	FieldSourceKey        = "source_key"
	FieldTitle            = "title"
	FieldType             = "type"
	FieldStatus           = "status"
	FieldPriority         = "priority"
	FieldAssignee         = "assignee"
	FieldReporter         = "reporter"
	FieldCreator          = "creator"
	FieldDescription      = "description"
	FieldParent           = "parent"
	FieldLabels           = "labels"
	FieldComment          = "comment"
	FieldCreatedAt        = "created_at"
	FieldUpdatedAt        = "updated_at"
	FieldStartDate        = "start_date"
	FieldDueDate          = "due_date"
	FieldStoryPoints      = "story_points"
	FieldSourceURL        = "source_url"
	FieldRelations        = "relations"
	FieldAttachment       = "attachment"
	FieldSprint           = "sprint"
	FieldResolution       = "resolution"
	FieldWatchers         = "watchers"
	FieldOriginalEstimate = "original_estimate"
	FieldProject          = "source_container"
	FieldCustom           = "custom"
)

// ParsedFile 是 CSV 解析结果。Rows 不含空行；Line 是文件中的行号（表头为第 1 行）。
func fileSourceName(source string) string {
	switch source {
	case "zentao_api":
		return "ZenTao API"
	case "tapd_api":
		return "TAPD API"
	case "shortcut_api":
		return "Shortcut API"
	case "pingcode_api":
		return "PingCode API"
	case "tapd_excel":
		return "TAPD Excel"
	case "teambition_api":
		return "Teambition API"
	case "teambition_csv":
		return "Teambition CSV"
	case "pingcode_csv":
		return "PingCode CSV"
	default:
		return "Jira CSV"
	}
}

func sourceSystem(source string) string {
	switch source {
	case "zentao_api":
		return "zentao"
	case "shortcut_api":
		return "shortcut"
	case "tapd_excel", "tapd_api":
		return "tapd"
	case "teambition_csv", "teambition_api":
		return "teambition"
	case "pingcode_csv", "pingcode_api":
		return "pingcode"
	default:
		return SourceSystem
	}
}

type ParsedFile struct {
	Origin      ProjectOrigin
	Source      string
	Headers     []string
	Rows        []ParsedRow
	SkippedRows int
	RowErrors   []RowError
}

type ParsedRow struct {
	Line  int
	Cells []string
}

type RowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// Preview 对应契约 ImportPreview；analysis / invitations / resolved 是服务端附加键。
type Preview struct {
	Columns        []PreviewColumn `json:"columns"`
	TotalRows      int             `json:"total_rows"`
	ImportableRows int             `json:"importable_rows"`
	SkippedRows    int             `json:"skipped_rows"`
	FailedRows     int             `json:"failed_rows"`
	RowErrors      []RowError      `json:"row_errors"`
}

type PreviewColumn struct {
	Index          int      `json:"index"`
	Header         string   `json:"header"`
	Samples        []string `json:"samples"`
	SuggestedField string   `json:"suggested_field"`
}

// 配置（对应契约 ImportConfig）。
type ColumnMapping struct {
	Index      int    `json:"index"`
	Field      string `json:"field"`
	CustomName string `json:"custom_name,omitempty"`
}

type StatusMapping struct {
	Source      string `json:"source"`
	StatusID    string `json:"status_id,omitempty"`
	NewName     string `json:"new_name,omitempty"`
	NewCategory string `json:"new_category,omitempty"`
}

type UserMapping struct {
	SourceID string `json:"source_id"`
	Mode     string `json:"mode"` // member / invite / external / skip
	UserID   string `json:"user_id,omitempty"`
	Email    string `json:"email,omitempty"`
}

type Options struct {
	FileProjects   *bool `json:"file_projects,omitempty"`
	ContinuousSync bool  `json:"continuous_sync,omitempty"`
	ImportOpen     bool  `json:"import_open"`
	ImportDone     bool  `json:"import_done"`
	MigratedLabel  bool  `json:"migrated_label"`
	Overwrite      bool  `json:"overwrite"`
	// ImportSprints 把 Sprint 导入为迭代（只有 API 来源、且目标空间为手动迭代或新空间时可用）。
	ImportSprints bool `json:"import_sprints"`
	// KeepNumbers 保留来源票号（只有导入到新空间时可用）。
	KeepNumbers bool `json:"keep_numbers"`
}

// NewSpace 是「导入到新空间」的参数（开始导入时才创建空间）。
type NewSpace struct {
	Name       string `json:"name"`
	Prefix     string `json:"prefix,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// FetchInfo 是 API 导入的拉取进度。Total 为空表示来源没有报告总数。
type FetchInfo struct {
	Fetched int  `json:"fetched"`
	Total   *int `json:"total"`
}

// APIInfo 是 API 导入的连接参数（不含凭据），保存在 preview.api。
type APIInfo struct {
	AccountIdentity string `json:"-"`
	Deployment      string `json:"deployment"`
	SiteURL         string `json:"site_url"`
	ProjectKey      string `json:"project_key"`
	JQL             string `json:"jql"`
	// AuthKind / AuthUser 是认证方式与邮箱或用户名（不是凭证），供「重新导入」预填。
	AuthKind string   `json:"auth_kind,omitempty"`
	AuthUser string   `json:"auth_user,omitempty"`
	FieldIDs FieldIDs `json:"field_ids"`
	// Failures 是连续拉取失败的次数（达到 3 次丢弃草稿）。
	Failures int `json:"failures,omitempty"`
	// Cursor 是拉取游标（cloud 为 nextPageToken，server 为 startAt）。
	Cursor CursorState `json:"cursor"`
}

// JobAPI 对应契约 ImportJob.api：API 导入的连接参数，不含任何凭证。
type JobAPI struct {
	Deployment string `json:"deployment"`
	SiteURL    string `json:"site_url"`
	ProjectKey string `json:"project_key"`
	JQL        string `json:"jql"`
	AuthKind   string `json:"auth_kind"`
	AuthUser   string `json:"auth_user"`
}

// FieldIDs 是 Sprint、故事点与开始日期的自定义字段 ID。
type FieldIDs struct {
	EpicLink string `json:"epic_link,omitempty"`
	Sprint   string `json:"sprint,omitempty"`
	Points   string `json:"points,omitempty"`
	Start    string `json:"start,omitempty"`
}

// CursorState 是拉取游标。
type CursorState struct {
	Token   string `json:"token,omitempty"`
	StartAt int    `json:"start_at,omitempty"`
}

type Config struct {
	Columns  []ColumnMapping `json:"columns"`
	Statuses []StatusMapping `json:"statuses"`
	Users    []UserMapping   `json:"users"`
	Options  Options         `json:"options"`
}

// Warning 对应契约 ImportWarning。
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}
