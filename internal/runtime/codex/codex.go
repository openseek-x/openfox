// Package codex 把本机安装的 Codex CLI 适配为 runtime 契约。
// 执行走 `codex app-server` 的 JSON-RPC 协议：每轮显式下发审批与沙箱策略，
// 每次 Run 使用独立的 CODEX_HOME。协议按受支持版本矩阵对待，未知的结果不会被当作成功。
package codex

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

	"github.com/pelletier/go-toml/v2"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

const RuntimeName = "codex"

var versionPattern = regexp.MustCompile(`(?m)codex-cli\s+([^\s]+)`)

type Adapter struct {
	// Executable 为空时自动发现（PATH、登录 shell、常见安装目录、macOS ChatGPT/Codex App）；
	// 非空或设置了 OPENFOX_CODEX_PATH / CODEX_PATH 时只使用该值。
	Executable        string
	Environment       map[string]string
	SupportedVersions []string
	ProbeTimeout      time.Duration
	// HomeRoot 是每次 Run 独立 CODEX_HOME 的父目录（Daemon 设为其状态目录下的子目录）；
	// 为空时使用系统临时目录。
	HomeRoot string
	// ExportRoot stores only rollout JSONL files for up to 24 hours.
	ExportRoot string
	// DisableNetwork 为 true 时沙箱禁止联网；默认允许（workspace-write + 网络）。
	DisableNetwork bool

	// discovery 为 nil 时使用真实环境；测试可替换。
	discovery *discovery
	mu        sync.Mutex
	// resolved 缓存上次自动发现成功的路径，避免每次 Start 都启动登录 shell。
	resolved           string
	devicePath         string
	supportsSocketAuth bool
	pool               hostPool
}

func New(executable string) *Adapter {
	return &Adapter{Executable: executable, ProbeTimeout: 5 * time.Second}
}

func (a *Adapter) Name() string { return RuntimeName }

// ReasoningEfforts 返回 Codex 模型菜单支持的思考深度。具体模型不支持的档位由 Codex 自身拒绝，
// 中心只在设备已升级且明确上报能力后展示这些选项。
func (a *Adapter) ReasoningEfforts() []string {
	return runtimepolicy.NormalizeReasoningEfforts([]string{
		runtimepolicy.ReasoningLow,
		runtimepolicy.ReasoningMedium,
		runtimepolicy.ReasoningHigh,
		runtimepolicy.ReasoningExtraHigh,
		runtimepolicy.ReasoningMax,
		runtimepolicy.ReasoningUltra,
	})
}

