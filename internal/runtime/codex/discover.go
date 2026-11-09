package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"sort"
	"strings"
	"time"

	"openfox/internal/runtime"
)

// 候选来源，按发现顺序排列；写入 Capabilities.Reason 之外的诊断日志时使用。
const (
	sourceConfigured   = "configured"
	sourceExplicitEnv  = "explicit_env"
	sourcePath         = "path"
	sourceLoginShell   = "login_shell"
	sourceWellKnownBin = "well_known_bin"
	sourceMacOSApp     = "macos_app"
)

// 登录 shell 探测上限：交互式 zsh 加载 nvm 等插件可能较慢，但不能拖住整个探测。
const loginShellTimeout = 3 * time.Second

// loginShellMarker 用于从交互式 shell 的杂散输出中定位 command -v 的结果。
const loginShellMarker = "__OPENFOX_CODEX_PATH__="

type candidate struct {
	path   string
	source string
}

// discovery 收集发现所需的外部输入，测试可替换。
type discovery struct {
	getenv     func(string) string
	home       string
	goos       string
	loginShell func(ctx context.Context, shell string) string
	// systemBins / systemApps 是与用户无关的系统级位置，测试中替换以免命中本机真实安装。
	systemBins []string
	systemApps string
}

func defaultDiscovery() discovery {
	home, _ := os.UserHomeDir()
	return discovery{
		getenv: os.Getenv, home: home, goos: stdruntime.GOOS, loginShell: loginShellLookup,
		systemBins: []string{"/opt/homebrew/bin", "/usr/local/bin"}, systemApps: "/Applications",
	}
}

// explicit 返回显式配置的可执行文件（参数优先，其次环境变量）。
// 显式配置只认该值：找不到时直接失败，避免静默换成另一个 Codex。
func (d discovery) explicit(configured string) (candidate, bool) {
	if value := strings.TrimSpace(configured); value != "" {
		return candidate{path: value, source: sourceConfigured}, true
	}
	for _, key := range []string{"OPENFOX_CODEX_PATH", "CODEX_PATH"} {
		if value := strings.TrimSpace(d.getenv(key)); value != "" {
			return candidate{path: value, source: sourceExplicitEnv}, true
		}
	}
	return candidate{}, false
}

// automatic 按 PATH → 登录 shell → 常见用户目录 → macOS App 内置路径 的顺序给出候选。
// 登录 shell 是惰性的：只有前面的候选都不可用时才启动。
func (d discovery) automatic(ctx context.Context, accept func(candidate) bool) bool {
	seen := map[string]struct{}{}
	try := func(item candidate) bool {
		if item.path == "" || !filepath.IsAbs(item.path) {
			return false
		}
		key := item.path
		if resolved, err := filepath.EvalSymlinks(item.path); err == nil {
			key = resolved
		}
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		return isExecutableFile(item.path) && accept(item)
	}
	for _, dir := range filepath.SplitList(d.getenv("PATH")) {
		if dir != "" && filepath.IsAbs(dir) && try(candidate{path: filepath.Join(dir, "codex"), source: sourcePath}) {
			return true
		}
	}
	if d.goos != "windows" && d.loginShell != nil {
		if shell := strings.TrimSpace(d.getenv("SHELL")); filepath.IsAbs(shell) && isExecutableFile(shell) {
			if path := d.loginShell(ctx, shell); try(candidate{path: path, source: sourceLoginShell}) {
				return true
			}
		}
	}
	for _, path := range d.wellKnownBins() {
		if try(candidate{path: path, source: sourceWellKnownBin}) {
			return true
		}
	}
	for _, path := range d.macOSAppBins() {
		if try(candidate{path: path, source: sourceMacOSApp}) {
			return true
		}
	}
	return false
}

// wellKnownBins 覆盖常见的 Node 版本管理器与包管理器安装位置。
// Daemon 由 launchd/桌面端启动时 PATH 往往不含这些目录。
func (d discovery) wellKnownBins() []string {
	var dirs []string
	add := func(values ...string) {
		for _, value := range values {
			if value != "" {
				dirs = append(dirs, value)
			}
		}
	}
	add(d.getenv("NVM_BIN"))
	if prefix := d.getenv("HOMEBREW_PREFIX"); prefix != "" {
		add(filepath.Join(prefix, "bin"))
	}
	if volta := d.getenv("VOLTA_HOME"); volta != "" {
		add(filepath.Join(volta, "bin"))
	}
	if d.home != "" {
		add(
			filepath.Join(d.home, ".local", "bin"),
			filepath.Join(d.home, ".npm-global", "bin"),
			filepath.Join(d.home, ".volta", "bin"),
			filepath.Join(d.home, ".bun", "bin"),
			filepath.Join(d.home, ".local", "share", "mise", "shims"),
			filepath.Join(d.home, ".asdf", "shims"),
			filepath.Join(d.home, ".local", "share", "pnpm"),
			filepath.Join(d.home, "Library", "pnpm"),
			filepath.Join(d.home, ".yarn", "bin"),
			filepath.Join(d.home, ".cargo", "bin"),
		)
		// nvm / fnm 的已安装版本：按目录名倒序，优先较新的 Node 版本。
		add(sortedGlobDesc(filepath.Join(d.home, ".nvm", "versions", "node", "*", "bin"))...)
		add(sortedGlobDesc(filepath.Join(d.home, ".fnm", "node-versions", "*", "installation", "bin"))...)
		add(sortedGlobDesc(filepath.Join(d.home, "Library", "Application Support", "fnm", "node-versions", "*", "installation", "bin"))...)
	}
	add(d.systemBins...)
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, "codex"))
	}
	return paths
}

