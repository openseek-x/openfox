package imports

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Opt-in local browser fixture: real API, PostgreSQL and TLS upstream. The
// CONNECT proxy accepts only the fixed Teambition origin and tunnels to the
// loopback TLS fixture; it cannot proxy external hosts. No Playwright routing.
func TestTeambitionBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_P02_BROWSER_FILE")
	if path == "" {
		t.Skip("local browser harness not requested")
	}
	p := newP02Env(t, 20*time.Minute)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "open.teambition.com:443" {
			http.Error(w, "fixed fixture origin only", 403)
			return
		}
		upstream, err := net.DialTimeout("tcp", p.upstream.Listener.Addr().String(), time.Second)
		if err != nil {
			http.Error(w, "fixture unavailable", 502)
			return
		}
		browser, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = fmt.Fprint(browser, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() {
			defer browser.Close()
			defer upstream.Close()
			go io.Copy(upstream, browser)
			_, _ = io.Copy(browser, upstream)
		}()
	}))
	t.Cleanup(proxy.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:18144")
	if err != nil {
		t.Fatal(err)
	}
	apiServer := httptest.NewUnstartedServer(http.StripPrefix("/api", p.server.Config.Handler))
	apiServer.Listener = listener
	apiServer.Start()
	t.Cleanup(apiServer.Close)
	data, err := json.Marshal(map[string]string{"organization_id": p.org, "email": "site-member@example.test", "password": httpPassword, "parent_id": p.parent.ID, "proxy": proxy.URL, "api_url": apiServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			t.Fatal("browser harness timed out")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
		}
	}
}
