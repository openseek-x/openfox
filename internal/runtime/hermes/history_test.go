package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// The center supplies bounded text history; native sessions never persist
// between Runs. This exercises actual Hermes twice, not an ACP executable mock.
// Center HTTP/PG history limits are tested separately by stage2.
func TestNativeHermesCenterHistory(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires the approved isolated Hermes installation")
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools    []json.RawMessage
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer history-fixture-key" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid controlled upstream request")
			w.WriteHeader(400)
			return
		}
		answer := "FIRST_NATIVE_REPLY"
		if len(body.Tools) > 0 {
			last := string(body.Messages[len(body.Messages)-1].Content)
			for _, message := range body.Messages {
				if message.Role == "assistant" {
					t.Error("previous native session history was reused")
				}
			}
			if strings.Contains(last, "FOLLOWUP_REQUEST") {
				if !strings.Contains(last, "Conversation so far:") || !strings.Contains(last, "[USER] FIRST_REQUEST") || !strings.Contains(last, "[ASSISTANT] FIRST_NATIVE_REPLY") {
					t.Error("center history did not reach the native model protocol")
				}
				answer = "FOLLOWUP_NATIVE_REPLY"
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"id": "history", "object": "chat.completion.chunk", "model": "r05-history-fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": answer}, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\ndata: {\"id\":\"history\",\"object\":\"chat.completion.chunk\",\"model\":\"r05-history-fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", chunk)
	}))
	defer upstream.Close()
	client := upstream.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	client.Transport = transport
	adapter := New()
	adapter.testClient = client
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var previousRoot, previousOutput string
	for _, turn := range []string{"FIRST_REQUEST", "FOLLOWUP_REQUEST"} {
		prompt := turn
		if previousOutput != "" {
			prompt = "Conversation so far:\n[USER] FIRST_REQUEST\n[ASSISTANT] " + previousOutput + "\n\nCurrent request:\n" + turn
		}
		var owned *nativeHome
		adapter.testObserve = func(home *nativeHome) { owned = home }
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		execution, err := adapter.Start(ctx, runtime.RunRequest{Prompt: prompt, WorkingDirectory: work, Model: "r05-history-fixture", EnvironmentAllowlist: []string{"HERMES_API_KEY", "HERMES_BASE_URL", "HERMES_CONTEXT_LENGTH"}, Environment: map[string]string{"HERMES_API_KEY": "history-fixture-key", "HERMES_BASE_URL": "https://example.com/v1", "HERMES_CONTEXT_LENGTH": "65536"}, Timeout: 60 * time.Second})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for range execution.Events() {
		}
		result, err := execution.Wait(ctx)
		cancel()
		if err != nil || result.Status != runtime.StatusSucceeded {
			t.Fatalf("turn=%s result=%+v error=%v", turn, result, err)
		}
		want := "FIRST_NATIVE_REPLY"
		if previousOutput != "" {
			want = "FOLLOWUP_NATIVE_REPLY"
		}
		if strings.TrimSpace(result.Output) != want {
			t.Fatalf("turn=%s output=%q", turn, result.Output)
		}
		if owned == nil || owned.root == previousRoot {
			t.Fatal("Run did not own a new native profile")
		}
		if _, err := os.Stat(owned.root); !os.IsNotExist(err) {
			t.Fatal("Run profile remained after completion")
		}
		previousRoot, previousOutput = owned.root, result.Output
		t.Logf("turn=%s native result=%s; distinct profile removed", turn, result.Status)
	}
}
