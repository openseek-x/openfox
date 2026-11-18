package codex

import (
	"context"
	"encoding/json"
	rt "openfox/internal/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type poolHandshake struct{}

func (poolHandshake) Begin(send rt.Sender, _ func(error)) error {
	return send([]byte(`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"openfox","version":"1"}}}`))
}
func (poolHandshake) Interrupt(rt.Sender) bool { return false }
func (poolHandshake) HandleLine(line []byte, _ rt.Sender) ([]rt.Event, bool, error) {
	var msg struct {
		ID     int
		Result json.RawMessage
	}
	if json.Unmarshal(line, &msg) != nil {
		return nil, false, rt.ErrProtocol
	}
	if msg.ID == 1 && len(msg.Result) > 0 {
		return []rt.Event{{Type: rt.EventCompleted}}, true, nil
	}
	return nil, false, nil
}

// This exercises the installed server's authenticated transport without starting a model turn.
func TestNativePersistentHostLifecycle(t *testing.T) {
	if os.Getenv("OFD_REAL_CODEX") != "1" {
		t.Skip("requires installed Codex with socket capability tokens")
	}
	exe, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	a := New(exe)
	defer a.CloseSessions()
	req := rt.RunRequest{ProcessScope: "test/conversation", SessionIdleTTL: time.Second, SessionMaxAge: time.Minute}
	run := func() *runtimeHost {
		home, err := prepareCodexHome(root, source)
		if err != nil {
			t.Fatal(err)
		}
		env := []string{"CODEX_HOME=" + home.dir, "PATH=" + os.Getenv("PATH"), "HOME=" + source}
		spec := rt.ProcessSpec{Executable: exe, Args: []string{"app-server"}, Directory: source, Environment: env, Timeout: 8 * time.Second, MaxLineBytes: 65536, MaxOutputBytes: 65536, MaxEvents: 10, RequireCompletion: true, Session: poolHandshake{}}
		p := preparedProcess{path: exe, env: env, home: home}
		ex, ok, err := a.startPooled(context.Background(), req, spec, p)
		if !ok || err != nil {
			t.Fatalf("pool: used=%t err=%v", ok, err)
		}
		for range ex.Events() {
		}
		result, err := ex.Wait(context.Background())
		if err != nil || result.Status != rt.StatusSucceeded {
			t.Fatalf("handshake failed: %+v %v", result, err)
		}
		a.pool.mu.Lock()
		defer a.pool.mu.Unlock()
		host := a.pool.hosts[poolIdentity(req, spec, p)]
		if host == nil {
			t.Fatal("missing idle host")
		}
		return host
	}
	first := run()
	if second := run(); second != first {
		t.Fatal("did not reuse the first host")
	}
	select {
	case <-first.done:
	case <-time.After(4 * time.Second):
		t.Fatal("idle timer did not reap process")
	}
	req.SessionIdleTTL = time.Minute
	req.SessionMaxAge = 1500 * time.Millisecond
	next := run()
	if next == first {
		t.Fatal("reused expired host")
	}
	select {
	case <-next.done:
	case <-time.After(4 * time.Second):
		t.Fatal("age limit did not reap process")
	}
}
