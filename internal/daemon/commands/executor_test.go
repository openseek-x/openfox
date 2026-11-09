package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
)

type report struct {
	id, status, code string
	result           json.RawMessage
}

type fakeClient struct {
	mu       sync.Mutex
	pending  []supervisor.DeviceCommand
	claimErr error
	claims   []string
	reports  []report
	failOnce bool
}

func (c *fakeClient) PendingCommands(context.Context, string, int) ([]supervisor.DeviceCommand, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending
	c.pending = nil
	return pending, nil
}

func (c *fakeClient) ClaimCommand(_ context.Context, _ string, id string) (supervisor.DeviceCommand, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claims = append(c.claims, id)
	if c.claimErr != nil {
		return supervisor.DeviceCommand{}, c.claimErr
	}
	return supervisor.DeviceCommand{ID: id, Kind: kindOf(id), Status: "DELIVERED", Params: json.RawMessage(`{}`)}, nil
}

func (c *fakeClient) CommandResult(_ context.Context, _ string, id, status string, result json.RawMessage, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failOnce {
		c.failOnce = false
		return &supervisor.APIError{Status: http.StatusServiceUnavailable}
	}
	c.reports = append(c.reports, report{id: id, status: status, code: code, result: append(json.RawMessage(nil), result...)})
	return nil
}

func (c *fakeClient) snapshot() ([]string, []report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.claims...), append([]report(nil), c.reports...)
}

// kindOf 从测试命令 ID（"<kind>#n"）取 kind。
func kindOf(id string) string {
	kind, _, _ := strings.Cut(id, "#")
	return kind
}

func command(id string) supervisor.DeviceCommand {
	return supervisor.DeviceCommand{ID: id, Kind: kindOf(id), Status: "PENDING"}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func start(t *testing.T, config Config) (*Executor, func()) {
	t.Helper()
	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); executor.Run(ctx) }()
	return executor, func() { cancel(); <-done }
}

func TestExecutorClaimsRunsAndReports(t *testing.T) {
	client := &fakeClient{pending: []supervisor.DeviceCommand{command(KindRuntimeProbe + "#1")}, failOnce: true}
	_, stop := start(t, Config{Client: client, Organizations: []string{"org"}, Handlers: map[string]Handler{
		KindRuntimeProbe: func(context.Context, json.RawMessage) (any, error) { return map[string]any{"checked_at": "now"}, nil },
	}})
	defer stop()
	waitFor(t, "report", func() bool { _, reports := client.snapshot(); return len(reports) == 1 })
	_, reports := client.snapshot()
	if r := reports[0]; r.status != "SUCCEEDED" || r.code != "" || string(r.result) != `{"checked_at":"now"}` {
		t.Fatalf("report = %+v", r)
	}
}

func TestExecutorSerializesSameKindAndTimesOut(t *testing.T) {
	client := &fakeClient{pending: []supervisor.DeviceCommand{command(KindStorageCleanup + "#1")}}
	var mu sync.Mutex
	running, maxRunning := 0, 0
	release := make(chan struct{})
	executor, stop := start(t, Config{Client: client, Organizations: []string{"org"},
		Timeouts: map[string]time.Duration{KindStorageCleanup: time.Second, KindRuntimeTest: 50 * time.Millisecond},
		Handlers: map[string]Handler{
			KindStorageCleanup: func(ctx context.Context, _ json.RawMessage) (any, error) {
				mu.Lock()
				running++
				if running > maxRunning {
					maxRunning = running
				}
				mu.Unlock()
				<-release
				mu.Lock()
				running--
				mu.Unlock()
				return map[string]int{"freed_bytes": 1}, nil
			},
			KindRuntimeTest: func(ctx context.Context, _ json.RawMessage) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}})
	defer stop()
	waitFor(t, "first claim", func() bool { claims, _ := client.snapshot(); return len(claims) == 1 })
	// 同类第二条在第一条完成前不领取；不同类可并行。
	client.mu.Lock()
	client.pending = []supervisor.DeviceCommand{command(KindStorageCleanup + "#2"), command(KindRuntimeTest + "#1")}
	client.mu.Unlock()
	executor.Wake()
	waitFor(t, "timeout report", func() bool { _, reports := client.snapshot(); return len(reports) == 1 })
	claims, reports := client.snapshot()
	if len(claims) != 2 || claims[1] != KindRuntimeTest+"#1" {
		t.Fatalf("claims = %v", claims)
	}
	if r := reports[0]; r.status != "FAILED" || r.code != "TIMEOUT" || string(r.result) != "null" {
		t.Fatalf("timeout report = %+v", r)
	}
	close(release)
	waitFor(t, "cleanup report", func() bool { _, reports := client.snapshot(); return len(reports) == 2 })
	if maxRunning != 1 {
		t.Fatalf("max concurrent cleanups = %d", maxRunning)
	}
}

