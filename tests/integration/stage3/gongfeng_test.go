package stage3

import (
	"bufio"
	"context"
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
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/secrets"
)

type gongfengAPIFake struct {
	mu                          sync.Mutex
	hook                        map[string]any
	secret                      string
	requests, creates, comments int
	tokenRevoked                bool
	afterIdentity               func()
}

func (f *gongfengAPIFake) project() map[string]any {
	return map[string]any{"id": 81, "name": "Controlled project", "path_with_namespace": "fixture/project", "type": "GIT", "archived": false, "issues_enabled": true, "visibility_level": 0, "public": false}
}
func (f *gongfengAPIFake) issue() map[string]any {
	return map[string]any{"id": 901, "iid": 7, "project_id": 81, "title": "Controlled Gongfeng issue", "description": "Actual local TLS source", "state": "opened", "confidential": false, "author": map[string]any{"id": 71, "username": "fixture", "state": "active"}, "assignees": []any{}, "created_at": "2026-11-14T01:00:00Z", "updated_at": "2026-11-14T01:00:00Z"}
}
func (f *gongfengAPIFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	w.Header().Set("Content-Type", "application/json")
	if r.Host != "git.tencent.com" || r.Header.Get("PRIVATE-TOKEN") == "" {
		w.WriteHeader(400)
		return
	}
	if f.tokenRevoked {
		w.WriteHeader(401)
		return
	}
	switch r.URL.Path {
	case "/api/v3/user":
		json.NewEncoder(w).Encode(map[string]any{"id": 71, "username": "fixture", "state": "active"})
		if f.afterIdentity != nil {
			f.afterIdentity()
		}
	case "/api/v3/projects":
		json.NewEncoder(w).Encode([]any{f.project()})
	case "/api/v3/projects/81":
		json.NewEncoder(w).Encode(f.project())
	case "/api/v3/projects/81/members/all":
		json.NewEncoder(w).Encode([]any{map[string]any{"id": 71, "username": "fixture", "state": "active", "access_level": 40, "expires_at": nil}})
	case "/api/v3/projects/81/members/all/71":
		json.NewEncoder(w).Encode(map[string]any{"id": 71, "username": "fixture", "state": "active", "access_level": 40, "expires_at": nil})
	case "/api/v3/projects/81/hooks":
		if r.Method == "POST" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(400)
				return
			}
			f.secret, _ = body["token"].(string)
			delete(body, "token")
			body["id"] = 41
			body["project_id"] = 81
			body["active"] = false
			f.hook = body
			f.creates++
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(body)
			return
		}
		rows := []any{}
		if f.hook != nil {
			rows = append(rows, f.hook)
		}
		json.NewEncoder(w).Encode(rows)
	case "/api/v3/projects/81/hooks/41":
		if r.Method == "DELETE" {
			f.hook = nil
			json.NewEncoder(w).Encode(true)
			return
		}
		if f.hook == nil {
			w.WriteHeader(404)
			return
		}
		if r.Method == "PUT" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.secret, _ = body["token"].(string)
			delete(body, "token")
			body["id"] = 41
			body["project_id"] = 81
			body["active"] = false
			f.hook = body
		}
		json.NewEncoder(w).Encode(f.hook)
	case "/api/v3/projects/81/hooks/41/activate":
		if f.hook != nil {
			f.hook["active"] = true
		}
		json.NewEncoder(w).Encode(true)
	case "/api/v3/projects/81/issues":
		rows := []any{}
		if r.URL.Query().Get("state") == "opened" && r.URL.Query().Get("updated_after") == "" {
			rows = append(rows, f.issue())
		}
		json.NewEncoder(w).Encode(rows)
	case "/api/v3/projects/81/issues/901":
		json.NewEncoder(w).Encode(f.issue())
	case "/api/v3/projects/81/issues/901/notes":
		if r.Method == "POST" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.comments++
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": 501, "body": body["body"], "author": map[string]any{"id": 71, "username": "fixture"}, "system": false, "created_at": time.Now().UTC().Format(time.RFC3339), "noteable_id": 901, "noteable_type": "Issue", "project_id": 81})
			return
		}
		json.NewEncoder(w).Encode([]any{})
	default:
		w.WriteHeader(404)
	}
}

