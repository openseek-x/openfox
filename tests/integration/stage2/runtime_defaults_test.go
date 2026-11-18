package stage2

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"openfox/internal/devices"
	"openfox/internal/dispatch"
)

func runtimeDefaultsPath(id, runtime string) string {
	return "devices/" + id + "/runtime-defaults/" + runtime
}

func TestRuntimeDefaultsOwnerScopeAndSecrets(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	path := runtimeDefaultsPath(e.deviceID, "codex")
	var saved devices.RuntimeDefaults
	owner.must(200, http.MethodGet, path, nil, &saved)
	if saved.Version != 0 || saved.Model != nil || len(saved.EnvVars) != 0 || !saved.SecretsAvailable {
		t.Fatalf("initial metadata %+v", saved)
	}
	for _, id := range []string{agAdminID, agManagerID, agMemberID, agGuestID} {
		e.client(id).do(http.MethodGet, path, nil).mustStatus(t, 404)
		e.client(id).do(http.MethodPatch, path, map[string]any{"expected_version": 0, "model": "m"}).mustStatus(t, 404)
	}
	e.client(agOutsiderID).doIn(e.otherOrg, http.MethodGet, path, nil).mustStatus(t, 404)
	const secret = "CONTROLLED-R06-SECRET"
	response := owner.do(http.MethodPatch, path, map[string]any{"expected_version": 0, "model": "machine-model", "set_env": map[string]string{"TOKEN": secret, "EMPTY": ""}})
	response.mustStatus(t, 200)
	response.decode(t, &saved)
	if response.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(response.Body), secret) || saved.Version != 1 || len(saved.EnvVars) != 2 {
		t.Fatal("metadata leaked or has wrong version")
	}
	var leaks int
	e.scalar(&leaks, `SELECT count(*) FROM openfox.audit_events WHERE organization_id=$1::uuid AND safe_metadata::text LIKE '%'||$2||'%'`, e.org, secret)
	if leaks != 0 {
		t.Fatal("audit leaked value")
	}
	var cipher []byte
	e.scalar(&cipher, `SELECT value_ciphertext FROM openfox.device_runtime_env_vars WHERE organization_id=$1::uuid AND device_id=$2::uuid AND name='TOKEN'`, e.org, e.deviceID)
	if strings.Contains(string(cipher), secret) {
		t.Fatal("plaintext persisted")
	}
	noKey := *owner
	noKey.server = e.noSecrets
	noKey.do(http.MethodPatch, path, map[string]any{"expected_version": 1, "set_env": map[string]string{"TOKEN": "next"}}).mustStatus(t, 503)
	noKey.must(200, http.MethodPatch, path, map[string]any{"expected_version": 1, "model": nil}, &saved)
	if saved.Version != 2 || saved.SecretsAvailable || saved.Model != nil {
		t.Fatal("model-only update without key failed")
	}
	// Same owner and physical computer in another organization still starts empty.
	e.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, e.otherOrg, agOwnerID)
	e.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, e.otherOrg, e.deviceID)
	separate := owner.doIn(e.otherOrg, http.MethodGet, path, nil)
	separate.mustStatus(t, 200)
	separate.decode(t, &saved)
	if saved.Version != 0 || len(saved.EnvVars) != 0 {
		t.Fatal("organization secrets crossed scope")
	}
	owner.must(200, http.MethodGet, runtimeDefaultsPath(e.deviceID, "pi"), nil, &saved)
	if saved.Version != 0 {
		t.Fatal("Runtime scope crossed")
	}
	e.exec(`UPDATE openfox.device_grants SET status='REVOKED',permission_version=permission_version+1 WHERE organization_id=$1::uuid AND device_id=$2::uuid`, e.org, e.deviceID)
	owner.do(http.MethodGet, path, nil).mustStatus(t, 404)
}

