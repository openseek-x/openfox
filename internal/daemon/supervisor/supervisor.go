// Package supervisor 是 Daemon 侧的 Run 执行监督者：响应中心的派发唤醒或轮询 offer，
// 按 claim → 上下文 → 工作目录 → 启动许可 → Runtime 进程 → 事件/结果上传 的顺序执行，
// 并在整个过程中续租。租约丢失、授权失效或用户取消时停止进程。
//
// 安全约束：
//   - 只有拿到 start 许可（RUNNING 租约）后才启动 Runtime 进程；
//   - 本地 SQLite 日志按 (run, generation) 去重，重复唤醒或重启不会产生第二个进程；
//   - 续租失败且本地租约期限（以发送请求时刻起算）已过，即停止进程（失效即安全）。
package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"openfox/internal/daemon"
	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
	"openfox/internal/runtime/mcpconfig"
	"openfox/internal/runtimepolicy"
)

const (
	DefaultPollInterval  = 15 * time.Second
	DefaultMaxConcurrent = 4

	// 停止进程时等待其退出的上限，覆盖 runtime 的 SIGINT→SIGTERM→SIGKILL 升级。
	stopTimeout = 20 * time.Second
	// 结果上传的总时限与最大尝试次数（同一结果 ID 幂等重试）。
	resultDeadline = time.Minute
	resultAttempts = 5
	// 事件批量上传阈值，低于中心的 100 条 / 256KiB 上限。
	eventBatchSize  = 50
	eventBatchBytes = 128 * 1024
	eventFlushEvery = 500 * time.Millisecond
	maxEventText    = 8 * 1024
	// 单条 data 上限：中心单条 safe_payload 上限 64 KiB，留出 text 与包装的余量。
	maxEventData    = 56 * 1024
	maxEventPayload = 64 * 1024
	// 结果字段上限，与中心 executiondelivery 一致。
	maxResultOutput  = 256 * 1024
	maxResultSummary = 16 * 1024
	maxPrepareDetail = 1024
)

var (
	errCancelRequested = errors.New("supervisor: run cancellation requested")
	errLeaseLost       = errors.New("supervisor: run lease lost")
	errShutdown        = errors.New("supervisor: daemon shutting down")
)

// Config 配置执行监督者。Organizations 是轮询兜底的组织列表；收到唤醒的组织会自动加入。
type Config struct {
	DeviceID string
	Client   *Client
	Adapter  runtime.Adapter
	// Extra 是 Adapter 之外的其他 Runtime 适配器；Run 按 RunContext.Runtime 选择适配器。
	Extra   []runtime.Adapter
	Workdir *workdir.Manager
	// DirectoryRoots 把 Agent 配置中 EXPLICIT 模式的逻辑根名映射到本机 Git 仓库绝对路径。
	// 每个对话在管理根下创建独立 worktree，不修改原仓库的工作区。
	DirectoryRoots map[string]string
	Journal        *Journal
	Organizations  []string
	PollInterval   time.Duration
	MaxConcurrent  int
	Logger         *slog.Logger
	// CleanupHomes 删除过期的独立 CODEX_HOME，返回删除数与释放字节数（storage_cleanup 使用）；可为空。
	CleanupHomes func(time.Time) (int, int64)
	// CleanupSessionExports removes expired native Codex rollout snapshots; can be nil.
	CleanupSessionExports func(time.Time) (int, int64)
	// AttachmentRoot 是消息附件的下载根目录：每个 Run 一个子目录，Run 结束后删除。
	// 必须在工作目录之外（DIRECT 隔离会直接用用户的真实仓库）；为空时用系统临时目录下的 openfox-attachments。
	AttachmentRoot string
	// ArtifactRoot 是会话产物目录的根：每个 Run 一个子目录 <ArtifactRoot>/<runID>，写进提示词供 Agent 保存要交付的文件，
	// Run 结束后删除。必须在工作目录之外；为空时用系统临时目录下的 openfox-run-artifacts。
	ArtifactRoot string
}

type activeRun struct {
	generation int64
	cancel     context.CancelCauseFunc
}

type Supervisor struct {
	config      Config
	prefsMu     sync.RWMutex
	preferences DeviceSettings
	wakes       chan string
	slots       chan struct{}

	mu            sync.Mutex
	organizations map[string]struct{}
	active        map[string]*activeRun
	// uploading 是有上传器在工作的 Run（计数）；outbox 补传跳过这些 Run，避免与上传器并发。
	uploading map[string]int
	wg        sync.WaitGroup
}

