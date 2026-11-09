package stage2

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"openfox/internal/agents"
	"openfox/internal/api"
	"openfox/internal/mcpconnect"
	"openfox/internal/platform/safehttp"
	"strings"
	"testing"
)

// Exercise the real callback and organization store; no provider accounts or user credentials.
func TestMCPOAuthCallback(t *testing.T) {
	var upstream *httptest.Server
	var challenge string
	var exchanges int
	const token = "test-mcp-oauth-secret"
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+upstream.URL+`/resource"`)
				w.WriteHeader(401)
				return
			}
			var rpc struct {
				Method string
				ID     int
			}
			_ = json.NewDecoder(r.Body).Decode(&rpc)
			switch rpc.Method {
			case "initialize":
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}}}}`)
			case "notifications/initialized":
				w.WriteHeader(202)
			case "tools/list":
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"proof","inputSchema":{"type":"object"}}]}}`)
			default:
				t.Error("unexpected tool call")
				w.WriteHeader(400)
			}
		case "/resource":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": upstream.URL + "/mcp", "authorization_servers": []string{upstream.URL}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": upstream.URL, "authorization_endpoint": upstream.URL + "/authorize", "token_endpoint": upstream.URL + "/token", "registration_endpoint": upstream.URL + "/registration", "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true})
		case "/registration":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"client_id":"openfox-test"}`)
		case "/token":
			verifier := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(verifier[:]) != challenge || r.FormValue("resource") != upstream.URL+"/mcp" || r.FormValue("code") != "test-code" || r.FormValue("client_id") != "openfox-test" {
				t.Error("exchange did not bind the code to its client, challenge and resource")
				w.WriteHeader(400)
				return
			}
			exchanges++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	e := newAgEnvWithOptions(t, api.DispatchOptions{ImportSafeHTTP: safehttp.Options{RootCAs: roots, AllowAddr: func(ip net.IP) bool { return ip.IsLoopback() }}})
	actor, other := e.client(agManagerID), e.client(agOwnerID)
	space := e.createSpace("SHARED")
	a := e.createAgent(actor, space, "SHARED", nil)
	base := agentPath(space, a.ID) + "/mcp-servers"
	path := base + "/" + agNewID(t)
	input := agents.MCPInput{Config: mcpconnect.Config{Name: "oauth-demo", Transport: "http", AuthMode: "oauth", URL: upstream.URL + "/mcp"}}
	var row agents.MCPServer
	actor.must(200, "PUT", path, input, &row)
	if row.Status != "authorizing" {
		t.Fatal(row.Status)
	}
	start := func() string {
		var out struct {
			URL string `json:"authorize_url"`
		}
		actor.must(200, "POST", path+"/oauth/start", map[string]string{}, &out)
		u, err := url.Parse(out.URL)
		if err != nil || u.Host != strings.TrimPrefix(upstream.URL, "https://") || u.Query().Get("code_challenge_method") != "S256" {
			t.Fatal("invalid authorization URL")
		}
		challenge = u.Query().Get("code_challenge")
		return "/v1/mcp/oauth/callback?state=" + url.QueryEscape(u.Query().Get("state"))
	}
	rejected := start() + "&error=access_denied"
	actor.must(400, "GET", rejected, nil, nil)
	status := func(want string) {
		var out struct{ Status string }
		actor.must(200, "GET", path+"/oauth/status", nil, &out)
		if out.Status != want {
			t.Fatalf("status %s, want %s", out.Status, want)
		}
	}
	status("failed")
	callback := start() + "&code=test-code&iss=" + url.QueryEscape(upstream.URL)
	other.must(400, "GET", callback, nil, nil)
	status("pending")
	actor.must(200, "GET", callback, nil, nil)
	status("connected")
	actor.must(400, "GET", callback, nil, nil)
	if exchanges != 1 {
		t.Fatalf("%d exchanges", exchanges)
	}
	var list agents.MCPList
	r := actor.must(200, "GET", base, nil, &list)
	if len(list.Servers) != 1 || list.Servers[0].Status != "connected" || len(list.Servers[0].Tools) != 1 || strings.Contains(string(r.Body), token) {
		t.Fatal("unexpected manifest or exposed bearer")
	}
}
