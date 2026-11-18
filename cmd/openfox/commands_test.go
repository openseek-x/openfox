package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/commands"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
)

type countingHeartbeat struct{ count int }

func (c *countingHeartbeat) RequestHeartbeat() { c.count++ }

func newHandlers(prober *fakeProber) (*commandHandlers, *countingHeartbeat, *daemon.StaticHeartbeatSource) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	heartbeat := &countingHeartbeat{}
	return &commandHandlers{reporter: &capabilityReporter{prober: prober, source: source}, heartbeat: heartbeat}, heartbeat, source
}

func TestRuntimeProbeCommandRefreshesCapabilities(t *testing.T) {
	prober := &fakeProber{report: runtime.Capabilities{Version: "0.1.0", Login: runtime.LoginLoggedIn, Runnable: true}, models: []codex.Model{{ID: "gpt-a"}}}
	handlers, heartbeat, source := newHandlers(prober)
	value, err := handlers.runtimeProbe(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	var result struct {
		Runtimes  map[string]runtimeCapabilities `json:"runtimes"`
		CheckedAt time.Time                      `json:"checked_at"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil || result.Runtimes["codex"].Version != "0.1.0" || result.CheckedAt.IsZero() {
		t.Fatalf("result = %s, %v", encoded, err)
	}
	if heartbeat.count != 1 || reported(t, source).Version != "0.1.0" {
		t.Fatalf("heartbeat requests = %d", heartbeat.count)
	}
	if _, err := handlers.runtimeProbe(context.Background(), json.RawMessage(`{"x":1}`)); !errors.Is(err, commands.ErrInvalidParams) {
		t.Fatalf("params error = %v", err)
	}
}

func TestRuntimeTestCommand(t *testing.T) {
	prober := &fakeProber{report: runtime.Capabilities{Login: runtime.LoginLoggedIn, Runnable: true}, models: []codex.Model{{ID: "gpt-a"}}}
	handlers, _, _ := newHandlers(prober)
	check := func(params string, wantOK, wantModel bool) runtimeTestResult {
		t.Helper()
		value, err := handlers.runtimeTest(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		result := value.(runtimeTestResult)
		if result.OK != wantOK || result.ModelAvailable != wantModel || result.Provider != "codex" || result.Message == "" {
			t.Fatalf("%s → %+v", params, result)
		}
		return result
	}
	check(`{"provider":"codex","model":"gpt-a"}`, true, true)
	check(`{"provider":"codex"}`, true, true)
	check(`{"provider":"codex","model":"gpt-x"}`, false, false)
	prober.report = runtime.Capabilities{Login: runtime.LoginLoggedOut}
	if result := check(`{"provider":"codex","model":"gpt-a"}`, false, false); result.LoginStatus != "LOGGED_OUT" || prober.listCalls != 3 {
		t.Fatalf("logged out = %+v, list calls = %d", result, prober.listCalls)
	}
	prober.detectErr = errors.New("runtime: unavailable: Codex executable not found")
	check(`{"provider":"codex"}`, false, false)
	for _, params := range []string{`{"provider":"claude"}`, `{"provider":"codex","extra":1}`, `[]`} {
		if _, err := handlers.runtimeTest(context.Background(), json.RawMessage(params)); !errors.Is(err, commands.ErrInvalidParams) {
			t.Fatalf("%s error = %v", params, err)
		}
	}
}

func TestStorageCleanupCommand(t *testing.T) {
	handlers, heartbeat, _ := newHandlers(&fakeProber{})
	handlers.cleanup = func(context.Context) (supervisor.CleanupResult, error) {
		var result supervisor.CleanupResult
		result.FreedBytes, result.Removed.Worktrees = 42, 1
		return result, nil
	}
	value, err := handlers.storageCleanup(context.Background(), nil)
	if err != nil || value.(supervisor.CleanupResult).FreedBytes != 42 || heartbeat.count != 1 {
		t.Fatalf("cleanup = %+v, %v, heartbeats = %d", value, err, heartbeat.count)
	}
	handlers.cleanup = func(context.Context) (supervisor.CleanupResult, error) {
		return supervisor.CleanupResult{}, errors.New("journal unavailable")
	}
	var coded *commands.CodedError
	if _, err := handlers.storageCleanup(context.Background(), json.RawMessage(``)); !errors.As(err, &coded) || coded.Code != "CLEANUP_FAILED" {
		t.Fatalf("cleanup error = %v", err)
	}
}

func TestRuntimeTestForOtherRuntimes(t *testing.T) {
	handlers, _, _ := newHandlers(&fakeProber{})
	claude := &fakeProber{report: runtime.Capabilities{Login: runtime.LoginUnknown, Runnable: true},
		models: []codex.Model{{ID: "default"}, {ID: "sonnet"}}}
	handlers.reporter.extras = []*namedProber{{name: "claude-code", prober: claude}}
	check := func(params string) runtimeTestResult {
		t.Helper()
		value, err := handlers.runtimeTest(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		return value.(runtimeTestResult)
	}
	if result := check(`{"provider":"claude-code","model":"sonnet"}`); !result.OK || !result.ModelAvailable || result.LoginStatus != "UNKNOWN" {
		t.Fatalf("known model = %+v", result)
	}
	if result := check(`{"provider":"claude-code","model":"nope"}`); result.OK || result.ModelAvailable {
		t.Fatalf("unknown model = %+v", result)
	}
	claude.report.Login = runtime.LoginLoggedOut
	claude.report.Runnable = false
	if result := check(`{"provider":"claude-code"}`); result.OK || result.LoginStatus != "LOGGED_OUT" {
		t.Fatalf("logged out = %+v", result)
	}
	if _, err := handlers.runtimeTest(context.Background(), json.RawMessage(`{"provider":"pi"}`)); err == nil {
		t.Fatal("a provider that is not installed on this computer must be rejected")
	}
}
