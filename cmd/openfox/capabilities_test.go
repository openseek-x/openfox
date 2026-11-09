package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
)

type fakeProber struct {
	report    runtime.Capabilities
	detectErr error
	models    []codex.Model
	listErr   error
	listCalls int
}

func (f *fakeProber) Detect(context.Context) (runtime.Capabilities, error) {
	return f.report, f.detectErr
}
func (f *fakeProber) ListModels(context.Context) ([]codex.Model, error) {
	f.listCalls++
	return f.models, f.listErr
}

func reported(t *testing.T, source *daemon.StaticHeartbeatSource) runtimeCapabilities {
	t.Helper()
	heartbeat, err := source.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Runtimes map[string]runtimeCapabilities `json:"runtimes"`
	}
	if err := json.Unmarshal(heartbeat.Capabilities, &value); err != nil {
		t.Fatalf("capabilities %s: %v", heartbeat.Capabilities, err)
	}
	return value.Runtimes["codex"]
}

func TestCapabilityReporterPublishesModelsAndKeepsLastOnFailure(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{Capabilities: []byte(`{"runtime":"unknown"}`)})
	prober := &fakeProber{
		report: runtime.Capabilities{Version: "0.164.0", Login: runtime.LoginLoggedIn, Runnable: true},
		models: []codex.Model{{ID: "gpt-a", DisplayName: "GPT A", IsDefault: true, ReasoningEfforts: []string{"DEFAULT", "LOW", "HIGH"}, DefaultReasoningEffort: "LOW"}},
	}
	reporter := &capabilityReporter{prober: prober, source: source, now: func() time.Time { return time.Unix(100, 0) }}
	reporter.refresh(context.Background())
	got := reported(t, source)
	if len(got.Models) != 1 || got.Models[0].DefaultReasoningEffort != "LOW" {
		t.Fatalf("model default was not published: %+v", got.Models)
	}
	if !got.Runnable || got.Version != "0.164.0" || got.Login != "LOGGED_IN" || len(got.Models) != 1 || got.Models[0].ID != "gpt-a" || !got.Models[0].IsDefault {
		t.Fatalf("capabilities = %#v", got)
	}

	// 列表探测失败：保留上次的模型，并附上原因。
	prober.listErr = errors.New("runtime: unavailable: model/list failed")
	reporter.refresh(context.Background())
	got = reported(t, source)
	if len(got.Models) != 1 || got.ModelsError == "" || got.Models[0].DefaultReasoningEffort != "LOW" {
		t.Fatalf("capabilities after failure = %#v", got)
	}
}

func TestCapabilityReporterSkipsModelListWhenLoggedOut(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	prober := &fakeProber{report: runtime.Capabilities{Version: "0.164.0", Login: runtime.LoginLoggedOut, Reason: "Codex login is not available"}}
	(&capabilityReporter{prober: prober, source: source}).refresh(context.Background())
	got := reported(t, source)
	if prober.listCalls != 0 || got.Runnable || got.Reason == "" || got.Models == nil || len(got.Models) != 0 {
		t.Fatalf("capabilities = %#v, list calls = %d", got, prober.listCalls)
	}
}

func TestCapabilityReporterReportsDetectFailure(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	prober := &fakeProber{detectErr: errors.New("runtime: unavailable: Codex executable not found")}
	(&capabilityReporter{prober: prober, source: source}).refresh(context.Background())
	if got := reported(t, source); got.Runnable || got.Reason == "" || prober.listCalls != 0 {
		t.Fatalf("capabilities = %#v", got)
	}
}

func TestCapabilityReporterPublishesOtherRuntimesAndSkipsMissing(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{Capabilities: []byte(`{"runtime":"unknown"}`)})
	codexProber := &fakeProber{report: runtime.Capabilities{Version: "0.164.0", Login: runtime.LoginLoggedIn, Runnable: true}}
	claude := &fakeProber{report: runtime.Capabilities{Version: "2.1.0", Login: runtime.LoginUnknown, Runnable: true}, models: []codex.Model{{ID: "default", IsDefault: true}, {ID: "sonnet"}}}
	opencode := &fakeProber{detectErr: errors.New("runtime: unavailable: OpenCode executable not found")}
	reporter := &capabilityReporter{prober: codexProber, source: source, extras: []*namedProber{
		{name: "claude-code", prober: claude}, {name: "opencode", prober: opencode},
	}}
	reporter.refresh(context.Background())
	heartbeat, _ := source.Current(context.Background())
	runtimes := devices.ParseRuntimes(heartbeat.Capabilities)
	if len(runtimes) != 2 || runtimes[0].Provider != "claude-code" || runtimes[1].Provider != "codex" {
		t.Fatalf("runtimes = %+v", runtimes)
	}
	// 登录未知但可运行：READY，并带模型。
	if runtimes[0].Status != devices.RuntimeReady || len(runtimes[0].Models) != 2 || claude.listCalls != 1 {
		t.Fatalf("claude = %+v list calls %d", runtimes[0], claude.listCalls)
	}
	if opencode.listCalls != 0 {
		t.Fatal("a missing runtime must not list models")
	}
	if _, ok := reporter.proberFor("pi"); ok {
		t.Fatal("unknown provider must not resolve")
	}
	if prober, ok := reporter.proberFor("claude-code"); !ok || prober != capabilityProber(claude) {
		t.Fatal("claude-code prober not resolved")
	}
}

// 心跳里每个 Runtime 带 approval_modes；探测器没有声明的只上报 FOLLOW_RUNTIME，
// 声明了的按严格程度规范化后上报；服务端 ParseRuntimes 能解析，旧心跳（没有该字段）视为只有 FOLLOW_RUNTIME。
type modesProber struct {
	*fakeProber
	modes []string
}

func (p modesProber) ApprovalModes() []string { return p.modes }

func TestCapabilityReporterPublishesApprovalModes(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{Capabilities: []byte(`{}`)})
	full := modesProber{fakeProber: &fakeProber{report: runtime.Capabilities{Version: "1", Login: runtime.LoginLoggedIn, Runnable: true}},
		modes: []string{"FULL_ACCESS", "READ_ONLY", "WORKSPACE_WRITE"}}
	plain := &fakeProber{report: runtime.Capabilities{Version: "1", Login: runtime.LoginLoggedIn, Runnable: true}}
	reporter := &capabilityReporter{prober: full, source: source, extras: []*namedProber{{name: "pi", prober: plain}}}
	reporter.refresh(context.Background())
	heartbeat, _ := source.Current(context.Background())
	got := map[string]string{}
	for _, item := range devices.ParseRuntimes(heartbeat.Capabilities) {
		got[item.Provider] = strings.Join(item.ApprovalModes, ",")
	}
	if got["codex"] != "FOLLOW_RUNTIME,READ_ONLY,WORKSPACE_WRITE,FULL_ACCESS" || got["pi"] != "FOLLOW_RUNTIME" {
		t.Fatalf("approval modes = %v", got)
	}
	// 旧版后台服务的心跳没有 approval_modes。
	legacy := devices.ParseRuntimes([]byte(`{"runtimes":{"codex":{"runnable":true}}}`))
	if len(legacy) != 1 || strings.Join(legacy[0].ApprovalModes, ",") != "FOLLOW_RUNTIME" {
		t.Fatalf("legacy = %+v", legacy)
	}
}
