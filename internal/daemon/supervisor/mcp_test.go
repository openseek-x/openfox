package supervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/mcpconnect"
	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
	"openfox/internal/runtime/mcpconfig"
)

func TestAgentMCPProxyAndAuthorization(t *testing.T) {
	const secret = "test-credential-never-sent-to-model"
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-Untrusted") != "" {
			t.Error("unexpected upstream headers")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "x=secret")
		b, _ := io.ReadAll(r.Body)
		switch string(b) {
		case "redirect":
			http.Redirect(w, r, "/other", 302)
		case "reflect":
			_, _ = io.WriteString(w, secret)
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer u.Close()
	rc := RunContext{Runtime: "codex", MCPActorUserID: "owner", MCPDeviceOwnerID: "owner", MCPServers: []mcpconnect.RunServer{{Scope: "DEVICE_OWNER", Transport: "HTTP", Name: "fixture", OwnerUserID: "owner", DeviceID: "device", URL: u.URL, Headers: map[string]string{"Authorization": "Bearer " + secret}}}}
	m, err := prepareMCP(rc, "device", mcpconfig.Location{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	entry, _ := json.Marshal(m.servers)
	if strings.Contains(string(entry), secret) || strings.Contains(string(entry), u.URL) {
		t.Fatal("upstream descriptor escaped")
	}
	endpoint := m.servers["fixture"]["url"].(string)
	for _, tt := range []struct {
		path, body, host, origin string
		status                   int
	}{{endpoint, "ok", "", "", 200}, {endpoint, "reflect", "", "", 502}, {endpoint, "redirect", "", "", 502}, {endpoint + "x", "ok", "", "", 404}, {endpoint, "ok", "attacker.invalid", "", 404}, {endpoint, "ok", "", "https://attacker.invalid", 404}} {
		req, _ := http.NewRequest("POST", tt.path, strings.NewReader(tt.body))
		req.Header.Set("Authorization", "attacker")
		req.Header.Set("X-Untrusted", "no")
		req.Header.Set("Origin", tt.origin)
		if tt.host != "" {
			req.Host = tt.host
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.status || resp.Header.Get("Set-Cookie") != "" || bytes.Contains(body, []byte(secret)) {
			t.Fatalf("unexpected response %d", resp.StatusCode)
		}
	}
	for _, edit := range []func(*RunContext){func(c *RunContext) { c.MCPActorUserID = "" }, func(c *RunContext) { c.MCPDeviceOwnerID = "another" }, func(c *RunContext) { c.MCPServers[0].DeviceID = "another" }, func(c *RunContext) { c.MCPServers[0].Scope = "PUBLIC" }, func(c *RunContext) { c.MCPServers[0].Transport = "INHERITED"; c.MCPServers[0].Name = "missing" }, func(c *RunContext) { c.MCPServers[0].Name = "openfox" }} {
		bad := rc
		bad.MCPServers = append([]mcpconnect.RunServer(nil), rc.MCPServers...)
		edit(&bad)
		if cfg, e := prepareMCP(bad, "device", mcpconfig.Location{Home: t.TempDir()}); e == nil {
			cfg.close()
			t.Fatal("unexpected authorization")
		}
	}
}

func TestAgentMCPPublicRejectsLocalAddress(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("local server reached") }))
	defer s.Close()
	m, err := prepareMCP(RunContext{Runtime: "codex", MCPServers: []mcpconnect.RunServer{{Name: "public", Scope: "PUBLIC", Transport: "HTTP", URL: s.URL}}}, "device", mcpconfig.Location{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	resp, err := http.Post(m.servers["public"]["url"].(string), "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRealCodexAgentMCP(t *testing.T) {
	if os.Getenv("OFD_REAL_CODEX") != "1" {
		t.Skip("set OFD_REAL_CODEX=1 and OFD_REAL_CODEX_VERSION for a real model call")
	}
	version := os.Getenv("OFD_REAL_CODEX_VERSION")
	if version == "" {
		t.Fatal("missing OFD_REAL_CODEX_VERSION")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "OPENFOX_" + hex.EncodeToString(nonce[:])
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-fixture" || r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Protocol string `json:"protocolVersion"`
				Name     string `json:"name"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if req.ID == nil {
			w.WriteHeader(202)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": req.Params.Protocol, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "read_marker", "description": "Read the synthetic validation marker.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			if req.Params.Name != "read_marker" {
				w.WriteHeader(400)
				return
			}
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": marker}}}
		case "resources/list":
			result = map[string]any{"resources": []any{}}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": []any{}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer up.Close()
	loc := mcpconfig.UserLocation()
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(loc.Home, ".codex")
	}
	auth, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal("expected a local Codex login")
	}
	source := t.TempDir()
	if err = os.WriteFile(filepath.Join(source, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	ad := codex.New("")
	ad.SupportedVersions = []string{version}
	ad.HomeRoot = t.TempDir()
	ad.Environment = map[string]string{"CODEX_HOME": source}
	m, err := prepareMCP(RunContext{Runtime: "codex", MCPActorUserID: "owner", MCPDeviceOwnerID: "owner", MCPServers: []mcpconnect.RunServer{{Name: "entrytest", Scope: "DEVICE_OWNER", Transport: "HTTP", URL: up.URL, Headers: map[string]string{"Authorization": "Bearer synthetic-fixture"}, OwnerUserID: "owner", DeviceID: "fixture-device"}}}, "fixture-device", loc)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run, err := ad.Start(ctx, runtime.RunRequest{Prompt: "Use the MCP entrytest server's read_marker tool once. Reply with just the marker from the tool. Do not use shell or files.", ApprovalMode: "READ_ONLY", WorkingDirectory: work, MCPServers: m.servers, MCPSecrets: m.secrets, Timeout: 110 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range run.Events() {
		if ev.Type == runtime.EventWarning {
			t.Logf("warning: %s", ev.Text)
		}
	}
	res, err := run.Wait(ctx)
	if err != nil || res.Status != runtime.StatusSucceeded || strings.TrimSpace(res.Output) != marker || calls.Load() != 1 {
		t.Fatalf("result %s verified-output=%t calls=%d code=%s err=%v", res.Status, strings.TrimSpace(res.Output) == marker, calls.Load(), res.ErrorCode, err)
	}
}

func TestAgentMCPProxyRejectsReflectedQuerySecrets(t *testing.T) {
	const secret = "synthetic query key"
	u := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != secret {
			t.Error("lost upstream query")
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := io.ReadAll(r.Body)
		switch string(b) {
		case "value":
			io.WriteString(w, `{"text":"echo synthetic query key"}`)
		case "encoded":
			io.WriteString(w, `{"text":"synthetic+query+key"}`)
		case "session":
			w.Header().Set("Mcp-Session-Id", secret)
			io.WriteString(w, "{}")
		default:
			io.WriteString(w, `{"id":"1","language":"en","content":[]}`)
		}
	}))
	defer u.Close()
	remote := u.URL + "?access_token=synthetic+query+key&lang=en&page=1"
	endpoint, stop, err := startMCPProxy(mcpconnect.Config{URL: remote}, u.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, test := range []struct {
		body   string
		status int
	}{{"value", 502}, {"encoded", 502}, {"session", 502}, {"clean", 200}} {
		resp, e := http.Post(endpoint, "application/json", strings.NewReader(test.body))
		if e != nil {
			t.Fatal(e)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != test.status || strings.Contains(resp.Header.Get("Mcp-Session-Id"), secret) {
			t.Fatalf("%s status %d", test.body, resp.StatusCode)
		}
	}
}

func TestBoundProviderCLIUsesOnlyApprovedOwnerAndExecutable(t *testing.T) {
	rc := RunContext{MCPDeviceOwnerID: "user", MCPActorUserID: "user", Runtime: "codex", MCPServers: []mcpconnect.RunServer{{Name: "feishu-docs", CatalogID: "feishu", DeviceID: "device", OwnerUserID: "user", Scope: "DEVICE_OWNER", Transport: "STDIO", Command: "openfox:provider-cli", Args: []string{"feishu"}}}}
	m, err := prepareMCP(rc, "device", mcpconfig.Location{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	entry := m.servers["feishu-docs"]
	exe, _ := os.Executable()
	if entry["command"] != exe || strings.Join(entry["args"].([]string), " ") != "mcp provider-cli feishu" {
		t.Fatal("provider did not map to current executable")
	}
	rc.MCPServers[0].Env = map[string]string{"PATH": "/override"}
	if m, e := prepareMCP(rc, "device", mcpconfig.Location{Home: t.TempDir()}); e == nil {
		m.close()
		t.Fatal("provider accepted command lookup override")
	}
}
