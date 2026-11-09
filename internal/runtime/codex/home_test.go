package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSourceAuth(t *testing.T, contents string) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, authFileName), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestCodexHomeIsPrivateAndContainsOnlyCredentialsAndConfig(t *testing.T) {
	source := writeSourceAuth(t, "token")
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("personal"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := prepareCodexHome(t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer home.remove()
	info, err := os.Stat(home.dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %v, %v", info, err)
	}
	entries, _ := os.ReadDir(home.dir)
	if len(entries) != 2 || entries[0].Name() != authFileName || entries[1].Name() != "config.toml" {
		t.Fatalf("entries = %v", entries)
	}
	for _, name := range []string{authFileName, "config.toml"} {
		if info, err := os.Stat(filepath.Join(home.dir, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", name, info, err)
		}
	}
}

func TestCodexHomeFinishWriteBack(t *testing.T) {
	t.Run("unchanged run credentials are not written back", func(t *testing.T) {
		source := writeSourceAuth(t, "token")
		home, err := prepareCodexHome(t.TempDir(), source)
		if err != nil {
			t.Fatal(err)
		}
		if wrote, err := home.finish(); wrote || err != nil {
			t.Fatalf("wrote=%v err=%v", wrote, err)
		}
		if _, err := os.Stat(home.dir); !os.IsNotExist(err) {
			t.Fatalf("home not removed: %v", err)
		}
	})
	t.Run("refreshed credentials are written back", func(t *testing.T) {
		source := writeSourceAuth(t, "token")
		home, err := prepareCodexHome(t.TempDir(), source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home.dir, authFileName), []byte("refreshed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if wrote, err := home.finish(); !wrote || err != nil {
			t.Fatalf("wrote=%v err=%v", wrote, err)
		}
		if contents, _ := os.ReadFile(filepath.Join(source, authFileName)); string(contents) != "refreshed" {
			t.Fatalf("source = %q", contents)
		}
		// 原子写回不残留临时文件。
		if entries, _ := os.ReadDir(source); len(entries) != 1 {
			t.Fatalf("source entries = %v", entries)
		}
	})
	t.Run("concurrently updated source wins", func(t *testing.T) {
		source := writeSourceAuth(t, "token")
		home, err := prepareCodexHome(t.TempDir(), source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home.dir, authFileName), []byte("refreshed-by-run"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, authFileName), []byte("refreshed-by-user"), 0o600); err != nil {
			t.Fatal(err)
		}
		if wrote, err := home.finish(); wrote || err != nil {
			t.Fatalf("wrote=%v err=%v", wrote, err)
		}
		if contents, _ := os.ReadFile(filepath.Join(source, authFileName)); string(contents) != "refreshed-by-user" {
			t.Fatalf("source = %q", contents)
		}
	})
}

func TestRemoveStaleHomes(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, homeDirPrefix+"stale")
	fresh := filepath.Join(root, homeDirPrefix+"fresh")
	other := filepath.Join(root, "unrelated")
	for _, dir := range []string{stale, fresh, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-staleHomeAfter - time.Minute)
	for _, dir := range []string{stale, other} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	removeStaleHomes(root, time.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale home kept: %v", err)
	}
	for _, dir := range []string{fresh, other} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s removed: %v", dir, err)
		}
	}
}

func TestSourceCodexHome(t *testing.T) {
	if got, _ := sourceCodexHome(map[string]string{"CODEX_HOME": "/custom", "HOME": "/h"}); got != "/custom" {
		t.Fatalf("got %s", got)
	}
	if got, _ := sourceCodexHome(map[string]string{"HOME": "/h"}); got != filepath.Join("/h", ".codex") {
		t.Fatalf("got %s", got)
	}
	if _, err := sourceCodexHome(map[string]string{}); err == nil {
		t.Fatal("expected error without HOME")
	}
}
