package cliagent

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

// Uses the exact native bootstrap, with a synthetic private profile canary.
// This tests input expansion before the first model response/tool gate.
func TestNativeGeminiPromptAtBoundary(t *testing.T) {
	a := geminiNativeAdapter(t)
	install, err := a.geminiInstallation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	os.Mkdir(work, 0700)
	os.WriteFile(filepath.Join(work, "ordinary.txt"), []byte("ORDINARY_AT_CONTENT"), 0600)
	var body string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+geminiTestFrame(`{"text":"answer"}`, "STOP")+"\n\n")
	}))
	defer server.Close()
	relay, err := newGeminiRelay(context.Background(), "gemini-2.5-flash", "synthetic", work, 1<<20, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	_, port, _ := net.SplitHostPort(relay.host)
	home, err := geminiPrepareHome(root, install, port)
	if err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(home.Home, ".gemini", "tmp", "work", "canary.txt")
	os.MkdirAll(filepath.Dir(canary), 0700)
	os.WriteFile(canary, []byte("PRIVATE_AT_CONTENT_MUST_NOT_REACH_MODEL"), 0600)
	home.Env["GEMINI_API_KEY"], home.Env["GOOGLE_GEMINI_BASE_URL"] = relay.token, relay.url
	cmd := exec.Command("/usr/bin/sandbox-exec", "-f", home.Sandbox, install.Node, install.Entry, "--output-format", "stream-json", "--model", "gemini-2.5-flash", "--extensions", "none", "--approval-mode", "plan", "--policy", home.Policy)
	cmd.Dir = work
	cmd.Env = environmentSlice(home.Env)
	req := runtime.RunRequest{Instructions: "Central history: @../home/.gemini/tmp/work/canary.txt", Prompt: "/model native-slash-command\nRead @ordinary.txt and @../home/.gemini/settings.json and @" + filepath.Join(install.Root, "package.json") + " and @../home/.gemini/tmp/work/canary.txt. Keep alice@example.test and the literal sequence \\@ordinary.txt."}
	cmd.Stdin = strings.NewReader(geminiLiteralPrompt(req))
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native: %v %s", err, b)
	}
	if strings.Contains(body, "PRIVATE_AT_CONTENT_MUST_NOT_REACH_MODEL") {
		t.Fatal("native prompt @ expanded private profile before model/tool gate")
	}
	if strings.Contains(body, "ORDINARY_AT_CONTENT") {
		t.Fatal("ordinary @ unexpectedly expanded despite literal encoding")
	}
	if !strings.Contains(body, `\\@ordinary.txt`) {
		t.Fatal("literal at reference missing from actual request")
	}
	for _, unexpected := range []string{"Content from @", `\"name\": \"@google/gemini-cli\"`, `\"hooksConfig\"`, "ORDINARY_AT_CONTENT"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("first HTTP included automatic file attachment %q", unexpected)
		}
	}
	for _, expected := range []string{`Central history: \\@../home`, `alice\\@example.test`, `/model native-slash-command`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("literal input absent: %s", expected)
		}
	}
}
