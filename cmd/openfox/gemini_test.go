package main

import (
	"context"
	"openfox/internal/daemon"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
	"testing"
)

func TestGeminiCapabilityRoundTrip(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	reporter := &capabilityReporter{source: source, prober: &fakeProber{detectErr: runtime.ErrUnavailable}, extras: []*namedProber{{name: "gemini", prober: &fakeProber{report: runtime.Capabilities{Runtime: "gemini", Version: "0.67.0", Login: runtime.LoginUnknown, ProtocolReady: true, RequiresRunConfiguration: true}}}}}
	reporter.refresh(context.Background())
	heartbeat, err := source.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtimes := devices.ParseRuntimes(heartbeat.Capabilities)
	if len(runtimes) != 2 || runtimes[1].Provider != "gemini" || runtimes[1].Runnable || runtimes[1].Status != devices.RuntimeUnavailable || !runtimes[1].ProtocolReady || !runtimes[1].RequiresRunConfiguration {
		t.Fatalf("device state=%+v", runtimes)
	}
	cap, ok := runtimepolicy.Find(runtimepolicy.ParseCapabilities(heartbeat.Capabilities), "gemini")
	if !ok || cap.Runnable || !cap.ProtocolReady || !cap.RequiresRunConfiguration {
		t.Fatalf("dispatch capability=%+v", cap)
	}
}
