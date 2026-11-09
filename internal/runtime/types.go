// Package runtime defines the process-independent contract used by local
// Runtime adapters. It deliberately has no HTTP, database, or daemon types.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"openfox/internal/runtime/mcpconfig"
	"openfox/internal/runtimepolicy"
)

var (
	ErrInvalidRequest    = errors.New("runtime: invalid request")
	ErrUnavailable       = errors.New("runtime: unavailable")
	ErrUnsupported       = errors.New("runtime: unsupported")
	ErrProtocol          = errors.New("runtime: protocol error")
	ErrUnsupportedEvent  = errors.New("runtime: unsupported event")
	ErrApprovalRequired  = errors.New("runtime: interactive approval required")
	ErrOutputLimit       = errors.New("runtime: output limit exceeded")
	ErrEventLimit        = errors.New("runtime: event limit exceeded")
	ErrEventBackpressure = errors.New("runtime: event consumer is too slow")
	ErrAlreadyCancelled  = errors.New("runtime: execution already cancelled")
	ErrReconcileRequired = errors.New("runtime: reconciliation required")
)

const (
	DefaultRunTimeout   = 30 * time.Minute
	MaxRunTimeout       = 8 * time.Hour
	DefaultMaxLineBytes = 256 * 1024
	DefaultMaxOutput    = 8 * 1024 * 1024
	DefaultMaxEvents    = 100_000
	MaxPromptBytes      = 512 * 1024
	// MaxRuntimeArgs / MaxRuntimeArgBytes 限制 RunRequest.Args 的数量与单个长度。
	MaxRuntimeArgs     = 64
	MaxRuntimeArgBytes = 1024
)

type Status string

const (
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"
	StatusCancelled Status = "CANCELLED"
	StatusTimedOut  Status = "TIMED_OUT"
)

type EventType string

const (
	EventStarted          EventType = "started"
	EventOutput           EventType = "output"
	EventTool             EventType = "tool"
	EventApprovalRequired EventType = "approval_required"
	EventCompleted        EventType = "completed"
	EventFailed           EventType = "failed"
	// EventWarning 是 Runtime 报告的非致命诊断（如重连、配置告警），不决定 Run 成败。
	EventWarning   EventType = "warning"
	EventCancelled EventType = "cancelled"
	EventUnknown   EventType = "unknown"
	// EventStep 是执行步骤事件（schema v1）：Payload = {"schema":1,"step":{...}}，
	// 同一步骤以相同 step.id 多次上报，消费方按 id 合并。与上面的旧事件并存。
	EventStep EventType = "step"
)

// StepSchemaVersion 是 EventStep 载荷的 schema 版本。
const StepSchemaVersion = 1

type LoginStatus string

const (
	LoginLoggedIn  LoginStatus = "LOGGED_IN"
	LoginLoggedOut LoginStatus = "LOGGED_OUT"
	LoginUnknown   LoginStatus = "UNKNOWN"
)

type RecoveryStatus string

const (
	RecoveryUnsupported RecoveryStatus = "UNSUPPORTED"
	RecoveryReconcile   RecoveryStatus = "RECONCILIATION_REQUIRED"
)

type Capabilities struct {
	Runtime            string      `json:"runtime"`
	Version            string      `json:"version"`
	Executable         string      `json:"executable"`
	Login              LoginStatus `json:"login_status"`
	Runnable           bool        `json:"runnable"`
	Reason             string      `json:"reason,omitempty"`
	Models             []string    `json:"models,omitempty"`
	SupportsJSONEvents bool        `json:"supports_json_events"`
	SupportsCancel     bool        `json:"supports_cancel"`
	SupportsResume     bool        `json:"supports_resume"`
	DirectoryModes     []string    `json:"directory_modes,omitempty"`
}

