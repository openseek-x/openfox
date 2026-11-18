package cliagent

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeBuddyProxyTLSAuthenticationAndNoFallback(t *testing.T) {
	for _, kind := range []string{"http", "https-trusted", "https-untrusted", "407", "502"} {
		t.Run(kind, func(t *testing.T) {
			var origins, connects, directDials atomic.Int32
			origin, ca := codeBuddyTLSFixture(t, "model.fixture.invalid", false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				origins.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-upstream-key" || r.Header.Get("Proxy-Authorization") != "" {
					t.Error("model/proxy credential boundary violated")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{}`)
			}))
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connects.Add(1)
				if r.Method != "CONNECT" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("fixture-user:fixture-password")) {
					t.Error("CONNECT authorization mismatch")
				}
				if kind == "407" {
					w.WriteHeader(407)
					return
				}
				if kind == "502" {
					w.WriteHeader(502)
					return
				}
				remote, err := net.Dial("tcp", origin.Listener.Addr().String())
				if err != nil {
					t.Error(err)
					return
				}
				client, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					remote.Close()
					t.Error(err)
					return
				}
				defer client.Close()
				defer remote.Close()
				rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
				rw.Flush()
				done := make(chan struct{})
				go func() { io.Copy(remote, rw); remote.Close(); close(done) }()
				io.Copy(client, remote)
				client.Close()
				<-done
			})
			var proxy *httptest.Server
			proxyCA := ""
			if strings.HasPrefix(kind, "https") {
				proxy, proxyCA = codeBuddyTLSFixture(t, "127.0.0.1", false, handler)
			} else {
				proxy = httptest.NewServer(handler)
				defer proxy.Close()
			}
			bundle, _ := os.ReadFile(ca)
			if kind == "https-trusted" {
				b, _ := os.ReadFile(proxyCA)
				bundle = append(bundle, b...)
			}
			caPath := filepath.Join(t.TempDir(), "bundle.pem")
			os.WriteFile(caPath, bundle, 0600)
			proxyURL := strings.Replace(proxy.URL, "://", "://fixture-user:fixture-password@", 1)
			endpoint := strings.Replace(origin.URL, "127.0.0.1", "model.fixture.invalid", 1) + "/v1"
			p := codeBuddyRelayForTest(t, endpoint, map[string]string{"HTTPS_PROXY": proxyURL, "SSL_CERT_FILE": caPath}, 1024)
			tr := p.client.Transport.(*http.Transport)
			dial := tr.DialContext
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if strings.HasPrefix(addr, "model.fixture.invalid:") {
					directDials.Add(1)
				}
				return dial(ctx, network, addr)
			}
			r, err := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			p.Close()
			if directDials.Load() != 0 {
				t.Fatal("proxy failure fell back to direct model connection")
			}
			if kind == "http" || kind == "https-trusted" {
				if r.StatusCode != 200 || origins.Load() != 1 || connects.Load() != 1 || p.FatalErr() != nil {
					t.Fatalf("proxy positive failed origin=%d connect=%d fatal=%v", origins.Load(), connects.Load(), p.FatalErr())
				}
			} else if origins.Load() != 0 || r.StatusCode != 502 || p.FatalErr() == nil {
				t.Fatalf("proxy rejection failed origin=%d status=%d fatal=%v", origins.Load(), r.StatusCode, p.FatalErr())
			}
		})
	}
}

func TestCodeBuddyRunTrustAndCredentialsAreIsolated(t *testing.T) {
	var counts [2]atomic.Int32
	var relays [2]*codeBuddyModelRelay
	for i := 0; i < 2; i++ {
		i := i
		server, ca := codeBuddyTLSFixture(t, "127.0.0.1", false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counts[i].Add(1)
			if r.Header.Get("Authorization") != "Bearer run-key-"+string(rune('a'+i)) {
				t.Error("cross Run key")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{}`)
		}))
		relays[i] = codeBuddyRelayForTest(t, server.URL, map[string]string{"SSL_CERT_FILE": ca, "CODEBUDDY_API_KEY": "run-key-" + string(rune('a'+i))}, 1024)
	}
	var wg sync.WaitGroup
	for _, p := range relays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
			if e != nil {
				t.Error(e)
				return
			}
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode != 200 {
				t.Error("concurrent Run failed")
			}
		}()
	}
	wg.Wait()
	if counts[0].Load() != 1 || counts[1].Load() != 1 || relays[0].token == relays[1].token {
		t.Fatal("Run isolation failed")
	}
	r, err := relays[0].client.Get(relays[1].target)
	if r != nil {
		r.Body.Close()
	}
	if err == nil {
		t.Fatal("Run inherited another Run CA")
	}
	req, _ := http.NewRequest("POST", relays[0].url+"/chat/completions", strings.NewReader(`{"model":"fixture-model"}`))
	req.Header.Set("Authorization", "Bearer "+relays[1].token)
	r, err = (&http.Client{Transport: &http.Transport{}, Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 401 || relays[0].FatalErr() == nil || relays[1].FatalErr() != nil {
		t.Fatal("cross Run token accepted or poisoned other Run")
	}
}

func TestCodeBuddyRelativeCAUsesRunDirectory(t *testing.T) {
	_, ca := codeBuddyTLSFixture(t, "127.0.0.1", false, http.NotFoundHandler())
	valid, _ := os.ReadFile(ca)
	good, bad := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(good, "ca.pem"), valid, 0600)
	os.WriteFile(filepath.Join(bad, "ca.pem"), []byte("wrong directory CA"), 0600)
	a := NewCodeBuddy("")
	a.Environment = map[string]string{"SSL_CERT_FILE": "", "NODE_EXTRA_CA_CERTS": "", "HTTP_PROXY": "", "HTTPS_PROXY": "", "http_proxy": "", "https_proxy": "", "NO_PROXY": "", "no_proxy": ""}
	request := codeBuddyRequest()
	request.Environment["SSL_CERT_FILE"] = "ca.pem"
	request.EnvironmentAllowlist = append(request.EnvironmentAllowlist, "SSL_CERT_FILE")
	if err := a.ValidateRequest(request); err != nil {
		t.Fatalf("workspace-less preflight read relative CA: %v", err)
	}
	request.WorkingDirectory = good
	if err := a.ValidateRequest(request); err != nil {
		t.Fatalf("valid Run CA rejected: %v", err)
	}
	for _, path := range []string{bad, t.TempDir()} {
		request.WorkingDirectory = path
		if err := a.ValidateRequest(request); err == nil {
			t.Fatal("invalid/missing Run-relative CA accepted")
		}
	}
}
