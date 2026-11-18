package stage3

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type tapdFixtureItem struct{ ID, Nick string }

type tapdPersonalFake struct {
	mu              sync.Mutex
	requests        int
	beforeDirectory func()
	items           []tapdFixtureItem
	nick, email     string
}

func (f *tapdPersonalFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	hook := f.beforeDirectory
	items := append([]tapdFixtureItem(nil), f.items...)
	currentNick, currentEmail := f.nick, f.email
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"status": 1, "data": data}) }
	if r.Method != "GET" || r.Host != "api.tapd.cn" {
		w.WriteHeader(500)
		return
	}
	auth := r.Header.Get("Authorization")
	if auth == "Bearer invalid" {
		w.WriteHeader(401)
		return
	}
	project := r.URL.Query().Get("workspace_id")
	switch r.URL.Path {
	case "/workspaces/projects":
		write([]any{map[string]any{"Workspace": map[string]string{"id": "101", "name": "Fixture TAPD project"}}, map[string]any{"Workspace": map[string]string{"id": "102", "name": "Other project"}}})
	case "/stories", "/bugs":
		write([]any{})
	case "/tasks":
		out := []any{}
		if r.URL.Query().Get("page") == "1" {
			for _, item := range items {
				out = append(out, map[string]any{"Task": map[string]string{"id": item.ID, "name": "Fixture task " + item.ID, "status": "Todo", "owner": item.Nick, "workspace_id": project}})
			}
		}
		write(out)
	case "/users/info":
		id, nick := "42", "member.nick"
		if auth == "Bearer other" {
			id, nick = "43", "other.nick"
		}
		if auth == "Bearer same-other" {
			id, nick = "42", "member.nick"
		}
		if currentNick != "" {
			nick = currentNick
		}
		write(map[string]any{"id": id, "nick": nick, "name": "Fixture Member", "enabled": "1", "status_id": "1"})
	case "/workspaces/get_workspace_info":
		write(map[string]any{"Workspace": map[string]any{"id": project, "company_id": 123333, "status": "normal"}})
	case "/workspaces/users":
		if hook != nil {
			hook()
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != "fixture-api" || p != "fixture-parent-secret" || r.URL.Query().Get("fields") != "user,email" {
			w.WriteHeader(403)
			return
		}
		nick := r.URL.Query().Get("user")
		email := s3Email(s3MemberID)
		if nick == "other.nick" || project == "90002" {
			email = s3Email(s3OtherID)
		}
		if currentEmail != "" {
			email = currentEmail
		}
		write([]any{map[string]any{"UserWorkspace": map[string]any{"user": nick, "email": email}}})
	default:
		w.WriteHeader(404)
	}
}

type tapdPersonalEnv struct {
	*s3Env
	fake      *tapdPersonalFake
	svc       integrations.Service
	parent    integrations.Connection
	transport *http.Transport
	box       *secrets.Box
}

func newTAPDPersonalEnv(t *testing.T) *tapdPersonalEnv {
	e := newS3Env(t)
	f := &tapdPersonalFake{}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, TAPDPersonalTransport: transport, ProjectTransport: transport}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	out := &tapdPersonalEnv{s3Env: e, fake: f, svc: integrations.Service{Box: box, TAPDPersonalTransport: transport, ProjectTransport: transport}, transport: transport, box: box}
	if err := out.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		var err error
		out.parent, err = out.svc.SaveProviderToken(ctx, tx, e.org, actor, "tapd", integrations.ProviderTokenInput{Method: "basic", Account: "fixture-api", Secret: "fixture-parent-secret", CompanyID: "123333"}, "123333", "Fixture TAPD Basic")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func (e *tapdPersonalEnv) tx(user string, fn func(context.Context, pgx.Tx, collaboration.Actor) error) error {
	var version uint64
	e.scalar(&version, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, user)
	sel := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: user, AuthorizationVersion: version}
	actor := collaboration.Actor{UserID: user, Role: policy.OrganizationRole(strings.ToLower(s3Roles[user])), AuthorizationVersion: version}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }), func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, actor) })
}
func (e *tapdPersonalEnv) input(project, token string, version uint64) map[string]any {
	return map[string]any{"connection_id": e.parent.ID, "project_id": project, "access_token": token, "version": version}
}
func (e *tapdPersonalEnv) count() int {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	return e.fake.requests
}
func (e *tapdPersonalEnv) view(c *s3Client) imports.AccountBindingProvider {
	var view imports.AccountBindingsView
	c.must(200, "GET", "me/account-bindings", nil, &view)
	for _, p := range view.Providers {
		if p.ID == "tapd" {
			return p
		}
	}
	e.t.Fatal("missing TAPD row")
	return imports.AccountBindingProvider{}
}

