package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opt-in OS test: only a unique test label and temporary files, never the
// user's io.openfox.daemon service or registered CLI.
func TestServiceStartupWithIsolatedLaunchdJob(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_LAUNCHD") != "1" {
		t.Skip("set OPENFOX_TEST_LAUNCHD=1 for the isolated macOS job")
	}
	m, binary := testServiceManager(t)
	m.UserID = os.Getuid()
	m.Label = fmt.Sprintf("io.openfox.test.startup.%d.%d", os.Getpid(), time.Now().UnixNano())
	m.RunCommand = func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	}
	pidPath := filepath.Join(filepath.Dir(binary), "pid")
	quoted := "'" + strings.ReplaceAll(pidPath, "'", "'\\''") + "'"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' \"$$\" > "+quoted+"\nexec /bin/sleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := m.InstallService(binary, ""); err != nil {
		t.Fatal(err)
	}
	loaded := false
	t.Cleanup(func() {
		if !loaded {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := m.Stop(stopCtx); err != nil {
			t.Errorf("remove isolated test job: %v", err)
		}
	})
	if err := m.Load(ctx); err != nil {
		t.Fatalf("isolated launchd bootstrap unavailable: %v", err)
	}
	loaded = true
	waitPID := func(previous int) int {
		t.Helper()
		deadline := time.Now().Add(18 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(pidPath)
			pid, _ := strconv.Atoi(string(data))
			if pid > 0 && pid != previous && syscall.Kill(pid, 0) == nil {
				return pid
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("isolated launchd job did not start/restart")
		return 0
	}
	pid := waitPID(0)
	if _, err := m.SetStartup("manual"); err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatal("startup switch stopped running job")
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	loaded = false
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	loaded = true
	pid = waitPID(pid)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	restarted := waitPID(pid)
	if _, err := m.SetStartup("login"); err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(restarted, 0) != nil {
		t.Fatal("switch back stopped restarted job")
	}
}
