//go:build darwin

package cliagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func snapshotFixture(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	source, dest := filepath.Join(base, "source"), filepath.Join(base, "snapshot")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	return source, dest
}

func snapshotWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func snapshotSizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if _, err := f.WriteAt([]byte{'Z'}, size-1); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected removed destination %q, got %v", path, err)
	}
}

func TestGeminiSnapshotOrdinaryAndExclusions(t *testing.T) {
	source, dest := snapshotFixture(t)
	allowed := map[string]string{
		"ordinary.txt":          "ordinary canary",
		"nested/deeper/code.go": "nested canary",
		"empty.txt":             "",
		"openfox-guide":         "ordinary name",
		"safe.key.txt":          "ordinary suffix",
	}
	for name, content := range allowed {
		snapshotWrite(t, filepath.Join(source, name), content)
	}
	excludedDirs := []string{".git", ".GeMiNi", ".agents", ".claude", ".codex", ".hermes", ".cursor", "node_modules", "__pycache__", ".ssh", ".aws", ".config", "openfox-run-123"}
	for _, name := range excludedDirs {
		snapshotWrite(t, filepath.Join(source, name, "secret.txt"), "excluded canary")
	}
	excludedFiles := []string{"GEMINI.md", ".env", ".env.local", ".ENV.example", "cert.pem", "cert.KEY", "cert.p8", "cert.pk8", "cert.p12", "cert.pfx", "cert.jks", "cert.keystore", "cert.kdbx", "nested/Gemini.md", "nested/.env.production"}
	for _, name := range excludedFiles {
		snapshotWrite(t, filepath.Join(source, name), "excluded canary")
	}
	stats, err := geminiSnapshot(context.Background(), source, dest)
	if err != nil {
		t.Fatal(err)
	}
	var wantBytes int64
	for name, want := range allowed {
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(data) != want {
			t.Fatalf("copied %s = %q, %v; want %q", name, data, err, want)
		}
		wantBytes += int64(len(want))
	}
	if stats.Files != len(allowed) || stats.Bytes != wantBytes || stats.Excluded != len(excludedDirs)+len(excludedFiles) {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	err = filepath.WalkDir(dest, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		wantMode := fs.FileMode(0600)
		if entry.IsDir() {
			wantMode = 0700
		}
		if info.Mode().Perm() != wantMode {
			return fmt.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), wantMode)
		}
		if !entry.IsDir() {
			rel, _ := filepath.Rel(dest, path)
			if _, ok := allowed[rel]; !ok {
				return fmt.Errorf("unexpected copied file: %s", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeminiSnapshotOutsideSymlinks(t *testing.T) {
	source, dest := snapshotFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	snapshotWrite(t, filepath.Join(outside, "secret.txt"), "outside canary")
	snapshotWrite(t, filepath.Join(source, "safe.txt"), "ordinary canary")
	for name, target := range map[string]string{"linked-file": filepath.Join(outside, "secret.txt"), "linked-dir": outside} {
		if err := os.Symlink(target, filepath.Join(source, name)); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := geminiSnapshot(context.Background(), source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Symlinks != 2 || stats.Files != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	for _, name := range []string{"linked-file", "linked-dir"} {
		snapshotAbsent(t, filepath.Join(dest, name))
	}
	t.Run("root symlink rejected", func(t *testing.T) {
		rootLink := filepath.Join(t.TempDir(), "root-link")
		if err := os.Symlink(source, rootLink); err != nil {
			t.Fatal(err)
		}
		linkDest := filepath.Join(t.TempDir(), "snapshot")
		if _, err := geminiSnapshot(context.Background(), rootLink, linkDest); err == nil {
			t.Fatal("source root symlink was accepted")
		}
		snapshotAbsent(t, linkDest)
	})
}

func TestGeminiSnapshotSpecialFileFailsAndCleans(t *testing.T) {
	source, dest := snapshotFixture(t)
	if err := unix.Mkfifo(filepath.Join(source, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := geminiSnapshot(context.Background(), source, dest); err == nil || !strings.Contains(err.Error(), "special file") {
		t.Fatalf("FIFO rejection = %v", err)
	}
	snapshotAbsent(t, dest)
}

func TestGeminiSnapshotByteLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   int
		size    int64
		extra   int64
		wantErr bool
	}{
		{"file exactly 8 MiB", 1, 8 << 20, 0, false},
		{"file exceeds 8 MiB by one", 1, (8 << 20) + 1, 0, true},
		{"total exactly 64 MiB", 8, 8 << 20, 0, false},
		{"total exceeds 64 MiB by one", 8, 8 << 20, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, dest := snapshotFixture(t)
			for i := 0; i < tc.files; i++ {
				snapshotSizedFile(t, filepath.Join(source, fmt.Sprintf("data-%02d", i)), tc.size)
			}
			if tc.extra > 0 {
				snapshotSizedFile(t, filepath.Join(source, "extra"), tc.extra)
			}
			stats, err := geminiSnapshot(context.Background(), source, dest)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "8 MiB per file or 64 MiB total") {
					t.Fatalf("limit rejection = %v", err)
				}
				snapshotAbsent(t, dest)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stats.Bytes != int64(tc.files)*tc.size || stats.Files != tc.files {
				t.Fatalf("unexpected stats: %+v", stats)
			}
			for i := 0; i < tc.files; i++ {
				f, err := os.Open(filepath.Join(dest, fmt.Sprintf("data-%02d", i)))
				if err != nil {
					t.Fatal(err)
				}
				info, statErr := f.Stat()
				var last [1]byte
				_, readErr := f.ReadAt(last[:], tc.size-1)
				closeErr := f.Close()
				if statErr != nil || readErr != nil || closeErr != nil || info.Size() != tc.size || last[0] != 'Z' {
					t.Fatalf("copied boundary file %d: stat=%v read=%v close=%v last=%q", i, statErr, readErr, closeErr, last)
				}
			}
		})
	}
}

// Trigger a real cancellation/deadline only after observing a nonempty output
// file. The snapshot still opens, reads and writes real files; no I/O is mocked.
type snapshotAfterWriteContext struct {
	context.Context
	path      string
	once      sync.Once
	interrupt func()
}

func (c *snapshotAfterWriteContext) Err() error {
	if info, err := os.Stat(c.path); err == nil && info.Size() > 0 {
		c.once.Do(c.interrupt)
	}
	return c.Context.Err()
}

func TestGeminiSnapshotInterruptCleansPartialOutput(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancel"
		wantErr := context.Canceled
		if timeout {
			name, wantErr = "deadline", context.DeadlineExceeded
		}
		t.Run(name, func(t *testing.T) {
			source, dest := snapshotFixture(t)
			snapshotSizedFile(t, filepath.Join(source, "data"), 8<<20)
			var base context.Context
			var cancel context.CancelFunc
			if timeout {
				base, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			} else {
				base, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			ctx := &snapshotAfterWriteContext{Context: base, path: filepath.Join(dest, "data"), interrupt: cancel}
			if timeout {
				ctx.interrupt = func() { <-base.Done() }
			}
			stats, err := geminiSnapshot(ctx, source, dest)
			if !errors.Is(err, wantErr) {
				t.Fatalf("interruption = %v, want %v", err, wantErr)
			}
			if stats.Bytes <= 0 || stats.Bytes >= 8<<20 {
				t.Fatalf("expected real partial copy before interruption: %+v", stats)
			}
			snapshotAbsent(t, dest)
		})
	}
}

func TestGeminiSnapshotDepthLimit(t *testing.T) {
	for _, depth := range []int{64, 65} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			source, dest := snapshotFixture(t)
			rel := strings.Repeat("d/", depth) + "canary"
			snapshotWrite(t, filepath.Join(source, rel), "ordinary canary")
			_, err := geminiSnapshot(context.Background(), source, dest)
			if depth == 65 {
				if err == nil || !strings.Contains(err.Error(), "depth exceeds 64") {
					t.Fatalf("depth rejection = %v", err)
				}
				snapshotAbsent(t, dest)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(dest, rel))
				if err != nil || string(data) != "ordinary canary" {
					t.Fatalf("depth 64 content = %q, %v", data, err)
				}
			}
		})
	}
}

