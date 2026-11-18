package stage3

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"openfox/internal/api"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/zohobugtracker"
	"openfox/internal/secrets"
)

type zohoFixture struct {
	mu          sync.Mutex
	org         string
	bugGets     int
	mode        string
	beforeBug   func()
	memberEmail string
}

func (f *zohoFixture) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-OpenFox-Test-Fixture", f.org)
	send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/oauth/v2/auth" {
		target, e := url.Parse(r.URL.Query().Get("redirect_uri"))
		if e != nil || target.Host != "127.0.0.1:4219" || target.Path != "/api/v1/integrations/zoho_bugtracker/callback" {
			w.WriteHeader(400)
			return
		}
		q := target.Query()
		q.Set("state", r.URL.Query().Get("state"))
		q.Set("code", "fixture")
		target.RawQuery = q.Encode()
		http.Redirect(w, r, target.String(), 302)
		return
	}
	if r.URL.Path == "/oauth/v2/token" {
		if r.Method != "POST" {
			w.WriteHeader(500)
			return
		}
		_ = r.ParseForm()
		v := map[string]any{"access_token": "org-access-" + f.org, "token_type": "Bearer", "expires_in": 3600}
		if r.Form.Get("client_id") == "identity-app" {
			v["access_token"] = "personal-access-" + f.org
		} else if r.Form.Get("grant_type") != "refresh_token" {
			v["refresh_token"] = "refresh-" + f.org
		}
		send(v)
		return
	}
	if r.Method != "GET" || !strings.HasPrefix(r.Header.Get("Authorization"), "Zoho-oauthtoken ") {
		w.WriteHeader(500)
		return
	}
	switch r.URL.Path {
	case "/restapi/portals/":
		id := "2060758"
		if strings.Contains(r.Header.Get("Authorization"), "personal-access") {
			id = "2060759"
		}
		send(map[string]any{"login_id": id, "portals": []any{map[string]any{"id": "2063927", "name": "Controlled portal", "role": "admin"}}})
	case "/restapi/portal/2063927/users/":
		send(map[string]any{"users": []any{map[string]any{"id": "2060758", "name": "Organization owner", "email": s3Email(s3OwnerID), "active": true, "role": "admin"}, map[string]any{"id": "2060759", "name": "Verified reporter", "email": s3Email(s3MemberID), "active": true, "role": "employee"}}})
	case "/restapi/portal/2063927/projects/", "/restapi/portal/2063927/projects/301/":
		send(map[string]any{"projects": []any{map[string]any{"id": "301", "name": "Controlled bugs", "status": "active"}}})
	case "/restapi/portal/2063927/projects/301/bugs/":
		f.mu.Lock()
		f.bugGets++
		mode, hook := f.mode, f.beforeBug
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		if mode == "429" {
			w.Header().Set("Retry-After", "2147483648")
			w.WriteHeader(429)
			send(map[string]string{"error": "controlled"})
			return
		}
		closed := r.URL.Query().Get("statustype") == "closed"
		bugs := []any{}
		if !closed || mode == "two" {
			id, status := "401", "11"
			if closed {
				id, status = "402", "12"
			}
			if mode == "unbound" {
				id = "411"
			}
			if mode == "manual" {
				id = "421"
			}
			bugs = append(bugs, map[string]any{"id": id, "title": "Bug <script>safe text</script> " + id, "status": map[string]string{"id": status, "type": "Source status"}, "severity": map[string]string{"id": "5", "type": "Source severity"}, "reporter_id": "2060759", "reported_person": "Verified reporter", "closed": closed, "created_time_long": 1767323045000, "description": "UNVERIFIED BODY MUST NOT IMPORT", "assignee_id": "2060758"})
		}
		if mode == "pages" || mode == "repeat" {
			bugs = []any{}
			if !closed {
				start := 0
				total := 100
				if r.URL.Query().Get("index") == "101" && mode == "pages" {
					start = 100
					total = 1
				}
				for n := start; n < start+total; n++ {
					bugs = append(bugs, map[string]any{"id": fmt.Sprint(1000 + n), "title": fmt.Sprint("Paged bug ", n), "status": map[string]string{"id": "11", "type": "Open"}, "closed": false})
				}
			}
		}
		send(map[string]any{"bugs": bugs})
	default:
		w.WriteHeader(404)
		send(map[string]any{})
	}
}

type zohoTransport func(*http.Request) (*http.Response, error)

