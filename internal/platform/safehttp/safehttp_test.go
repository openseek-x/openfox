package safehttp

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPAllowed(t *testing.T) {
	for _, v := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"100.64.0.1", "192.168.1.1", "172.16.0.1", "198.18.0.6", "2001:2::e", "0.0.0.0", "fe80::1", "224.0.0.1"} {
		if IPAllowed(net.ParseIP(v)) {
			t.Fatalf("%s 应被拦截", v)
		}
	}
	for _, v := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !IPAllowed(net.ParseIP(v)) {
			t.Fatalf("%s 应放行", v)
		}
	}
	if IPAllowed(nil) {
		t.Fatal("nil 应被拦截")
	}
}

func TestCheckURL(t *testing.T) {
	bad := []string{"", "http://example.com", "https://user:pw@example.com", "https://example.com/#frag", "ftp://example.com", "https://", "example.com"}
	for _, raw := range bad {
		if _, err := CheckURL(raw); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("%q: err = %v", raw, err)
		}
	}
	u, err := CheckURL("https://jira.example.com:8443/jira")
	if err != nil || u.Host != "jira.example.com:8443" {
		t.Fatalf("u=%v err=%v", u, err)
	}
}

func testCA(srv *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return pool
}

func TestClientBlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client := Client(Options{RootCAs: testCA(srv)})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := client.Do(req); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientBlocksResolvedPrivateAddress(t *testing.T) {
	client := Client(Options{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.5")}, nil
	}})
	req, _ := http.NewRequest(http.MethodGet, "https://jira.example.com/", nil)
	if _, err := client.Do(req); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientDNSFailure(t *testing.T) {
	client := Client(Options{LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}})
	req, _ := http.NewRequest(http.MethodGet, "https://nope.example.com/", nil)
	_, err := client.Do(req)
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientAllowsInjectedAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	client := Client(Options{RootCAs: testCA(srv), AllowAddr: func(net.IP) bool { return true }})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// 证书不被信任时是 TLS 错误。
	untrusted := Client(Options{AllowAddr: func(net.IP) bool { return true }})
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := untrusted.Do(req2); err == nil {
		t.Fatal("未信任证书应失败")
	}
}

func TestClientRedirectChecks(t *testing.T) {
	var target *httptest.Server
	target = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/http":
			http.Redirect(w, r, "http://example.com/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			http.Redirect(w, r, "https://internal.test/", http.StatusFound)
		}
	}))
	defer redirector.Close()
	pool := x509.NewCertPool()
	pool.AddCert(redirector.Certificate())
	pool.AddCert(target.Certificate())
	// 只放行 127.0.0.1；internal.test 解析到 10.0.0.1，被拦截。
	allow := func(ip net.IP) bool { return ip.Equal(net.ParseIP("127.0.0.1")) }
	client := Client(Options{RootCAs: pool, AllowAddr: allow, LookupIP: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.1")}, nil
	}})
	get := func(path string) error {
		req, _ := http.NewRequest(http.MethodGet, redirector.URL+path, nil)
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get("/internal"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("内网重定向: %v", err)
	}
	if err := get("/http"); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("http 重定向: %v", err)
	}
	if err := get("/loop"); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("循环重定向: %v", err)
	}
}
