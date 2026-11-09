// Package commands 执行中心下发的远程命令（device_commands）：WSS device.command 唤醒或心跳后拉取
// → claim（中心 CAS）→ 按 kind 执行（同类串行、带超时）→ 回执结果。命令内容以设备 HTTPS 端点为准，
// 唤醒只是提示。
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
)

const (
	KindWorkspacePath        = "workspace_path"
	KindDeviceSettings       = "device_settings"
	KindRuntimeProbe         = "runtime_probe"
	KindRuntimeTest          = "runtime_test"
	KindRuntimeSessionExport = "runtime_session_export"
	KindStorageCleanup       = "storage_cleanup"
	KindSkillScan            = "skill_scan"
	KindSkillImport          = "skill_import"

	// 每次拉取每个组织最多处理的命令数，与中心 limit 一致。
	pendingLimit = 20
	// 结果上限，与 device_commands.result 的约束一致。
	MaxResultBytes = 16 * 1024
	// MaxSkillScanResultBytes 是 skill_scan 结果的上限（DeviceSkillScanResult ≤ 64 KB）。
	MaxSkillScanResultBytes = 64 * 1024
	// 回执重试：最多 resultAttempts 次，总时限 resultDeadline。
	resultAttempts = 4
	resultDeadline = 30 * time.Second
	// 没有唤醒时的兜底轮询周期（心跳成功后也会拉取）。
	fallbackPoll = 2 * time.Minute
)

var errorCodePattern = regexp.MustCompile(`^[A-Z0-9_]{0,64}$`)

// ErrInvalidParams 由处理器返回，表示命令参数不合法（回执 FAILED INVALID_PARAMS）。
var ErrInvalidParams = errors.New("commands: invalid params")

// Client 是设备侧命令端点（supervisor.Client 实现）。
type Client interface {
	PendingCommands(ctx context.Context, organizationID string, limit int) ([]supervisor.DeviceCommand, error)
	ClaimCommand(ctx context.Context, organizationID, commandID string) (supervisor.DeviceCommand, error)
	CommandResult(ctx context.Context, organizationID, commandID, status string, result json.RawMessage, errorCode string) error
}

// Handler 执行一条命令，返回结果对象。返回错误时回执 FAILED；错误可用 CodedError 指定 error_code。
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// CodedError 让处理器指定回执的 error_code（只允许 ^[A-Z0-9_]{0,64}$）。
type CodedError struct {
	Code   string
	Result any
}

func (e *CodedError) Error() string { return "commands: " + e.Code }

type Config struct {
	Client        Client
	Organizations []string
	Handlers      map[string]Handler
	// Timeouts 按 kind 指定执行上限；缺省 30 秒。
	Timeouts map[string]time.Duration
	Logger   *slog.Logger
	// PollEvery 为 0 时取 fallbackPoll。
	PollEvery time.Duration
}

// DefaultTimeouts 是规格约定的执行上限：probe / test / skill_scan 30 秒，cleanup / skill_import 5 分钟。
func DefaultTimeouts() map[string]time.Duration {
	return map[string]time.Duration{
		KindDeviceSettings: 30 * time.Second, KindRuntimeProbe: 30 * time.Second, KindRuntimeTest: 30 * time.Second, KindRuntimeSessionExport: 3 * time.Minute, KindStorageCleanup: 5 * time.Minute,
		KindSkillScan: 30 * time.Second, KindSkillImport: 5 * time.Minute,
	}
}

// resultLimit 返回该 kind 的结果字节上限。
func resultLimit(kind string) int {
	if kind == KindSkillScan {
		return MaxSkillScanResultBytes
	}
	return MaxResultBytes
}

type commandContextKey struct{}

type commandIdentity struct{ organizationID, commandID string }

