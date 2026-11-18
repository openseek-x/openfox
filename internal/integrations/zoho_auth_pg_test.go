package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/zohobugtracker"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type zohoRoundTrip func(*http.Request) (*http.Response, error)

func (f zohoRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type zohoPG struct {
	t                              *testing.T
	ctx                            context.Context
	admin                          *pgxpool.Pool
	pool                           *postgres.Pool
	org, other                     string
	owner, member, second          collaboration.Actor
	s                              Service
	mu                             sync.Mutex
	refreshes, codes               int
	invalidRefresh                 bool
	refreshStarted, refreshRelease chan struct{}
	memberActive                   bool
	memberEmail                    string
}

func newZohoPG(t *testing.T) *zohoPG {
	t.Helper()
	dsn := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires dedicated migrated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(admin.Close)
	pool, e := postgres.New(ctx, postgres.Config{URL: dsn, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	id := func() string {
		v, e := membership.NewID()
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	b := &zohoPG{t: t, ctx: ctx, admin: admin, pool: pool, org: id(), other: id(), memberActive: true}
	// This suite owns its dedicated PostgreSQL database and runs sequentially.
	// A preceding 429 case may intentionally leave a decades-long cooldown.
	// Reset only this synthetic shared budget at fixture creation, never in production.
	b.exec(`UPDATE openfox_private.zoho_request_budget SET next_at='-infinity',cooldown_until='-infinity'`)
	b.owner = collaboration.Actor{UserID: id(), Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	b.member = collaboration.Actor{UserID: id(), Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	b.second = collaboration.Actor{UserID: id(), Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	b.memberEmail = "zoho-" + b.member.UserID + "@example.test"
	for _, org := range []string{b.org, b.other} {
		b.exec(`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Zoho controlled')`, org, "zoho-"+org)
	}
	for _, actor := range []collaboration.Actor{b.owner, b.member, b.second} {
		b.exec(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, actor.UserID, "zoho-"+actor.UserID+"@example.test")
		b.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,$3)`, b.org, actor.UserID, strings.ToUpper(string(actor.Role)))
	}
	box, e := secrets.New([]byte(strings.Repeat("z", 32)))
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(b.serve))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	transport := server.Client().Transport
	seam := zohoRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "accounts.zoho.com" && r.URL.Host != "bugtracker.zoho.com") {
			return nil, errors.New("unexpected origin")
		}
		clone := r.Clone(r.Context())
		target := *r.URL
		target.Host = u.Host
		clone.URL = &target
		return transport.RoundTrip(clone)
	})
	b.s = Service{Box: box, PublicURL: "https://openfox.example", Zoho: &zohobugtracker.Client{App: zohobugtracker.App{ClientID: "org-client", ClientSecret: "org-secret"}, Transport: seam}, ZohoIdentity: &zohobugtracker.Client{App: zohobugtracker.App{ClientID: "personal-client", ClientSecret: "personal-secret"}, Transport: seam}}
	return b
}
func (b *zohoPG) exec(q string, args ...any) {
	b.t.Helper()
	if _, e := b.admin.Exec(b.ctx, q, args...); e != nil {
		b.t.Fatal(e)
	}
}
func (b *zohoPG) count(q string, args ...any) int {
	b.t.Helper()
	var n int
	if e := b.admin.QueryRow(b.ctx, q, args...).Scan(&n); e != nil {
		b.t.Fatal(e)
	}
	return n
}
func (b *zohoPG) tx(a collaboration.Actor, fn func(context.Context, pgx.Tx) error) error {
	sel := tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: a.UserID, AuthorizationVersion: a.AuthorizationVersion}
	return b.pool.RunForOrganization(b.ctx, b.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }), fn)
}
func (b *zohoPG) runtime() ZohoRuntime { return ZohoRuntime{Pool: b.pool, Service: b.s} }
func (b *zohoPG) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/oauth/v2/token" {
		if r.Method != "POST" {
			b.t.Error("token method")
		}
		if e := r.ParseForm(); e != nil {
			b.t.Error(e)
		}
		personal := r.Form.Get("client_id") == "personal-client"
		refresh := r.Form.Get("grant_type") == "refresh_token"
		b.mu.Lock()
		if refresh {
			b.refreshes++
		} else {
			b.codes++
		}
		invalid, started, release := b.invalidRefresh, b.refreshStarted, b.refreshRelease
		b.mu.Unlock()
		if refresh && started != nil {
			close(started)
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
		}
		if refresh && invalid {
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		token := map[string]any{"access_token": "org-access-" + b.org, "token_type": "Bearer", "expires_in": 3600}
		if personal {
			token["access_token"] = "personal-access-" + b.org
		} else if !refresh {
			token["refresh_token"] = "org-refresh-" + b.org
		}
		json.NewEncoder(w).Encode(token)
		return
	}
	if r.Method != "GET" || !strings.HasPrefix(r.Header.Get("Authorization"), "Zoho-oauthtoken ") {
		b.t.Error("business request not readonly")
	}
	switch r.URL.Path {
	case "/restapi/portals/":
		subject := "2060758"
		if strings.Contains(r.Header.Get("Authorization"), "personal-access") {
			subject = "2060759"
		}
		json.NewEncoder(w).Encode(map[string]any{"login_id": subject, "portals": []map[string]string{{"id": "2063927", "name": "controlled portal", "role": "admin"}}})
	case "/restapi/portal/2063927/users/":
		b.mu.Lock()
		active, email := b.memberActive, b.memberEmail
		b.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"users": []map[string]any{{"id": "2060758", "name": "org owner", "email": "zoho-" + b.owner.UserID + "@example.test", "active": true, "role": "admin"}, {"id": "2060759", "name": "member", "email": email, "active": active, "role": "employee"}}})
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{}`)
	}
}
func (b *zohoPG) authorize(a collaboration.Actor, in ZohoStartInput) StartResult {
	b.t.Helper()
	var start StartResult
	e := b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		start, e = b.s.StartZoho(ctx, tx, b.org, a, in)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	var target ZohoCallbackTarget
	e = b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = b.s.ConsumeZohoCallback(ctx, tx, b.org, a, start.SessionID)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	result, e := b.s.VerifyZohoCallback(b.ctx, b.runtime(), b.org, target, "synthetic-code")
	if e != nil {
		b.t.Fatal(e)
	}
	if e = b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		return b.s.FinishZohoCallback(ctx, tx, b.org, a, start.SessionID, result, "")
	}); e != nil {
		b.t.Fatal(e)
	}
	return start
}
func (b *zohoPG) target(a collaboration.Actor, id string) ZohoCompleteTarget {
	b.t.Helper()
	var out ZohoCompleteTarget
	e := b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = b.s.PrepareZohoComplete(ctx, tx, b.org, a, id, ZohoCompleteInput{PortalID: "2063927"})
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	out, e = b.s.VerifyZohoComplete(b.ctx, b.runtime(), b.org, out)
	if e != nil {
		b.t.Fatal(e)
	}
	return out
}
func (b *zohoPG) complete(a collaboration.Actor, session StartResult) Connection {
	b.t.Helper()
	target := b.target(a, session.SessionID)
	var c Connection
	e := b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, _, e = b.s.CompleteZoho(ctx, tx, b.org, a, session.SessionID, target)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	return c
}
func (b *zohoPG) connect() Connection {
	return b.complete(b.owner, b.authorize(b.owner, ZohoStartInput{Purpose: PurposeOrganization}))
}
func (b *zohoPG) disconnect(c Connection) {
	b.t.Helper()
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.Disconnect(ctx, tx, b.org, b.owner, c.ID, c.Version, ConfirmPhrase("Zoho BugTracker"))
		return e
	}); e != nil {
		b.t.Fatal(e)
	}
}
func TestZohoPGOrganizationProofReplayAndRLS(t *testing.T) {
	b := newZohoPG(t)
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.StartZoho(ctx, tx, b.org, b.member, ZohoStartInput{Purpose: PurposeOrganization})
		return e
	}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	session := b.authorize(b.owner, ZohoStartInput{Purpose: PurposeOrganization})
	target := b.target(b.owner, session.SessionID)
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.ConsumeZohoCallback(ctx, tx, b.org, b.owner, session.SessionID)
		return e
	}); !errors.Is(e, ErrOAuthSessionState) {
		t.Fatal("callback replay", e)
	}
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.GetZohoSession(ctx, tx, b.org, b.member, session.SessionID)
		return e
	}); !errors.Is(e, ErrNotFound) {
		t.Fatal("other user sees session", e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	changed := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var change bool
			e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
				var e error
				_, change, e = b.s.CompleteZoho(ctx, tx, b.org, b.owner, session.SessionID, target)
				return e
			})
			changed <- change
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	close(changed)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	n := 0
	for c := range changed {
		if c {
			n++
		}
	}
	if n != 1 {
		t.Fatal("non-idempotent complete", n)
	}
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_zoho_runtime WHERE organization_id=$1::uuid`, b.other).Scan(&n)
		if n != 0 {
			t.Error("cross tenant visible")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='zoho_bugtracker'`, b.org) != 1 {
		t.Fatal("duplicate binding")
	}
}
func TestZohoPGPersonalNoTokenAndDisconnect(t *testing.T) {
	b := newZohoPG(t)
	parent := b.connect()
	session := b.authorize(b.member, ZohoStartInput{Purpose: PurposePersonal, ConnectionID: parent.ID})
	if b.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE organization_id=$1::uuid AND id=$2::uuid AND pending_sealed IS NULL AND code_verifier_sealed IS NULL`, b.org, session.SessionID) != 1 {
		t.Fatal("personal token persisted")
	}
	child := b.complete(b.member, session)
	if child.ExternalID != "us:2060759" {
		t.Fatal("wrong identity", child.ExternalID)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND credentials_sealed IS NULL AND credentials_key_id='' AND token_expires_at IS NULL`, b.org, child.ID) != 1 {
		t.Fatal("personal credentials persisted")
	}
	b.disconnect(parent)
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='zoho_bugtracker' AND status='DISCONNECTED' AND credentials_sealed IS NULL`, b.org) != 2 {
		t.Fatal("disconnect did not revoke children")
	}
	var target ZohoCompleteTarget
	e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = b.s.PrepareZohoComplete(ctx, tx, b.org, b.member, session.SessionID, ZohoCompleteInput{})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		c, changed, e := b.s.CompleteZoho(ctx, tx, b.org, b.member, session.SessionID, target)
		if changed || c.Status != StatusDisconnected {
			t.Error("completed replay revived identity")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestZohoPGParentChangeAndEmailFence(t *testing.T) {
	b := newZohoPG(t)
	parent := b.connect()
	session := b.authorize(b.member, ZohoStartInput{Purpose: PurposePersonal, ConnectionID: parent.ID})
	target := b.target(b.member, session.SessionID)
	b.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, b.member.UserID, "changed-"+b.member.UserID+"@example.test")
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, _, e := b.s.CompleteZoho(ctx, tx, b.org, b.member, session.SessionID, target)
		return e
	}); !errors.Is(e, ErrPersonalIdentity) {
		t.Fatal("stale email accepted", e)
	}
	b.disconnect(parent)
	if _, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(string) error { t.Error("disconnected source called"); return nil }); !errors.Is(e, ErrNeedsReauth) {
		t.Fatal(e)
	}
}
func TestZohoPGNonRotatingRefreshConcurrentAndUnknown(t *testing.T) {
	b := newZohoPG(t)
	parent := b.connect()
	b.exec(`UPDATE openfox.integration_connections SET token_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID)
	b.mu.Lock()
	b.refreshStarted = make(chan struct{})
	b.refreshRelease = make(chan struct{})
	started, release := b.refreshStarted, b.refreshRelease
	b.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(string) error { return nil })
		done <- e
	}()
	select {
	case <-started:
	case <-b.ctx.Done():
		t.Fatal(b.ctx.Err())
	}
	if _, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(string) error { t.Error("concurrent request entered"); return nil }); e == nil {
		t.Fatal("missing refresh lease")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	b.mu.Lock()
	refreshes := b.refreshes
	b.refreshStarted = nil
	b.refreshRelease = nil
	b.mu.Unlock()
	if refreshes != 1 {
		t.Fatal("duplicate refresh", refreshes)
	}
	var raw []byte
	var version uint64
	if e := b.admin.QueryRow(b.ctx, `SELECT credentials_sealed,version FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID).Scan(&raw, &version); e != nil {
		t.Fatal(e)
	}
	creds, e := b.s.openCredentials(b.org, parent.ID, raw)
	if e != nil || creds.RefreshToken != "org-refresh-"+b.org || version != parent.Version {
		t.Fatal("nonrotating grant/version changed", e)
	}
}
func TestZohoPGRefreshResultCannotUndoDisconnect(t *testing.T) {
	b := newZohoPG(t)
	parent := b.connect()
	b.exec(`UPDATE openfox.integration_connections SET token_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID)
	b.mu.Lock()
	b.refreshStarted = make(chan struct{})
	b.refreshRelease = make(chan struct{})
	started, release := b.refreshStarted, b.refreshRelease
	b.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(string) error { t.Error("revoked request issued"); return nil })
		done <- e
	}()
	select {
	case <-started:
	case <-b.ctx.Done():
		t.Fatal(b.ctx.Err())
	}
	b.disconnect(parent)
	close(release)
	if e := <-done; !errors.Is(e, ErrVersionConflict) {
		t.Fatal("late refresh not fenced", e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND status='DISCONNECTED' AND credentials_sealed IS NULL`, b.org, parent.ID) != 1 {
		t.Fatal("refresh restored credential")
	}
}
func TestZohoPGBudgetAndSealedBoundaries(t *testing.T) {
	b := newZohoPG(t)
	var delay float64
	b.exec(`UPDATE openfox_private.zoho_request_budget SET next_at=clock_timestamp()-interval '1 second',cooldown_until='-infinity'`)
	if e := b.pool.RunControlPlane(b.ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT openfox_private.reserve_zoho_request()`).Scan(&delay)
	}); e != nil || delay != 0 {
		t.Fatal(delay, e)
	}
	if e := b.pool.RunControlPlane(b.ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT openfox_private.reserve_zoho_request()`).Scan(&delay)
	}); e != nil || delay <= 0 || delay > 2.01 {
		t.Fatal(delay, e)
	}
	max := 16384 - keyIDLen - secrets.NonceSize - 16
	blob, _, e := b.s.sealZoho([]byte(strings.Repeat("x", max)), "test")
	if e != nil || len(blob) != 16384 {
		t.Fatal(len(blob), e)
	}
	if _, _, e = b.s.sealZoho([]byte(strings.Repeat("x", max+1)), "test"); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("oversize envelope accepted", e)
	}
}

func TestZohoPGQueuedAdmission(t *testing.T) {
	for _, mode := range []string{"get_disconnect", "get_expired", "refresh_disconnect", "refresh_expired"} {
		t.Run(mode, func(t *testing.T) {
			b := newZohoPG(t)
			parent := b.connect()
			refresh := strings.HasPrefix(mode, "refresh")
			if refresh {
				b.exec(`UPDATE openfox.integration_connections SET token_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID)
			}
			b.exec(`UPDATE openfox_private.zoho_request_budget SET next_at=clock_timestamp()+interval '1800 milliseconds',cooldown_until='-infinity'`)
			var mu sync.Mutex
			sent := 0
			done := make(chan error, 1)
			go func() {
				_, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(token string) error {
					mu.Lock()
					sent++
					mu.Unlock()
					_, e := b.s.Zoho.PortalsPage(b.ctx, token, 1)
					return e
				})
				done <- e
			}()
			column := "request"
			if refresh {
				column = "refresh"
			}
			claimed := false
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if b.count(`SELECT count(*) FROM openfox.integration_zoho_runtime WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND `+column+`_lease IS NOT NULL`, b.org, parent.ID) == 1 {
					claimed = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !claimed {
				t.Fatal("request did not enter queued lease")
			}
			if strings.HasSuffix(mode, "disconnect") {
				b.disconnect(parent)
			} else {
				b.exec(`UPDATE openfox.integration_zoho_runtime SET `+column+`_lease_until=clock_timestamp()+interval '100 milliseconds' WHERE organization_id=$1::uuid AND connection_id=$2::uuid`, b.org, parent.ID)
			}
			e := <-done
			if !errors.Is(e, ErrVersionConflict) {
				t.Fatal("expected admission fence", e)
			}
			mu.Lock()
			getSent := sent
			mu.Unlock()
			b.mu.Lock()
			refreshSent := b.refreshes
			b.mu.Unlock()
			if getSent != 0 || refreshSent != 0 {
				t.Fatalf("HTTP admitted after queued authorization/lease changed: GET=%d refresh=%d", getSent, refreshSent)
			}
		})
	}
}

