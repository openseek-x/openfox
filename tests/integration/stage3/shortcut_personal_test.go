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

const shortcutWorkspace = "11111111-1111-4111-8111-111111111111"
const shortcutUser = "22222222-2222-4222-8222-222222222222"
const shortcutOther = "33333333-3333-4333-8333-333333333333"

type shortcutItem struct {
	ID    int
	Owner string
}
type shortcutFake struct {
	mu       sync.Mutex
	requests int
	items    []shortcutItem
	comments []string
	before   func(*http.Request)
}

func (f *shortcutFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	before := f.before
	items := append([]shortcutItem(nil), f.items...)
	f.mu.Unlock()
	if before != nil {
		before(r)
	}
	if r.Host != "api.app.shortcut.com" {
		http.Error(w, "unexpected host", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	token := r.Header.Get("Shortcut-Token")
	if token == "invalid" {
		w.WriteHeader(401)
		return
	}
	if token == "limited" {
		w.WriteHeader(429)
		return
	}
	id, email := shortcutUser, s3Email(s3MemberID)
	if token == "other" {
		id, email = shortcutOther, s3Email(s3OtherID)
	}
	if token == "same-other" {
		email = s3Email(s3OtherID)
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v3/member":
		workspace := shortcutWorkspace
		if token == "wrong-workspace" {
			workspace = shortcutOther
		}
		write(map[string]any{"id": id, "workspace2": map[string]string{"id": workspace}})
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v3/members/"):
		if token == "wrong-id" {
			id = shortcutOther
		}
		if token == "missing-email" {
			email = ""
		}
		write(map[string]any{"id": id, "disabled": token == "disabled", "state": "full", "profile": map[string]any{"deactivated": token == "deactivated", "email_address": email, "name": "Fixture Shortcut Member"}})
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects":
		write([]any{map[string]any{"id": 101, "name": "Fixture Shortcut project"}})
	case r.Method == "GET" && r.URL.Path == "/api/v3/workflows":
		write([]any{map[string]any{"id": 1, "states": []any{map[string]any{"id": 1, "name": "Todo", "type": "unstarted"}}}})
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects/101/stories":
		out := []any{}
		for _, v := range items {
			out = append(out, map[string]any{"id": v.ID, "name": fmt.Sprint("Shortcut task ", v.ID), "description": "Fixture description", "project_id": 101, "workflow_state_id": 1, "owner_ids": []string{v.Owner}, "story_type": "feature"})
		}
		write(out)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/comments"):
		if token != "member" && token != "rotated" && token != "other" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["text"] == nil {
			w.WriteHeader(400)
			return
		}
		text, _ := body["text"].(string)
		f.mu.Lock()
		f.comments = append(f.comments, text)
		count := len(f.comments)
		f.mu.Unlock()
		w.WriteHeader(201)
		if text == "unknown-response" {
			_, _ = w.Write([]byte("{"))
			return
		}
		var story int
		_, _ = fmt.Sscanf(r.URL.Path, "/api/v3/stories/%d/comments", &story)
		write(map[string]any{"id": 1000 + count, "story_id": story, "author_id": id})
	default:
		w.WriteHeader(404)
	}
}

type shortcutEnv struct {
	*s3Env
	fake      *shortcutFake
	transport http.RoundTripper
	box       *secrets.Box
	svc       integrations.Service
	parent    integrations.Connection
}

func newShortcutEnv(t *testing.T) *shortcutEnv {
	e := newS3Env(t)
	f := &shortcutFake{}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.app.shortcut.com:443" {
			return nil, fmt.Errorf("unexpected upstream")
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, ProjectTransport: tr}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	out := &shortcutEnv{s3Env: e, fake: f, transport: tr, box: box, svc: integrations.Service{Box: box, ProjectTransport: tr}}
	if err = out.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
		var err error
		out.parent, err = out.svc.SaveProviderToken(ctx, tx, e.org, a, "shortcut", integrations.ProviderTokenInput{Method: "token", Secret: "fixture-parent"}, shortcutWorkspace, "Fixture Shortcut workspace")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func (e *shortcutEnv) tx(user string, fn func(context.Context, pgx.Tx, collaboration.Actor) error) error {
	var v uint64
	e.scalar(&v, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, user)
	a := collaboration.Actor{UserID: user, Role: policy.OrganizationRole(strings.ToLower(s3Roles[user])), AuthorizationVersion: v}
	sel := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: user, AuthorizationVersion: v}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }), func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, a) })
}
func (e *shortcutEnv) input(token string, enabled bool, version uint64) map[string]any {
	return map[string]any{"connection_id": e.parent.ID, "token": token, "comments_enabled": enabled, "version": version}
}
func (e *shortcutEnv) bind(enabled bool) integrations.Connection {
	var c integrations.Connection
	e.member().must(200, "PUT", "me/account-bindings/shortcut", e.input("member", enabled, 0), &c)
	return c
}
func (e *shortcutEnv) setItems(items ...shortcutItem) {
	e.fake.mu.Lock()
	e.fake.items = items
	e.fake.mu.Unlock()
}
func (e *shortcutEnv) worker() *imports.Worker {
	return &imports.Worker{Pool: e.database, Secrets: e.box, ProjectTransport: e.transport, Instance: "shortcut-test", MaxBatches: 1}
}
func (e *shortcutEnv) importJob(continuous bool) imports.Job {
	var j imports.Job
	e.owner().must(201, "POST", "imports/shortcut/project", map[string]any{"connection_id": e.parent.ID, "project_id": "101", "space_id": e.space.ID}, &j)
	cfg := *j.Config
	cfg.Users = nil
	for _, u := range j.Analysis.Users {
		m := imports.UserMapping{SourceID: u.SourceID, Mode: "external"}
		if b := u.VerifiedBinding; b != nil {
			if u.SuggestedUserID != nil {
				e.t.Fatal("legacy suggestion")
			}
			m.Mode, m.UserID, m.VerifiedBindingID = "member", b.UserID, b.ID
		}
		cfg.Users = append(cfg.Users, m)
	}
	cfg.Options.ContinuousSync = continuous
	e.owner().must(200, "PUT", "imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
	return j
}
func (e *shortcutEnv) start(j imports.Job) imports.Job {
	e.owner().must(200, "POST", "imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion}, &j)
	return j
}
func (e *shortcutEnv) drain(j imports.Job) imports.Job {
	w := e.worker()
	for i := 0; i < 20; i++ {
		if err := w.RunOrganization(e.ctx, e.org); err != nil {
			e.t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+j.ID, nil, &j)
		if j.Status == "SUCCEEDED" {
			return j
		}
		if j.Status == "FAILED" {
			e.t.Fatalf("failed %+v", j)
		}
	}
	e.t.Fatal("did not finish")
	return j
}

func TestShortcutPersonalHTTPModesCASAndIsolation(t *testing.T) {
	e := newShortcutEnv(t)
	member := e.member()
	e.guest().expectError(403, "", "PUT", "me/account-bindings/shortcut", e.input("member", false, 0))
	e.outsider().expectError(404, "", "PUT", "me/account-bindings/shortcut", e.input("member", false, 0))
	if e.fake.requests != 0 {
		t.Fatal("unauthorized HTTP reached upstream")
	}
	for token, code := range map[string]string{"invalid": "SHORTCUT_AUTH_FAILED", "wrong-id": "SHORTCUT_AUTH_FAILED", "disabled": "SHORTCUT_AUTH_FAILED", "deactivated": "SHORTCUT_AUTH_FAILED", "missing-email": "SHORTCUT_EMAIL_MISMATCH", "other": "SHORTCUT_EMAIL_MISMATCH", "wrong-workspace": "SHORTCUT_WORKSPACE_MISMATCH", "limited": "SHORTCUT_RATE_LIMITED"} {
		status := 422
		if token == "limited" {
			status = 429
		}
		member.expectError(status, code, "PUT", "me/account-bindings/shortcut", e.input(token, false, 0))
	}
	c := e.bind(false)
	if c.AccountKind != "identity" || e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NULL AND credentials_key_id=''`, c.ID) != 1 {
		t.Fatal("identity retained token")
	}
	var dup integrations.Connection
	member.must(200, "PUT", "me/account-bindings/shortcut", e.input("member", false, 0), &dup)
	if dup.ID != c.ID || dup.Version != c.Version {
		t.Fatal("not idempotent")
	}
	e.other().expectError(409, "SHORTCUT_ACCOUNT_IN_USE", "PUT", "me/account-bindings/shortcut", e.input("same-other", false, 0))
	member.expectError(409, "VERSION_CONFLICT", "PUT", "me/account-bindings/shortcut", e.input("member", true, 0))
	member.must(200, "PUT", "me/account-bindings/shortcut", e.input("member", true, c.Version), &c)
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NOT NULL AND settings->>'comments_enabled'='true' AND NOT settings::text LIKE '%member%'`, c.ID) != 1 {
		t.Fatal("comment secret missing or leaked")
	}
	var view imports.AccountBindingsView
	member.must(200, "GET", "me/account-bindings", nil, &view)
	found := false
	for _, row := range view.Providers {
		if row.ID == "shortcut" {
			found = true
			if row.Method != "token" || row.Binding == nil || len(row.Instances) != 1 || row.Instances[0].Binding.ShortcutIdentity == nil || !row.Instances[0].Binding.ShortcutIdentity.CommentsEnabled {
				t.Fatalf("wrong account view %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("Shortcut missing")
	}
	before := e.fake.requests
	member.must(200, "PUT", "me/account-bindings/shortcut", e.input("", false, c.Version), &c)
	if e.fake.requests != before {
		t.Fatal("disabling reached upstream")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NULL AND settings->>'comments_enabled'='false' AND NOT settings ? 'comments_authorized_at'`, c.ID) != 1 {
		t.Fatal("disable retained authorization")
	}
	member.must(200, "PUT", "me/account-bindings/shortcut", e.input("member", true, c.Version), &c)
	e.owner().must(200, "POST", "integrations/connections/"+e.parent.ID+"/disconnect", map[string]any{"version": e.parent.Version, "confirm_phrase": "断开 Shortcut"}, nil)
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND status='DISCONNECTED' AND credentials_sealed IS NULL AND credentials_key_id=''`, c.ID) != 1 {
		t.Fatal("parent disconnect left personal token")
	}
}

func TestShortcutPersonalLateParentAndUnlinkProof(t *testing.T) {
	e := newShortcutEnv(t)
	e.fake.before = func(r *http.Request) {
		if strings.Contains(r.URL.Path, "/members/") {
			e.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE id=$1::uuid`, e.parent.ID)
		}
	}
	e.member().expectError(409, "VERSION_CONFLICT", "PUT", "me/account-bindings/shortcut", e.input("member", false, 0))
	e.fake.before = nil
	// A first bind proof cannot recreate an identity after another bind was unlinked.
	in := integrations.ShortcutPersonalInput{ConnectionID: e.parent.ID, Token: "member"}
	var proof integrations.ShortcutPersonalGrant
	if err := e.tx(s3MemberID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
		var err error
		proof, err = e.svc.PrepareShortcutPersonal(ctx, tx, e.org, a, in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	proof, err = integrations.VerifyShortcutPersonal(e.ctx, proof)
	if err != nil {
		t.Fatal(err)
	}
	c := e.bind(false)
	e.member().must(200, "POST", "integrations/connections/"+c.ID+"/disconnect", map[string]any{"version": c.Version}, nil)
	err = e.tx(s3MemberID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
		_, err := e.svc.SaveShortcutPersonal(ctx, tx, e.org, a, in, proof)
		return err
	})
	if err != integrations.ErrVersionConflict {
		t.Fatalf("late proof: %v", err)
	}
}
