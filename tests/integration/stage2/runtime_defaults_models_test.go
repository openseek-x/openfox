package stage2

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"openfox/internal/agents"
	"openfox/internal/conversations"
)

func TestRuntimeDefaultsModelsAndLegacyConfig(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	e.exec(`UPDATE openfox.physical_devices SET capabilities='{"runtimes":{"codex":{"runnable":true},"pi":{"runnable":true},"claude-code":{"runnable":true}}}'::jsonb WHERE id=$1::uuid`, e.deviceID)
	for _, runtime := range []string{"codex", "pi"} {
		owner.must(200, http.MethodPatch, runtimeDefaultsPath(e.deviceID, runtime), map[string]any{"expected_version": 0, "model": "machine-" + runtime}, nil)
	}
	space := e.createSpace("shared")
	created := e.createAgent(owner, space, "SHARED", map[string]any{"config": e.configBody(map[string]any{"model": "default", "model_source": "MACHINE"})})
	var agent agents.Agent
	owner.must(200, http.MethodGet, agentPath(space, created.ID), nil, &agent)
	if agent.Config.ModelSource != "MACHINE" {
		t.Fatal("model source not persisted")
	}
	// An old client editing instructions and keeping model preserves the mode.
	update := e.configBody(map[string]any{"model": "default", "instructions": "old client edit", "expected_agent_version": agent.Version})
	owner.must(201, http.MethodPost, agentPath(space, agent.ID)+"/config-versions", update, &agent)
	if agent.Config.ModelSource != "MACHINE" {
		t.Fatal("old client erased machine selection")
	}
	var copy agents.Agent
	owner.must(201, http.MethodPost, agentPath(space, agent.ID)+"/copy", map[string]any{}, &copy)
	if copy.Config.ModelSource != "MACHINE" {
		t.Fatal("copy lost source mode")
	}
	var conv conversations.Summary
	owner.must(201, http.MethodPost, "conversations", map[string]string{"space_id": space, "agent_id": agent.ID}, &conv)
	owner.must(200, http.MethodGet, conversationPath(conv.ID), nil, &conv)
	if conv.Effective.Model != "machine-codex" {
		t.Fatal("initial machine resolution")
	}
	cases := []struct {
		runtime, model any
		want, source   string
	}{
		{"pi", nil, "machine-pi", "computer"}, {"pi", "default", "default", "conversation"},
		{"pi", "conversation-model", "conversation-model", "conversation"}, {"claude-code", nil, "default", "runtime"}, {nil, nil, "machine-codex", "computer"},
	}
	for _, tc := range cases {
		owner.must(200, http.MethodPatch, conversationPath(conv.ID)+"/overrides", map[string]any{"expected_version": conv.Overrides.Version, "runtime": tc.runtime, "model": tc.model}, &conv)
		if conv.Effective.Model != tc.want || conv.Effective.ModelSource != tc.source {
			t.Fatalf("effective model %s source %s", conv.Effective.Model, conv.Effective.ModelSource)
		}
	}
	// Changing the model from an old client is an explicit selection.
	update = e.configBody(map[string]any{"model": "old-client-model", "expected_agent_version": agent.Version})
	owner.must(201, http.MethodPost, agentPath(space, agent.ID)+"/config-versions", update, &agent)
	if agent.Config.ModelSource != "EXPLICIT" {
		t.Fatal("legacy explicit model did not leave machine mode")
	}
	owner.must(200, http.MethodGet, conversationPath(conv.ID), nil, &conv)
	if conv.Effective.Model != "old-client-model" {
		t.Fatal("Agent explicit model not respected")
	}
	update = e.configBody(map[string]any{"model": "default", "expected_agent_version": agent.Version, "model_source": "EXPLICIT"})
	owner.must(201, http.MethodPost, agentPath(space, agent.ID)+"/config-versions", update, &agent)
	owner.must(200, http.MethodGet, conversationPath(conv.ID), nil, &conv)
	if conv.Effective.Model != "default" || conv.Effective.ModelSource != "runtime" {
		t.Fatal("explicit default was silently inherited")
	}
}

