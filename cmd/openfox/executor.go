package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"openfox/internal/daemon/storage"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
	"openfox/internal/runtime/cliagent"
	"openfox/internal/runtime/codex"
)

type executorOptions struct {
	deviceID          string
	wssURL            string
	tokenProvider     func(context.Context) (string, error)
	invalidate        func(string)
	workspaceRoot     string
	runtimeExecutable string
	supportedVersions []string
	journalPath       string
	socketPath        string
	organizations     []string
	pollInterval      time.Duration
	maxRuns           int
	directoryRoots    []string
}

type executorRuntime struct {
	supervisor *supervisor.Supervisor
	journal    *supervisor.Journal
	adapter    *codex.Adapter
	// others 是 Claude Code / OpenCode / pi 适配器（未安装时探测不到，不上报，也不会被派发）。
	others         []*cliagent.Adapter
	client         *supervisor.Client
	root           string
	directoryRoots map[string]string
}

// storageClassifyLimit 是每次统计从 journal 读取的 worktree 用途条数上限。
const storageClassifyLimit = 5000

// storagePaths 返回存储统计的位置：工作区根、独立 CODEX_HOME 根与 journal 文件；
// 可清理缓存目录为消息附件与新 Runtime 的运行临时目录；worktree 用途来自 journal（每次统计读取一次）。
func (e *executorRuntime) storagePaths() storage.Paths {
	stateDir := filepath.Dir(e.journal.Path())
	paths := storage.Paths{
		WorkspaceRoot: e.root, WorktreePrefix: workdir.ManagedWorktreePrefix,
		CodexHomeRoot: e.adapter.HomeRoot, CodexHomePrefix: codex.HomeDirPrefix, JournalPath: e.journal.Path(),
		CacheDirs: []string{filepath.Join(stateDir, "repository-cache"), filepath.Join(stateDir, "attachments"), filepath.Join(stateDir, "runtime-tmp"), filepath.Join(stateDir, "runtime-session-exports")},
	}
	kinds, err := e.journal.WorkspaceKinds(context.Background(), storageClassifyLimit)
	if err != nil {
		kinds = map[string]string{}
	}
	paths.KindOf = func(path string) string { return kinds[path] }
	return paths
}

func (e *executorRuntime) close() {
	if e != nil && e.journal != nil {
		_ = e.journal.Close()
	}
}

func (e *executorRuntime) sessionExporter(name string) runtime.NativeSessionExporter {
	if e == nil {
		return nil
	}
	if e.adapter != nil && e.adapter.Name() == name {
		return e.adapter
	}
	for _, adapter := range e.others {
		if adapter != nil && adapter.Name() == name {
			return adapter
		}
	}
	return nil
}

