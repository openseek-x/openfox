package supervisor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/runtime"
	"openfox/internal/runtime/cliagent"
	"openfox/internal/runtime/codex"
)

// This opt-in matrix uses installed binaries pinned by SHA and a loopback-only
// OS sandbox. Provider credentials, registration, HOME and settings are synthetic.
// The Codex wrapper appends only a controlled provider to its generated profile;
// actual adapter detection, app-server arguments, environment and events remain.
func TestMachineProxyNativeAdapters(t *testing.T) {
	manifest := os.Getenv("OPENFOX_R10_NATIVE_MANIFEST")
	if manifest == "" {
		t.Skip("requires fixed native manifest and isolated OS sandbox")
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var paths map[string]struct {
		Path string `json:"path"`
		SHA  string `json:"sha256"`
	}
	if err = json.Unmarshal(raw, &paths); err != nil {
		t.Fatal(err)
	}
	for name, item := range paths {
		b, e := os.ReadFile(item.Path)
		if e != nil {
			t.Fatal(name, e)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != item.SHA {
			t.Fatal(name, "binary SHA changed")
		}
	}
	for _, testCase := range []string{"codex", "claude", "opencode", "pi", "codebuddy", "codex:401", "codex:timeout", "claude:timeout", "claude:cancel", "claude:conflict", "codex:recovery", "claude:recovery"} {
		name, mode, _ := strings.Cut(testCase, ":")
		t.Run(testCase, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"home", "config", "data", "cache", "state", "tmp", "agent", "codex", "claude"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			const key = "SYNTHETIC-R10-MODEL-KEY"
			var hits, authOK, closed atomic.Int32
			var center *fakeCenter
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					http.NotFound(w, r)
					return
				}
				hits.Add(1)
				if r.Header.Get("Authorization") == "Bearer "+key || r.Header.Get("X-Api-Key") == key {
					authOK.Add(1)
				}
				io.Copy(io.Discard, io.LimitReader(r.Body, 2<<20))
				if mode == "401" {
					http.Error(w, `{"error":{"type":"authentication_error","message":"controlled credential rejected"}}`, 401)
					return
				}
				if mode == "timeout" || mode == "cancel" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					if mode == "cancel" {
						center.mu.Lock()
						center.cancelOnRenew = true
						center.mu.Unlock()
					}
					<-r.Context().Done()
					closed.Add(1)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch {
				case strings.Contains(r.URL.Path, "responses"):
					io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r10\"}}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"R10_ADAPTER_VERIFIED\"}]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r10\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3,\"total_tokens\":10}}}\n\n")
				case strings.Contains(r.URL.Path, "chat/completions"):
					io.WriteString(w, "data: {\"id\":\"r10\",\"object\":\"chat.completion.chunk\",\"model\":\"r10\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"R10_ADAPTER_VERIFIED\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"r10\",\"object\":\"chat.completion.chunk\",\"model\":\"r10\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
				default:
					for _, e := range []string{`{"type":"message_start","message":{"id":"r10","type":"message","role":"assistant","content":[],"model":"r10","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":0}}}`, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"R10_ADAPTER_VERIFIED"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`, `{"type":"message_stop"}`} {
						var frame map[string]any
						json.Unmarshal([]byte(e), &frame)
						fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame["type"], e)
					}
				}
			})
			origin, ca := machineNativeTLS(t, root, handler)
			originURL, _ := url.Parse(origin.URL)
			var proxyHits atomic.Int32
			proxy := machineNativeProxy(t, originURL.Host, &proxyHits)
			var overrideHits atomic.Int32
			overrideProxy := ""
			if mode == "conflict" {
				overrideProxy = machineNativeProxy(t, originURL.Host, &overrideHits).URL
			}
			cfg := daemon.DeviceConfig{Version: 1, CenterURL: "https://center.invalid", WSSURL: "wss://center.invalid/connect", OrganizationID: testOrg, DeviceID: "device", KeyID: "key", PrivateKeyFile: filepath.Join(root, "key"), TokenFile: filepath.Join(root, "token"), WorkspaceRoot: filepath.Join(root, "workspace"), CenterEpoch: 1}
			configPath := filepath.Join(root, "config.json")
			if err = daemon.WriteDeviceConfig(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			store := daemon.MachineProxyStore{ConfigPath: configPath, Scope: daemon.ProxyScope(cfg)}
			pu, _ := url.Parse(proxy.URL)
			port, _ := strconv.Atoi(pu.Port())
			if _, err = store.Save(context.Background(), daemon.MachineProxySave{ScopeToken: store.Scope.Token(), Mode: "CUSTOM", Effective: &daemon.MachineProxyEffective{Mode: "proxy", Protocol: "http", Host: "127.0.0.1", Port: port}}); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": filepath.Join(root, "home"), "TMPDIR": filepath.Join(root, "tmp"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"), "SSL_CERT_FILE": ca, "NODE_EXTRA_CA_CERTS": ca, "CI": "1"}
			for _, k := range daemon.MachineProxyEnvironmentKeys {
				env[k] = ""
			}
			endpoint := "https://r10.invalid/v1"
			model := "r10"
			runtimeName := name
			var adapter runtime.Adapter
			write := func(p string, b []byte) {
				if e := os.WriteFile(p, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			switch name {
			case "codex":
				env["CODEX_HOME"] = filepath.Join(root, "codex")
				env["R10_SYNTHETIC_TOKEN"] = key
				write(filepath.Join(root, "codex", "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"`+key+`"}`))
				config := fmt.Sprintf("\n[model_providers.controlled]\nname=\"Controlled\"\nbase_url=%q\nenv_key=\"R10_SYNTHETIC_TOKEN\"\nwire_api=\"responses\"\nrequires_openai_auth=false\nsupports_websockets=false\nrequest_max_retries=0\nstream_max_retries=0\nstream_idle_timeout_ms=5000\n", endpoint)
				provider := filepath.Join(root, "provider.toml")
				write(provider, []byte(config))
				wrapper := filepath.Join(root, "codex-wrapper")
				script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + strconv.Quote(filepath.Join(root, "native.args")) + "\ncase \" $* \" in *' app-server '*) if [ -f \"$CODEX_HOME/config.toml\" ]; then { printf '%s\\n' 'model_provider=\"controlled\"'; /bin/cat \"$CODEX_HOME/config.toml\"; /bin/cat " + strconv.Quote(provider) + "; } > \"$CODEX_HOME/r10.toml\"; /bin/mv \"$CODEX_HOME/r10.toml\" \"$CODEX_HOME/config.toml\"; /bin/cat \"$CODEX_HOME/config.toml\" > " + strconv.Quote(filepath.Join(root, "native.config")) + "; fi;; esac\nexec " + strconv.Quote(paths[name].Path) + " \"$@\"\n"
				if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				a := codex.New(wrapper)
				a.Environment = env
				a.HomeRoot = filepath.Join(root, "runtime-state")
				a.ExportRoot = filepath.Join(root, "exports")
				adapter = a
				defer a.CloseSessions()
			case "claude":
				runtimeName = "claude-code"
				if mode == "conflict" {
					settings, _ := json.Marshal(map[string]any{"env": map[string]string{"HTTP_PROXY": overrideProxy, "HTTPS_PROXY": overrideProxy, "http_proxy": overrideProxy, "https_proxy": overrideProxy, "NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1"}})
					write(filepath.Join(root, "claude", "settings.json"), settings)
				}
				env["CLAUDE_CONFIG_DIR"] = filepath.Join(root, "claude")
				env["ANTHROPIC_API_KEY"] = key
				env["ANTHROPIC_BASE_URL"] = "https://r10.invalid"
				env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
				a := cliagent.NewClaudeCode(paths[name].Path)
				a.Environment = env
				a.StateRoot = filepath.Join(root, "runtime-state")
				adapter = a
			case "opencode":
				env["ANTHROPIC_API_KEY"] = key
				env["OPENCODE_CONFIG_CONTENT"] = fmt.Sprintf(`{"model":"anthropic/r10","small_model":"anthropic/r10","enabled_providers":["anthropic"],"provider":{"anthropic":{"options":{"baseURL":%q,"apiKey":%q},"models":{"r10":{"name":"R10","limit":{"context":32000,"output":1024}}}}}}`, endpoint, key)
				model = "anthropic/r10"
				a := cliagent.NewOpenCode(paths[name].Path)
				a.Environment = env
				a.StateRoot = filepath.Join(root, "runtime-state")
				adapter = a
			case "pi":
				env["PI_CODING_AGENT_DIR"] = filepath.Join(root, "agent")
				env["PI_OFFLINE"] = "1"
				env["PATH"] = filepath.Dir(paths["node"].Path) + ":" + env["PATH"]
				write(filepath.Join(root, "agent", "models.json"), []byte(fmt.Sprintf(`{"providers":{"controlled":{"api":"anthropic-messages","baseUrl":"https://r10.invalid","apiKey":%q,"models":[{"id":"r10"}]}}}`, key)))
				model = "controlled/r10"
				wrapper := filepath.Join(root, "pi-wrapper")
				if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+strconv.Quote(paths["node"].Path)+" "+strconv.Quote(paths[name].Path)+" \"$@\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				a := cliagent.NewPi(wrapper)
				a.Environment = env
				a.StateRoot = filepath.Join(root, "runtime-state")
				adapter = a
			case "codebuddy":
				a := cliagent.NewCodeBuddy(paths[name].Path)
				a.Environment = env
				a.StateRoot = filepath.Join(root, "runtime-state")
				adapter = a
			}
			center = newCenter()
			center.runtimeName = runtimeName
			center.runTimeoutSecs = 15
			if mode == "timeout" {
				center.runTimeoutSecs = 3
			}
			center.extend = func(r *RunContext) {
				r.Model = model
				r.Prompt = "Reply with R10_ADAPTER_VERIFIED. Do not use tools."
				r.ApprovalMode = "READ_ONLY"
				if name == "pi" {
					r.ApprovalMode = "FOLLOW_RUNTIME"
				}
				r.Environment = []EnvironmentVariable{{Name: "HTTPS_PROXY", Value: "http://agent-proxy.invalid:9"}, {Name: "SSL_CERT_FILE", Value: ca}, {Name: "NODE_EXTRA_CA_CERTS", Value: ca}}
				if name == "codebuddy" {
					r.CollaborationMode = "PLAN"
					r.Environment = append(r.Environment, EnvironmentVariable{Name: "CODEBUDDY_API_KEY", Value: key}, EnvironmentVariable{Name: "CODEBUDDY_BASE_URL", Value: endpoint})
				}
			}
			var reads atomic.Int32
			runner, stop := machineProxyRunner(t, center, adapter, func() (map[string]string, error) { reads.Add(1); return store.Environment() })
			defer stop()
			deadline := time.Now().Add(25 * time.Second)
			for len(center.snapshot().results) == 0 && len(center.snapshot().prepareFailed) == 0 && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			snapshot := center.snapshot()
			if len(snapshot.results) != 1 {
				t.Fatalf("no terminal result; prepare_failed=%v starts=%d", snapshot.prepareFailed, snapshot.starts)
			}
			result := snapshot.results[0]
			wantStatus := "SUCCEEDED"
			if mode == "401" {
				wantStatus = "FAILED"
			}
			if mode == "timeout" {
				wantStatus = "TIMED_OUT"
			}
			if mode == "cancel" {
				wantStatus = "CANCELED"
			}
			proxyOK := proxyHits.Load() > 0
			if mode == "conflict" {
				proxyOK = proxyHits.Load() == 0 && overrideHits.Load() > 0
			}
			if result.Status != wantStatus || (wantStatus == "SUCCEEDED" && !strings.Contains(result.Output, "R10_ADAPTER_VERIFIED")) || hits.Load() < 1 || authOK.Load() != hits.Load() || !proxyOK || reads.Load() != 1 {
				if name == "codex" {
					b, _ := os.ReadFile(filepath.Join(root, "native.config"))
					a, _ := os.ReadFile(filepath.Join(root, "native.args"))
					t.Logf("controlled config=%s args=%s", b, a)
				}
				t.Fatalf("status=%s output=%s summary=%s origin=%d auth=%d proxy=%d snapshots=%d", result.Status, result.Output, result.Summary, hits.Load(), authOK.Load(), proxyHits.Load(), reads.Load())
			}
			if a, ok := adapter.(*codex.Adapter); ok {
				a.CloseSessions()
			}
			waitFor(t, "run resources released", func() bool { return runner.ActiveRuns() == 0 })
			if mode == "timeout" || mode == "cancel" {
				waitFor(t, "origin request canceled", func() bool { return closed.Load() == hits.Load() })
			}
			entries, e := os.ReadDir(filepath.Join(root, "runtime-state"))
			if e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			for _,entry := range entries {
 if name != "opencode" || entry.Name() != "opencode-sessions" { t.Fatal("runtime temporary state retained",entry.Name()) }
 }
			t.Logf("native=%s mode=%s status=%s origin=%d synthetic_auth_matches=%d proxy_connects=%d native_override_proxy=%d policy_snapshots=%d state_clean=true", name, mode, result.Status, hits.Load(), authOK.Load(), proxyHits.Load(), overrideHits.Load(), reads.Load())
		})
	}
}

func machineNativeTLS(t *testing.T, root string, h http.Handler) (*httptest.Server, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: []string{"r10.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(h)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s, ca
}
func machineNativeProxy(t *testing.T, target string, count *atomic.Int32) *httptest.Server {
	t.Helper()
	var sockets sync.Map
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "r10.invalid:443" {
			http.Error(w, "fixture target only", 403)
			return
		}
		count.Add(1)
		up, e := net.DialTimeout("tcp", target, time.Second)
		if e != nil {
			http.Error(w, "fixture unavailable", 502)
			return
		}
		down, b, e := w.(http.Hijacker).Hijack()
		if e != nil {
			up.Close()
			return
		}
		sockets.Store(up, true)
		sockets.Store(down, true)
		b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		b.Flush()
		done := make(chan struct{})
		go func() { io.Copy(up, b); up.Close(); close(done) }()
		io.Copy(down, up)
		down.Close()
		<-done
		sockets.Delete(up)
		sockets.Delete(down)
	}))
	t.Cleanup(func() { sockets.Range(func(k, v any) bool { k.(net.Conn).Close(); return true }); s.Close() })
	return s
}