func TestRuntimeDefaultsCASAndValidation(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	path := runtimeDefaultsPath(e.deviceID, "codex")
	for _, raw := range []string{
		`{"expected_version":0,"model":"x","unknown":1}`, `{"expected_version":0,"set_env":{"A":"a","A":"b"}}`,
		`{"expected_version":0,"expected_version":0,"model":"x"}`, `{"expected_version":0,"set_env":{"A":null}}`,
		`{"expected_version":0,"set_env":{"HOME":"a"}}`, `{"expected_version":0,"set_env":{"LD_PRELOAD":"a"}}`,
		`{"expected_version":0,"set_env":{"A":"a"},"remove_env":["A"]}`, `{"expected_version":0,"remove_env":["A","A"]}`,
		`{"model":"x"}`, `{"expected_version":0,"set_env":{"A":"a\u0000b"}}`,
	} {
		owner.do(http.MethodPatch, path, json.RawMessage(raw)).mustStatus(t, 400)
	}
	clients := []*agClient{e.client(agOwnerID), e.client(agOwnerID)}
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i] = clients[i].do(http.MethodPatch, path, map[string]any{"expected_version": 0, "model": "machine", "set_env": map[string]string{"RACE": "value"}}).Status
		}(i)
	}
	close(start)
	wg.Wait()
	if !((statuses[0] == 200 && statuses[1] == 409) || (statuses[1] == 200 && statuses[0] == 409)) {
		t.Fatalf("CAS %v", statuses)
	}
	var saved devices.RuntimeDefaults
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 1, "model": nil, "remove_env": []string{"RACE"}}, &saved)
	if saved.Version != 2 || len(saved.EnvVars) != 0 {
		t.Fatal("clear must retain parent revision")
	}
	owner.do(http.MethodPatch, path, map[string]any{"expected_version": 0, "model": "stale"}).mustStatus(t, 409)
	env := map[string]string{}
	for i := 0; i < 100; i++ {
		env["VAR_"+strings.Repeat("a", i)] = "x"
	}
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 2, "set_env": env}, &saved)
	owner.do(http.MethodPatch, path, map[string]any{"expected_version": 3, "model": "should-not-save", "set_env": map[string]string{"ONE_TOO_MANY": "x"}}).mustStatus(t, 400)
	owner.must(200, http.MethodGet, path, nil, &saved)
	if saved.Version != 3 || saved.Model != nil || len(saved.EnvVars) != 100 {
		t.Fatal("rejected batch was not atomic")
	}
}

func TestRuntimeDefaultsRunEnvironment(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	space, agent, run := e.devRunFixture(executor, nil)
	path := runtimeDefaultsPath(executor.ID, "codex")
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 0, "set_env": map[string]string{"MACHINE": "CONTROLLED-machine-secret", "OVERRIDE": "unused-cipher", "EMPTY": "machine-value"}}, nil)
	owner.must(200, http.MethodPut, agentPath(space, agent)+"/env-vars/OVERRIDE", map[string]string{"value": "CONTROLLED-agent-secret"}, nil)
	owner.must(200, http.MethodPut, agentPath(space, agent)+"/env-vars/EMPTY", map[string]string{"value": ""}, nil)
	// A damaged losing machine value must not prevent an Agent override.
	e.exec(`UPDATE openfox.device_runtime_env_vars SET value_ciphertext=decode(repeat('00',16),'hex') WHERE organization_id=$1::uuid AND device_id=$2::uuid AND name='OVERRIDE'`, e.org, executor.ID)
	contextPath := "/v1/device/organizations/" + e.org + "/runs/" + run + "/context?generation=1"
	read := func() map[string]string {
		t.Helper()
		response := e.deviceCall(executor, http.MethodGet, contextPath, nil)
		response.mustStatus(t, 200)
		var rc dispatch.RunContext
		response.decode(t, &rc)
		result := map[string]string{}
		for _, v := range rc.Environment {
			result[v.Name] = v.Value
		}
		return result
	}
	values := read()
	if len(values) != 3 || values["OVERRIDE"] != "CONTROLLED-agent-secret" || values["EMPTY"] != "" || values["MACHINE"] != "CONTROLLED-machine-secret" {
		t.Fatal("environment precedence failed")
	}
	owner.must(200, http.MethodPatch, path, map[string]any{"expected_version": 1, "set_env": map[string]string{"MACHINE": "CONTROLLED-next-secret"}}, nil)
	if read()["MACHINE"] != "CONTROLLED-next-secret" {
		t.Fatal("next context did not read current environment")
	}
	other := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	e.deviceCall(other, http.MethodGet, contextPath, nil).mustStatus(t, 404)
	e.deviceCall(executor, http.MethodGet, strings.ReplaceAll(contextPath, "generation=1", "generation=2"), nil).mustStatus(t, 409)
	// Same valid ciphertext transplanted to another name cannot authenticate its AAD.
	e.exec(`UPDATE openfox.device_runtime_env_vars SET value_ciphertext=(SELECT value_ciphertext FROM openfox.device_runtime_env_vars WHERE organization_id=$1::uuid AND device_id=$2::uuid AND name='MACHINE'),value_nonce=(SELECT value_nonce FROM openfox.device_runtime_env_vars WHERE organization_id=$1::uuid AND device_id=$2::uuid AND name='MACHINE') WHERE organization_id=$1::uuid AND device_id=$2::uuid AND name='OVERRIDE'`, e.org, executor.ID)
	owner.must(204, http.MethodDelete, agentPath(space, agent)+"/env-vars/OVERRIDE", nil, nil)
	response := e.deviceCall(executor, http.MethodGet, contextPath, nil)
	expectCode(t, response, 409, "SECRETS_UNAVAILABLE")
	if strings.Contains(string(response.Body), "CONTROLLED") {
		t.Fatal("failed decryption partially returned secrets")
	}
}
