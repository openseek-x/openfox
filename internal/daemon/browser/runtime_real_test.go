package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	scope "openfox/internal/browser"
	"openfox/internal/runtime"
	"openfox/internal/runtime/cliagent"
	"openfox/internal/runtime/codex"
)

// The center is a metadata fixture; the Go broker/socket, Electron host, CDP,
// shared MCP bridge and installed model adapter all execute production code.
// Real center ownership/RLS/Run checks are covered by the PostgreSQL suite.
type nativeCenter struct {
	mu      sync.Mutex
	binding scope.Binding
}

type fixtureLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (l *fixtureLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if left := 128*1024 - l.text.Len(); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = l.text.Write(p)
	}
	return n, nil
}
func (l *fixtureLog) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.text.String() }

func (c *nativeCenter) ClaimBrowser(_ context.Context, _ string, _ string, _ string, identity scope.Identity) (scope.Binding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.binding.HostInstanceID, c.binding.HostEpoch, c.binding.Revision = identity.HostInstanceID, identity.HostEpoch, identity.Revision
	c.binding.LeaseExpiresAt = time.Now().Add(30 * time.Second)
	return c.binding, nil
}
func (c *nativeCenter) HeartbeatBrowser(context.Context, string, string, scope.Identity) (scope.Binding, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.binding.LeaseExpiresAt = time.Now().Add(30 * time.Second)
	return c.binding, nil
}
func (c *nativeCenter) AuthorizeBrowser(_ context.Context, _ string, id string, input scope.AuthorizationInput) (scope.Authorization, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return scope.Authorization{Binding: c.binding, RunID: id, Generation: input.Generation}, nil
}

