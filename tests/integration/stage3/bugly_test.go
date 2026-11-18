package stage3

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/secrets"
)

func newBuglyAPIEnv(t *testing.T) *s3Env {
	dsn := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL not set")
	}
	u, e := url.Parse(dsn)
	if e != nil || !strings.HasPrefix(u.Path, "/openfox_i07_") {
		t.Fatal("requires exclusive I07 database")
	}
	env := newS3Env(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, e := secrets.New(key)
	if e != nil {
		t.Fatal(e)
	}
	publicURL := "https://openfox.example.test"
	if raw := os.Getenv("OPENFOX_I07_BROWSER_PUBLIC_URL"); raw != "" {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
			t.Fatal("browser fixture requires owned loopback URL")
		}
		publicURL = raw
	}
	env.server = httptest.NewServer(api.NewWithDispatch(env.auth, env.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, PublicURL: publicURL}))
	t.Cleanup(env.server.Close)
	env.clients = map[string]*s3Client{}
	env.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, env.org, env.otherOrg)
	return env
}

func TestBuglyBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_BUGLY_BROWSER_FILE")
	if file == "" {
		t.Skip("local browser harness not requested")
	}
	e := newBuglyAPIEnv(t)
	raw, _ := json.Marshal(map[string]string{"organization_id": e.org, "space_id": e.space.ID, "email": s3Email(s3OwnerID), "password": s3Password, "api_url": e.server.URL})
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			t.Fatal("browser fixture timeout")
		case <-ticker.C:
			if _, err := os.Stat(file + ".stop"); err == nil {
				return
			}
		}
	}
}
func buglyConfigureHTTP(t *testing.T, e *s3Env) (integrations.Connection, integrations.BuglyBinding, string) {
	t.Helper()
	var c integrations.Connection
	owner := e.owner()
	owner.must(201, "POST", "integrations/bugly/connections", map[string]any{}, &c)
	var kind string
	e.scalar(&kind, `SELECT id::text FROM openfox.task_types WHERE organization_id=$1::uuid AND space_id=$2::uuid AND is_default`, e.org, e.space.ID)
	var b integrations.BuglyBinding
	owner.must(201, "POST", "integrations/bugly/connections/"+c.ID+"/bindings", map[string]any{"connection_version": c.Version, "product_id": "ExactProduct", "space_id": e.space.ID, "task_type_id": kind, "priority_mapping": map[string]string{"urgent": "HIGH"}}, &b)
	owner.must(200, "POST", "integrations/bugly/connections/"+c.ID+"/enable", map[string]any{"version": c.Version}, &c)
	var secret integrations.BuglySecret
	r := owner.must(200, "POST", "integrations/bugly/connections/"+c.ID+"/secret", map[string]any{"version": c.Version}, &secret)
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("secret cacheable")
	}
	u, _ := url.Parse(secret.URL)
	return c, b, strings.TrimPrefix(u.Path, "/api")
}
func buglyPost(t *testing.T, e *s3Env, path string, body []byte) (int, integrations.BuglyReceipt) {
	t.Helper()
	r, err := http.Post(e.server.URL+path, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out integrations.BuglyReceipt
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(r.StatusCode, string(raw), err)
	}
	return r.StatusCode, out
}
func buglyHTTPBody(issue string) []byte {
	b, _ := json.Marshal(map[string]any{"ProductID": "ExactProduct", "IssueKey": issue, "IssueType": "crash", "ProjectName": "arbitrary display", "FieldValues": map[string]any{"title": "Bugly actual HTTP task", "description": "<script>alert('not executable')</script> https://external.example/path?token=CANARY_SECRET&ok=1", "priority": "urgent", "workspace_id": "untrusted", "executor_agent_id": "untrusted"}})
	return b
}
func TestBuglyHTTPCreateAuthorizationAndDuplicate(t *testing.T) {
	e := newBuglyAPIEnv(t)
	e.member().expectError(403, "FORBIDDEN", "POST", "integrations/bugly/connections", map[string]any{})
	c, b, path := buglyConfigureHTTP(t, e)
	e.member().expectError(403, "FORBIDDEN", "GET", "integrations/bugly/configuration", nil)
	e.outsider().expectError(404, "NOT_FOUND", "GET", "integrations/bugly/configuration", nil)
	e.owner().expectError(403, "CSRF_REQUIRED", "POST", "integrations/bugly/connections/"+c.ID+"/rotate", map[string]any{"version": c.Version}, "X-CSRF-Token", "")
	status, out := buglyPost(t, e, path, buglyHTTPBody("CASE-001"))
	if status != 200 || out.Code != 0 || !strings.Contains(out.URL, e.space.ID) {
		t.Fatal(status, out)
	}
	status, duplicate := buglyPost(t, e, path, buglyHTTPBody("CASE-001"))
	if status != 200 || duplicate.URL != out.URL {
		t.Fatal(status, duplicate)
	}
	var taskID, description, priority string
	var assignees int
	e.scalar(&taskID, `SELECT task_id::text FROM openfox.integration_bugly_sources WHERE organization_id=$1::uuid AND binding_id=$2::uuid`, e.org, b.ID)
	e.scalar(&description, `SELECT description FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, taskID)
	e.scalar(&priority, `SELECT priority FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, taskID)
	e.scalar(&assignees, `SELECT count(*) FROM openfox.task_assignees WHERE organization_id=$1::uuid AND task_id=$2::uuid`, e.org, taskID)
	if strings.Contains(description, "CANARY_SECRET") || !strings.HasPrefix(description, "```text") || priority != "HIGH" || assignees != 0 {
		t.Fatal("unsafe fields imported", description, priority, assignees)
	}
	var config integrations.BuglyConfiguration
	e.owner().must(200, "GET", "integrations/bugly/configuration", nil, &config)
	if len(config.Deliveries) != 2 || len(config.Bindings) != 1 || config.Connection.WebhookLastDeliveryAt == nil {
		t.Fatal("durable receipt missing", config)
	}
	e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/rotate", map[string]any{"version": c.Version}, &c)
	status, out = buglyPost(t, e, path, buglyHTTPBody("old-url"))
	if status != 404 || out.Code == 0 {
		t.Fatal("old URL valid", status, out)
	}
	e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/disconnect", map[string]any{"version": c.Version}, &c)
	e.owner().must(200, "GET", "integrations/bugly/configuration", nil, &config)
	if len(config.Bindings) != 1 || config.Connection.Status != integrations.StatusDisconnected {
		t.Fatal("disconnect erased provenance", config)
	}
	// Explicit reconnect keeps the source identity but generates a new URL.
	e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/enable", map[string]any{"version": c.Version}, &c)
	var secret integrations.BuglySecret
	e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/secret", map[string]any{"version": c.Version}, &secret)
	u, _ := url.Parse(secret.URL)
	freshPath := strings.TrimPrefix(u.Path, "/api")
	if freshPath == path {
		t.Fatal("reconnect reused disconnected token")
	}
	status, out = buglyPost(t, e, freshPath, buglyHTTPBody("CASE-001"))
	if status != 200 || out.URL != duplicate.URL {
		t.Fatal("reconnect duplicated prior source", status, out)
	}
	base := "integrations/bugly/connections/" + c.ID + "/bindings/" + b.ID
	oldVersion := b.Version
	e.owner().must(200, "POST", base+"/pause", map[string]any{"version": b.Version}, &b)
	e.owner().expectError(409, "VERSION_CONFLICT", "POST", base+"/resume", map[string]any{"version": oldVersion})
	status, out = buglyPost(t, e, freshPath, buglyHTTPBody("paused"))
	if status != 409 || out.Message != "binding_inactive" {
		t.Fatal(status, out)
	}
	e.owner().must(200, "POST", base+"/resume", map[string]any{"version": b.Version}, &b)
	e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/disable", map[string]any{"version": c.Version}, &c)
	status, out = buglyPost(t, e, freshPath, buglyHTTPBody("disabled"))
	if status != 404 {
		t.Fatal(status, out)
	}
	e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/enable", map[string]any{"version": c.Version}, &c)
	e.owner().must(200, "POST", base+"/remove", map[string]any{"version": b.Version}, &b)
	status, out = buglyPost(t, e, freshPath, buglyHTTPBody("removed"))
	if status != 422 || out.Message != "product_not_bound" {
		t.Fatal(status, out)
	}
	e.owner().must(200, "GET", "integrations/bugly/configuration", nil, &config)
	if len(config.Bindings) != 0 {
		t.Fatal("removed route listed")
	}
}
func TestBuglyHTTPBoundsAndPrivateSpace(t *testing.T) {
	e := newBuglyAPIEnv(t)
	c, _, path := buglyConfigureHTTP(t, e)
	for name, tc := range map[string]struct {
		body   []byte
		status int
	}{"invalid": {[]byte(`{"x":1,"x":2}`), 400}, "limit": {[]byte(strings.Repeat(" ", integrations.BuglyBodyLimit+1)), 413}, "unknown_product": {[]byte(strings.Replace(string(buglyHTTPBody("x")), "ExactProduct", "exactproduct", 1)), 422}} {
		t.Run(name, func(t *testing.T) {
			status, out := buglyPost(t, e, path, tc.body)
			if status != tc.status || out.Code == 0 || out.URL != "" {
				t.Fatal(status, out)
			}
		})
	}
	private := e.createSpace("private")
	var kind string
	e.scalar(&kind, `SELECT id::text FROM openfox.task_types WHERE organization_id=$1::uuid AND space_id=$2::uuid AND is_default`, e.org, private.ID)
	e.admin().expectError(403, "FORBIDDEN", "POST", "integrations/bugly/connections/"+c.ID+"/bindings", map[string]any{"connection_version": c.Version, "product_id": "Private", "space_id": private.ID, "task_type_id": kind})
	e.addSpaceMember(e.space.ID, s3AdminID, "VIEWER")
	e.admin().expectError(403, "FORBIDDEN", "POST", "integrations/bugly/connections/"+c.ID+"/bindings", map[string]any{"connection_version": c.Version, "product_id": "Viewer", "space_id": e.space.ID, "task_type_id": kind})
	// Per-key Unicode limits do not imply that the aggregate JSONB fits.
	e.scalar(&kind, `SELECT id::text FROM openfox.task_types WHERE organization_id=$1::uuid AND space_id=$2::uuid AND is_default`, e.org, e.space.ID)
	oversizedMapping := map[string]string{}
	for i := range 20 {
		oversizedMapping[fmt.Sprintf("%02d%s", i, strings.Repeat("😀", 62))] = "HIGH"
	}
	e.owner().expectError(400, "INVALID_REQUEST", "POST", "integrations/bugly/connections/"+c.ID+"/bindings", map[string]any{"connection_version": c.Version, "product_id": "MappingBudget", "space_id": e.space.ID, "task_type_id": kind, "priority_mapping": oversizedMapping})
	// A valid URL with a partial body remains bounded without holding a DB tx.
	u, _ := url.Parse(e.server.URL)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{", path, u.Host); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal("slow body unbounded", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 400 || time.Since(start) > 4*time.Second {
		t.Fatal("read deadline", response.StatusCode, time.Since(start))
	}
	t.Run("rate_limit_and_bounded_receipts", func(t *testing.T) {
		// A fresh URL gives this actual HTTP test a new limiter key. Invalid
		// complete payloads still consume the budget and persist safe receipts.
		e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/rotate", map[string]any{"version": c.Version}, &c)
		var secret integrations.BuglySecret
		e.owner().must(200, "POST", "integrations/bugly/connections/"+c.ID+"/secret", map[string]any{"version": c.Version}, &secret)
		u, _ := url.Parse(secret.URL)
		for i := range 61 {
			r, err := http.Post(e.server.URL+strings.TrimPrefix(u.Path, "/api"), "application/json", strings.NewReader(`{"x":1,"x":2}`))
			if err != nil {
				t.Fatal(err)
			}
			var receipt integrations.BuglyReceipt
			err = json.NewDecoder(r.Body).Decode(&receipt)
			r.Body.Close()
			want := 400
			if i == 60 {
				want = 429
				if r.Header.Get("Retry-After") == "" {
					t.Fatal("rate limit missing retry guidance")
				}
			}
			if err != nil || r.StatusCode != want || receipt.Code == 0 || receipt.URL != "" {
				t.Fatal("bounded request", i, r.StatusCode, err, receipt)
			}
		}
		var configuration integrations.BuglyConfiguration
		e.owner().must(200, "GET", "integrations/bugly/configuration", nil, &configuration)
		if len(configuration.Deliveries) != 50 {
			t.Fatal("receipt query must remain bounded", len(configuration.Deliveries))
		}
	})
}