// adapterFor 返回名称对应的 Runtime 适配器；没有时返回 nil。
func (s *Supervisor) adapterFor(name string) runtime.Adapter {
	if s.config.Adapter.Name() == name {
		return s.config.Adapter
	}
	for _, adapter := range s.config.Extra {
		if adapter != nil && adapter.Name() == name {
			return adapter
		}
	}
	return nil
}

func New(config Config) (*Supervisor, error) {
	if config.Client == nil || config.Adapter == nil || config.Workdir == nil || config.Journal == nil {
		return nil, errors.New("supervisor: client, adapter, workdir and journal are required")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = DefaultPollInterval
	}
	if config.PollInterval < time.Second || config.PollInterval > 10*time.Minute {
		return nil, errors.New("supervisor: poll interval must be between 1s and 10m")
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = DefaultMaxConcurrent
	}
	if config.MaxConcurrent > 64 {
		return nil, errors.New("supervisor: max concurrent runs must be at most 64")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	s := &Supervisor{
		config: config, wakes: make(chan string, 64), slots: make(chan struct{}, config.MaxConcurrent),
		organizations: map[string]struct{}{}, active: map[string]*activeRun{}, uploading: map[string]int{},
	}
	for _, organizationID := range config.Organizations {
		if organizationID = strings.TrimSpace(organizationID); organizationID != "" {
			s.organizations[organizationID] = struct{}{}
		}
	}
	if err := s.loadDeviceSettings(); err != nil {
		return nil, err
	}
	return s, nil
}

// ActiveRuns 返回正在执行的 Run 数（含准备阶段）。
func (s *Supervisor) ActiveRuns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// HandleMessage 是 RemoteClient.OnRunMessage 回调：非阻塞，只做登记与转发。
func (s *Supervisor) HandleMessage(message daemon.Message) {
	switch message.Type {
	case daemon.MessageRunDispatch:
		s.mu.Lock()
		s.organizations[message.OrganizationID] = struct{}{}
		s.mu.Unlock()
		select {
		case s.wakes <- message.OrganizationID:
		default:
			// 唤醒队列满时丢弃，周期轮询会兜底。
		}
	case daemon.MessageRunCancel:
		s.mu.Lock()
		run := s.active[message.RunID]
		s.mu.Unlock()
		if run != nil && run.generation == message.DispatchGeneration {
			run.cancel(errCancelRequested)
		}
	}
}

// Run 处理唤醒与周期轮询，直到 ctx 取消；返回前停止并等待所有在途 Run。
func (s *Supervisor) Run(ctx context.Context) error {
	if abandoned, err := s.config.Journal.Recover(ctx); err != nil {
		return err
	} else if abandoned > 0 {
		s.config.Logger.Warn("supervisor_abandoned_runs", "count", abandoned)
	}
	if s.config.CleanupSessionExports != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			cleanup := func() {
				removed, freed := s.config.CleanupSessionExports(time.Now().UTC())
				if removed > 0 {
					s.config.Logger.Info("runtime_session_exports_expired", "count", removed, "freed_bytes", freed)
				}
			}
			cleanup()
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					cleanup()
				}
			}
		}()
	}
	// 在途 Run 的上下文不直接继承 ctx 的取消，退出时显式以 errShutdown 取消，
	// 让执行协程能区分“Daemon 退出”与用户取消。
	runContext, stopAll := context.WithCancelCause(context.WithoutCancel(ctx))
	defer func() {
		stopAll(errShutdown)
		s.wg.Wait()
		if closer, ok := s.config.Adapter.(interface{ CloseSessions() }); ok {
			closer.CloseSessions()
		}
		// 进程退出前尽力上传积压的事件（在途 Run 已全部结束）。
		drainContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), outboxShutdownBudget)
		defer cancel()
		s.drainOutbox(drainContext)
	}()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.runStoragePolicies(ctx) }()
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	s.pollAll(runContext)
	for {
		select {
		case <-ctx.Done():
			return nil
		case organizationID := <-s.wakes:
			s.poll(runContext, organizationID)
		case <-ticker.C:
			s.pollAll(runContext)
		}
	}
}

func (s *Supervisor) pollAll(ctx context.Context) {
	s.mu.Lock()
	organizations := make([]string, 0, len(s.organizations))
	for organizationID := range s.organizations {
		organizations = append(organizations, organizationID)
	}
	s.mu.Unlock()
	for _, organizationID := range organizations {
		s.poll(ctx, organizationID)
	}
	// 周期重试上次上传失败、写入 outbox 的事件。
	drainContext, cancel := context.WithTimeout(ctx, outboxShutdownBudget)
	defer cancel()
	s.drainOutbox(drainContext)
}