func (a *Adapter) Detect(ctx context.Context) (runtime.Capabilities, error) {
	if a == nil {
		return runtime.Capabilities{}, runtime.ErrUnavailable
	}
	path, versionOutput, err := a.locate(ctxOrBackground(ctx))
	if err != nil {
		if path != "" {
			return runtime.Capabilities{Runtime: RuntimeName, Executable: path, Login: runtime.LoginUnknown, Reason: "version probe failed"}, err
		}
		return runtime.Capabilities{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), a.probeTimeout())
	defer cancel()

	baseEnv := withExecutableDir(a.baseEnvironment(), path)
	version := parseVersion(versionOutput)
	if version == "" {
		version = "unknown"
	}
	helpOutput, err := runProbe(probeCtx, path, baseEnv, "app-server", "--help")
	if err != nil || !strings.Contains(helpOutput, "app-server") {
		return runtime.Capabilities{Runtime: RuntimeName, Version: version, Executable: path, Login: runtime.LoginUnknown, Reason: "codex app-server is unavailable"}, runtime.ErrUnsupported
	}
	a.mu.Lock()
	a.supportsSocketAuth = strings.Contains(helpOutput, "--ws-token-sha256") && strings.Contains(helpOutput, "--ws-auth")
	a.mu.Unlock()
	loginOutput, loginErr := runProbe(probeCtx, path, baseEnv, "login", "status")
	login := runtime.LoginUnknown
	if loginErr == nil {
		lowerLoginOutput := strings.ToLower(loginOutput)
		if strings.Contains(lowerLoginOutput, "not logged") || strings.Contains(lowerLoginOutput, "logged out") {
			login = runtime.LoginLoggedOut
		} else if strings.Contains(lowerLoginOutput, "logged in") {
			login = runtime.LoginLoggedIn
		}
	}
	report := runtime.Capabilities{
		Runtime: RuntimeName, Version: version, Executable: path, Login: login,
		Runnable:           login == runtime.LoginLoggedIn,
		SupportsJSONEvents: true, SupportsCancel: true, SupportsResume: false,
		DirectoryModes: []string{"NONE", "WORKSPACE", "EXPLICIT"},
	}
	// 默认不按版本拦截，能探测到版本且已登录即可运行；
	// 配置了版本名单（--supported-version / OPENFOX_CODEX_SUPPORTED_VERSIONS）时才按名单限制。
	if len(a.SupportedVersions) > 0 && !contains(a.SupportedVersions, version) {
		report.Runnable = false
		report.Reason = "Codex version is not in the supported version matrix"
	}
	if !report.Runnable {
		if report.Reason != "" {
			// Preserve the more specific capability/version reason above.
		} else if loginErr != nil {
			report.Reason = "login status could not be verified"
		} else {
			report.Reason = "Codex login is not available"
		}
	}
	return report, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if strings.TrimSpace(candidate) == value {
			return true
		}
	}
	return false
}

// ApprovalModes 返回适配器支持的权限档位：四档都支持（沙箱 read-only / workspace-write / danger-full-access），
// 审批策略始终是 never，运行中收到审批请求仍然判失败。写进心跳 capabilities。
func (a *Adapter) ApprovalModes() []string {
	return runtimepolicy.ApprovalModes
}

// CollaborationModes 返回适配器支持的协作模式：只有 FOLLOW_RUNTIME。
// 现有调用方式（app-server 的 thread/start、turn/start）里，仓库代码、协议注释与已有记录都没有能确认的
// 计划或协作模式参数，因此 DEFAULT 与 PLAN 都按不支持处理，收到时在 Start 里失败，不会忽略后照常执行。
func (a *Adapter) CollaborationModes() []string {
	return runtimepolicy.NormalizeCollaborationModes(nil)
}

// SupportsTools 表示能经 dynamicTools 把 OpenFox 工具（任务工具、发布工具）声明给模型。
// 当前 Codex 拒绝 dynamicTools 时会去掉工具降级继续运行（见 appServerSession.handleResponse）。
func (a *Adapter) SupportsTools() bool { return true }

