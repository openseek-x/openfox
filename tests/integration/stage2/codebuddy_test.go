package stage2

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"openfox/internal/agents"
	"openfox/internal/conversations"
	"openfox/internal/devices"
	"openfox/internal/mcpconnect"
)

func codeBuddyCaps() string {
	return `{"runtimes":{"codebuddy":{"version":"2.184.0","runnable":false,"protocol_ready":true,"requires_run_configuration":true,"approval_modes":["READ_ONLY"],"collaboration_modes":["DEFAULT","PLAN"],"models_error":"Explicit model required"}}}`
}

func TestCodeBuddyConfigurationBoundaries(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	e.exec(`UPDATE openfox.physical_devices SET capabilities=$2::jsonb WHERE id=$1`, e.deviceID, codeBuddyCaps())
	space := e.createSpace("shared")
	agent := e.createAgent(owner, space, "SHARED", map[string]any{"config": e.configBody(map[string]any{"runtime": "codebuddy", "model": "default", "approval_mode": "READ_ONLY", "collaboration_mode": "PLAN"})})
	path := agentPath(space, agent.ID)
	for _, extra := range []map[string]any{
		{"approval_mode": "FULL_ACCESS"}, {"approval_mode": "WORKSPACE_WRITE"}, {"custom_args": []string{"--plugin-dir", "unsafe"}}, {"model": "auto"},
	} {
		extra["runtime"] = "codebuddy"
		extra["expected_agent_version"] = agent.Version
		owner.do(http.MethodPost, path+"/config-versions", e.configBody(extra)).mustStatus(t, 422)
	}
	owner.do(http.MethodPut, path+"/env-vars/CODEBUDDY_AUTH_TOKEN", map[string]string{"value": "CONTROLLED-rejected"}).mustStatus(t, 422)
	owner.do(http.MethodPut, path+"/env-vars/HOME", map[string]string{"value": "/outside"}).mustStatus(t, 422)
	owner.must(200, http.MethodPut, path+"/env-vars/CODEBUDDY_MODEL", map[string]string{"value": "fixture-model"}, nil)
	var copied agents.Agent
	owner.must(201, http.MethodPost, path+"/copy", map[string]any{}, &copied)
	if copied.Config == nil || copied.Config.Runtime != "codebuddy" || copied.Config.ApprovalMode != "READ_ONLY" || copied.Config.CollaborationMode != "PLAN" {
		t.Fatal("copy lost restricted configuration")
	}
	owner.do(http.MethodPost, path+"/mcp-servers/discover", agents.MCPInput{Config: mcpconnect.Config{Name: "unsupported", Transport: "http", URL: "https://127.0.0.1:1"}}).mustStatus(t, 422)

	local := agents.MCPInput{Config: mcpconnect.Config{Name: "not-supported", Transport: "stdio", Command: "definitely-not-executed"}}
	owner.do(http.MethodPut, path+"/mcp-servers/"+agNewID(t), local).mustStatus(t, 422)
	owner.do(http.MethodPut, path+"/skills", map[string]any{"skill_ids": []string{agNewID(t)}}).mustStatus(t, 422)
	owner.must(200, http.MethodPut, path+"/skills", map[string]any{"skill_ids": []string{}}, nil)
	e.client(agMemberID).do(http.MethodPost, path+"/config-versions", e.configBody(map[string]any{"runtime": "codebuddy", "expected_agent_version": agent.Version})).mustStatus(t, 403)
	e.client(agOutsiderID).doIn(e.otherOrg, http.MethodGet, path, nil).mustStatus(t, 404)
	var conv conversations.Summary
	owner.must(201, http.MethodPost, "conversations", map[string]string{"space_id": space, "agent_id": agent.ID}, &conv)
	for _, patch := range []map[string]any{{"approval_mode": "FULL_ACCESS"}, {"approval_mode": "WORKSPACE_WRITE"}, {"model": "auto"}, {"reasoning_effort": "HIGH"}} {
		patch["expected_version"] = conv.Overrides.Version
		owner.do(http.MethodPatch, conversationPath(conv.ID)+"/overrides", patch).mustStatus(t, 422)
	}
	owner.must(200, http.MethodPatch, conversationPath(conv.ID)+"/overrides", map[string]any{"expected_version": conv.Overrides.Version, "runtime": "codebuddy", "model": "explicit-model"}, &conv)
	if conv.Effective.Runtime != "codebuddy" || conv.Effective.Model != "explicit-model" {
		t.Fatal("runtime/model override was not persisted")
	}
	var options conversations.OverrideOptions
	owner.must(200, http.MethodGet, conversationPath(conv.ID)+"/override-options", nil, &options)
	found := false
	for _, r := range options.Runtimes {
		if r.Provider == "codebuddy" {
			found = !r.Runnable && r.ProtocolReady && r.RequiresRunConfiguration
		}
	}
	if !found {
		t.Fatal("protocol readiness falsely became runnable or was lost")
	}
	defaults := runtimeDefaultsPath(e.deviceID, "codebuddy")
	for _, id := range []string{agAdminID, agMemberID} {
		e.client(id).do(http.MethodGet, defaults, nil).mustStatus(t, 404)
	}
	for _, bad := range []map[string]any{{"model": "auto"}, {"set_env": map[string]string{"CODEBUDDY_CONFIG_DIR": "/unsafe"}}, {"set_env": map[string]string{"PATH": "/unsafe"}}} {
		bad["expected_version"] = 0
		owner.do(http.MethodPatch, defaults, bad).mustStatus(t, 400)
	}
	const secret = "CONTROLLED-R03-KEY"
	var saved devices.RuntimeDefaults
	response := owner.must(200, http.MethodPatch, defaults, map[string]any{"expected_version": 0, "model": "machine-model", "set_env": map[string]string{"CODEBUDDY_API_KEY": secret, "CODEBUDDY_BASE_URL": "http://127.0.0.1:1234/v1", "CODEBUDDY_MODEL": "environment-model"}}, &saved)
	if strings.Contains(string(response.Body), secret) || saved.Version != 1 {
		t.Fatal("secret leaked or wrong revision")
	}
	clients := []*agClient{e.client(agOwnerID), e.login(e.server, agOwnerID)}
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	start := make(chan struct{})
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i] = clients[i].do(http.MethodPatch, defaults, map[string]any{"expected_version": 1, "model": "racing-model"}).Status
		}(i)
	}
	close(start)
	wg.Wait()
	if !((statuses[0] == 200 && statuses[1] == 409) || (statuses[0] == 409 && statuses[1] == 200)) {
		t.Fatalf("CAS=%v", statuses)
	}
	var leaks int
	e.scalar(&leaks, `SELECT count(*) FROM openfox.audit_events WHERE organization_id=$1 AND safe_metadata::text LIKE '%'||$2||'%'`, e.org, secret)
	if leaks != 0 {
		t.Fatal("audit contains key")
	}
}