type gongfengAPIEnv struct {
	*s3Env
	fake   *gongfengAPIFake
	svc    integrations.Service
	worker *integrations.SyncWorker
	box    *secrets.Box
}

func newGongfengAPIEnv(t *testing.T) *gongfengAPIEnv {
	e := newS3Env(t)
	f := &gongfengAPIFake{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(upstream.Close)
	tr := upstream.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = upstream.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "git.tencent.com:443" {
			t.Errorf("unexpected upstream %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	client := gongfeng.New()
	client.HTTP.Transport = tr
	key := make([]byte, secrets.KeySize)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.Service{Gongfeng: client, Box: box, PublicURL: "https://openfox.example.test"}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, GongfengClient: client, PublicURL: svc.PublicURL}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, e.org, e.otherOrg)
	return &gongfengAPIEnv{s3Env: e, fake: f, svc: svc, box: box, worker: &integrations.SyncWorker{Pool: e.database, Service: svc, Instance: "gongfeng-api-controlled"}}
}
func (e *gongfengAPIEnv) pass() {
	e.t.Helper()
	if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
}
func (e *gongfengAPIEnv) bind() integrations.Connection {
	e.t.Helper()
	r := e.owner().do("POST", "integrations/gongfeng/token", map[string]any{"access_token": "fixture-pat"})
	if r.Status != 201 {
		e.t.Fatalf("bind %d %s", r.Status, r.Body)
	}
	var c integrations.Connection
	r.decode(e.t, &c)
	return c
}
func TestGongfengHTTPAuthenticationHookAndImport(t *testing.T) {
	e := newGongfengAPIEnv(t)
	denied := e.member().do("POST", "integrations/gongfeng/token", map[string]any{"access_token": "fixture-pat"})
	if denied.Status != 403 {
		t.Fatal("member binding", denied.Status, string(denied.Body))
	}
	c := e.bind()
	base := "integrations/connections/" + c.ID + "/gongfeng"
	projects := e.owner().do("GET", base+"/projects", nil)
	if projects.Status != 200 || !strings.Contains(string(projects.Body), "fixture/project") {
		t.Fatal(projects.Status, string(projects.Body))
	}
	denied = e.outsider().do("GET", base+"/configuration", nil)
	if denied.Status != 403 && denied.Status != 404 {
		t.Fatal("cross tenant configuration", denied.Status)
	}
	response := e.owner().do("POST", "integrations/gongfeng/repositories", map[string]any{"connection_id": c.ID, "connection_version": c.Version, "project_id": 81, "space_id": e.space.ID, "import_existing": true})
	if response.Status != 201 {
		t.Fatal(response.Status, string(response.Body))
	}
	var repo integrations.GongfengRepository
	response.decode(t, &repo)
	if repo.Status != "PAUSED" {
		t.Fatal(repo.Status)
	}
	for i := 0; i < 9; i++ {
		e.pass()
	}
	config := e.owner().do("GET", base+"/configuration", nil)
	if config.Status != 200 {
		t.Fatal(config.Status, string(config.Body))
	}
	var cfg integrations.GongfengConfiguration
	config.decode(t, &cfg)
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].Status != "ACTIVE" || cfg.Repositories[0].Progress.Issues == nil || cfg.Repositories[0].Progress.Issues.Status != "complete" {
		t.Fatalf("configuration: %s", config.Body)
	}
	if strings.Contains(string(config.Body), "fixture-pat") || strings.Contains(string(config.Body), "hooks/git/") || strings.Contains(string(config.Body), "hook_url_hash") {
		t.Fatal("configuration disclosed credential")
	}
	if count := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='Controlled Gongfeng issue'`, e.org); count != 1 {
		t.Fatal("import", count)
	}
	e.fake.mu.Lock()
	callback, _ := e.fake.hook["url"].(string)
	secret := e.fake.secret
	before := e.fake.requests
	creates, comments := e.fake.creates, e.fake.comments
	e.fake.mu.Unlock()
	if creates != 1 || comments != 1 {
		t.Fatal("hook/backlink effects", creates, comments)
	}
	u, _ := url.Parse(callback)
	body := `{"object_kind":"issue","project_id":81,"object_attributes":{"id":901,"project_id":81}}`
	send := func(test bool) int {
		t.Helper()
		payload := body
		if test {
			payload = ""
		}
		q, _ := http.NewRequestWithContext(e.ctx, "POST", e.server.URL+strings.TrimPrefix(u.Path, "/api"), strings.NewReader(payload))
		q.Header.Set("X-Token", secret)
		q.Header.Set("X-Event", "Issue Hook")
		if test {
			q.Header.Set("X-Source-Type", "Test")
		}
		r, err := http.DefaultClient.Do(q)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		io.Copy(io.Discard, r.Body)
		return r.StatusCode
	}
	if status := send(true); status != 200 {
		t.Fatal("Test hook", status)
	}
	if status := send(false); status != 200 {
		t.Fatal("event", status)
	}
	e.fake.mu.Lock()
	after := e.fake.requests
	e.fake.mu.Unlock()
	if after != before {
		t.Fatal("webhook ACK performed HTTP", before, after)
	}
	e.pass()
	if count := e.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='Controlled Gongfeng issue'`, e.org); count != 1 {
		t.Fatal("duplicate import", count)
	}
	e.fake.mu.Lock()
	e.fake.tokenRevoked = true
	e.fake.mu.Unlock()
	revoked := e.owner().do("GET", base+"/projects", nil)
	if revoked.Status < 400 {
		t.Fatal("revoked token accepted")
	}
	var status string
	e.scalar(&status, `SELECT status FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, c.ID)
	if status != "NEEDS_ATTENTION" {
		t.Fatal("revoked token state", status)
	}
}

func TestGongfengBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_GONGFENG_BROWSER_FILE")
	if file == "" {
		t.Skip("local browser harness not requested")
	}
	e := newGongfengAPIEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:18214")
	if err != nil {
		t.Fatal(err)
	}
	browser := httptest.NewUnstartedServer(http.StripPrefix("/api", e.server.Config.Handler))
	browser.Listener.Close()
	browser.Listener = listener
	browser.Start()
	t.Cleanup(browser.Close)
	raw, _ := json.Marshal(map[string]string{"organization_id": e.org, "space_id": e.space.ID, "email": s3Email(s3OwnerID), "password": s3Password, "api_url": browser.URL})
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			t.Fatal("browser fixture timeout")
		case <-ticker.C:
			if _, err = os.Stat(file + ".stop"); err == nil {
				return
			}
			e.pass()
		}
	}
}

func TestGongfengRevocationAndWebhookBounds(t *testing.T) {
	e := newGongfengAPIEnv(t)
	owner := e.admin()
	e.fake.mu.Lock()
	e.fake.afterIdentity = func() {
		e.exec(`UPDATE openfox.organization_members SET role='MEMBER',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3AdminID)
	}
	e.fake.mu.Unlock()
	r := owner.do("POST", "integrations/gongfeng/token", map[string]any{"access_token": "fixture-pat"})
	if r.Status != 401 || !strings.Contains(string(r.Body), "AUTHORIZATION_STALE") {
		t.Fatalf("late authorization %d %s", r.Status, r.Body)
	}
	if n := e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='gongfeng'`, e.org); n != 0 {
		t.Fatal("revoked creator saved PAT", n)
	}
	token := strings.Repeat("x", 43)
	u, _ := url.Parse(e.server.URL)
	c, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = fmt.Fprintf(c, "POST /v1/hooks/git/gongfeng/%s HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{", token, u.Host)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal("slow body did not get bounded rejection", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 400 || time.Since(start) > 4*time.Second {
		t.Fatal("body deadline", response.StatusCode, time.Since(start))
	}
	large, err := http.Post(e.server.URL+"/v1/hooks/git/gongfeng/"+token, "application/json", strings.NewReader(strings.Repeat(" ", 5<<20|1)))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, large.Body)
	large.Body.Close()
	if large.StatusCode != 413 {
		t.Fatal("body size", large.StatusCode)
	}
	e.fake.mu.Lock()
	requests := e.fake.requests
	e.fake.mu.Unlock()
	if requests != 1 {
		t.Fatal("webhook budget checks called platform", requests)
	}
}
