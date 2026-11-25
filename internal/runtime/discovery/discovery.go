// Package discovery 统一查找当前用户安装的 Runtime CLI。
package discovery

import (
	"bytes"
	"context"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	domain "openfox/internal/runtime"
)

const ShellTimeout = 3 * time.Second

type Candidate struct{ Path, Source string }
type Tool struct {
	Name              string
	EnvKeys, UserBins []string
}
type Search struct {
	Home, OS, Apps string
	Getenv         func(string) string
	LoginPath      func(context.Context, string) string
	SystemBins     []string
}

func Local() Search {
	home, _ := os.UserHomeDir()
	return Search{
		Home: home, OS: runtime.GOOS, Apps: "/Applications",
		Getenv: os.Getenv, LoginPath: loginPath, SystemBins: []string{"/opt/homebrew/bin", "/usr/local/bin"},
	}
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func (s Search) explicit(tool Tool, override string) Candidate {
	if override = strings.TrimSpace(override); override != "" {
		return Candidate{override, "configured"}
	}
	for _, key := range tool.EnvKeys {
		if path := strings.TrimSpace(s.Getenv(key)); path != "" {
			return Candidate{path, "environment"}
		}
	}
	return Candidate{}
}

// Resolve 对每个自动候选调用同一验证器；显式路径验证失败时保留该错误。
func (s Search) Resolve(ctx context.Context, tool Tool, override string, probe func(string) (string, error)) (Candidate, string, error) {
	if c := s.explicit(tool, override); c.Path != "" {
		if !filepath.IsAbs(c.Path) {
			path, err := exec.LookPath(c.Path)
			if err == nil {
				c.Path, err = filepath.Abs(path)
			}
			if err != nil {
				return Candidate{}, "", fmt.Errorf("%w: %s executable not found", domain.ErrUnavailable, tool.Name)
			}
		}
		if !executable(c.Path) {
			return c, "", fmt.Errorf("%w: %s executable not found", domain.ErrUnavailable, tool.Name)
		}
		output, err := probe(c.Path)
		return c, output, err
	}
	var found, failed Candidate
	var output string
	var failedErr error
	s.visit(ctx, tool, func(c Candidate) bool {
		value, err := probe(c.Path)
		if err != nil {
			if failedErr == nil {
				failed, failedErr = c, err
			}
			return false
		}
		found, output = c, value
		return true
	})
	if found.Path != "" {
		return found, output, nil
	}
	if err := ctx.Err(); err != nil {
		return failed, "", err
	}
	if failedErr != nil {
		return failed, "", failedErr
	}
	return Candidate{}, "", fmt.Errorf("%w: %s executable not found", domain.ErrUnavailable, tool.Name)
}

// KnownPath 只接受属于本机该供应商清单的路径，保留 CLI 的启动入口以查找同目录的解释器。
func (s Search) KnownPath(ctx context.Context, tool Tool, configured, requested string) (string, error) {
	if strings.HasPrefix(requested, "~/") {
		requested = filepath.Join(s.Home, requested[2:])
	}
	if !filepath.IsAbs(requested) || !executable(requested) {
		return "", domain.ErrUnavailable
	}
	target, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return "", domain.ErrUnavailable
	}
	var path string
	match := func(c Candidate) bool {
		actual, err := filepath.EvalSymlinks(c.Path)
		if err == nil && actual == target {
			path = c.Path
			return true
		}
		return false
	}
	explicit := s.explicit(tool, configured)
	if !(filepath.IsAbs(explicit.Path) && match(explicit)) {
		s.visit(ctx, tool, match)
	}
	if path == "" {
		return "", domain.ErrUnavailable
	}
	return path, nil
}

