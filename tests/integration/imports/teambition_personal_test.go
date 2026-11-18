package imports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

type p02Transport func(*http.Request) (*http.Response, error)

func (f p02Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type p02Env struct {
	*apiEnv
	upstream  *httptest.Server
	transport http.RoundTripper
	parent    integrations.Connection
	exchanges atomic.Int32
	count     atomic.Int32
	before    func(*http.Request)
}

func newP02Env(t *testing.T, lifetime ...time.Duration) *p02Env {
	e, hasher := newSiteEnv(t, lifetime...)
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid ON CONFLICT DO NOTHING`, e.org)
	p := &p02Env{apiEnv: e}
	p.count.Store(1)
	p.upstream = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.upstream.Close)
	up, _ := url.Parse(p.upstream.URL)
	p.transport = p02Transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "open.teambition.com" || r.URL.Scheme != "https" {
			return nil, fmt.Errorf("unexpected upstream origin")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Host = up.Host
		return p.upstream.Client().Transport.RoundTrip(clone)
	})
	handler := api.NewWithDispatch(identity.Service{Store: identity.PostgresStore{Pool: e.pool}, PasswordHasher: hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}, e.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: e.box, PublicURL: "http://127.0.0.1:4194", TeambitionPersonalClient: &integrations.TeambitionPersonalClient{Transport: p.transport}, ProjectTransport: p.transport})
	e.server = httptest.NewServer(handler)
	t.Cleanup(e.server.Close)
	e.owner = httpLogin(t, e.server.URL, "site-owner@example.test")
	e.member = httpLogin(t, e.server.URL, "site-member@example.test")
	e.guest = httpLogin(t, e.server.URL, "site-guest@example.test")
	for _, c := range []*httpClient{e.owner, e.member, e.guest} {
		c.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	e.worker = &imports.Worker{Pool: e.pool, Secrets: e.box, ProjectTransport: p.transport, Instance: "p02"}
	p.parent = p.addParent("p02-company")
	return p
}
func (p *p02Env) actor(user string) collaboration.Actor {
	role := policy.OrganizationRoleMember
	if user == siteOwner {
		role = policy.OrganizationRoleOwner
	}
	return collaboration.Actor{UserID: user, Role: role, AuthorizationVersion: uint64(p.num(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, p.org, user))}
}
func (p *p02Env) run(user string, fn func(context.Context, pgx.Tx) error) error {
	actor := p.actor(user)
	selection := tenancy.OrganizationSelection{OrganizationID: p.org, ActorID: user, AuthorizationVersion: actor.AuthorizationVersion}
	return p.pool.RunForOrganization(p.ctx, p.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
}
func (p *p02Env) addParent(company string) integrations.Connection {
	var c integrations.Connection
	actor := p.actor(siteOwner)
	err := p.run(siteOwner, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = (integrations.Service{Box: p.box}).SaveProviderToken(ctx, tx, p.org, actor, "teambition", integrations.ProviderTokenInput{Method: "app", Account: "p02-app", Secret: "test-only-app-secret", CompanyID: company}, company, company)
		return err
	})
	if err != nil {
		p.t.Fatal(err)
	}
	return c
}
func (p *p02Env) serve(w http.ResponseWriter, r *http.Request) {
	if p.before != nil {
		p.before(r)
	}
	if r.URL.Path == "/api/oauth/sso" {
		q := r.URL.Query()
		callback, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		v := callback.Query()
		v.Set("state", q.Get("state"))
		v.Set("code", "member")
		callback.RawQuery = v.Encode()
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Controlled Teambition sign in</title><a href="%s">Authorize local test identity</a>`, callback.String())
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
		w.WriteHeader(401)
		return
	}
	var in map[string]any
	if r.Method == "POST" {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	result := any(nil)
	switch r.URL.Path {
	case "/api/oauth/userAccessToken":
		p.exchanges.Add(1)
		code, _ := in["code"].(string)
		result = map[string]any{"accessToken": "ephemeral-" + code, "refreshToken": "never-store-refresh", "userId": "remote-" + code, "tokenType": "Bearer", "expires": 600}
	case "/api/oauth/userInfo":
		token, _ := in["userAccessToken"].(string)
		code := strings.TrimPrefix(token, "ephemeral-")
		result = map[string]any{"userId": "remote-" + code, "name": "Teambition " + code}
	case "/api/org/member/batchGet":
		if r.Header.Get("X-Tenant-Type") != "organization" || r.Header.Get("X-Tenant-Id") == "" || len(in) != 1 {
			w.WriteHeader(400)
			return
		}
		ids, _ := in["userIds"].([]any)
		if len(ids) != 1 {
			w.WriteHeader(400)
			return
		}
		id, _ := ids[0].(string)
		code := strings.TrimPrefix(id, "remote-")
		role, disabled := 0, 0
		if code == "external" {
			role = -1
		}
		if code == "disabled" {
			disabled = 1
		}
		email := "site-member@example.test"
		if code == "owner" {
			email = "site-owner@example.test"
		}
		if code == "mismatch" {
			email = "wrong@example.test"
		}
		result = []any{map[string]any{"userId": id, "role": role, "isDisabled": disabled, "email": email}}
	case "/api/v3/project/query":
		result = []any{map[string]any{"id": "project", "name": "P02 Project", "organizationId": r.Header.Get("X-Tenant-Id")}}
	case "/api/v3/project/project/taskflowstatus/search":
		result = []any{map[string]any{"id": "todo", "name": "To Do"}}
	case "/api/v3/project/project/task/query":
		items := []any{}
		for i := 1; i <= int(p.count.Load()); i++ {
			items = append(items, map[string]any{"id": fmt.Sprintf("item-%d", i), "content": fmt.Sprintf("P02 imported %d", i), "projectId": "project", "tfsId": "todo", "executorId": "remote-member"})
		}
		result = items
	default:
		w.WriteHeader(404)
		return
	}
	writeJSONResponse(w, 200, map[string]any{"result": result, "errorMessage": ""})
}
func (p *p02Env) endpoint() string { return p.prefix + "/me/account-bindings/teambition/sessions" }
func (p *p02Env) start(c *httpClient, parent string, version uint64) integrations.StartResult {
	var s integrations.StartResult
	c.json(200, "POST", p.endpoint(), map[string]any{"connection_id": parent, "version": version}, &s)
	return s
}
func (p *p02Env) callback(c *httpClient, s integrations.StartResult, code string) string {
	u, _ := url.Parse(s.AuthorizeURL)
	req, _ := http.NewRequest("GET", c.base+"/v1/integrations/teambition/callback?"+url.Values{"state": {u.Query().Get("state")}, "code": {code}}.Encode(), nil)
	resp, err := c.client.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		p.t.Fatalf("callback status=%d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if strings.Contains(location, "code=") || strings.Contains(location, "state=") {
		p.t.Fatal("sensitive redirect")
	}
	return location
}
func (p *p02Env) complete(c *httpClient, s integrations.StartResult) integrations.Connection {
	var b integrations.Connection
	c.json(200, "POST", p.endpoint()+"/"+s.SessionID+"/complete", nil, &b)
	return b
}
func (p *p02Env) bindMember() integrations.Connection {
	s := p.start(p.member, p.parent.ID, 0)
	if loc := p.callback(p.member, s, "member"); strings.Contains(loc, "status=error") {
		p.t.Fatal(loc)
	}
	return p.complete(p.member, s)
}