// newExecutor 组装 Run 执行监督者：HTTPS 基址由 WSS 地址推导，工作目录限制在
// 管理根下，Runtime 只接受受支持版本矩阵中的 Codex。
func newExecutor(options executorOptions) (*executorRuntime, error) {
	baseURL, err := centerBaseURL(options.wssURL)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(strings.TrimSpace(options.workspaceRoot))
	if err != nil {
		return nil, err
	}
	directoryRoots, err := parseDirectoryRoots(options.directoryRoots, root)
	if err != nil {
		return nil, err
	}
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: root, AllowNonGit: true})
	if err != nil {
		return nil, err
	}
	supported := append([]string(nil), options.supportedVersions...)
	if len(supported) == 0 {
		if value := getenv("OPENFOX_CODEX_SUPPORTED_VERSIONS"); value != "" {
			for _, item := range strings.Split(value, ",") {
				if item = strings.TrimSpace(item); item != "" {
					supported = append(supported, item)
				}
			}
		}
	}
	if options.tokenProvider == nil {
		return nil, errors.New("daemon execution requires a device session token")
	}
	adapter := codex.New(options.runtimeExecutable)
	adapter.SupportedVersions = supported
	journalPath := strings.TrimSpace(options.journalPath)
	if journalPath == "" {
		journalPath = filepath.Join(filepath.Dir(options.socketPath), "runs.db")
	}
	journalPath, err = filepath.Abs(journalPath)
	if err != nil {
		return nil, err
	}
	// 每次 Run 的独立 CODEX_HOME 放在 Daemon 状态目录下（0700，由适配器创建），与 journal 同级。
	adapter.HomeRoot = filepath.Join(filepath.Dir(journalPath), "codex-homes")
	adapter.ExportRoot = filepath.Join(filepath.Dir(journalPath), "runtime-session-exports")
	journal, err := supervisor.OpenJournal(context.Background(), journalPath)
	if err != nil {
		return nil, err
	}
	client := &supervisor.Client{BaseURL: baseURL, Token: options.tokenProvider, Invalidate: options.invalidate}
	homeRoot := adapter.HomeRoot
	var others []*cliagent.Adapter
	var extra []runtime.Adapter
	for _, name := range cliagent.Names {
		other := cliagent.New(name, "")
		// 每次 Run 的临时配置（任务工具桥的 MCP 配置）放在状态目录下，0700。
		other.StateRoot = filepath.Join(filepath.Dir(journalPath), "runtime-tmp")
		others = append(others, other)
		extra = append(extra, other)
	}
	instance, err := supervisor.New(supervisor.Config{
		DeviceID: options.deviceID, Client: client, Adapter: adapter, Extra: extra, Workdir: manager, DirectoryRoots: directoryRoots, Journal: journal, Organizations: options.organizations,
		PollInterval: options.pollInterval, MaxConcurrent: options.maxRuns,
		// 消息附件下载到状态目录下（工作目录之外），Run 结束后删除。
		AttachmentRoot: filepath.Join(filepath.Dir(journalPath), "attachments"),
		// 每个 Run 的产物目录放在状态目录下（工作目录之外），Run 结束后删除。
		ArtifactRoot:          filepath.Join(filepath.Dir(journalPath), "run-artifacts"),
		CleanupHomes:          func(now time.Time) (int, int64) { return codex.RemoveStaleHomes(homeRoot, now) },
		CleanupSessionExports: func(now time.Time) (int, int64) { return codex.RemoveStaleSessionExports(adapter.ExportRoot, now) },
	})
	if err != nil {
		_ = journal.Close()
		return nil, err
	}
	return &executorRuntime{supervisor: instance, journal: journal, adapter: adapter, others: others, client: client, root: manager.ManagedRoot(), directoryRoots: directoryRoots}, nil
}

// deviceConnectPath 是中心 WSS 端点相对 API 根的路径。
const deviceConnectPath = "/v1/device/connect"

// centerBaseURL 把 wss://host/<前缀>/v1/device/connect 转为 https://host/<前缀>（ws 对应 http，仅用于本地开发）。
// 保留前缀：中心 API 挂在 /api 下时，设备 HTTP 端点同样需要该前缀。
func centerBaseURL(wssURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(wssURL))
	if err != nil || parsed.Host == "" {
		return "", errors.New("daemon: invalid --wss-url")
	}
	switch parsed.Scheme {
	case "wss":
		parsed.Scheme = "https"
	case "ws":
		parsed.Scheme = "http"
	default:
		return "", errors.New("daemon: --wss-url must use ws or wss")
	}
	prefix := strings.TrimRight(strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), deviceConnectPath), "/")
	return parsed.Scheme + "://" + parsed.Host + prefix, nil
}

// parseDirectoryRoots 解析可重复的 --directory-root name=/abs/path。
// name 与中心 Agent 配置的 EXPLICIT 根名规则一致（相对逻辑引用）；path 必须是已存在的
// 绝对目录，且不能与管理根互相包含（worktree 创建在管理根下，避免嵌套）。
// 是否为 Git 仓库在 Run 准备时检查，失败回报 REPO_UNAVAILABLE。
func parseDirectoryRoots(values []string, managedRoot string) (map[string]string, error) {
	roots := make(map[string]string, len(values))
	managed := managedRoot
	if resolved, err := filepath.EvalSymlinks(managedRoot); err == nil {
		managed = resolved
	}
	for _, value := range values {
		name, path, ok := strings.Cut(value, "=")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		if !ok || !validRootName(name) {
			return nil, fmt.Errorf("daemon: --directory-root must be name=/absolute/path with a relative logical name, got %q", value)
		}
		if _, exists := roots[name]; exists {
			return nil, fmt.Errorf("daemon: --directory-root %s is configured more than once", name)
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("daemon: --directory-root %s path must be absolute", name)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("daemon: --directory-root %s path is unavailable: %w", name, err)
		}
		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("daemon: --directory-root %s path is not a directory", name)
		}
		if pathWithin(managed, resolved) || pathWithin(resolved, managed) {
			return nil, fmt.Errorf("daemon: --directory-root %s must not overlap --workspace-root", name)
		}
		roots[name] = resolved
	}
	return roots, nil
}

// validRootName 与 internal/agents 中 EXPLICIT 根名校验保持一致。
func validRootName(name string) bool {
	if name == "" || len(name) > 256 || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") ||
		strings.Contains(name, "\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
