package codex

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openfox/internal/runtime"
)

// 每次 Run 使用独立的 CODEX_HOME：
//   - 只包含登录凭据 auth.json 与本适配器生成的 config.toml；
//     不继承用户 CODEX_HOME 中的 config.toml、AGENTS.md、skills、MCP、会话记录等，
//     使 Agent 的行为不随本机个人配置漂移；
//   - auth.json 复制为 0600；Run 结束后若 Codex 刷新了令牌，且源文件期间未被其他进程更新，
//     原子写回源文件，避免轮换后的刷新令牌丢失导致用户本机登录失效；
//   - Run 结束后整个目录删除；异常退出遗留的目录在之后的 Run 准备时清理。
const (
	authFileName   = "auth.json"
	homeDirPrefix  = "codex-home-"
	staleHomeAfter = runtime.MaxRunTimeout + time.Hour
)

// isolatedConfig 是独立 CODEX_HOME 的 config.toml。每轮请求仍显式下发审批与沙箱策略，
// 这里作为同一策略的兜底默认值。
const isolatedConfig = `# 由 OpenFox Daemon 为单次 Run 生成，Run 结束后删除。
approval_policy = "never"
sandbox_mode = "workspace-write"
cli_auth_credentials_store = "file"

[sandbox_workspace_write]
network_access = true
`

// isolatedConfigFor 按权限档位生成 config.toml：FOLLOW_RUNTIME / WORKSPACE_WRITE 与 isolatedConfig 完全一致；
// READ_ONLY 兜底为 read-only，FULL_ACCESS 为 danger-full-access（不写 sandbox_workspace_write 段）。
func isolatedConfigFor(mode string) string {
	switch sandboxMode(mode) {
	case "read-only":
		return strings.Replace(isolatedConfig, `sandbox_mode = "workspace-write"`, `sandbox_mode = "read-only"`, 1)
	case "danger-full-access":
		return strings.Replace(isolatedConfig, `sandbox_mode = "workspace-write"`, `sandbox_mode = "danger-full-access"`, 1)
	default:
		return isolatedConfig
	}
}

type codexHome struct {
	dir        string
	exportRoot string
	// 源 auth.json 及其在复制时刻的状态，用于判断能否安全写回。
	sourceAuth string
	sourceMod  time.Time
	sourceSize int64
	sourceSum  [32]byte
}

// prepareCodexHome 在 root 下创建独立 CODEX_HOME。source 为用户的 CODEX_HOME（通常是 ~/.codex）。
func prepareCodexHome(root, source string) (*codexHome, error) {
	return prepareCodexHomeWithConfig(root, source, isolatedConfig)
}

// prepareCodexHomeWithConfig 同 prepareCodexHome，但 config.toml 的内容由调用方给出（见 isolatedConfigFor）。
func prepareCodexHomeWithConfig(root, source, config string) (*codexHome, error) {
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: prepare codex home root: %v", runtime.ErrUnavailable, safeProbeError(err))
	}
	removeStaleHomes(root, time.Now())
	sourceAuth := filepath.Join(source, authFileName)
	contents, err := os.ReadFile(sourceAuth)
	if err != nil {
		// 凭据只存于系统钥匙串（cli_auth_credentials_store = "keyring"）时也会走到这里；暂不支持。
		return nil, fmt.Errorf("%w: Codex login credentials file not found", runtime.ErrUnavailable)
	}
	info, err := os.Stat(sourceAuth)
	if err != nil {
		return nil, fmt.Errorf("%w: Codex login credentials file not readable", runtime.ErrUnavailable)
	}
	dir, err := os.MkdirTemp(root, homeDirPrefix)
	if err != nil {
		return nil, fmt.Errorf("%w: create codex home: %v", runtime.ErrUnavailable, safeProbeError(err))
	}
	home := &codexHome{dir: dir, sourceAuth: sourceAuth, sourceMod: info.ModTime(), sourceSize: info.Size(), sourceSum: sha256.Sum256(contents)}
	if err := os.Chmod(dir, 0o700); err != nil {
		home.remove()
		return nil, fmt.Errorf("%w: secure codex home: %v", runtime.ErrUnavailable, safeProbeError(err))
	}
	if err := os.WriteFile(filepath.Join(dir, authFileName), contents, 0o600); err != nil {
		home.remove()
		return nil, fmt.Errorf("%w: copy Codex login credentials: %v", runtime.ErrUnavailable, safeProbeError(err))
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		home.remove()
		return nil, fmt.Errorf("%w: write codex config: %v", runtime.ErrUnavailable, safeProbeError(err))
	}
	return home, nil
}

