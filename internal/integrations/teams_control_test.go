package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/teams"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type teamsFixture struct {
	t                                                 *testing.T
	dsn, org, user, space, device, grant, tenant, app string
	actor                                             collaboration.Actor
	service                                           Service
	mu                                                sync.Mutex
	tokenCalls                                        int
	member                                            teams.Member
	replies                                           []map[string]any
	replyPaths                                        []string
	loseReply                                         bool
	channelType                                       string
	channelLayout                                     string
	channelArchived                                   bool
	channelCalls                                      int
	memberStarted                                     chan struct{}
	memberGate                                        chan struct{}
	replyStarted                                      chan struct{}
	replyGate                                         chan struct{}
}
type teamsFixtureTransport struct {
	origin *url.URL
	inner  http.RoundTripper
}

func (t teamsFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || (r.URL.Host != "login.microsoftonline.com" && r.URL.Host != "smba.trafficmanager.net" && r.URL.Host != "graph.microsoft.com") {
		return nil, errors.New("fixture rejects origin")
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.origin.Scheme
	u.Host = t.origin.Host
	copy.URL = &u
	return t.inner.RoundTrip(copy)
}
func newTeamsFixture(t *testing.T) *teamsFixture {
	t.Helper()
	f := &teamsFixture{t: t, dsn: os.Getenv("OPENFOX_I16_TEST_DATABASE_URL"), org: refsTestID(t), user: refsTestID(t), space: refsTestID(t), device: refsTestID(t), tenant: refsTestID(t), app: refsTestID(t)}
	if f.dsn == "" {
		t.Skip("requires exclusive I16 PostgreSQL database")
	}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	f.member = teams.Member{ID: "29:controlled-person", AADObjectID: refsTestID(t), TenantID: f.tenant, Name: "Controlled person"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("scope") != teams.ConnectorScope && r.Form.Get("scope") != teams.GraphScope || r.Form.Get("client_secret") != "synthetic-secret" {
				w.WriteHeader(401)
				return
			}
			f.tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"token_type": "Bearer", "access_token": "controlled-token", "expires_in": 3600})
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/activities/"):
			var body map[string]any
			if r.Header.Get("Authorization") != "Bearer controlled-token" || json.NewDecoder(r.Body).Decode(&body) != nil {
				w.WriteHeader(401)
				return
			}
			f.replies = append(f.replies, body)
			f.replyPaths = append(f.replyPaths, r.URL.Path)
			if f.replyStarted != nil {
				close(f.replyStarted)
				f.replyStarted = nil
				select {
				case <-f.replyGate:
				case <-r.Context().Done():
					return
				}
			}
			if f.loseReply {
				conn, _, e := w.(http.Hijacker).Hijack()
				if e == nil {
					_ = conn.Close()
				}
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "controlled/response;opaque"})
		case strings.HasPrefix(r.URL.Path, "/v1.0/teams/"):
			f.channelCalls++
			kind, layout := f.channelType, f.channelLayout
			if kind == "" {
				kind = "standard"
			}
			if layout == "" {
				layout = "post"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "19:channel;opaque", "membershipType": kind, "layoutType": layout, "isArchived": f.channelArchived})
		case strings.Contains(r.URL.Path, "/members/"):
			if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer controlled-token" {
				w.WriteHeader(401)
				return
			}
			if f.memberStarted != nil {
				close(f.memberStarted)
				f.memberStarted = nil
				select {
				case <-f.memberGate:
				case <-r.Context().Done():
					return
				}
			}
			_ = json.NewEncoder(w).Encode(f.member)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	box, err := secrets.New(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test", Teams: teams.NewClientWithHTTP(&http.Client{Transport: teamsFixtureTransport{u, server.Client().Transport}})}
	c, err := pgx.Connect(context.Background(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var name string
	if err = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || !strings.HasPrefix(name, "i16_") {
		t.Fatal("requires isolated i16_ database")
	}
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, []any{f.user, f.user + "@example.test"}},
		{`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Teams fixture')`, []any{f.org, f.org}},
		{`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, []any{f.org}},
		{`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, []any{f.org, f.user}},
		{`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES($1::uuid,$2::uuid,'TMTEST','Teams','PRIVATE')`, []any{f.org, f.space}},
		{`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, []any{f.org, f.space, f.user}},
		{`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'Teams fixture',clock_timestamp())`, []any{f.device, "teams-" + f.org, f.user}},
		{`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, []any{f.org, f.device}},
	}
	for _, q := range seed {
		if _, err = c.Exec(context.Background(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.QueryRow(context.Background(), `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, f.org, f.device).Scan(&f.grant); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *teamsFixture) run(fn func(context.Context, pgx.Tx) error) error {
	tenant, err := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: f.org, ActorID: f.user, AuthorizationVersion: f.actor.AuthorizationVersion}, tenancy.SourceUserRequest)
	if err != nil {
		return err
	}
	ctx := tenancy.WithTenantContext(context.Background(), tenant)
	c, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,$3,'user_request')`, f.org, f.user, f.actor.AuthorizationVersion); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE openfox_app`); err != nil {
		return err
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (f *teamsFixture) must(fn func(context.Context, pgx.Tx) error) {
	f.t.Helper()
	if err := f.run(fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *teamsFixture) input() TeamsConnectInput {
	return TeamsConnectInput{Kind: BotKindTaskAssistant, SpaceID: f.space, Name: "Controlled Teams", TenantID: f.tenant, AppID: f.app, Secret: "synthetic-secret", Runtime: &FeishuAssistantRuntime{ComputerID: f.device, GrantID: f.grant, Runtime: "codex", Model: "default"}, Manifest: TeamsManifestInput{Version: "1.0.0", DeveloperName: "Controlled developer", WebsiteURL: "https://example.test", PrivacyURL: "https://example.test/privacy", TermsURL: "https://example.test/terms"}}
}
func (f *teamsFixture) connect() (TeamsBot, error) {
	var plan TeamsConnectTarget
	var bot TeamsBot
	err := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareTeamsConnection(ctx, tx, f.org, f.actor, f.input())
		return e
	})
	if err != nil {
		return bot, err
	}
	proof, err := f.service.VerifyTeamsConnection(context.Background(), plan)
	if err != nil {
		return bot, err
	}
	err = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		bot, e = f.service.SaveTeamsConnection(ctx, tx, f.org, f.actor, plan, proof)
		return e
	})
	return bot, err
}
func (f *teamsFixture) route(kind string) teams.Route {
	return teams.Route{TenantID: f.tenant, AppID: f.app, ServiceURL: "https://smba.trafficmanager.net/emea", ConversationID: "19:private-conversation-different-from-user", ActivityID: "opaque/activity;id", FromID: f.member.ID, FromAADObjectID: f.member.AADObjectID, RecipientID: "28:" + f.app, ScopeKind: kind}
}
func TestTeamsControlAndIdentityPG(t *testing.T) {
	f := newTeamsFixture(t)
	bot, err := f.connect()
	if err != nil {
		t.Fatal(err)
	}
	if bot.State != "ACTIVE" || bot.CredentialsVerifiedAt == nil || bot.FirstActivityAt != nil || bot.LastDeliveryAt != nil {
		t.Fatal("credential preflight must not claim install/activity/delivery")
	}
	var challenge TeamsBindingChallenge
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		challenge, e = f.service.IssueTeamsChallenge(ctx, tx, f.org, f.actor, bot.ID)
		return e
	})
	event := teams.VerifiedActivity{Route: f.route("personal"), Text: challenge.Command, BodyHash: fmt.Sprintf("%064x", 1)}
	event.Activity.Type = "message"
	var target TeamsReceiveTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = f.service.PrepareTeamsActivity(ctx, tx, f.org, bot.ID, event)
		return e
	})
	proof, err := f.service.VerifyTeamsActivity(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- f.run(func(ctx context.Context, tx pgx.Tx) error {
				r, e := loadTeamsBot(ctx, tx, bot.ID, true)
				if e != nil {
					return e
				}
				_, e = teamsConsumeChallenge(ctx, tx, f.org, r, event, proof.member, target.challengeHash)
				return e
			})
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			successes++
		} else if errors.Is(e, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("challenge was not one-shot", successes, conflicts)
	}
	var bindings []TeamsPersonalBinding
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		bindings, e = f.service.TeamsPersonalBindings(ctx, tx, f.org, f.actor)
		return e
	})
	if len(bindings) != 1 || !bindings[0].Active || bindings[0].AADObjectID != f.member.AADObjectID {
		t.Fatal("wrong identity")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var clean bool
		e := tx.QueryRow(ctx, `SELECT credentials_sealed IS NULL AND credentials_key_id='' AND settings->>'from_id'=$2 AND settings->'private_route'->>'conversation_id'=$3 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, bindings[0].ID, f.member.ID, event.Route.ConversationID).Scan(&clean)
		if e == nil && !clean {
			return errors.New("personal secret or route mismatch")
		}
		return e
	})
	foreign := newTeamsFixture(t)
	err = foreign.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := foreign.service.GetTeamsBot(ctx, tx, foreign.org, foreign.actor, bot.ID)
		return e
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant expected not found: %v", err)
	}
	foreign.app = f.app
	if _, err = foreign.connect(); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("global App claim: %v", err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.RemoveTeamsPersonalBinding(ctx, tx, f.org, f.actor, bot.ID, bindings[0].Version)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		v, e := f.service.TeamsPersonalBindings(ctx, tx, f.org, f.actor)
		if e == nil && (len(v) != 1 || v[0].Active) {
			return errors.New("unbind retained active identity")
		}
		return e
	})
}
func TestTeamsCredentialProofCannotReviveRemovalPG(t *testing.T) {
	f := newTeamsFixture(t)
	bot, err := f.connect()
	if err != nil {
		t.Fatal(err)
	}
	var p TeamsConnectTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.PrepareTeamsCredentials(ctx, tx, f.org, f.actor, bot.ID, TeamsCredentialsInput{Version: bot.Version, Secret: "synthetic-secret"})
		return e
	})
	proof, err := f.service.VerifyTeamsConnection(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.RemoveTeamsBot(ctx, tx, f.org, f.actor, bot.ID, bot.Version)
	})
	err = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.SaveTeamsConnection(ctx, tx, f.org, f.actor, p, proof)
		return e
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("late proof revived removal: %v", err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var clean bool
		e := tx.QueryRow(ctx, `SELECT status='DISCONNECTED' AND credentials_sealed IS NULL FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, bot.ConnectionID).Scan(&clean)
		if e == nil && !clean {
			return errors.New("credentials retained")
		}
		return e
	})
}

func TestTeamsBindingCommandNeverBecomesRuntimeInput(t *testing.T) {
	for _, tc := range []struct{ scope, text string }{{"personal", "prefix /openfox bind code"}, {"group", "/openfox bind code"}, {"personal", "/openfox binding code"}} {
		e := teams.VerifiedActivity{Route: teams.Route{ScopeKind: tc.scope}, Text: tc.text}
		e.Activity.Type = "message"
		_, binding, err := teamsChallengeHash(e)
		if !binding || err == nil {
			t.Fatalf("must reject instead of run: %s", tc.scope)
		}
	}
}
