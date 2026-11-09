package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openfox/internal/runtimepolicy"
)

func TestScheduledStoragePolicyRespectsScopeAgeVersionAndLocalFiles(t *testing.T) {
	requireGit(t)
	center := newCenter()
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/repo"}
	h := newHarnessWithRoots(t, center, &fakeAdapter{}, "", map[string]string{"team/repo": gitRepository(t)})
	waitFor(t, "finished run", func() bool { return len(center.snapshot().results) == 1 })
	h.stop()
	ctx := context.Background()
	ws, err := h.journal.Workspaces(ctx, 10)
	if err != nil || len(ws) != 1 {
		t.Fatal(ws, err)
	}
	path := ws[0].Path
	p := runtimepolicy.DefaultStoragePolicy()
	p.Enabled = true
	p.RetainDays = 1
	p.MinFreeGiB = 0
	save := func(org string, version int64) error {
		_, err := h.supervisor.StoragePolicy(ctx, org, runtimepolicy.DeviceSettingsCommand{Version: version, Policy: &p, Storage: true})
		return err
	}
	due := func(org string) {
		t.Helper()
		if _, err := h.journal.db.Exec("UPDATE storage_policies SET next_check=1 WHERE organization_id=?", org); err != nil {
			t.Fatal(err)
		}
		if err := h.supervisor.dueStoragePolicy(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := save("another", 1); err != nil {
		t.Fatal(err)
	}
	ageWorkspaces(t, h.journal)
	due("another")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("cross-organization deletion", err)
	}
	if err := save(testOrg, 1); err != nil {
		t.Fatal(err)
	}
	if err := save(testOrg, 1); err == nil {
		t.Fatal("accepted stale policy")
	}
	if _, err := h.journal.db.Exec("UPDATE workspaces SET last_used_at=?", h.journal.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	due(testOrg)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("recent workspace removed", err)
	}
	ageWorkspaces(t, h.journal)
	// A finished worktree can outlive its journal row. Only terminal rows may be pruned.
	old := h.journal.now().Add(-journalRetention - time.Hour).UnixMilli()
	if _, err := h.journal.db.Exec("UPDATE run_journal SET state='RUNNING',updated_at=?", old); err != nil {
		t.Fatal(err)
	}
	if _, err := h.journal.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	due(testOrg)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unfinished run removed", err)
	}
	if _, err := h.journal.db.Exec("UPDATE run_journal SET state='FINISHED',updated_at=?", old); err != nil {
		t.Fatal(err)
	}
	if _, err := h.journal.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err := h.journal.State(ctx, ws[0].LastRunID, ws[0].LastGeneration); err != nil || state != "" {
		t.Fatal("terminal fixture did not expire", state, err)
	}
	draft := filepath.Join(path, "local.tmp")
	if err := os.WriteFile(draft, []byte("local work"), 0600); err != nil {
		t.Fatal(err)
	}
	due(testOrg)
	if _, err := os.Stat(draft); err != nil {
		t.Fatal("local changes removed", err)
	}
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, path, "checkout", "--detach", "HEAD")
	gitOutput(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-m", "local-only")
	due(testOrg)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unreferenced commit removed", err)
	}
	gitOutput(t, path, "branch", "retained-fixture", "HEAD")
	due(testOrg)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired clean workspace retained", err)
	}
}