func (s *Supervisor) poll(ctx context.Context, organizationID string) {
	offers, err := s.config.Client.ListOffers(ctx, organizationID)
	if err != nil {
		if ctx.Err() == nil {
			s.config.Logger.Warn("supervisor_list_offers_failed", "organization_id", organizationID, "error", err.Error())
		}
		return
	}
	pressure := false
	if len(offers) > 0 {
		pressure = underResourcePressure(ctx, s.DeviceSettings().Settings)
	}
	for _, offer := range offers {
		if pressure && s.ActiveRuns() > 0 {
			return
		}
		offer.OrganizationID = organizationID
		s.launch(ctx, offer)
	}
}

// launch 在并发槽位允许时为 offer 启动执行协程；已在执行的 Run 直接跳过。
func (s *Supervisor) launch(ctx context.Context, offer Offer) {
	s.mu.Lock()
	if _, exists := s.active[offer.RunID]; exists {
		s.mu.Unlock()
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.Unlock()
		// 本地槽位满：不领取，offer 过期后中心会重新派发。
		return
	}
	runContext, cancel := context.WithCancelCause(ctx)
	s.active[offer.RunID] = &activeRun{generation: offer.Generation, cancel: cancel}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			cancel(nil)
			s.mu.Lock()
			delete(s.active, offer.RunID)
			s.mu.Unlock()
			<-s.slots
		}()
		s.execute(runContext, cancel, offer)
	}()
}

// execute 执行单个 offer 的完整生命周期。
func (s *Supervisor) execute(ctx context.Context, cancel context.CancelCauseFunc, offer Offer) {
	logger := s.config.Logger.With("organization_id", offer.OrganizationID, "run_id", offer.RunID, "generation", offer.Generation)
	journal := s.config.Journal
	// 日志写入不跟随 Run 取消，保证终态能落盘。
	local := context.WithoutCancel(ctx)
	fresh, err := journal.Begin(local, offer.OrganizationID, offer.RunID, offer.Generation, offer.OfferID)
	if err != nil {
		logger.Error("supervisor_journal_failed", "error", err.Error())
		return
	}
	if !fresh {
		// 本机已处理过该代次（包括重启前），绝不重复启动。
		return
	}
	lease, err := s.config.Client.Claim(ctx, offer.OrganizationID, offer.OfferID)
	if err != nil {
		_ = journal.Mark(local, offer.RunID, offer.Generation, JournalAbandoned, "claim failed: "+errorCode(err))
		logger.Info("supervisor_claim_failed", "error", err.Error())
		return
	}
	sentAt := time.Now()
	run := &runState{
		supervisor: s, offer: offer, logger: logger, cancel: cancel,
		deadline:   sentAt.Add(time.Duration(lease.LeaseTTLMillis) * time.Millisecond),
		renewEvery: time.Duration(lease.RenewMillis) * time.Millisecond,
	}
	run.events = newEventUploader(run)
	defer run.events.close()
	renewDone := make(chan struct{})
	stopRenew := make(chan struct{})
	go func() {
		defer close(renewDone)
		run.renewLoop(ctx, stopRenew)
	}()
	defer func() {
		close(stopRenew)
		<-renewDone
	}()

	status, detail := run.prepareAndRun(ctx)
	_ = journal.Mark(local, offer.RunID, offer.Generation, status, detail)
}

// runState 持有单个 Run 在执行期间的可变状态。
type runState struct {
	supervisor *Supervisor
	offer      Offer
	logger     *slog.Logger
	cancel     context.CancelCauseFunc

	mu         sync.Mutex
	deadline   time.Time
	renewEvery time.Duration

	// events 上传本 Run 的全部事件；prep 记录 prep_env 步骤的进度。
	events *eventUploader
	prep   prepProgress
	// redact 在取得 RunContext 后设置（启动 Runtime 事件协程之前），对上传内容脱敏。
	redact *redactor
	// artifacts 是本次 Run 的发布工具状态；没有声明发布工具时为 nil。
	artifacts *artifactPublisher
}

