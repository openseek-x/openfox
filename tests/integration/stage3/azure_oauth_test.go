package stage3

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const azureTestIdentityApp = "55555555-5555-4555-8555-555555555555"
const azureTestPersonal = "66666666-6666-4666-8666-666666666666"
const azureTestAccountB = "77777777-7777-4777-8777-777777777777"
const azureTestProject = "88888888-8888-4888-8888-888888888888"

type azureOAuthFake struct {
	mu          sync.Mutex
	profile     string
	scope       string
	projectHook func()
	extra       func(http.ResponseWriter, *http.Request) bool
}

func (f *azureOAuthFake) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	f.mu.Lock()
	personal, scope, hook, extra := f.profile, f.scope, f.projectHook, f.extra
	f.mu.Unlock()
	if personal == "" {
		personal = azureTestPersonal
	}
	if extra != nil && extra(w, r) {
		return
	}
	switch r.Host {
	case "login.microsoftonline.com":
		_ = r.ParseForm()
		token, sc, refresh := strings.Repeat("o", 32768), azureTestScope, strings.Repeat("r", 32768)
		if r.Form.Get("client_id") == azureTestIdentityApp {
			token, sc, refresh = "identity-token", "vso.profile", ""
		}
		if scope != "" {
			sc = scope
		}
		reply(map[string]any{"access_token": token, "refresh_token": refresh, "expires_in": 3600, "token_type": "Bearer", "scope": sc})
	case "app.vssps.visualstudio.com":
		if r.URL.Path == "/_apis/profile/profiles/me" {
			id := azureTestSubject
			if r.Header.Get("Authorization") == "Bearer identity-token" {
				id = personal
			}
			reply(azuredevops.Profile{ID: id, DisplayName: strings.Repeat("名", 210)})
			return
		}
		if r.URL.Path == "/_apis/accounts" {
			reply(map[string]any{"value": []azuredevops.Account{{ID: azureTestAccount, Name: "fixture-org"}, {ID: azureTestAccountB, Name: "fixture-second"}}})
			return
		}
		w.WriteHeader(404)
	case "vssps.dev.azure.com":
		id := r.URL.Query().Get("identityIds")
		reply(map[string]any{"value": []azuredevops.Identity{{ID: id, Descriptor: "fixture;" + id, DisplayName: "Fixture user", IsActive: true}}})
	case "dev.azure.com":
		if hook != nil {
			hook()
		}
		reply(map[string]any{"value": []azuredevops.Project{{ID: azureTestProject, Name: "Fixture Project", Visibility: "private"}}})
	default:
		w.WriteHeader(404)
	}
}

type azureOAuthEnv struct {
	*azureGrantEnv
	fake            *azureOAuthFake
	upstreamAddress string
}

func newAzureOAuthEnv(t *testing.T) *azureOAuthEnv {
	e := newAzureGrantEnv(t)
	f := &azureOAuthFake{}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	e.svc.Azure.Transport = tr
	e.svc.AzureIdentity = &azuredevops.Client{App: azuredevops.App{ClientID: azureTestIdentityApp, ClientSecret: "fixture-secret"}, Transport: tr}
	e.runtime.Service = e.svc
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: e.svc.Box, AzureClient: e.svc.Azure, AzureIdentityClient: e.svc.AzureIdentity, PublicURL: e.svc.PublicURL}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	return &azureOAuthEnv{azureGrantEnv: e, fake: f, upstreamAddress: srv.Listener.Addr().String()}
}
func (e *azureOAuthEnv) authorize(c *s3Client, in integrations.AzureStartInput) integrations.AzureSession {
	e.t.Helper()
	var start integrations.StartResult
	c.must(200, "POST", "integrations/azure_devops/oauth/start", in, &start)
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		e.t.Fatal(err)
	}
	if u.Host != "login.microsoftonline.com" || u.Query().Get("code_challenge_method") != "S256" {
		e.t.Fatal("invalid authorize URL")
	}
	callback := "/v1/integrations/azure_devops/callback"
	if in.Purpose == "personal" {
		callback = "/v1/integrations/azure_devops/identity/callback"
	}
	old := c.http.CheckRedirect
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response := c.must(302, "GET", callback+"?state="+u.Query().Get("state")+"&code=fixture-code", nil, nil)
	c.http.CheckRedirect = old
	location, _ := url.Parse(response.Header.Get("Location"))
	if location.Query().Get("status") != "ready" {
		e.t.Fatalf("callback %s", location)
	}
	var session integrations.AzureSession
	c.must(200, "GET", "integrations/azure_devops/oauth/sessions/"+start.SessionID, nil, &session)
	if session.Status != "SELECTING" {
		e.t.Fatalf("session %+v", session)
	}
	return session
}
func (e *azureOAuthEnv) connect(account string) integrations.Connection {
	s := e.authorize(e.owner(), integrations.AzureStartInput{Purpose: "organization", TenantID: azureTestTenant})
	var c integrations.Connection
	e.owner().must(200, "POST", "integrations/azure_devops/oauth/sessions/"+s.ID+"/complete", integrations.AzureCompleteInput{AccountID: account}, &c)
	return c
}
func (e *azureOAuthEnv) bind(client *s3Client, parent string) integrations.Connection {
	s := e.authorize(client, integrations.AzureStartInput{Purpose: "personal", ConnectionID: parent})
	var c integrations.Connection
	client.must(200, "POST", "integrations/azure_devops/oauth/sessions/"+s.ID+"/complete", integrations.AzureCompleteInput{}, &c)
	return c
}