// finish 在 Run 结束后调用：按需写回刷新后的凭据，然后删除目录。返回写回是否发生。
func (h *codexHome) finish(sessionIDs ...string) (bool, error) {
	defer h.remove()
	var snapshotErr error
	if len(sessionIDs) > 0 && sessionIDs[0] != "" && h.exportRoot != "" {
		snapshotErr = copySessionToExport(h.exportRoot, h.dir, sessionIDs[0])
	}
	updated, err := os.ReadFile(filepath.Join(h.dir, authFileName))
	if err != nil || len(updated) == 0 || sha256.Sum256(updated) == h.sourceSum {
		return false, snapshotErr
	}
	// 源文件在 Run 期间被其他进程（例如用户自己的 Codex）更新过：保留源文件，放弃本次刷新结果。
	current, err := os.ReadFile(h.sourceAuth)
	if err != nil {
		return false, errors.Join(snapshotErr, err)
	}
	info, err := os.Stat(h.sourceAuth)
	if err != nil {
		return false, errors.Join(snapshotErr, err)
	}
	if sha256.Sum256(current) != h.sourceSum || !info.ModTime().Equal(h.sourceMod) || info.Size() != h.sourceSize {
		return false, snapshotErr
	}
	writeErr := atomicWrite(h.sourceAuth, updated, 0o600)
	return writeErr == nil, errors.Join(snapshotErr, writeErr)
}

func (h *codexHome) remove() {
	if h != nil && h.dir != "" {
		_ = os.RemoveAll(h.dir)
	}
}

func atomicWrite(path string, contents []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".openfox-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	cleanup := func(err error) error {
		_ = temp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		return cleanup(err)
	}
	if _, err := temp.Write(contents); err != nil {
		return cleanup(err)
	}
	if err := temp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// removeStaleHomes 删除超过最长 Run 时长仍存在的独立 CODEX_HOME（Daemon 异常退出的遗留，含凭据副本）。
func removeStaleHomes(root string, now time.Time) {
	RemoveStaleHomes(root, now)
}

// HomeDirPrefix 是独立 CODEX_HOME 目录名前缀（存储统计按它识别）。
const HomeDirPrefix = homeDirPrefix

// RemoveStaleHomes 删除 root 下超过 MaxRunTimeout+1h 未更新的独立 CODEX_HOME，返回删除数量与释放字节数。
// 活动 Run 的目录不会超过最长 Run 时长，因此不会被删除；符号链接与非目录项一律跳过。
func RemoveStaleHomes(root string, now time.Time) (int, int64) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0
	}
	removed, freed := 0, int64(0)
	for _, entry := range entries {
		// DirEntry.IsDir 基于 Lstat：符号链接不是目录，不会被跟随删除。
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), homeDirPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= staleHomeAfter {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, live := liveHomes.Load(path); live {
			continue
		}
		size := treeSize(path)
		if os.RemoveAll(path) == nil {
			removed++
			freed += size
		}
	}
	return removed, freed
}

// treeSize 统计目录下普通文件的字节数（不跟随符号链接）。CODEX_HOME 很小，不设遍历上限。
func treeSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type().IsRegular() {
			if info, err := entry.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// sourceCodexHome 返回用户的 CODEX_HOME：显式环境值优先，否则为 ~/.codex。
func sourceCodexHome(environment map[string]string) (string, error) {
	if value := strings.TrimSpace(environment["CODEX_HOME"]); value != "" {
		return value, nil
	}
	home := environment["HOME"]
	if home == "" {
		return "", errors.New("home directory is unknown")
	}
	return filepath.Join(home, ".codex"), nil
}
