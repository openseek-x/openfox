package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestRunRequestNormalizeRequiresExplicitEnvironmentAllowlist(t *testing.T) {
	_, err := (RunRequest{Prompt: "work", Environment: map[string]string{"TOKEN": "secret"}}).Normalize()
	if err == nil || !strings.Contains(err.Error(), "allowlisted") {
		t.Fatalf("expected allowlist rejection, got %v", err)
	}
	normalized, err := (RunRequest{
		Prompt: "work", Environment: map[string]string{"TOKEN": "secret"}, EnvironmentAllowlist: []string{"TOKEN"},
	}).Normalize()
	if err != nil || normalized.Timeout != DefaultRunTimeout {
		t.Fatalf("expected normalized request, got %#v, %v", normalized, err)
	}
}

func TestStartProcessCancellationAndExitEvidence(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	path := writeRuntimeFixture(t, `#!/bin/sh
printf '%s\n' '{"type":"started"}'
sleep 5
`)
	exec, err := StartProcess(context.Background(), ProcessSpec{
		Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
		Timeout: 10 * time.Second, MaxLineBytes: 1024, MaxOutputBytes: 4096, MaxEvents: 10,
		DecodeLine: decodeTestEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := exec.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCancelled || !result.ProcessExited {
		t.Fatalf("expected confirmed cancellation, got %#v", result)
	}
}

func TestStartProcessTimeoutAndOutputLimit(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	path := writeRuntimeFixture(t, `#!/bin/sh
sleep 5
`)
	exec, err := StartProcess(context.Background(), ProcessSpec{
		Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
		Timeout: 20 * time.Millisecond, MaxLineBytes: 1024, MaxOutputBytes: 4096, MaxEvents: 10,
		DecodeLine: decodeTestEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exec.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusTimedOut || !result.ProcessExited || result.ErrorCode != "RUN_TIMEOUT" {
		t.Fatalf("expected timeout, got %#v", result)
	}

	large := writeRuntimeFixture(t, `#!/bin/sh
awk 'BEGIN { for (i = 0; i < 300; i++) printf "x"; print "" }'
`)
	exec, err = StartProcess(context.Background(), ProcessSpec{
		Executable: large, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
		Timeout: 5 * time.Second, MaxLineBytes: 64, MaxOutputBytes: 4096, MaxEvents: 10,
		DecodeLine: decodeTestEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = exec.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusFailed || result.ErrorCode != "OUTPUT_LIMIT" {
		t.Fatalf("expected output limit, got %#v", result)
	}
}

func TestStartProcessNonZeroExitIsFailure(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	path := writeRuntimeFixture(t, `#!/bin/sh
	printf '%s\n' "diagnostic token=$TEST_TOKEN" >&2
exit 7
`)
	exec, err := StartProcess(context.Background(), ProcessSpec{
		Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin", "TEST_TOKEN=secret"},
		// Leave enough startup margin for constrained CI/desktop hosts; this
		// test verifies exit classification, not the timeout path.
		Timeout: 5 * time.Second, MaxLineBytes: 1024, MaxOutputBytes: 4096, MaxEvents: 10,
		DecodeLine: decodeTestEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exec.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusFailed || result.ExitCode != 7 || !strings.Contains(result.Error, "[REDACTED]") {
		t.Fatalf("expected redacted non-zero failure, got %#v", result)
	}
}

func decodeTestEvent(line []byte) (Event, error) {
	var value struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &value); err != nil {
		return Event{}, ErrProtocol
	}
	if value.Type == "unknown" {
		return Event{Critical: true}, nil
	}
	return Event{Type: EventType(value.Type), Text: value.Type}, nil
}

func writeRuntimeFixture(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime-fixture.sh")
	if err := os.WriteFile(path, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRequestNormalizeAllowsMultilineTextOnly(t *testing.T) {
	if _, err := (RunRequest{Prompt: "第一段\n\n第二段\tend", Instructions: "规则\r\n一"}).Normalize(); err != nil {
		t.Fatalf("multi-line prompt rejected: %v", err)
	}
	if _, err := (RunRequest{Prompt: "bad\x00byte"}).Normalize(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("NUL prompt error = %v", err)
	}
	if _, err := (RunRequest{Prompt: "ok", Model: "a\nb"}).Normalize(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("multi-line model error = %v", err)
	}
}

// flushingSession 读一行 "done" 即结束；Flush 每次调用发出一条 step 事件，并报告固定用量。
type flushingSession struct{ flushes int }

func (s *flushingSession) Begin(Sender, func(error)) error { return nil }
func (s *flushingSession) HandleLine(line []byte, _ Sender) ([]Event, bool, error) {
	if string(line) == "done" {
		return []Event{{Type: EventCompleted}}, true, nil
	}
	return nil, false, nil
}
func (s *flushingSession) Interrupt(Sender) bool { return false }
func (s *flushingSession) Flush(time.Time) []Event {
	s.flushes++
	return []Event{{Type: EventStep, Payload: json.RawMessage(`{"schema":1}`)}}
}
func (s *flushingSession) Usage() *Usage { return &Usage{Input: 3, Output: 2} }

func TestStartProcessCallsSessionFlushAndReportsUsage(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	previous := SessionFlushInterval
	SessionFlushInterval = 20 * time.Millisecond
	t.Cleanup(func() { SessionFlushInterval = previous })
	path := writeRuntimeFixture(t, "#!/bin/sh\nsleep 0.3\necho done\n")
	session := &flushingSession{}
	exec, err := StartProcess(context.Background(), ProcessSpec{
		Executable: path, Directory: t.TempDir(), Environment: []string{"PATH=/usr/bin:/bin"},
		Timeout: 5 * time.Second, MaxLineBytes: 1024, MaxOutputBytes: 4096, MaxEvents: 1000,
		RequireCompletion: true, Session: session,
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	for event := range exec.Events() {
		if event.Type == EventStep {
			steps++
		}
	}
	result, err := exec.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusSucceeded || steps == 0 || result.Usage == nil || result.Usage.Input != 3 || result.Usage.Output != 2 {
		t.Fatalf("status=%s steps=%d usage=%+v", result.Status, steps, result.Usage)
	}
}
