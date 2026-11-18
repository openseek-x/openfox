package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"openfox/internal/daemon"
	runtimepkg "openfox/internal/runtime"
	"openfox/internal/runtime/codex"
)

func TestLocalMachineProxyCLI(t *testing.T) {
	dir := t.TempDir()
	m := daemon.Manager{GOOS: "darwin", SocketPath: filepath.Join(dir, "daemon.sock")}
	config := daemon.DeviceConfig{Version: 1, CenterURL: "https://center.example", WSSURL: "wss://center.example/api/v1/device/connect", OrganizationID: "org", DeviceID: "device", KeyID: "key", PrivateKeyFile: filepath.Join(dir, "key"), TokenFile: filepath.Join(dir, "token"), WorkspaceRoot: filepath.Join(dir, "work"), CenterEpoch: 1}
	if err := daemon.WriteDeviceConfig(filepath.Join(dir, daemon.DeviceConfigFileName), config); err != nil {
		t.Fatal(err)
	}
	call := func(op, payload string) (daemon.MachineProxyView, error) {
		var out bytes.Buffer
		err := runLocalServiceInput([]string{op, "--organization-id", "org", "--device-id", "device", "--center-url", config.CenterURL}, m, strings.NewReader(payload), &out)
		var v daemon.MachineProxyView
		if err == nil {
			err = json.Unmarshal(out.Bytes(), &v)
		} else if out.Len() != 0 {
			t.Fatal("failed operation returned data")
		}
		return v, err
	}
	if runtime.GOOS == "darwin" {
		m.GOOS = ""
	} // DefaultManager leaves GOOS unset.
	initial, err := call("proxy-status", "")
	if err != nil || initial.Mode != "DISABLED" || initial.Revision != 0 {
		t.Fatal(initial, err)
	}
	body, _ := json.Marshal(daemon.MachineProxySave{ScopeToken: initial.ScopeToken, Mode: "CUSTOM", Effective: &daemon.MachineProxyEffective{Mode: "proxy", Protocol: "http", Host: "proxy.invalid", Port: 12345}})
	saved, err := call("proxy-save", string(body))
	if err != nil || saved.Revision != 1 {
		t.Fatal(saved, err)
	}
	for _, payload := range []string{string(body) + "{}", strings.Repeat(" ", daemon.MaxMachineProxyBytes+1), `{"mode":"DISABLED","path":"/etc/passwd"}`, `null`} {
		if _, err = call("proxy-save", payload); err == nil {
			t.Fatal("invalid payload accepted")
		}
	}
	again, err := call("proxy-status", "")
	if err != nil || again.Revision != 1 || again.Effective.Host != "proxy.invalid" {
		t.Fatal("failed write modified policy", again, err)
	}
	config.KeyID = "new-registration"
	if err = daemon.WriteDeviceConfig(filepath.Join(dir, daemon.DeviceConfigFileName), config); err != nil {
		t.Fatal(err)
	}
	changed, err := call("proxy-status", "")
	if err != nil || changed.Effective != nil || changed.Problem != "machine_proxy_registration_changed" {
		t.Fatal("old endpoint exposed", changed, err)
	}
	if _, err = call("proxy-save", string(body)); err == nil {
		t.Fatal("stale scope accepted")
	}
}

func TestConfiguredCodexConsumersPreserveUnknownAuthentication(t *testing.T) {
	prober := &fakeProber{report: runtimepkg.Capabilities{Login: runtimepkg.LoginUnknown, Runnable: true, Reason: "authentication is verified when a request is sent"}, models: []codex.Model{{ID: "controlled"}}}
	handlers, _, source := newHandlers(prober)
	handlers.reporter.refresh(context.Background())
	got := reported(t, source)
	if !got.Runnable || got.Login != "UNKNOWN" || len(got.Models) != 1 {
		t.Fatal(got)
	}
	result, err := handlers.runtimeTest(context.Background(), json.RawMessage(`{"provider":"codex","model":"controlled"}`))
	if err != nil {
		t.Fatal(err)
	}
	tested := result.(runtimeTestResult)
	if !tested.OK || tested.LoginStatus != "UNKNOWN" || !strings.Contains(tested.Message, "authentication is verified when") {
		t.Fatal(tested)
	}
	prober.listErr = errors.New("controlled 401")
	result, err = handlers.runtimeTest(context.Background(), json.RawMessage(`{"provider":"codex"}`))
	if err != nil {
		t.Fatal(err)
	}
	tested = result.(runtimeTestResult)
	if tested.OK || tested.LoginStatus != "UNKNOWN" || strings.Contains(tested.Message, "logged in") {
		t.Fatal(tested)
	}
	prober.report.Runnable = false
	prober.report.Reason = "Codex version is not in the supported version matrix"
	calls := prober.listCalls
	handlers.reporter.refresh(context.Background())
	result, _ = handlers.runtimeTest(context.Background(), json.RawMessage(`{"provider":"codex"}`))
	if result.(runtimeTestResult).OK || prober.listCalls != calls {
		t.Fatal("unsupported version attempted model probe")
	}
}