func TestTeambitionPersonalHTTP(t *testing.T) {
	p := newP02Env(t)
	p.guest.expect(403, "FORBIDDEN", "POST", p.endpoint(), map[string]any{"connection_id": p.parent.ID})
	p.member.expect(404, "NOT_FOUND", "POST", p.endpoint(), map[string]any{"connection_id": "00000000-0000-0000-0000-000000000001"})
	s := p.start(p.member, p.parent.ID, 0)
	u, _ := url.Parse(s.AuthorizeURL)
	if u.Host != "open.teambition.com" || u.Path != "/api/oauth/sso" || len(u.Query().Get("state")) != 43 || u.Query().Get("redirect_uri") != "http://127.0.0.1:4194/api/v1/integrations/teambition/callback" {
		t.Fatalf("authorize URL shape=%v", u)
	}
	if p.str(`SELECT encode(state_hash,'hex') FROM openfox.integration_oauth_states WHERE id=$1::uuid`, s.SessionID) == u.Query().Get("state") {
		t.Fatal("raw state stored")
	}
	p.owner.expect(404, "NOT_FOUND", "GET", p.endpoint()+"/"+s.SessionID, nil)
	p.owner.expect(404, "NOT_FOUND", "POST", p.endpoint()+"/"+s.SessionID+"/complete", nil)
	p.callback(p.owner, s, "member")
	if p.exchanges.Load() != 0 {
		t.Fatal("other actor exchanged code")
	}
	if loc := p.callback(p.member, s, "member"); strings.Contains(loc, "status=error") {
		t.Fatal(loc)
	}
	p.callback(p.member, s, "member")
	if p.exchanges.Load() != 1 {
		t.Fatal("state replay exchanged twice")
	}
	var session integrations.TeambitionSession
	p.member.json(200, "GET", p.endpoint()+"/"+s.SessionID, nil, &session)
	if session.Status != "SELECTING" || session.Identity == nil || session.Identity.ID != "remote-member" {
		t.Fatalf("session=%+v", session)
	}
	if p.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND scope='PERSONAL'`, p.org) != 0 {
		t.Fatal("callback implicitly bound identity")
	}
	const n = 6
	var wg sync.WaitGroup
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body := p.member.do("POST", p.endpoint()+"/"+s.SessionID+"/complete", nil)
			if status != 200 {
				results <- fmt.Sprintf("status %d %s", status, body)
				return
			}
			var b integrations.Connection
			_ = json.Unmarshal([]byte(body), &b)
			results <- b.ID
		}()
	}
	wg.Wait()
	close(results)
	id := ""
	for got := range results {
		if id == "" {
			id = got
		}
		if id != got || strings.Contains(got, "status ") {
			t.Fatalf("completion not idempotent=%s / %s", id, got)
		}
	}
	binding := p.complete(p.member, s)
	if binding.ID != id || binding.OwnerUserID == nil || *binding.OwnerUserID != siteMember {
		t.Fatalf("binding=%+v", binding)
	}
	if p.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND scope='PERSONAL' AND credentials_sealed IS NULL AND credentials_key_id=''`, p.org) != 1 {
		t.Fatal("personal credential persisted")
	}
	for _, table := range []string{"integration_connections", "integration_oauth_states", "audit_events"} {
		if p.num(`SELECT count(*) FROM openfox.`+table+` WHERE organization_id=$1::uuid AND row_to_json(`+table+`)::text ~ '(ephemeral-|never-store-refresh|one-time-code)'`, p.org) != 0 {
			t.Fatal("token persisted in " + table)
		}
	}
	p.owner.expect(404, "NOT_FOUND", "POST", p.prefix+"/integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version})
	p.member.json(200, "POST", p.prefix+"/integrations/connections/"+binding.ID+"/disconnect", map[string]any{"version": binding.Version}, nil)
	if p.str(`SELECT status FROM openfox.integration_connections WHERE id=$1::uuid`, binding.ID) != "DISCONNECTED" {
		t.Fatal("disconnect failed")
	}
	p.complete(p.member, s) // Replay returns the same disconnected row, never reactivates it.
	if p.str(`SELECT status FROM openfox.integration_connections WHERE id=$1::uuid`, binding.ID) != "DISCONNECTED" {
		t.Fatal("completed session reactivated identity")
	}
}

