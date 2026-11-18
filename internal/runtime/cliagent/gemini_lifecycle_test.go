package cliagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

func waitGemini(t *testing.T, execution runtime.Execution) runtime.Result {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range execution.Events() {
		}
	}()
	result, err := execution.Wait(context.Background())
	<-done
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGeminiRunCredentialBoundary(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "HOST-KEY-MUST-NOT-BE-DONATED")
	t.Setenv("GEMINI_MODEL", "host-model")
	a := NewGemini("")
	a.Environment = map[string]string{"GEMINI_API_KEY": "ADAPTER-KEY", "GEMINI_MODEL": "adapter-model"}
	req := geminiNativeRequest("")
	req.Environment = nil
	if err := validateGeminiRequest(a, req); err == nil {
		t.Fatal("missing Run key borrowed a daemon credential")
	}
	req.Environment = map[string]string{"GEMINI_API_KEY": "RUN-KEY"}
	req.Model = "default"
	if err := validateGeminiRequest(a, req); err == nil {
		t.Fatal("missing Run model borrowed daemon model")
	}
	req.Environment["GEMINI_MODEL"] = "gemini-2.5-flash"
	if err := validateGeminiRequest(a, req); err != nil {
		t.Fatal(err)
	}
}

func TestNativeGeminiPreparationContext(t *testing.T) {
	a := geminiNativeAdapter(t)
	for _, timeout := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		want := context.Canceled
		if timeout {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		_, err := a.Start(ctx, geminiNativeRequest(""))
		cancel()
		if !errors.Is(err, want) {
			t.Fatalf("prepare context lost: %v want %v", err, want)
		}
	}
	entries, _ := os.ReadDir(a.StateRoot)
	if len(entries) != 0 {
		t.Fatalf("preparation created state after cancel: %v", entries)
	}
}

func TestNativeGeminiCentralHistory(t *testing.T) {
	a := geminiNativeAdapter(t)
	var bodies []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+geminiTestFrame(`{"text":"CENTRAL_FIRST_ANSWER"}`, "STOP")+"\n\n")
	}))
	defer server.Close()
	a.geminiHTTP, a.geminiEndpoint = server.Client(), server.URL
	first, err := a.Start(context.Background(), geminiNativeRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	r1 := waitGemini(t, first)
	second := geminiNativeRequest("")
	second.Instructions = "Central history supplied by OpenFox:\nUser: first question\nAssistant: " + r1.Output
	second.Prompt = "Use that prior answer for this follow-up."
	exec2, err := a.Start(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	r2 := waitGemini(t, exec2)
	if r1.Status != runtime.StatusSucceeded || r2.Status != runtime.StatusSucceeded || r1.SessionID == "" || r1.SessionID == r2.SessionID || len(bodies) != 2 || strings.Contains(bodies[0], "CENTRAL_FIRST_ANSWER") || !strings.Contains(bodies[1], "Central history supplied by OpenFox") || !strings.Contains(bodies[1], "CENTRAL_FIRST_ANSWER") {
		t.Fatalf("two Run history did not retain central context and new sessions: %+v %+v requests=%d", r1, r2, len(bodies))
	}
	t.Logf("two actual native sessions distinct; second HTTP carries central history; first=%s second=%s", r1.SessionID, r2.SessionID)
}

func TestNativeGeminiIncompleteStream(t *testing.T) {
	a := geminiNativeAdapter(t)
	for _, tc := range []struct{ name, wire string }{
		{"empty", ""},
		{"missing-stop", "data: " + geminiTestFrame(`{"text":"incomplete"}`, "") + "\n\n"},
		{"truncated-event", "data: " + geminiTestFrame(`{"text":"incomplete"}`, "STOP")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.wire)
			}))
			defer server.Close()
			a.geminiHTTP, a.geminiEndpoint = server.Client(), server.URL
			execution, err := a.Start(context.Background(), geminiNativeRequest(""))
			if err != nil {
				t.Fatal(err)
			}
			result := waitGemini(t, execution)
			if result.Status != runtime.StatusFailed {
				t.Fatalf("incomplete native stream succeeded: %+v", result)
			}
			t.Logf("actual native %s: %s", tc.name, result.Error)
		})
	}
}

func TestNativeGeminiTimeoutAndPortCleanup(t *testing.T) {
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
	a.geminiHTTP, a.geminiEndpoint = server.Client(), server.URL
	req := geminiNativeRequest("")
	req.Timeout = 2 * time.Second
	began := time.Now()
	execution, err := a.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	relay := execution.(*cleanupExecution).Execution.(*geminiExecution).relay
	result := waitGemini(t, execution)
	select {
	case <-started:
	default:
		t.Fatal("timeout happened before actual native HTTP")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("timeout left model stream open")
	}
	if result.Status != runtime.StatusTimedOut {
		t.Fatalf("%+v", result)
	}
	c, err := net.DialTimeout("tcp", relay.host, time.Second)
	if err == nil {
		c.Close()
		t.Fatal("Run listener still open")
	}
	entries, _ := os.ReadDir(a.StateRoot)
	if len(entries) != 0 {
		t.Fatalf("private state leaked %v", entries)
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Fatalf("deadline exceeded preparation+Run budget: %s", elapsed)
	}
	t.Logf("actual native timeout %s; listener refused and state absent", time.Since(began))
}

func TestGeminiParserUsageAndFailure(t *testing.T) {
	s := newSession(geminiProtocol(), "/work", func(v string) string { return v })
	for _, line := range []string{`{"type":"init","session_id":"one"}`, `{"type":"message","role":"assistant","content":"answer"}`, `{"type":"result","status":"success","stats":{"input_tokens":42,"output_tokens":9,"cached":5}}`} {
		parseGemini(s, []byte(line))
	}
	if usage := s.Usage(); usage == nil || usage.Input != 42 || usage.Output != 9 || usage.CachedInput != 5 {
		t.Fatalf("aggregate usage wrong: %+v", usage)
	}
	for _, line := range []string{`{"type":"tool_result","tool_id":"missing","status":"success"}`, `{"type":"tool_use","tool_id":"one","tool_name":"write_file"}`, `{"type":"result","status":"success"}`} {
		s = newSession(geminiProtocol(), "/work", func(v string) string { return v })
		events, done := parseGemini(s, []byte(line))
		if !done || len(events) == 0 || events[len(events)-1].Type != runtime.EventFailed {
			t.Fatalf("invalid event accepted: %s %+v", line, events)
		}
	}
}