func TestZohoPGSharedMaintenanceAndRefreshBudget(t *testing.T) {
	b := newZohoPG(t)
	b.exec(`UPDATE openfox_private.zoho_request_budget SET next_at='-infinity',cooldown_until='-infinity'`)
	b.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, b.org)
	session := b.authorize(b.owner, ZohoStartInput{Purpose: PurposeOrganization})
	b.exec(`UPDATE openfox.integration_oauth_states SET expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, session.SessionID)
	var found bool
	if e := b.pool.RunControlPlane(b.ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox_private.list_asana_maintenance(NULL,100) WHERE organization_id=$1::uuid)`, b.org).Scan(&found)
	}); e != nil || !found {
		t.Fatal("expired Zoho session not scanned", e)
	}
	if e := (AsanaRuntime{Pool: b.pool}).MaintainOrganization(b.ctx, b.org); e != nil {
		t.Fatal(e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE organization_id=$1::uuid AND id=$2::uuid AND status='EXPIRED' AND pending_sealed IS NULL AND code_verifier_sealed IS NULL`, b.org, session.SessionID) != 1 {
		t.Fatal("expired ciphertext retained")
	}
	parent := b.connect()
	b.exec(`UPDATE openfox.integration_connections SET token_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID)
	b.exec(`UPDATE openfox_private.zoho_request_budget SET next_at=clock_timestamp()+interval '1 minute',cooldown_until='-infinity'`)
	_, err := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(string) error { t.Error("budget blocked GET issued"); return nil })
	var retry *ZohoRetry
	if !errors.As(err, &retry) {
		t.Fatal("expected shared budget delay", err)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_zoho_runtime WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND refresh_failures=0 AND refresh_lease IS NULL AND retry_at>clock_timestamp()`, b.org, parent.ID) != 1 {
		t.Fatal("waiting counted as failed refresh")
	}
	b.exec(`UPDATE openfox_private.zoho_request_budget SET next_at='-infinity',cooldown_until='-infinity'`)
	b.exec(`UPDATE openfox.integration_zoho_runtime SET retry_at=NULL WHERE organization_id=$1::uuid AND connection_id=$2::uuid`, b.org, parent.ID)
	b.mu.Lock()
	b.invalidRefresh = true
	b.mu.Unlock()
	_, err = b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 0, func(string) error { t.Error("invalid credential GET issued"); return nil })
	if err == nil {
		t.Fatal("invalid grant accepted")
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND status='NEEDS_ATTENTION'`, b.org, parent.ID) != 1 {
		t.Fatal("invalid grant not paused")
	}
}
