package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

func geminiNativeAdapter(t *testing.T) *Adapter {
	t.Helper()
	if os.Getenv("OPENFOX_TEST_GEMINI_NATIVE") != "1" {
		t.Skip("fixed official Node/Gemini with isolated controlled model required")
	}
	a := NewGemini(os.Getenv("OPENFOX_GEMINI_PATH"))
	a.StateRoot = t.TempDir()
	return a
}
func geminiNativeRequest(work string) runtime.RunRequest {
	return runtime.RunRequest{Prompt: "Read the authorized ordinary file and explain it.", Model: "gemini-2.5-flash", WorkingDirectory: work, ApprovalMode: runtimepolicy.ReadOnly, CollaborationMode: runtimepolicy.CollaborationPlan, Environment: map[string]string{"GEMINI_API_KEY": "GOOGLE-SYNTHETIC-NATIVE-ONLY"}, EnvironmentAllowlist: []string{"GEMINI_API_KEY"}, Timeout: 20 * time.Second}
}
func TestNativeGeminiReadAndGate(t *testing.T) {
	a := geminiNativeAdapter(t)
	work, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(work, "nested"), 0700); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(work, "nested", "ordinary.txt"), []byte("NATIVE_GEMINI_SENTINEL"), 0600)
	os.MkdirAll(filepath.Join(work, ".gemini"), 0700)
	os.WriteFile(filepath.Join(work, ".gemini", "settings.json"), []byte(`{"hooksConfig":{"enabled":true},"tools":{"discoveryCommand":"/bin/echo EVIL_DISCOVERY"},"mcpServers":{"evil":{"command":"/bin/echo","args":["EVIL_MCP"]}}}`), 0600)
	os.WriteFile(filepath.Join(work, "GEMINI.md"), []byte("EVIL_PROJECT_MEMORY"), 0600)
	os.WriteFile(filepath.Join(work, ".env"), []byte("GEMINI_MODEL=EVIL_MODEL"), 0600)
	for _, tc := range []struct {
		name, tool, args string
		fail             bool
	}{
		{"read", "read_file", `{"file_path":"nested/ordinary.txt"}`, false},
		{"read-default", "read_file", `{"file_path":"nested/ordinary.txt"}`, false},
		{"search", "grep_search", `{"pattern":"NATIVE_GEMINI","dir_path":"nested"}`, false},
		{"glob", "glob", `{"pattern":"nested/*.txt"}`, false},
		{"list", "list_directory", `{"dir_path":"nested"}`, false},
		{"parent-glob", "glob", `{"pattern":"../home/**/*"}`, true},
		{"private-at", "read_file", `{"file_path":"@../home/.gemini/tmp/work/canary"}`, true},
		{"unknown", "run_shell_command", `{"command":"/bin/echo BAD"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			observed := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "EVIL_PROJECT_MEMORY") || strings.Contains(string(b), "EVIL_DISCOVERY") || strings.Contains(string(b), "EVIL_MCP") {
					t.Error("project startup metadata reached model")
				}
				if r.Header.Get("X-Goog-Api-Key") != "GOOGLE-SYNTHETIC-NATIVE-ONLY" {
					t.Error("upstream credential mismatch")
				}
				mu.Lock()
				calls++
				n := calls
				observed = observed || strings.Contains(string(b), "NATIVE_GEMINI_SENTINEL") || strings.Contains(string(b), "ordinary.txt")
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				parts := `{"text":"Native verified\u2028second line\u2029end"}`
				if n == 1 {
					parts = `{"functionCall":{"name":"` + tc.tool + `","args":` + tc.args + `}}`
				}
				// Use literal Unicode separators to cover the SDK's JS regex limitation.
				frame := geminiTestFrame(parts, "STOP")
				frame = strings.ReplaceAll(strings.ReplaceAll(frame, `\u2028`, "\u2028"), `\u2029`, "\u2029")
				for _, ch := range []byte("data: " + frame + "\n\n") {
					w.Write([]byte{ch})
					http.NewResponseController(w).Flush()
				}
			}))
			defer server.Close()
			a.geminiHTTP = server.Client()
			a.geminiEndpoint = server.URL
			req := geminiNativeRequest(work)
			if tc.name == "read-default" {
				req.CollaborationMode = runtimepolicy.CollaborationDefault
				req.ApprovalMode = runtimepolicy.FollowRuntime
			}
			execution, e := a.Start(context.Background(), req)
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan []runtime.Event, 1)
			go func() {
				var events []runtime.Event
				for event := range execution.Events() {
					events = append(events, event)
				}
				done <- events
			}()
			result, e := execution.Wait(context.Background())
			events := <-done
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("status=%s exit=%d calls=%d events=%d error=%s output=%s", result.Status, result.ExitCode, calls, len(events), result.Error, result.Output)
			if tc.fail {
				if result.Status != runtime.StatusFailed || calls != 1 {
					t.Fatalf("unsafe call was not rejected %+v", result)
				}
			} else {
				if result.Status != runtime.StatusSucceeded || calls != 2 || !observed || !strings.Contains(result.Output, "Native verified") {
					t.Fatalf("native read tool did not complete %+v", result)
				}
			}
			entries, _ := os.ReadDir(a.StateRoot)
			if len(entries) != 0 {
				t.Fatalf("Run state leaked %v", entries)
			}
		})
	}
}
func TestNativeGeminiCancel(t *testing.T) {
	a := geminiNativeAdapter(t)
	started := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	a.geminiHTTP = server.Client()
	a.geminiEndpoint = server.URL
	execution, e := a.Start(context.Background(), geminiNativeRequest(""))
	if e != nil {
		t.Fatal(e)
	}
	go func() {
		for range execution.Events() {
		}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("native did not request model")
	}
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if e = execution.Cancel(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancel did not close upstream stream")
	}
	result, e := execution.Wait(ctx)
	if e != nil || result.Status != runtime.StatusCancelled {
		t.Fatalf("%+v %v", result, e)
	}
	entries, _ := os.ReadDir(a.StateRoot)
	if len(entries) != 0 {
		b, _ := json.Marshal(entries)
		t.Fatalf("state leaked %s", b)
	}
	t.Logf("actual native cancellation completed in %s", time.Since(began))
}
func TestNativeGeminiDetect(t *testing.T) {
	a := geminiNativeAdapter(t)
	caps, e := a.Detect(context.Background())
	if e != nil || !caps.ProtocolReady || caps.Runnable || caps.SupportsResume || caps.Login != runtime.LoginUnknown {
		t.Fatalf("%+v %v", caps, e)
	}
	t.Log(fmt.Sprintf("fixed native %s, authentication remains %s", caps.Version, caps.Login))
}

func TestNativeGeminiSandbox(t *testing.T) {
	a := geminiNativeAdapter(t)
	install, e := a.geminiInstallation(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	home, e := geminiPrepareHome(root, install, "")
	if e != nil {
		t.Fatal(e)
	}
	os.Mkdir(home.Work, 0700)
	os.WriteFile(filepath.Join(home.Work, "ordinary"), []byte("allowed"), 0600)
	outside, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(outside, "canary"), []byte("outside"), 0600)
	paths, _ := json.Marshal(map[string]string{"normal": filepath.Join(home.Work, "ordinary"), "outside": filepath.Join(outside, "canary"), "optional": filepath.Join(install.Root, "node_modules", "@github", "keytar", "package.json"), "write": filepath.Join(home.Work, "forbidden")})
	code := `const fs=require('node:fs'),cp=require('node:child_process');const paths=` + string(paths) + `;const out={};for(const[k,p]of Object.entries(paths)){try{if(k==='write')fs.writeFileSync(p,'bad');else fs.readFileSync(p);out[k]='OK'}catch(e){out[k]=e.code}}for(const p of ['/bin/sh','/bin/echo','/bin/launchctl']){const r=cp.spawnSync(p,['-c','exit 0']);out[p]=r.error?.code??'OK'}console.log(JSON.stringify(out))`
	cmd := exec.Command("/usr/bin/sandbox-exec", "-f", home.Sandbox, install.Node, "-e", code)
	cmd.Dir = home.Work
	cmd.Env = environmentSlice(home.Env)
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, b)
	}
	var got map[string]string
	if json.Unmarshal(b, &got) != nil {
		t.Fatalf("invalid sandbox output %s", b)
	}
	t.Logf("actual sandbox: %s", b)
	if got["normal"] != "OK" {
		t.Fatal("positive file control denied")
	}
	for _, key := range []string{"outside", "optional", "write", "/bin/sh", "/bin/echo", "/bin/launchctl"} {
		if got[key] != "EPERM" {
			t.Errorf("%s should be EPERM: %s", key, got[key])
		}
	}
}

func TestNativeGeminiAuthenticationAndRecovery(t *testing.T) {
	a := geminiNativeAdapter(t)
	for _, status := range []int{401, 403, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if status == 429 {
						w.Header().Set("Retry-After", "1")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":{"code":%d,"message":"DO-NOT-LEAK-KEY-OR-UPSTREAM-DETAILS","status":"%s"}}`, status, map[int]string{401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 429: "RESOURCE_EXHAUSTED"}[status])
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+geminiTestFrame(`{"text":"retry recovered"}`, "STOP")+"\n\n")
			}))
			defer server.Close()
			a.geminiHTTP = server.Client()
			a.geminiEndpoint = server.URL
			req := geminiNativeRequest("")
			execution, e := a.Start(context.Background(), req)
			if e != nil {
				t.Fatal(e)
			}
			go func() {
				for range execution.Events() {
				}
			}()
			result, e := execution.Wait(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("status=%d actual=%s calls=%d error=%s", status, result.Status, calls, result.Error)
			if strings.Contains(result.Error, "DO-NOT-LEAK") || strings.Contains(result.Error, "GOOGLE-SYNTHETIC") {
				t.Fatal("secret upstream error escaped")
			}
			if status == 429 {
				if result.Status != runtime.StatusSucceeded || calls < 2 || !strings.Contains(result.Output, "retry recovered") {
					t.Fatalf("retry failed %+v", result)
				}
			} else {
				if result.Status != runtime.StatusFailed || !strings.Contains(result.Error, fmt.Sprint(status)) || calls != 1 {
					t.Fatalf("authentication classification failed %+v", result)
				}
			}
		})
	}
}