// renewLoop 以服务端给出的周期续租，直到 stop 关闭。续租请求的发送时刻
// 是新租约期限的起点；网络失败时继续重试，本地期限过后停止进程。
func (r *runState) renewLoop(ctx context.Context, stop <-chan struct{}) {
	// 续租周期取自 claim 返回的租约，执行期间不变。
	interval := r.renewEvery
	if interval <= 0 {
		interval = 20 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	client := r.supervisor.config.Client
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		// 续租必须在 Run 被取消后继续，直到结果上传完成；因此使用独立上下文。
		renewContext := context.WithoutCancel(ctx)
		challenge := randomToken()
		sentAt := time.Now()
		lease, err := client.Renew(renewContext, r.offer.OrganizationID, r.offer.RunID, r.offer.Generation, challenge)
		if err == nil && lease.Challenge != challenge {
			err = errors.New("supervisor: lease challenge mismatch")
		}
		if err != nil {
			if permanent(err) {
				r.logger.Warn("supervisor_lease_rejected", "code", errorCode(err))
				r.cancel(errLeaseLost)
				continue
			}
			r.mu.Lock()
			expired := time.Now().After(r.deadline)
			r.mu.Unlock()
			if expired {
				r.logger.Warn("supervisor_lease_expired_locally", "error", err.Error())
				r.cancel(errLeaseLost)
			}
			continue
		}
		r.mu.Lock()
		r.deadline = sentAt.Add(time.Duration(lease.LeaseTTLMillis) * time.Millisecond)
		r.mu.Unlock()
		if lease.CancelRequested {
			r.cancel(errCancelRequested)
		}
	}
}

