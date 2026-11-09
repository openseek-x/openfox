package skills

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIPAllowed(t *testing.T) {
	denied := []string{
		"127.0.0.1", "127.8.9.10", "::1", "10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "169.254.0.1", "fe80::1", "100.64.0.1", "100.127.255.255", "224.0.0.1", "239.255.255.255", "ff02::1",
		"0.0.0.0", "::", "0.1.2.3", "240.0.0.1", "255.255.255.255", "fc00::1", "fd12:3456::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.0.1", "::ffff:100.64.0.1",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", "2001:db8::1", "64:ff9b::7f00:1", "fec0::1", "2002:7f00:1::1",
	}
	for _, value := range denied {
		if IPAllowed(net.ParseIP(value)) {
			t.Errorf("%s should be denied", value)
		}
	}
	allowed := []string{"93.184.216.34", "8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8", "172.32.0.1", "100.128.0.1"}
	for _, value := range allowed {
		if !IPAllowed(net.ParseIP(value)) {
			t.Errorf("%s should be allowed", value)
		}
	}
	if IPAllowed(nil) || IPAllowed(net.IP{1, 2}) {
		t.Error("invalid ip should be denied")
	}
}

func TestParseURL(t *testing.T) {
	forbidden := []string{
		"http://example.com/SKILL.md", "ftp://example.com/x", "file:///etc/passwd", "gopher://example.com/",
		"https://user@example.com/x", "https://user:pw@example.com/x", "https://example.com:8443/x", "https://example.com:80/x",
		"https://127.0.0.1/x", "https://127.0.0.1:443/x", "https://[::1]/x", "https://10.0.0.1/x", "https://172.16.5.5/x",
		"https://192.168.0.1/x", "https://169.254.169.254/latest/meta-data", "https://100.64.0.1/x", "https://224.0.0.1/x",
		"https://0.0.0.0/x", "https://[::ffff:127.0.0.1]/x", "https://[::ffff:10.0.0.1]/x", "https://[fc00::1]/x",
		"https://[fe80::1]/x", "https://240.0.0.1/x",
	}
	for _, raw := range forbidden {
		if _, err := ParseURL(raw); !errors.Is(err, ErrURLForbidden) {
			t.Errorf("%s: err = %v, want forbidden", raw, err)
		}
	}
	for _, raw := range []string{"", "   ", "not a url", "https://", "://x", "example.com/x", "https:///x", strings.Repeat("a", 2049)} {
		if _, err := ParseURL(raw); !errors.Is(err, ErrURLInvalid) {
			t.Errorf("%q: err = %v, want invalid", raw, err)
		}
	}
	for _, raw := range []string{"https://example.com/SKILL.md", "https://example.com:443/a.zip", "https://8.8.8.8/x"} {
		if _, err := ParseURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestStrictDialerChecksActualAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// 直接拨号回环地址：Control 在实际连接的 IP 上拒绝（即使上层的 DNS 校验被绕过）。
	conn, err := strictDialer().DialContext(context.Background(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("dial to loopback should fail")
	}
	if !errors.Is(err, ErrURLForbidden) {
		t.Errorf("err = %v", err)
	}
	if err := controlAddress("tcp", "10.0.0.1:443", nil); !errors.Is(err, ErrURLForbidden) {
		t.Errorf("private: %v", err)
	}
	if err := controlAddress("tcp", "8.8.8.8:80", nil); !errors.Is(err, ErrURLForbidden) {
		t.Errorf("port: %v", err)
	}
	if err := controlAddress("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("public: %v", err)
	}
}

func TestDefaultFetcherRejectsLocalhost(t *testing.T) {
	// 零值 Fetcher 用系统解析器：localhost 解析到回环，必须被拒绝。
	if _, err := (Fetcher{}).Fetch(context.Background(), "https://localhost/SKILL.md"); !errors.Is(err, ErrURLForbidden) {
		t.Errorf("localhost: %v", err)
	}
}

type fetchServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	lookups  map[string][]net.IP
}

// newFetchServer 启动 TLS 测试服务；Fetcher 的解析与拨号被注入：任何主机名都解析到 lookups 指定的地址（默认公网地址），
// 通过地址校验后实际连接测试服务。生产代码默认严格，这里只替换「解析」与「建立连接」两步。
func newFetchServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*fetchServer, Fetcher) {
	t.Helper()
	fs := &fetchServer{lookups: map[string][]net.IP{}}
	fs.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.requests = append(fs.requests, r)
		fs.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(fs.Close)
	address := fs.Listener.Addr().String()
	return fs, Fetcher{
		LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
			if ips, ok := fs.lookups[host]; ok {
				return ips, nil
			}
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
		TLSConfig: &tls.Config{InsecureSkipVerify: true},
		Timeout:   5 * time.Second,
	}
}

func TestFetchDirectMarkdown(t *testing.T) {
	_, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "OpenFox-SkillImporter/1" || r.Header.Get("Cookie") != "" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(skillMD("remote"))
	})
	result, err := fetcher.Fetch(context.Background(), "https://skills.example.test/path/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildPackage(result.Files)
	if err != nil || pkg.Name != "remote" || result.Host != "skills.example.test" {
		t.Fatalf("pkg = %+v host = %q err = %v", pkg, result.Host, err)
	}
}

