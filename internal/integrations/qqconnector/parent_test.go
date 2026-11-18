package qqconnector

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type parentFixture struct{ Node, Root, Port, CA, Result string }
type ownedHelper struct {
	Home string
	PID  int
}

// This is the actual Go parent killed by the outer test, not a mock of EOF.
func TestQQParentHelper(t *testing.T) {
	path := os.Getenv("OPENFOX_QQ_PARENT_HELPER")
	if path == "" {
		t.Skip("subprocess helper only")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var f parentFixture
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture")
	}
	c := Config{NodePath: f.Node, Root: f.Root}
	var home string
	ca, _ := json.Marshal(f.CA)
	setup := func(h string, script []byte) ([]byte, func(*exec.Cmd), error) {
		home, _ = filepath.EvalSymlinks(h)
		prefix := `import fixtureFS from 'node:fs';fixtureFS.writeFileSync('child.pid',String(process.pid));process.on('exit',()=>fixtureFS.writeFileSync('test-exit.json',JSON.stringify({home:process.env.HOME,url:import.meta.url})));import fixtureHTTPS from 'node:https';const fixtureRequest=fixtureHTTPS.request.bind(fixtureHTTPS);fixtureHTTPS.request=(options,cb)=>{if(options.hostname!=='q.qq.com')throw new Error('fixture origin');return fixtureRequest({...options,hostname:'127.0.0.1',port:` + f.Port + `,servername:'example.com',ca:` + string(ca) + `,headers:{...options.headers,Host:'q.qq.com'}},cb)};` + "\n"
		policy := sandbox(t, c, home, f.Port)
		return append([]byte(prefix), script...), func(cmd *exec.Cmd) {
			cmd.Path = "/usr/bin/sandbox-exec"
			cmd.Args = append([]string{"/usr/bin/sandbox-exec", "-f", policy}, cmd.Args...)
		}, nil
	}
	e = c.run(context.Background(), func(_ context.Context, v Event) error {
		if v.Kind != "qr" {
			return nil
		}
		p, e := os.ReadFile(filepath.Join(home, "child.pid"))
		if e != nil {
			return e
		}
		pid, e := strconv.Atoi(string(p))
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(ownedHelper{home, pid})
		return os.WriteFile(f.Result, raw, 0600)
	}, setup)
	t.Fatalf("parent was expected to be killed, Run returned: %v", e)
}

func TestNativeQQParentKilled(t *testing.T) {
	c := nativeConfig(t)
	entered := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "q.qq.com" || r.Method != "POST" {
			t.Error("unexpected native target")
			w.WriteHeader(400)
			return
		}
		var body map[string]string
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if r.URL.Path == "/lite/create_bind_task" {
			_, _ = w.Write([]byte(`{"retcode":0,"data":{"task_id":"owned-parent-death"}}`))
			return
		}
		if r.URL.Path != "/lite/poll_bind_result" {
			t.Error("unexpected native request")
			w.WriteHeader(404)
			return
		}
		entered <- struct{}{}
		<-r.Context().Done()
		stopped <- struct{}{}
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	dir := t.TempDir()
	result := filepath.Join(dir, "owned.json")
	config := filepath.Join(dir, "fixture.json")
	raw, _ := json.Marshal(parentFixture{c.NodePath, c.Root, port, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})), result})
	if e := os.WriteFile(config, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestQQParentHelper$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "OPENFOX_QQ_PARENT_HELPER="+config)
	var log strings.Builder
	cmd.Stdout = &log
	cmd.Stderr = &log
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	select {
	case <-entered:
	case <-time.After(12 * time.Second):
		t.Fatal("native request did not start")
	}
	var owned ownedHelper
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, e := os.ReadFile(result)
		if e == nil && json.Unmarshal(raw, &owned) == nil && owned.PID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if owned.PID <= 0 || !strings.HasPrefix(filepath.Base(owned.Home), "openfox-qq-helper-") {
		t.Fatal("owned child proof missing")
	}
	defer func() { _ = syscall.Kill(owned.PID, syscall.SIGKILL); _ = os.RemoveAll(owned.Home) }()
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	select {
	case <-stopped:
	case <-time.After(8 * time.Second):
		t.Fatal("native HTTP survived parent SIGKILL")
	}
	deadline = time.Now().Add(5 * time.Second)
	var gone, clean bool
	for time.Now().Before(deadline) {
		gone = errors.Is(syscall.Kill(owned.PID, 0), syscall.ESRCH)
		_, e := os.Stat(owned.Home)
		clean = errors.Is(e, os.ErrNotExist)
		if gone && clean {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone || !clean {
		raw, _ := os.ReadFile(filepath.Join(owned.Home, "test-exit.json"))
		t.Logf("owned cleanup probe: %s", raw)
		t.Fatalf("parent SIGKILL cleanup: child_exited=%v directory_removed=%v", gone, clean)
	}
	t.Log(fmt.Sprintf("actual Go parent SIGKILL: fixed SDK HTTP cancelled, child %d exited, only owned directory removed", owned.PID))
}
