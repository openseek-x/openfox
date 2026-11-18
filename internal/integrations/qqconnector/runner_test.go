package qqconnector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func nativeConfig(t *testing.T) Config {
	t.Helper()
	c := Config{NodePath: os.Getenv("OPENFOX_TEST_QQ_CONNECTOR_NODE"), Root: os.Getenv("OPENFOX_TEST_QQ_CONNECTOR_ROOT")}
	if !c.Configured() {
		t.Skip("requires separately verified official SDK and Node artifacts")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("this controlled network-isolation harness uses macOS sandbox-exec")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

// Only the local fixture port can be reached. This test policy is deliberately
// separate from server deployment; it proves no real QR was requested by tests.
func sandbox(t *testing.T, c Config, home, port string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow sysctl-read)\n")
	fmt.Fprintf(&b, "(allow process-exec (literal %s))\n", strconv.Quote(c.NodePath))
	b.WriteString("(allow file-read* (subpath \"/usr\") (subpath \"/System\") (subpath \"/Library/Apple\") (literal \"/dev/null\") (literal \"/dev/urandom\") (literal \"/dev/random\"))\n")
	b.WriteString("(allow file-read* (literal \"/\") (literal \"/private\") (literal \"/private/tmp\") (literal \"/dev\"))\n")
	fmt.Fprintf(&b, "(allow file-read* (literal %s) (subpath %s) (subpath %s))\n", strconv.Quote(c.NodePath), strconv.Quote(c.Root), strconv.Quote(home))
	fmt.Fprintf(&b, "(allow file-write* (subpath %s) (literal \"/dev/null\"))\n", strconv.Quote(home))
	for _, p := range []string{c.NodePath, c.Root, home} {
		for p = filepath.Dir(p); ; p = filepath.Dir(p) {
			fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", strconv.Quote(p))
			if p == "/" {
				break
			}
		}
	}
	b.WriteString("(allow file-read-metadata (literal \"/tmp\") (literal \"/var\"))\n")
	if port != "" {
		fmt.Fprintf(&b, "(allow network-outbound (remote ip \"localhost:%s\"))\n", port)
	}
	path := filepath.Join(home, "sandbox.sb")
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeQQSandbox(t *testing.T) {
	c := nativeConfig(t)
	home, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	allowed := filepath.Join(home, "canary")
	denied := filepath.Join(outside, "canary")
	for _, p := range []string{allowed, denied} {
		if err := os.WriteFile(p, []byte("owned canary"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	quote := func(s string) string { v, _ := json.Marshal(s); return string(v) }
	script := `import fs from 'node:fs';import cp from 'node:child_process';import net from 'node:net';const r={};for(const [k,p] of Object.entries({allowed:` + quote(allowed) + `,outside:` + quote(denied) + `})) {try {r[k]=fs.readFileSync(p,'utf8')}catch(e){r[k]=e.code}}for(const p of ['/bin/sh','/bin/launchctl']) {r[p]=cp.spawnSync(p,['-c','exit 0']).error?.code??'EXECUTED'}await new Promise(resolve=>{const s=net.connect({host:'192.0.2.1',port:443});s.setTimeout(1000,()=>{r.network='TIMEOUT';s.destroy();resolve()});s.once('error',e=>{r.network=e.code;resolve()});s.once('connect',()=>{r.network='CONNECTED';s.destroy();resolve()})});console.log(JSON.stringify(r));`
	f := filepath.Join(home, "probe.mjs")
	_ = os.WriteFile(f, []byte(script), 0600)
	cmd := exec.Command("/usr/bin/sandbox-exec", "-f", sandbox(t, c, home, ""), c.NodePath, f)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "TMPDIR=" + home, "LANG=C.UTF-8"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
	var got map[string]string
	if json.Unmarshal(out, &got) != nil {
		t.Fatalf("invalid probe: %s", out)
	}
	t.Logf("sandbox actual boundaries: %s", out)
	if got["allowed"] != "owned canary" || got["outside"] != "EPERM" || got["/bin/sh"] != "EPERM" || got["/bin/launchctl"] != "EPERM" || got["network"] != "EPERM" {
		t.Fatal("sandbox boundary not established")
	}
}

// These fixture details come from the pinned SDK source, not from an invented
// public REST contract. Production invokes only SDK startQrConnect.
func TestNativeQQConnector(t *testing.T) {
	c := nativeConfig(t)
	for _, scenario := range []string{"success", "expired_then_success", "cancel_inflight", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			keys := map[string][]byte{}
			var creates, polls atomic.Int32
			entered := make(chan struct{}, 1)
			cancelObserved := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Host != "q.qq.com" {
					t.Errorf("unexpected request %s %s", r.Method, r.Host)
					w.WriteHeader(400)
					return
				}
				var body map[string]string
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/lite/create_bind_task":
					n := creates.Add(1)
					key, err := base64.StdEncoding.DecodeString(body["key"])
					if err != nil || len(key) != 32 {
						t.Error("SDK did not provide expected random key")
						w.WriteHeader(400)
						return
					}
					id := fmt.Sprintf("owned-task-%d", n)
					mu.Lock()
					keys[id] = key
					mu.Unlock()
					_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "data": map[string]any{"task_id": id}})
				case "/lite/poll_bind_result":
					polls.Add(1)
					if scenario == "cancel_inflight" {
						entered <- struct{}{}
						<-r.Context().Done()
						cancelObserved <- struct{}{}
						return
					}
					if scenario == "oversize" {
						_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
						return
					}
					if scenario == "expired_then_success" && body["task_id"] == "owned-task-1" {
						_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "data": map[string]any{"status": 3}})
						return
					}
					mu.Lock()
					key := keys[body["task_id"]]
					mu.Unlock()
					if len(key) != 32 {
						t.Error("unknown task")
						w.WriteHeader(400)
						return
					}
					block, _ := aes.NewCipher(key)
					gcm, _ := cipher.NewGCM(block)
					nonce := make([]byte, gcm.NonceSize())
					_, _ = rand.Read(nonce)
					encrypted := append(nonce, gcm.Seal(nil, nonce, []byte("owned-synthetic-app-secret"), nil)...)
					_ = json.NewEncoder(w).Encode(map[string]any{"retcode": 0, "data": map[string]any{"status": 2, "bot_appid": "owned-app", "bot_encrypt_secret": base64.StdEncoding.EncodeToString(encrypted), "user_openid": "IGNORED_BY_ADAPTER"}})
				default:
					t.Errorf("unexpected SDK path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
			ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
			caJSON, _ := json.Marshal(ca)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if scenario == "cancel_inflight" {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			var events []Event
			var ownedHome string
			setup := func(home string, script []byte) ([]byte, func(*exec.Cmd), error) {
				ownedHome, _ = filepath.EvalSymlinks(home)
				prefix := `import fixtureHTTPS from 'node:https';const fixtureRequest=fixtureHTTPS.request.bind(fixtureHTTPS);fixtureHTTPS.request=(options,cb)=>{if(options.hostname!=='q.qq.com')throw new Error('fixture origin');return fixtureRequest({...options,hostname:'127.0.0.1',port:` + port + `,servername:'example.com',ca:` + string(caJSON) + `,headers:{...options.headers,Host:'q.qq.com'}},cb)};` + "\n"
				policy := sandbox(t, c, ownedHome, port)
				return append([]byte(prefix), script...), func(cmd *exec.Cmd) {
					cmd.Path = "/usr/bin/sandbox-exec"
					cmd.Args = append([]string{"/usr/bin/sandbox-exec", "-f", policy}, cmd.Args...)
				}, nil
			}
			started := time.Now()
			err := c.run(ctx, func(_ context.Context, event Event) error { events = append(events, event); return nil }, setup)
			if _, e := os.Stat(ownedHome); !errors.Is(e, os.ErrNotExist) {
				t.Error("owned helper directory not removed")
			}
			switch scenario {
			case "success", "expired_then_success":
				if err != nil {
					t.Fatal(err)
				}
				last := events[len(events)-1]
				if last.Kind != "success" || last.AppID != "owned-app" || last.AppSecret != "owned-synthetic-app-secret" {
					t.Fatal("SDK credentials not conveyed")
				}
				want := int32(1)
				if scenario == "expired_then_success" {
					want = 2
					if len(events) != 4 || events[1].Kind != "expired" {
						t.Fatal("expiry did not refresh via SDK")
					}
				}
				if creates.Load() != want || polls.Load() != want {
					t.Fatal("unexpected native request count")
				}
			case "cancel_inflight":
				if !errors.Is(err, context.Canceled) || time.Since(started) > 8*time.Second {
					t.Fatalf("cancellation: %v duration=%s", err, time.Since(started))
				}
				select {
				case <-cancelObserved:
				case <-time.After(time.Second):
					t.Error("owned HTTP request not cancelled")
				}
			case "oversize":
				if !errors.Is(err, ErrUnknown) {
					t.Fatalf("oversize: %v", err)
				}
			}
			t.Logf("native %s: creates=%d polls=%d duration=%s, owned directory removed", scenario, creates.Load(), polls.Load(), time.Since(started))
		})
	}
}
