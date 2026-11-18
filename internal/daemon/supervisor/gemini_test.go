package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/workdir"
	"openfox/internal/mcpconnect"
	"openfox/internal/runtime"
	"openfox/internal/runtime/cliagent"
	"openfox/internal/runtimepolicy"
)

type geminiPreparingAdapter struct {
	*proxyValidatedAdapter
	entered chan struct{}
	err     error
}

func (a *geminiPreparingAdapter) Start(ctx context.Context, _ runtime.RunRequest) (runtime.Execution, error) {
	close(a.entered)
	if a.err != nil {
		return nil, a.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestGeminiPreparationTerminalCause(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "failure"} {
		t.Run(mode, func(t *testing.T) {
			center := newCenter()
			center.runtimeName = runtimepolicy.Gemini
			adapter := &geminiPreparingAdapter{proxyValidatedAdapter: &proxyValidatedAdapter{fakeAdapter: &fakeAdapter{}, name: runtimepolicy.Gemini, validate: func(runtime.RunRequest) error { return nil }}, entered: make(chan struct{})}
			want := "CANCELED"
			if mode == "timeout" {
				adapter.err, want = fmt.Errorf("snapshot deadline: %w", context.DeadlineExceeded), "TIMED_OUT"
			} else if mode == "failure" {
				adapter.err, want = errors.New("controlled snapshot failure"), "FAILED"
			}
			runner, stop := machineProxyRunner(t, center, adapter, func() (map[string]string, error) { return nil, nil })
			defer stop()
			select {
			case <-adapter.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("Runtime preparation did not start")
			}
			if mode == "cancel" {
				runner.HandleMessage(daemon.Message{Envelope: daemon.Envelope{Type: daemon.MessageRunCancel, OrganizationID: testOrg, RunID: testRun, DispatchGeneration: 1}})
			}
			waitFor(t, "preparation terminal result", func() bool { return len(center.snapshot().results) != 0 })
			snapshot := center.snapshot()
			if len(snapshot.results) != 1 || snapshot.results[0].Status != want || snapshot.starts != 1 || len(snapshot.prepareFailed) != 0 {
				t.Fatalf("preparation result=%+v starts=%d prepareFailed=%v", snapshot.results, snapshot.starts, snapshot.prepareFailed)
			}
		})
	}
}

func TestGeminiDoesNotBypassComputerProxy(t *testing.T) {
	for _, mode := range []string{"disabled", "direct", "proxy", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			center := newCenter()
			center.runtimeName = runtimepolicy.Gemini
			center.extend = func(r *RunContext) {
				r.Model = "fixture-model"
				r.Environment = []EnvironmentVariable{{Name: "GEMINI_API_KEY", Value: "CONTROLLED-R02-KEY"}}
			}
			fake := &fakeAdapter{}
			var reads, validations atomic.Int32
			adapter := &proxyValidatedAdapter{fakeAdapter: fake, name: runtimepolicy.Gemini, validate: func(r runtime.RunRequest) error {
				validations.Add(1)
				_, err := runtimepolicy.ResolveGeminiModel(r.Model, r.Environment)
				return err
			}}
			fake.onStart = func(r runtime.RunRequest) {
				if _, present := r.Environment["HTTPS_PROXY"]; present {
					t.Error("computer proxy keys were injected into Gemini")
				}
			}
			_, stop := machineProxyRunner(t, center, adapter, func() (map[string]string, error) {
				reads.Add(1)
				switch mode {
				case "invalid":
					return nil, errors.New("PRIVATE-PROXY-DETAIL")
				case "proxy":
					return map[string]string{"HTTPS_PROXY": "http://proxy.invalid:8080", "NO_PROXY": "localhost"}, nil
				case "direct":
					return map[string]string{"HTTPS_PROXY": "", "HTTP_PROXY": "", "NO_PROXY": ""}, nil
				default:
					return nil, nil
				}
			})
			defer stop()
			if mode == "proxy" || mode == "invalid" {
				waitFor(t, "proxy rejected", func() bool { return len(center.snapshot().prepareFailed) != 0 })
				if fake.startCount() != 0 || center.snapshot().starts != 0 || validations.Load() != 0 {
					t.Fatal("unsupported proxy passed the preparation boundary")
				}
			} else {
				waitFor(t, "direct result", func() bool { return len(center.snapshot().results) != 0 })
				if fake.startCount() != 1 || validations.Load() != 1 {
					t.Fatal("direct mode did not reach the validated Runtime")
				}
			}
			if reads.Load() != 1 {
				t.Fatal("computer proxy was not sampled exactly once")
			}
		})
	}
}