func TestFetchZip(t *testing.T) {
	data := buildZip(t, map[string]string{"demo/SKILL.md": string(skillMD("zipped")), "demo/a.md": "a"}, nil)
	_, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) })
	result, err := fetcher.Fetch(context.Background(), "https://example.test/download/skill.zip")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildPackage(result.Files)
	if err != nil || pkg.Name != "zipped" || len(pkg.Files) != 2 {
		t.Fatalf("pkg = %+v, %v", pkg, err)
	}
}

func TestFetchRejectsInternalTargets(t *testing.T) {
	server, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(skillMD("x")) })
	server.lookups["internal.test"] = []net.IP{net.ParseIP("10.0.0.5")}
	server.lookups["meta.test"] = []net.IP{net.ParseIP("169.254.169.254")}
	server.lookups["mapped.test"] = []net.IP{net.ParseIP("::ffff:127.0.0.1")}
	server.lookups["mixed.test"] = []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}
	server.lookups["ula.test"] = []net.IP{net.ParseIP("fd00::1")}
	for _, host := range []string{"internal.test", "meta.test", "mapped.test", "mixed.test", "ula.test"} {
		if _, err := fetcher.Fetch(context.Background(), "https://"+host+"/SKILL.md"); !errors.Is(err, ErrURLForbidden) {
			t.Errorf("%s: err = %v", host, err)
		}
	}
	if len(server.requests) != 0 {
		t.Errorf("no request should reach the server, got %d", len(server.requests))
	}
}

func TestFetchRedirects(t *testing.T) {
	server, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/to-ip":
			http.Redirect(w, r, "https://127.0.0.1/secret", http.StatusFound)
		case r.URL.Path == "/to-http":
			http.Redirect(w, r, "http://example.test/ok", http.StatusFound)
		case r.URL.Path == "/to-internal-name":
			http.Redirect(w, r, "https://internal.test/ok", http.StatusFound)
		case r.URL.Path == "/to-port":
			http.Redirect(w, r, "https://example.test:8443/ok", http.StatusFound)
		case r.URL.Path == "/to-userinfo":
			http.Redirect(w, r, "https://a:b@example.test/ok", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/r/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/"))
			if n == 0 {
				_, _ = w.Write(skillMD("redirected"))
				return
			}
			http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	server.lookups["internal.test"] = []net.IP{net.ParseIP("192.168.1.1")}
	for _, path := range []string{"/to-ip", "/to-http", "/to-internal-name", "/to-port", "/to-userinfo"} {
		if _, err := fetcher.Fetch(context.Background(), "https://example.test"+path); !errors.Is(err, ErrURLForbidden) {
			t.Errorf("%s: err = %v", path, err)
		}
	}
	// 3 次重定向可以成功，第 4 次失败。
	if _, err := fetcher.Fetch(context.Background(), "https://example.test/r/3"); err != nil {
		t.Errorf("3 redirects should work: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), "https://example.test/r/4"); err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("4 redirects should fail: %v", err)
	}
}

func TestFetchLimits(t *testing.T) {
	_, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Length", "6000000")
			_, _ = w.Write(make([]byte, 6000000))
		case "/chunked":
			flusher := w.(http.Flusher)
			chunk := make([]byte, 1<<20)
			for i := 0; i < 7; i++ {
				_, _ = w.Write(chunk)
				flusher.Flush()
			}
		case "/exact":
			_, _ = w.Write(make([]byte, MaxTotalBytes))
		case "/status":
			http.Error(w, "nope", http.StatusNotFound)
		case "/slow":
			time.Sleep(2 * time.Second)
		case "/bomb":
			_, _ = w.Write(buildZip(t, map[string]string{"SKILL.md": string(skillMD("a")), "z.bin": strings.Repeat("\x00", MaxTotalBytes+1)}, nil))
		}
	})
	for _, path := range []string{"/big", "/chunked", "/bomb"} {
		if _, err := fetcher.Fetch(context.Background(), "https://example.test"+path); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: err = %v", path, err)
		}
	}
	if result, err := fetcher.Fetch(context.Background(), "https://example.test/exact"); err != nil || len(result.Files[0].Content) != MaxTotalBytes {
		t.Errorf("exact limit: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), "https://example.test/status"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("status: %v", err)
	}
	fetcher.Timeout = 300 * time.Millisecond
	started := time.Now()
	if _, err := fetcher.Fetch(context.Background(), "https://example.test/slow"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Error("timeout was not enforced")
	}
}

