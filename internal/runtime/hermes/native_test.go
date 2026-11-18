package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// Opt-in tests exercise the actual pinned native interpreter/source. Their
// model is a real local TLS fixture, never a paid or authenticated provider.
func TestNativeHermes(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires the approved isolated Hermes installation")
	}
	for _, scenario := range []string{"basic", "retry", "empty", "truncated", "read", "search", "search_plan", "write", "patch", "cancel", "secret"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			var observed atomic.Bool
			var observedPlan atomic.Bool
			entered, disconnected := make(chan struct{}, 1), make(chan struct{}, 1)
			work, _ := filepath.EvalSymlinks(t.TempDir())
			if err := os.WriteFile(filepath.Join(work, "read.txt"), []byte("READ_SENTINEL"), 0600); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer native-test-canary" {
					t.Error("upstream identity mismatch")
					w.WriteHeader(400)
					return
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				var body struct {
					Stream   bool
					Model    string
					Tools    []json.RawMessage
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				if json.Unmarshal(data, &body) != nil || body.Model != "r05-native-fixture" {
					t.Error("model mismatch")
				}
				attempt := calls.Add(1)
				if scenario == "retry" && attempt == 1 {
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if scenario == "empty" {
					return
				}
				chunk := func(delta any, finish any) {
					b, _ := json.Marshal(map[string]any{"id": "r05", "object": "chat.completion.chunk", "model": body.Model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
					fmt.Fprintf(w, "data: %s\n\n", b)
					w.(http.Flusher).Flush()
				}
				main := len(body.Tools) > 0
				if main && scenario == "search_plan" {
					for _, message := range body.Messages {
						if strings.Contains(string(message.Content), "Plan only.") {
							observedPlan.Store(true)
						}
					}
				}
				lastTool := len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Role == "tool"
				if main && scenario == "cancel" {
					chunk(map[string]string{"role": "assistant", "content": "waiting"}, nil)
					entered <- struct{}{}
					<-r.Context().Done()
					disconnected <- struct{}{}
					return
				}
				if main && lastTool {
					content := string(body.Messages[len(body.Messages)-1].Content)
					t.Logf("native tool result=%s", content)
					if (scenario == "read" || (scenario == "search" || scenario == "search_plan")) && strings.Contains(content, "READ_SENTINEL") {
						observed.Store(true)
					}
					if (scenario == "write" && strings.Contains(content, "Tool 'write_file' does not exist")) || (scenario == "patch" && strings.Contains(content, "Tool 'patch' does not exist")) {
						observed.Store(true)
					}
				}
				if main && !lastTool && (scenario == "read" || (scenario == "search" || scenario == "search_plan") || scenario == "write" || scenario == "patch") {
					name := "read_file"
					args := map[string]any{"path": filepath.Join(work, "read.txt")}
					if scenario == "search" || scenario == "search_plan" {
						name = "search_files"
						args = map[string]any{"path": work, "pattern": "READ_SENTINEL"}
					}
					if scenario == "write" {
						name = "write_file"
						args = map[string]any{"path": filepath.Join(work, "write.txt"), "content": "must not write"}
					}
					if scenario == "patch" {
						name = "patch"
						args = map[string]any{"path": filepath.Join(work, "read.txt"), "old_string": "READ_SENTINEL", "new_string": "must not patch"}
					}
					raw, _ := json.Marshal(args)
					chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "r05-tool", "type": "function", "function": map[string]string{"name": name, "arguments": string(raw)}}}}, nil)
					chunk(map[string]any{}, "tool_calls")
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				if scenario == "secret" {
					chunk(map[string]string{"role": "assistant", "content": "R05_NATIVE_OK native-test-"}, nil)
					chunk(map[string]string{"content": "canary"}, nil)
				} else {
					chunk(map[string]string{"role": "assistant", "content": "R05_NATIVE_OK"}, nil)
				}
				if scenario != "truncated" {
					chunk(map[string]string{}, "stop")
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer upstream.Close()
			client := upstream.Client()
			transport := client.Transport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			client.Transport = transport
			a := New()
			a.StateRoot = t.TempDir()
			a.testClient = client
			var owned *nativeHome
			a.testObserve = func(h *nativeHome) { owned = h }
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			approval, collaboration := "READ_ONLY", "DEFAULT"
			if scenario == "search_plan" {
				collaboration = "PLAN"
			}
			execution, err := a.Start(ctx, runtime.RunRequest{ApprovalMode: approval, CollaborationMode: collaboration, Prompt: "R05_BASIC", WorkingDirectory: work, Model: "r05-native-fixture", EnvironmentAllowlist: []string{"HERMES_API_KEY", "HERMES_BASE_URL", "HERMES_CONTEXT_LENGTH"}, Environment: map[string]string{"HERMES_API_KEY": "native-test-canary", "HERMES_BASE_URL": "https://example.com/api/v1", "HERMES_CONTEXT_LENGTH": "65536"}, Timeout: 60 * time.Second})
			if err != nil {
				if owned != nil {
					t.Logf("controlled gateway startup diagnostics: %s", owned.gatewayLog.Bytes())
				}
				t.Fatal(err)
			}
			if scenario == "cancel" {
				go func() {
					select {
					case <-entered:
						_ = execution.Cancel(context.Background())
					case <-ctx.Done():
					}
				}()
			}
			for event := range execution.Events() {
				if strings.Contains(event.Text, "native-test-canary") || strings.Contains(string(event.Payload), "native-test-canary") {
					t.Error("credential leaked")
				}
				t.Logf("event=%s text=%s", event.Type, event.Text)
			}
			result, err := execution.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("result=%+v calls=%d", result, calls.Load())
			if scenario == "search_plan" && !observedPlan.Load() {
				t.Error("planning scope was not sent to the native model request")
			}
			if strings.Contains(result.Output, "native-test-canary") {
				t.Error("split provider credential escaped output")
			}
			if scenario == "secret" && !strings.Contains(result.Output, "[REDACTED]") {
				t.Error("split key was not masked")
			}
			if scenario != "empty" && scenario != "truncated" && scenario != "cancel" {
				if result.Status != runtime.StatusSucceeded || !strings.Contains(result.Output, "R05_NATIVE_OK") {
					t.Fatalf("native completion failed: %+v", result)
				}
			} else if result.Status == runtime.StatusSucceeded {
				t.Fatalf("incomplete native SSE reported success: %+v", result)
			}
			if scenario == "read" || (scenario == "search" || scenario == "search_plan") || scenario == "write" || scenario == "patch" {
				if !observed.Load() {
					t.Error("expected native tool observation missing")
				}
			}
			if scenario == "cancel" {
				if result.Status != runtime.StatusCancelled {
					t.Error("cancel did not terminate Run")
				}
				select {
				case <-disconnected:
				case <-time.After(time.Second):
					t.Error("cancel did not disconnect upstream")
				}
			}
			if data, _ := os.ReadFile(filepath.Join(work, "read.txt")); string(data) != "READ_SENTINEL" {
				t.Error("read-only file changed")
			}
			if _, err := os.Stat(filepath.Join(work, "write.txt")); !os.IsNotExist(err) {
				t.Error("read-only write escaped")
			}
			if owned == nil {
				t.Fatal("actual Run ownership not observed")
			}
			if _, err := os.Stat(owned.root); !os.IsNotExist(err) {
				t.Error("actual Run profile was not cleaned")
			}
			select {
			case <-owned.gatewayDone:
			default:
				t.Error("owned gateway was not reaped")
			}
			connection, err := net.DialTimeout("tcp", "127.0.0.1:"+owned.proxyPort, time.Second)
			if err == nil {
				_ = connection.Close()
				t.Error("owned proxy port remains open")
			}
		})
	}
}
