package stage2

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/daemon/workdir"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/runtime/cliagent"
	"openfox/internal/tenancy"
)

func runtimeDefaultsPool(e *agEnv) *postgres.Pool {
	e.t.Helper()
	pool, err := postgres.New(e.ctx, postgres.Config{URL: os.Getenv("OPENFOX_TEST_DATABASE_URL"), MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(pool.Close)
	return pool
}
func runtimeDefaultsMaterialize(e *agEnv, pool *postgres.Pool) {
	e.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}
	err := pool.RunForOrganization(e.ctx, e.org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), func(ctx context.Context, tx pgx.Tx) error {
		_, _, err := (dispatch.Store{}).MaterializeInputs(ctx, tx, e.org, 100)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// Real HTTP -> tenant PG -> materialization/lease -> supervisor -> existing pi
// adapter -> a controlled local executable. The executable is a protocol sink,
// not a real model provider or a claim of external Runtime interoperability.
func TestRuntimeDefaultsContextToProcess(t *testing.T) {
	e := newAgEnv(t)
	e.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,4)`, e.org)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	caps := map[string]any{"runtimes": map[string]any{"pi": map[string]any{"runnable": true, "login_status": "LOGGED_IN", "models": []map[string]string{{"id": "machine-first"}, {"id": "machine-next"}}}}}
	e.deviceCall(executor, http.MethodPost, "/v1/device/heartbeat", map[string]any{"center_epoch": executor.CenterEpoch, "protocol_major": 1, "protocol_minor": 0, "daemon_version": "controlled-r06", "operating_system": "darwin", "architecture": "arm64", "capabilities": caps}).mustStatus(t, 200)
	path := runtimeDefaultsPath(executor.ID, "pi")
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 0, "model": "machine-first", "set_env": map[string]string{"MACHINE_TOKEN": "CONTROLLED-MACHINE-R06", "OVERLAP": "CONTROLLED-machine-loser", "GIT_AUTHOR_NAME": "wrong-author"}}, nil)
	var grant string
	e.scalar(&grant, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, e.org, executor.ID)
	space := e.createSpace("shared")
	agent := e.createAgent(owner, space, "SHARED", map[string]any{"config": e.configBody(map[string]any{"computer_id": executor.ID, "grant_id": grant, "runtime": "pi", "model": "default", "model_source": "MACHINE"})})
	owner.must(200, http.MethodPut, agentPath(space, agent.ID)+"/env-vars/OVERLAP", map[string]string{"value": "CONTROLLED-AGENT-R06"}, nil)
	var conversation conversations.Summary
	owner.must(201, http.MethodPost, "conversations", map[string]string{"space_id": space, "agent_id": agent.ID}, &conversation)
	owner.must(200, http.MethodGet, conversationPath(conversation.ID), nil, &conversation)
	if conversation.Effective == nil || conversation.Effective.Model != "machine-first" || conversation.Effective.ModelSource != "computer" || len(conversation.Effective.Environment) != 3 {
		t.Fatal("summary does not resolve machine model/environment")
	}
	owner.must(201, http.MethodPost, conversationPath(conversation.ID)+"/messages", map[string]any{"body": "controlled process test", "client_message_id": agNewID(t), "mentions": []map[string]string{{"agent_id": agent.ID}}}, nil)
	pool := runtimeDefaultsPool(e)
	runtimeDefaultsMaterialize(e, pool)
	var runID, model string
	e.scalar(&runID, `SELECT id::text FROM openfox.runs WHERE organization_id=$1::uuid AND conversation_id=$2::uuid`, e.org, conversation.ID)
	e.scalar(&model, `SELECT model FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
	if model != "machine-first" {
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
	executable := filepath.Join(root, "pi-sink")
	// Only test values and selected environment names are captured, in t.TempDir.
	script := `#!/usr/bin/env python3
import os,sys,json
if '--version' in sys.argv:
 print('1.0.0');sys.exit(0)
if '--help' in sys.argv:
 print('--mode --session-dir');sys.exit(0)
sys.stdin.read()
with open(` + strconvQuote(capture) + `, 'w') as f:
 os.chmod(f.name,0o600)
 json.dump({'args':sys.argv[1:],'env':{k:os.environ.get(k) for k in ['MACHINE_TOKEN','OVERLAP','GIT_AUTHOR_NAME']}},f)
print(json.dumps({'type':'session','id':'controlled-r06-session'}))
print(json.dumps({'type':'message_end','message':{'role':'assistant','stopReason':'stop','content':[{'type':'text','text':os.environ.get('MACHINE_TOKEN','missing')}]}}))
print(json.dumps({'type':'agent_end','willRetry':False}))
print(json.dumps({'type':'agent_settled','aborted':False}))
`
	if err = os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	adapter := cliagent.NewPi(executable)
	adapter.Environment = map[string]string{"PATH": os.Getenv("PATH"), "HOME": root}
	adapter.StateRoot = filepath.Join(root, "pi-state")
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
	if state != "SUCCEEDED" {
		t.Fatalf("controlled Run state %s", state)
	}
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
	if !strings.Contains(strings.Join(captured.Args, " "), "--model machine-first") || captured.Env["MACHINE_TOKEN"] != "CONTROLLED-MACHINE-R06" || captured.Env["OVERLAP"] != "CONTROLLED-AGENT-R06" || captured.Env["GIT_AUTHOR_NAME"] == "wrong-author" {
		t.Fatal("child process model/environment/author precedence mismatch")
	}
	var leaks int
	e.scalar(&leaks, `SELECT count(*) FROM openfox.run_events WHERE organization_id=$1::uuid AND run_id=$2::uuid AND safe_payload::text LIKE '%CONTROLLED-MACHINE-R06%'`, e.org, runID)
	if leaks != 0 {
		t.Fatal("process output secret escaped redaction")
	}
	if os.Getenv("MACHINE_TOKEN") != "" {
		t.Fatal("Run variable leaked into supervisor parent")
	}
}

func strconvQuote(value string) string { raw, _ := json.Marshal(value); return string(raw) }
