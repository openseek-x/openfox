package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServiceManager(t *testing.T) (Manager, string) {
	t.Helper()
	dir := t.TempDir()
	manager := Manager{GOOS: "darwin", UserID: 501, LaunchAgentPath: filepath.Join(dir, "LaunchAgents", "test.plist"),
		ManualAgentPath: filepath.Join(dir, "state", "daemon-service.plist"), SocketPath: filepath.Join(dir, "state", "daemon.sock"), LogPath: filepath.Join(dir, "logs", "daemon.log")}
	manager.RunCommand = func(context.Context, string, ...string) error { t.Fatal("unexpected service execution"); return nil }
	binary := filepath.Join(dir, "openfox")
	if err := os.WriteFile(binary, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	return manager, binary
}

func TestServiceStartupPreservesRunningJobAndRegistration(t *testing.T) {
	m, binary := testServiceManager(t)
	config := filepath.Join(filepath.Dir(m.SocketPath), "config.json")
	if err := m.InstallService(binary, config); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(m.LaunchAgentPath)
	settings, err := m.ServiceSettings()
	if err != nil || settings.Startup != "login" || !settings.KeepAlive {
		t.Fatalf("initial %+v %v", settings, err)
	}
	for _, mode := range []string{"manual", "manual", "login", "manual"} {
		settings, err = m.SetStartup(mode)
		if err != nil || settings.Startup != mode || !settings.KeepAlive {
			t.Fatalf("switch %+v %v", settings, err)
		}
		path, exists, err := m.servicePath()
		data, readErr := os.ReadFile(path)
		if err != nil || readErr != nil || !exists || !bytes.Equal(data, original) {
			t.Fatal("switch changed registration arguments or lost the service")
		}
	}
	if err := m.InstallService(binary, config); err != nil {
		t.Fatal(err)
	}
	settings, err = m.ServiceSettings()
	if err != nil || settings.Startup != "manual" {
		t.Fatalf("install reset mode: %+v %v", settings, err)
	}
	var calls []string
	m.RunCommand = func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "kickstart" {
			return errors.New("not loaded")
		}
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.HasSuffix(calls[1], m.ManualAgentPath) {
		t.Fatalf("manual start = %v", calls)
	}
}

func TestServiceStartupFailureAndConcurrentChange(t *testing.T) {
	m, binary := testServiceManager(t)
	if err := m.InstallService(binary, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetStartup("invalid"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := m.withServiceLock(func() error {
		if _, err := m.SetStartup("manual"); !errors.Is(err, ErrServiceBusy) {
			t.Fatalf("concurrent mutation: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ManualAgentPath, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetStartup("manual"); err == nil {
		t.Fatal("conflicting plist overwritten")
	}
	if _, err := os.Stat(m.LaunchAgentPath); err != nil {
		t.Fatal("original lost")
	}
	if err := os.Remove(m.ManualAgentPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m.LaunchAgentPath, m.ManualAgentPath); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetStartup("manual"); err == nil {
		t.Fatal("symlink accepted")
	}
	m.GOOS = "linux"
	settings, err := m.ServiceSettings()
	if err != nil || settings.Supported {
		t.Fatalf("unsupported platform = %+v %v", settings, err)
	}
	if _, err := m.SetStartup("login"); err == nil {
		t.Fatal("unsupported platform mutation")
	}
}

func TestServiceLogsBoundedRedactedPagination(t *testing.T) {
	m, _ := testServiceManager(t)
	if err := os.MkdirAll(filepath.Dir(m.LogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	line := "2026/11/13 12:34:56 WARN supervisor_runtime_start_failed error=Bearer-secret-token path=/private/secret user_id=private\n"
	if err := os.WriteFile(m.LogPath, []byte(strings.Repeat(line, 350)), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := m.ServiceLogs("")
	if err != nil || len(page.Lines) != 200 || page.NextCursor == nil {
		t.Fatalf("first page %+v %v", page, err)
	}
	for _, line := range page.Lines {
		if strings.Contains(line, "secret") || strings.Contains(line, "private") {
			t.Fatal("secret leaked")
		}
	}
	older, err := m.ServiceLogs(*page.NextCursor)
	if err != nil || len(older.Lines) != 150 || older.NextCursor != nil {
		t.Fatalf("older count %d cursor %v err %v", len(older.Lines), older.NextCursor, err)
	}
	if err := os.WriteFile(m.LogPath, []byte("token=secret\n-----BEGIN PRIVATE KEY-----\nsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := m.ServiceLogs(*page.NextCursor)
	if err != nil || !rotated.Reset || strings.Contains(strings.Join(rotated.Lines, ""), "secret") {
		t.Fatalf("rotation %+v %v", rotated, err)
	}
	if _, err := m.ServiceLogs("../../etc/passwd"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if err := os.Remove(m.LogPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", m.LogPath); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ServiceLogs(""); err == nil {
		t.Fatal("symlink read")
	}
}