func TestRealNativeBrowserRuntimes(t *testing.T) {
	if os.Getenv("OPENFOX_BROWSER_REAL_NATIVE") != "1" || goruntime.GOOS != "darwin" {
		t.Skip("explicit macOS model/native-browser integration")
	}
	for _, name := range []string{"codex", cliagent.NameClaudeCode, cliagent.NameOpenCode} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			t.Cleanup(cancel)
			// macOS Unix socket paths are short; Go's test-name-based temporary
			// directory can exceed that limit before browser.sock is appended.
			dir, err := os.MkdirTemp("/tmp", "ofx-browser-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			root := filepath.Join(dir, "browser")
			socket := filepath.Join(root, "browser.sock")
			center := &nativeCenter{binding: scope.Binding{ID: bindingID, OrganizationID: org, ConversationID: conversation, OwnerUserID: owner, DeviceID: device}}
			broker := New(center, device)
			serveDone := make(chan error, 1)
			go func() { serveDone <- broker.Serve(ctx, socket) }()
			t.Cleanup(func() {
				cancel()
				if err := <-serveDone; err != nil {
					t.Errorf("broker: %v", err)
				}
			})
			codeBytes := make([]byte, 3)
			if _, err := rand.Read(codeBytes); err != nil {
				t.Fatal(err)
			}
			code := strings.ToUpper(hex.EncodeToString(codeBytes))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, `<!doctype html><title>OpenFox model browser fixture</title><style>body{margin:32px;background:white;font:20px sans-serif}button{padding:18px;font:20px sans-serif}canvas{display:block}</style><button id="reveal">Reveal verification</button><canvas width="700" height="400" aria-label="Verification image"></canvas><script>document.querySelector('button').onclick=()=>{document.querySelector('button').remove();const x=document.querySelector('canvas').getContext('2d');x.fillStyle='black';x.font='bold 56px monospace';x.fillText('%s',200,80);x.fillStyle='red';x.beginPath();x.arc(180,230,65,0,Math.PI*2);x.fill();x.fillStyle='blue';x.fillRect(430,165,130,130)}</script>`, code)
			}))
			defer server.Close()
			ready := filepath.Join(dir, "ready")
			config, _ := json.Marshal(map[string]string{"profile": filepath.Join(dir, "profile"), "root": root, "socket": socket, "ready": ready, "origin": server.URL, "org": org, "conversation": conversation, "owner": owner, "device": device, "binding": bindingID, "run": runID})
			configPath := filepath.Join(dir, "fixture.json")
			if err := os.WriteFile(configPath, config, 0600); err != nil {
				t.Fatal(err)
			}
			for start := time.Now(); ; {
				if _, err := os.Stat(socket); err == nil {
					break
				}
				if time.Since(start) > 5*time.Second {
					t.Fatal("socket did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			fixture, err := filepath.Abs("../../../apps/desktop/browser/runtime-fixture.cjs")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, "node", fixture, configPath)
			var fixtureOutput fixtureLog
			command.Stderr = &fixtureOutput
			command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
			command.WaitDelay = 7 * time.Second
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = stdin.Write([]byte("close\n"))
				_ = stdin.Close()
				if err := command.Wait(); err != nil {
					t.Errorf("fixture stopped: %v", err)
				}
			})
			for start := time.Now(); ; {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Since(start) > 15*time.Second {
					t.Fatalf("native browser did not become ready: %s", fixtureOutput.String())
				}
				time.Sleep(25 * time.Millisecond)
			}
			var adapter runtime.Adapter
			model := ""
			if name == "codex" {
				version := os.Getenv("OFD_REAL_CODEX_VERSION")
				if version == "" {
					t.Fatal("OFD_REAL_CODEX_VERSION required")
				}
				a := codex.New("")
				a.SupportedVersions = []string{version}
				a.HomeRoot = filepath.Join(dir, "codex")
				adapter = a
				model = os.Getenv("OPENFOX_BROWSER_VERIFY_MODEL")
			} else {
				a := cliagent.New(name, "")
				a.StateRoot = filepath.Join(dir, "runtime")
				adapter = a
			}
			var mu sync.Mutex
			calls := map[string]int{}
			run := Run{OrganizationID: org, ConversationID: conversation, ActorID: owner, ID: runID, Generation: 1}
			defer broker.EndRun(run)
			started := time.Now()
			execution, err := adapter.Start(ctx, runtime.RunRequest{
				WorkingDirectory: dir, ApprovalMode: "WORKSPACE_WRITE", Timeout: 2 * time.Minute, Model: model, RequireTools: true, Tools: Tools(false),
				Prompt: "Use only the four openfox_browser tools for this test, never shell, files, fetch, or other tools. The user has explicitly granted this Run control of one test website. List the tabs, snapshot the test page, click the button named Reveal verification exactly once using its fresh ref, then take a screenshot. Read the random code visible in that screenshot and identify the left and right colored shapes. Return one JSON object with code,left_color,left_shape,right_color,right_shape using English words. The code is not in this prompt or the accessibility snapshot; do not guess if you cannot see it.",
				ToolCall: func(ctx context.Context, tool, id string, args json.RawMessage) runtime.ToolResult {
					var action struct {
						Action string `json:"action"`
					}
					_ = json.Unmarshal(args, &action)
					mu.Lock()
					calls[tool+"/"+action.Action]++
					mu.Unlock()
					return broker.Call(ctx, run, tool, id, args)
				},
			})
			if err != nil {
				t.Fatalf("runtime start: %v", err)
			}
			for event := range execution.Events() {
				if strings.Contains(string(event.Payload), `"type":"image"`) || strings.Contains(string(event.Payload), `"mimeType":"image/png"`) {
					t.Error("image leaked into execution events")
				}
			}
			out, err := execution.Wait(ctx)
			if err != nil || out.Status != runtime.StatusSucceeded {
				t.Fatalf("model failed: status=%s err=%v code=%s detail=%s", out.Status, err, out.ErrorCode, runtime.OutputPreview(out.Error, 1000))
			}
			for _, expected := range []string{strings.ToLower(code), "red", "circle", "blue", "square"} {
				if !strings.Contains(strings.ToLower(out.Output), expected) {
					t.Fatalf("incorrect visual result: %s", runtime.OutputPreview(out.Output, 1500))
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if calls["openfox_browser_act/click"] != 1 || calls["openfox_browser_observe/screenshot"] < 1 || calls["openfox_browser_observe/snapshot"] < 1 {
				t.Fatalf("missing native workflow: %v", calls)
			}
			if evidence := os.Getenv("OPENFOX_BROWSER_EVIDENCE_DIR"); evidence != "" {
				body, _ := json.MarshalIndent(map[string]any{"status": "passed", "runtime": name, "transport": "Runtime -> MCP -> Go broker -> private socket -> Electron BrowserHost/CDP", "duration_ms": time.Since(started).Milliseconds(), "calls": calls, "response": out.Output}, "", "  ")
				if err := os.MkdirAll(evidence, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(evidence, name+"-native-result.json"), append(body, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
