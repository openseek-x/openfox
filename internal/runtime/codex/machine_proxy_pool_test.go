package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rt "openfox/internal/runtime"
)

func TestMachineProxyNativePoolIsolation(t *testing.T) {
	executable := os.Getenv("OPENFOX_CONTROLLED_CODEX_PATH")
	if executable == "" {
		t.Skip("requires fixed native Codex in isolated loopback sandbox")
	}
	b, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != "7cb8700a47be3c81bb3c5efbf2d2adc716ce3db4806e9dc992f2925f78b2b194" {
		t.Fatal("native Codex SHA changed")
	}
	var outbound, loopbackProxy atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outbound.Add(1)
		if strings.HasPrefix(r.Host, "127.0.0.1:") || strings.HasPrefix(r.Host, "localhost:") || strings.HasPrefix(r.Host, "[::1]:") {
			loopbackProxy.Add(1)
		}
		http.Error(w, "no model request permitted", 407)
	}))
	defer proxy.Close()
	source, root := t.TempDir(), t.TempDir()
	if err = os.WriteFile(filepath.Join(source, "auth.json"), []byte(`{"OPENAI_API_KEY":"synthetic-pool-test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := New(executable)
	defer a.CloseSessions()
	req := rt.RunRequest{ProcessScope: "r10/controlled", SessionIdleTTL: time.Minute, SessionMaxAge: time.Minute}
	run := func(endpoint string) *runtimeHost {
		home, err := prepareCodexHome(root, source)
		if err != nil {
			t.Fatal(err)
		}
		env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + source, "CODEX_HOME=" + home.dir, "TMPDIR=" + os.TempDir(), "HTTP_PROXY=" + endpoint, "HTTPS_PROXY=" + endpoint, "http_proxy=" + endpoint, "https_proxy=" + endpoint, "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1"}
		spec := rt.ProcessSpec{Executable: executable, Args: []string{"app-server"}, Directory: source, Environment: env, Timeout: 8 * time.Second, MaxLineBytes: 65536, MaxOutputBytes: 65536, MaxEvents: 10, RequireCompletion: true, Session: poolHandshake{}}
		prepared := preparedProcess{path: executable, env: env, home: home}
		execution, used, err := a.startPooled(context.Background(), req, spec, prepared)
		if !used || err != nil {
			t.Fatal(used, err)
		}
		for range execution.Events() {
		}
		result, err := execution.Wait(context.Background())
		if err != nil || result.Status != rt.StatusSucceeded {
			t.Fatal(result, err)
		}
		a.pool.mu.Lock()
		defer a.pool.mu.Unlock()
		host := a.pool.hosts[poolIdentity(req, spec, prepared)]
		if host == nil {
			t.Fatal("host not retained")
		}
		return host
	}
	first := run(proxy.URL)
	if same := run(proxy.URL); same != first {
		t.Fatal("same policy did not reuse host")
	}
	second := run("http://127.0.0.1:1")
	if second == first {
		t.Fatal("changed policy reused old process environment")
	}
	select {
	case <-first.done:
		t.Fatal("new policy stopped old live host")
	default:
	}
	a.CloseSessions()
	for _, host := range []*runtimeHost{first, second} {
		select {
		case <-host.done:
		case <-time.After(5 * time.Second):
			t.Fatal("host cleanup timed out")
		}
	}
	if loopbackProxy.Load() != 0 {
		t.Fatal("loopback app-server IPC escaped to proxy")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, _ := os.ReadDir(root)
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pooled home retained")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("same policy reused host; changed policy isolated host; old host retained until close; local IPC proxy requests=0; blocked native background requests=%d; both processes/homes cleaned", outbound.Load())
}
