package supervisor

import (
	"context"
	"errors"
	"os"
	"time"

	"openfox/internal/daemon/storage"
	"openfox/internal/daemon/workdir"
)

const (
	// worktreeCleanupAfter：worktree 最后一次使用（Run 结束）超过该时长才可被清理。
	worktreeCleanupAfter = 24 * time.Hour
	// 单次清理最多检查的 worktree 数。
	cleanupWorktreeBatch = 500
)

// CleanupResult 是 storage_cleanup 命令的结果。
type CleanupResult struct {
	FreedBytes int64 `json:"freed_bytes"`
	Removed    struct {
		Worktrees           int   `json:"worktrees"`
		CodexHomes          int   `json:"codex_homes"`
		CodexSessionExports int   `json:"codex_session_exports"`
		JournalRows         int64 `json:"journal_rows"`
	} `json:"removed"`
	SkippedActive int `json:"skipped_active"`
	// SkippedDirty 是因有未提交改动而保留的 worktree 数（--force 会丢弃改动）。
	SkippedDirty int `json:"skipped_dirty"`
}

// CleanupStorage 按决策 3 清理本机存储：
//   - worktree：journal 登记、最后使用的 Run 已终态、不在活动集合、没有被锁定、没有未提交改动，
//     且最后使用超过 worktreeCleanupAfter；路径必须在工作区根内（解析符号链接后），
//     用 `git worktree remove --force` 删除，分支与提交保留；
//   - 过期 CODEX_HOME（CleanupHomes，与 Runtime 适配器的过期规则一致）；
//   - journal 中超过保留期的终态记录与 outbox 事件。
func (s *Supervisor) CleanupStorage(ctx context.Context) (CleanupResult, error) {
	var result CleanupResult
	journal, manager := s.config.Journal, s.config.Workdir
	now := time.Now()
	workspaces, err := journal.Workspaces(ctx, cleanupWorktreeBatch)
	if err != nil {
		return result, err
	}
	root := manager.ManagedRoot()
	for _, workspace := range workspaces {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if s.runActive(workspace.LastRunID) {
			result.SkippedActive++
			continue
		}
		state, err := journal.State(ctx, workspace.LastRunID, workspace.LastGeneration)
		if err != nil {
			return result, err
		}
		if state == JournalClaiming || state == JournalPreparing || state == JournalRunning {
			result.SkippedActive++
			continue
		}
		if now.Sub(workspace.LastUsedAt) < worktreeCleanupAfter {
			continue
		}
		if _, err := os.Lstat(workspace.Path); errors.Is(err, os.ErrNotExist) {
			// 目录已不存在（用户手动删除）：只清掉登记。
			_ = journal.DeleteWorkspace(ctx, workspace.Path)
			continue
		}
		if root == "" || !storage.Within(root, workspace.Path) {
			// 不在工作区根内的路径一律不删（防止误删）。
			s.config.Logger.Warn("supervisor_cleanup_outside_root")
			continue
		}
		walker := storage.NewWalker(now)
		size := walker.Size(ctx, workspace.Path)
		err = manager.RemoveWorktree(ctx, workspace.Path, workspace.RepositoryRoot)
		switch {
		case err == nil:
			result.Removed.Worktrees++
			result.FreedBytes += size
			_ = journal.DeleteWorkspace(ctx, workspace.Path)
		case workdir.IsPathConflict(err):
			result.SkippedActive++
		case errors.Is(err, workdir.ErrWorktreeDirty):
			result.SkippedDirty++
		default:
			s.config.Logger.Warn("supervisor_cleanup_worktree_failed", "error", err.Error())
		}
	}
	if s.config.CleanupHomes != nil {
		removed, freed := s.config.CleanupHomes(now)
		result.Removed.CodexHomes += removed
		result.FreedBytes += freed
	}
	if s.config.CleanupSessionExports != nil {
		removed, freed := s.config.CleanupSessionExports(now)
		result.Removed.CodexSessionExports = removed
		result.FreedBytes += freed
	}
	rows, err := journal.Prune(ctx)
	result.Removed.JournalRows = rows
	return result, err
}

func (s *Supervisor) runActive(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, active := s.active[runID]
	return active
}