type RunRequest struct {
	// Assigned only by the supervisor after the center authorizes the run.
	ProcessScope                  string
	SessionIdleTTL, SessionMaxAge time.Duration
	MCPServers                    mcpconfig.Servers
	MCPSecrets                    []string
	Prompt                        string
	Instructions                  string
	Model                         string
	WorkingDirectory              string
	Environment                   map[string]string
	EnvironmentAllowlist          []string
	// Args 是追加给 Runtime CLI 的参数（例如 Codex 的 `-c key=value` 配置覆盖）。
	// 调用方负责按白名单筛选；适配器只接受自己认识的形式。
	Args []string
	// ApprovalMode 是本次 Run 要求的权限档位（runtimepolicy 的四档）；空串等于 FOLLOW_RUNTIME，行为与之前完全一致。
	// 适配器不支持该档位时必须在 Start 里返回 ErrUnsupported，绝不能退回更宽的默认行为。
	ApprovalMode string
	// CollaborationMode 是本次 Run 要求的协作模式（FOLLOW_RUNTIME / DEFAULT / PLAN）；空串与历史值 FOLLOW_AGENT
	// 等于 FOLLOW_RUNTIME，行为与之前完全一致。适配器不支持该模式时必须在 Start 里返回 ErrUnsupported，绝不能忽略。
	// PLAN 比任何权限档位都严格：与 ApprovalMode 同时设置时以 PLAN 为准。
	CollaborationMode string
	// ReasoningEffort 是本次 Run 要求的模型思考深度；空串等于 DEFAULT。
	ReasoningEffort string
	// ResumeSession 非空时续接该 Runtime 会话（Claude Code / OpenCode / pi 支持）；不支持的适配器忽略。
	ResumeSession  string
	Timeout        time.Duration
	MaxLineBytes   int
	MaxOutputBytes int
	MaxEvents      int
	// Skills 是本次 Run 要下发的技能；为空时与之前行为完全一致。
	// 适配器负责写入其独立的运行目录；内容不得写入日志或事件。
	Skills []Skill
	// Tools 是本次 Run 向模型声明的动态工具（Codex dynamicTools）；为空时行为与之前完全一致。
	Tools []Tool
	// ToolCall 执行模型发起的动态工具调用，返回给模型看的文本与是否成功。
	// 实现须自行限制耗时，并保证返回文本不含令牌；适配器另加超时并对日志脱敏。
	ToolCall func(ctx context.Context, name, callID string, arguments json.RawMessage) (text string, ok bool)
	// ArtifactDirectory 是电脑端为本次 Run 新建的专属产物目录（绝对路径，工作目录之外）。
	// 非空时适配器把它加入 Runtime 的可写目录（Codex workspace-write 的 writableRoots、Claude Code 的 --add-dir），
	// 只读档位与 PLAN 不放宽；为空时行为与之前完全一致。
	ArtifactDirectory string
}

// ToolCapableAdapter 由能把 RunRequest.Tools 暴露给模型的适配器实现：
// 返回 false 的适配器（例如 pi）不声明发布工具，提示词里也不提。
type ToolCapableAdapter interface {
	SupportsTools() bool
}

// Tool 是一个动态工具声明：InputSchema 为 JSON Schema（object）。
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// MaxTools 限制单次 Run 声明的动态工具数量。
const MaxTools = 16

// Skill 是一个技能包：Name 为目录名，Files 的 Path 相对技能目录、用 / 分隔。
type Skill struct {
	Name  string
	Files []SkillFile
}

type SkillFile struct {
	Path    string
	Content []byte
}

