package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/daemon"
)

func TestLocalServiceScopedCLI(t *testing.T) {
	dir := t.TempDir()
	m := daemon.Manager{GOOS: "darwin", LaunchAgentPath: filepath.Join(dir, "auto", "test.plist"), ManualAgentPath: filepath.Join(dir, "config", "daemon-service.plist"), SocketPath: filepath.Join(dir, "config", "daemon.sock"), LogPath: filepath.Join(dir, "logs", "daemon.log")}
	config := daemon.DeviceConfig{Version: 1, CenterURL: "https://center.example/base", WSSURL: "wss://center.example/base/api/v1/device/connect", DeviceID: "d1", OrganizationID: "o1", KeyID: "key", PrivateKeyFile: filepath.Join(dir, "key"), TokenFile: filepath.Join(dir, "token"), WorkspaceRoot: filepath.Join(dir, "workspace"), CenterEpoch: 1}
	if err := daemon.WriteDeviceConfig(filepath.Join(filepath.Dir(m.SocketPath), daemon.DeviceConfigFileName), config); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "openfox")
	if err := os.WriteFile(binary, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.InstallService(binary, filepath.Join(filepath.Dir(m.SocketPath), daemon.DeviceConfigFileName)); err != nil {
		t.Fatal(err)
	}
	identity := []string{"--organization-id", "o1", "--device-id", "d1", "--center-url", config.CenterURL}
	call := func(operation string, extra ...string) (string, error) {
		var output bytes.Buffer
		args := append([]string{operation}, identity...)
		args = append(args, extra...)
		err := runLocalService(args, m, &output)
		return output.String(), err
	}
	for _, mode := range []string{"manual", "login"} {
		result, err := call("startup", "--mode", mode)
		if err != nil || !strings.Contains(result, `"startup":"`+mode+`"`) {
			t.Fatalf("mode %s: %s %v", mode, result, err)
		}
	}
	if _, err := call("service-status", "--center-url", "https://CENTER.example:443/base/"); err != nil {
		t.Fatalf("equivalent center rejected: %v", err)
	}
	for _, extra := range [][]string{{"--device-id", "remote"}, {"--organization-id", "other"}, {"--center-url", "https://center.example/other"}, {"--path", "/etc/passwd"}, {"--mode", "manual"}} {
		result, err := call("logs", extra...)
		if err == nil || result != "" {
			t.Fatalf("request accepted: %v => %s %v", extra, result, err)
		}
	}
	if _, err := call("startup", "--mode", "anything;shutdown"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := os.MkdirAll(filepath.Dir(m.LogPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.LogPath, []byte("2026/11/13 12:00:00 ERROR supervisor_runtime_start_failed token=topsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := call("logs")
	if err != nil || !strings.Contains(result, "supervisor_runtime_start_failed") || strings.Contains(result, "topsecret") {
		t.Fatalf("logs %s %v", result, err)
	}
	calls := 0
	m.RunCommand = func(context.Context, string, ...string) error {
		calls++
		return errors.New("launchctl failed with private data")
	}
	if result, err := call("service-action", "--action", "stop"); err == nil || result != "" || calls != 1 {
		t.Fatalf("false success %q %v calls=%d", result, err, calls)
	}
	m.GOOS = "linux"
	if _, err := call("service-action", "--action", "start"); err == nil || calls != 1 {
		t.Fatal("unsupported platform invoked launchctl")
	}
	m.GOOS = "darwin"
	defaultPlist, err := os.ReadFile(m.LaunchAgentPath)
	if err != nil {
		t.Fatal(err)
	}
	custom := config
	custom.OrganizationID, custom.DeviceID = "other-org", "other-device"
	customPath := filepath.Join(dir, "custom", daemon.DeviceConfigFileName)
	if err := daemon.WriteDeviceConfig(customPath, custom); err != nil {
		t.Fatal(err)
	}
	other := m
	other.SocketPath = filepath.Join(filepath.Dir(customPath), "daemon.sock")
	if err := other.InstallService(binary, customPath); err != nil {
		t.Fatal(err)
	}
	customPlist, err := os.ReadFile(m.LaunchAgentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		operation string
		extra     []string
	}{
		{"service-status", nil}, {"startup", []string{"--mode", "manual"}},
		{"logs", nil}, {"service-action", []string{"--action", "stop"}},
	} {
		t.Run("default_identity_rejects_custom_service_"+request.operation, func(t *testing.T) {
			result, err := call(request.operation, request.extra...)
			if err == nil || result != "" {
				t.Fatalf("custom registration controlled: %q %v", result, err)
			}
			if calls != 1 {
				t.Fatal("wrong registration invoked launchctl")
			}
			after, err := os.ReadFile(m.LaunchAgentPath)
			if err != nil || !bytes.Equal(after, customPlist) {
				t.Fatal("wrong registration plist changed")
			}
			if _, err := os.Stat(m.ManualAgentPath); !os.IsNotExist(err) {
				t.Fatal("wrong registration plist moved")
			}
		})
	}
	if err := daemon.WritePrivateFile(m.LaunchAgentPath, defaultPlist); err != nil {
		t.Fatal(err)
	}
	if _, err := call("service-status"); err != nil {
		t.Fatalf("restored default registration rejected: %v", err)
	}
}
