package cliconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreRoundTripWithPrivatePermissions(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "cli")}
	if file, err := store.Load(); err != nil || len(file.Profiles) != 0 {
		t.Fatalf("empty load = %#v, %v", file, err)
	}
	if err := store.WriteToken("work", "ofx_pat_secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(File{Current: "work", Profiles: map[string]Profile{"work": {Server: "https://x.example", Org: "o"}}}); err != nil {
		t.Fatal(err)
	}
	for path, perm := range map[string]os.FileMode{store.Dir: 0o700, filepath.Join(store.Dir, "tokens"): 0o700,
		filepath.Join(store.Dir, "tokens", "work"): 0o600, filepath.Join(store.Dir, "profiles.json"): 0o600} {
		if info, err := os.Lstat(path); err != nil || info.Mode().Perm() != perm {
			t.Fatalf("%s = %v, %v", path, info, err)
		}
	}
	file, err := store.Load()
	if err != nil || file.Current != "work" || file.Profiles["work"].Org != "o" {
		t.Fatalf("load = %#v, %v", file, err)
	}
	if token, err := store.ReadToken("work"); err != nil || token != "ofx_pat_secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if err := store.DeleteToken("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadToken("work"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("after delete: %v", err)
	}
	if err := store.DeleteToken("work"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	// 没有遗留临时文件。
	entries, _ := os.ReadDir(store.Dir)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temp file left: %s", entry.Name())
		}
	}
}

func TestStoreTightensLooseDirectoryAndRefusesLooseTokenFile(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "cli")}
	if err := os.MkdirAll(filepath.Join(store.Dir, "tokens"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteToken("p", "ofx_pat_x"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(store.Dir, "tokens")); info.Mode().Perm() != 0o700 {
		t.Fatalf("tokens dir mode = %o", info.Mode().Perm())
	}
	if err := os.Chmod(filepath.Join(store.Dir, "tokens", "p"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadToken("p"); err == nil || !strings.Contains(err.Error(), "chmod 600") || strings.Contains(err.Error(), "ofx_pat_x") {
		t.Fatalf("loose token file accepted: %v", err)
	}
}

func TestStoreRefusesSymbolicLinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	// 配置目录本身是符号链接。
	link := filepath.Join(root, "cli")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := (Store{Dir: link}).Save(File{}); err == nil {
		t.Fatal("saved through a symlinked directory")
	}
	// tokens 子目录是符号链接。
	dir := filepath.Join(root, "real")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "tokens")); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Dir: dir}).WriteToken("p", "ofx_pat_x"); err == nil {
		t.Fatal("wrote a token through a symlinked tokens directory")
	}
	// 目标文件是符号链接：写入与读取都拒绝，且不会写到链接指向的位置。
	other := Store{Dir: filepath.Join(root, "other")}
	if err := other.WriteToken("p", "ofx_pat_x"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "victim")
	tokenPath := filepath.Join(other.Dir, "tokens", "p")
	_ = os.Remove(tokenPath)
	if err := os.Symlink(target, tokenPath); err != nil {
		t.Fatal(err)
	}
	if err := other.WriteToken("p", "ofx_pat_y"); err == nil {
		t.Fatal("overwrote a symlinked token file")
	}
	if _, err := other.ReadToken("p"); err == nil || errors.Is(err, ErrNoToken) {
		t.Fatalf("read through symlink: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}
}

func TestValidateProfileName(t *testing.T) {
	for _, ok := range []string{"default", "work-2", "A_b"} {
		if err := ValidateProfileName(ok); err != nil {
			t.Fatalf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", ".hidden", "a b", strings.Repeat("a", 65)} {
		if err := ValidateProfileName(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