type Event struct {
	Sequence      uint64          `json:"sequence"`
	Type          EventType       `json:"type"`
	At            time.Time       `json:"at"`
	Text          string          `json:"text,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Critical      bool            `json:"-"`
	CriticalError error           `json:"-"`
}

type Result struct {
	Status        Status    `json:"status"`
	ExitCode      int       `json:"exit_code"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	EventCount    uint64    `json:"event_count"`
	Output        string    `json:"output,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	Error         string    `json:"error,omitempty"`
	ProcessExited bool      `json:"process_exited"`
	Recovery      Recovery  `json:"recovery"`
	// Usage 是 Runtime 报告的整轮 token 用量；Runtime 未报告时为 nil。
	Usage *Usage `json:"usage,omitempty"`
	// SessionID 是 Runtime 报告的会话 ID，可用于之后以 ResumeSession 续会话；未报告时为空。
	SessionID string `json:"session_id,omitempty"`
}

// SessionFile is one credential-free native Runtime session file selected for export.
// The caller is responsible for wrapping it in an archive and protecting the contents.
type SessionFile struct {
	Name string
	Data []byte
}

// NativeSessionExporter is an optional adapter capability for exporting one native session.
type NativeSessionExporter interface {
	ExportSession(context.Context, string) (SessionFile, error)
}

// Usage 是 token 用量（累计值）。各字段非负。
type Usage struct {
	Input           int64 `json:"input"`
	CachedInput     int64 `json:"cached_input"`
	CacheWriteInput int64 `json:"cache_write_input"`
	Output          int64 `json:"output"`
	ReasoningOutput int64 `json:"reasoning_output"`
}

type Recovery struct {
	Status RecoveryStatus `json:"status"`
	Reason string         `json:"reason,omitempty"`
}

type Adapter interface {
	Name() string
	Detect(context.Context) (Capabilities, error)
	Start(context.Context, RunRequest) (Execution, error)
}

type Execution interface {
	Events() <-chan Event
	Wait(context.Context) (Result, error)
	Cancel(context.Context) error
	Recovery() Recovery
}

func (r RunRequest) Normalize() (RunRequest, error) {
	r.Prompt = strings.TrimSpace(r.Prompt)
	r.Instructions = strings.TrimSpace(r.Instructions)
	r.Model = strings.TrimSpace(r.Model)
	switch {
	case r.ApprovalMode == "":
		r.ApprovalMode = runtimepolicy.FollowRuntime
	case !runtimepolicy.ValidApprovalMode(r.ApprovalMode):
		return RunRequest{}, fmt.Errorf("%w: unknown approval mode", ErrInvalidRequest)
	}
	if r.CollaborationMode = runtimepolicy.NormalizeCollaborationMode(r.CollaborationMode); !runtimepolicy.ValidCollaborationMode(r.CollaborationMode) {
		return RunRequest{}, fmt.Errorf("%w: unknown collaboration mode", ErrInvalidRequest)
	}
	// Prompt 与 Instructions 是多行自然语言，允许换行与制表符；其他控制字符仍拒绝。
	if r.Prompt == "" || len([]byte(r.Prompt)) > MaxPromptBytes || hasUnsafeTextControl(r.Prompt) {
		return RunRequest{}, ErrInvalidRequest
	}
	if len([]byte(r.Model)) > 128 || hasUnsafeTextControl(r.Instructions) || hasControl(r.Model) {
		return RunRequest{}, ErrInvalidRequest
	}
	if r.WorkingDirectory != "" {
		if !filepath.IsAbs(r.WorkingDirectory) || filepath.Clean(r.WorkingDirectory) != r.WorkingDirectory || hasControl(r.WorkingDirectory) {
			return RunRequest{}, fmt.Errorf("%w: working directory must be a clean absolute path", ErrInvalidRequest)
		}
	}
	if r.ArtifactDirectory != "" {
		if !filepath.IsAbs(r.ArtifactDirectory) || filepath.Clean(r.ArtifactDirectory) != r.ArtifactDirectory || hasControl(r.ArtifactDirectory) {
			return RunRequest{}, fmt.Errorf("%w: artifact directory must be a clean absolute path", ErrInvalidRequest)
		}
	}
	if r.Timeout == 0 {
		r.Timeout = DefaultRunTimeout
	}
	if r.Timeout <= 0 || r.Timeout > MaxRunTimeout {
		return RunRequest{}, fmt.Errorf("%w: timeout outside supported range", ErrInvalidRequest)
	}
	if r.MaxLineBytes == 0 {
		r.MaxLineBytes = DefaultMaxLineBytes
	}
	if r.MaxOutputBytes == 0 {
		r.MaxOutputBytes = DefaultMaxOutput
	}
	if r.MaxEvents == 0 {
		r.MaxEvents = DefaultMaxEvents
	}
	if r.MaxLineBytes < 1 || r.MaxLineBytes > DefaultMaxLineBytes || r.MaxOutputBytes < 1 || r.MaxOutputBytes > DefaultMaxOutput || r.MaxEvents < 1 || r.MaxEvents > DefaultMaxEvents {
		return RunRequest{}, fmt.Errorf("%w: output limits outside supported range", ErrInvalidRequest)
	}
	if len(r.Args) > MaxRuntimeArgs {
		return RunRequest{}, fmt.Errorf("%w: too many runtime arguments", ErrInvalidRequest)
	}
	for _, arg := range r.Args {
		if arg == "" || len(arg) > MaxRuntimeArgBytes || hasControl(arg) {
			return RunRequest{}, fmt.Errorf("%w: invalid runtime argument", ErrInvalidRequest)
		}
	}
	r.Args = append([]string(nil), r.Args...)
	if len(r.Tools) > MaxTools {
		return RunRequest{}, fmt.Errorf("%w: too many tools", ErrInvalidRequest)
	}
	if len(r.Tools) > 0 {
		tools := make([]Tool, 0, len(r.Tools))
		for _, tool := range r.Tools {
			if !validToolName(tool.Name) || len(tool.Description) > 2048 || hasUnsafeTextControl(tool.Description) ||
				len(tool.InputSchema) > 16*1024 || !json.Valid(tool.InputSchema) {
				return RunRequest{}, fmt.Errorf("%w: invalid tool %q", ErrInvalidRequest, tool.Name)
			}
			tool.InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
			tools = append(tools, tool)
		}
		r.Tools = tools
	}
	if len(r.ResumeSession) > 256 || hasControl(r.ResumeSession) || strings.HasPrefix(r.ResumeSession, "-") {
		return RunRequest{}, fmt.Errorf("%w: invalid resume session", ErrInvalidRequest)
	}
	if r.Environment == nil {
		r.Environment = map[string]string{}
	}
	allowed := make(map[string]struct{}, len(r.EnvironmentAllowlist))
	for _, key := range r.EnvironmentAllowlist {
		if !validEnvName(key) {
			return RunRequest{}, fmt.Errorf("%w: invalid environment allowlist", ErrInvalidRequest)
		}
		allowed[key] = struct{}{}
	}
	for key, value := range r.Environment {
		// 值允许多行（例如 PEM），但不允许 NUL 与其他控制字符。
		if !validEnvName(key) || hasUnsafeTextControl(value) {
			return RunRequest{}, fmt.Errorf("%w: invalid environment entry", ErrInvalidRequest)
		}
		if _, ok := allowed[key]; !ok {
			return RunRequest{}, fmt.Errorf("%w: environment key %q is not allowlisted", ErrInvalidRequest, key)
		}
	}
	return r, nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) }) >= 0
}

func hasUnsafeTextControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) >= 0
}

func validEnvName(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func validToolName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// RequireApprovalMode 校验适配器是否支持请求的权限档位；FOLLOW_RUNTIME 永远支持。
// 不支持时返回 ErrUnsupported（调用方据此在准备阶段失败，不执行）。
func RequireApprovalMode(runtimeName string, supported []string, mode string) error {
	if mode == "" || mode == runtimepolicy.FollowRuntime {
		return nil
	}
	for _, item := range supported {
		if item == mode {
			return nil
		}
	}
	return fmt.Errorf("%w: %s does not support approval mode %s", ErrUnsupported, runtimeName, mode)
}

// RequireCollaborationMode 校验适配器是否支持请求的协作模式；FOLLOW_RUNTIME（及空串、FOLLOW_AGENT）永远支持。
// 不支持时返回 ErrUnsupported（调用方据此在准备阶段失败，不执行，更不会忽略该模式按普通模式执行）。
func RequireCollaborationMode(runtimeName string, supported []string, mode string) error {
	mode = runtimepolicy.NormalizeCollaborationMode(mode)
	if mode == runtimepolicy.CollaborationFollowRuntime {
		return nil
	}
	for _, item := range supported {
		if item == mode {
			return nil
		}
	}
	return fmt.Errorf("%w: %s does not support collaboration mode %s", ErrUnsupported, runtimeName, mode)
}

// RequireReasoningEffort 校验适配器是否支持请求的思考深度；DEFAULT（及空串）永远支持。
func RequireReasoningEffort(runtimeName string, supported []string, effort string) error {
	if effort == "" || effort == runtimepolicy.ReasoningDefault {
		return nil
	}
	for _, item := range supported {
		if item == effort {
			return nil
		}
	}
	return fmt.Errorf("%w: %s does not support reasoning effort %s", ErrUnsupported, runtimeName, effort)
}
