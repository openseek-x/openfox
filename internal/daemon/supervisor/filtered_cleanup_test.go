package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"openfox/internal/daemon/storage"
)

func TestScopedCleanupPreviewsAndRemovesOnlyConfirmedWorkspace(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	center := newCenter()
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	h := newHarnessWithRoots(t, center, &fakeAdapter{}, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	h.stop()
	ctx := context.Background()
	ws, err := h.journal.Workspaces(ctx, 10)
	if err != nil || len(ws) != 1 {
		t.Fatalf("workspaces %v %v", ws, err)
	}
	path := ws[0].Path
	days := 0
	f := storage.CleanupFilter{Action: "preview", Days: &days, Categories: []string{"task", "conversation"}}
	preview := func(org string) CleanupPreview {
		t.Helper()
		v, e := h.supervisor.FilteredCleanup(ctx, org, f)
		if e != nil {
			t.Fatal(e)
		}
		return v.(CleanupPreview)
	}
	if p := preview("other"); p.Worktrees != 0 {
		t.Fatal("cross-organization match")
	}
	if p := preview(testOrg); p.Worktrees != 1 {
		t.Fatalf("preview %+v", p)
	}
	dirty := filepath.Join(path, "local-draft.txt")
	if err = os.WriteFile(dirty, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if p := preview(testOrg); p.Worktrees != 0 {
		t.Fatal("dirty workspace eligible")
	}
	if err = os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	f.PreviewID = preview(testOrg).PreviewID
	f.Action = "clean"
	ageWorkspaces(t, h.journal)
	if _, err = h.supervisor.FilteredCleanup(ctx, testOrg, f); !errors.Is(err, ErrCleanupChanged) {
		t.Fatalf("changed preview: %v", err)
	}
	f.Action = "preview"
	f.PreviewID = ""
	f.PreviewID = preview(testOrg).PreviewID
	f.Action = "clean"
	r, err := h.supervisor.FilteredCleanup(ctx, testOrg, f)
	if err != nil || r.(CleanupResult).Removed.Worktrees != 1 {
		t.Fatalf("cleanup %v %v", r, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disposable worktree still present")
	}
}