func TestGitHubTarget(t *testing.T) {
	parse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	raw, tree, err := GitHubTarget(parse("https://github.com/acme/skills/blob/main/pdf/SKILL.md"))
	if err != nil || tree != nil || raw.String() != "https://raw.githubusercontent.com/acme/skills/main/pdf/SKILL.md" {
		t.Errorf("blob: %v %v %v", raw, tree, err)
	}
	raw, tree, err = GitHubTarget(parse("https://github.com/acme/skills/tree/v1.2/skills/pdf"))
	if err != nil || raw != nil || *tree != (GitHubTree{Owner: "acme", Repo: "skills", Ref: "v1.2", Dir: "skills/pdf"}) {
		t.Errorf("tree: %v %v %v", raw, tree, err)
	}
	_, tree, err = GitHubTarget(parse("https://github.com/acme/skills/tree/main"))
	if err != nil || tree == nil || tree.Dir != "" {
		t.Errorf("tree root: %v %v", tree, err)
	}
	passthrough := []string{"https://example.com/a.zip", "https://github.com/acme/skills", "https://github.com/acme/skills/issues/1", "https://raw.githubusercontent.com/a/b/main/SKILL.md"}
	for _, value := range passthrough {
		u := parse(value)
		got, tree, err := GitHubTarget(u)
		if err != nil || tree != nil || got != u {
			t.Errorf("%s should pass through: %v %v %v", value, got, tree, err)
		}
	}
	for _, value := range []string{"https://github.com/acme/skills/blob/main", "https://github.com/a b/skills/blob/main/x", "https://github.com/acme/skills/blob/main/../x"} {
		if _, _, err := GitHubTarget(parse(value)); !errors.Is(err, ErrURLInvalid) {
			t.Errorf("%s: %v", value, err)
		}
	}
}

func TestFetchGitHubBlobAndTree(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	_, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Host+r.URL.RequestURI())
		mu.Unlock()
		switch {
		case r.Host == "raw.githubusercontent.com" && r.URL.Path == "/acme/skills/main/pdf/SKILL.md":
			_, _ = w.Write(skillMD("pdf"))
		case r.Host == "raw.githubusercontent.com" && r.URL.Path == "/acme/skills/main/pdf/ref/a.md":
			_, _ = w.Write([]byte("a"))
		case r.Host == "api.github.com" && r.URL.Path == "/repos/acme/skills/contents/pdf":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "SKILL.md", "path": "pdf/SKILL.md", "type": "file", "size": 10, "download_url": "https://raw.githubusercontent.com/acme/skills/main/pdf/SKILL.md"},
				{"name": "ref", "path": "pdf/ref", "type": "dir", "size": 0, "download_url": nil},
			})
		case r.Host == "api.github.com" && r.URL.Path == "/repos/acme/skills/contents/pdf/ref":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "a.md", "path": "pdf/ref/a.md", "type": "file", "size": 1, "download_url": "https://raw.githubusercontent.com/acme/skills/main/pdf/ref/a.md"},
			})
		case r.Host == "api.github.com" && r.URL.Path == "/repos/acme/skills/contents/evil":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "SKILL.md", "path": "evil/SKILL.md", "type": "file", "size": 1, "download_url": "https://evil.example.test/x"},
			})
		default:
			http.NotFound(w, r)
		}
	})
	result, err := fetcher.Fetch(context.Background(), "https://github.com/acme/skills/blob/main/pdf/SKILL.md")
	if err != nil || result.Host != "github.com" || len(result.Files) != 1 {
		t.Fatalf("blob: %+v %v", result, err)
	}
	result, err = fetcher.Fetch(context.Background(), "https://github.com/acme/skills/tree/main/pdf")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildPackage(result.Files)
	if err != nil || pkg.Name != "pdf" || len(pkg.Files) != 2 || pkg.Files[1].Path != "ref/a.md" {
		t.Fatalf("tree: %+v %v", pkg, err)
	}
	if _, err := fetcher.Fetch(context.Background(), "https://github.com/acme/skills/tree/main/evil"); err == nil {
		t.Error("download_url outside raw.githubusercontent.com should be refused")
	}
	if !strings.Contains(strings.Join(seen, " "), "api.github.com/repos/acme/skills/contents/pdf?ref=main") {
		t.Errorf("requests = %v", seen)
	}
}

func TestFetchGitHubTreeLimits(t *testing.T) {
	_, fetcher := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/skills/contents/many":
			items := make([]map[string]any, 101)
			for i := range items {
				items[i] = map[string]any{"name": "f", "path": "many/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)), "type": "file", "size": 1,
					"download_url": "https://raw.githubusercontent.com/acme/skills/main/x"}
			}
			_ = json.NewEncoder(w).Encode(items)
		case "/repos/acme/skills/contents/large":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "b", "path": "large/b", "type": "file", "size": MaxTotalBytes + 1,
				"download_url": "https://raw.githubusercontent.com/acme/skills/main/b"}})
		case "/repos/acme/skills/contents/link":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "l", "path": "link/l", "type": "symlink", "size": 1, "download_url": nil}})
		default:
			_, _ = w.Write([]byte("x"))
		}
	})
	for _, dir := range []string{"many", "large"} {
		if _, err := fetcher.Fetch(context.Background(), "https://github.com/acme/skills/tree/main/"+dir); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if _, err := fetcher.Fetch(context.Background(), "https://github.com/acme/skills/tree/main/link"); !errors.Is(err, ErrInvalid) {
		t.Errorf("symlink: %v", err)
	}
}
