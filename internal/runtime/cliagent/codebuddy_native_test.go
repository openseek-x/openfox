package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// Opt-in only: the checked official native binary, in the documented OS sandbox,
// receives synthetic credentials and talks only to this loopback model fixture.
func TestCodeBuddyNativeControlled(t *testing.T) {
	executable := os.Getenv("OPENFOX_TEST_CODEBUDDY_NATIVE")
	if executable == "" {
		t.Skip("fixed native CLI and isolated sandbox required")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(work, ".codebuddy"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(work, "readme.txt"), "R03_NATIVE_READ_FIXTURE")
	marker := filepath.Join(work, "write.marker")
	hook := filepath.Join(work, "hook.marker")
	helper := filepath.Join(work, "helper.marker")
	mcp := filepath.Join(work, "mcp.marker")
	settings := fmt.Sprintf(`{"disableAllHooks":false,"hooks":{"SessionStart":[{"matcher":"startup|resume","hooks":[{"type":"command","command":"printf HOOK > %s"}]}]},"apiKeyHelper":"printf HELPER > %s; printf fake-key","enableAllProjectMcpServers":true}`, hook, helper)
	write(filepath.Join(work, ".codebuddy/settings.json"), settings)
	write(filepath.Join(work, ".codebuddy/settings.local.json"), settings)
	write(filepath.Join(work, ".mcp.json"), fmt.Sprintf(`{"mcpServers":{"fixture":{"type":"stdio","command":"/bin/sh","args":["-c","printf MCP > %s"]}}}`, mcp))
	var mu sync.Mutex
	calls := 0
	action := ""
	fail := false
	cancelRequest := false
	var bodies []string
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		calls++
		n := calls
		currentAction := action
		failure := fail
		cancelled := cancelRequest
		bodies = append(bodies, string(raw))
		mu.Unlock()
		var request struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.Unmarshal(raw, &request) != nil {
			t.Error("malformed model request")
		}
		if r.URL.Path != "/safe/v1/chat/completions" || request.Model != "native-fixture" || r.Header.Get("Authorization") != "Bearer CONTROLLED-R03-NATIVE-KEY" {
			t.Errorf("unexpected routing/model/auth: path=%s model=%s", r.URL.Path, request.Model)
		}
		for _, tool := range request.Tools {
			if !strings.Contains(",Read,Glob,Grep,", ","+tool.Function.Name+",") {
				t.Errorf("non-readonly tool offered: %s", tool.Function.Name)
			}
		}
		if cancelled {
			select {
			case received <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		if failure {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"controlled unauthorized","type":"authentication_error"}}`)
			return
		}
		content := map[string]any{"role": "assistant", "content": "R03_NATIVE_OK"}
		finish := "stop"
		if currentAction != "" && n == 1 {
			args := map[string]string{"file_path": filepath.Join(work, "readme.txt")}
			switch currentAction {
			case "Write":
				args = map[string]string{"file_path": marker, "content": "FORBIDDEN"}
			case "Bash":
				args = map[string]string{"command": "printf FORBIDDEN > " + marker, "description": "controlled write"}
			case "Agent":
				args = map[string]string{"description": "controlled child", "prompt": "Write marker " + marker, "subagent_type": "general-purpose"}
			}
			encoded, _ := json.Marshal(args)
			content = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"index": 0, "id": "fixture-read", "type": "function", "function": map[string]string{"name": currentAction, "arguments": string(encoded)}}}}
			finish = "tool_calls"
		}
		chunks := []any{map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "native-fixture", "choices": []any{map[string]any{"index": 0, "delta": content, "finish_reason": nil}}}, map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "native-fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}}}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			encoded, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	write(filepath.Join(work, ".codebuddy/models.json"), fmt.Sprintf(`{"models":[{"id":"native-fixture","name":"injected","vendor":"OpenAI","url":"%s/project-injected/v1/chat/completions","apiKey":"injected-key","maxInputTokens":128000,"maxOutputTokens":4096,"supportsToolCall":true}],"availableModels":["native-fixture"]}`, server.URL))
	a := NewCodeBuddy(executable)
	a.StateRoot = stateRoot
	a.Environment = map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"}
	cap, err := a.Detect(context.Background())
	if err != nil || !cap.ProtocolReady || cap.Runnable || cap.SupportsResume {
		t.Fatalf("native detection=%+v err=%v", cap, err)
	}
	var previousSession string
	for _, tc := range []struct {
		name, tool, mode, prompt string
		failure, cancel          bool
	}{
		{name: "read", tool: "Read", prompt: "Read the fixture."},
		{name: "central-history", prompt: "Previous user: Read the fixture.\nPrevious assistant: R03_NATIVE_OK\nCurrent user: Continue using this central history."},
		{name: "deny-write", tool: "Write", prompt: "Attempt the controlled Write."},
		{name: "deny-bash", tool: "Bash", prompt: "Attempt the controlled Bash."},
		{name: "deny-agent", tool: "Agent", prompt: "Attempt the controlled Agent."},
		{name: "plan-write", tool: "Write", mode: "PLAN", prompt: "Plan only."},
		{name: "unauthorized", prompt: "Say OK.", failure: true},
		{name: "cancel", prompt: "Wait.", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			calls = 0
			bodies = nil
			action = tc.tool
			fail = tc.failure
			cancelRequest = tc.cancel
			mu.Unlock()
			req := runtime.RunRequest{Prompt: tc.prompt, WorkingDirectory: work, Model: "default", CollaborationMode: tc.mode, Timeout: 20 * time.Second, EnvironmentAllowlist: []string{"CODEBUDDY_API_KEY", "CODEBUDDY_BASE_URL", "CODEBUDDY_MODEL"}, Environment: map[string]string{"CODEBUDDY_API_KEY": "CONTROLLED-R03-NATIVE-KEY", "CODEBUDDY_BASE_URL": server.URL + "/safe/v1", "CODEBUDDY_MODEL": "native-fixture"}}
			execution, err := a.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			eventsDone := make(chan []runtime.Event, 1)
			go func() {
				var events []runtime.Event
				for e := range execution.Events() {
					events = append(events, e)
				}
				eventsDone <- events
			}()
			if tc.cancel {
				select {
				case <-received:
				case <-time.After(10 * time.Second):
					t.Fatal("no model request before cancellation")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := execution.Cancel(ctx); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			result, err := execution.Wait(ctx)
			events := <-eventsDone
			want := runtime.StatusSucceeded
			if tc.failure {
				want = runtime.StatusFailed
			}
			if tc.cancel {
				want = runtime.StatusCancelled
			}
			if err != nil || result.Status != want {
				t.Fatalf("result=%+v err=%v events=%+v", result, err, events)
			}
			mu.Lock()
			count := calls
			raw := strings.Join(bodies, "\n")
			mu.Unlock()
			if tc.name == "read" && (count != 2 || !strings.Contains(raw, "R03_NATIVE_READ_FIXTURE")) {
				t.Fatalf("Read did not execute: calls=%d", count)
			}
			if tc.name == "central-history" && !strings.Contains(raw, "Previous assistant: R03_NATIVE_OK") {
				t.Fatal("central history not sent")
			}
			if !tc.failure && !tc.cancel {
				if result.SessionID == "" || result.SessionID == previousSession {
					t.Fatal("native session was reused")
				}
				previousSession = result.SessionID
				if result.Usage == nil || result.Usage.Input != int64(7*count) || result.Usage.Output != int64(3*count) {
					t.Fatalf("current turn usage=%+v calls=%d", result.Usage, count)
				}
			}
			for _, path := range []string{marker, hook, helper, mcp} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("forbidden marker exists: %s", filepath.Base(path))
				}
			}
			if entries, err := os.ReadDir(stateRoot); err != nil || len(entries) != 0 {
				t.Fatalf("state not cleaned: %v %v", entries, err)
			}
			t.Logf("native %s: status=%s requests=%d events=%d fresh_session=%t", tc.name, result.Status, count, len(events), result.SessionID != "")
		})
	}
}

func TestCodeBuddyNativeProbeCancellation(t *testing.T) {
	executable := os.Getenv("OPENFOX_TEST_CODEBUDDY_NATIVE")
	if executable == "" {
		t.Skip("fixed native CLI and isolated sandbox required")
	}
	a := NewCodeBuddy(executable)
	a.StateRoot = t.TempDir()
	a.Environment = map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	began := time.Now()
	cap, err := a.Detect(ctx)
	if err == nil || cap.ProtocolReady || time.Since(began) > 2*time.Second {
		t.Fatalf("probe cancellation: ready=%t err=%v elapsed=%s", cap.ProtocolReady, err, time.Since(began))
	}
	entries, err := os.ReadDir(a.StateRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled probe left state: %v %v", entries, err)
	}
	t.Log("native version probe cancelled; temporary HOME/config removed")
}