func (s Search) visit(ctx context.Context, tool Tool, accept func(Candidate) bool) {
	seen := map[string]bool{}
	try := func(path, source string) bool {
		if ctx.Err() != nil {
			return true
		}
		if !filepath.IsAbs(path) || !executable(path) {
			return false
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || seen[real] {
			return false
		}
		seen[real] = true
		return accept(Candidate{path, source})
	}
	dirs := func(paths []string, source string) bool {
		for _, dir := range paths {
			if filepath.IsAbs(dir) && try(filepath.Join(dir, tool.Name), source) {
				return true
			}
		}
		return false
	}
	if dirs(filepath.SplitList(s.Getenv("PATH")), "path") {
		return
	}
	shell := s.Getenv("SHELL")
	if shell == "" && s.OS == "darwin" {
		shell = "/bin/zsh"
	}
	if s.OS != "windows" && s.LoginPath != nil && filepath.IsAbs(shell) && executable(shell) {
		lookup := func() string { return s.LoginPath(ctx, shell) }
		if scan, ok := ctx.Value(scanKey{}).(*shellScan); ok {
			scan.once.Do(func() { scan.path = lookup() })
			if dirs(filepath.SplitList(scan.path), "login_shell") {
				return
			}
		} else if dirs(filepath.SplitList(lookup()), "login_shell") {
			return
		}
	}
	var bins []string
	for _, item := range []struct{ key, sub string }{{"NVM_BIN", ""}, {"FNM_MULTISHELL_PATH", "bin"}, {"HOMEBREW_PREFIX", "bin"}, {"VOLTA_HOME", "bin"}, {"MISE_DATA_DIR", "shims"}} {
		if v := s.Getenv(item.key); filepath.IsAbs(v) {
			bins = append(bins, filepath.Join(v, item.sub))
		}
	}
	if s.Home != "" {
		for _, rel := range slices.Concat(tool.UserBins, []string{".local/bin", ".npm-global/bin", ".volta/bin", ".bun/bin", ".local/share/mise/shims", ".asdf/shims", ".local/share/pnpm", "Library/pnpm", ".yarn/bin", ".cargo/bin"}) {
			bins = append(bins, filepath.Join(s.Home, rel))
		}
		for _, manager := range []struct{ env, def, sub, bin string }{{"NVM_DIR", ".nvm", "versions/node", "bin"}, {"FNM_DIR", ".fnm", "node-versions", "installation/bin"}, {"", "Library/Application Support/fnm", "node-versions", "installation/bin"}} {
			root := filepath.Join(s.Home, manager.def)
			if manager.env != "" && filepath.IsAbs(s.Getenv(manager.env)) {
				root = s.Getenv(manager.env)
			}
			parent := filepath.Join(root, manager.sub)
			entries, _ := os.ReadDir(parent)
			// 活动版本经 PATH 或版本管理器环境优先命中，其余按版本号倒序尝试。
			sort.SliceStable(entries, func(i, j int) bool {
				return version.Compare("go"+strings.TrimPrefix(entries[i].Name(), "v"), "go"+strings.TrimPrefix(entries[j].Name(), "v")) > 0
			})
			for _, e := range entries {
				bins = append(bins, filepath.Join(parent, e.Name(), manager.bin))
			}
		}
	}
	if dirs(append(bins, s.SystemBins...), "well_known_bin") {
		return
	}
	if tool.Name == "codex" && s.OS == "darwin" {
		for _, root := range []string{s.Apps, filepath.Join(s.Home, "Applications")} {
			if root == "" {
				continue
			}
			for _, app := range []string{"ChatGPT.app", "Codex.app"} {
				for _, rel := range []string{"codex-cli/bin/codex", "codex"} {
					if try(filepath.Join(root, app, "Contents/Resources", rel), "macos_app") {
						return
					}
				}
			}
		}
	}
}

type scanKey struct{}
type shellScan struct {
	once sync.Once
	path string
}

// WithScan 让同一次设备清点中的各供应商共用一次登录环境探测。
func WithScan(ctx context.Context) context.Context {
	return context.WithValue(ctx, scanKey{}, &shellScan{})
}

func loginPath(ctx context.Context, shell string) string {
	ctx, cancel := context.WithTimeout(ctx, ShellTimeout)
	defer cancel()
	const marker = "__OPENFOX_DISCOVERY_PATH__="
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '\n__OPENFOX_DISCOVERY_PATH__=%s\n' "$PATH"`)
	cmd.Dir = os.TempDir()
	cmd.WaitDelay = 250 * time.Millisecond
	var output boundedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return ""
	}
	for _, line := range strings.Split(output.buf.String(), "\n") {
		if path, ok := strings.CutPrefix(line, marker); ok {
			return path
		}
	}
	return ""
}

type boundedOutput struct{ buf bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if left := 65536 - b.buf.Len(); left > 0 {
		b.buf.Write(p[:min(len(p), left)])
	}
	return len(p), nil
}

// ExecutablePath 将所选安装的 bin 目录置于首位，使脚本使用同一工具链的解释器。
func ExecutablePath(current, executable string) string {
	dir := filepath.Dir(executable)
	entries := slices.DeleteFunc(filepath.SplitList(current), func(entry string) bool { return entry == dir })
	return strings.Join(append([]string{dir}, entries...), string(filepath.ListSeparator))
}
