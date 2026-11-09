// Package cliagent 把本机安装的 Claude Code、OpenCode、pi 命令行适配为 runtime 契约。
// 三者都是「一次 Run 启动一个进程、stdout 输出 JSONL 事件」的模式：
//   - Claude Code：`claude -p --output-format stream-json --verbose`（提示经 stdin）
//   - OpenCode：`opencode run --format json`（提示经参数）
//   - pi：`pi --mode json`（提示经 stdin）
//
// 协议出处见各适配器文件头。事件被转换为与 Codex 相同的执行步骤（schema v1）。
package cliagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
	"openfox/internal/runtimepolicy"
)

// Model 与 Codex 的模型描述同构，直接写入心跳 capabilities.runtimes.{provider}.models。
type Model = codex.Model

const (
	NameClaudeCode = "claude-code"
	NameOpenCode   = "opencode"
	NamePi         = "pi"
)

// Names 是本包支持的 Runtime 名称。
var Names = []string{NameClaudeCode, NameOpenCode, NamePi}

// maxPromptViaArgs 是经命令行参数传递提示的上限（Linux 单个参数上限 128 KiB，留余量）。
const maxPromptViaArgs = 96 * 1024

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+[0-9A-Za-z.+_-]*`)

// invocation 是协议适配器为一次 Run 生成的进程调用描述。
type invocation struct {
	args  []string
	stdin []byte
	env   map[string]string
}

// protocol 描述一种命令行的差异部分。
type protocol struct {
	name        string
	display     string
	executables []string
	envPath     string   // 显式指定路径的环境变量
	wellKnown   []string // 相对 HOME 的常见安装目录
	envPrefixes []string // 从 Daemon 环境透传的变量前缀（凭据与代理配置）
	envSuffixes []string

	versionArgs   []string
	login         func(ctx context.Context, a *Adapter, path string, env map[string]string) runtime.LoginStatus
	listModels    func(ctx context.Context, a *Adapter, path string, env map[string]string) ([]Model, error)
	defaultModels []Model
	build         func(a *Adapter, req runtime.RunRequest, bridge *bridgeInfo, stateDir string, sess *session) (invocation, error)
	// parse 解析 stdout 的一行；done=true 表示已有终态。finalize 在进程退出且无终态时调用（可为空）。
	parse         func(s *session, line []byte) (events []runtime.Event, done bool)
	finalize      func(s *session) []runtime.Event
	stderrSummary func(string) string
	// approvalModes 是适配器支持的非默认权限档位；FOLLOW_RUNTIME 永远支持。
	// 为空表示只支持 FOLLOW_RUNTIME，收到其他档位的 Run 在 Start 里直接失败，不执行。
	approvalModes []string
	// collaborationModes 是适配器真正实现了的非 FOLLOW_RUNTIME 协作模式（DEFAULT / PLAN）。
	// 为空表示只支持 FOLLOW_RUNTIME，收到其他模式的 Run 在 Start 里直接失败，不执行，也不会忽略该模式。
	collaborationModes []string
	// supportsTools 表示能把 OpenFox 任务工具经 HTTP MCP 暴露给该 Runtime。
	supportsTools bool
}

// Adapter 是通用的命令行适配器；用 NewClaudeCode / NewOpenCode / NewPi 构造。
type Adapter struct {
	// Executable 为空时自动发现（环境变量、PATH、常见安装目录）；非空时只使用该值。
	Executable   string
	Environment  map[string]string
	ProbeTimeout time.Duration
	// StateRoot 是每次 Run 的临时配置文件的父目录（0700）；为空时使用系统临时目录。
	StateRoot string

	proto protocol
	mu    sync.Mutex
	// resolved 缓存上次发现成功的路径。
	resolved string
}

// ApprovalModes 返回适配器真正支持的权限档位（一定含 FOLLOW_RUNTIME），写进心跳 capabilities。
func (a *Adapter) ApprovalModes() []string {
	return runtimepolicy.NormalizeApprovalModes(a.proto.approvalModes)
}

// CollaborationModes 返回适配器真正支持的协作模式（一定含 FOLLOW_RUNTIME），写进心跳 capabilities。
func (a *Adapter) CollaborationModes() []string {
	return runtimepolicy.NormalizeCollaborationModes(a.proto.collaborationModes)
}

// SupportsTools 表示能经 HTTP MCP 桥把 OpenFox 工具暴露给该 Runtime；发布工具只在这类 Runtime 上声明。
func (a *Adapter) SupportsTools() bool { return a.proto.supportsTools }

func NewClaudeCode(executable string) *Adapter {
	return &Adapter{Executable: executable, ProbeTimeout: 5 * time.Second, proto: claudeProtocol()}
}

func NewOpenCode(executable string) *Adapter {
	return &Adapter{Executable: executable, ProbeTimeout: 5 * time.Second, proto: openCodeProtocol()}
}

func NewPi(executable string) *Adapter {
	return &Adapter{Executable: executable, ProbeTimeout: 5 * time.Second, proto: piProtocol()}
}

// New 按名称构造适配器；名称不认识时返回 nil。
func New(name, executable string) *Adapter {
	switch name {
	case NameClaudeCode:
		return NewClaudeCode(executable)
	case NameOpenCode:
		return NewOpenCode(executable)
	case NamePi:
		return NewPi(executable)
	}
	return nil
}

func (a *Adapter) Name() string { return a.proto.name }

// DisplayName 是用户可见的 Runtime 名称。
func (a *Adapter) DisplayName() string { return a.proto.display }

func (a *Adapter) Detect(ctx context.Context) (runtime.Capabilities, error) {
	if a == nil {
		return runtime.Capabilities{}, runtime.ErrUnavailable
	}
	ctx = ctxOrBackground(ctx)
	path, output, err := a.locate(ctx)
	if err != nil {
		if path != "" {
			return runtime.Capabilities{Runtime: a.proto.name, Executable: path, Login: runtime.LoginUnknown, Reason: "version probe failed"}, err
		}
		return runtime.Capabilities{}, err
	}
	version := versionPattern.FindString(output)
	if version == "" {
		version = "unknown"
	}
	env := a.baseEnv(path)
	login := runtime.LoginUnknown
	if a.proto.login != nil {
		probeCtx, cancel := context.WithTimeout(ctx, a.probeTimeout())
		login = a.proto.login(probeCtx, a, path, env)
		cancel()
	}
	report := runtime.Capabilities{
		Runtime: a.proto.name, Version: version, Executable: path, Login: login,
		// 登录状态判断不了（unknown）时不拦截：能否运行取决于命令可执行，失败原因由 Run 结果说明。
		Runnable:           login != runtime.LoginLoggedOut,
		SupportsJSONEvents: true, SupportsCancel: true, SupportsResume: true,
		DirectoryModes: []string{"NONE", "WORKSPACE", "EXPLICIT"},
	}
	if !report.Runnable {
		report.Reason = a.proto.display + " login is not available"
	}
	return report, nil
}

// ListModels 返回可选模型：第一项总是 default（不指定模型）。命令取不到列表时返回内置默认并带错误。
func (a *Adapter) ListModels(ctx context.Context) ([]Model, error) {
	if a == nil {
		return nil, runtime.ErrUnavailable
	}
	ctx = ctxOrBackground(ctx)
	defaults := append([]Model(nil), a.proto.defaultModels...)
	if a.proto.listModels == nil {
		return defaults, nil
	}
	path, _, err := a.locate(ctx)
	if err != nil {
		return defaults, err
	}
	listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, err := a.proto.listModels(listCtx, a, path, a.baseEnv(path))
	if err != nil || len(models) == 0 {
		if err == nil {
			err = errors.New("model list is empty")
		}
		return defaults, err
	}
	result := make([]Model, 0, len(models)+1)
	result = append(result, defaults...)
	seen := map[string]bool{}
	for _, model := range result {
		seen[model.ID] = true
	}
	for _, model := range models {
		if len(result) >= 100 {
			break
		}
		if model.ID == "" || len(model.ID) > 128 || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if model.DisplayName == "" {
			model.DisplayName = model.ID
		}
		result = append(result, model)
	}
	return result, nil
}

func (a *Adapter) Start(ctx context.Context, request runtime.RunRequest) (runtime.Execution, error) {
	if a == nil {
		return nil, runtime.ErrUnavailable
	}
	request, err := request.Normalize()
	if err != nil {
		return nil, err
	}
	// 不支持的权限档位在任何探测、进程启动之前失败：绝不退回更宽的默认行为。
	if err := runtime.RequireApprovalMode(a.proto.display, a.proto.approvalModes, request.ApprovalMode); err != nil {
		return nil, err
	}
	// 协作模式同理：没有真正实现的模式直接失败，绝不忽略后按普通模式执行。
	if err := runtime.RequireCollaborationMode(a.proto.display, a.proto.collaborationModes, request.CollaborationMode); err != nil {
		return nil, err
	}
	if err := runtime.RequireReasoningEffort(a.proto.display, nil, request.ReasoningEffort); err != nil {
		return nil, err
	}
	capabilities, err := a.Detect(ctx)
	if err != nil {
		return nil, err
	}
	if !capabilities.Runnable {
		return nil, fmt.Errorf("%w: %s", runtime.ErrUnavailable, capabilities.Reason)
	}
	if request.WorkingDirectory == "" {
		if request.WorkingDirectory, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("%w: working directory: %v", runtime.ErrInvalidRequest, err)
		}
	}
	if request.WorkingDirectory, err = filepath.Abs(request.WorkingDirectory); err != nil {
		return nil, fmt.Errorf("%w: working directory: %v", runtime.ErrInvalidRequest, err)
	}
	baseEnv := a.baseEnv(capabilities.Executable)
	var bridge *bridgeInfo
	var cleanups []func()
	cleanup := func() {
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanups[index]()
		}
	}
	if a.proto.supportsTools && len(request.Tools) > 0 && request.ToolCall != nil {
		bridge, err = startBridge(ctxOrBackground(ctx), request.Tools, request.ToolCall)
		if err != nil {
			return nil, fmt.Errorf("%w: task tool bridge: %v", runtime.ErrUnavailable, err)
		}
		cleanups = append(cleanups, bridge.close)
	}
	env := map[string]string{}
	for key, value := range baseEnv {
		env[key] = value
	}
	for key, value := range request.Environment {
		env[key] = value
	}
	secrets := make([]string, 0, len(request.Environment)+1)
	for _, value := range request.Environment {
		secrets = append(secrets, value)
	}
	if bridge != nil {
		secrets = append(secrets, bridge.token)
	}
	secrets = append(secrets, request.MCPSecrets...)
	redact := redactor(env, secrets)
	sess := newSession(a.proto, request.WorkingDirectory, redact)
	root, err := a.makeStateDir()
	if err != nil {
		cleanup()
		return nil, err
	}
	cleanups = append(cleanups, func() { _ = os.RemoveAll(root) })
	invocation, err := a.proto.build(a, request, bridge, root, sess)
	if err != nil {
		cleanup()
		return nil, err
	}
	for key, value := range invocation.env {
		env[key] = value
		secrets = append(secrets, value)
	}
	redact = redactor(env, secrets)
	sess.redact = redact
	execution, err := runtime.StartProcess(ctxOrBackground(ctx), runtime.ProcessSpec{
		Executable: capabilities.Executable, Args: invocation.args, Directory: request.WorkingDirectory,
		Environment: environmentSlice(env), Timeout: request.Timeout, MaxLineBytes: request.MaxLineBytes,
		MaxOutputBytes: request.MaxOutputBytes, MaxEvents: request.MaxEvents,
		RequireCompletion: true, Redact: redact, Session: sess,
		SummarizeStderr: a.proto.stderrSummary,
		StdinOnce:       invocation.stdin, CloseStdin: true,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	wrapped := &cleanupExecution{Execution: execution, done: make(chan struct{})}
	go func() {
		defer close(wrapped.done)
		// 进程结束（含被信号终止）后才清理：关闭任务工具桥并删除临时配置。
		_, _ = execution.Wait(context.Background())
		cleanup()
	}()
	return wrapped, nil
}

// cleanupExecution 保证 Wait 返回时临时资源已清理。
type cleanupExecution struct {
	runtime.Execution
	done chan struct{}
}

func (e *cleanupExecution) Wait(ctx context.Context) (runtime.Result, error) {
	ctx = ctxOrBackground(ctx)
	select {
	case <-e.done:
		return e.Execution.Wait(ctx)
	case <-ctx.Done():
		return runtime.Result{}, ctx.Err()
	}
}

func (a *Adapter) makeStateDir() (string, error) {
	parent := a.StateRoot
	if parent != "" {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", fmt.Errorf("%w: state directory: %v", runtime.ErrUnavailable, err)
		}
	}
	dir, err := os.MkdirTemp(parent, "openfox-"+a.proto.name+"-")
	if err != nil {
		return "", fmt.Errorf("%w: state directory: %v", runtime.ErrUnavailable, err)
	}
	return dir, nil
}

// locate 返回可执行文件路径及其 --version 输出。显式配置失败时直接报错。
func (a *Adapter) locate(ctx context.Context) (string, string, error) {
	probe := func(path string) (string, error) {
		probeCtx, cancel := context.WithTimeout(ctx, a.probeTimeout())
		defer cancel()
		return runProbe(probeCtx, path, a.baseEnv(path), a.proto.versionArgs...)
	}
	explicit := strings.TrimSpace(a.Executable)
	if explicit == "" {
		explicit = strings.TrimSpace(os.Getenv(a.proto.envPath))
	}
	if explicit != "" {
		path := explicit
		if !filepath.IsAbs(path) {
			resolved, err := exec.LookPath(path)
			if err != nil {
				return "", "", fmt.Errorf("%w: %s executable %q not found", runtime.ErrUnavailable, a.proto.display, explicit)
			}
			path = resolved
		}
		if !isExecutableFile(path) {
			return "", "", fmt.Errorf("%w: %s executable %q is not executable", runtime.ErrUnavailable, a.proto.display, explicit)
		}
		output, err := probe(path)
		if err != nil {
			return path, "", fmt.Errorf("%w: version probe: %s", runtime.ErrUnavailable, safeError(err))
		}
		return path, output, nil
	}
	a.mu.Lock()
	cached := a.resolved
	a.mu.Unlock()
	if cached != "" && isExecutableFile(cached) {
		if output, err := probe(cached); err == nil {
			return cached, output, nil
		}
	}
	for _, path := range a.candidates() {
		output, err := probe(path)
		if err != nil {
			continue
		}
		a.mu.Lock()
		a.resolved = path
		a.mu.Unlock()
		return path, output, nil
	}
	return "", "", fmt.Errorf("%w: %s executable not found", runtime.ErrUnavailable, a.proto.display)
}

// candidates 按顺序返回候选路径：PATH、HOME 下的常见安装目录、系统目录；去重。
func (a *Adapter) candidates() []string {
	var dirs []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(dir) {
			dirs = append(dirs, dir)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, rel := range a.proto.wellKnown {
			dirs = append(dirs, filepath.Join(home, rel))
		}
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	seen := map[string]bool{}
	var result []string
	for _, dir := range dirs {
		for _, name := range a.proto.executables {
			path := filepath.Join(dir, name)
			if !seen[path] && isExecutableFile(path) {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	return result
}

func (a *Adapter) probeTimeout() time.Duration {
	if a.ProbeTimeout <= 0 {
		return 5 * time.Second
	}
	if a.ProbeTimeout > 30*time.Second {
		return 30 * time.Second
	}
	return a.ProbeTimeout
}

// baseEnv 是子进程的基础环境：必要的系统变量、凭据与代理前缀的透传，以及适配器配置的覆盖。
func (a *Adapter) baseEnv(executable string) map[string]string {
	env := map[string]string{}
	for _, key := range []string{"HOME", "PATH", "LANG", "LC_ALL", "USER", "LOGNAME", "SHELL", "TMPDIR",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	if _, ok := env["HOME"]; !ok {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			env["HOME"] = home
		}
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || !validEnvName(key) {
			continue
		}
		if hasAnyPrefix(key, a.proto.envPrefixes) || hasAnySuffix(key, a.proto.envSuffixes) {
			env[key] = value
		}
	}
	for key, value := range a.Environment {
		env[key] = value
	}
	if executable != "" {
		dir := filepath.Dir(executable)
		// 可执行文件所在目录放到 PATH 最前，使 node 等同目录的依赖可被找到。
		if path := env["PATH"]; path == "" {
			env["PATH"] = dir
		} else if !containsDir(path, dir) {
			env["PATH"] = dir + string(os.PathListSeparator) + path
		}
	}
	return env
}

func containsDir(path, dir string) bool {
	for _, item := range filepath.SplitList(path) {
		if item == dir {
			return true
		}
	}
	return false
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func hasAnySuffix(value string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
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

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// runProbe 运行探测命令并返回合并的输出（上限 64 KiB）；非零退出返回错误，但输出仍然返回。
func runProbe(ctx context.Context, executable string, env map[string]string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = environmentSlice(env)
	cmd.Dir = os.TempDir()
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return output.String(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := 64*1024 - b.Len(); remaining > 0 {
		if len(p) > remaining {
			b.Buffer.Write(p[:remaining])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func environmentSlice(environment map[string]string) []string {
	result := make([]string, 0, len(environment))
	for key, value := range environment {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

// minRedactLength 与 Codex 适配器一致：更短的值替换会破坏正常文本。
const minRedactLength = 4

func redactor(env map[string]string, extra []string) func(string) string {
	var secrets []string
	for _, value := range extra {
		if len(value) >= minRedactLength {
			secrets = append(secrets, value)
		}
	}
	for key, value := range env {
		upper := strings.ToUpper(key)
		if len(value) >= minRedactLength && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "CREDENTIAL")) {
			secrets = append(secrets, value)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return func(text string) string {
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
		return text
	}
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "probe timeout"
	}
	text := strings.Join(strings.Fields(err.Error()), " ")
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