func TestTeambitionPersonalRejectedAndChanged(t *testing.T) {
	p := newP02Env(t)
	for _, code := range []string{"mismatch", "disabled", "external"} {
		s := p.start(p.member, p.parent.ID, 0)
		p.callback(p.member, s, code)
		var session integrations.TeambitionSession
		p.member.json(200, "GET", p.endpoint()+"/"+s.SessionID, nil, &session)
		if session.Status != "FAILED" || session.ErrorCode != "account_mismatch" || session.Identity != nil {
			t.Fatalf("invalid identity=%+v", session)
		}
	}
	s := p.start(p.member, p.parent.ID, 0)
	p.exec(`UPDATE openfox.integration_oauth_states SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1::uuid`, s.SessionID)
	before := p.exchanges.Load()
	p.callback(p.member, s, "member")
	if p.exchanges.Load() != before {
		t.Fatal("expired exchange")
	}
	s = p.start(p.member, p.parent.ID, 0)
	p.callback(p.member, s, "member")
	p.exec(`UPDATE openfox.users SET email='changed-member@example.test' WHERE id=$1::uuid`, siteMember)
	p.member.expect(422, "PERSONAL_IDENTITY_MISMATCH", "POST", p.endpoint()+"/"+s.SessionID+"/complete", nil)
	p.exec(`UPDATE openfox.users SET email='site-member@example.test' WHERE id=$1::uuid`, siteMember)
	s = p.start(p.member, p.parent.ID, 0)
	p.callback(p.member, s, "member")
	p.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE id=$1::uuid`, p.parent.ID)
	p.member.expect(409, "VERSION_CONFLICT", "POST", p.endpoint()+"/"+s.SessionID+"/complete", nil)
	s = p.start(p.member, p.parent.ID, 0)
	p.callback(p.member, s, "member")
	p.exec(`UPDATE openfox.organization_members SET role='GUEST',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, p.org, siteMember)
	status, _ := p.member.do("POST", p.endpoint()+"/"+s.SessionID+"/complete", nil)
	if status != 403 {
		t.Fatalf("revoked role status=%d", status)
	}
}

