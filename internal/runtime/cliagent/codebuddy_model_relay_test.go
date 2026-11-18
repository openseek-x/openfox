package cliagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func codeBuddyTLSFixture(t *testing.T, host string, expired bool, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	caPublic, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OpenFox ephemeral CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte("ephemeral-ca")}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, AuthorityKeyId: ca.SubjectKeyId}
	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{host}
	}
	if expired {
		leaf.NotAfter = now.Add(-time.Minute)
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der, caDER}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return srv, path
}

func codeBuddyRelayForTest(t *testing.T, endpoint string, extras map[string]string, limit int) *codeBuddyModelRelay {
	t.Helper()
	env := map[string]string{"CODEBUDDY_API_KEY": "synthetic-upstream-key", "CODEBUDDY_BASE_URL": endpoint}
	for k, v := range extras {
		env[k] = v
	}
	p, err := newCodeBuddyModelRelay(context.Background(), env, "fixture-model", limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func codeBuddyRelayPost(p *codeBuddyModelRelay, body string) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPost, p.url+"/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.token)
	return (&http.Client{Transport: &http.Transport{}, Timeout: 3 * time.Second}).Do(req)
}

func TestCodeBuddyTransportConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{"HTTPS_PROXY": "http://good.invalid", "https_proxy": "http://other.invalid"},
		{"NO_PROXY": "one.invalid", "no_proxy": "two.invalid"},
		{"HTTPS_PROXY": "http://["}, {"HTTPS_PROXY": "socks5://localhost:9"},
		{"HTTPS_PROXY": "http://localhost:65536"}, {"HTTPS_PROXY": "https://localhost/else"},
		{"HTTP_PROXY": "http://localhost/?key=synthetic-secret"},
	} {
		if _, err := codeBuddyModelClient(env); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("proxy config accepted or leaked: %v", err)
		}
	}
	for _, contents := range []string{"", "not a PEM", "-----BEGIN CERTIFICATE-----\nYg==\n-----END CERTIFICATE-----"} {
		path := filepath.Join(t.TempDir(), "synthetic-path-private.pem")
		os.WriteFile(path, []byte(contents), 0600)
		if _, err := codeBuddyModelClient(map[string]string{"NODE_EXTRA_CA_CERTS": path}); err == nil || strings.Contains(err.Error(), path) {
			t.Fatalf("CA accepted/leaked: %v", err)
		}
	}
	if _, err := codeBuddyModelClient(map[string]string{"SSL_CERT_FILE": t.TempDir()}); err == nil {
		t.Fatal("directory CA accepted")
	}
	c, err := codeBuddyModelClient(map[string]string{"HTTPS_PROXY": "localhost:8888", "NO_PROXY": "*.invalid,10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	tr := c.Transport.(*http.Transport)
	for _, target := range []string{"https://127.0.0.1:444/v1", "https://localhost:44/v1", "https://10.2.3.4:444/v1", "https://api.example.invalid/v1"} {
		r, _ := http.NewRequest("POST", target, nil)
		p, err := tr.Proxy(r)
		if p != nil || err != nil {
			t.Fatalf("bypass %s => %v %v", target, p, err)
		}
	}
	r, _ := http.NewRequest("POST", "https://example.com:444/v1", nil)
	p, _ := tr.Proxy(r)
	if p == nil || p.Host != "localhost:8888" || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 || tr.ResponseHeaderTimeout != 30*time.Second {
		t.Fatal("transport policy mismatch")
	}
}

func TestCodeBuddyRelayTLSAndRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, host     string
		trust, expired bool
	}{
		{"unknown-ca", "127.0.0.1", false, false}, {"wrong-hostname", "wrong.invalid", true, false}, {"expired", "127.0.0.1", true, true}, {"trusted", "127.0.0.1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			srv, ca := codeBuddyTLSFixture(t, tc.host, tc.expired, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.URL.Path != "/original/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-upstream-key" {
					t.Error("target/key mismatch")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"ok":true}`)
			}))
			extra := map[string]string{}
			if tc.trust {
				extra["SSL_CERT_FILE"] = ca
			}
			p := codeBuddyRelayForTest(t, srv.URL+"/original/v1/", extra, 1024)
			resp, err := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if tc.name == "trusted" {
				if count.Load() != 1 || resp.StatusCode != 200 || p.FatalErr() != nil {
					t.Fatal("valid TLS failed")
				}
			} else if count.Load() != 0 || resp.StatusCode != 502 || p.FatalErr() == nil {
				t.Fatalf("TLS boundary count=%d status=%d fatal=%v", count.Load(), resp.StatusCode, p.FatalErr())
			}
		})
	}
	t.Run("redirect", func(t *testing.T) {
		var destination atomic.Int32
		dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destination.Add(1) }))
		defer dest.Close()
		src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", dest.URL); w.WriteHeader(307) }))
		defer src.Close()
		p := codeBuddyRelayForTest(t, src.URL, nil, 1024)
		r, err := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if destination.Load() != 0 || p.FatalErr() == nil || r.StatusCode != 502 {
			t.Fatal("redirect followed")
		}
	})
}

func TestCodeBuddyRelayLocalPolicyAndSafeErrors(t *testing.T) {
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		for _, h := range []string{"Cookie", "Proxy-Authorization", "Forwarded", "X-Forwarded-For"} {
			if r.Header.Get(h) != "" {
				t.Errorf("forwarded %s", h)
			}
		}
		w.Header().Set("Location", "https://secret.invalid")
		w.Header().Set("Set-Cookie", "secret")
		w.WriteHeader(401)
		io.WriteString(w, "provider-private-body synthetic-upstream-key")
	}))
	defer origin.Close()
	for _, kind := range []string{"host", "query", "escaped-path", "absolute", "method", "wrong-token", "duplicate-token", "model", "duplicate-model", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			p := codeBuddyRelayForTest(t, origin.URL, nil, 1024)
			body := `{"model":"fixture-model"}`
			if kind == "model" {
				body = `{"model":"other"}`
			}
			if kind == "duplicate-model" {
				body = `{"model":"fixture-model","model":"fixture-model"}`
			}
			if kind == "oversize" {
				body = strings.Repeat("x", codeBuddyMaxRequestBytes+1)
			}
			r := httptest.NewRequest("POST", p.url+"/chat/completions", strings.NewReader(body))
			r.RequestURI = "/v1/chat/completions"
			r.URL.Scheme = ""
			r.URL.Host = ""
			r.Header.Set("Authorization", "Bearer "+p.token)
			switch kind {
			case "host":
				r.Host = "other.invalid"
			case "query":
				r.RequestURI += "?x=1"
			case "escaped-path":
				r.URL.RawPath = "/v1/%63hat/completions"
			case "absolute":
				r.URL.Scheme = "http"
				r.URL.Host = p.host
			case "method":
				r.Method = "GET"
			case "wrong-token":
				r.Header.Set("Authorization", "Bearer wrong")
			case "duplicate-token":
				r.Header.Add("Authorization", "Bearer "+p.token)
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code < 400 || p.FatalErr() == nil || requests.Load() != 0 {
				t.Fatalf("policy accepted %s", kind)
			}
		})
	}
	p := codeBuddyRelayForTest(t, origin.URL, nil, 1024)
	r, _ := http.NewRequest("POST", p.url+"/chat/completions", strings.NewReader(`{"model":"fixture-model"}`))
	r.Header.Set("Authorization", "Bearer "+p.token)
	r.Header.Set("Cookie", "private")
	r.Header.Set("Forwarded", "private")
	r.Header.Set("Proxy-Authorization", "private")
	res, err := (&http.Client{Transport: &http.Transport{}}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 401 || strings.Contains(string(body), "private") || strings.Contains(string(body), "synthetic") || res.Header.Get("Location") != "" || res.Header.Get("Set-Cookie") != "" || p.FatalErr() != nil || requests.Load() != 1 {
		t.Fatal("unsafe provider error or unexpected retry")
	}
}

func TestCodeBuddyRelayBudgetConcurrencyAndCleanup(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, strings.Repeat("x", 2048))
		}))
		defer s.Close()
		p := codeBuddyRelayForTest(t, s.URL, nil, 1024)
		r, err := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
		if err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		if p.FatalErr() == nil {
			t.Fatal("response limit ignored")
		}
		wrapped := &codeBuddyRelaySession{session: newSession(codeBuddyProtocol(), "", func(s string) string { return s }), relay: p}
		_, done, err := wrapped.HandleLine([]byte(`{"type":"result","subtype":"success","result":"false success"}`), nil)
		if err == nil || done {
			t.Fatal("native success erased fatal")
		}
	})
	t.Run("concurrency-and-close", func(t *testing.T) {
		started, closed := make(chan struct{}, 2), make(chan struct{}, 2)
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			started <- struct{}{}
			<-r.Context().Done()
			closed <- struct{}{}
		}))
		defer s.Close()
		p := codeBuddyRelayForTest(t, s.URL, nil, 1024)
		done := make(chan struct{}, 2)
		for i := 0; i < 2; i++ {
			go func() {
				r, _ := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
				if r != nil {
					r.Body.Close()
				}
				done <- struct{}{}
			}()
		}
		for i := 0; i < 2; i++ {
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not start")
			}
		}
		r, err := codeBuddyRelayPost(p, `{"model":"fixture-model"}`)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 429 || p.FatalErr() == nil {
			t.Fatal("third request queued")
		}
		p.Close()
		for i := 0; i < 2; i++ {
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("upstream not cancelled")
			}
			<-done
		}
		if c, err := net.DialTimeout("tcp", p.host, 100*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("listener still open")
		}
	})
}

func TestCodeBuddyFinalEnvironmentKeepsSecretsInGo(t *testing.T) {
	p := &codeBuddyModelRelay{url: "http://127.0.0.1:456/v1", token: "temporary-token", model: "frozen-model"}
	env := map[string]string{"CODEBUDDY_API_KEY": "synthetic-key", "CODEBUDDY_BASE_URL": "https://original.invalid/v1", "CODEBUDDY_MODEL": "old", "HTTPS_PROXY": "http://proxy:password@proxy.invalid", "http_proxy": "http://proxy.invalid", "ALL_PROXY": "socks5://proxy.invalid", "NODE_EXTRA_CA_CERTS": "/private/ca", "SSL_CERT_FILE": "/private/ca", "NODE_TLS_REJECT_UNAUTHORIZED": "1", "NODE_OPTIONS": "bad"}
	codeBuddyLocalEnvironment(env, p)
	for k, v := range env {
		if strings.Contains(v, "synthetic-key") || strings.Contains(v, "password") || strings.Contains(v, "/private/ca") {
			t.Fatalf("child retained %s", k)
		}
	}
	if env["CODEBUDDY_MODEL"] != "frozen-model" || env["NO_PROXY"] != "127.0.0.1,localhost" || env["CODEBUDDY_API_KEY"] != p.token {
		t.Fatal("native configuration not frozen")
	}
}