// macOSAppBins 是 ChatGPT/Codex 桌面应用内置 CLI 的位置。
func (d discovery) macOSAppBins() []string {
	if d.goos != "darwin" {
		return nil
	}
	var roots []string
	if d.systemApps != "" {
		roots = append(roots, d.systemApps)
	}
	if d.home != "" {
		roots = append(roots, filepath.Join(d.home, "Applications"))
	}
	var paths []string
	for _, root := range roots {
		paths = append(paths,
			filepath.Join(root, "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin", "codex"),
			filepath.Join(root, "ChatGPT.app", "Contents", "Resources", "codex"),
			filepath.Join(root, "Codex.app", "Contents", "Resources", "codex-cli", "bin", "codex"),
			filepath.Join(root, "Codex.app", "Contents", "Resources", "codex"),
		)
	}
	return paths
}

func sortedGlobDesc(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// loginShellLookup 在用户的登录 + 交互 shell 中执行 command -v codex，
// 以覆盖只在 ~/.zshrc 等文件里配置的 PATH（例如 nvm）。stdin 为空、限时、输出有界。
func loginShellLookup(ctx context.Context, shell string) string {
	lookupContext, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()
	script := `p=$(command -v codex 2>/dev/null) && printf '` + loginShellMarker + `%s\n' "$p"`
	cmd := exec.CommandContext(lookupContext, shell, "-ilc", script)
	cmd.Stdin = nil
	var output bytes.Buffer
	cmd.Stdout = &limitedBuffer{buffer: &output, limit: 64 * 1024}
	// shell 启动脚本的 stderr 噪声与结果无关，直接丢弃。
	cmd.Stderr = nil
	cmd.WaitDelay = 500 * time.Millisecond
	_ = cmd.Run()
	for _, line := range strings.Split(output.String(), "\n") {
		if index := strings.Index(line, loginShellMarker); index >= 0 {
			return strings.TrimSpace(line[index+len(loginShellMarker):])
		}
	}
	return ""
}

type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buffer.Write(p[:remaining])
		} else {
			b.buffer.Write(p)
		}
	}
	return len(p), nil
}

// resolveConfigured 解析显式配置：含路径分隔符的视为文件路径，否则按 PATH 查找。
func resolveConfigured(value string) (string, error) {
	path := value
	if !strings.ContainsRune(value, filepath.Separator) {
		resolved, err := exec.LookPath(value)
		if err != nil {
			return "", fmt.Errorf("%w: Codex executable not found", runtime.ErrUnavailable)
		}
		path = resolved
	}
	absolute, err := filepath.Abs(path)
	if err != nil || !isExecutableFile(absolute) {
		return "", fmt.Errorf("%w: Codex executable not found", runtime.ErrUnavailable)
	}
	return absolute, nil
}

// withExecutableDir 把可执行文件所在目录放到 PATH 最前面，
// 使 nvm 等安装的 `#!/usr/bin/env node` 脚本能找到同目录的 node。
func withExecutableDir(environment map[string]string, executable string) map[string]string {
	dir := filepath.Dir(executable)
	current := environment["PATH"]
	if current == "" {
		current = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	for _, entry := range filepath.SplitList(current) {
		if entry == dir {
			environment["PATH"] = current
			return environment
		}
	}
	environment["PATH"] = dir + string(filepath.ListSeparator) + current
	return environment
}

// ValidateDevicePath never probes an arbitrary file received from the network.
// A device owner may select an existing installation in the daemon's own inventory.
func (a *Adapter) ValidateDevicePath(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	d := defaultDiscovery()
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(d.home, path[2:])
	}
	if !filepath.IsAbs(path) || !isExecutableFile(path) {
		return "", runtime.ErrUnavailable
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	matches := func(c candidate) bool {
		v, e := resolveConfigured(c.path)
		if e != nil {
			return false
		}
		p, e := filepath.EvalSymlinks(v)
		return e == nil && p == real
	}
	item, explicit := d.explicit(a.Executable)
	if !(explicit && matches(item)) && !d.automatic(ctx, matches) {
		return "", runtime.ErrUnavailable
	}
	probe := New(real)
	report, err := probe.Detect(ctx)
	if err != nil || report.Version == "unknown" {
		return "", runtime.ErrUnavailable
	}
	return real, nil
}

func (a *Adapter) UseDevicePath(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.devicePath = path
	a.resolved = ""
}