func (a *Adapter) Start(ctx context.Context, request runtime.RunRequest) (runtime.Execution, error) {
	if a == nil {
		return nil, runtime.ErrUnavailable
	}
	request, err := request.Normalize()
	if err != nil {
		return nil, err
	}
	if err := runtime.RequireCollaborationMode("Codex", nil, request.CollaborationMode); err != nil {
		return nil, err
	}
	if err := runtime.RequireReasoningEffort("Codex", a.ReasoningEfforts(), request.ReasoningEffort); err != nil {
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
		request.WorkingDirectory, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("%w: working directory: %v", runtime.ErrInvalidRequest, err)
		}
	}
	// app-server 要求绝对路径的 cwd 与可写根。
	if request.WorkingDirectory, err = filepath.Abs(request.WorkingDirectory); err != nil {
		return nil, fmt.Errorf("%w: working directory: %v", runtime.ErrInvalidRequest, err)
	}
	args, err := configOverrideArgs(request.Args)
	if err != nil {
		return nil, err
	}
	if effort := runtimepolicy.NormalizeReasoningEffort(request.ReasoningEffort); effort != runtimepolicy.ReasoningDefault {
		value := strings.ToLower(effort)
		if effort == runtimepolicy.ReasoningExtraHigh {
			value = "xhigh"
		}
		args = append(args, "-c", "model_reasoning_effort="+value)
	}
	prepared, err := a.prepareWithMode(capabilities.Executable, request.Environment, request.EnvironmentAllowlist, request.Skills, request.ApprovalMode)
	if err != nil {
		return nil, err
	}
	if len(request.MCPServers) > 0 {
		encoded, e := toml.Marshal(map[string]any{"mcp_servers": request.MCPServers})
		if e == nil {
			e = os.WriteFile(filepath.Join(prepared.home.dir, "config.toml"), append([]byte(isolatedConfigFor(request.ApprovalMode)), encoded...), 0600)
		}
		if e != nil {
			prepared.home.remove()
			return nil, fmt.Errorf("%w: could not prepare agent MCP configuration", runtime.ErrInvalidRequest)
		}
	}
	baseRedact := prepared.redact
	mcpRedact := redactEnvironment(nil, request.MCPSecrets...)
	prepared.redact = func(s string) string { return mcpRedact(baseRedact(s)) }
	spec := runtime.ProcessSpec{
		Executable: prepared.path, Args: append(args, "app-server"), Directory: request.WorkingDirectory, Environment: prepared.env,
		Timeout: request.Timeout, MaxLineBytes: request.MaxLineBytes,
		MaxOutputBytes: request.MaxOutputBytes, MaxEvents: request.MaxEvents,
		RequireCompletion: true, Redact: prepared.redact,
		Session: &appServerSession{
			prompt: request.Prompt, instructions: request.Instructions, model: sessionModel(request.Model),
			cwd: request.WorkingDirectory, networkAccess: !a.DisableNetwork, approvalMode: request.ApprovalMode, redact: prepared.redact,
			tools: request.Tools, toolCall: request.ToolCall, toolCtx: ctx, artifactDir: request.ArtifactDirectory,
		},
	}
	a.mu.Lock()
	supported := a.supportsSocketAuth
	a.mu.Unlock()
	// Servers with a per-run credential bridge must exit with that bridge.
	if supported && request.ProcessScope != "" && request.SessionIdleTTL > 0 && request.SessionMaxAge > 0 && len(request.MCPServers) == 0 {
		if execution, used, err := a.startPooled(ctx, request, spec, prepared); used {
			return execution, err
		}
	}
	execution, err := runtime.StartProcess(ctx, spec)
	if err != nil {
		prepared.home.remove()
		return nil, err
	}
	home := prepared.home
	wrapped := &homeExecution{Execution: execution, done: make(chan struct{})}
	go func() {
		defer close(wrapped.done)
		// 进程结束（含被信号终止）后才清理，避免删除仍在使用的目录；不依赖调用方是否 Wait。
		result, _ := execution.Wait(context.Background())
		_, _ = home.finish(result.SessionID)
	}()
	return wrapped, nil
}

// sessionModel 把 Agent 配置中的模型名转为 app-server 参数：空或 "default"（不区分大小写）表示
// 不指定模型，由 Codex 使用其自身默认模型；兼容在支持模型列表之前创建的配置。
func sessionModel(model string) string {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "default") {
		return ""
	}
	return model
}

type preparedProcess struct {
	path   string
	env    []string
	redact func(string) string
	home   *codexHome
}

// prepare 组装 app-server 进程环境：基础白名单 + 请求环境，并创建本次独立的 CODEX_HOME。
// 返回成功后，调用方负责在进程结束后调用 home.finish()（或启动失败时 home.remove()）。
func (a *Adapter) prepare(path string, requestEnv map[string]string, requestAllowlist []string) (preparedProcess, error) {
	return a.prepareWithSkills(path, requestEnv, requestAllowlist, nil)
}