func TestExecutorReportsErrorsAndSkipsClaimConflicts(t *testing.T) {
	client := &fakeClient{pending: []supervisor.DeviceCommand{command(KindRuntimeProbe + "#1"), command("unknown#1")}}
	_, stop := start(t, Config{Client: client, Organizations: []string{"org"}, Handlers: map[string]Handler{
		KindRuntimeProbe: func(context.Context, json.RawMessage) (any, error) { return nil, ErrInvalidParams },
	}})
	waitFor(t, "reports", func() bool { _, reports := client.snapshot(); return len(reports) == 2 })
	stop()
	_, reports := client.snapshot()
	codes := map[string]string{}
	for _, r := range reports {
		codes[r.id] = r.status + "/" + r.code
	}
	if codes[KindRuntimeProbe+"#1"] != "FAILED/INVALID_PARAMS" || codes["unknown#1"] != "FAILED/UNSUPPORTED_KIND" {
		t.Fatalf("reports = %v", codes)
	}

	// 领取冲突（已被领取 / 过期）：不执行、不回执。
	conflict := &fakeClient{pending: []supervisor.DeviceCommand{command(KindRuntimeProbe + "#2")},
		claimErr: &supervisor.APIError{Status: http.StatusConflict, Code: "COMMAND_CLAIMED"}}
	ran := false
	_, stop = start(t, Config{Client: conflict, Organizations: []string{"org"}, Handlers: map[string]Handler{
		KindRuntimeProbe: func(context.Context, json.RawMessage) (any, error) { ran = true; return nil, nil },
	}})
	waitFor(t, "claim", func() bool { claims, _ := conflict.snapshot(); return len(claims) == 1 })
	stop()
	if _, reports := conflict.snapshot(); ran || len(reports) != 0 {
		t.Fatalf("conflicting command ran=%v reports=%v", ran, reports)
	}
}

func TestExecutorRejectsOversizedResultAndBadCodes(t *testing.T) {
	client := &fakeClient{pending: []supervisor.DeviceCommand{command(KindRuntimeProbe + "#1"), command(KindRuntimeTest + "#1")}}
	_, stop := start(t, Config{Client: client, Organizations: []string{"org"}, Handlers: map[string]Handler{
		KindRuntimeProbe: func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"blob": strings.Repeat("x", MaxResultBytes)}, nil
		},
		KindRuntimeTest: func(context.Context, json.RawMessage) (any, error) {
			return nil, &CodedError{Code: "bad code with spaces"}
		},
	}})
	waitFor(t, "reports", func() bool { _, reports := client.snapshot(); return len(reports) == 2 })
	stop()
	_, reports := client.snapshot()
	for _, r := range reports {
		switch kindOf(r.id) {
		case KindRuntimeProbe:
			if r.status != "FAILED" || r.code != "RESULT_TOO_LARGE" {
				t.Fatalf("oversized = %+v", r)
			}
		case KindRuntimeTest:
			if r.status != "FAILED" || r.code != "COMMAND_FAILED" {
				t.Fatalf("bad code = %+v", r)
			}
		}
	}
}

func TestExecutorWakeRegistersOrganization(t *testing.T) {
	client := &fakeClient{}
	executor, err := New(Config{Client: client, Handlers: map[string]Handler{KindRuntimeProbe: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	executor.HandleMessage(daemon.Message{Envelope: daemon.Envelope{Type: daemon.MessageDeviceCommand, OrganizationID: "org-2"}})
	executor.mu.Lock()
	_, ok := executor.organizations["org-2"]
	executor.mu.Unlock()
	if !ok || len(executor.wakes) != 1 {
		t.Fatalf("wake did not register organization")
	}
	if _, err := New(Config{}); err == nil || errors.Is(err, nil) {
		t.Fatal("empty config accepted")
	}
}

func TestExecutorPassesIdentityAndSkillScanResultLimit(t *testing.T) {
	big := strings.Repeat("x", 30*1024)
	client := &fakeClient{pending: []supervisor.DeviceCommand{command(KindSkillScan + "#1"), command(KindRuntimeProbe + "#1")}}
	var mu sync.Mutex
	var gotOrg, gotCommand string
	_, stop := start(t, Config{Client: client, Organizations: []string{"org"}, Handlers: map[string]Handler{
		KindSkillScan: func(ctx context.Context, _ json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			gotOrg, gotCommand, _ = FromContext(ctx)
			return map[string]any{"pad": big}, nil
		},
		// 其他 kind 仍受 16 KB 限制。
		KindRuntimeProbe: func(context.Context, json.RawMessage) (any, error) { return map[string]any{"pad": big}, nil },
	}})
	defer stop()
	waitFor(t, "reports", func() bool { _, reports := client.snapshot(); return len(reports) == 2 })
	_, reports := client.snapshot()
	byID := map[string]report{}
	for _, r := range reports {
		byID[r.id] = r
	}
	if r := byID[KindSkillScan+"#1"]; r.status != "SUCCEEDED" {
		t.Fatalf("skill_scan report = %+v", r.status)
	}
	if r := byID[KindRuntimeProbe+"#1"]; r.status != "FAILED" || r.code != "RESULT_TOO_LARGE" {
		t.Fatalf("probe report = %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotOrg != "org" || gotCommand != KindSkillScan+"#1" {
		t.Fatalf("identity = %q %q", gotOrg, gotCommand)
	}
	timeouts := DefaultTimeouts()
	if timeouts[KindSkillScan] != 30*time.Second || timeouts[KindSkillImport] != 5*time.Minute {
		t.Fatalf("timeouts = %v", timeouts)
	}
}
