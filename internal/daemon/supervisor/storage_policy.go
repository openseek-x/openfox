package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"openfox/internal/artifacts"
	"openfox/internal/daemon/storage"
	"openfox/internal/daemon/workdir"
	"openfox/internal/runtimepolicy"
)

type StoragePolicyResult struct {
	Version int64                       `json:"version"`
	Policy  runtimepolicy.StoragePolicy `json:"policy"`
}

func (s *Supervisor) StoragePolicy(ctx context.Context, org string, c runtimepolicy.DeviceSettingsCommand) (StoragePolicyResult, error) {
	out := StoragePolicyResult{Version: 1, Policy: runtimepolicy.DefaultStoragePolicy()}
	if org == "" {
		return out, errors.New("missing organization")
	}
	db := s.config.Journal.db
	if c.Policy == nil {
		var raw string
		err := db.QueryRowContext(ctx, "SELECT version,policy FROM storage_policies WHERE organization_id=?", org).Scan(&out.Version, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		err = json.Unmarshal([]byte(raw), &out.Policy)
		return out, err
	}
	if !c.Policy.Valid() || c.Version < 1 {
		return out, errors.New("invalid storage policy")
	}
	if c.Policy.RepoDays == 0 {
		existing, err := s.StoragePolicy(ctx, org, runtimepolicy.DeviceSettingsCommand{Storage: true})
		if err != nil {
			return out, err
		}
		c.Policy.RepoDays = existing.Policy.RepoDays
	}
	raw, err := json.Marshal(c.Policy)
	if err != nil {
		return out, err
	}
	next := int64(0)
	if c.Policy.Enabled {
		next = s.config.Journal.now().Add(time.Duration(c.Policy.IntervalHours) * time.Hour).UnixMilli()
	}
	res, err := db.ExecContext(ctx, `INSERT INTO storage_policies(organization_id,version,policy,next_check) SELECT ?,?,?,? WHERE ?=1 OR EXISTS(SELECT 1 FROM storage_policies WHERE organization_id=?)
	ON CONFLICT(organization_id) DO UPDATE SET version=excluded.version,policy=excluded.policy,next_check=excluded.next_check WHERE storage_policies.version=?`, org, c.Version+1, string(raw), next, c.Version, org, c.Version)
	if err != nil {
		return out, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return out, errors.New("storage policy changed")
	}
	return StoragePolicyResult{Version: c.Version + 1, Policy: *c.Policy}, nil
}

func (s *Supervisor) runStoragePolicies(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		runCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := s.dueStoragePolicy(runCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.config.Logger.Warn("storage_policy_check_failed", "error", err.Error())
		}
	}
}

func (s *Supervisor) dueStoragePolicy(ctx context.Context) error {
	j := s.config.Journal
	now := j.now()
	var org, raw string
	var version, due int64
	err := j.db.QueryRowContext(ctx, `SELECT organization_id,version,policy,next_check FROM storage_policies WHERE next_check>0 AND next_check<=? ORDER BY next_check LIMIT 1`, now.UnixMilli()).Scan(&org, &version, &raw, &due)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p := runtimepolicy.DefaultStoragePolicy()
	if json.Unmarshal([]byte(raw), &p) != nil || !p.Valid() || !p.Enabled {
		return errors.New("invalid stored cleanup policy")
	}
	res, err := j.db.ExecContext(ctx, "UPDATE storage_policies SET next_check=? WHERE organization_id=? AND version=? AND next_check=?", now.Add(time.Duration(p.IntervalHours)*time.Hour).UnixMilli(), org, version, due)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if err := s.config.Workdir.ExpireRepositoryCache(ctx, filepath.Join(filepath.Dir(j.Path()), "repository-cache"), org, now.AddDate(0, 0, -p.RepoDays)); err != nil {
		return err
	}
	root := s.config.Workdir.ManagedRoot()
	if root == "" {
		return nil
	}
	// Count only data inside the daemon's managed root. User repository roots
	// are outside this allocation and are never cache eviction candidates.
	const gib = int64(1 << 30)
	var used int64
	if p.MaxGiB > 0 {
		w := storage.NewWalker(time.Now())
		used = w.Size(ctx, root)
	}
	free := int64(0)
	if p.MinFreeGiB > 0 {
		v, err := (artifacts.Local{Root: root}).FreeBytes(ctx)
		if err != nil {
			return err
		}
		free = int64(v)
	}
	pressure := func() bool {
		return p.MaxGiB > 0 && used > int64(p.MaxGiB)*gib || p.MinFreeGiB > 0 && free < int64(p.MinFreeGiB)*gib
	}
	rows, err := j.db.QueryContext(ctx, `SELECT path,repository_root,last_run_id,last_generation,last_used_at FROM workspaces w
	WHERE organization_id=? AND last_used_at<=? AND NOT EXISTS (
	SELECT 1 FROM run_journal r WHERE r.run_id=w.last_run_id AND r.generation=w.last_generation AND r.state NOT IN ('FINISHED','ABANDONED'))
	ORDER BY last_used_at,path LIMIT 500`, org, now.Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	var candidates []Workspace
	for rows.Next() {
		var w Workspace
		var usedAt int64
		if err = rows.Scan(&w.Path, &w.RepositoryRoot, &w.LastRunID, &w.LastGeneration, &usedAt); err != nil {
			rows.Close()
			return err
		}
		w.LastUsedAt = time.UnixMilli(usedAt)
		candidates = append(candidates, w)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, w := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if now.Sub(w.LastUsedAt) < time.Duration(p.RetainDays)*24*time.Hour && !pressure() {
			break
		}
		if s.runActive(w.LastRunID) {
			continue
		}
		check := func() error {
			var matches bool
			err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces w JOIN storage_policies p ON p.organization_id=w.organization_id WHERE w.organization_id=? AND p.version=? AND w.path=? AND w.last_run_id=? AND w.last_generation=? AND w.last_used_at=?)`, org, version, w.Path, w.LastRunID, w.LastGeneration, w.LastUsedAt.UnixMilli()).Scan(&matches)
			if err != nil {
				return err
			}
			if !matches {
				return ErrCleanupChanged
			}
			return nil
		}
		size := storage.NewWalker(time.Now()).Size(ctx, w.Path)
		err := s.config.Workdir.RemoveExpiredWorktree(ctx, w.Path, w.RepositoryRoot, check)
		if errors.Is(err, ErrCleanupChanged) {
			return nil
		}
		if errors.Is(err, workdir.ErrWorktreeDirty) || workdir.IsPathConflict(err) {
			continue
		}
		if err != nil {
			continue
		}
		_, err = j.db.ExecContext(ctx, `DELETE FROM workspaces WHERE organization_id=? AND path=? AND last_run_id=? AND last_generation=? AND last_used_at=?`, org, w.Path, w.LastRunID, w.LastGeneration, w.LastUsedAt.UnixMilli())
		if err != nil {
			return err
		}
		used -= size
		if p.MinFreeGiB > 0 {
			v, err := (artifacts.Local{Root: root}).FreeBytes(ctx)
			if err != nil {
				return err
			}
			free = int64(v)
		}
	}
	return nil
}
