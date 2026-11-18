package gitlab

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"openfox/internal/integrations/gitapi"
	"openfox/internal/platform/safehttp"
)

func TestValidateInstanceURLStrict(t *testing.T) {
	// 解析器固定返回公网 / 内网地址，不依赖真实 DNS。
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "gitlab.example.test":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "internal.example.test":
			return []net.IP{net.ParseIP("10.1.2.3")}, nil
		case "mixed.example.test":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("192.168.0.9")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	opts := Options{Safe: safehttp.Options{LookupIP: lookup}}
	ok := map[string]string{
		"https://gitlab.example.test":      "https://gitlab.example.test",
		"https://GitLab.Example.test/":     "https://gitlab.example.test",
		"https://gitlab.example.test/gl//": "https://gitlab.example.test/gl",
		"https://93.184.216.34:8443":       "https://93.184.216.34:8443",
	}
	for raw, want := range ok {
		got, err := ValidateInstanceURL(context.Background(), raw, opts)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "http://gitlab.example.test", "ftp://gitlab.example.test", "https://", "https://u:p@gitlab.example.test",
		"https://gitlab.example.test#x", "https://gitlab.example.test?x=1", "https://internal.example.test", "https://mixed.example.test",
		"https://nope.example.test", "https://127.0.0.1", "https://[::1]", "https://169.254.169.254", "https://10.0.0.1", "https://192.168.1.1",
		"https://172.16.0.1", "https://0.0.0.0", "https://100.64.0.1", "https://[::ffff:10.0.0.1]", strings.Repeat("a", 3000)} {
		if got, err := ValidateInstanceURL(context.Background(), raw, opts); err == nil {
			t.Errorf("%q must be rejected, got %q", raw, got)
		}
	}
}

func TestValidateInstanceURLAllowLocal(t *testing.T) {
	opts := Options{AllowLocal: true}
	for raw, want := range map[string]string{"http://127.0.0.1:18102": "http://127.0.0.1:18102", "http://localhost:8080/": "http://localhost:8080", "https://gitlab.example.test": "https://gitlab.example.test"} {
		if got, err := ValidateInstanceURL(context.Background(), raw, opts); err != nil || got != want {
			t.Errorf("%q = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"ftp://x", "http://u:p@127.0.0.1", "http://127.0.0.1?x=1", "file:///etc/passwd", ""} {
		if _, err := ValidateInstanceURL(context.Background(), raw, opts); err == nil {
			t.Errorf("%q must be rejected", raw)
		}
	}
}

func TestAuthorizeURL(t *testing.T) {
	c := New("https://gitlab.example.test/", "app", "secret", "https://openfox.example.test/api/v1/integrations/gitlab/callback", Options{AllowLocal: true})
	raw := c.AuthorizeURL("st", "ch", "https://openfox.example.test/cb")
	parsed, _ := url.Parse(raw)
	q := parsed.Query()
	if parsed.Path != "/oauth/authorize" || q.Get("client_id") != "app" || q.Get("response_type") != "code" || q.Get("scope") != "api" ||
		q.Get("code_challenge") != "ch" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" || strings.Contains(raw, "secret") {
		t.Fatalf("url = %s", raw)
	}
}

func TestErrorMapping(t *testing.T) {
	status := http.StatusOK
	retryAfter := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":1,"username":"lin"}`))
	}))
	defer server.Close()
	c := New(server.URL, "app", "secret", "", Options{AllowLocal: true})
	if _, err := c.User(context.Background(), "bad"); err != gitapi.ErrTokenRevoked {
		t.Fatalf("401 = %v", err)
	}
	status = http.StatusNotFound
	if _, err := c.User(context.Background(), "tok"); err != gitapi.ErrNotFound {
		t.Fatalf("404 = %v", err)
	}
	status, retryAfter = http.StatusTooManyRequests, "30"
	_, err := c.User(context.Background(), "tok")
	limited, ok := err.(*gitapi.ErrRateLimited)
	if !ok || limited.ResetAt.IsZero() {
		t.Fatalf("429 = %v", err)
	}
	status, retryAfter = http.StatusOK, ""
	if user, err := c.User(context.Background(), "tok"); err != nil || user.Login != "lin" || user.ID != 1 {
		t.Fatalf("ok = %+v %v", user, err)
	}
}

func TestRedirectDoesNotLeakTokenToAnotherHost(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer origin.Close()
	c := New(origin.URL, "app", "secret", "", Options{AllowLocal: true})
	_, _ = c.User(context.Background(), "tok")
	if leaked {
		t.Fatal("authorization header followed a redirect to another host")
	}
}
