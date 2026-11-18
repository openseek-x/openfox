package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeValidationBindsComputerAndEpoch(t *testing.T) {
	envelope := Envelope{
		ProtocolMajor: 1, ProtocolMinor: 0, MessageID: "message-1",
		CenterEpoch: 7, Type: "heartbeat", ComputerID: "computer-1",
	}
	if err := envelope.Validate("computer-1", 7); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := envelope.Validate("computer-2", 7); err != ErrEnvelopeMismatch {
		t.Fatalf("computer mismatch error = %v", err)
	}
	if err := envelope.Validate("computer-1", 8); err != ErrProtocolMismatch {
		t.Fatalf("epoch mismatch error = %v", err)
	}
}

func TestControlServerUsesPrivateUnixSocket(t *testing.T) {
	socketPath := filepath.Join("/tmp", "openfox-daemon-test-"+strconv.Itoa(os.Getpid())+".sock")
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	server := NewControlServer(socketPath)
	server.Now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	server.SetRunning(3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	ready := false
	for index := 0; index < 400; index++ {
		if _, err := os.Stat(socketPath); err == nil {
			ready = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !ready {
		select {
		case err := <-done:
			if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
				t.Skipf("sandbox does not permit Unix sockets: %v", err)
			}
			t.Fatalf("daemon socket was not created: %v", err)
		default:
			t.Fatalf("daemon socket was not created")
		}
	}
	status, err := QuerySocket(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRunning || status.CenterEpoch != 3 {
		t.Fatalf("status = %+v", status)
	}
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %o, want 600", info.Mode().Perm())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("control server did not stop")
	}
}

func TestLaunchAgentPlistEscapesPaths(t *testing.T) {
	contents, err := launchAgentPlist([]string{`/tmp/openfox&daemon`, "daemon", "run", "--socket", `/tmp/socket<1>`}, "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, "/tmp/openfox&daemon") || strings.Contains(text, "/tmp/socket<1>") {
		t.Fatalf("plist did not escape paths: %s", text)
	}
	if !strings.Contains(text, "openfox&amp;daemon") || !strings.Contains(text, "socket&lt;1&gt;") {
		t.Fatalf("plist escaped paths missing: %s", text)
	}
}
