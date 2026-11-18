package stage12i

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Browser mode uses the same temporary stack and real runtime as the API drill.
// Session cookies are only written to a private temporary fixture, never logs.
func (s *stack) startReviewBrowserWeb() string {
	s.t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		s.t.Fatal(err)
	}
	if os.Getenv("OFD_REAL_REVIEW_BUILT") == "1" || os.Getenv("OFD_REAL_OFFICE_STAGE8") == "1" {
		upstream, _ := url.Parse(s.center)
		proxy := httputil.NewSingleHostReverseProxy(upstream)
		conf, err := os.ReadFile(filepath.Join(root, "deploy/nginx-web.example.conf"))
		if err != nil {
			s.t.Fatal(err)
		}
		policy := regexp.MustCompile(`Content-Security-Policy "([^"]+)"`).FindStringSubmatch(string(conf))[1]
		static := http.FileServer(http.Dir(filepath.Join(root, "apps/web/dist")))
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				proxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Security-Policy", policy)
			if filepath.Ext(r.URL.Path) == "" {
				http.ServeFile(w, r, filepath.Join(root, "apps/web/dist/index.html"))
				return
			}
			if strings.HasSuffix(r.URL.Path, ".mjs") {
				w.Header().Set("Content-Type", "text/javascript")
			}
			static.ServeHTTP(w, r)
		}))
		s.t.Cleanup(server.Close)
		return server.URL
	}
	port := freePort(s.t)
	origin := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := append(os.Environ(), "OPENFOX_WEB_PORT="+fmt.Sprint(port), "OPENFOX_WEB_API_TARGET="+s.center)
	web := startProc(s.t, "review browser web", filepath.Join(s.dir, "web.log"), env, "pnpm", "--dir", root, "dev")
	s.waitFor("browser web ready", time.Minute, func() bool {
		if web.exited() {
			s.t.Fatalf("web exited: %v\n%s", web.err, tail(web.log))
		}
		response, e := (&http.Client{Timeout: time.Second}).Get(origin)
		if e != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == 200
	})
	return origin
}
func (s *stack) browserReview(origin, kind, conversation, space, node, artifact, document, quote, replacement string, details ...map[string]any) {
	s.t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		s.t.Fatal(err)
	}
	parsed, _ := url.Parse(s.center)
	cookies := []map[string]string{}
	for _, c := range s.owner().http.Jar.Cookies(parsed) {
		cookies = append(cookies, map[string]string{"name": c.Name, "value": c.Value, "url": origin})
	}
	fixture := map[string]any{"origin": origin, "kind": kind, "organization": s.org, "conversation": conversation, "space": space, "node": node, "artifact": artifact, "document": document, "quote": quote, "replacement": replacement, "cookies": cookies, "evidence": os.Getenv("OPENFOX_REVIEW_BROWSER_EVIDENCE")}
	for _, values := range details {
		for key, value := range values {
			fixture[key] = value
		}
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		s.t.Fatal(err)
	}
	name := filepath.Join(s.dir, "browser-"+kind+".json")
	if err = os.WriteFile(name, data, 0o600); err != nil {
		s.t.Fatal(err)
	}
	defer os.Remove(name)
	script := "tests/frontend/real-artifact-review.mjs"
	if fixture["stage8"] == true {
		script = "tests/frontend/real-office-review.mjs"
	}
	command := exec.CommandContext(s.ctx, "node", filepath.Join(root, script), name)
	output, err := command.CombinedOutput()
	if err != nil {
		s.t.Fatalf("%s browser: %v\n%s\n%s", kind, err, output, s.diagnostics())
	}
	s.t.Logf("%s", output)
}