// FromContext 返回处理器正在执行的命令所属的组织与命令 ID（skill_import 上传技能时需要）。
func FromContext(ctx context.Context) (organizationID, commandID string, ok bool) {
	identity, ok := ctx.Value(commandContextKey{}).(commandIdentity)
	return identity.organizationID, identity.commandID, ok
}

type Executor struct {
	config Config
	wakes  chan struct{}

	mu            sync.Mutex
	organizations map[string]struct{}
	busy          map[string]bool
	wg            sync.WaitGroup
}

func New(config Config) (*Executor, error) {
	if config.Client == nil || len(config.Handlers) == 0 {
		return nil, errors.New("commands: client and handlers are required")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Timeouts == nil {
		config.Timeouts = DefaultTimeouts()
	}
	if config.PollEvery <= 0 {
		config.PollEvery = fallbackPoll
	}
	executor := &Executor{config: config, wakes: make(chan struct{}, 1), organizations: map[string]struct{}{}, busy: map[string]bool{}}
	for _, organizationID := range config.Organizations {
		if organizationID = strings.TrimSpace(organizationID); organizationID != "" {
			executor.organizations[organizationID] = struct{}{}
		}
	}
	return executor, nil
}

// Wake 请求尽快拉取一次；非阻塞，多次请求合并。
func (e *Executor) Wake() {
	select {
	case e.wakes <- struct{}{}:
	default:
	}
}

// HandleMessage 是 RemoteClient.OnCommandMessage 回调：登记组织并唤醒。
func (e *Executor) HandleMessage(message daemon.Message) {
	if message.Type != daemon.MessageDeviceCommand {
		return
	}
	if organizationID := strings.TrimSpace(message.OrganizationID); organizationID != "" {
		e.mu.Lock()
		e.organizations[organizationID] = struct{}{}
		e.mu.Unlock()
	}
	e.Wake()
}

// Run 处理唤醒与兜底轮询，直到 ctx 取消；返回前等待在途命令结束（命令自身有超时）。
func (e *Executor) Run(ctx context.Context) {
	ticker := time.NewTicker(e.config.PollEvery)
	defer ticker.Stop()
	defer e.wg.Wait()
	e.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.wakes:
		case <-ticker.C:
		}
		e.poll(ctx)
	}
}

func (e *Executor) poll(ctx context.Context) {
	e.mu.Lock()
	organizations := make([]string, 0, len(e.organizations))
	for organizationID := range e.organizations {
		organizations = append(organizations, organizationID)
	}
	e.mu.Unlock()
	sort.Strings(organizations)
	for _, organizationID := range organizations {
		commands, err := e.config.Client.PendingCommands(ctx, organizationID, pendingLimit)
		if err != nil {
			if ctx.Err() == nil && !supervisor.IsAPIError(err, http.StatusNotFound, "") {
				e.config.Logger.Warn("commands_list_failed", "organization_id", organizationID, "error", err.Error())
			}
			continue
		}
		for _, command := range commands {
			if command.Status != "" && command.Status != "PENDING" {
				continue
			}
			e.start(ctx, organizationID, command)
		}
	}
}

// start 在该 kind 空闲时领取并执行命令；同一 kind 串行（中心也保证同 kind 只有一条未完成）。
func (e *Executor) start(ctx context.Context, organizationID string, command supervisor.DeviceCommand) {
	kind := command.Kind
	e.mu.Lock()
	if e.busy[kind] {
		e.mu.Unlock()
		return
	}
	e.busy[kind] = true
	e.mu.Unlock()
	release := func() {
		e.mu.Lock()
		delete(e.busy, kind)
		e.mu.Unlock()
	}
	claimed, err := e.config.Client.ClaimCommand(ctx, organizationID, command.ID)
	if err != nil {
		release()
		if !supervisor.IsAPIError(err, http.StatusConflict, "") && ctx.Err() == nil {
			e.config.Logger.Warn("commands_claim_failed", "command_id", command.ID, "kind", kind, "error", err.Error())
		}
		return
	}
	if claimed.ID == "" {
		claimed = command
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			release()
			// 同类可能还有排队的命令（例如前一条过期后新建的）。
			e.Wake()
		}()
		e.execute(ctx, organizationID, claimed)
	}()
}

