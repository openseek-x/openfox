package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexSessionExportCopiesOnlyRolloutAndExpiresAfterRetention(t *testing.T) {
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	home := t.TempDir()
	exportRoot := filepath.Join(t.TempDir(), "exports")
	rolloutName := "rollout-2026-11-08T12-00-00-000000000-" + sessionID + ".jsonl"
	source := filepath.Join(home, "sessions", "2026", "11", "08", rolloutName)
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := []byte("{\"type\":\"session_meta\"}\n")
	if err := os.WriteFile(source, rollout, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copySessionToExport(exportRoot, home, sessionID); err != nil {
		t.Fatal(err)
	}

	exporter := &Adapter{ExportRoot: exportRoot}
	file, err := exporter.ExportSession(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "sessions/2026/11/08/"+rolloutName || string(file.Data) != string(rollout) {
		t.Fatalf("exported session = %q, %q", file.Name, file.Data)
	}
	stagedPath := filepath.Join(exportRoot, sessionID, filepath.FromSlash(strings.TrimPrefix(file.Name, "sessions/")))
	for _, directory := range []string{exportRoot, filepath.Join(exportRoot, sessionID)} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("staged directory %q mode = %o, want 700", directory, info.Mode().Perm())
		}
	}
	info, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("staged file mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(exportRoot, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("authentication data must not be staged")
	}

	now := info.ModTime().Add(sessionExportRetention - time.Second)
	if removed, _ := RemoveStaleSessionExports(exportRoot, now); removed != 0 {
		t.Fatalf("removed before retention elapsed = %d", removed)
	}
	now = info.ModTime().Add(sessionExportRetention + time.Second)
	if removed, _ := RemoveStaleSessionExports(exportRoot, now); removed != 1 {
		t.Fatalf("removed after retention elapsed = %d, want 1", removed)
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatal("expired rollout snapshot remains on disk")
	}
}
