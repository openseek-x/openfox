package main

import (
	"context"
	"openfox/internal/daemon"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
	"testing"
)

func TestCodeBuddyCapabilityRoundTrip(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	reporter := &capabilityReporter{source: source, prober: &fakeProber{detectErr: runtime.ErrUnavailable}, extras: []*namedProber{{name: "codebuddy", prober: &fakeProber{report: runtime.Capabilities{Runtime: "codebuddy", Version: "2.184.0", Login: runtime.LoginUnknown, ProtocolReady: true, RequiresRunConfiguration: true}}}}}
	reporter.refresh(context.Background())
	heartbeat, err := source.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runtimes := devices.ParseRuntimes(heartbeat.Capabilities)
	if len(runtimes) != 2 || runtimes[0].Provider != "codebuddy" || runtimes[0].Runnable || runtimes[0].Status != devices.RuntimeUnavailable || !runtimes[0].ProtocolReady || !runtimes[0].RequiresRunConfiguration {
		t.Fatalf("device state=%+v", runtimes)
	}
	cap, ok := runtimepolicy.Find(runtimepolicy.ParseCapabilities(heartbeat.Capabilities), "codebuddy")
	if !ok || cap.Runnable || !cap.ProtocolReady || !cap.RequiresRunConfiguration {
		t.Fatalf("dispatch capability=%+v", cap)
	}
}
