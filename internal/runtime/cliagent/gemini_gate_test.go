package cliagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func geminiTestFrame(parts string, finish string) string {
	return `{"candidates":[{"index":0,"content":{"role":"model","parts":[` + parts + `]},"finishReason":"` + finish + `"}]}`
}
func TestGeminiToolGate(t *testing.T) {
	root := "/private/tmp/run/work"
	for _, tc := range []struct {
		name, call string
		ok         bool
	}{
		{"read", `{"name":"read_file","args":{"file_path":"nested/file.go","start_line":1,"end_line":50}}`, true},
		{"absolute-own", `{"name":"read_file","args":{"file_path":"/private/tmp/run/work/nested/file.go"}}`, true},
		{"private-profile", `{"name":"read_file","args":{"file_path":"/private/tmp/run/home/.gemini/tmp/work/canary"}}`, false},
		{"native-at-rewrite", `{"name":"read_file","args":{"file_path":"@../home/.gemini/tmp/work/canary"}}`, false},
		{"traversal", `{"name":"read_file","args":{"file_path":"nested/../../secret"}}`, false},
		{"glob", `{"name":"glob","args":{"pattern":"nested/**/*.go"}}`, true},
		{"glob-parent", `{"name":"glob","args":{"pattern":"../home/**/*"}}`, false},
		{"glob-brace", `{"name":"glob","args":{"pattern":"{*,/Users/*}"}}`, false},
		{"glob-class-parent", `{"name":"glob","args":{"pattern":"[.][.]/*"}}`, false},
		{"grep-regex", `{"name":"grep_search","args":{"dir_path":"nested","pattern":"func\\s+test","exclude_pattern":"test\\.go","include_pattern":"*.go","total_max_matches":100}}`, true},
		{"wrong-field", `{"name":"grep_search","args":{"path":"outside","pattern":"key"}}`, false},
		{"oversize", `{"name":"grep_search","args":{"pattern":"x","total_max_matches":201}}`, false},
		{"case-alias", `{"name":"read_file","args":{"file_path":"../home/key"},"Args":{"file_path":"safe"}}`, false},
		{"case-name", `{"Name":"read_file","args":{"file_path":"safe"}}`, false},
		{"unknown", `{"name":"run_shell_command","args":{"command":"cat key"}}`, false},
		{"partial", `{"name":"read_file","partialArgs":[{"jsonPath":"file_path","stringValue":"../secret"}],"args":{}}`, false},
		{"will-continue", `{"name":"read_file","args":{"file_path":"x"},"willContinue":true}`, false},
		{"list", `{"name":"list_directory","args":{"dir_path":".","ignore":["*.tmp"],"file_filtering_options":{"respect_git_ignore":true}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := geminiToolCall(root, json.RawMessage(tc.call))
			if (e == nil) != tc.ok {
				t.Fatalf("unexpected gate: %v", e)
			}
		})
	}
}
func TestGeminiStreamGate(t *testing.T) {
	valid := `{"functionCall":{"name":"read_file","args":{"file_path":"ordinary.txt"}}}`
	bad := `{"functionCall":{"name":"write_file","args":{"file_path":"ordinary.txt","content":"bad"}}}`
	cases := []struct {
		name, event string
		ok          bool
	}{
		{"text", geminiTestFrame(`{"text":"answer"}`, "STOP"), true},
		{"call", geminiTestFrame(valid, "STOP"), true},
		{"mixed", geminiTestFrame(valid+","+bad, "STOP"), false},
		{"duplicate", strings.Replace(geminiTestFrame(valid, "STOP"), `"name":"read_file"`, `"name":"run_shell_command","name":"read_file"`, 1), false},
		{"case-content", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"file_path":"../home/key"}}}]},"Content":{"parts":[{"text":"safe"}]}}]}`, false},
		{"unicode-fold-parts", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"file_path":"../home/key"}}}],"part\u017f":[{"text":"safe"}]},"finishReason":"STOP"}]}`, false},
		{"null-candidates", `{"candidates":null}`, true},
		{"code", geminiTestFrame(`{"executableCode":{"code":"bad"}}`, "STOP"), false},
		{"empty-part", geminiTestFrame(`{}`, "STOP"), false},
		{"max-token", geminiTestFrame(`{"text":"incomplete"}`, "MAX_TOKENS"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := geminiStreamState{}
			e := s.accept("/work", []byte(tc.event))
			if (e == nil) != tc.ok {
				t.Fatalf("%v", e)
			}
		})
	}
}
func TestGeminiSSEFrames(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		ok         bool
	}{
		{"one", "data: {\"a\":1}\n\n", true},
		{"multiline", "data: {\n" + "data: \"a\":1}\n\n", true},
		{"crlf", "data: {\"a\":1}\r\n\r\n", true},
		{"truncated", "data: {\"a\":1}", false},
		{"event-id", "id: x\ndata: {}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			e := geminiSSE(&geminiByteReader{data: []byte(tc.wire)}, func(b []byte) error {
				n++
				if !geminiJSONObject(b) {
					return fmt.Errorf("invalid")
				}
				return nil
			})
			if (e == nil) != tc.ok || (tc.ok && n != 1) {
				t.Fatalf("n=%d e=%v", n, e)
			}
		})
	}
}

type geminiByteReader struct{ data []byte }

func (r *geminiByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
func TestGeminiRelayTLSAndBoundary(t *testing.T) {
	var posts atomic.Int32
	var mode atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.URL.RequestURI() != "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" || r.Header.Get("X-Goog-Api-Key") != "REAL-SYNTHETIC-KEY" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("upstream routing/key/header mismatch")
		}
		if mode.Load() == 1 {
			w.WriteHeader(429)
			return
		}
		if mode.Load() == 2 {
			w.Header().Set("Location", "https://other.invalid")
			w.WriteHeader(307)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		wire := "data: " + geminiTestFrame(`{"text":"TLS verified"}`, "STOP") + "\n\n"
		if mode.Load() == 3 {
			wire = "data: " + geminiTestFrame(`{"text":"partial"}`, "") + "\n\n"
		}
		if mode.Load() == 4 {
			wire = "data: " + geminiTestFrame(`{"functionCall":{"name":"glob","args":{"pattern":"../home/**/*"}}}`, "STOP") + "\n\n"
		}
		if mode.Load() == 5 {
			wire = strings.Repeat(": harmless comment\n", 200)
		}
		if mode.Load() == 6 {
			wire = "data: " + geminiTestFrame(`{"text":"mixed"},{"functionCall":{"name":"read_file","args":{"file_path":"../home/key"}}}`, "STOP") + "\n\n"
		}
		for _, v := range []byte(wire) {
			_, _ = w.Write([]byte{v})
			http.NewResponseController(w).Flush()
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		name  string
		mode  int32
		fatal bool
	}{
		{"success", 0, false}, {"redirect", 2, true}, {"no-finish", 3, true}, {"glob-escape", 4, true}, {"comment-budget", 5, true}, {"mixed", 6, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			budget := 8 << 20
			if tc.mode == 5 {
				budget = 20
			}
			p, e := newGeminiRelay(context.Background(), "gemini-2.5-flash", "REAL-SYNTHETIC-KEY", "/work", budget, server.URL, server.Client())
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			r, _ := http.NewRequest("POST", p.url+p.route, strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"test"}]}]}`))
			r.Header.Set("X-Goog-Api-Key", p.token)
			r.Header.Set("Authorization", "DO-NOT-FORWARD")
			r.Header.Set("X-Forwarded-Host", "DO-NOT-FORWARD")
			res, e := http.DefaultClient.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			deadline := time.Now().Add(time.Second)
			for p.FatalErr() == nil && tc.fatal && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if (p.FatalErr() != nil) != tc.fatal {
				t.Fatalf("fatal=%v", p.FatalErr())
			}
			if tc.mode == 4 || tc.mode == 6 {
				if len(b) != 0 {
					t.Fatalf("unsafe event escaped: %s", b)
				}
			}
		})
	}
	t.Run("retry-recovers", func(t *testing.T) {
		p, e := newGeminiRelay(context.Background(), "gemini-2.5-flash", "REAL-SYNTHETIC-KEY", "/work", 8<<20, server.URL, server.Client())
		if e != nil {
			t.Fatal(e)
		}
		defer p.Close()
		for _, m := range []int32{1, 0} {
			mode.Store(m)
			r, _ := http.NewRequest("POST", p.url+p.route, bytes.NewBufferString(`{"contents":[{}]}`))
			r.Header.Set("X-Goog-Api-Key", p.token)
			res, e := http.DefaultClient.Do(r)
			if e != nil {
				t.Fatal(e)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		if p.FatalErr() != nil {
			t.Fatal(p.FatalErr())
		}
	})
	t.Run("untrusted-tls", func(t *testing.T) {
		p, e := newGeminiRelay(context.Background(), "gemini-2.5-flash", "REAL-SYNTHETIC-KEY", "/work", 8<<20, server.URL, &http.Client{Timeout: time.Second})
		if e != nil {
			t.Fatal(e)
		}
		defer p.Close()
		before := posts.Load()
		r, _ := http.NewRequest("POST", p.url+p.route, bytes.NewBufferString(`{"contents":[{}]}`))
		r.Header.Set("X-Goog-Api-Key", p.token)
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if p.FatalErr() == nil || posts.Load() != before {
			t.Fatal("TLS failure was not fail closed")
		}
	})
}

func TestGeminiSSEUnicodeSeparators(t *testing.T) {
	raw := []byte(`{"text":"before` + "\u2028" + `middle` + "\u2029" + `after"}`)
	encoded := geminiSSEJSON(raw)
	if bytes.Contains(encoded, []byte("\u2028")) || bytes.Contains(encoded, []byte("\u2029")) {
		t.Fatal("literal JS line separator escaped into SDK regex")
	}
	var before, after any
	if json.Unmarshal(raw, &before) != nil || json.Unmarshal(encoded, &after) != nil || fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("SSE encoding changed JSON value")
	}
}