func TestTeambitionPersonalCallbackNetworkOutsideTransaction(t *testing.T) {
	p := newP02Env(t)
	s := p.start(p.member, p.parent.ID, 0)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	p.before = func(r *http.Request) {
		if r.URL.Path == "/api/oauth/userAccessToken" {
			close(entered)
			<-release
		}
	}
	done := make(chan string, 1)
	go func() { done <- p.callback(p.member, s, "member") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exchange not reached")
	}
	// This succeeds while the external exchange is held, proving no parent lock spans network I/O.
	p.owner.json(200, "POST", p.prefix+"/integrations/connections/"+p.parent.ID+"/disconnect", map[string]any{"version": p.parent.Version, "confirm_phrase": "断开 Teambition"}, nil)
	once.Do(func() { close(release) })
	select {
	case loc := <-done:
		if !strings.Contains(loc, "reason=connection_changed") {
			t.Fatal(loc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not finish")
	}
	if p.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND scope='PERSONAL'`, p.org) != 0 {
		t.Fatal("revoked parent produced binding")
	}
}

func TestTeambitionConcurrentCallbacksAndFinalLocks(t *testing.T) {
	p := newP02Env(t)
	s := p.start(p.member, p.parent.ID, 0)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.callback(p.member, s, "member") }()
	}
	wg.Wait()
	if p.exchanges.Load() != 1 {
		t.Fatalf("concurrent state consumed %d times", p.exchanges.Load())
	}
	for _, mutation := range []string{"email", "membership"} {
		t.Run(mutation, func(t *testing.T) {
			if mutation == "membership" {
				s = p.start(p.member, p.parent.ID, 1)
				p.callback(p.member, s, "member")
			}
			ready, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			done := make(chan error, 1)
			actor := p.actor(siteMember)
			go func() {
				done <- p.run(siteMember, func(ctx context.Context, tx pgx.Tx) error {
					_, _, err := (integrations.Service{Box: p.box}).CompleteTeambitionPersonal(ctx, tx, p.org, actor, s.SessionID)
					if err != nil {
						return err
					}
					close(ready)
					<-release
					return nil
				})
			}()
			select {
			case <-ready:
			case err := <-done:
				t.Fatalf("completion=%v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("completion barrier")
			}
			c, err := pgx.Connect(p.ctx, p.url)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close(context.Background())
			changed := make(chan error, 1)
			go func() {
				query := `UPDATE openfox.users SET email=email WHERE id=$1::uuid`
				args := []any{siteMember}
				if mutation == "membership" {
					query = `UPDATE openfox.organization_members SET role='GUEST',authorization_version=authorization_version+1 WHERE user_id=$1::uuid AND organization_id=$2::uuid`
					args = append(args, p.org)
				}
				_, err := c.Exec(p.ctx, query, args...)
				changed <- err
			}()
			select {
			case err := <-changed:
				t.Fatalf("%s crossed confirmation lock: %v", mutation, err)
			case <-time.After(150 * time.Millisecond):
			}
			once.Do(func() { close(release) })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTeambitionIdentityConstraintAndTenant(t *testing.T) {
	p := newP02Env(t)
	binding := p.bindMember()
	var other string
	if err := p.super.QueryRow(p.ctx, `SELECT gen_random_uuid()::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	p.exec(`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'P02 other')`, other, "p02-other-"+other[:8])
	p.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER'),($1::uuid,$3::uuid,'MEMBER')`, other, siteOwner, siteMember)
	p.member.expect(404, "NOT_FOUND", "POST", "/v1/organizations/"+other+"/me/account-bindings/teambition/sessions", map[string]any{"connection_id": p.parent.ID})
	// Direct fixture insertion tests the independent database guarantee, including
	// concurrent callers that passed application validation before another write.
	_, err := p.super.Exec(p.ctx, `INSERT INTO openfox.integration_connections(organization_id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) SELECT organization_id,provider,scope,parent_connection_id,$2::uuid,external_id,display_name,account_kind,settings,$2::uuid FROM openfox.integration_connections WHERE id=$1::uuid`, binding.ID, siteOwner)
	if err == nil || !strings.Contains(err.Error(), "integration_connections_teambition_user_unique") {
		t.Fatalf("enterprise identity uniqueness=%v", err)
	}
	_, err = p.super.Exec(p.ctx, `UPDATE openfox.integration_connections SET credentials_key_id='forbidden' WHERE id=$1::uuid`, binding.ID)
	if err == nil || !strings.Contains(err.Error(), "integration_connections_teambition_identity_valid") {
		t.Fatalf("personal credentials constraint=%v", err)
	}
}