// prepareAndRun 返回本地日志终态与说明。
func (r *runState) prepareAndRun(ctx context.Context) (string, string) {
	config := r.supervisor.config
	client := config.Client
	offer := r.offer
	local := context.WithoutCancel(ctx)
	_ = config.Journal.Mark(local, offer.RunID, offer.Generation, JournalPreparing, "")
	r.prepBegin()

	runContext, err := client.Context(ctx, offer.OrganizationID, offer.RunID, offer.Generation)
	if err != nil {
		return r.prepareFailed(ctx, "PREPARE_FAILED", "run context unavailable: "+errorCode(err))
	}
	adapter := r.supervisor.adapterFor(runContext.Runtime)
	if adapter == nil {
		r.prepSubstep(prepRuntime, "")
		return r.prepareFailed(ctx, "RUNTIME_UNAVAILABLE", "runtime "+runContext.Runtime+" is not available on this computer")
	}
	// 要求的权限档位该适配器不支持时，在准备阶段直接失败，不执行，更不会退回更宽的默认行为。
	if err := runtime.RequireApprovalMode(adapter.Name(), adapterApprovalModes(adapter), runContext.ApprovalMode); err != nil {
		r.prepSubstep(prepRuntime, "")
		return r.prepareFailed(ctx, "RUNTIME_UNAVAILABLE", "runtime "+runContext.Runtime+" does not support approval mode "+runContext.ApprovalMode+" on this computer")
	}
	// 协作模式同理，中心派发前已按心跳核对，这里再校验一次；不支持就失败，绝不忽略该模式照常执行。
	if err := runtime.RequireCollaborationMode(adapter.Name(), adapterCollaborationModes(adapter), runContext.CollaborationMode); err != nil {
		r.prepSubstep(prepRuntime, "")
		return r.prepareFailed(ctx, "RUNTIME_UNAVAILABLE", "runtime "+runContext.Runtime+" does not support collaboration mode "+
			runtimepolicy.NormalizeCollaborationMode(runContext.CollaborationMode)+" on this computer")
	}
	if err := runtime.RequireReasoningEffort(adapter.Name(), adapterReasoningEfforts(adapter), runContext.ReasoningEffort); err != nil {
		r.prepSubstep(prepRuntime, "")
		return r.prepareFailed(ctx, "RUNTIME_UNAVAILABLE", "runtime "+runContext.Runtime+" does not support reasoning depth "+
			runtimepolicy.NormalizeReasoningEffort(runContext.ReasoningEffort)+" on this computer")
	}
	settings := buildRunSettings(runContext)
	mcp, err := prepareMCP(runContext, config.DeviceID, mcpconfig.UserLocation())
	if err != nil {
		return r.prepareFailed(ctx, "PREPARE_FAILED", errAgentMCP.Error())
	}
	defer mcp.close()
	for _, repo := range runContext.Repositories {
		if repo.Authorization != nil {
			settings.secrets = append(settings.secrets, repo.Authorization.Token)
		}
	}
	r.redact = newRedactor(append(settings.secrets, mcp.secrets...))
	for _, warning := range settings.warnings {
		r.events.add(warningEvent(warning))
	}
	// 启动 Runtime 前下载装配的技能；任何一个失败都不静默跳过（日志不含内容）。
	skills, err := r.downloadSkills(ctx, runContext)
	if err != nil {
		if context.Cause(ctx) != nil {
			return r.prepareFailed(ctx, "", "")
		}
		r.logger.Warn("supervisor_skill_download_failed", "error", err.Error())
		return r.prepareFailed(ctx, "PREPARE_FAILED", "技能下载失败")
	}
	workspace, err := r.prepareConversationWorkspace(ctx, runContext)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return r.prepareFailed(ctx, "", "")
		}
		if errors.Is(err, errDirectBusy) {
			return r.prepareFailed(ctx, "WORKTREE_PREP_FAILED", "the selected directory is in use by another run on this computer")
		}
		if errors.Is(err, workdir.ErrNotGitRepository) || errors.Is(err, workdir.ErrRepositoryUnavailable) {
			// 只回报逻辑根名，不把本机绝对路径发给中心。
			return r.prepareFailed(ctx, "REPO_UNAVAILABLE", "the requested directory or local Git branch is unavailable on this computer")
		}
		return r.prepareFailed(ctx, "WORKTREE_PREP_FAILED", "working directory could not be prepared")
	}
	if err := config.Journal.recordWorkDirectory(local, config.DeviceID, runContext, workspace.Path); err != nil {
		r.logger.Warn("supervisor_directory_fact_unavailable")
	}
	if workspace.IsGit && workspace.Owned && strings.HasPrefix(filepath.Base(workspace.Path), workdir.ManagedWorktreePrefix) {
		// 登记 worktree 与使用它的 Run，供存储清理判断；失败不影响执行（只是不会被自动清理）。
		if err := config.Journal.RecordWorkspace(local, workspace.Path, workspace.RepositoryRoot, offer.RunID, offer.Generation); err != nil {
			r.logger.Warn("supervisor_workspace_record_failed", "error", err.Error())
		}
		kind := "conversation"
		if runContext.AutomationOrigin {
			kind = "automation"
		} else if runContext.TaskID != "" {
			kind = "task"
		}
		_ = config.Journal.SetWorkspaceKind(local, workspace.Path, kind)
		defer func() { _ = config.Journal.TouchWorkspace(local, workspace.Path) }()
	}
	defer func() { _ = workspace.Release() }()
	if cause := context.Cause(ctx); cause != nil {
		return r.prepareFailed(ctx, "", "")
	}
	// 消息附件下载到 daemon 自己的每个 Run 临时目录（工作目录之外，Run 结束后删除），绝对路径写在 prompt 开头；失败不启动 Runtime。
	attachmentNote, cleanupAttachments, err := r.fetchAttachments(ctx, runContext)
	defer cleanupAttachments()
	if err != nil {
		if context.Cause(ctx) != nil {
			return r.prepareFailed(ctx, "", "")
		}
		r.logger.Warn("supervisor_attachment_download_failed", "error", err.Error())
		return r.prepareFailed(ctx, "PREPARE_FAILED", "附件下载失败")
	}
	runContext.Prompt = attachmentNote + runContext.Prompt
	r.prepSubstep(prepRuntime, adapter.Name())
	if _, err := client.Start(ctx, offer.OrganizationID, offer.RunID, offer.Generation); err != nil {
		if errorCode(err) == "CANCEL_REQUESTED" {
			return r.prepareFailed(ctx, "CANCELED", "canceled before start")
		}
		return r.prepareFailed(ctx, "PREPARE_FAILED", "start permission denied: "+errorCode(err))
	}
	// 已取得启动许可：此后的失败只能通过结果上报，不能再走 prepare-failed。
	_ = config.Journal.Mark(local, offer.RunID, offer.Generation, JournalRunning, "")

	timeout := time.Duration(runContext.RunTimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > runtime.MaxRunTimeout {
		timeout = runtime.DefaultRunTimeout
	}
	// 支持工具的 Runtime 一律声明发布工具（与任务无关），并为本次 Run 新建产物目录；
	// 不支持工具的 Runtime（pi、测试替身）不声明、提示词也不提，行为与之前一致。
	tools, toolCall := taskTools(runContext), r.taskToolCall(runContext)
	var artifactDir, artifactSection string
	if supportsTools(adapter) {
		dir, cleanupArtifacts := r.prepareArtifactDir()
		defer cleanupArtifacts()
		if dir != "" {
			artifactDir, artifactSection = dir, buildArtifactSection(dir)
			r.artifacts = newArtifactPublisher([]string{workspace.Path, dir}, dir, r.uploadArtifact)
			tools = append(tools, publishTool())
			toolCall = r.combinedToolCall(toolCall)
		}
	}
	if runContext.Runtime == "codex" {
		args, _ := sanitizeCustomArgs(r.supervisor.DeviceSettings().Settings.CodexArgs)
		settings.args = append(args, settings.args...)
	}
	p := r.supervisor.DeviceSettings().Settings
	var scope string
	if runContext.ActorID != "" && runContext.ConversationID != "" && runContext.AgentID != "" && runContext.OrganizationID != "" {
		scope = strings.Join([]string{runContext.OrganizationID, runContext.SpaceID, runContext.AgentID, runContext.ConversationID, runContext.TaskID, runContext.ActorID, fmt.Sprint(runContext.ConfigVersion)}, "/")
	}
	execution, err := adapter.Start(ctx, runtime.RunRequest{
		ProcessScope: scope, SessionIdleTTL: time.Duration(p.SessionIdleMinutes) * time.Minute, SessionMaxAge: time.Duration(p.SessionMaxMinutes) * time.Minute,
		Prompt: buildPrompt(runContext, artifactSection), Instructions: runContext.Instructions, Model: runContext.Model, ApprovalMode: runContext.ApprovalMode,
		ReasoningEffort:   runContext.ReasoningEffort,
		CollaborationMode: runContext.CollaborationMode,
		WorkingDirectory:  workspace.Path, Timeout: timeout,
		Environment: settings.environment, EnvironmentAllowlist: settings.allowlist, Args: settings.args, Skills: skills,
		Tools: tools, ToolCall: toolCall, ArtifactDirectory: artifactDir, MCPServers: mcp.servers, MCPSecrets: mcp.secrets,
	})
	if err != nil {
		r.logger.Warn("supervisor_runtime_start_failed", "error", r.redact.text(err.Error()))
		r.prepEnd(runtime.StepFailed)
		r.uploadResult(ctx, ResultInput{Status: "FAILED", Summary: "The runtime could not be started on this computer."})
		return JournalFinished, "runtime start failed"
	}
	r.prepEnd(runtime.StepDone)

	events := make(chan struct{})
	go func() {
		defer close(events)
		for event := range execution.Events() {
			r.events.add(event)
		}
	}()
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stopContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
			defer cancel()
			if err := execution.Cancel(stopContext); err != nil {
				r.logger.Error("supervisor_runtime_stop_failed", "error", err.Error())
			}
		case <-stopped:
		}
	}()
	result, waitErr := execution.Wait(context.WithoutCancel(ctx))
	close(stopped)
	<-events
	if waitErr != nil {
		r.logger.Error("supervisor_runtime_wait_failed", "error", waitErr.Error())
		return JournalFinished, "runtime wait failed"
	}
	if errors.Is(context.Cause(ctx), errLeaseLost) {
		// 租约已丢失：中心会要求核对，此时上传结果只会被拒绝。
		r.logger.Warn("supervisor_run_stopped_lease_lost", "status", string(result.Status))
		return JournalFinished, "stopped after lease loss"
	}
	input := resultInput(result)
	input.Output, input.Summary = r.redact.text(input.Output), r.redact.text(input.Summary)
	if errors.Is(context.Cause(ctx), errShutdown) && input.Status == "CANCELED" {
		// Daemon 退出导致的停止不是用户取消，按失败上报。
		input.Status, input.Summary = "FAILED", "DAEMON_SHUTDOWN: the computer's daemon stopped while the run was in progress"
	}
	r.uploadResult(ctx, input)
	return JournalFinished, input.Status
}