func TestCodeBuddyRuntimeSwitchAndAttachmentsSerialize(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	other := e.login(e.server, agOwnerID)
	space := e.createSpace("shared")
	for attempt := 0; attempt < 4; attempt++ {
		agent := e.createAgent(owner, space, "SHARED", nil)
		path := agentPath(space, agent.ID)
		statuses := make([]int, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			statuses[0] = owner.do(http.MethodPost, path+"/config-versions", e.configBody(map[string]any{"runtime": "codebuddy", "model": "default", "expected_agent_version": agent.Version})).Status
		}()
		go func() {
			defer wg.Done()
			<-start
			statuses[1] = other.do(http.MethodPut, path+"/mcp-servers/"+agNewID(t), agents.MCPInput{Config: mcpconnect.Config{Name: "local", Transport: "stdio", Command: "not-executed"}}).Status
		}()
		close(start)
		wg.Wait()
		if !((statuses[0] == 201 && statuses[1] == 422) || (statuses[0] == 422 && statuses[1] == 200)) {
			t.Fatalf("racing writes=%v", statuses)
		}
		var invalid bool
		e.scalar(&invalid, `SELECT EXISTS(SELECT 1 FROM openfox.agents a JOIN openfox.agent_config_versions c ON c.organization_id=a.organization_id AND c.agent_id=a.id AND c.config_version=a.current_config_version JOIN openfox.agent_mcp_servers m ON m.organization_id=a.organization_id AND m.agent_id=a.id WHERE a.id=$1 AND c.runtime='codebuddy')`, agent.ID)
		if invalid {
			t.Fatal("runtime switch raced attachment into unsupported configuration")
		}
	}
}

