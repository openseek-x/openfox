package imports

// 克隆空间到另一个组织。
//
// 复用 import_jobs / 租约 / 批次 / openfox.import_job 事务标记：任务挂在「目标组织」下，worker 只访问目标组织，
// 源空间的内容在开始时由 internal/spacebundle 在源组织事务里导出为快照包，worker 之后只读快照包。

import (
	"errors"
	"time"
)

const (
	SourceSpaceClone = "space_clone"

	StatusCleaning      = "CLEANING"
	StatusCleaned       = "CLEANED"
	StatusCleanupFailed = "CLEANUP_FAILED"

	// CloneSourceSystem 是克隆在 import_mappings.source_system / external_accounts.provider 里的取值。
	CloneSourceSystem = "openfox"
)

var (
	// ErrCloneBlocked 表示预览有阻塞项，不能开始；用 *BlockedError 携带阻塞项。
	ErrCloneBlocked = errors.New("imports: clone blocked")
	// ErrCloneTargetInvalid 表示目标组织不合法（等于源组织、不存在，或发起人不是目标组织的所有者 / 管理员以外的错误请求）。
	ErrCloneTargetInvalid = errors.New("imports: clone target is invalid")
	// ErrCloneNameTaken 表示目标组织已有同名空间（标识符冲突用 collaboration.ErrIdentifierTaken）。
	ErrCloneNameTaken = errors.New("imports: clone name is taken")
	// ErrCloneStorage 表示快照包存储不可用。
	ErrCloneStorage = errors.New("imports: clone storage unavailable")
)

// BlockedError 携带预览的阻塞项。
type BlockedError struct{ Blockers []CloneIssue }

func (e *BlockedError) Error() string { return ErrCloneBlocked.Error() }
func (e *BlockedError) Unwrap() error { return ErrCloneBlocked }

// CloneRequest 是预览 / 开始克隆的请求。
type CloneRequest struct {
	TargetOrganizationID string `json:"target_organization_id"`
	Name                 string `json:"name"`
	Prefix               string `json:"prefix"`
}

// CloneIssue 是预览里的一项（阻塞 / 警告 / 已接受风险 / 已跳过）；Message 是中文文案。
type CloneIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}

// CloneCounts 对应契约 ClonePreview.counts。
type CloneCounts struct {
	Spaces       int `json:"spaces"`
	Tasks        int `json:"tasks"`
	Projects     int `json:"projects"`
	Comments     int `json:"comments"`
	Activities   int `json:"activities"`
	TaskTypes    int `json:"task_types"`
	TaskStatuses int `json:"task_statuses"`
	Labels       int `json:"labels"`
	Agents       int `json:"agents"`
	CustomFields int `json:"custom_fields"`
}

// ClonePreview 对应契约 ClonePreview。
type ClonePreview struct {
	SourceName             string       `json:"source_name"`
	SourcePrefix           string       `json:"source_prefix"`
	TargetOrganizationID   string       `json:"target_organization_id"`
	TargetOrganizationName string       `json:"target_organization_name"`
	TargetName             string       `json:"target_name"`
	TargetPrefix           string       `json:"target_prefix"`
	NameConflict           bool         `json:"name_conflict"`
	Counts                 CloneCounts  `json:"counts"`
	Blockers               []CloneIssue `json:"blockers"`
	Warnings               []CloneIssue `json:"warnings"`
	AcceptedRisks          []CloneIssue `json:"accepted_risks"`
	Skipped                []CloneIssue `json:"skipped"`
	CanStart               bool         `json:"can_start"`
}

// CloneFailure 是失败项目。
type CloneFailure struct {
	Ref   string `json:"ref"`
	Error string `json:"error"`
}

// CloneJob 对应契约 CloneJob。
type CloneJob struct {
	ID                   string         `json:"id"`
	Status               string         `json:"status"`
	Phase                string         `json:"phase"`
	SourceOrganizationID string         `json:"source_organization_id"`
	SourceSpaceID        string         `json:"source_space_id"`
	SourceSpaceName      string         `json:"source_space_name"`
	TargetOrganizationID string         `json:"target_organization_id"`
	TargetSpaceID        *string        `json:"target_space_id"`
	TargetSpaceName      string         `json:"target_space_name"`
	TotalCount           int            `json:"total_count"`
	CreatedCount         int            `json:"created_count"`
	SkippedCount         int            `json:"skipped_count"`
	FailedCount          int            `json:"failed_count"`
	Failures             []CloneFailure `json:"failures"`
	Warnings             []Warning      `json:"warnings"`
	ErrorCode            string         `json:"error_code"`
	ErrorMessage         string         `json:"error_message"`
	Cancellable          bool           `json:"cancellable"`
	OperationID          string         `json:"operation_id"`
	CreatedAt            time.Time      `json:"created_at"`
	StartedAt            *time.Time     `json:"started_at"`
	FinishedAt           *time.Time     `json:"finished_at"`
	Version              int64          `json:"version"`
}

// cloneDoc 是克隆任务 import_jobs.preview 的内容（克隆任务不使用 previewDoc）。
type cloneDoc struct {
	SourceOrganizationName string       `json:"source_organization_name"`
	SourceSpaceName        string       `json:"source_space_name"`
	SourcePrefix           string       `json:"source_prefix"`
	TargetOrganizationName string       `json:"target_organization_name"`
	TargetName             string       `json:"target_name"`
	TargetPrefix           string       `json:"target_prefix"`
	Counts                 CloneCounts  `json:"counts"`
	Skipped                []CloneIssue `json:"skipped"`
	// Cause 是进入清理的原因：cancelled / failed。
	Cause string     `json:"cause,omitempty"`
	State cloneState `json:"state"`
}

// cloneState 是 worker 跨批次保存的进度。
type cloneState struct {
	// Stage 是阶段内的子步骤：linking 为 parents / relations；comments 为 comments / activities。
	Stage  string `json:"stage,omitempty"`
	Cursor int    `json:"cursor"`
	// Created 表示已经创建过东西（空间或组织级配置）；清理结束据此区分 FAILED / CANCELLED 与 CLEANED。
	Created       bool           `json:"created,omitempty"`
	SourceChecked bool           `json:"source_checked,omitempty"`
	MaxSequence   int64          `json:"max_sequence,omitempty"`
	NextNumber    int64          `json:"next_number,omitempty"`
	ArchiveTypes  []string       `json:"archive_types,omitempty"`
	DefaultType   string         `json:"default_type,omitempty"`
	Resolved      *cloneResolved `json:"resolved,omitempty"`
}

// cloneResolved 是 creating_space 阶段解析出的「来源 ID → 目标 ID」映射。
type cloneResolved struct {
	Statuses map[string]string      `json:"statuses"`
	Types    map[string]string      `json:"types"`
	Labels   map[string]string      `json:"labels"`
	Fields   map[string]cloneField  `json:"fields"`
	Projects map[string]string      `json:"projects"`
	RelTypes map[string]string      `json:"rel_types"`
	People   map[string]clonePerson `json:"people"`
}

type cloneField struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"`
	Options map[string]string `json:"options,omitempty"` // 来源选项 ID → 目标选项 ID
}

// clonePerson 是一个来源人员的落地方式：能访问新空间的成员，或外部账号。
type clonePerson struct {
	UserID     string `json:"user_id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Name       string `json:"name"`
}
