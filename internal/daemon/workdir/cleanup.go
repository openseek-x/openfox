package workdir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ManagedWorktreePrefix 是管理根下 worktree 目录名前缀（见 newManagedPath）。
const ManagedWorktreePrefix = "git-"

// ErrWorktreeDirty 表示 worktree 有未提交的改动，清理时跳过。
var ErrWorktreeDirty = errors.New("workdir: worktree has uncommitted changes")

// ManagedRoot 返回规范化后的管理根（未配置时为空）。
func (m *Manager) ManagedRoot() string {
	if m == nil {
		return ""
	}
	return m.policy.ManagedRoot
}

// PrepareDirectRun 为 workdir_isolation = DIRECT 的单次 Run 锁定仓库目录本身：不创建 worktree，
// 也不建立会话绑定（同一仓库可被多个会话轮流使用）；锁随 Release 释放。
// 目录已被其他 Run 锁定时返回 ErrPathConflict，由调用方决定排队或拒绝。
func (m *Manager) PrepareDirectRun(ctx context.Context, key BindingKey, path string) (*PreparedWorkspace, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: manager is nil", ErrInvalidRequest)
	}
	request := Request{Key: key, Mode: ModeDirect, RequestedPath: path}
	if err := request.validate(); err != nil {
		return nil, err
	}
	m.prepareMu.Lock()
	defer m.prepareMu.Unlock()
	workspace, err := m.prepareDirect(ctx, request)
	if err != nil {
		return nil, err
	}
	workspace.Binding = Binding{Key: key, Mode: ModeDirect, ConfiguredPath: path, ActualPath: workspace.Path, IsGit: workspace.IsGit}
	return &workspace, nil
}

// RemoveWorktree 删除管理根下一个已不再使用的 worktree：路径（解析符号链接后）必须是管理根的直接子目录且以
// ManagedWorktreePrefix 开头；目录正被锁定（活动 Run）时返回 ErrPathConflict，有未提交改动时返回
// ErrWorktreeDirty。删除用
// `git worktree remove --force`（分支与提交保留在仓库中），成功后解除指向它的会话绑定。
func (m *Manager) RemoveWorktree(ctx context.Context, path, repositoryRoot string) error {
	return m.CheckOrRemoveWorktree(ctx, path, repositoryRoot, true, nil)
}

// CheckOrRemoveWorktree performs the same ownership, lock and git checks for a
// preview and deletion. current runs under prepareMu to reject a reused path.
func (m *Manager) CheckOrRemoveWorktree(ctx context.Context, path, repositoryRoot string, remove bool, current func() error) error {
	return m.checkWorktree(ctx, path, repositoryRoot, remove, false, current)
}

// Automatic cleanup also preserves ignored files and lets Git veto removal.
func (m *Manager) RemoveExpiredWorktree(ctx context.Context, path, repositoryRoot string, current func() error) error {
	return m.checkWorktree(ctx, path, repositoryRoot, true, true, current)
}
func (m *Manager) checkWorktree(ctx context.Context, path, repositoryRoot string, remove, automatic bool, current func() error) error {
	if m == nil || strings.TrimSpace(m.policy.ManagedRoot) == "" {
		return ErrManagedRootRequired
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		// 符号链接或普通文件：不是本管理器创建的 worktree。
		return fmt.Errorf("%w: %s is not a managed worktree directory", ErrPathOutsideAllowed, filepath.Base(path))
	}
	resolved, err := canonicalPath(path)
	if err != nil {
		return err
	}
	root, err := canonicalPath(m.policy.ManagedRoot)
	if err != nil {
		return err
	}
	if filepath.Dir(resolved) != root || !strings.HasPrefix(filepath.Base(resolved), ManagedWorktreePrefix) {
		return fmt.Errorf("%w: worktree must be directly under the managed root", ErrPathOutsideAllowed)
	}
	repo, err := canonicalPath(repositoryRoot)
	if err != nil || repo == "" {
		return fmt.Errorf("%w: repository root is unknown", ErrRepositoryUnavailable)
	}
	m.prepareMu.Lock()
	defer m.prepareMu.Unlock()
	if m.locks.Held(resolved) {
		return fmt.Errorf("%w: worktree is in use", ErrPathConflict)
	}
	// 有未提交改动（含未跟踪且未忽略的文件）时不删：--force 会丢弃这些改动。
	// 被 .gitignore 忽略的构建产物不算改动，随 worktree 一起删除。
	args := []string{"status", "--porcelain=v1", "--untracked-files=normal"}
	if automatic {
		args = append(args, "--ignored")
	}
	status, err := m.git.Run(ctx, resolved, args...)
	if err != nil {
		return fmt.Errorf("%w: inspect worktree", ErrGitOperation)
	}
	if strings.TrimSpace(status.Stdout) != "" {
		return ErrWorktreeDirty
	}
	if automatic {
		// A clean detached HEAD can still contain the only reference to an Agent's work.
		refs, err := m.git.Run(ctx, resolved, "for-each-ref", "--count=1", "--contains=HEAD", "--format=%(refname)", "refs/heads", "refs/tags")
		if err != nil {
			return fmt.Errorf("%w: inspect reachable commits", ErrGitOperation)
		}
		if strings.TrimSpace(refs.Stdout) == "" {
			return ErrWorktreeDirty
		}
	}
	if current != nil {
		if err := current(); err != nil {
			return err
		}
	}
	if !remove {
		return nil
	}
	if err := m.repoLock(ctx, repo); err != nil {
		return err
	}
	args = []string{"worktree", "remove"}
	if !automatic {
		args = append(args, "--force")
	}
	_, gitErr := m.git.Run(ctx, repo, append(args, resolved)...)
	m.releaseRepoLock(repo)
	if gitErr != nil {
		return fmt.Errorf("%w: remove worktree", ErrGitOperation)
	}
	m.bindings.forgetPath(resolved)
	return nil
}

// Held 报告 path 是否与任一已持有的锁重叠。
func (m *LockManager) Held(path string) bool {
	if m == nil {
		return false
	}
	resolved, err := canonicalPath(path)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for heldPath := range m.byPath {
		if pathContains(heldPath, resolved) || pathContains(resolved, heldPath) {
			return true
		}
	}
	return false
}

// forgetPath 解除所有指向 path 的绑定（目录被清理后，下次 Run 重新准备而不是复用失效路径）。
func (r *BindingRegistry) forgetPath(path string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, binding := range r.byKey {
		if binding.ActualPath == path {
			delete(r.byKey, key)
			delete(r.byID, binding.ID)
		}
	}
}

// IsPathConflict 报告错误是否为目录被占用。
func IsPathConflict(err error) bool {
	return errors.Is(err, ErrPathConflict)
}