func TestCodeBuddyDefaultRuntimeRequiresProtocolCapability(t *testing.T) {
	f := newChatFixture(t, map[string]any{"runtime": "codebuddy", "model": "fixture"})
	run := f.queueRun("")
	for _, caps := range []string{`{}`, `{"runtimes":{"codebuddy":{"runnable":true}}}`, `{"runtimes":{"codebuddy":{"runnable":false,"protocol_ready":true}}}`} {
		f.env.exec(`UPDATE openfox.physical_devices SET capabilities=$2::jsonb WHERE id=$1`, f.device.ID, caps)
		if len(f.pass()) != 0 {
			t.Fatal("default runtime bypassed protocol gate")
		}
		if reason, offered := f.waitReason(run); offered || reason != "RUNTIME_UNAVAILABLE" {
			t.Fatalf("wait=%s offered=%t", reason, offered)
		}
	}
	f.env.exec(`UPDATE openfox.physical_devices SET capabilities=$2::jsonb WHERE id=$1`, f.device.ID, codeBuddyCaps())
	if _, ok := f.pass()[run]; !ok {
		t.Fatal("protocol ready, run-config-aware device did not receive offer")
	}
	f.expectSlots(0, 1)
}

// An application rollback keeps the expanded constraints/data and first contains
// the affected runtime through existing authorized APIs; it never rewrites the
// Agent to a broader or different runtime.
func TestCodeBuddyRollbackContainment(t *testing.T) {
	f := newChatFixture(t, map[string]any{"runtime": "codebuddy", "model": "fixture"})
	e := f.env
	owner := e.client(agOwnerID)
	path := runtimeDefaultsPath(f.device.ID, "codebuddy")
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 0, "set_env": map[string]string{"CODEBUDDY_API_KEY": "CONTROLLED-RETAINED"}}, nil)
	queued := f.queueRun("")
	f.pass()
	owner.must(200, http.MethodPost, "runs/"+queued+"/cancel", nil, nil, "Idempotency-Key", "r03-rollback-"+queued)
	current := e.getAgent(owner, f.space, f.agentID)
	owner.must(200, http.MethodPatch, agentPath(f.space, f.agentID), map[string]any{"version": current.Version, "archived": true}, nil)
	if len(f.pass()) != 0 {
		t.Fatal("contained Run was offered")
	}
	var state string
	e.scalar(&state, `SELECT state FROM openfox.runs WHERE organization_id=$1 AND id=$2`, e.org, queued)
	if state != "CANCELED" {
		t.Fatalf("rollback Run state=%s", state)
	}
	var persisted string
	e.scalar(&persisted, `SELECT runtime FROM openfox.agent_config_versions WHERE organization_id=$1 AND agent_id=$2 AND config_version=1`, e.org, f.agentID)
	if persisted != "codebuddy" {
		t.Fatal("rollback silently changed runtime")
	}
	var defaults devices.RuntimeDefaults
	owner.must(200, http.MethodGet, path, nil, &defaults)
	if len(defaults.EnvVars) != 1 || defaults.EnvVars[0].Name != "CODEBUDDY_API_KEY" {
		t.Fatal("rollback lost encrypted configuration")
	}
	var validated int
	e.scalar(&validated, `SELECT count(*) FROM pg_constraint WHERE conname IN ('conversations_overrides_valid','device_runtime_defaults_runtime_check') AND convalidated AND pg_get_constraintdef(oid) LIKE '%codebuddy%'`)
	if validated != 2 {
		t.Fatalf("expanded constraints retained=%d", validated)
	}
}
