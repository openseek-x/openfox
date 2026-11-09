package providercli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runRPC(t *testing.T, provider string, messages ...string) []map[string]json.RawMessage {
	t.Helper()
	var out bytes.Buffer
	if err := Serve(context.Background(), provider, strings.NewReader(strings.Join(messages, "\n")), &out); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	var responses []map[string]json.RawMessage
	for dec.More() {
		var row map[string]json.RawMessage
		if err := dec.Decode(&row); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, row)
	}
	return responses
}
func TestProtocolAndAllowlist(t *testing.T) {
	for id, provider := range providers {
		t.Run(id, func(t *testing.T) {
			replies := runRPC(t, id,
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
				`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run","arguments":{"command":"sh"}}}`,
				`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"login"}}`)
			if len(replies) != 4 || !bytes.Contains(replies[0]["result"], []byte("2025-06-18")) || replies[2]["error"] == nil || replies[3]["error"] == nil {
				t.Fatal(replies)
			}
			var list struct {
				Tools []struct {
					Name        string
					Annotations struct {
						ReadOnly bool `json:"readOnlyHint"`
					}
				}
			}
			if err := json.Unmarshal(replies[1]["result"], &list); err != nil {
				t.Fatal(err)
			}
			if len(provider.operations) != len(list.Tools) {
				t.Fatal(list)
			}
			for i, tool := range list.Tools {
				if tool.Name != provider.operations[i].name || !tool.Annotations.ReadOnly {
					t.Fatal(tool)
				}
			}
		})
	}
	if err := Serve(context.Background(), "arbitrary/path", strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("unsupported CLI accepted")
	}
}

func TestCallsUseOnlyFixedArgvAndDoNotForwardRuntimeSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("OPENFOX_TEST_MARKER", "private")
	for id, p := range providers {
		if err := os.WriteFile(filepath.Join(dir, p.binary), []byte("#!/bin/sh\n[ -z \"$OPENFOX_TEST_MARKER\" ] && [ ! -e go.mod ] || exit 1\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, op := range p.operations {
			args := runRPC(t, id, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+op.name+`","arguments":{}}}`)
			var result struct {
				Content []struct{ Text string }
				IsError bool
			}
			if err := json.Unmarshal(args[0]["result"], &result); err != nil {
				t.Fatal(err)
			}
			if result.IsError || len(result.Content) != 1 || result.Content[0].Text != strings.Join(op.args, "\n") {
				t.Fatal(op.name, result)
			}
		}
	}
}
func TestCLIUnavailableAndFailedLogin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	call := `{"jsonrpc":"2.0","id":"one","method":"tools/call","params":{"name":"list_projects"}}`
	body := runRPC(t, "apifox", call)[0]["result"]
	if !bytes.Contains(body, []byte(`"isError":true`)) || !bytes.Contains(body, []byte("was not found")) {
		t.Fatal(string(body))
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "apifox"), []byte("#!/bin/sh\nprintf 'credential-marker' >&2\nprintf 'credential-marker'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	body = runRPC(t, "apifox", call)[0]["result"]
	if !bytes.Contains(body, []byte(`"isError":true`)) || bytes.Contains(body, []byte("credential-marker")) {
		t.Fatal(string(body))
	}
}
