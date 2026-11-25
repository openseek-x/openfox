package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"openfox/internal/daemon/storage"
	"openfox/internal/daemon/workdir"
)

type CleanupPreview struct {
	PreviewID string `json:"preview_id"`
	Worktrees int    `json:"worktrees"`
	Bytes     int64  `json:"bytes"`
	// Items 只在按会话预览时返回：逻辑目录名，不含本机路径。
	Items []CleanupItem `json:"items,omitempty"`
}

// CleanupItem 是会话预览里的一个工作目录。非 worktree（DIRECT 模式、无仓库目录）和当前不可清理的 worktree 标为不可删。
type CleanupItem struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // worktree / directory
	Bytes     int64  `json:"bytes"`
	Deletable bool   `json:"deletable"`
}

const maxCleanupItems = 20

var ErrCleanupChanged = errors.New("workspaces changed since preview")

// Scope every candidate to the command organization. Paths never leave the device.
func (s *Supervisor) FilteredCleanup(ctx context.Context, org string, f storage.CleanupFilter) (any, error) {
	if org == "" || (len(f.Statuses) > 0 && f.MatchedRuns == nil) {
		return nil, storage.ErrInvalidFilter
	}
	j, m := s.config.Journal, s.config.Workdir
	cutoff := time.Now().AddDate(0, 0, -*f.Days).UnixMilli()
	marks := strings.TrimSuffix(strings.Repeat("?,", len(f.Categories)), ",")
	args := []any{org, cutoff}
	for _, category := range f.Categories {
		args = append(args, category)
	}
	// 按会话限定：只匹配 conversation_id 精确相同的行（NULL 永不匹配）。
	conversationClause := ""
	if f.ConversationID != "" {
		conversationClause = " AND w.conversation_id=?"
		args = append(args, f.ConversationID)
	}
	rows, err := j.db.QueryContext(ctx, `SELECT w.path,w.repository_root,w.last_run_id,w.last_generation,w.last_used_at
		FROM workspaces w WHERE w.organization_id=? AND w.last_used_at<=? AND w.kind IN (`+marks+`)`+conversationClause+`
		AND NOT EXISTS (SELECT 1 FROM run_journal r WHERE r.run_id=w.last_run_id AND r.generation=w.last_generation AND r.state NOT IN ('FINISHED','ABANDONED'))
		ORDER BY w.last_used_at,w.path LIMIT 501`, args...)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		Path, Repo, Run string
		Gen, Used       int64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.Path, &c.Repo, &c.Run, &c.Gen, &c.Used); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) > cleanupWorktreeBatch {
		return nil, errors.New("too many workspaces: narrow the filter")
	}
	var preview CleanupPreview
	hash := sha256.New()
	enc := json.NewEncoder(hash)
	_ = enc.Encode([]any{org, *f.Days, f.Categories, f.Statuses, f.ConversationID})
	var clean []candidate
	sizes := map[string]int64{}
	walker := storage.NewWalker(time.Now())
	for _, c := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if f.MatchedRuns != nil && !f.MatchedRuns[c.Run] {
			continue
		}
		if s.runActive(c.Run) || m.CheckOrRemoveWorktree(ctx, j.StoragePaths.Current(c.Path), j.StoragePaths.Current(c.Repo), false, nil) != nil {
			continue
		}
		_ = enc.Encode(c)
		clean = append(clean, c)
		size := walker.Size(ctx, c.Path)
		sizes[c.Path] = size
		preview.Bytes += size
	}
	preview.PreviewID = hex.EncodeToString(hash.Sum(nil))
	preview.Worktrees = len(clean)
	if f.Action == "preview" {
		if f.ConversationID != "" {
			if preview.Items, err = s.conversationItems(ctx, org, f.ConversationID, sizes); err != nil {
				return nil, err
			}
		}
		return preview, nil
	}
	if preview.PreviewID != f.PreviewID {
		return nil, ErrCleanupChanged
	}
	var result CleanupResult
	for _, c := range clean {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		check := func() error {
			var matches bool
			err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces WHERE path=? AND organization_id=? AND last_run_id=? AND last_generation=? AND last_used_at=?)`, c.Path, org, c.Run, c.Gen, c.Used).Scan(&matches)
			if err != nil {
				return err
			}
			if !matches {
				return ErrCleanupChanged
			}
			return nil
		}
		size := storage.NewWalker(time.Now()).Size(ctx, c.Path)
		err := m.CheckOrRemoveWorktree(ctx, j.StoragePaths.Current(c.Path), j.StoragePaths.Current(c.Repo), true, check)
		switch {
		case err == nil:
			result.Removed.Worktrees++
			result.FreedBytes += size
			if _, err = j.db.ExecContext(ctx, `DELETE FROM workspaces WHERE path=? AND organization_id=? AND last_run_id=? AND last_generation=? AND last_used_at=?`, c.Path, org, c.Run, c.Gen, c.Used); err != nil {
				return result, err
			}
		case errors.Is(err, workdir.ErrWorktreeDirty):
			result.SkippedDirty++
		case workdir.IsPathConflict(err), errors.Is(err, ErrCleanupChanged):
			result.SkippedActive++
		default:
			return result, err
		}
	}
	return result, nil
}

// conversationItems 列出该会话在本机的工作目录（只含逻辑目录名）。cleanable 是本次可清理的 worktree 路径及大小。
func (s *Supervisor) conversationItems(ctx context.Context, org, conversation string, cleanable map[string]int64) ([]CleanupItem, error) {
	rows, err := s.config.Journal.db.QueryContext(ctx, `SELECT f.path, COALESCE(d.logical_directory,''),
		EXISTS(SELECT 1 FROM workspaces w WHERE w.path=f.path AND w.organization_id=f.organization_id AND w.conversation_id=f.conversation_id)
		FROM conversation_directory_facts f
		LEFT JOIN conversation_workdirs d ON d.device_id=f.device_id AND d.organization_id=f.organization_id AND d.space_id=f.space_id
		 AND d.conversation_id=f.conversation_id AND d.agent_id=f.agent_id
		WHERE f.device_id=? AND f.organization_id=? AND f.conversation_id=? ORDER BY f.path LIMIT ?`,
		s.config.DeviceID, org, conversation, maxCleanupItems)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []CleanupItem
	for rows.Next() {
		var path string
		var item CleanupItem
		var managed bool
		if err = rows.Scan(&path, &item.Name, &managed); err != nil {
			return nil, err
		}
		item.Kind = "directory"
		if managed {
			item.Kind = "worktree"
			if size, ok := cleanable[path]; ok {
				item.Bytes, item.Deletable = size, true
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
