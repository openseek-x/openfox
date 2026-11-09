package cliagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtime/mcpconfig"
)

func TestMCPIsolatedCLIConfigurations(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": home, "XDG_DATA_HOME": home, "PI_CODING_AGENT_DIR": filepath.Join(home, "pi"), "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "", "OPENCODE_AUTH_CONTENT": ""}
	write := func(file, body string) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(file, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(filepath.Join(home, "opencode", "opencode.json"), `{"mcp":{"unauthorized":{"type":"local","command":["never"]}},"model":"provider/demo","provider":{"fixture":{"options":{"baseURL":"http://synthetic.invalid"}}}}`)
	write(filepath.Join(home, "opencode", "auth.json"), `{"fixture":{"type":"api","key":"fixture-model-key"},"malicious":{"type":"wellknown","key":"provider","token":"not-imported"}}`)
	write(filepath.Join(home, "pi", "mcp.json"), `{"mcpServers":{"unauthorized":{"command":"never"}}}`)
	for _, name := range []string{NameClaudeCode, NameOpenCode, NamePi} {
		t.Run(name, func(t *testing.T) {
			adapter := New(name, "")
			adapter.Environment = env
			adapter.StateRoot = t.TempDir()
			root := t.TempDir()
			req := runtime.RunRequest{WorkingDirectory: t.TempDir(), MCPServers: mcpconfig.Servers{"chosen": mcpconfig.Native(name, "", nil, nil, "http://127.0.0.1:2/selected")}}
			got, err := adapter.proto.build(adapter, req, nil, root, nil)
			if err != nil {
				t.Fatal(err)
			}
			var file string
			switch name {
			case NameClaudeCode:
				file = filepath.Join(root, "mcp.json")
				if !strings.Contains(strings.Join(got.args, " "), "--strict-mcp-config") || !strings.Contains(strings.Join(got.args, " "), "mcp__chosen") {
					t.Fatal("missing Claude selection")
				}
			case NameOpenCode:
				file = got.env["OPENCODE_CONFIG"]
				if got.env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" || got.env["XDG_CONFIG_HOME"] != root {
					t.Fatal("OpenCode config not isolated")
				}
				content, e := os.ReadFile(filepath.Join(got.env["XDG_DATA_HOME"], "opencode", "auth.json"))
				if e != nil || !strings.Contains(string(content), "fixture-model-key") || strings.Contains(string(content), "wellknown") {
					t.Fatal("model auth selection")
				}
			case NamePi:
				file = filepath.Join(root, "mcp.json")
				if got.env["PI_CODING_AGENT_DIR"] != root || !strings.Contains(strings.Join(got.args, " "), "--no-approve") {
					t.Fatal("Pi project config not isolated")
				}
			}
			data, e := os.ReadFile(file)
			st, se := os.Stat(file)
			if e != nil || se != nil || st.Mode().Perm() != 0600 || !strings.Contains(string(data), `"chosen"`) || strings.Contains(string(data), "unauthorized") {
				t.Fatal("wrong config or permissions")
			}
			if name == NameOpenCode && !strings.Contains(string(data), "provider/demo") {
				t.Fatal("OpenCode model config lost")
			}
		})
	}
}

// The actual installed Pi and OpenCode CLIs discover only a loopback fixture.
// There is no model call and no personal CLI data is read or modified.
func TestRealMCPCLIIsolation(t *testing.T) {
	if os.Getenv("OFD_REAL_RUNTIME_MCP") != "1" {
		t.Skip("set OFD_REAL_RUNTIME_MCP=1 to check the installed Pi and OpenCode")
	}
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var m struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params struct {
				Version string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&m) != nil {
			w.WriteHeader(400)
			return
		}
		if m.ID == nil {
			w.WriteHeader(202)
			return
		}
		var result any
		switch m.Method {
		case "initialize":
			connections.Add(1)
			result = map[string]any{"protocolVersion": m.Params.Version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{}}
		default:
			result = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}))
	defer server.Close()
	for _, name := range []string{NameOpenCode, NamePi} {
		t.Run(name, func(t *testing.T) {
			start := connections.Load()
			bin, e := exec.LookPath(name)
			if e != nil {
				t.Fatal(e)
			}
			root, project, home := t.TempDir(), t.TempDir(), t.TempDir()
			ad := New(name, bin)
			ad.StateRoot = t.TempDir()
			ad.Environment = map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "XDG_STATE_HOME": filepath.Join(home, "state"), "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "", "OPENCODE_AUTH_CONTENT": "", "PI_CODING_AGENT_DIR": filepath.Join(home, "pi")}
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unselected MCP contacted"); w.WriteHeader(403) }))
			defer bad.Close()
			var configKey string
			var files []string
			if name == NamePi {
				configKey = "mcpServers"
				files = []string{filepath.Join(home, "pi", "mcp.json"), filepath.Join(project, ".pi", "mcp.json")}
			} else {
				configKey = "mcp"
				files = []string{filepath.Join(home, "config", "opencode", "opencode.json"), filepath.Join(project, "opencode.json")}
			}
			trap, _ := json.Marshal(map[string]any{configKey: map[string]any{"unselected": mcpconfig.Native(name, "", nil, nil, bad.URL)}})
			for _, file := range files {
				if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(file, trap, 0600); e != nil {
					t.Fatal(e)
				}
			}
			req := runtime.RunRequest{WorkingDirectory: project, MCPServers: mcpconfig.Servers{"approved": mcpconfig.Native(name, "", nil, nil, server.URL)}}
			inv, e := ad.proto.build(ad, req, nil, root, nil)
			if e != nil {
				t.Fatal(e)
			}
			env := ad.Environment
			for k, v := range inv.env {
				env[k] = v
			}
			env["PATH"] = os.Getenv("PATH")
			env["OPENCODE_DISABLE_MODELS_FETCH"] = "1"
			env["OPENCODE_DISABLE_AUTOUPDATE"] = "1"
			env["PI_OFFLINE"] = "1"
			args := []string{"mcp", "list"}
			if name == NamePi {
				args = append(args, "--json")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Dir = project
			cmd.Env = environmentSlice(env)
			var output strings.Builder
			cmd.Stdout = &output
			cmd.Stderr = io.Discard
			if e = cmd.Run(); e != nil || !strings.Contains(output.String(), "approved") || strings.Contains(output.String(), "unselected") || connections.Load() == start {
				t.Fatalf("selected server not discovered in isolated config: %v", e)
			}
		})
	}
}
