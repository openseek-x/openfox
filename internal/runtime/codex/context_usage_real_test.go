package codex

import (
	"context"
	"encoding/json"
	"fmt"
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
)

// Opt-in real CLI, local-only provider. This bypasses credential discovery and
// tests the actual app-server session + process event path with an isolated home.
func TestContextUsageRealCLIWithControlledProvider(t *testing.T) {
	cli := os.Getenv("OPENFOX_CONTROLLED_CODEX_PATH")
	if cli == "" {
		t.Skip("set OPENFOX_CONTROLLED_CODEX_PATH for isolated real CLI test")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cliHome, work := filepath.Join(root, "cli-home"), filepath.Join(root, "work")
	for _, dir := range []string{cliHome, work} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
		var request struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "controlled-model-a" {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "controlled-response"}},
			{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "controlled-message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Controlled response, no tools."}}}},
			{"type": "response.completed", "response": map[string]any{"id": "controlled-response", "usage": map[string]any{"input_tokens": 100, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 10, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 110}}},
		} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	defer provider.Close()
	config := fmt.Sprintf(`model = "controlled-model-a"
model_provider = "controlled"
cli_auth_credentials_store = "file"
model_context_window = 131072
model_auto_compact_token_limit = 120000
approval_policy = "never"
sandbox_mode = "read-only"
[model_providers.controlled]
name = "OpenFox local controlled test"
base_url = %q
env_key = "C05_PROVIDER_TOKEN"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
stream_idle_timeout_ms = 5000
`, provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cliHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "CODEX_HOME=" + cliHome, "TMPDIR=" + root, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost", "C05_PROVIDER_TOKEN=controlled-not-a-real-key"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, cli, "--version")
	version.Env = env
	output, err := version.Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(string(output)))
	execution, err := runtime.StartProcess(ctx, runtime.ProcessSpec{
		Executable: cli, Args: []string{"app-server"}, Directory: work, Environment: env, Timeout: 25 * time.Second, RequireCompletion: true,
		MaxLineBytes: runtime.DefaultMaxLineBytes, MaxOutputBytes: runtime.DefaultMaxOutput, MaxEvents: runtime.DefaultMaxEvents,
		Session: &appServerSession{cwd: work, prompt: "Reply with a short sentence. Do not use tools.", model: "controlled-model-a", approvalMode: "READ_ONLY", redact: func(s string) string { return s }},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sample *runtime.ContextUsage
	for event := range execution.Events() {
		if event.Type != runtime.EventContextUsage {
			continue
		}
		var decoded runtime.ContextUsage
		if err := json.Unmarshal(event.Payload, &decoded); err != nil {
			t.Fatal(err)
		}
		sample = &decoded
	}
	result, err := execution.Wait(ctx)
	if err != nil || result.Status != runtime.StatusSucceeded {
		t.Fatalf("status=%s error=%v code=%s detail=%s", result.Status, err, result.ErrorCode, result.Error)
	}
	if calls.Load() != 1 || sample == nil || sample.CurrentTokens == nil || *sample.CurrentTokens != 110 || sample.WindowTokens == nil || *sample.WindowTokens != 124518 || sample.Model == nil || *sample.Model != "controlled-model-a" || sample.RequestedModel != "controlled-model-a" {
		t.Fatalf("calls=%d sample=%+v", calls.Load(), sample)
	}
	if result.Usage == nil || result.Usage.Input != 100 || result.Usage.Output != 10 {
		t.Fatalf("usage=%+v", result.Usage)
	}
	t.Logf("real CLI → app-server parser → process event: current=%d window=%d; cumulative input=%d output=%d; local requests=%d", *sample.CurrentTokens, *sample.WindowTokens, result.Usage.Input, result.Usage.Output, calls.Load())
}