func TestRuntimeDefaultsEnvironmentMergedLimitAndRevocation(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	space, agent, run := e.devRunFixture(executor, nil)
	path := runtimeDefaultsPath(executor.ID, "codex")
	env := map[string]string{}
	for i := 0; i < 100; i++ {
		env[fmt.Sprintf("VAR_%03d", i)] = "CONTROLLED-value"
	}
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 0, "set_env": env}, nil)
	contextPath := "/v1/device/organizations/" + e.org + "/runs/" + run + "/context?generation=1"
	e.deviceCall(executor, http.MethodGet, contextPath, nil).mustStatus(t, 200)
	owner.must(200, http.MethodPut, agentPath(space, agent)+"/env-vars/EXTRA", map[string]string{"value": "CONTROLLED-extra"}, nil)
	response := e.deviceCall(executor, http.MethodGet, contextPath, nil)
	expectCode(t, response, 409, "SECRETS_UNAVAILABLE")
	if strings.Contains(string(response.Body), "CONTROLLED") {
		t.Fatal("over-budget context partially returned values")
	}
	owner.must(204, http.MethodDelete, agentPath(space, agent)+"/env-vars/EXTRA", nil, nil)
	e.exec(`UPDATE openfox.device_grants SET status='REVOKED',permission_version=permission_version+1 WHERE organization_id=$1::uuid AND device_id=$2::uuid`, e.org, executor.ID)
	denied := e.deviceCall(executor, http.MethodGet, contextPath, nil)
	if denied.Status == 200 || strings.Contains(string(denied.Body), "CONTROLLED") {
		t.Fatal("revoked grant could still obtain secrets")
	}
}

func TestRuntimeDefaultsOldRunKeepsComputerAndRuntime(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	first := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	second := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	space, agentID, runID := e.devRunFixture(first, nil)
	for _, item := range []struct{ device, runtime, value string }{{first.ID, "codex", "first-codex"}, {first.ID, "pi", "first-pi"}, {second.ID, "codex", "second-codex"}} {
		owner.must(200, http.MethodPatch, runtimeDefaultsPath(item.device, item.runtime), map[string]any{"expected_version": 0, "set_env": map[string]string{"CONTROLLED_SCOPE": item.value}}, nil)
	}
	var grant string
	e.scalar(&grant, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, e.org, second.ID)
	var agent agents.Agent
	owner.must(200, http.MethodGet, agentPath(space, agentID), nil, &agent)
	owner.must(201, http.MethodPost, agentPath(space, agentID)+"/config-versions", e.configBody(map[string]any{"computer_id": second.ID, "grant_id": grant, "expected_agent_version": agent.Version}), &agent)
	contextPath := "/v1/device/organizations/" + e.org + "/runs/" + runID + "/context?generation=1"
	response := e.deviceCall(first, http.MethodGet, contextPath, nil)
	response.mustStatus(t, 200)
	if !strings.Contains(string(response.Body), "first-codex") || strings.Contains(string(response.Body), "second-codex") {
		t.Fatal("old Run read new computer defaults")
	}
	// A controlled legacy lease fixture with a conversation Runtime override.
	e.exec(`UPDATE openfox.runs SET runtime='pi',model='default',approval_mode='FOLLOW_RUNTIME' WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, runID)
	response = e.deviceCall(first, http.MethodGet, contextPath, nil)
	response.mustStatus(t, 200)
	if !strings.Contains(string(response.Body), "first-pi") || strings.Contains(string(response.Body), "first-codex") {
		t.Fatal("Run Runtime override read Agent Runtime variables")
	}
	e.deviceCall(second, http.MethodGet, contextPath, nil).mustStatus(t, 404)
}

func TestRuntimeDefaultsEnvironmentBoundaries(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	path := runtimeDefaultsPath(e.deviceID, "codex")
	name := strings.Repeat("A", 128)
	value := strings.Repeat("界", 2730) + "ab" // exactly 8192 UTF-8 bytes
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 0, "set_env": map[string]string{name: value}}, nil)
	for _, env := range []map[string]string{{name + "A": "ok"}, {name: value + "x"}, {"NUL": "a\x00b"}, {"OPENFOX_TOKEN": "unsafe"}, {"PATH": "unsafe"}} {
		owner.do(http.MethodPatch, path, map[string]any{"expected_version": 1, "set_env": env}).mustStatus(t, 400)
	}
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 1, "set_env": map[string]string{"MULTILINE": "line 1\nline 2\tvalue"}}, nil)
	owner.do(http.MethodPatch, path, map[string]any{"expected_version": 2, "model": strings.Repeat("界", 43)}).mustStatus(t, 400)
}
