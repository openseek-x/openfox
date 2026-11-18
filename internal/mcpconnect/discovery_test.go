package mcpconnect

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openfox/internal/platform/safehttp"
)

func TestBoundedDiscovery(t *testing.T) {
	var methods []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			ID     int
			Params map[string]any
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
		}
		methods = append(methods, req.Method)
		if r.Header.Get("Authorization") != "Bearer token" || r.Method != "POST" || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Error("missing request headers")
		}
		if req.Method != "initialize" && (r.Header.Get("MCP-Session-ID") != "bound-1" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18") {
			t.Error("missing session headers")
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("MCP-Session-Id", "bound-1")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{},"resources":{}}}}`)
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			if req.Params["cursor"] == nil {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message\ndata: "+`{"jsonrpc":"2.0","method":"notifications/progress"}`+"\n\nevent: message\ndata: "+`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"find","annotations":{"readOnlyHint":true},"inputSchema":{"type":"object"}}],"nextCursor":"p2"}}`+"\n\n")
			} else {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":3,"result":{"tools":[]}}`)
			}
		case "resources/list":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":4,"result":{"resources":[{"uri":"file:///not-fetched","name":"guide","mimeType":"text/plain"}]}}`)
		default:
			t.Error("unexpected MCP operation")
		}
	}))
	defer srv.Close()
	c := Config{Name: "test", Transport: "http", URL: srv.URL, AuthMode: "api_key", Credential: "token"}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	m, e := Discover(context.Background(), safehttp.Options{AllowAddr: func(ip net.IP) bool { return ip.IsLoopback() }, RootCAs: roots}, c)
	if e != nil || len(m.Tools) != 1 || !m.Tools[0].ReadOnly || len(m.Resources) != 1 || m.Resources[0].MimeType != "text/plain" || len(methods) != 5 {
		t.Fatalf("manifest=%+v methods=%v error=%v", m, methods, e)
	}
	if _, e = Discover(context.Background(), safehttp.Options{}, c); e == nil {
		t.Fatal("public discovery allowed loopback")
	}
}

func TestConfigMaskBinding(t *testing.T) {
	c, _ := (Config{Name: "one", Transport: "http", URL: "https://one.test/mcp", AuthMode: "api_key", Credential: "secret", Headers: map[string]string{"X-Token": "value"}}).Normalize()
	edit := c.Redacted()
	edit.Name = "two"
	v, e := edit.Merge(c)
	if e != nil || v.Headers["X-Token"] != "value" || v.Credential != "secret" {
		t.Fatalf("same target: %v", e)
	}
	edit.URL = "https://two.test/mcp"
	if _, e := edit.Merge(c); e == nil {
		t.Fatal("rebound a masked key")
	}
	local, _ := (Config{Name: "local", Transport: "stdio", Command: "node", Args: []string{"--token", "password", "first.js"}}).Normalize()
	edit = local.Redacted()
	edit.Args[2] = "attacker.js"
	if _, e = edit.Merge(local); e == nil {
		t.Fatal("rebound masked argv")
	}
	for _, c := range []Config{
		{Name: "bad", Transport: "http", AuthMode: "oauth", URL: "https://one.test/mcp?token=foo"},
		{Name: "bad", Transport: "http", URL: "https://one.test/mcp", Headers: map[string]string{"Connection": "upgrade"}},
		{Name: "bad", Transport: "http", URL: "http://localhost"},
		{Name: "bad", Transport: "stdio", Scope: Public, Command: "sh"},
		{Name: "bad", Transport: "http", URL: "https://one.test/mcp", Headers: map[string]string{"x": "a\r\nb"}},
	} {
		if _, e = c.Normalize(); e == nil {
			t.Fatalf("accepted invalid config %+v", c)
		}
	}
}