func (e *azureOAuthEnv) tx(user string, fn func(context.Context, pgx.Tx, collaboration.Actor) error) error {
	selected := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: user, AuthorizationVersion: 1}
	actor := collaboration.Actor{UserID: user, Role: policy.OrganizationRole(strings.ToLower(s3Roles[user])), AuthorizationVersion: 1}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selected, nil }), func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, actor) })
}

func TestAzureOAuthHTTPGrantSharingPersonalClaimAndUnlink(t *testing.T) {
	e := newAzureOAuthEnv(t)
	a := e.connect(azureTestAccount)
	b := e.connect(azureTestAccountB)
	if e.count(`SELECT count(*) FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("same app tenant subject did not share grant")
	}
	var size int
	e.scalar(&size, `SELECT octet_length(credentials_sealed) FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid`, e.org)
	if size < 65536 {
		t.Fatal("large tokens did not reach PG")
	}
	first := e.bind(e.member(), a.ID)
	second := e.bind(e.member(), b.ID)
	var summary imports.AccountBindingsView
	e.member().must(200, "GET", "me/account-bindings", nil, &summary)
	found := false
	for _, row := range summary.Providers {
		if row.ID == "azure_devops" {
			found = true
			if row.Binding == nil || len(row.Instances) != 2 {
				t.Fatal("Azure summary lost active personal binding")
			}
		}
	}
	if !found {
		t.Fatal("Azure summary missing")
	}

	if first.ID == second.ID {
		t.Fatal("account claims collapsed")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND scope='PERSONAL' AND provider='azure_devops' AND (credentials_sealed IS NOT NULL OR settings ? 'verified_email')`, e.org) != 0 {
		t.Fatal("personal token/email authority persisted")
	}
	candidate := e.authorize(e.other(), integrations.AzureStartInput{Purpose: "personal", ConnectionID: a.ID})
	e.other().must(409, "POST", "integrations/azure_devops/oauth/sessions/"+candidate.ID+"/complete", integrations.AzureCompleteInput{}, nil)
	// Unlinking frees only this account's claim. Another account remains bound.
	e.member().must(200, "POST", "integrations/connections/"+first.ID+"/disconnect", map[string]any{"version": first.Version}, nil)
	var moved integrations.Connection
	e.other().must(200, "POST", "integrations/azure_devops/oauth/sessions/"+candidate.ID+"/complete", integrations.AzureCompleteInput{}, &moved)
	if moved.OwnerUserID == nil || *moved.OwnerUserID != s3OtherID {
		t.Fatal("claim did not transfer after last unlink")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND status='ACTIVE'`, second.ID) != 1 {
		t.Fatal("other account claim lost")
	}
}

func TestAzureOAuthSessionIsolationReauthorizationAndReplay(t *testing.T) {
	e := newAzureOAuthEnv(t)
	a := e.connect(azureTestAccount)
	child := e.bind(e.member(), a.ID)
	s := e.authorize(e.owner(), integrations.AzureStartInput{Purpose: "reauthorize", ConnectionID: a.ID, Version: a.Version})
	e.member().must(404, "GET", "integrations/azure_devops/oauth/sessions/"+s.ID, nil, nil)
	var renewed integrations.Connection
	e.owner().must(200, "POST", "integrations/azure_devops/oauth/sessions/"+s.ID+"/complete", integrations.AzureCompleteInput{AccountID: azureTestAccount}, &renewed)
	if renewed.Version <= a.Version {
		t.Fatal("reauth did not fence parent")
	}
	var parentVersion uint64
	e.scalar(&parentVersion, `SELECT (settings->>'parent_version')::bigint FROM openfox.integration_connections WHERE id=$1::uuid`, child.ID)
	if parentVersion == renewed.Version {
		t.Fatal("old personal proof revived")
	}
	e.owner().must(200, "POST", "integrations/connections/"+a.ID+"/disconnect", map[string]any{"version": renewed.Version, "confirm_phrase": "断开 Azure DevOps"}, nil)
	var replay integrations.Connection
	e.owner().must(200, "POST", "integrations/azure_devops/oauth/sessions/"+s.ID+"/complete", integrations.AzureCompleteInput{AccountID: azureTestAccount}, &replay)
	if replay.Status != "DISCONNECTED" {
		t.Fatal("completed replay resurrected parent")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid AND credentials_sealed IS NOT NULL`, e.org) != 0 {
		t.Fatal("last disconnect retained unneeded grant")
	}
}

