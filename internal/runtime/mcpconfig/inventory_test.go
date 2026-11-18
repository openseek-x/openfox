package mcpconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUserInventoryNeverExecutesOrReportsConfiguration(t *testing.T) {
	for _, tt := range []struct{ runtime, file, body, transport string }{
		{"codex", ".codex/config.toml", "[mcp_servers.personal]\ncommand=\"secret-program\"\nargs=[\"secret-argument\"]\nenabled=false\n", "STDIO"},
		{"claude-code", ".claude.json", `{"mcpServers":{"personal":{"type":"sse","url":"https://secret-host/","headers":{"Authorization":"secret-value"},"disabled":true}},"projects":{"some-project":{"mcpServers":{"other":{}}}}}`, "SSE"},
		{"opencode", ".config/opencode/opencode.jsonc", "// hello\n{\"mcp\":{/* disabled */\"personal\":{\"type\":\"remote\",\"url\":\"https://secret-host//x\",\"enabled\":false,},},}\n", "HTTP"},
		{"pi", ".pi/agent/mcp.json", `{"mcpServers":{"personal":{"url":"https://secret-host","enabled":false,"headers":{"Authorization":"!exit 99"}}}}`, "HTTP"},
	} {
		t.Run(tt.runtime, func(t *testing.T) {
			l := Location{Home: t.TempDir()}
			writeAt(t, filepath.Join(l.Home, tt.file), tt.body)
			i := Read(tt.runtime, l)
			if len(i.Servers) != 1 || i.Status != "ok" || i.Servers[0] != (Metadata{Name: "personal", Source: "USER_CONFIG", Transport: tt.transport}) {
				t.Fatalf("metadata %+v", i)
			}
			b, e := json.Marshal(i)
			if e != nil || strings.Contains(string(b), "secret") || strings.Contains(string(b), "other") {
				t.Fatal("configuration leaked")
			}
			if _, err := Inherited(tt.runtime, "personal", l); err == nil {
				t.Fatal("inherited disabled server")
			}
		})
	}
}

func TestInventoryOverridesAndBounds(t *testing.T) {
	h := t.TempDir()
	l := Location{Home: h}
	if i := Read("pi", l); i.Status != "missing" {
		t.Fatal(i.Status)
	}
	target := filepath.Join(h, ".pi", "agent", "mcp.json")
	writeAt(t, target, `{"mcpServers":{"a":{"command":"ok"}}}`)
	if _, err := Inherited("pi", "gone", l); err == nil {
		t.Fatal("missing server inherited")
	}
	for _, tc := range []struct{ body, status string }{{"{", "invalid"}, {strings.Repeat("x", MaxConfigBytes+1), "too_large"}, {`{"mcpServers":{}}`, "ok"}} {
		writeAt(t, target, tc.body)
		if i := Read("pi", l); i.Status != tc.status {
			t.Fatalf("status %s", i.Status)
		}
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h, "arbitrary"), target); err != nil {
		t.Fatal(err)
	}
	if i := Read("pi", l); i.Status != "unavailable" {
		t.Fatal(i.Status)
	}
	writeAt(t, filepath.Join(h, ".config/opencode/opencode.json"), `{"mcp":{"visible":{"type":"local","command":["example"],"enabled":false}}}`)
	extra := filepath.Join(h, "extra.json")
	writeAt(t, extra, `{"mcp":{"visible":{"enabled":true}}}`)
	l.Env = func(key string) string {
		if key == "OPENCODE_CONFIG" {
			return extra
		}
		return ""
	}
	i := Read("opencode", l)
	if i.Status != "ok" || len(i.Servers) != 1 || !i.Servers[0].Enabled {
		t.Fatalf("override %+v", i)
	}
	entry, err := Inherited("opencode", "visible", l)
	if err != nil || entry["type"] != "local" {
		t.Fatalf("override not inherited")
	}
}

func TestInheritedCodexResolvesOnlyNamedLocalVariables(t *testing.T) {
	loc := Location{Home: t.TempDir(), Env: func(k string) string {
		switch k {
		case "FIXTURE_TOKEN":
			return "synthetic-secret"
		case "FIXTURE_ENV":
			return "fixture-child"
		}
		return ""
	}}
	path := filepath.Join(loc.Home, ".codex", "config.toml")
	writeAt(t, path, "[mcp_servers.remote]\nurl=\"http://localhost/mock\"\nbearer_token_env_var=\"FIXTURE_TOKEN\"\ndefault_tools_approval_mode=\"prompt\"\n[mcp_servers.local]\ncommand=\"fixture\"\nenv_vars=[\"FIXTURE_ENV\"]\n")
	r, e := Inherited("codex", "remote", loc)
	if e != nil || r["default_tools_approval_mode"] != "prompt" || r["http_headers"].(map[string]any)["Authorization"] != "Bearer synthetic-secret" {
		t.Fatal("configured per-tool policy or API key was lost")
	}
	l, e := Inherited("codex", "local", loc)
	if e != nil || l["env"].(map[string]any)["FIXTURE_ENV"] != "fixture-child" {
		t.Fatal("selected environment value missing")
	}
	i, _ := json.Marshal(Read("codex", loc))
	if strings.Contains(string(i), "fixture-child") || strings.Contains(string(i), "FIXTURE_TOKEN") {
		t.Fatal("public inventory contains executable configuration")
	}
}