func TestGeminiPreflightBeforeSideEffects(t *testing.T) {
	for name, alter := range map[string]func(*RunContext){
		"missing key": func(r *RunContext) { r.Environment = nil },
		"MCP": func(r *RunContext) {
			r.MCPServers = []mcpconnect.RunServer{{Name: "must-not-start", Transport: "HTTP", URL: "http://127.0.0.1:1"}}
		},
		"skills":           func(r *RunContext) { r.Skills = []RunSkill{{ID: skillOne, Name: "must-not-download", Version: 1}} },
		"custom arguments": func(r *RunContext) { r.CustomArgs = []string{"--plugin-dir", "must-not-load"} },
		"protected native environment": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "GEMINI_CONFIG_DIR", Value: "must-not-read"})
		},
		"message attachments": func(r *RunContext) {
			r.Attachments = []RunAttachment{{ID: "must-not-download", SizeBytes: 1}}
		},
		"custom endpoint": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "GEMINI_BASE_URL", Value: "http://127.0.0.1:1"})
		},
		"proxy": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "HTTPS_PROXY", Value: "http://127.0.0.1:1"})
		},
		"TLS bypass": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "NODE_TLS_REJECT_UNAUTHORIZED", Value: "0"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			center := newCenter()
			center.runtimeName = "gemini"
			center.extend = func(r *RunContext) {
				r.Model = "fixture"
				r.Environment = []EnvironmentVariable{{Name: "GEMINI_API_KEY", Value: "CONTROLLED"}}
				alter(r)
			}
			handler := center.handler(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasSuffix(req.URL.Path, "/prepare-failed") && center.snapshot().starts != 0 {
					t.Error("prepare-failed was sent after Start permission")
					http.Error(w, "STATE_CONFLICT", http.StatusConflict)
					return
				}
				handler.ServeHTTP(w, req)
			}))
			defer server.Close()
			root := t.TempDir()
			marker := filepath.Join(root, "probe.marker")
			executable := filepath.Join(root, "cli")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf PROBED > "+marker+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			adapter := cliagent.NewGemini(executable)
			adapter.Environment = map[string]string{"PATH": "/usr/bin:/bin"}
			manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "workspace"), AllowNonGit: true})
			if err != nil {
				t.Fatal(err)
			}
			journal, err := OpenJournal(context.Background(), filepath.Join(root, "journal.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			runner, err := New(Config{Client: &Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "device-token", nil }}, Adapter: adapter, Workdir: manager, Journal: journal, Organizations: []string{testOrg}, PollInterval: time.Second, AttachmentRoot: filepath.Join(root, "attachments"), ArtifactRoot: filepath.Join(root, "artifacts")})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx) }()
			defer func() { cancel(); <-done }()
			waitFor(t, "configuration rejected", func() bool { return len(center.snapshot().prepareFailed) > 0 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != "PREPARE_FAILED" || snapshot.starts != 0 || len(snapshot.skillCalls) != 0 || len(snapshot.artifactUploads) != 0 {
				t.Fatalf("side effects before preflight: %+v", &snapshot)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("configuration failure invoked CLI probe")
			}
			if _, err := os.Stat(filepath.Join(root, "workspace")); !os.IsNotExist(err) {
				t.Fatal("configuration failure prepared workspace")
			}
		})
	}
}