func TestAzureOAuthCompetingClaimAndLastUnlinkInterleave(t *testing.T) {
	e := newAzureOAuthEnv(t)
	parent := e.connect(azureTestAccount)
	sessions := []integrations.AzureSession{e.authorize(e.member(), integrations.AzureStartInput{Purpose: "personal", ConnectionID: parent.ID}), e.authorize(e.other(), integrations.AzureStartInput{Purpose: "personal", ConnectionID: parent.ID})}
	users := []string{s3MemberID, s3OtherID}
	type result struct {
		index      int
		connection integrations.Connection
		err        error
	}
	out := make(chan result, 2)
	start := make(chan struct{})
	for i := range users {
		go func(i int) {
			<-start
			var c integrations.Connection
			err := e.tx(users[i], func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
				var err error
				c, _, err = e.svc.CompleteAzurePersonal(ctx, tx, e.org, a, sessions[i].ID)
				return err
			})
			out <- result{i, c, err}
		}(i)
	}
	close(start)
	r1, r2 := <-out, <-out
	winner, loser := r1, r2
	if winner.err != nil {
		winner, loser = loser, winner
	}
	if winner.err != nil || !errors.Is(loser.err, integrations.ErrPersonalInUse) {
		t.Fatalf("claim outcomes %v / %v", winner.err, loser.err)
	}
	start = make(chan struct{})
	go func() {
		<-start
		err := e.tx(users[winner.index], func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
			_, err := e.svc.Disconnect(ctx, tx, e.org, a, winner.connection.ID, winner.connection.Version, "")
			return err
		})
		out <- result{index: winner.index, err: err}
	}()
	go func() {
		<-start
		var c integrations.Connection
		err := e.tx(users[loser.index], func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
			var err error
			c, _, err = e.svc.CompleteAzurePersonal(ctx, tx, e.org, a, sessions[loser.index].ID)
			return err
		})
		out <- result{index: loser.index, connection: c, err: err}
	}()
	close(start)
	for i := 0; i < 2; i++ {
		r := <-out
		if r.index == winner.index && r.err != nil {
			t.Fatal(r.err)
		}
		if r.index == loser.index && r.err != nil && !errors.Is(r.err, integrations.ErrPersonalInUse) {
			t.Fatal(r.err)
		}
	}
	if err := e.tx(users[loser.index], func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
		_, _, err := e.svc.CompleteAzurePersonal(ctx, tx, e.org, a, sessions[loser.index].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND parent_connection_id=$2::uuid AND scope='PERSONAL' AND status<>'DISCONNECTED'`, e.org, parent.ID) != 1 {
		t.Fatal("claim lost exclusivity")
	}
	// An application-role tenant transaction cannot observe the other tenant's grants.
	selected := tenancy.OrganizationSelection{OrganizationID: e.otherOrg, ActorID: s3OutsiderID, AuthorizationVersion: 1}
	err := e.database.RunForOrganization(e.ctx, e.otherOrg, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selected, nil }), func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid`, e.org).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("cross-tenant grant visible")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
