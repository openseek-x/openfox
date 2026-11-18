package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"openfox/internal/runtime"
)

type lifecycleReceipt struct {
	Root, ProxyPort         string
	Owner, Gateway, ACP, RG int
	Cancelled               bool
}

// A real native rg is stopped after creation, while its result pipe is still
// owned by Hermes. Only test code is instrumented; the verified installation,
// sandbox, official ACP transport and production cleanup are unchanged.
func TestNativeHermesSearchLifecycleHelper(t *testing.T) {
	mode := os.Getenv("OPENFOX_HERMES_LIFECYCLE_HELPER")
	if mode == "" {
		t.Skip("subprocess helper only")
	}
	original := bootstrap
	defer func() { bootstrap = original }()
	bootstrap = strings.Replace(bootstrap, "children.add(process)\n            return process", `children.add(process)
            os.kill(process.pid, signal.SIGSTOP)
            pathlib.Path(options["run_root"], "rg.pid").write_text(str(process.pid))
            return process`, 1)
	bootstrap = strings.Replace(bootstrap, `sys.argv = ["hermes", *command]`, `pathlib.Path(options["run_root"], command[0]+".pid").write_text(str(os.getpid()))
sys.argv = ["hermes", *command]`, 1)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "search.txt"), []byte(strings.Repeat("LIFECYCLE_SENTINEL\n", 250000)), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools    []json.RawMessage
			Messages []struct{ Role string }
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer controlled-lifetime-key" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid controlled request")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta any, finish any) {
			b, _ := json.Marshal(map[string]any{"id": "lifetime", "object": "chat.completion.chunk", "model": "r05-lifetime-fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		if len(body.Tools) > 0 {
			if len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Role == "tool" {
				t.Error("stopped rg must not complete before cancellation")
				w.WriteHeader(500)
				return
			}
			args, _ := json.Marshal(map[string]string{"pattern": "LIFECYCLE_SENTINEL", "path": work})
			chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "lifetime-tool", "type": "function", "function": map[string]string{"name": "search_files", "arguments": string(args)}}}}, nil)
			chunk(map[string]any{}, "tool_calls")
		} else {
			chunk(map[string]string{"role": "assistant", "content": "lifetime"}, nil)
			chunk(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	client := upstream.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	client.Transport = transport
	a := New()
	a.testClient = client
	var h *nativeHome
	a.testObserve = func(home *nativeHome) { h = home }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	e, err := a.Start(ctx, runtime.RunRequest{ApprovalMode: "READ_ONLY", Prompt: "search workspace", WorkingDirectory: work, Model: "r05-lifetime-fixture", EnvironmentAllowlist: []string{"HERMES_API_KEY", "HERMES_BASE_URL", "HERMES_CONTEXT_LENGTH"}, Environment: map[string]string{"HERMES_API_KEY": "controlled-lifetime-key", "HERMES_BASE_URL": "https://example.com/v1", "HERMES_CONTEXT_LENGTH": "65536"}, Timeout: 55 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Cancel(context.Background()); _, _ = e.Wait(context.Background()) }()
	go func() {
		for range e.Events() {
		}
	}()
	receipt := lifecycleReceipt{Root: h.root, ProxyPort: h.proxyPort, Owner: os.Getpid(), Gateway: h.gateway.Process.Pid}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rgRaw, _ := os.ReadFile(filepath.Join(h.root, "rg.pid"))
		acpRaw, _ := os.ReadFile(filepath.Join(h.root, "acp.pid"))
		_, _ = fmt.Sscan(string(rgRaw), &receipt.RG)
		_, _ = fmt.Sscan(string(acpRaw), &receipt.ACP)
		if receipt.RG > 0 && receipt.ACP > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if receipt.RG == 0 || receipt.ACP == 0 {
		t.Fatal("real ACP did not start controlled rg")
	}
	save := func() {
		raw, _ := json.Marshal(receipt)
		path := os.Getenv("OPENFOX_HERMES_LIFECYCLE_RECEIPT")
		if err = os.WriteFile(path+".new", raw, 0600); err == nil {
			err = os.Rename(path+".new", path)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	save()
	if mode == "kill" {
		<-ctx.Done()
		t.Fatal("parent did not kill owned helper")
	}
	if err = e.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := e.Wait(ctx)
	if err != nil || result.Status != runtime.StatusCancelled {
		t.Fatalf("cancel status=%s err=%v", result.Status, err)
	}
	receipt.Cancelled = true
	save()
}

func lifecycleIdentity(pid int) string {
	raw, err := exec.Command("/bin/ps", "-p", fmt.Sprint(pid), "-o", "lstart=", "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func TestNativeHermesSearchLifecycle(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	for _, mode := range []string{"cancel", "kill"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "receipt.json")
			log, err := os.Create(filepath.Join(dir, "helper.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeHermesSearchLifecycleHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "OPENFOX_HERMES_LIFECYCLE_HELPER="+mode, "OPENFOX_HERMES_LIFECYCLE_RECEIPT="+path)
			cmd.Stdout = log
			cmd.Stderr = log
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				raw, _ := os.ReadFile(log.Name())
				t.Logf("owned helper: %s", raw)
			}()
			var receipt lifecycleReceipt
			deadline := time.Now().Add(40 * time.Second)
			for time.Now().Before(deadline) {
				raw, _ := os.ReadFile(path)
				if json.Unmarshal(raw, &receipt) == nil && receipt.RG > 0 {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if receipt.RG == 0 {
				t.Fatal("helper did not reach in-flight rg")
			}
			identities := map[int]string{}
			for _, pid := range []int{receipt.Gateway, receipt.ACP, receipt.RG} {
				identities[pid] = lifecycleIdentity(pid)
			}
			defer func() {
				for pid, identity := range identities {
					if identity != "" && lifecycleIdentity(pid) == identity {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
				_ = os.RemoveAll(receipt.Root)
			}()
			if mode == "kill" {
				for pid, identity := range identities {
					if identity == "" {
						t.Fatalf("owned native pid %d missing before owner death", pid)
					}
				}
				if err = cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = cmd.Wait()
			} else {
				if err = cmd.Wait(); err != nil {
					t.Fatalf("cancel helper failed: %v", err)
				}
				raw, _ := os.ReadFile(path)
				_ = json.Unmarshal(raw, &receipt)
				if !receipt.Cancelled {
					t.Fatal("ACP did not report cancelled")
				}
			}
			deadline = time.Now().Add(7 * time.Second)
			gone := func() bool {
				for pid, identity := range identities {
					if identity != "" && lifecycleIdentity(pid) == identity {
						return false
					}
				}
				_, e := os.Stat(receipt.Root)
				return os.IsNotExist(e)
			}
			for time.Now().Before(deadline) && !gone() {
				time.Sleep(50 * time.Millisecond)
			}
			remaining := map[int]string{}
			for pid, identity := range identities {
				if identity != "" && lifecycleIdentity(pid) == identity {
					remaining[pid] = identity
				}
			}
			_, rootErr := os.Stat(receipt.Root)
			c, portErr := net.DialTimeout("tcp", "127.0.0.1:"+receipt.ProxyPort, 200*time.Millisecond)
			if c != nil {
				_ = c.Close()
			}
			t.Logf("mode=%s owner=%d gateway=%d acp=%d rg=%d remaining=%v rootGone=%v proxyClosed=%v", mode, receipt.Owner, receipt.Gateway, receipt.ACP, receipt.RG, remaining, os.IsNotExist(rootErr), portErr != nil)
			if len(remaining) > 0 || !os.IsNotExist(rootErr) || portErr == nil {
				t.Fatal("owned native execution was not fully reclaimed")
			}
		})
	}
}