// prepareWithSkills 在 prepare 的基础上，把 skills 写入独立 CODEX_HOME 的 skills/ 目录。
func (a *Adapter) prepareWithSkills(path string, requestEnv map[string]string, requestAllowlist []string, skills []runtime.Skill) (preparedProcess, error) {
	return a.prepareWithMode(path, requestEnv, requestAllowlist, skills, runtimepolicy.FollowRuntime)
}

// prepareWithMode 在 prepareWithSkills 的基础上，让独立 CODEX_HOME 的 config.toml 兜底沙箱与权限档位一致
// （每轮请求仍显式下发策略；兜底值绝不比请求的档位更宽）。
func (a *Adapter) prepareWithMode(path string, requestEnv map[string]string, requestAllowlist []string, skills []runtime.Skill, approvalMode string) (preparedProcess, error) {
	envMap, allowlist := withExecutableDir(a.baseEnvironment(), path), a.baseAllowlist()
	for _, key := range requestAllowlist {
		allowlist[key] = struct{}{}
	}
	for key, value := range requestEnv {
		if _, ok := allowlist[key]; !ok {
			return preparedProcess{}, fmt.Errorf("%w: environment key %q is not allowed", runtime.ErrInvalidRequest, key)
		}
		if key == "CODEX_HOME" {
			return preparedProcess{}, fmt.Errorf("%w: environment key %q is managed by the adapter", runtime.ErrInvalidRequest, key)
		}
		envMap[key] = value
	}
	source, err := sourceCodexHome(envMap)
	if err != nil {
		return preparedProcess{}, fmt.Errorf("%w: %v", runtime.ErrUnavailable, err)
	}
	home, err := prepareCodexHomeWithConfig(a.HomeRoot, source, isolatedConfigFor(approvalMode))
	if err != nil {
		return preparedProcess{}, err
	}
	home.exportRoot = a.ExportRoot
	RemoveStaleSessionExports(a.ExportRoot, time.Now())
	if err := writeSkills(home.dir, skills); err != nil {
		home.remove()
		if errors.Is(err, runtime.ErrInvalidRequest) {
			return preparedProcess{}, err
		}
		return preparedProcess{}, fmt.Errorf("%w: write skills: %v", runtime.ErrUnavailable, safeProbeError(err))
	}
	envMap["CODEX_HOME"] = home.dir
	// redact 在覆盖 CODEX_HOME 之后生成，覆盖全部最终环境值；请求环境（Agent 环境变量）的值一律视为机密。
	secrets := make([]string, 0, len(requestEnv))
	for _, value := range requestEnv {
		secrets = append(secrets, value)
	}
	return preparedProcess{path: path, env: environmentSlice(envMap), redact: redactEnvironment(envMap, secrets...), home: home}, nil
}

// configOverrideArgs 只接受成对的 `-c key=value`（`--config` 规范化为 `-c`），作为 codex 根参数放在
// 子命令 app-server 之前。白名单筛选由调用方完成；这里拒绝任何其他形式，防止参数注入。
func configOverrideArgs(args []string) ([]string, error) {
	if len(args)%2 != 0 {
		return nil, fmt.Errorf("%w: runtime arguments must be -c key=value pairs", runtime.ErrInvalidRequest)
	}
	result := make([]string, 0, len(args))
	for index := 0; index < len(args); index += 2 {
		flag, pair := args[index], args[index+1]
		key, _, ok := strings.Cut(pair, "=")
		if (flag != "-c" && flag != "--config") || !ok || key == "" || strings.HasPrefix(pair, "-") {
			return nil, fmt.Errorf("%w: runtime arguments must be -c key=value pairs", runtime.ErrInvalidRequest)
		}
		result = append(result, "-c", pair)
	}
	return result, nil
}

// homeExecution 保证 Wait 返回时独立 CODEX_HOME 已完成凭据写回与删除。
type homeExecution struct {
	runtime.Execution
	done chan struct{}
}

