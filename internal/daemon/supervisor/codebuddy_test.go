package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon/workdir"
	"openfox/internal/mcpconnect"
	"openfox/internal/runtime/cliagent"
)

func TestCodeBuddyPreflightBeforeSideEffects(t *testing.T) {
	for name, alter := range map[string]func(*RunContext){
		"missing key": func(r *RunContext) { r.Environment = nil },
		"MCP": func(r *RunContext) {
			r.MCPServers = []mcpconnect.RunServer{{Name: "must-not-start", Transport: "HTTP", URL: "http://127.0.0.1:1"}}
		},
		"skills":           func(r *RunContext) { r.Skills = []RunSkill{{ID: skillOne, Name: "must-not-download", Version: 1}} },
		"custom arguments": func(r *RunContext) { r.CustomArgs = []string{"--plugin-dir", "must-not-load"} },
		"protected native environment": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "CODEBUDDY_CONFIG_DIR", Value: "must-not-read"})
		},
		"missing absolute CA": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "SSL_CERT_FILE", Value: filepath.Join(t.TempDir(), "missing.pem")})
		},
		"missing relative CA after workspace": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "SSL_CERT_FILE", Value: "missing-run-ca.pem"})
		},
		"TLS bypass": func(r *RunContext) {
			r.Environment = append(r.Environment, EnvironmentVariable{Name: "NODE_TLS_REJECT_UNAUTHORIZED", Value: "0"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			center := newCenter()
			center.runtimeName = "codebuddy"
			center.extend = func(r *RunContext) {
				r.Model = "fixture"
				r.Environment = []EnvironmentVariable{{Name: "CODEBUDDY_API_KEY", Value: "CONTROLLED"}, {Name: "CODEBUDDY_BASE_URL", Value: "http://127.0.0.1:1/v1"}}
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
			adapter := cliagent.NewCodeBuddy(executable)
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
			if _, err := os.Stat(filepath.Join(root, "workspace")); name != "missing relative CA after workspace" && !os.IsNotExist(err) {
				t.Fatal("configuration failure prepared workspace")
			}
		})
	}
}
