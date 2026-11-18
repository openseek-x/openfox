package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func machineTLS(t *testing.T, hostname string, handler http.Handler) (*httptest.Server, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if ip := net.ParseIP(hostname); ip != nil {
		template.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, leaf
}

// The proxy only connects to its one synthetic origin, never to request.Host.
func machineConnectProxy(t *testing.T, target string, secure bool, hostname string, count *atomic.Int32, status *atomic.Int32) (*httptest.Server, *x509.Certificate) {
	t.Helper()
	var sockets sync.Map
	t.Cleanup(func() { sockets.Range(func(k, v any) bool { k.(net.Conn).Close(); return true }) })
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if code := status.Load(); code != 0 {
			http.Error(w, "controlled failure", int(code))
			return
		}
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 405)
			return
		}
		upstream, err := net.DialTimeout("tcp", target, time.Second)
		if err != nil {
			http.Error(w, "origin failed", 502)
			return
		}
		downstream, b, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		sockets.Store(downstream, true)
		sockets.Store(upstream, true)
		b.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		b.Flush()
		done := make(chan struct{})
		go func() { io.Copy(upstream, b); upstream.Close(); close(done) }()
		io.Copy(downstream, upstream)
		downstream.Close()
		<-done
		sockets.Delete(downstream)
		sockets.Delete(upstream)
	})
	if secure {
		return machineTLS(t, hostname, h)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s, nil
}

func saveMachineEndpoint(t *testing.T, s MachineProxyStore, raw string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	v := s.View()
	if _, err = s.Save(context.Background(), MachineProxySave{ExpectedRevision: v.Revision, ScopeToken: v.ScopeToken, Mode: "CUSTOM", Effective: &MachineProxyEffective{Mode: "proxy", Protocol: u.Scheme, Host: u.Hostname(), Port: port}}); err != nil {
		t.Fatal(err)
	}
}

func TestMachineProxyTLSLayersAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		secure, trustProxy, trustOrigin bool
		proxyHost, originHost           string
		status                          int
	}{
		{"http-connect", false, false, true, "127.0.0.1", "target.invalid", 0},
		{"https-both-trusted", true, true, true, "127.0.0.1", "target.invalid", 0},
		{"unknown-proxy-ca", true, false, true, "127.0.0.1", "target.invalid", 0},
		{"wrong-proxy-host", true, true, true, "wrong.invalid", "target.invalid", 0},
		{"unknown-origin-ca", true, true, false, "127.0.0.1", "target.invalid", 0},
		{"wrong-origin-host", true, true, true, "127.0.0.1", "wrong.invalid", 0},
		{"407", false, false, true, "127.0.0.1", "target.invalid", 407},
		{"502", false, false, true, "127.0.0.1", "target.invalid", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var originCount, proxyCount, status atomic.Int32
			status.Store(int32(tc.status))
			origin, originCert := machineTLS(t, tc.originHost, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { originCount.Add(1); io.WriteString(w, "controlled") }))
			originURL, _ := url.Parse(origin.URL)
			proxy, proxyCert := machineConnectProxy(t, originURL.Host, tc.secure, tc.proxyHost, &proxyCount, &status)
			store, _ := proxyStoreFixture(t)
			saveMachineEndpoint(t, store, proxy.URL)
			network := NewMachineProxyNetwork(store)
			defer network.Close()
			roots := x509.NewCertPool()
			if tc.trustOrigin {
				roots.AddCert(originCert)
			}
			if tc.trustProxy {
				roots.AddCert(proxyCert)
			}
			network.base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			client := network.Client()
			client.Timeout = 2 * time.Second
			response, err := client.Get("https://target.invalid/data")
			if response != nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			}
			good := tc.name == "http-connect" || tc.name == "https-both-trusted"
			if good && (err != nil || originCount.Load() != 1) {
				t.Fatalf("expected actual proxied request: err=%v origin=%d", err, originCount.Load())
			}
			if !good && (!errors.Is(err, ErrMachineProxyRequest) || originCount.Load() != 0) {
				t.Fatalf("failure not closed: err=%v origin=%d", err, originCount.Load())
			}
			if tc.status != 0 {
				status.Store(0)
				r, e := client.Get("https://target.invalid/recovery")
				if e != nil {
					t.Fatal(e)
				}
				r.Body.Close()
				if originCount.Load() != 1 {
					t.Fatal("no recovery")
				}
			}
			t.Logf("origin=%d proxy=%d expected_success=%t", originCount.Load(), proxyCount.Load(), good)
		})
	}
}

