package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/storage"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/daemon/workdir"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
	"openfox/internal/runtime/discovery"
	"openfox/internal/runtime/mcpconfig"
	"openfox/internal/runtimepolicy"
)

// capabilityRefreshInterval 是重新探测 Runtime 与可用模型的周期；心跳之间复用最近一次结果。
const capabilityRefreshInterval = 10 * time.Minute

// runtimeCapabilities 是心跳能力 JSON 中 runtimes.codex 的结构。中心按不透明 JSON 保存（上限 64KiB），
// UI 从设备列表的 capabilities.runtimes.codex.models 读取可选模型。
type runtimeCapabilities struct {
	InstallPath              string        `json:"install_path,omitempty"`
	DiscoverySource          string        `json:"discovery_source,omitempty"`
	DiscoveryCached          bool          `json:"discovery_cached,omitempty"`
	DiscoveryOnly            bool          `json:"discovery_only,omitempty"`
	Version                  string        `json:"version,omitempty"`
	Login                    string        `json:"login_status,omitempty"`
	Runnable                 bool          `json:"runnable"`
	ProtocolReady            bool          `json:"protocol_ready,omitempty"`
	RequiresRunConfiguration bool          `json:"requires_run_configuration,omitempty"`
	Reason                   string        `json:"reason,omitempty"`
	Models                   []codex.Model `json:"models"`
	ModelsError              string        `json:"models_error,omitempty"`
	// ApprovalModes 是适配器真正支持的权限档位，一定含 FOLLOW_RUNTIME；
	// 中心据此在派发前拒绝设备不支持的档位。旧版后台服务没有该字段，中心按只支持 FOLLOW_RUNTIME 处理。
	ApprovalModes    []string `json:"approval_modes"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
	// CollaborationModes 是适配器真正实现了的协作模式（阶段 9a-2），一定含 FOLLOW_RUNTIME，与 approval_modes 并列；
	// 旧版后台服务没有该字段，中心按只支持 FOLLOW_RUNTIME 处理。
	CollaborationModes []string  `json:"collaboration_modes"`
	CheckedAt          time.Time `json:"checked_at"`
}

// approvalModesOf 读取探测器（适配器）支持的权限档位；没有声明的只支持 FOLLOW_RUNTIME。
func approvalModesOf(prober capabilityProber) []string {
	if typed, ok := prober.(interface{ ApprovalModes() []string }); ok {
		return runtimepolicy.NormalizeApprovalModes(typed.ApprovalModes())
	}
	return runtimepolicy.NormalizeApprovalModes(nil)
}

// collaborationModesOf 读取探测器（适配器）支持的协作模式；没有声明的只支持 FOLLOW_RUNTIME。
func collaborationModesOf(prober capabilityProber) []string {
	if typed, ok := prober.(interface{ CollaborationModes() []string }); ok {
		return runtimepolicy.NormalizeCollaborationModes(typed.CollaborationModes())
	}
	return runtimepolicy.NormalizeCollaborationModes(nil)
}

func reasoningEffortsOf(prober capabilityProber) []string {
	if typed, ok := prober.(interface{ ReasoningEfforts() []string }); ok {
		return runtimepolicy.NormalizeReasoningEfforts(typed.ReasoningEfforts())
	}
	return runtimepolicy.NormalizeReasoningEfforts(nil)
}

type capabilityProber interface {
	Detect(context.Context) (runtime.Capabilities, error)
	ListModels(context.Context) ([]codex.Model, error)
}

// namedProber 是 Codex 之外的 Runtime 探测器（阶段 7f：Claude Code / OpenCode / pi）。
type namedProber struct {
	name   string
	prober capabilityProber
	// models 保留最近一次成功的模型列表。
	models []codex.Model
}

// capabilityReporter 统一维护心跳 runtimes、development_tools 与 storage，
// 任一部分更新后合并写入心跳源。prober 是 Codex；extras 是其他 Runtime，未安装的不上报。
type capabilityReporter struct {
	directoryRoots        map[string]string
	gitRoots              map[string]bool
	prober                capabilityProber
	cachePath             string
	cacheFingerprint      func() string
	developmentToolsProbe func(context.Context) []devices.DevelopmentTool
	timezoneProbe         func() string
	extras                []*namedProber
	source                *daemon.StaticHeartbeatSource
	now                   func() time.Time

	// refreshMu 串行化 Runtime 探测（周期刷新与 runtime_probe 命令可能并发）。
	refreshMu sync.Mutex
	// models 保留最近一次成功的 Codex 模型列表；探测失败时继续上报，并附上失败原因。
	models []codex.Model

	mu               sync.Mutex
	runtimes         *runtimeCapabilities
	others           map[string]runtimeCapabilities
	storage          *storage.Stats
	developmentTools []devices.DevelopmentTool
	timezone         string
	inventoryOrg     string
	inventoryWake    chan struct{}
	refreshWake      chan struct{}
	storeInventory   func(context.Context, string, map[string]mcpconfig.Inventory) error
	inventoryMu      sync.Mutex
	// additional 是阶段 12e 起附加的能力位与本机状态（本机并发上限、受保护根、密钥轮换、多组织、配置版本），
	// 由 setExtra 更新；旧中心忽略未知键。
	additional map[string]any
}

// setExtra 更新一个附加能力键并重新发布心跳 capabilities；value 为 nil 时删除该键。
func (r *capabilityReporter) setExtra(key string, value any) {
	r.mu.Lock()
	if r.additional == nil {
		r.additional = map[string]any{}
	}
	if value == nil {
		delete(r.additional, key)
	} else {
		r.additional[key] = value
	}
	r.mu.Unlock()
	r.publish()
}

func (r *capabilityReporter) run(ctx context.Context) {
	ticker := time.NewTicker(capabilityRefreshInterval)
	defer ticker.Stop()
	r.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refresh(ctx)
		case <-r.refreshWake:
			r.refresh(ctx)
		case <-r.inventoryWake:
			_ = r.uploadInventory(ctx)
		}
	}
}

// recordDiscovery 仅上报通过版本探测的路径，用户目录用 ~ 缩写。
func (v *runtimeCapabilities) recordDiscovery(c runtime.Capabilities) {
	v.DiscoveryOnly = c.DiscoveryOnly
	if c.Version == "" || c.Version == "unknown" || c.Executable == "" {
		return
	}
	path := c.Executable
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	v.InstallPath, v.DiscoverySource = path, c.DiscoverySource
}

// detect 先取得安装和协议状态，模型目录的阻塞不会延迟其他 CLI 的登记。
func (r *capabilityReporter) detect(ctx context.Context, entry *namedProber) (runtimeCapabilities, bool, bool) {
	prober := entry.prober
	value := runtimeCapabilities{Models: entry.models, ApprovalModes: approvalModesOf(prober), ReasoningEfforts: reasoningEffortsOf(prober), CollaborationModes: collaborationModesOf(prober), CheckedAt: r.clock().UTC().Truncate(time.Second)}
	if value.Models == nil {
		value.Models = []codex.Model{}
	}
	report, err := prober.Detect(ctx)
	value.recordDiscovery(report)
	value.Version, value.Login, value.Runnable, value.Reason = report.Version, string(report.Login), report.Runnable, report.Reason
	value.ProtocolReady, value.RequiresRunConfiguration = report.ProtocolReady, report.RequiresRunConfiguration
	if err != nil {
		value.Reason = boundedText(err.Error())
		value.Runnable = false
		value.ProtocolReady = false
	}
	found := err == nil || report.Executable != ""
	list := err == nil && !report.DiscoveryOnly && report.Login != runtime.LoginLoggedOut && (entry.name != codex.RuntimeName || report.Runnable)
	return value, found, list
}

// publishRuntimes 发布一份独立快照，使模型刷新期间的读者始终看到完整清单。
func (r *capabilityReporter) publishRuntimes(values map[string]runtimeCapabilities) {
	value, hasCodex := values[codex.RuntimeName]
	others := maps.Clone(values)
	delete(others, codex.RuntimeName)
	r.mu.Lock()
	r.others = others
	r.runtimes = nil
	if hasCodex {
		r.runtimes = &value
	}
	r.mu.Unlock()
	r.publish()
}

// refresh 重新探测全部 Runtime，更新心跳并返回 Codex 的结果（runtime_probe 命令复用 allRuntimes 取全部）。
func (r *capabilityReporter) refresh(ctx context.Context) runtimeCapabilities {
	result, _ := r.refreshWithInventory(ctx)
	return result
}

func (r *capabilityReporter) refreshWithInventory(ctx context.Context) (runtimeCapabilities, error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.timezoneProbe != nil {
		zone := r.timezoneProbe()
		r.mu.Lock()
		r.timezone = zone
		r.mu.Unlock()
		r.publish()
	}
	if r.developmentToolsProbe != nil {
		tools := r.developmentToolsProbe(ctx)
		r.mu.Lock()
		r.developmentTools = tools
		r.mu.Unlock()
		r.publish()
	}
	if r.prober == nil {
		return runtimeCapabilities{}, nil
	}
	probeCtx, cancel := context.WithTimeout(discovery.WithScan(ctx), time.Minute)
	defer cancel()
	var fingerprint string
	if r.cacheFingerprint != nil {
		fingerprint = r.cacheFingerprint()
	}
	primary := &namedProber{name: codex.RuntimeName, prober: r.prober, models: r.models}
	entries := append([]*namedProber{primary}, r.extras...)
	values := make(map[string]runtimeCapabilities, len(entries))
	var catalogs []*namedProber
	for _, entry := range entries {
		value, found, list := r.detect(probeCtx, entry)
		if probeCtx.Err() != nil {
			return runtimeCapabilities{}, probeCtx.Err()
		}
		if found || entry == primary {
			values[entry.name] = value
		}
		if list {
			catalogs = append(catalogs, entry)
		}
	}
	r.publishRuntimes(values)
	if fingerprint != "" && fingerprint == r.cacheFingerprint() {
		entries := make(map[string]daemon.DiscoveryEntry, len(values))
		for name, value := range values {
			if value.InstallPath != "" && value.DiscoverySource != "" {
				entries[name] = daemon.DiscoveryEntry{Path: value.InstallPath, Source: value.DiscoverySource, Version: value.Version}
			}
		}
		if err := daemon.WriteDiscoveryCache(r.cachePath, fingerprint, r.clock(), entries); err != nil {
			slog.Warn("runtime discovery cache write failed", "error", err)
		}
	}
	for _, entry := range catalogs {
		if probeCtx.Err() != nil {
			break
		}
		value := values[entry.name]
		models, err := entry.prober.ListModels(probeCtx)
		if err != nil {
			value.ModelsError = boundedText(err.Error())
		}
		if err == nil || (entry.models == nil && len(models) > 0 && entry != primary) {
			entry.models = models
		}
		if entry.models != nil {
			value.Models = entry.models
		}
		values[entry.name] = value
	}
	r.models = primary.models
	r.publishRuntimes(values)
	return values[codex.RuntimeName], r.uploadInventory(ctx)
}

// restoreDiscovery 在首次心跳前登记同一环境中仍有效的安装记录。
func (r *capabilityReporter) restoreDiscovery() {
	entries, checked := daemon.ReadDiscoveryCache(r.cachePath, r.cacheFingerprint(), r.clock())
	if len(entries) == 0 {
		return
	}
	values := make(map[string]runtimeCapabilities, len(entries))
	for name, entry := range entries {
		if _, ok := r.proberFor(name); !ok {
			continue
		}
		values[name] = runtimeCapabilities{
			InstallPath: entry.Path, DiscoverySource: entry.Source, Version: entry.Version,
			DiscoveryCached: true, DiscoveryOnly: name == "cursor", CheckedAt: checked,
			Models: []codex.Model{},
		}
	}
	r.publishRuntimes(values)
}

// discoveryFingerprint 只包含影响发现顺序和显式路径的设置，不读取凭据。
func discoveryFingerprint(executable string, preferences runtimepolicy.DevicePreferences) string {
	hash := sha256.New()
	// 空映射经设置 JSON 往返后会变成 nil，两者代表同一自动发现配置。
	if len(preferences.RuntimePaths) == 0 {
		preferences.RuntimePaths = nil
	}
	input := []any{executable, preferences.CodexPath, preferences.RuntimePaths}
	for _, key := range []string{
		"HOME", "PATH", "SHELL", "NVM_BIN", "NVM_DIR", "FNM_DIR", "FNM_MULTISHELL_PATH",
		"HOMEBREW_PREFIX", "VOLTA_HOME", "MISE_DATA_DIR", "OPENFOX_CODEX_PATH", "CODEX_PATH",
		"OPENFOX_CLAUDE_PATH", "OPENFOX_OPENCODE_PATH", "OPENFOX_PI_PATH", "OPENFOX_CURSOR_PATH",
		"OPENFOX_CODEBUDDY_PATH", "OPENFOX_GEMINI_PATH", "OPENFOX_GEMINI_NODE_PATH",
	} {
		input = append(input, os.Getenv(key))
	}
	_ = json.NewEncoder(hash).Encode(input)
	return hex.EncodeToString(hash.Sum(nil))
}

// allRuntimes 返回当前全部 Runtime 的上报（心跳 capabilities.runtimes 的结构）。
func (r *capabilityReporter) allRuntimes() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runtimesLocked()
}

func (r *capabilityReporter) runtimesLocked() map[string]any {
	result := map[string]any{}
	if r.runtimes != nil {
		result[codex.RuntimeName] = *r.runtimes
	}
	for name, value := range r.others {
		result[name] = value
	}
	return result
}

// proberFor 返回指定 Runtime 的探测器；名称不认识时 ok 为 false。
func (r *capabilityReporter) proberFor(provider string) (capabilityProber, bool) {
	if provider == codex.RuntimeName {
		return r.prober, true
	}
	for _, extra := range r.extras {
		if extra.name == provider {
			return extra.prober, true
		}
	}
	return nil, false
}

// setStorage 更新存储统计部分。
func (r *capabilityReporter) setStorage(stats storage.Stats) {
	r.mu.Lock()
	r.storage = &stats
	r.mu.Unlock()
	r.publish()
}

// probeGitRoots inspects only registered directory roots, once at daemon startup.
// The regular heartbeat advertises the cached Git subset without shelling out.
func probeGitRoots(ctx context.Context, roots map[string]string) map[string]bool {
	result := make(map[string]bool)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var names []string
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		d, err := workdir.ResolveDirectory(name, roots, []string{name})
		if err == nil && d.IsGitRoot(ctx) {
			result[name] = true
		}
	}
	return result
}

func (r *capabilityReporter) setInventoryPrivacy(org string, supported bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !supported {
		if r.inventoryOrg == org {
			r.inventoryOrg = ""
		}
		return
	}
	if r.inventoryOrg != "" {
		return
	}
	r.inventoryOrg = org
	select {
	case r.inventoryWake <- struct{}{}:
	default:
	}
}

func (r *capabilityReporter) uploadInventory(ctx context.Context) error {
	r.inventoryMu.Lock()
	defer r.inventoryMu.Unlock()
	r.mu.Lock()
	org := r.inventoryOrg
	runtimes := r.runtimesLocked()
	r.mu.Unlock()
	if org == "" || r.storeInventory == nil {
		return nil
	}
	inventory := make(map[string]mcpconfig.Inventory, len(runtimes))
	for name := range runtimes {
		if !mcpconfig.SupportsInventory(name) {
			continue
		}
		inventory[name] = mcpconfig.Read(name, mcpconfig.UserLocation())
	}
	if err := r.storeInventory(ctx, org, inventory); err != nil {
		var apiErr *supervisor.APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 403 || apiErr.Status == 404 || apiErr.Status == 405) {
			r.setInventoryPrivacy(org, false)
			if apiErr.Status != 403 {
				return nil
			}
		}
		return errors.New("device MCP inventory could not be refreshed")
	}
	return nil
}

func (r *capabilityReporter) publish() {
	names := make([]string, 0, len(r.directoryRoots))
	for name := range r.directoryRoots {
		if _, err := workdir.ResolveDirectory(name, r.directoryRoots, []string{name}); err == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	gitNames := make([]string, 0, len(names))
	for _, name := range names {
		if r.gitRoots[name] {
			gitNames = append(gitNames, name)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	capabilities := map[string]any{}
	if r.prober != nil || r.directoryRoots != nil {
		capabilities = map[string]any{"workspace_path": true, "gitlab_proxy": true, "agent_mcp": true, "conversation_workdir": true, "runtime_session_export": true, "storage.conversation_filter": true, "workdir_busy_requeue": true, "file_comments": true, "conversation_workdir_roots": names, "conversation_workdir_git_roots": gitNames}
	}
	if runtimes := r.runtimesLocked(); len(runtimes) > 0 {
		capabilities["runtimes"] = runtimes
	}
	if r.developmentTools != nil {
		capabilities["development_tools"] = r.developmentTools
	}
	if r.storage != nil {
		capabilities["storage"] = *r.storage
	}
	if r.timezone != "" {
		capabilities["timezone"] = r.timezone
	}
	for key, value := range r.additional {
		if _, exists := capabilities[key]; !exists {
			capabilities[key] = value
		}
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return
	}
	r.source.SetCapabilities(encoded)
}

func (r *capabilityReporter) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// storageReporter 每 10 分钟统计一次存储占用并写入心跳。
type storageReporter struct {
	// pathsFn 每次统计时调用，使 worktree 用途等动态信息保持最新。
	pathsFn  func() storage.Paths
	reporter *capabilityReporter
}

func (s *storageReporter) measure(ctx context.Context) storage.Stats {
	stats := storage.Measure(ctx, s.pathsFn(), time.Now())
	s.reporter.setStorage(stats)
	return stats
}

func (s *storageReporter) run(ctx context.Context) {
	ticker := time.NewTicker(storage.MeasureInterval)
	defer ticker.Stop()
	for {
		s.measure(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func boundedText(text string) string {
	if len(text) > 512 {
		return text[:512]
	}
	return text
}
