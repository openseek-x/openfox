package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/daemon"
	"openfox/internal/daemon/commands"
	"openfox/internal/devices"
	"openfox/internal/runtime"
)

// 阶段 12e：心跳 capabilities 附带本机并发上限与命中受保护名单的根名；附加键不能覆盖既有键，旧行为（没有附加键）不变。
func TestCapabilityReporterPublishesAdditionalKeys(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{Capabilities: []byte(`{}`)})
	prober := &fakeProber{report: runtime.Capabilities{Version: "0.164.0", Login: runtime.LoginLoggedIn, Runnable: true}}
	reporter := &capabilityReporter{prober: prober, source: source}
	reporter.refresh(context.Background())
	read := func() map[string]json.RawMessage {
		heartbeat, err := source.Current(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(heartbeat.Capabilities, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if _, ok := read()["local_max_runs"]; ok {
		t.Fatal("legacy reporter must not publish local_max_runs")
	}
	reporter.additional = map[string]any{"local_max_runs": 3, "protected_directory_roots": []string{"home"}, "runtimes": "overridden"}
	reporter.publish()
	value := read()
	if string(value["local_max_runs"]) != "3" || string(value["protected_directory_roots"]) != `["home"]` {
		t.Fatalf("capabilities = %v", value)
	}
	if got := devices.ParseLocalMaxRuns(mustMarshal(t, value)); got == nil || *got != 3 {
		t.Fatalf("center parses local limit as %v", got)
	}
	if string(value["runtimes"]) == `"overridden"` {
		t.Fatal("additional keys must not override built-in capability keys")
	}
	reporter.setExtra("local_max_runs", nil)
	if _, ok := read()["local_max_runs"]; ok {
		t.Fatal("setExtra(nil) must remove the key")
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// 能力位：只有配置了轮换器（--config 模式的新 daemon）才注册 key_rotate；旧行为不变。
func TestKeyRotateCommandOnlyWithRotator(t *testing.T) {
	legacy := &commandHandlers{}
	if _, ok := legacy.all()[commands.KindKeyRotate]; ok {
		t.Fatal("key_rotate registered without a rotator")
	}
	calls := 0
	handlers := &commandHandlers{keyRotate: func(context.Context) (daemon.KeyRotationResult, error) {
		calls++
		if calls == 2 {
			return daemon.KeyRotationResult{}, daemon.ErrKeyRotationUnsupported
		}
		return daemon.KeyRotationResult{PreviousKeyID: "old", KeyID: "new"}, nil
	}}
	handler, ok := handlers.all()[commands.KindKeyRotate]
	if !ok {
		t.Fatal("key_rotate missing")
	}
	if _, err := handler(context.Background(), json.RawMessage(`{"x":1}`)); !errors.Is(err, commands.ErrInvalidParams) {
		t.Fatalf("unexpected params accepted: %v", err)
	}
	result, err := handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"key_id":"new"`) || strings.Contains(string(encoded), "private") {
		t.Fatalf("result = %s", encoded)
	}
	_, err = handler(context.Background(), json.RawMessage(`{}`))
	var coded *commands.CodedError
	if !errors.As(err, &coded) || coded.Code != "KEY_ROTATION_UNSUPPORTED" {
		t.Fatalf("unsupported error = %v", err)
	}
}
func TestComputerRotateRequiresRegistration(t *testing.T) {
	dir := t.TempDir()
	err := runComputerWith([]string{"rotate", "--config-dir", dir, "--offline"}, computerDeps{stdout: os.Stdout})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("rotate without registration = %v", err)
	}
}

func TestComputerStatusListsOrganizations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	config := daemon.DeviceConfig{
		Version: daemon.DeviceConfigVersion, CenterURL: "https://center.example.com", WSSURL: "wss://center.example.com/api/v1/device/connect",
		DeviceID: "device-1", OrganizationID: "org-1", KeyID: "key-1",
		PrivateKeyFile: filepath.Join(dir, daemon.DeviceKeyFileName), TokenFile: filepath.Join(dir, daemon.DeviceTokenFileName),
		WorkspaceRoot: filepath.Join(dir, "workspaces"), CenterEpoch: 1,
	}
	path := filepath.Join(dir, daemon.DeviceConfigFileName)
	if err := daemon.WriteDeviceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runComputerWith([]string{"status", "--json", "--config-dir", dir}, computerDeps{stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"organization_ids":["org-1"]`) {
		t.Fatalf("legacy status = %s", out.String())
	}
	second := "11111111-2222-3333-4444-555555555555"
	if err := daemon.WriteOrganizations(path, []string{second}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runComputerWith([]string{"status", "--json", "--config-dir", dir}, computerDeps{stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"organization_id":"org-1"`) || !strings.Contains(out.String(), second) {
		t.Fatalf("multi status = %s", out.String())
	}
}