func TestGeminiWorkspacePrivateRunOverlap(t *testing.T) {
	base := t.TempDir()
	runRoot := filepath.Join(base, "openfox-gemini-12345")
	child := filepath.Join(runRoot, "work")
	sibling := filepath.Join(base, "workspace")
	for _, path := range []string{child, sibling} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(child, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		wantErr    bool
	}{
		{"workspace contains private run", base, true},
		{"workspace equals private run", runRoot, true},
		{"workspace inside private run", child, true},
		{"resolved alias into private run", alias, true},
		{"ordinary sibling", sibling, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Match startGemini's EvalSymlinks precondition on both paths.
			work, err := filepath.EvalSymlinks(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			root, err := filepath.EvalSymlinks(runRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := geminiWorkspaceAllowed(work, root); (err != nil) != tc.wantErr {
				t.Fatalf("workspaceAllowed(%s) = %v; wantErr=%v", work, err, tc.wantErr)
			}
		})
	}
}

func TestGeminiSnapshotDirectorySymlinkSwap(t *testing.T) {
	source, _ := snapshotFixture(t)
	base := filepath.Dir(source)
	outside, changing, parked := filepath.Join(base, "outside"), filepath.Join(source, "changing"), filepath.Join(base, "parked")
	snapshotWrite(t, filepath.Join(outside, "canary"), "OUTSIDE_SECRET_CANARY")
	snapshotWrite(t, filepath.Join(changing, "canary"), "ordinary canary")
	snapshotWrite(t, filepath.Join(source, "stable"), "ordinary canary")
	stop, done, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	attackErr := make(chan error, 1)
	var swaps atomic.Int64
	go func() {
		defer close(done)
		defer func() {
			select {
			case <-started:
			default:
				close(started)
			}
		}()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := os.Rename(changing, parked); err != nil {
				attackErr <- err
				return
			}
			if err := os.Symlink(outside, changing); err != nil {
				attackErr <- err
				return
			}
			if swaps.Add(1) == 1 {
				close(started)
			}
			runtime.Gosched()
			if err := os.Remove(changing); err != nil {
				attackErr <- err
				return
			}
			if err := os.Rename(parked, changing); err != nil {
				attackErr <- err
				return
			}
			runtime.Gosched()
		}
	}()
	defer func() { close(stop); <-done }()
	<-started
	var rejected, succeeded int
	for i := 0; i < 80; i++ {
		dest := filepath.Join(base, fmt.Sprintf("snapshot-%d", i))
		_, err := geminiSnapshot(context.Background(), source, dest)
		if err != nil {
			rejected++
			snapshotAbsent(t, dest)
			continue
		}
		succeeded++
		err = filepath.WalkDir(dest, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("nonregular copied entry: %s", path)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(content) != "ordinary canary" {
				return fmt.Errorf("outside/changed content escaped into snapshot: %s = %q", path, content)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dest); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-attackErr:
		t.Fatalf("swap probe failed: %v", err)
	default:
	}
	if swaps.Load() == 0 {
		t.Fatal("swap probe made no symlink substitutions")
	}
	t.Logf("real swap probe: attempts=80 swaps=%d rejected=%d safe_successes=%d", swaps.Load(), rejected, succeeded)
}
