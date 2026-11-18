package cliagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func codeBuddyRequest() runtime.RunRequest {
	return runtime.RunRequest{Prompt: "read the fixture", Model: "fixture-model", EnvironmentAllowlist: []string{"CODEBUDDY_API_KEY", "CODEBUDDY_BASE_URL"}, Environment: map[string]string{
		"CODEBUDDY_API_KEY": "fixture-secret-unique", "CODEBUDDY_BASE_URL": "http://127.0.0.1:1234/v1",
	}}
}

func TestCodeBuddyParserAndInvocation(t *testing.T) {
	a := NewCodeBuddy("")
	// This fixture tests the adapter process lifecycle, not native platform detection.
	a.proto.probe = func(context.Context, *Adapter, string) (string, error) { return "CodeBuddy 2.184.0", nil }
	f := newFixture(t, a)
	usage := `"_meta":{"codebuddy.ai/turnUsage":{"version":1,"scope":"turn","total":{"usage":{"input_tokens":7,"output_tokens":3}}}}`
	f.set(scenario{Stdout: []string{
		`{"type":"system","subtype":"init","session_id":"codebuddy-1"}`,
		`{"type":"system","subtype":"usage_update",` + usage + `}`,
		`{"type":"system","subtype":"usage_update",` + usage + `}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"read1","name":"Read","input":{"file_path":"/work/readme.txt"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"read1","content":"fixture"}]}}`,
		`{"type":"result","subtype":"success","result":"ok fixture-secret-unique","usage":{"input_tokens":9999},` + usage + `}`,
	}})
	req := codeBuddyRequest()
	req.CollaborationMode = "PLAN"
	req.Instructions = "Be brief."
	out := f.run(req)
	if out.result.Status != runtime.StatusSucceeded || out.result.Usage == nil || out.result.Usage.Input != 7 || out.result.Usage.Output != 3 {
		t.Fatalf("result=%+v", out.result)
	}
	if strings.Contains(out.result.Output, "fixture-secret-unique") {
		t.Fatal("secret leaked")
	}
	if step, ok := out.finalStep("read1"); !ok || step.State != runtime.StepDone || step.Kind != runtime.StepKindRead {
		t.Fatalf("read step=%+v", step)
	}
	rec := f.record()
	args := strings.Join(rec.Args, " ")
	for _, want := range []string{"--permission-mode plan", "--setting-sources user", "--strict-mcp-config", "--tools Read,Glob,Grep", "--model fixture-model"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "--resume") || strings.Contains(args, "fixture-secret-unique") {
		t.Fatal("resume or key passed in argv")
	}
	if rec.Stdin != "Be brief.\n\nread the fixture" {
		t.Fatalf("stdin=%q", rec.Stdin)
	}
	if _, err := os.Stat(rec.Home); !os.IsNotExist(err) {
		t.Fatalf("temporary HOME was not removed: %v", err)
	}
	cap, err := a.Detect(context.Background())
	if err != nil || cap.Runnable || !cap.ProtocolReady || !cap.RequiresRunConfiguration || cap.SupportsResume || cap.Login != runtime.LoginUnknown {
		t.Fatalf("capability=%+v err=%v", cap, err)
	}
}

func TestCodeBuddyRejectsBeforeProbe(t *testing.T) {
	a := NewCodeBuddy("")
	calls := 0
	a.proto.probe = func(context.Context, *Adapter, string) (string, error) { calls++; return "2.184.0", nil }
	for _, alter := range []func(*runtime.RunRequest){
		func(r *runtime.RunRequest) { r.ApprovalMode = "FULL_ACCESS" },
		func(r *runtime.RunRequest) { r.Args = []string{"--plugin-dir", "x"} },
		func(r *runtime.RunRequest) { r.ResumeSession = "previous" },
		func(r *runtime.RunRequest) { r.Environment["CODEBUDDY_AUTH_TOKEN"] = "token" },
		func(r *runtime.RunRequest) { r.Environment["CODEBUDDY_API_KEY"] = "" },
		func(r *runtime.RunRequest) { r.Environment["HOME"] = "/outside" },
	} {
		req := codeBuddyRequest()
		alter(&req)
		if _, err := a.Start(context.Background(), req); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if calls != 0 {
		t.Fatalf("invalid request started %d probes", calls)
	}
}

func TestCodeBuddyErrorsAndMissingUsage(t *testing.T) {
	for _, line := range []string{
		`{"type":"result","subtype":"success","is_error":true,"errors":["synthetic unauthorized"]}`,
		`{"type":"result","subtype":"error_during_execution","errors":["synthetic unauthorized"]}`,
		`{"type":"error","error":"synthetic unauthorized"}`,
	} {
		s := newSession(codeBuddyProtocol(), "", func(v string) string { return v })
		events, done := parseCodeBuddy(s, []byte(line))
		if !done || events[len(events)-1].Type != runtime.EventFailed {
			t.Fatalf("events=%+v", events)
		}
		if s.Usage() != nil {
			t.Fatal("missing usage became zero")
		}
	}
	s := newSession(codeBuddyProtocol(), "", func(v string) string { return v })
	events, done := parseCodeBuddy(s, []byte(`{"type":"result","subtype":"success","terminal_reason":"cancelled"}`))
	if !done || events[len(events)-1].Type != runtime.EventCancelled {
		t.Fatal(events)
	}
}

func TestCodeBuddyProbeIsolatedAndVersionBound(t *testing.T) {
	if goruntime.GOOS != "darwin" || goruntime.GOARCH != "arm64" {
		t.Skip("supported platform is macOS arm64")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codebuddy")
	script := `#!/bin/sh
case "$HOME" in *openfox-codebuddy-*/home) ;; *) exit 9 ;; esac
test -z "$CODEBUDDY_API_KEY" || exit 10
case "$1" in
 --version) echo 'CodeBuddy 2.184.0';;
 --help) echo '--setting-sources --strict-mcp-config --tools --permission-mode --output-format --settings';;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := NewCodeBuddy(path)
	a.StateRoot = filepath.Join(dir, "states")
	a.Environment = map[string]string{"CODEBUDDY_API_KEY": "never-pass-to-probe"}
	cap, err := a.Detect(context.Background())
	if err != nil || !cap.ProtocolReady || cap.Runnable {
		t.Fatalf("%+v %v", cap, err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(script, "2.184.0", "2.185.0")), 0700); err != nil {
		t.Fatal(err)
	}
	cap, err = a.Detect(context.Background())
	if err != nil || cap.ProtocolReady || cap.Runnable {
		t.Fatalf("unsupported version: %+v %v", cap, err)
	}
	if _, err = a.Start(context.Background(), codeBuddyRequest()); !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("start=%v", err)
	}
}