func (e *homeExecution) Wait(ctx context.Context) (runtime.Result, error) {
	ctx = ctxOrBackground(ctx)
	select {
	case <-e.done:
		return e.Execution.Wait(ctx)
	case <-ctx.Done():
		return runtime.Result{}, ctx.Err()
	}
}

// locate 返回可用的 Codex 路径及其 --version 输出。显式配置失败时直接报错；
// 自动发现时跳过 --version 失败的候选，继续尝试下一个。
// 返回非空路径 + 错误表示找到了文件但版本探测失败。
func (a *Adapter) locate(ctx context.Context) (string, string, error) {
	d := defaultDiscovery()
	if a.discovery != nil {
		d = *a.discovery
	}
	probe := func(path string) (string, error) {
		probeCtx, cancel := context.WithTimeout(ctx, a.probeTimeout())
		defer cancel()
		return runProbe(probeCtx, path, withExecutableDir(a.baseEnvironment(), path), "--version")
	}
	a.mu.Lock()
	configured := a.devicePath
	a.mu.Unlock()
	if configured == "" {
		configured = a.Executable
	}
	if item, ok := d.explicit(configured); ok {
		path, err := resolveConfigured(item.path)
		if err != nil {
			return "", "", err
		}
		output, err := probe(path)
		if err != nil {
			return path, "", fmt.Errorf("%w: version probe: %v", runtime.ErrUnavailable, safeProbeError(err))
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
	discoveryCtx, cancel := context.WithTimeout(ctx, a.probeTimeout()+loginShellTimeout)
	defer cancel()
	var found, output string
	d.automatic(discoveryCtx, func(item candidate) bool {
		result, err := probe(item.path)
		if err != nil {
			// 版本探测失败的候选不采用。
			return false
		}
		found, output = item.path, result
		return true
	})
	if found == "" {
		return "", "", fmt.Errorf("%w: Codex executable not found", runtime.ErrUnavailable)
	}
	a.mu.Lock()
	a.resolved = found
	a.mu.Unlock()
	return found, output, nil
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

func (a *Adapter) baseEnvironment() map[string]string {
	result := make(map[string]string)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		result["HOME"] = home
	}
	if path := os.Getenv("PATH"); path != "" {
		result["PATH"] = path
	}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		result["CODEX_HOME"] = codexHome
	}
	if lang := os.Getenv("LANG"); lang != "" {
		result["LANG"] = lang
	}
	for key, value := range a.Environment {
		result[key] = value
	}
	return result
}

func (a *Adapter) baseAllowlist() map[string]struct{} {
	allowed := make(map[string]struct{})
	for key := range a.baseEnvironment() {
		allowed[key] = struct{}{}
	}
	return allowed
}

func runProbe(ctx context.Context, executable string, environment map[string]string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = environmentSlice(environment)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", err
	}
	if output.Len() > 64*1024 {
		return "", runtime.ErrOutputLimit
	}
	return output.String(), nil
}

func parseVersion(output string) string {
	match := versionPattern.FindStringSubmatch(output)
	if len(match) == 2 {
		return match[1]
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "WARNING:") {
			return line
		}
	}
	return ""
}

func environmentSlice(environment map[string]string) []string {
	result := make([]string, 0, len(environment))
	for key, value := range environment {
		result = append(result, key+"="+value)
	}
	sortStrings(result)
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// minRedactLength 是按值脱敏的最短长度：更短的值（如 "1"、"on"）替换会破坏正常文本。
const minRedactLength = 4

func redactEnvironment(environment map[string]string, extra ...string) func(string) string {
	secrets := make([]string, 0, len(extra))
	for _, value := range extra {
		if len(value) >= minRedactLength {
			secrets = append(secrets, value)
		}
	}
	for key, value := range environment {
		upper := strings.ToUpper(key)
		if value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "CREDENTIAL")) {
			secrets = append(secrets, value)
		}
	}
	// 长的先替换：一个值是另一个值的子串时，避免只替换掉一部分。
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

func safeProbeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "probe timeout"
	}
	text := err.Error()
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