func (r *runState) prepareDefaultWorkspace(ctx context.Context, runContext RunContext) (*workdir.PreparedWorkspace, error) {
	config := r.supervisor.config
	mode, source, err := resolveWorkspace(runContext, config.DirectoryRoots)
	if err != nil {
		r.prepSubstep(prepRepo, "")
		return nil, fmt.Errorf("%w: %v", workdir.ErrRepositoryUnavailable, err)
	}
	if mode == workdir.ModeSpecifiedGit {
		// 逻辑根名可以展示；本机绝对路径不上报。
		r.prepSubstep(prepRepo, runContext.DirectoryRoots[0])
	}
	key := workdir.BindingKey{
		OrganizationID: runContext.OrganizationID, SpaceID: runContext.SpaceID,
		AgentID: runContext.AgentID, ConversationID: runContext.ConversationID,
	}
	shared := runContext.SharedTaskCrewID != "" && runContext.TaskID != ""
	if shared {
		key.AgentID = "crew:" + runContext.SharedTaskCrewID
		key.ConversationID = "task:" + runContext.TaskID
	}
	if runContext.DirectoryMode == "WORKSPACE" && len(runContext.Repositories) > 0 {
		return config.Workdir.PrepareRepositories(ctx, key, runContext.Repositories, filepath.Join(filepath.Dir(config.Journal.Path()), "repository-cache"))
	}
	direct := mode == workdir.ModeSpecifiedGit && strings.EqualFold(runContext.WorkdirIsolation, IsolationDirect)
	var workspace *workdir.PreparedWorkspace
	if direct {
		workspace, err = r.prepareDirect(ctx, key, source)
	} else if shared {
		deadline := time.Now().Add(directWaitLimit)
		for {
			workspace, err = config.Workdir.Prepare(ctx, workdir.Request{Key: key, Mode: mode, SourcePath: source, Shared: true})
			if !workdir.IsPathConflict(err) || time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(directWaitPoll):
				continue
			}
			break
		}
	} else {
		workspace, err = config.Workdir.Prepare(ctx, workdir.Request{Key: key, Mode: mode, SourcePath: source})
	}
	return workspace, err
}

