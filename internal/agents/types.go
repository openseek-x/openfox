// Package agents contains the WP-06 managed Agent definition and immutable
// configuration-version boundary. Runtime execution is intentionally outside
// this package.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

var (
	ErrInvalidRequest  = errors.New("agents: invalid request")
	ErrInvalidConfig   = errors.New("agents: invalid configuration")
	ErrNotFound        = errors.New("agents: Agent not found")
	ErrForbidden       = errors.New("agents: action forbidden")
	ErrArchived        = errors.New("agents: Agent is archived")
	ErrVersionConflict = errors.New("agents: Agent version conflict")
	ErrNameConflict    = errors.New("agents: Agent name already exists")
	ErrImmutableConfig = errors.New("agents: configuration version is immutable")
)

type Kind string

const (
	KindCode   Kind = "CODE"
	KindOffice Kind = "OFFICE"
)

type Visibility string

const (
	VisibilityPrivate Visibility = "PRIVATE"
	VisibilityShared  Visibility = "SHARED"
)

type DirectoryMode string

const (
	DirectoryNone      DirectoryMode = "NONE"
	DirectoryWorkspace DirectoryMode = "WORKSPACE"
	DirectoryExplicit  DirectoryMode = "EXPLICIT"
)

type Actor struct {
	AuthenticatedSession bool
	UserID               string
	Role                 policy.OrganizationRole
	AuthorizationVersion uint64
	RequestID            string
}

type ConfigInput struct {
	Instructions        string        `json:"instructions"`
	ComputerID          string        `json:"computer_id"`
	GrantID             string        `json:"grant_id"`
	Runtime             string        `json:"runtime"`
	Model               string        `json:"model"`
	MaxConcurrency      int           `json:"max_concurrency"`
	QueueTimeoutSeconds int64         `json:"queue_timeout_seconds"`
	RunTimeoutSeconds   int64         `json:"run_timeout_seconds"`
	DirectoryMode       DirectoryMode `json:"directory_mode"`
	DirectoryRoots      []string      `json:"directory_roots"`
	RunSettings
}

// RunSettings 包含运行设置与 Git 提交人（迁移 000028）。缺省值与旧版本行为一致。
type RunSettings struct {
	ChatConcurrency      string   `json:"chat_concurrency"`
	ChatConcurrencyValue *int     `json:"chat_concurrency_value"`
	WorkdirIsolation     string   `json:"workdir_isolation"`
	ApprovalMode         string   `json:"approval_mode"`
	CollaborationMode    string   `json:"collaboration_mode"`
	CustomArgs           []string `json:"custom_args"`
	GitAuthorNameMode    string   `json:"git_author_name_mode"`
	GitAuthorName        string   `json:"git_author_name"`
	GitAuthorEmailMode   string   `json:"git_author_email_mode"`
	GitAuthorEmail       string   `json:"git_author_email"`
	GitCoAuthorEnabled   *bool    `json:"git_co_author_enabled"`
}

type CreateInput struct {
	Name        string
	Description string
	Kind        Kind
	Visibility  Visibility
	Config      ConfigInput
	// 创建时可一并填写的资料；缺省与列默认值一致（头衔为空、无头像、允许指派与单聊）。
	Title           string
	Avatar          json.RawMessage
	AllowAssign     *bool
	AllowDirectChat *bool
}

type UpdateInput struct {
	Name        *string
	Description *string
	Kind        *Kind
	Visibility  *Visibility
	Archived    *bool
	Version     uint64
}

type ListOptions struct {
	IncludeArchived bool
}

type ConfigVersion struct {
	OrganizationID      string        `json:"organization_id"`
	AgentID             string        `json:"agent_id"`
	Version             uint64        `json:"config_version"`
	Instructions        string        `json:"instructions"`
	ComputerID          string        `json:"computer_id"`
	GrantID             string        `json:"grant_id"`
	Runtime             string        `json:"runtime"`
	Model               string        `json:"model"`
	MaxConcurrency      int           `json:"max_concurrency"`
	QueueTimeoutSeconds int64         `json:"queue_timeout_seconds"`
	RunTimeoutSeconds   int64         `json:"run_timeout_seconds"`
	DirectoryMode       DirectoryMode `json:"directory_mode"`
	DirectoryRoots      []string      `json:"directory_roots"`
	RunSettings
	// EnvDigest 是创建该版本时环境变量的摘要，仅用于审计；空串表示没有环境变量或无法计算。
	EnvDigest string    `json:"env_digest"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type Agent struct {
	OrganizationID string     `json:"organization_id"`
	SpaceID        string     `json:"space_id"`
	ID             string     `json:"id"`
	OwnerUserID    string     `json:"owner_user_id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Kind           Kind       `json:"kind"`
	Visibility     Visibility `json:"visibility"`
	// 资料：头衔、头像、可指派 / 可单聊、触发开关；ProfileVersion 是资料类修改的乐观锁。
	Title           string          `json:"title"`
	Avatar          json.RawMessage `json:"avatar"`
	AllowAssign     bool            `json:"allow_assign"`
	AllowDirectChat bool            `json:"allow_direct_chat"`
	TriggerCrew     bool            `json:"trigger_crew"`
	TriggerMention  bool            `json:"trigger_mention"`
	TriggerWorkflow bool            `json:"trigger_workflow"`
	ProfileVersion  uint64          `json:"profile_version"`
	// Online：当前配置的电脑在心跳窗口内且授权 ACTIVE；ComputerName 为空表示没有配置。
	Online       bool    `json:"online"`
	ComputerName *string `json:"computer_name"`
	Favorite     bool    `json:"favorite"`
	// BuiltinKind 非空表示内置 Agent（目前只有 ASSISTANT，即首页小助手），不出现在目录和空间列表里。
	BuiltinKind          string         `json:"builtin_kind,omitempty"`
	ArchivedAt           *time.Time     `json:"archived_at,omitempty"`
	CurrentConfigVersion uint64         `json:"current_config_version"`
	Version              uint64         `json:"version"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	Config               *ConfigVersion `json:"config,omitempty"`
}

type Store interface {
	Create(context.Context, tenancy.OrganizationSelection, Actor, string, CreateInput) (Agent, error)
	List(context.Context, tenancy.OrganizationSelection, Actor, string, ListOptions) ([]Agent, error)
	Get(context.Context, tenancy.OrganizationSelection, Actor, string, string) (Agent, error)
	Update(context.Context, tenancy.OrganizationSelection, Actor, string, string, UpdateInput) (Agent, error)
	CreateConfig(context.Context, tenancy.OrganizationSelection, Actor, string, string, uint64, ConfigInput) (Agent, error)
	ListConfigs(context.Context, tenancy.OrganizationSelection, Actor, string, string) ([]ConfigVersion, error)
}