func (f zohoTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type zohoEnv struct {
	*s3Env
	fake                          *zohoFixture
	svc                           integrations.Service
	client                        *zohobugtracker.Client
	box                           *secrets.Box
	upstreamAddress, upstreamSPKI string
}

func newZohoEnv(t *testing.T) *zohoEnv {
	e := newS3Env(t)
	e.exec(`UPDATE openfox_private.zoho_request_budget SET cooldown_until='-infinity',next_at='-infinity'`)
	f := &zohoFixture{org: e.org}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	tr := srv.Client().Transport
	seam := zohoTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "accounts.zoho.com" && r.URL.Host != "bugtracker.zoho.com") {
			return nil, fmt.Errorf("unexpected upstream")
		}
		q := r.Clone(r.Context())
		u := *r.URL
		u.Host = target.Host
		q.URL = &u
		return tr.RoundTrip(q)
	})
	key := make([]byte, secrets.KeySize)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	c := &zohobugtracker.Client{App: zohobugtracker.App{ClientID: "organization-app", ClientSecret: "fixture-secret"}, Transport: seam}
	personal := &zohobugtracker.Client{App: zohobugtracker.App{ClientID: "identity-app", ClientSecret: "fixture-secret"}, Transport: seam}
	s := integrations.Service{Zoho: c, ZohoIdentity: personal, Box: box, PublicURL: "http://localhost:18159"}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, ZohoClient: c, ZohoIdentityClient: personal, PublicURL: s.PublicURL}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	publicDER, err := x509.MarshalPKIXPublicKey(srv.Certificate().PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(publicDER)
	return &zohoEnv{s3Env: e, fake: f, svc: s, client: c, box: box, upstreamAddress: srv.Listener.Addr().String(), upstreamSPKI: base64.StdEncoding.EncodeToString(sum[:])}
}
func (e *zohoEnv) authorize(c *s3Client, in integrations.ZohoStartInput) integrations.ZohoSession {
	e.t.Helper()
	var start integrations.StartResult
	c.must(200, "POST", "integrations/zoho_bugtracker/oauth/start", in, &start)
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil || u.Host != "accounts.zoho.com" || u.Query().Get("code_challenge_method") != "S256" {
		e.t.Fatal("invalid OAuth URL")
	}
	old := c.http.CheckRedirect
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response := c.must(302, "GET", "/v1/integrations/zoho_bugtracker/callback?state="+u.Query().Get("state")+"&code=fixture", nil, nil)
	c.http.CheckRedirect = old
	location, _ := url.Parse(response.Header.Get("Location"))
	if location.Query().Get("status") != "ready" {
		e.t.Fatalf("callback failed: %s", location.Query().Get("reason"))
	}
	var session integrations.ZohoSession
	c.must(200, "GET", "integrations/zoho_bugtracker/oauth/sessions/"+start.SessionID, nil, &session)
	if session.Status != "SELECTING" {
		e.t.Fatal("not selecting", session.Status)
	}
	return session
}
func (e *zohoEnv) connect() integrations.Connection {
	s := e.authorize(e.owner(), integrations.ZohoStartInput{Purpose: "organization"})
	var c integrations.Connection
	e.owner().must(200, "POST", "integrations/zoho_bugtracker/oauth/sessions/"+s.ID+"/complete", map[string]string{"portal_id": "2063927"}, &c)
	return c
}
func (e *zohoEnv) draft(c integrations.Connection, key string) imports.Job {
	e.t.Helper()
	var j imports.Job
	e.owner().must(201, "POST", "imports/zoho_bugtracker/project", map[string]string{"connection_id": c.ID, "project_id": "301", "space_id": e.space.ID}, &j, "Idempotency-Key", key)
	return j
}
func (e *zohoEnv) worker() *imports.Worker {
	return &imports.Worker{Pool: e.database, Zoho: e.client, Secrets: e.box, Instance: "zoho-controlled-" + e.org}
}
func (e *zohoEnv) run(w *imports.Worker, j imports.Job) imports.Job {
	e.t.Helper()
	if err := w.RunOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
	e.owner().must(200, "GET", "imports/"+j.ID, nil, &j)
	return j
}
func TestZohoHTTPImportReadOnlyMappingAndIdentity(t *testing.T) {
	e := newZohoEnv(t)
	parent := e.connect()
	session := e.authorize(e.member(), integrations.ZohoStartInput{Purpose: "personal", ConnectionID: parent.ID})
	var binding integrations.Connection
	e.member().must(200, "POST", "integrations/zoho_bugtracker/oauth/sessions/"+session.ID+"/complete", map[string]string{}, &binding)
	e.fake.mu.Lock()
	e.fake.mode = "two"
	e.fake.mu.Unlock()
	var page struct {
		Projects  []any `json:"projects"`
		NextIndex int   `json:"next_index"`
	}
	e.owner().must(200, "GET", "imports/zoho_bugtracker/projects?connection_id="+parent.ID, nil, &page)
	if len(page.Projects) != 1 || page.NextIndex != 0 {
		t.Fatal("project list", page)
	}
	job := e.draft(parent, "zoho-full-1")
	w := e.worker()
	job = e.run(w, job)
	if job.Phase != "" || job.Preview.TotalRows != 2 || job.Analysis == nil {
		t.Fatalf("fetch incomplete: %+v", job)
	}
	if job.Analysis.Capabilities.ProjectSync || job.Analysis.Capabilities.KeepNumbers {
		t.Fatal("unsupported capability")
	}
	cfg := *job.Config
	cfg.Options.ContinuousSync = true
	e.owner().expectError(400, "", "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg})
	cfg.Options.ContinuousSync = false
	cfg.Statuses = []imports.StatusMapping{{Source: "11", StatusID: e.statusID(e.org, "TODO")}, {Source: "12", StatusID: e.statusID(e.org, "DONE")}}
	cfg.Types = []imports.TypeMapping{{Source: "Bug", UseDefault: true}}
	cfg.Priorities = []imports.PriorityMapping{{Source: "5", Priority: "HIGH"}}
	if len(job.Analysis.Users) != 1 || job.Analysis.Users[0].VerifiedBinding == nil {
		t.Fatalf("verified reporter missing: %+v", job.Analysis.Users)
	}
	cfg.Users = []imports.UserMapping{{SourceID: "us:2060759", Mode: "member", UserID: s3MemberID, VerifiedBindingID: binding.ID}}
	e.owner().must(200, "PUT", "imports/"+job.ID+"/config", map[string]any{"config_version": job.ConfigVersion, "config": cfg}, &job)
	e.owner().must(200, "POST", "imports/"+job.ID+"/start", map[string]any{"config_version": job.ConfigVersion}, &job)
	for i := 0; i < 10 && job.Status != "SUCCEEDED"; i++ {
		job = e.run(w, job)
	}
	if job.Status != "SUCCEEDED" {
		t.Fatalf("import failed: %+v", job)
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.organization_id=t.organization_id AND i.task_id=t.id WHERE i.job_id=$1::uuid AND t.priority='HIGH' AND t.description='' AND t.assignee_id IS NULL AND t.completed_at IS NULL`, job.ID)
	if count != 2 {
		t.Fatal("unverified fields imported", count)
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NULL AND credentials_key_id=''`, binding.ID)
	if count != 1 {
		t.Fatal("personal token persisted")
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.import_sync_links WHERE job_id=$1::uuid`, job.ID)
	if count != 0 {
		t.Fatal("unexpected sync")
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.task_activities a JOIN openfox.import_items i ON i.organization_id=a.organization_id AND i.task_id=a.task_id WHERE i.job_id=$1::uuid AND a.kind='task.imported' AND a.actor_id=$2::uuid AND a.safe_diff->>'source_reporter_id'='2060759' AND a.safe_diff->>'mapped_user_id'=$3`, job.ID, s3OwnerID, s3MemberID)
	if count != 2 {
		t.Fatal("source reporter mapping/actual actor missing", count)
	}
	e.member().must(200, "POST", "integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	for _, mode := range []string{"unbound", "manual"} {
		e.fake.mu.Lock()
		e.fake.mode = mode
		e.fake.mu.Unlock()
		fresh := e.draft(parent, "zoho-"+mode)
		fresh = e.run(w, fresh)
		if fresh.Analysis == nil || len(fresh.Analysis.Users) != 1 || fresh.Analysis.Users[0].VerifiedBinding != nil {
			t.Fatal("disconnected identity still suggested")
		}
		nextCfg := cfg
		nextCfg.Users = []imports.UserMapping{}
		if mode == "manual" {
			nextCfg.Users = []imports.UserMapping{{SourceID: "us:2060759", Mode: "member", UserID: s3MemberID}}
		}
		e.owner().must(200, "PUT", "imports/"+fresh.ID+"/config", map[string]any{"config_version": fresh.ConfigVersion, "config": nextCfg}, &fresh)
		e.owner().must(200, "POST", "imports/"+fresh.ID+"/start", map[string]any{"config_version": fresh.ConfigVersion}, &fresh)
		for i := 0; i < 10 && fresh.Status != "SUCCEEDED"; i++ {
			fresh = e.run(w, fresh)
		}
		if fresh.Status != "SUCCEEDED" {
			t.Fatalf("new import failed: %+v", fresh)
		}
		query := `SELECT count(*) FROM openfox.task_activities a JOIN openfox.import_items i ON i.organization_id=a.organization_id AND i.task_id=a.task_id WHERE i.job_id=$1::uuid AND a.kind='task.imported' AND a.safe_diff->>'source_reporter_id'='2060759' AND `
		if mode == "manual" {
			query += `a.safe_diff->>'mapped_user_id'='` + s3MemberID + `'`
		} else {
			query += `NOT(a.safe_diff ? 'mapped_user_id') AND a.safe_diff ? 'mapped_external_account_id'`
		}
		e.scalar(&count, query, fresh.ID)
		if count != 1 {
			t.Fatal("reporter mapping state", mode, count)
		}
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.task_activities a JOIN openfox.import_items i ON i.organization_id=a.organization_id AND i.task_id=a.task_id WHERE i.job_id=$1::uuid AND a.kind='task.imported' AND a.safe_diff->>'mapped_user_id'=$2`, job.ID, s3MemberID)
	if count != 2 {
		t.Fatal("historical mapping changed")
	}
	e.owner().must(200, "POST", "integrations/connections/"+parent.ID+"/disconnect", map[string]any{"version": parent.Version, "confirm_phrase": integrations.ConfirmPhrase("Zoho BugTracker")}, nil)
	e.scalar(&count, `SELECT count(*) FROM openfox.import_items WHERE job_id=$1::uuid AND task_id IS NOT NULL`, job.ID)
	if count != 2 {
		t.Fatal("disconnect lost copy")
	}
}
func TestZohoHTTPFetchAuthorizationAndRateFailure(t *testing.T) {
	for _, mode := range []string{"archived", "disconnect", "unconfigured", "429"} {
		t.Run(mode, func(t *testing.T) {
			e := newZohoEnv(t)
			parent := e.connect()
			job := e.draft(parent, "zoho-stop")
			w := e.worker()
			if mode == "unconfigured" {
				w.Zoho = nil
			}
			if mode == "archived" {
				e.exec(`UPDATE openfox.spaces SET status='ARCHIVED' WHERE id=$1::uuid`, e.space.ID)
			}
			if mode == "disconnect" {
				e.owner().must(200, "POST", "integrations/connections/"+parent.ID+"/disconnect", map[string]any{"version": parent.Version, "confirm_phrase": integrations.ConfirmPhrase("Zoho BugTracker")}, nil)
			}
			if mode == "429" {
				e.fake.mu.Lock()
				e.fake.mode = mode
				e.fake.mu.Unlock()
			}
			if err := w.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
			var status string
			var count int
			e.scalar(&status, `SELECT status FROM openfox.import_jobs WHERE id=$1::uuid`, job.ID)
			if status != "DISCARDED" {
				t.Fatal("job not terminal", status)
			}
			e.scalar(&count, `SELECT count(*) FROM openfox.import_items WHERE job_id=$1::uuid`, job.ID)
			if count != 0 {
				t.Fatal("partial draft retained")
			}
			e.fake.mu.Lock()
			calls := e.fake.bugGets
			e.fake.mu.Unlock()
			if mode != "429" && calls != 0 {
				t.Fatal("HTTP after lost permission")
			}
		})
	}
}

func TestZohoHTTPPaginationAndNoProgress(t *testing.T) {
	for _, mode := range []string{"pages", "repeat"} {
		t.Run(mode, func(t *testing.T) {
			e := newZohoEnv(t)
			parent := e.connect()
			e.fake.mu.Lock()
			e.fake.mode = mode
			e.fake.mu.Unlock()
			job := e.draft(parent, "zoho-page")
			w := e.worker()
			// Exercise the production control-plane organization scan as well as claim.
			if err := w.RunOnce(e.ctx); err != nil {
				t.Fatal(err)
			}
			e.owner().must(200, "GET", "imports/"+job.ID, nil, &job)
			if mode == "pages" {
				if job.Phase != "" || job.Preview.TotalRows != 101 {
					t.Fatalf("pagination not complete: %+v", job)
				}
			} else {
				if job.Status != "DISCARDED" {
					t.Fatal("repeat did not stop", job.Status)
				}
				var n int
				e.scalar(&n, `SELECT count(*) FROM openfox.import_items WHERE job_id=$1::uuid AND (raw<>'{}'::jsonb OR task_id IS NOT NULL)`, job.ID)
				if n != 0 {
					t.Fatal("partial repeated page retained")
				}
			}
		})
	}
}

func TestZohoHTTPImportPermissionErrors(t *testing.T) {
	e := newZohoEnv(t)
	parent := e.connect()
	e.member().expectError(403, "FORBIDDEN", "GET", "imports/zoho_bugtracker/projects?connection_id="+parent.ID, nil)
	e.owner().expectError(404, "SPACE_NOT_FOUND", "POST", "imports/zoho_bugtracker/project", map[string]string{"connection_id": parent.ID, "project_id": "301", "space_id": e.otherSpace.ID}, "Idempotency-Key", "zoho-foreign-space")
	e.fake.mu.Lock()
	gets := e.fake.bugGets
	e.fake.mu.Unlock()
	if gets != 0 {
		t.Fatal("unauthorized fetch dispatched")
	}
}