func TestMachineProxyWSSRevisionAndCancellation(t *testing.T) {
	var hitsA, hitsB, status atomic.Int32
	origin, cert := machineTLS(t, "target.invalid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{ProtocolSubprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			kind, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err = c.Write(r.Context(), kind, data); err != nil {
				return
			}
		}
	}))
	u, _ := url.Parse(origin.URL)
	a, _ := machineConnectProxy(t, u.Host, false, "", &hitsA, &status)
	b, _ := machineConnectProxy(t, u.Host, false, "", &hitsB, &status)
	store, _ := proxyStoreFixture(t)
	saveMachineEndpoint(t, store, a.URL)
	network := NewMachineProxyNetwork(store)
	defer network.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	network.base.TLSClientConfig = &tls.Config{RootCAs: roots}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := network.Dial(ctx, "wss://target.invalid/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	saveMachineEndpoint(t, store, b.URL)
	// A new HTTP request loads revision 2 but cannot close the old upgraded socket.
	response, err := network.Client().Get("https://target.invalid/not-websocket")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	state := network.Status()
	if *state.LoadedRevision != 2 || *state.ConnectionRevision != 1 {
		t.Fatal(state)
	}
	if err = first.Write(ctx, []byte("old-still-live")); err != nil {
		t.Fatal(err)
	}
	message, err := first.Read(ctx)
	if err != nil || string(message) != "old-still-live" {
		t.Fatal(string(message), err)
	}
	second, err := network.Dial(ctx, "wss://target.invalid/connect", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if *network.Status().ConnectionRevision != 2 || hitsA.Load() != 1 || hitsB.Load() < 1 {
		t.Fatal(network.Status(), hitsA.Load(), hitsB.Load())
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err = network.Dial(canceled, "wss://target.invalid/connect", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A failed future dial neither falls back nor changes the connected revision.
	status.Store(407)
	if _, err = network.Dial(ctx, "wss://target.invalid/connect", nil); !errors.Is(err, ErrMachineProxyRequest) {
		t.Fatal(err)
	}
	if *network.Status().ConnectionRevision != 2 {
		t.Fatal("failed dial changed live revision")
	}
}

func TestMachineProxyGitLabOuterTransport(t *testing.T) {
	var proxyHits, status, outerHits atomic.Int32
	const org = "11111111-1111-4111-8111-111111111111"
	const command = "22222222-2222-4222-8222-222222222222"
	origin, cert := machineTLS(t, "target.invalid", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/device/organizations/"+org+"/gitlab-proxy/"+command || r.Header.Get("Authorization") != "Bearer controlled-device" {
			http.Error(w, "wrong envelope", 403)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		outerHits.Add(1)
		// Inner loopback remains forbidden by the existing safehttp boundary.
		c.Write(r.Context(), websocket.MessageText, []byte("127.0.0.1:1"))
		c.Read(r.Context())
	}))
	u, _ := url.Parse(origin.URL)
	proxy, _ := machineConnectProxy(t, u.Host, false, "", &proxyHits, &status)
	store, _ := proxyStoreFixture(t)
	saveMachineEndpoint(t, store, proxy.URL)
	network := NewMachineProxyNetwork(store)
	defer network.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	network.base.TLSClientConfig = &tls.Config{RootCAs: roots}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ForwardGitLab(ctx, "wss://target.invalid/api/v1/device/connect", func(context.Context) (string, error) { return "controlled-device", nil }, Message{Envelope: Envelope{Type: "gitlab.proxy", OrganizationID: org}, Payload: []byte(`{"command_id":"` + command + `"}`)}, network.Client())
	if proxyHits.Load() != 1 || outerHits.Load() != 1 {
		t.Fatal("outer transport not inherited", proxyHits.Load(), outerHits.Load())
	}
}