// directWaitLimit / directWaitPoll：DIRECT 模式同一仓库已有 Run 时排队等待的上限与轮询间隔
// （等待期间续租照常进行）。定义为变量以便测试缩短。
var (
	directWaitLimit = 5 * time.Minute
	directWaitPoll  = 500 * time.Millisecond
)

var errDirectBusy = errors.New("supervisor: direct repository is busy")

// prepareDirect 锁定仓库目录本身（DIRECT 隔离）：目录正被本机另一个 Run 使用时排队等待，
// 超过 directWaitLimit 返回 errDirectBusy；Run 被取消时立即返回。
func (r *runState) prepareDirect(ctx context.Context, key workdir.BindingKey, source string) (*workdir.PreparedWorkspace, error) {
	deadline := time.Now().Add(directWaitLimit)
	logged := false
	for {
		workspace, err := r.supervisor.config.Workdir.PrepareDirectRun(ctx, key, source)
		if err == nil || !workdir.IsPathConflict(err) {
			if err != nil && errors.Is(err, workdir.ErrRepositoryUnavailable) {
				return nil, err
			}
			return workspace, err
		}
		if !logged {
			r.logger.Info("supervisor_direct_repository_busy")
			logged = true
		}
		if time.Now().After(deadline) {
			return nil, errDirectBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(directWaitPoll):
		}
	}
}

// prepareFailed 在启动许可前报告失败；failureCode 为空表示依据取消原因决定。
func (r *runState) prepareFailed(ctx context.Context, failureCode, detail string) (string, string) {
	cause := context.Cause(ctx)
	if errors.Is(cause, errLeaseLost) || errors.Is(cause, errShutdown) {
		// 租约已失或 Daemon 退出：不再上报，交给中心的租约过期处理。
		r.prepEnd(runtime.StepInterrupted)
		return JournalAbandoned, cause.Error()
	}
	if errors.Is(cause, errCancelRequested) || failureCode == "" || failureCode == "CANCELED" {
		failureCode, detail = "CANCELED", "canceled before start"
		r.prepEnd(runtime.StepInterrupted)
	} else {
		r.prepEnd(runtime.StepFailed)
	}
	reportContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultRequestTimeout)
	defer cancel()
	if err := r.supervisor.config.Client.PrepareFailed(reportContext, r.offer.OrganizationID, r.offer.RunID,
		r.offer.Generation, failureCode, boundUTF8(detail, maxPrepareDetail)); err != nil {
		r.logger.Warn("supervisor_prepare_failed_report_failed", "error", err.Error())
	}
	return JournalAbandoned, failureCode
}

// resolveWorkspace 把 Agent 的目录模式映射为本地工作目录请求。
// NONE/WORKSPACE 使用管理根下按对话绑定的非仓库目录；EXPLICIT 目前只支持单个根，
// 根名必须在本机通过 --directory-root 映射到 Git 仓库。
func resolveWorkspace(runContext RunContext, roots map[string]string) (workdir.Mode, string, error) {
	switch runContext.DirectoryMode {
	case "NONE", "WORKSPACE", "":
		return workdir.ModeNoRepo, "", nil
	case "EXPLICIT":
		if len(runContext.DirectoryRoots) != 1 {
			return "", "", fmt.Errorf("directory mode EXPLICIT supports exactly one directory root on this daemon, got %d", len(runContext.DirectoryRoots))
		}
		name := runContext.DirectoryRoots[0]
		path, ok := roots[name]
		if !ok || path == "" {
			return "", "", fmt.Errorf("directory root %s is not configured on this computer", name)
		}
		return workdir.ModeSpecifiedGit, path, nil
	default:
		return "", "", fmt.Errorf("directory mode %s is not supported by this daemon", runContext.DirectoryMode)
	}
}

