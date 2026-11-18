package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

func TestProbeRuntimeReportsUnavailableWithoutLeakingInput(t *testing.T) {
	response := probeRuntime(context.Background(), "/tmp/does-not-exist", nil, time.Second)
	if response.Capabilities.Runtime != "codex" {
		t.Fatalf("runtime = %q, want codex", response.Capabilities.Runtime)
	}
	if response.Capabilities.Runnable || response.Error == "" {
		t.Fatalf("response = %#v, want unavailable probe error", response)
	}
	if response.Capabilities.Reason == "" {
		t.Fatal("missing capability reason")
	}
	if response.Capabilities.Login != runtime.LoginUnknown {
		t.Fatalf("login = %q, want unknown", response.Capabilities.Login)
	}
}

func TestSafeRuntimeErrorFlattensAndBoundsOutput(t *testing.T) {
	message := "first\nsecond\t" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	got := safeRuntimeError(fakeError(message))
	if len(got) > 512 || got == "" {
		t.Fatalf("safeRuntimeError length/value = %d/%q", len(got), got)
	}
	if strings.ContainsAny(got, "\n\r\t") || !strings.HasPrefix(got, "first second ") {
		t.Fatalf("safeRuntimeError = %q", got)
	}
}

type fakeError string

func (e fakeError) Error() string { return string(e) }
