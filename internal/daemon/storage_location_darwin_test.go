package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

func TestStorageMoveAndReturn(t *testing.T) {
	home, err := os.MkdirTemp("/private/tmp", "ofm-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	m, err := DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	// Only an isolated launchd label is inspected. No job is installed or stopped in launchd.
	m.Label = fmt.Sprintf("io.openfox.test.storage.%d.%d", os.Getpid(), time.Now().UnixNano())
	root := filepath.Dir(m.SocketPath)
	configPath := filepath.Join(root, DeviceConfigFileName)
	cfg := DeviceConfig{Version: 1, CenterURL: "https://example.test", WSSURL: "wss://example.test/device", DeviceID: "fixture-device", OrganizationID: "fixture-org", KeyID: "fixture", PrivateKeyFile: filepath.Join(root, "device-key"), TokenFile: filepath.Join(root, "device-token"), WorkspaceRoot: filepath.Join(root, "workspaces"), CenterEpoch: 1}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	privateKey, keyID, err := GenerateDeviceKey()
	must(err)
	cfg.KeyID = keyID
	must(WriteDeviceConfig(configPath, cfg))
	must(WriteDeviceKey(cfg.PrivateKeyFile, privateKey))
	must(WritePrivateFile(cfg.TokenFile, []byte("fixture-only")))
	must(os.MkdirAll(cfg.WorkspaceRoot, 0700))
	must(os.WriteFile(filepath.Join(cfg.WorkspaceRoot, "task"), []byte("fixture work"), 0600))
	must(m.InstallService("/bin/echo", configPath))
	m = m.ForRegistration(configPath)
	journal := filepath.Join(root, "runs.db")
	db, err := sql.Open("sqlite", journal)
	must(err)
	_, err = db.Exec("CREATE TABLE fixture (v TEXT); INSERT INTO fixture VALUES ('kept')")
	must(err)
	must(db.Close())
	before, err := os.Stat(journal)
	must(err)
	parent := filepath.Join(home, "destination")
	must(os.Mkdir(parent, 0700))
	dest := filepath.Join(parent, "OpenFox")
	must(os.Mkdir(dest, 0700))
	_, err = m.RelocateStorage(parent, cfg)
	if !errors.Is(err, ErrStorageTarget) {
		t.Fatalf("existing target: %v", err)
	}
	must(os.Remove(dest))
	if _, err := m.RelocateStorage(cfg.WorkspaceRoot, cfg); !errors.Is(err, ErrStorageTarget) {
		t.Fatalf("nested target: %v", err)
	}
	var rootDev, devFS unix.Stat_t
	if unix.Stat(root, &rootDev) == nil && unix.Stat("/dev", &devFS) == nil && rootDev.Dev != devFS.Dev {
		if _, err := m.RelocateStorage("/dev", cfg); !errors.Is(err, ErrStorageVolume) {
			t.Fatalf("cross-volume target: %v", err)
		}
	}
	identity, err := LockDeviceIdentity(root)
	must(err)
	if _, err := m.RelocateStorage(parent, cfg); !errors.Is(err, ErrStorageBusy) {
		t.Fatalf("identity change: %v", err)
	}
	must(identity.Close())
	lock, err := m.LockStorageRun()
	must(err)
	_, err = m.RelocateStorage(parent, cfg)
	if !errors.Is(err, ErrStorageBusy) {
		t.Fatalf("active run lock: %v", err)
	}
	must(lock.Close())
	job := m
	job.RunCommand = func(context.Context, string, ...string) error { return nil }
	_, err = job.RelocateStorage(parent, cfg)
	if !errors.Is(err, ErrStorageBusy) {
		t.Fatalf("loaded job: %v", err)
	}
	other := cfg
	other.DeviceID = "another"
	_, err = m.RelocateStorage(parent, other)
	if !errors.Is(err, ErrStorageBusy) {
		t.Fatalf("wrong registration: %v", err)
	}
	parent2 := filepath.Join(home, "second")
	must(os.Mkdir(parent2, 0700))
	for _, path := range []string{parent, parent, parent2, parent, filepath.Dir(root)} {
		result, err := m.RelocateStorage(path, cfg)
		must(err)
		if result != filepath.Join(path, "OpenFox") {
			t.Fatalf("returned %q", result)
		}
		physical, err := m.StorageLocation()
		must(err)
		if physical != result {
			t.Fatalf("location %q, want %q", physical, result)
		}
		after, err := os.Stat(filepath.Join(result, "runs.db"))
		must(err)
		if !os.SameFile(before, after) {
			t.Fatal("database file changed")
		}
		db, err = sql.Open("sqlite", journal)
		must(err)
		var row string
		must(db.QueryRow("SELECT v FROM fixture").Scan(&row))
		must(db.Close())
		if row != "kept" {
			t.Fatal("database contents changed")
		}
		restored, err := LoadDeviceConfig(configPath)
		must(err)
		deviceKey, err := ReadDeviceKey(restored.PrivateKeyFile)
		must(err)
		if restored.DeviceID != cfg.DeviceID || restored.KeyID != keyID || !deviceKey.Equal(privateKey) {
			t.Fatal("registered device identity changed")
		}
		key, err := readPrivateFile(cfg.TokenFile, 1024)
		must(err)
		if string(key) != "fixture-only" {
			t.Fatal("fixture token changed")
		}
		work, err := os.ReadFile(filepath.Join(cfg.WorkspaceRoot, "task"))
		must(err)
		if string(work) != "fixture work" {
			t.Fatal("workspace changed")
		}
		must(WriteDeviceConfig(configPath, cfg))
		_, err = m.SetStartup("manual")
		must(err)
		_, err = m.SetStartup("login")
		must(err)
	}
}

func TestStorageRecoveryAtAtomicSwap(t *testing.T) {
	for _, swapped := range []bool{false, true} {
		t.Run(fmt.Sprint(swapped), func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(base, "OpenFox")
			dest := filepath.Join(base, "moved")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			var st unix.Stat_t
			if err := unix.Stat(root, &st); err != nil {
				t.Fatal(err)
			}
			if err := writeStorageRecord(root, storageRecord{Inode: st.Ino, From: root, To: dest}); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dest, dest); err != nil {
				t.Fatal(err)
			}
			want := root
			if swapped {
				if err := unix.RenamexNp(root, dest, unix.RENAME_SWAP); err != nil {
					t.Fatal(err)
				}
				want = dest
			}
			actual, err := storageLocation(root)
			if err != nil || actual != want {
				t.Fatalf("recovery: %q %v", actual, err)
			}
			if !swapped {
				if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("reservation left behind")
				}
			}
		})
	}
}

func TestStorageAliasRejectsReplacementDirectory(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, first := filepath.Join(base, "OpenFox"), filepath.Join(base, "first")
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, root); err != nil {
		t.Fatal(err)
	}
	if _, err := storageLocation(root); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("unregistered alias: %v", err)
	}
	var st unix.Stat_t
	if err := unix.Stat(first, &st); err != nil {
		t.Fatal(err)
	}
	if err := writeStorageRecord(root, storageRecord{Inode: st.Ino}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(first, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	if _, err := storageLocation(root); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("missing data: %v", err)
	}
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := storageLocation(root); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("replaced data: %v", err)
	}
}