// buildPrompt 把有界历史与本次输入合成单个 prompt；超长时优先丢弃最早的历史。
// extra 是附加说明段（产物交付说明），与任务段一样排在最前、计入预算。
func buildPrompt(runContext RunContext, extra ...string) string {
	budget := runtime.MaxPromptBytes - len(runContext.Instructions) - 64
	// 任务段（有任务时）排在历史与本次输入之前，超长时优先丢弃历史。
	taskSection := strings.Join(extra, "") + buildSpaceToolsSection(runContext.SpaceTools) + buildTaskSection(runContext.Task)
	budget -= len(taskSection)
	request := runContext.Prompt
	if len(request) >= budget {
		return taskSection + boundUTF8(request, budget)
	}
	lines := make([]string, 0, len(runContext.History))
	for _, message := range runContext.History {
		lines = append(lines, "["+message.AuthorKind+"] "+message.Body)
	}
	for len(lines) > 0 {
		candidate := "Conversation so far:\n" + strings.Join(lines, "\n") + "\n\nCurrent request:\n" + request
		if len(candidate) <= budget {
			return taskSection + candidate
		}
		lines = lines[1:]
	}
	return taskSection + request
}

func resultInput(result runtime.Result) ResultInput {
	input := ResultInput{Output: boundUTF8(result.Output, maxResultOutput), Usage: result.Usage}
	switch result.Status {
	case runtime.StatusSucceeded:
		input.Status = "SUCCEEDED"
	case runtime.StatusCancelled:
		input.Status = "CANCELED"
	case runtime.StatusTimedOut:
		input.Status = "TIMED_OUT"
	default:
		input.Status = "FAILED"
	}
	if input.Status != "SUCCEEDED" {
		summary := result.Error
		if result.ErrorCode != "" {
			summary = strings.TrimSpace(result.ErrorCode + ": " + summary)
		}
		input.Summary = boundUTF8(summary, maxResultSummary)
	}
	return input
}

// uploadResult 以固定结果 ID 幂等重试，直到成功、被永久拒绝或超过时限。
func (r *runState) uploadResult(ctx context.Context, input ResultInput) {
	// 先送达全部过程事件，再上报结果（见 eventUploader.close）。
	r.events.close()
	input.ID = newUUID()
	input.Generation = r.offer.Generation
	input.Checks = json.RawMessage("[]")
	input.ArtifactIDs = r.artifacts.ids()
	uploadContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), resultDeadline)
	defer cancel()
	delay := 500 * time.Millisecond
	var err error
	for attempt := 0; attempt < resultAttempts; attempt++ {
		err = r.supervisor.config.Client.AppendResult(uploadContext, r.offer.OrganizationID, r.offer.RunID, input)
		if err != nil && input.Usage != nil && errorCode(err) == "INVALID_REQUEST" {
			// 兼容尚未支持 usage 的中心（严格解码会拒绝未知字段）：去掉 usage 立即重试。
			r.logger.Warn("supervisor_result_usage_rejected")
			input.Usage = nil
			attempt--
			continue
		}
		if err != nil && len(input.ArtifactIDs) > 0 && errorCode(err) == "INVALID_REQUEST" {
			// 产物已上传，但中心拒绝了 artifact_ids（例如某个 ID 无效）：去掉后立即重试，
			// 保证结果一定送达；这些产物不会随结果关联，按中心的保留策略不会被自动清理。
			r.logger.Warn("supervisor_result_artifacts_rejected", "count", len(input.ArtifactIDs))
			input.ArtifactIDs = nil
			attempt--
			continue
		}
		if err == nil || permanent(err) {
			break
		}
		select {
		case <-uploadContext.Done():
			attempt = resultAttempts
		case <-time.After(delay):
			delay *= 2
		}
	}
	if err != nil {
		r.logger.Error("supervisor_result_upload_failed", "status", input.Status, "error", err.Error())
	}
}

func boundUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func randomToken() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func newUUID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// approvalModeAdapter 由支持权限档位的适配器实现；没有实现的适配器只支持 FOLLOW_RUNTIME。
type approvalModeAdapter interface {
	ApprovalModes() []string
}

func adapterApprovalModes(adapter runtime.Adapter) []string {
	if typed, ok := adapter.(approvalModeAdapter); ok {
		return typed.ApprovalModes()
	}
	return nil
}

// collaborationModeAdapter 由支持协作模式的适配器实现；没有实现的适配器只支持 FOLLOW_RUNTIME。
type collaborationModeAdapter interface {
	CollaborationModes() []string
}

type reasoningEffortAdapter interface {
	ReasoningEfforts() []string
}

func adapterReasoningEfforts(adapter runtime.Adapter) []string {
	if typed, ok := adapter.(reasoningEffortAdapter); ok {
		return typed.ReasoningEfforts()
	}
	return nil
}

func adapterCollaborationModes(adapter runtime.Adapter) []string {
	if typed, ok := adapter.(collaborationModeAdapter); ok {
		return typed.CollaborationModes()
	}
	return nil
}
