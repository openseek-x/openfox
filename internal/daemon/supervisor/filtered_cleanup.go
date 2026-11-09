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
}

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
	rows, err := j.db.QueryContext(ctx, `SELECT w.path,w.repository_root,w.last_run_id,w.last_generation,w.last_used_at
		FROM workspaces w WHERE w.organization_id=? AND w.last_used_at<=? AND w.kind IN (`+marks+`)
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
	_ = enc.Encode([]any{org, *f.Days, f.Categories, f.Statuses})
	var clean []candidate
	walker := storage.NewWalker(time.Now())
	for _, c := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if f.MatchedRuns != nil && !f.MatchedRuns[c.Run] {
			continue
		}
		if s.runActive(c.Run) || m.CheckOrRemoveWorktree(ctx, c.Path, c.Repo, false, nil) != nil {
			continue
		}
		_ = enc.Encode(c)
		clean = append(clean, c)
		preview.Bytes += walker.Size(ctx, c.Path)
	}
	preview.PreviewID = hex.EncodeToString(hash.Sum(nil))
	preview.Worktrees = len(clean)
	if f.Action == "preview" {
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
		err := m.CheckOrRemoveWorktree(ctx, c.Path, c.Repo, true, check)
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
