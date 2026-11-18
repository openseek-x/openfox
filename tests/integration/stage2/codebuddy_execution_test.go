package stage2

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"openfox/internal/conversations"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/daemon/workdir"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/runtime/cliagent"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real HTTP -> PG -> materialization/lease -> supervisor -> CodeBuddy adapter.
// The protocol executable is a fixture; the separate opt-in native test validates
// the fixed official CLI against a controlled local model.
func TestCodeBuddyContextToProcess(t *testing.T) { codeBuddyContextToProcess(t, "", "") }
func TestCodeBuddyNativeContextToProcess(t *testing.T) {
	path := os.Getenv("OPENFOX_TEST_CODEBUDDY_SANDBOX_WRAPPER")
	if path == "" {
		t.Skip("requires fixed native CLI wrapped in controlled OS sandbox")
	}
	codeBuddyContextToProcess(t, path, "")
}
func TestCodeBuddyCAFailureStateMachine(t *testing.T) {
	for _, phase := range []string{"relative-before-start", "removed-during-probe"} {
		t.Run(phase, func(t *testing.T) { codeBuddyContextToProcess(t, "", phase) })
	}
}
func codeBuddyContextToProcess(t *testing.T, native, caFailure string) {
	e := newAgEnv(t)
	endpoint := "http://127.0.0.1:1/v1"
	var modelCalls atomic.Int32
	if native != "" {
		model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "fixture-model" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer CONTROLLED-R03-KEY" {
				t.Error("native Run routing/model/auth mismatch")
			}
			modelCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture-model","choices":[{"index":0,"delta":{"role":"assistant","content":"CONTROLLED-MACHINE-R03"},"finish_reason":null}]}

data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}

data: [DONE]

