package supervisor

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
)

func machineProxyRunner(t *testing.T, center *fakeCenter, adapter runtime.Adapter, policy func() (map[string]string, error)) (*Supervisor, func()) {
	t.Helper()
	server := httptest.NewServer(center.handler(t))
	root := t.TempDir()
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "work"), AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := OpenJournal(context.Background(), filepath.Join(root, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(Config{Client: &Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "device-token", nil }}, Adapter: adapter, Workdir: manager, Journal: journal, Organizations: []string{testOrg}, PollInterval: time.Second, AttachmentRoot: filepath.Join(root, "attachments"), ArtifactRoot: filepath.Join(root, "artifacts"), MachineProxy: policy})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	stop := func() { cancel(); <-done; journal.Close(); server.Close() }
	return runner, stop
}

func TestMachineProxyRunSnapshotAndRedaction(t *testing.T) {
	for _, mode := range []string{"DISABLED", "CUSTOM", "DIRECT", "INVALID"} {
		t.Run(mode, func(t *testing.T) {
			center := newCenter()
			center.extend = func(r *RunContext) {
				r.Environment = []EnvironmentVariable{{Name: "HTTPS_PROXY", Value: "http://agent-private.invalid:88"}, {Name: "SSL_CERT_FILE", Value: "preserved-ca"}, {Name: "UNRELATED", Value: "kept"}}
			}
			var reads atomic.Int32
			adapter := &fakeAdapter{output: "proxy-private.invalid:18765 and http://agent-private.invalid:88"}
			var captured runtime.RunRequest
			adapter.onStart = func(r runtime.RunRequest) { captured = r }
			_, stop := machineProxyRunner(t, center, adapter, func() (map[string]string, error) {
				reads.Add(1)
				if mode == "INVALID" {
					return nil, errors.New("private-config-detail")
				}
				if mode == "DISABLED" {
					return nil, nil
				}
				env := map[string]string{}
				for _, k := range daemon.MachineProxyEnvironmentKeys {
					env[k] = ""
				}
				if mode == "CUSTOM" {
					env["HTTPS_PROXY"] = "http://proxy-private.invalid:18765"
				}
				return env, nil
			})
			defer stop()
			if mode == "INVALID" {
				waitFor(t, "prepare failure", func() bool { return len(center.snapshot().prepareFailed) > 0 })
				if adapter.startCount() != 0 || center.snapshot().starts != 0 {
					t.Fatal("bad policy allowed start")
				}
				return
			}
			waitFor(t, "result", func() bool { return len(center.snapshot().results) > 0 })
			if reads.Load() != 1 || captured.Environment["SSL_CERT_FILE"] != "preserved-ca" || captured.Environment["UNRELATED"] != "kept" {
				t.Fatal("snapshot/other env changed", reads.Load(), captured.Environment)
			}
			want := "http://agent-private.invalid:88"
			if mode == "DIRECT" {
				want = ""
			}
			if mode == "CUSTOM" {
				want = "http://proxy-private.invalid:18765"
			}
			if captured.Environment["HTTPS_PROXY"] != want {
				t.Fatal(captured.Environment)
			}
			snapshot := center.snapshot()
			if mode == "CUSTOM" && strings.Contains(snapshot.results[0].Output, "proxy-private.invalid") {
				t.Fatal("bare proxy host leaked")
			}
			for _, event := range snapshot.accepted {
				if event.Type == "WARNING" && strings.Contains(string(event.Payload), "private.invalid") {
					t.Fatal("warning leaked endpoint")
				}
			}
		})
	}
}

type proxyValidatedAdapter struct {
	*fakeAdapter
	name     string
	validate func(runtime.RunRequest) error
}

func (a *proxyValidatedAdapter) Name() string                               { return a.name }
func (a *proxyValidatedAdapter) ValidateRequest(r runtime.RunRequest) error { return a.validate(r) }
func TestMachineProxySnapshotBeforePreflightAndHermesExcluded(t *testing.T) {
	for _, name := range []string{"codebuddy", "hermes"} {
		t.Run(name, func(t *testing.T) {
			center := newCenter()
			center.runtimeName = name
			center.extend = func(r *RunContext) {
				r.Environment = []EnvironmentVariable{{Name: "HTTPS_PROXY", Value: "http://agent.invalid:9"}}
			}
			var reads, validations atomic.Int32
			var policyChanged atomic.Bool
			fake := &fakeAdapter{}
			want := "http://machine.invalid:8"
			if name == "hermes" {
				want = "http://agent.invalid:9"
			}
			a := &proxyValidatedAdapter{fakeAdapter: fake, name: name, validate: func(r runtime.RunRequest) error {
				validations.Add(1)
				if r.Environment["HTTPS_PROXY"] != want {
					t.Error("validation used different environment")
				}
				policyChanged.Store(true)
				return nil
			}}
			fake.onStart = func(r runtime.RunRequest) {
				if r.Environment["HTTPS_PROXY"] != want {
					t.Error("Start resampled policy after preparation")
				}
			}
			_, stop := machineProxyRunner(t, center, a, func() (map[string]string, error) {
				reads.Add(1)
				if policyChanged.Load() {
					return map[string]string{"HTTPS_PROXY": "http://changed.invalid:7"}, nil
				}
				return map[string]string{"HTTPS_PROXY": want}, nil
			})
			defer stop()
			waitFor(t, "result", func() bool { return len(center.snapshot().results) > 0 })
			if validations.Load() < 1 || (name == "codebuddy" && validations.Load() < 2) {
				t.Fatal("preflight/Start validation not covered")
			}
			expected := int32(1)
			if name == "hermes" {
				expected = 0
			}
			if reads.Load() != expected {
				t.Fatal("wrong policy reads", reads.Load())
			}
		})
	}
}
