package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"openfox/internal/daemon"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtime/cliagent"
)

// 心跳里每个 Runtime 带 collaboration_modes，与 approval_modes 并列；
// 探测器没有声明的只上报 FOLLOW_RUNTIME，声明了的规范化后上报；旧心跳（没有该字段）视为只有 FOLLOW_RUNTIME。
type collaborationProber struct {
	*fakeProber
	modes []string
}

func (p collaborationProber) CollaborationModes() []string { return p.modes }

func TestCapabilityReporterPublishesCollaborationModes(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{Capabilities: []byte(`{}`)})
	ready := runtime.Capabilities{Version: "1", Login: runtime.LoginLoggedIn, Runnable: true}
	plan := collaborationProber{fakeProber: &fakeProber{report: ready}, modes: []string{"PLAN", "BOGUS", "DEFAULT"}}
	reporter := &capabilityReporter{prober: &fakeProber{report: ready}, source: source,
		extras: []*namedProber{{name: "claude-code", prober: plan}}}
	reporter.refresh(context.Background())
	heartbeat, _ := source.Current(context.Background())

	var raw struct {
		Runtimes map[string]map[string]json.RawMessage `json:"runtimes"`
	}
	if err := json.Unmarshal(heartbeat.Capabilities, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw.Runtimes["claude-code"]["collaboration_modes"]) != `["FOLLOW_RUNTIME","DEFAULT","PLAN"]` ||
		string(raw.Runtimes["codex"]["collaboration_modes"]) != `["FOLLOW_RUNTIME"]` {
		t.Fatalf("heartbeat runtimes = %s", heartbeat.Capabilities)
	}
	got := map[string]string{}
	for _, item := range devices.ParseRuntimes(heartbeat.Capabilities) {
		got[item.Provider] = strings.Join(item.CollaborationModes, ",")
	}
	if got["claude-code"] != "FOLLOW_RUNTIME,DEFAULT,PLAN" || got["codex"] != "FOLLOW_RUNTIME" {
		t.Fatalf("collaboration modes = %v", got)
	}
	legacy := devices.ParseRuntimes([]byte(`{"runtimes":{"claude-code":{"runnable":true,"approval_modes":["FOLLOW_RUNTIME","READ_ONLY"]}}}`))
	if len(legacy) != 1 || strings.Join(legacy[0].CollaborationModes, ",") != "FOLLOW_RUNTIME" {
		t.Fatalf("legacy = %+v", legacy)
	}
}

// 真实的适配器经探测器接口上报的协作模式（与 Runtime 适配器实现一致）。
func TestRealAdaptersReportCollaborationModes(t *testing.T) {
	cases := map[string]capabilityProber{
		"FOLLOW_RUNTIME,DEFAULT,PLAN": cliagent.NewClaudeCode(""),
		"FOLLOW_RUNTIME":              cliagent.NewOpenCode(""),
	}
	for want, prober := range cases {
		if got := strings.Join(collaborationModesOf(prober), ","); got != want {
			t.Fatalf("%T modes = %s, want %s", prober, got, want)
		}
	}
	if got := strings.Join(collaborationModesOf(cliagent.NewPi("")), ","); got != "FOLLOW_RUNTIME" {
		t.Fatalf("pi modes = %s", got)
	}
}