func (e *Executor) execute(ctx context.Context, organizationID string, command supervisor.DeviceCommand) {
	logger := e.config.Logger.With("command_id", command.ID, "kind", command.Kind)
	status, result, code := "SUCCEEDED", any(nil), ""
	handler, ok := e.config.Handlers[command.Kind]
	if !ok {
		status, code = "FAILED", "UNSUPPORTED_KIND"
	} else {
		timeout := e.config.Timeouts[command.Kind]
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		runContext, cancel := context.WithTimeout(context.WithValue(ctx, commandContextKey{}, commandIdentity{organizationID, command.ID}), timeout)
		started := time.Now()
		value, err := handler(runContext, command.Params)
		cancel()
		result = value
		if err != nil {
			status = "FAILED"
			var coded *CodedError
			switch {
			case errors.As(err, &coded):
				code = coded.Code
				if coded.Result != nil {
					result = coded.Result
				}
			case errors.Is(err, ErrInvalidParams):
				code = "INVALID_PARAMS"
			case errors.Is(err, context.DeadlineExceeded):
				code = "TIMEOUT"
			case ctx.Err() != nil:
				code = "DAEMON_SHUTDOWN"
			default:
				code = "COMMAND_FAILED"
			}
			logger.Warn("commands_execute_failed", "error_code", code, "duration_ms", time.Since(started).Milliseconds())
		} else {
			logger.Info("commands_executed", "duration_ms", time.Since(started).Milliseconds())
		}
	}
	if !errorCodePattern.MatchString(code) {
		code = "COMMAND_FAILED"
	}
	encoded := encodeResult(result)
	if len(encoded) > resultLimit(command.Kind) {
		status, code, encoded = "FAILED", "RESULT_TOO_LARGE", nil
	}
	if status == "FAILED" {
		if code == "" {
			// 中心要求 FAILED 带非空 error_code。
			code = "COMMAND_FAILED"
		}
		if result == nil {
			// FAILED 允许 result 为 null；SUCCEEDED 必须是 object（encodeResult 保证）。
			encoded = json.RawMessage(`null`)
		}
	}
	e.report(context.WithoutCancel(ctx), logger, organizationID, command.ID, status, encoded, code)
}

// encodeResult 把结果编码为 JSON object；nil 或非 object 时为 {}。
func encodeResult(result any) json.RawMessage {
	if result == nil {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) == 0 || encoded[0] != '{' {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// report 回执结果：瞬时失败退避重试；中心永久拒绝（409 COMMAND_EXPIRED 超过执行截止、
// 409 COMMAND_CONFLICT 终态不同）只记录。
func (e *Executor) report(ctx context.Context, logger *slog.Logger, organizationID, commandID, status string, result json.RawMessage, code string) {
	reportContext, cancel := context.WithTimeout(ctx, resultDeadline)
	defer cancel()
	delay := 500 * time.Millisecond
	var err error
	for attempt := 0; attempt < resultAttempts; attempt++ {
		if err = e.config.Client.CommandResult(reportContext, organizationID, commandID, status, result, code); err == nil || supervisor.Permanent(err) {
			break
		}
		select {
		case <-reportContext.Done():
			attempt = resultAttempts
		case <-time.After(delay):
			delay *= 2
		}
	}
	if err != nil {
		logger.Warn("commands_result_failed", "status", status, "error", err.Error())
	}
}

// WithIdentityForTest 构造带命令身份的上下文，仅供其他包的测试直接调用处理器时使用。
func WithIdentityForTest(ctx context.Context, organizationID, commandID string) context.Context {
	return context.WithValue(ctx, commandContextKey{}, commandIdentity{organizationID, commandID})
}