`)
		}))
		defer model.Close()
		endpoint = model.URL + "/v1"
	}
	e.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,4)`, e.org)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	caps := json.RawMessage(codeBuddyCaps())
	e.deviceCall(executor, http.MethodPost, "/v1/device/heartbeat", map[string]any{"center_epoch": executor.CenterEpoch, "protocol_major": 1, "protocol_minor": 0, "daemon_version": "controlled-r03", "operating_system": "darwin", "architecture": "arm64", "capabilities": caps}).mustStatus(t, 200)
	path := runtimeDefaultsPath(executor.ID, "codebuddy")
	variables := map[string]string{"MACHINE_TOKEN": "CONTROLLED-MACHINE-R03", "OVERLAP": "CONTROLLED-machine-loser", "GIT_AUTHOR_NAME": "wrong-author", "CODEBUDDY_API_KEY": "CONTROLLED-R03-KEY", "CODEBUDDY_BASE_URL": endpoint}
	if caFailure == "relative-before-start" {
		variables["SSL_CERT_FILE"] = "missing-run-ca.pem"
	}
	if caFailure == "removed-during-probe" {
		server := httptest.NewTLSServer(http.NotFoundHandler())
		certificate := server.Certificate().Raw
		server.Close()
		caPath := filepath.Join(t.TempDir(), "fixture-ca.pem")
		if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600); err != nil {
			t.Fatal(err)
		}
		variables["SSL_CERT_FILE"] = caPath
	}
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 0, "model": "fixture-model", "set_env": variables}, nil)
	var grant string
	e.scalar(&grant, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, e.org, executor.ID)
	space := e.createSpace("shared")
	agent := e.createAgent(owner, space, "SHARED", map[string]any{"config": e.configBody(map[string]any{"computer_id": executor.ID, "grant_id": grant, "runtime": "codebuddy", "model": "default", "model_source": "MACHINE"})})
	owner.must(200, http.MethodPut, agentPath(space, agent.ID)+"/env-vars/OVERLAP", map[string]string{"value": "CONTROLLED-AGENT-R03"}, nil)
	var conversation conversations.Summary
	owner.must(201, http.MethodPost, "conversations", map[string]string{"space_id": space, "agent_id": agent.ID}, &conversation)
	owner.must(200, http.MethodGet, conversationPath(conversation.ID), nil, &conversation)
	if conversation.Effective == nil || conversation.Effective.Model != "fixture-model" || conversation.Effective.ModelSource != "computer" || len(conversation.Effective.Environment) != len(variables) {
		t.Fatal("summary does not resolve machine model/environment")
	}
	owner.must(201, http.MethodPost, conversationPath(conversation.ID)+"/messages", map[string]any{"body": "controlled process test", "client_message_id": agNewID(t), "mentions": []map[string]string{{"agent_id": agent.ID}}}, nil)
	pool := runtimeDefaultsPool(e)
	runtimeDefaultsMaterialize(e, pool)
	var runID, model string
	e.scalar(&runID, `SELECT id::text FROM openfox.runs WHERE organization_id=$1::uuid AND conversation_id=$2::uuid`, e.org, conversation.ID)
	e.scalar(&model, `SELECT model FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
	if model != "fixture-model" {
		t.Fatalf("frozen model %q", model)
	}
	var saved devices.RuntimeDefaults
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 1, "model": "machine-next"}, &saved)
	owner.must(200, http.MethodGet, conversationPath(conversation.ID), nil, &conversation)
	if conversation.Effective.Model != "machine-next" {
		t.Fatal("next-run summary stale")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(root, "capture.json")
	executable := filepath.Join(root, "codebuddy-sink")
	// Only test values and selected environment names are captured, in t.TempDir.
	script := `#!/usr/bin/env python3
import os,sys,json
if '--version' in sys.argv:
 print('CodeBuddy 2.184.0');sys.exit(0)
if '--help' in sys.argv:
 print('--setting-sources --strict-mcp-config --tools --permission-mode --output-format --settings');sys.exit(0)
prompt=sys.stdin.read()
with open(` + strconvQuote(capture) + `, 'w') as f:
 os.chmod(f.name,0o600)
 json.dump({'prompt':prompt,'args':sys.argv[1:],'env':{k:os.environ.get(k) for k in ['MACHINE_TOKEN','OVERLAP','GIT_AUTHOR_NAME']}},f)
print(json.dumps({'type':'system','subtype':'init','session_id':'controlled-r03-session'}))
print(json.dumps({'type':'result','subtype':'success','result':os.environ.get('MACHINE_TOKEN','missing'),'_meta':{'codebuddy.ai/turnUsage':{'version':1,'scope':'turn','total':{'usage':{'input_tokens':7,'output_tokens':3}}}}}))
`
	if err = os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if caFailure == "removed-during-probe" {
		script = strings.Replace(script, " print('CodeBuddy 2.184.0');sys.exit(0)", " os.remove("+strconvQuote(variables["SSL_CERT_FILE"])+")\n print('CodeBuddy 2.184.0');sys.exit(0)", 1)
		if err = os.WriteFile(executable, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if native != "" {
		executable = native
	}
	adapter := cliagent.NewCodeBuddy(executable)
	adapter.Environment = map[string]string{"PATH": os.Getenv("PATH"), "HOME": root}
	adapter.StateRoot = filepath.Join(root, "codebuddy-state")
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "workspaces"), AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := supervisor.OpenJournal(e.ctx, filepath.Join(root, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	runner, err := supervisor.New(supervisor.Config{DeviceID: executor.ID, Client: &supervisor.Client{BaseURL: e.server.URL, Token: func(context.Context) (string, error) { return executor.session, nil }}, Adapter: adapter, Workdir: manager, Journal: journal, Organizations: []string{e.org}, PollInterval: time.Second, AttachmentRoot: filepath.Join(root, "attachments"), ArtifactRoot: filepath.Join(root, "artifacts")})
	if err != nil {
		t.Fatal(err)
	}
	worker := dispatch.Worker{Pool: pool}
	if _, err = worker.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(15 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		e.scalar(&state, `SELECT state FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
		if state == "SUCCEEDED" || state == "FAILED" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if caFailure != "" {
		var started bool
		var failure string
		e.scalar(&started, `SELECT started_at IS NOT NULL FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
		e.scalar(&failure, `SELECT COALESCE(failure_code,'') FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
		if state != "FAILED" || (caFailure == "relative-before-start" && (started || failure != "PREPARE_FAILED")) || (caFailure == "removed-during-probe" && (!started || failure == "PREPARE_FAILED")) {
			t.Fatalf("CA failure transition: phase=%s state=%s started=%t code=%s", caFailure, state, started, failure)
		}
		if _, err := os.Stat(capture); !os.IsNotExist(err) {
			t.Fatal("invalid CA started native execution")
		}
		t.Logf("phase=%s state=%s started=%t failure_code=%s no_native_execution=true", caFailure, state, started, failure)
		return
	}
	if state != "SUCCEEDED" {
		t.Fatalf("controlled Run state %s", state)
	}
	if native == "" {
		raw, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		var captured struct {
			Args []string          `json:"args"`
			Env  map[string]string `json:"env"`
		}
		if json.Unmarshal(raw, &captured) != nil {
			t.Fatal("capture malformed")
		}
		if !strings.Contains(strings.Join(captured.Args, " "), "--model fixture-model") || captured.Env["MACHINE_TOKEN"] != "CONTROLLED-MACHINE-R03" || captured.Env["OVERLAP"] != "CONTROLLED-AGENT-R03" || captured.Env["GIT_AUTHOR_NAME"] == "wrong-author" {
			t.Fatal("child process model/environment/author precedence mismatch")
		}
	} else if modelCalls.Load() != 1 {
		t.Fatalf("native model calls=%d", modelCalls.Load())
	}

	var leaks int
	e.scalar(&leaks, `SELECT count(*) FROM openfox.run_events WHERE organization_id=$1::uuid AND run_id=$2::uuid AND safe_payload::text LIKE '%CONTROLLED-MACHINE-R03%'`, e.org, runID)
	if leaks != 0 {
		t.Fatal("process output secret escaped redaction")
	}
	if os.Getenv("MACHINE_TOKEN") != "" {
		t.Fatal("Run variable leaked into supervisor parent")
	}
}
