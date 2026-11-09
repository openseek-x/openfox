package stage2

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openfox/internal/agents"
	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/mcpconnect"
	"openfox/internal/platform/safehttp"
)

func TestMCPConnections(t *testing.T) {
	n := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string
			ID     int
		}
		if e := json.NewDecoder(r.Body).Decode(&rpc); e != nil {
			t.Error(e)
		}
		n++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/private" && r.Header.Get("Authorization") != "Bearer integration-secret" {
			w.WriteHeader(401)
			return
		}
		switch rpc.Method {
		case "initialize":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}}}}`)
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"lookup","inputSchema":{"type":"object"}}]}}`)
		default:
			t.Error("unexpected request")
		}
	}))
	defer up.Close()
	roots := x509.NewCertPool()
	roots.AddCert(up.Certificate())
	e := newAgEnvWithOptions(t, api.DispatchOptions{ImportSafeHTTP: safehttp.Options{AllowAddr: func(ip net.IP) bool { return ip.IsLoopback() }, RootCAs: roots}})
	manager, owner, member, foreign := e.client(agManagerID), e.client(agOwnerID), e.client(agMemberID), e.client(agOutsiderID)
	space := e.createSpace("SHARED")
	agent := e.createAgent(manager, space, "SHARED", nil)
	base := agentPath(space, agent.ID) + "/mcp-servers"
	url := base + "/" + agNewID(t)
	in := agents.MCPInput{Config: mcpconnect.Config{Name: "sample", Transport: "http", URL: up.URL + "/public"}}
	member.expectError(403, "", "PUT", url, in)
	if n != 0 {
		t.Fatal("unauthorized probe reached the server")
	}
	var saved agents.MCPServer
	manager.must(200, "PUT", url, in, &saved)
	if n != 3 || len(saved.Tools) != 1 || saved.Status != "connected" || saved.Version != 1 {
		t.Fatalf("anonymous: %+v", saved)
	}
	in.ExpectedVersion = 1
	in.URL = up.URL + "/private"
	in.AuthMode = "api_key"
	in.Credential = "integration-secret"
	response := manager.must(200, "PUT", url, in, &saved)
	if strings.Contains(string(response.Body), "integration-secret") || saved.Config.Credential != mcpconnect.Mask {
		t.Fatal("secret in response")
	}
	var list agents.MCPList
	response = member.must(200, "GET", base, nil, &list)
	if list.CanEdit || list.CanUseLocal || len(list.Servers) != 1 || strings.Contains(string(response.Body), "integration-secret") {
		t.Fatalf("reader: %s", response.Body)
	}
	in.Config = saved.Config
	in.ExpectedVersion = saved.Version
	in.URL = up.URL + "/retarget"
	before := n
	manager.expectError(400, "INVALID_REQUEST", "PUT", url, in)
	if n != before {
		t.Fatal("credential sent to new destination")
	}
	foreign.expectError(404, "", "GET", base, nil)
	member.expectError(403, "", "DELETE", url+"?version=2", nil)
	manager.expectError(409, "VERSION_CONFLICT", "DELETE", url+"?version=1", nil)
	manager.must(204, "DELETE", url+"?version=2", nil, nil)
	var persisted bool
	e.scalar(&persisted, `SELECT EXISTS(SELECT 1 FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2)`, e.org, agent.ID)
	if persisted {
		t.Fatal("detach left a secret")
	}
	local := agents.MCPInput{Config: mcpconnect.Config{Name: "private-tool", Transport: "stdio", Command: "node", Args: []string{"tool.js"}}}
	var pat struct{ Secret string }
	owner.must(201, "POST", "/v1/me/tokens", map[string]any{"name": "MCP PAT test"}, &pat)
	var conv convSummary
	owner.must(201, "POST", "conversations", map[string]any{"space_id": space, "agent_id": agent.ID}, &conv)
	for _, tc := range []struct {
		headers     []string
		interactive bool
	}{{nil, true}, {[]string{"Authorization", "Bearer " + pat.Secret}, false}} {
		id := agNewID(t)
		owner.must(201, "POST", conversationPath(conv.ID)+"/messages", map[string]any{"id": id, "client_message_id": id, "body": "MCP source"}, nil, tc.headers...)
		var verified bool
		e.scalar(&verified, `SELECT authenticated_user_input FROM openfox.messages WHERE id=$1 AND organization_id=$2`, id, e.org)
		if verified != tc.interactive {
			t.Fatalf("wrong interactive provenance: %t", verified)
		}
	}
	owner.must(403, "PUT", url, local, nil, "Authorization", "Bearer "+pat.Secret)
	owner.must(403, "GET", base+"/inventory", nil, nil, "Authorization", "Bearer "+pat.Secret)
	manager.expectError(403, "", "PUT", url, local)
	owner.must(200, "PUT", url, local, &saved)
	manager.must(200, "GET", base, nil, &list)
	if len(list.Servers) != 0 {
		t.Fatal("leaked local config")
	}
	manager.expectError(404, "", "DELETE", url+"?version=1", nil)
	e.exec(`UPDATE openfox.physical_devices SET capabilities='{"runtime_mcp_inventory":{"codex":{"status":"ok","servers":[{"name":"Local_DB","transport":"STDIO","enabled":true,"source":"USER_CONFIG","secret":"device-only"}]}}}' WHERE id=$1`, e.deviceID)
	var leaked bool
	e.scalar(&leaked, `SELECT capabilities?'runtime_mcp_inventory' FROM openfox.physical_devices WHERE id=$1`, e.deviceID)
	if leaked {
		t.Fatal("legacy heartbeat persisted private inventory")
	}
	key, token := agNewID(t), agNewID(t)
	e.exec(`WITH k AS (INSERT INTO openfox.device_keys(device_id,key_id,public_key) VALUES($1,$2,$3) RETURNING device_id,key_id,generation)
 INSERT INTO openfox.device_sessions(device_id,key_id,device_generation,key_generation,token_hash,expires_at)
 SELECT d.id,k.key_id,d.generation,k.generation,$4,clock_timestamp()+interval '1 minute' FROM k JOIN openfox.physical_devices d ON d.id=k.device_id`, e.deviceID, key, identity.HashSessionToken(key), identity.HashSessionToken(token))
	entry := map[string]any{"name": "Local_DB", "transport": "STDIO", "source": "USER_CONFIG", "enabled": true}
	report := map[string]any{"runtimes": map[string]any{"codex": map[string]any{"status": "ok", "servers": []any{entry}}}}
	deviceAPI := "/v1/device/organizations/" + e.org + "/mcp-inventory"
	owner.must(401, "PUT", deviceAPI, report, nil)
	owner.must(204, "PUT", deviceAPI, report, nil, "Authorization", "Bearer "+token)
	entry["command"] = "device-only"
	owner.must(400, "PUT", deviceAPI, report, nil, "Authorization", "Bearer "+token)
	var inv agents.MCPInventory
	response = owner.must(200, "GET", base+"/inventory", nil, &inv)
	if !inv.Online || inv.CheckedAt == nil || inv.Status != "ok" || len(inv.Servers) != 1 || strings.Contains(string(response.Body), "device-only") {
		t.Fatalf("inventory: %s", response.Body)
	}
	manager.expectError(403, "", "GET", base+"/inventory", nil)
	ch := agents.MCPInheritInput{Enabled: true, DeviceID: e.deviceID, Runtime: inv.Runtime}
	owner.must(200, "PUT", base+"/inherited/Local_DB", ch, &inv)
	if !inv.Servers[0].Selected {
		t.Fatal("enable not persisted")
	}
	ch.Enabled = false
	owner.must(200, "PUT", base+"/inherited/Local_DB", ch, &inv)
	if inv.Servers[0].Selected {
		t.Fatal("detach inheritance")
	}
}