func TestTAPDPersonalHTTPIdentityScopeAndCAS(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	member := e.member()
	var binding integrations.Connection
	member.must(200, "PUT", "me/account-bindings/tapd", e.input("101", "member", 0), &binding)
	if e.count() != 4 || binding.AccountKind != "identity" || binding.OwnerUserID == nil || *binding.OwnerUserID != s3MemberID {
		t.Fatalf("bad binding %+v, calls=%d", binding, e.count())
	}
	row := e.view(member)
	if row.Binding == nil || row.Binding.TAPDIdentity == nil || row.Binding.TAPDIdentity.Nick != "member.nick" || len(row.Binding.TAPDIdentity.Projects) != 1 {
		t.Fatalf("bad view %+v", row)
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint='' AND settings->>'verified_email'=$2 AND NOT settings::text LIKE '%fixture-parent-secret%'`, binding.ID, s3Email(s3MemberID))
	if count != 1 {
		t.Fatal("identity stored a secret or wrong email")
	}
	var duplicate integrations.Connection
	member.must(200, "PUT", "me/account-bindings/tapd", e.input("101", "member", 0), &duplicate)
	if duplicate.ID != binding.ID || duplicate.Version != binding.Version {
		t.Fatal("repeat changed identity")
	}
	member.expectError(409, "VERSION_CONFLICT", "PUT", "me/account-bindings/tapd", e.input("102", "member", 0))
	member.must(200, "PUT", "me/account-bindings/tapd", e.input("102", "member", binding.Version), &binding)
	other := e.other()
	other.expectError(404, "", "DELETE", "me/account-bindings/tapd/projects/101", map[string]any{"binding_id": binding.ID, "version": binding.Version})
	member.expectError(409, "VERSION_CONFLICT", "DELETE", "me/account-bindings/tapd/projects/101", map[string]any{"binding_id": binding.ID, "version": binding.Version - 1})
	member.must(200, "DELETE", "me/account-bindings/tapd/projects/101", map[string]any{"binding_id": binding.ID, "version": binding.Version}, &binding)
	member.must(200, "DELETE", "me/account-bindings/tapd/projects/102", map[string]any{"binding_id": binding.ID, "version": binding.Version}, &binding)
	if binding.Status != "DISCONNECTED" || e.view(member).Binding != nil {
		t.Fatal("last scope did not disconnect")
	}
}

func TestTAPDPersonalAuthorizationAndProjectLimit(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	e.guest().expectError(403, "", "PUT", "me/account-bindings/tapd", e.input("101", "member", 0))
	e.outsider().expectError(404, "", "PUT", "me/account-bindings/tapd", e.input("101", "member", 0))
	if e.count() != 0 {
		t.Fatal("unauthorized actor reached provider")
	}
	e.member().expectError(422, "TAPD_EMAIL_MISMATCH", "PUT", "me/account-bindings/tapd", e.input("101", "other", 0))
	var tokenParent integrations.Connection
	if err := e.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		var err error
		tokenParent, err = e.svc.SaveProviderToken(ctx, tx, e.org, actor, "tapd", integrations.ProviderTokenInput{Method: "token", Secret: "fixture-other-parent-token"}, "999", "Token parent")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := e.count()
	input := e.input("101", "member", 0)
	input["connection_id"] = tokenParent.ID
	e.member().expectError(409, "TAPD_BASIC_REQUIRED", "PUT", "me/account-bindings/tapd", input)
	if e.count() != before {
		t.Fatal("unsupported parent reached provider")
	}
	var binding integrations.Connection
	for i := 0; i < 20; i++ {
		e.member().must(200, "PUT", "me/account-bindings/tapd", e.input(fmt.Sprint(100+i), "member", binding.Version), &binding)
	}
	e.member().expectError(409, "TAPD_PROJECT_LIMIT", "PUT", "me/account-bindings/tapd", e.input("120", "member", binding.Version))
	if len(e.view(e.member()).Binding.TAPDIdentity.Projects) != 20 {
		t.Fatal("scope limit corrupted")
	}
}

func TestTAPDPersonalConcurrentBindAndDisconnect(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	member := e.member()
	responses := make(chan s3Response, 2)
	for i := 0; i < 2; i++ {
		go func() { responses <- member.do("PUT", "me/account-bindings/tapd", e.input("101", "member", 0)) }()
	}
	var ids []string
	for i := 0; i < 2; i++ {
		r := <-responses
		if r.Status != 200 {
			t.Fatalf("concurrent bind %d %s", r.Status, r.Body)
		}
		var c integrations.Connection
		r.decode(t, &c)
		ids = append(ids, c.ID)
	}
	if ids[0] != ids[1] {
		t.Fatal("duplicate identity")
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='tapd' AND scope='PERSONAL' AND status='ACTIVE'`, e.org)
	if count != 1 {
		t.Fatal("duplicate row")
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.fake.mu.Lock()
	e.fake.beforeDirectory = func() { once.Do(func() { close(started) }); <-release }
	e.fake.mu.Unlock()
	go func() { responses <- member.do("PUT", "me/account-bindings/tapd", e.input("102", "member", 1)) }()
	<-started
	e.owner().must(200, "POST", "integrations/connections/"+e.parent.ID+"/disconnect", map[string]any{"version": e.parent.Version, "confirm_phrase": integrations.ConfirmPhrase("TAPD")}, nil)
	close(release)
	r := <-responses
	if r.Status != 404 && r.Status != 409 {
		t.Fatalf("stale grant committed %d %s", r.Status, r.Body)
	}
	e.scalar(&count, `SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='tapd' AND scope='PERSONAL' AND status='ACTIVE'`, e.org)
	if count != 0 {
		t.Fatal("parent disconnect retained active identity")
	}
}

func TestTAPDPersonalUniqueIdentityAcrossMembers(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	member, other := e.member(), e.other()
	responses := make(chan s3Response, 2)
	go func() { responses <- member.do("PUT", "me/account-bindings/tapd", e.input("90001", "member", 0)) }()
	go func() { responses <- other.do("PUT", "me/account-bindings/tapd", e.input("90002", "same-other", 0)) }()
	ok, conflict := 0, 0
	for i := 0; i < 2; i++ {
		r := <-responses
		switch {
		case r.Status == 200:
			ok++
		case r.Status == 409 && r.code() == "TAPD_ACCOUNT_IN_USE":
			conflict++
		default:
			t.Fatalf("unexpected race %d %s", r.Status, r.Body)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
	var raw string
	e.scalar(&raw, `SELECT coalesce(string_agg(settings::text,' '),'') FROM openfox.integration_connections WHERE organization_id=$1::uuid AND scope='PERSONAL'`, e.org)
	if strings.Contains(raw, "access_token") || strings.Contains(raw, "fixture-parent-secret") {
		t.Fatal("stored secret")
	}
}
