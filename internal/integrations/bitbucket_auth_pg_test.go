package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

type bbPG struct {
	t                     *testing.T
	ctx                   context.Context
	admin                 *pgxpool.Pool
	pool                  *postgres.Pool
	org, other            string
	owner, member, second collaboration.Actor
	s                     Service
	f                     *bbOAuthFake
}

func newBBPG(t *testing.T) *bbPG {
	t.Helper()
	dsn := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires dedicated migrated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
		s, e := membership.NewID()
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	b := &bbPG{t: t, ctx: ctx, admin: admin, pool: pool, org: id(), other: id()}
	b.s, b.f = bbFixture(t)
	b.owner = collaboration.Actor{UserID: id(), Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	b.member = collaboration.Actor{UserID: id(), Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	b.second = collaboration.Actor{UserID: id(), Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	for _, org := range []string{b.org, b.other} {
		b.exec(`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Bitbucket controlled')`, org, "bb-"+org)
	}
	for _, a := range []collaboration.Actor{b.owner, b.member, b.second} {
		b.exec(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, a.UserID, "bb-"+a.UserID+"@example.test")
		b.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,$3)`, b.org, a.UserID, strings.ToUpper(string(a.Role)))
	}
	return b
}
func (b *bbPG) exec(q string, args ...any) {
	b.t.Helper()
	if _, e := b.admin.Exec(b.ctx, q, args...); e != nil {
		b.t.Fatal(e)
	}
}
func (b *bbPG) count(q string, args ...any) int {
	b.t.Helper()
	var n int
	if e := b.admin.QueryRow(b.ctx, q, args...).Scan(&n); e != nil {
		b.t.Fatal(e)
	}
	return n
}
func (b *bbPG) tx(a collaboration.Actor, fn func(context.Context, pgx.Tx) error) error {
	sel := tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: a.UserID, AuthorizationVersion: a.AuthorizationVersion}
	return b.pool.RunForOrganization(b.ctx, b.org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }), fn)
}
func (b *bbPG) start(a collaboration.Actor, in BitbucketStartInput) StartResult {
	b.t.Helper()
	var out StartResult
	e := b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = b.s.StartBitbucket(ctx, tx, b.org, a, in)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	return out
}
func (b *bbPG) authorize(a collaboration.Actor, in BitbucketStartInput) StartResult {
	b.t.Helper()
	s := b.start(a, in)
	var target BitbucketCallbackTarget
	e := b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = b.s.ConsumeBitbucketCallback(ctx, tx, b.org, a, s.SessionID)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	result, e := b.s.VerifyBitbucketCallback(b.ctx, target, "synthetic-code")
	if e != nil {
		b.t.Fatal(e)
	}
	e = b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		return b.s.FinishBitbucketCallback(ctx, tx, b.org, a, s.SessionID, result, "")
	})
	if e != nil {
		b.t.Fatal(e)
	}
	return s
}
func (b *bbPG) target(a collaboration.Actor, s StartResult) BitbucketCompleteTarget {
	b.t.Helper()
	var target BitbucketCompleteTarget
	e := b.tx(a, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = b.s.PrepareBitbucketComplete(ctx, tx, b.org, a, s.SessionID, BitbucketCompleteInput{WorkspaceID: bbWorkspace})
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	target, e = b.s.VerifyBitbucketComplete(b.ctx, target)
	if e != nil {
		b.t.Fatal(e)
	}
	return target
}
func (b *bbPG) connect() Connection {
	b.t.Helper()
	s := b.authorize(b.owner, BitbucketStartInput{Purpose: PurposeOrganization})
	target := b.target(b.owner, s)
	var c Connection
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, _, e = b.s.CompleteBitbucketOrganization(ctx, tx, b.org, b.owner, s.SessionID, target)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	if c.BitbucketDelegatingUserID != bbSubject || c.BitbucketDelegatingUser != "Verified identity" {
		b.t.Fatal("missing verified sender DTO")
	}
	return c
}
func (b *bbPG) expire(id string) {
	b.exec(`UPDATE openfox.integration_connections SET token_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, id)
}
func (b *bbPG) runtime() BitbucketRuntime { return BitbucketRuntime{Pool: b.pool, Service: b.s} }
func (b *bbPG) identityRequest(token string) error {
	_, e := b.s.Bitbucket.Identity(b.ctx, token)
	return e
}

func TestBitbucketPGOrganizationProofPermissionsAndReplay(t *testing.T) {
	b := newBBPG(t)
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.StartBitbucket(ctx, tx, b.org, b.member, BitbucketStartInput{Purpose: PurposeOrganization})
		return e
	}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	session := b.authorize(b.owner, BitbucketStartInput{Purpose: PurposeOrganization})
	target := b.target(b.owner, session)
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.ConsumeBitbucketCallback(ctx, tx, b.org, b.owner, session.SessionID)
		return e
	}); !errors.Is(e, ErrOAuthSessionState) {
		t.Fatal("replayed callback", e)
	}
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.GetBitbucketSession(ctx, tx, b.org, b.member, session.SessionID)
		return e
	}); !errors.Is(e, ErrNotFound) {
		t.Fatal("another actor session", e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	changed := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var change bool
			e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
				var e error
				_, change, e = b.s.CompleteBitbucketOrganization(ctx, tx, b.org, b.owner, session.SessionID, target)
				return e
			})
			changed <- change
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	close(changed)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	n := 0
	for v := range changed {
		if v {
			n++
		}
	}
	if n != 1 {
		t.Fatal("completion not idempotent", n)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION'`, b.org) != 1 {
		t.Fatal("duplicate parent")
	}
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_bitbucket_runtime WHERE organization_id=$1::uuid`, b.other).Scan(&n)
		if e == nil && n != 0 {
			t.Error("cross-tenant runtime visible")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var parentID string
	if e := b.admin.QueryRow(b.ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION'`, b.org).Scan(&parentID); e != nil {
		t.Fatal(e)
	}
	reached := false
	if _, e := b.runtime().Do(b.ctx, b.other, parentID, 0, 0, func(string) error { reached = true; return nil }); !errors.Is(e, ErrNotFound) || reached {
		t.Fatal("cross-tenant grant accessed", e)
	}
	if e := b.runtime().inOrg(b.ctx, b.other, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_bitbucket_runtime WHERE organization_id=$1::uuid`, b.org).Scan(&n)
		if n != 0 {
			t.Error("RLS exposed real other-tenant row")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	session2 := b.authorize(b.owner, BitbucketStartInput{Purpose: PurposeOrganization})
	target2 := b.target(b.owner, session2)
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, _, e := b.s.CompleteBitbucketOrganization(ctx, tx, b.org, b.owner, session2.SessionID, target2)
		return e
	})
	if !errors.Is(e, ErrBitbucketAlreadyConnected) {
		t.Fatal(e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE organization_id=$1::uuid AND id=$2::uuid AND status='COMPLETED' AND pending_sealed IS NULL`, b.org, session.SessionID) != 1 {
		t.Fatal("completed ciphertext retained")
	}
}
func TestBitbucketPGPersonalUUIDCompetitionNoSecretsAndRevoke(t *testing.T) {
	b := newBBPG(t)
	parent := b.connect()
	one := b.authorize(b.member, BitbucketStartInput{Purpose: PurposePersonal, ConnectionID: parent.ID})
	two := b.authorize(b.second, BitbucketStartInput{Purpose: PurposePersonal, ConnectionID: parent.ID})
	if b.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE organization_id=$1::uuid AND purpose='PERSONAL' AND (pending_sealed IS NOT NULL OR code_verifier_sealed IS NOT NULL OR pending_accounts::text LIKE '%synthetic-access%' OR pending_accounts::text LIKE '%synthetic-refresh%')`, b.org) != 0 {
		t.Fatal("personal secret persisted")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	actors := []collaboration.Actor{b.member, b.second}
	sessions := []StartResult{one, two}
	for i := range actors {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- b.tx(actors[i], func(ctx context.Context, tx pgx.Tx) error {
				_, _, e := b.s.CompleteBitbucketPersonal(ctx, tx, b.org, actors[i], sessions[i].SessionID)
				return e
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ErrPersonalInUse) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='bitbucket' AND scope='PERSONAL' AND credentials_sealed IS NULL AND credentials_key_id=''`, b.org) != 1 {
		t.Fatal("identity shape")
	}
	var child, owner string
	var version uint64
	if e := b.admin.QueryRow(b.ctx, `SELECT id::text,owner_user_id::text,version FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='bitbucket' AND scope='PERSONAL'`, b.org).Scan(&child, &owner, &version); e != nil {
		t.Fatal(e)
	}
	actor := b.member
	if owner == b.second.UserID {
		actor = b.second
	}
	if e := b.tx(actor, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.Disconnect(ctx, tx, b.org, actor, child, version, "")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND status='DISCONNECTED' AND external_id=$3`, b.org, child, bbSubject) != 1 {
		t.Fatal("disconnect erased history")
	}
	s := b.authorize(b.member, BitbucketStartInput{Purpose: PurposePersonal, ConnectionID: parent.ID})
	b.exec(`UPDATE openfox.organization_members SET status='REVOKED' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, b.org, b.member.UserID)
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, _, e := b.s.CompleteBitbucketPersonal(ctx, tx, b.org, b.member, s.SessionID)
		return e
	}); e == nil {
		t.Fatal("suspended member completed proof")
	}
}
func TestBitbucketPGRefreshConcurrencyAndHookPreservation(t *testing.T) {
	b := newBBPG(t)
	parent := b.connect()
	b.expire(parent.ID)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	b.f.mu.Lock()
	b.f.entered, b.f.release = entered, release
	b.f.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 1, b.identityRequest)
		done <- e
	}()
	select {
	case <-entered:
	case <-b.ctx.Done():
		t.Fatal("refresh did not start")
	}
	var retry *BitbucketRetry
	if _, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 1, b.identityRequest); !errors.As(e, &retry) {
		t.Fatal("parallel refresh not deferred", e)
	}
	// Hook writes serialize on parent but do not alter OAuth generation.
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var blob []byte
		if e := tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, parent.ID).Scan(&blob); e != nil {
			return e
		}
		c, e := b.s.openCredentials(b.org, parent.ID, blob)
		if e != nil {
			return e
		}
		c.WebhookToken = "new-hook-token"
		c.WebhookSecret = "new-hook-secret"
		raw, _ := json.Marshal(c)
		blob, key, e := b.s.sealBitbucket(raw, connectionAAD(b.org, parent.ID))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, parent.ID, blob, key)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	var blob []byte
	if e = b.admin.QueryRow(b.ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID).Scan(&blob); e != nil {
		t.Fatal(e)
	}
	c, e := b.s.openCredentials(b.org, parent.ID, blob)
	if e != nil || c.AccessToken != "synthetic-renewed" || c.RefreshToken != "synthetic-rotated" || c.WebhookToken != "new-hook-token" || c.WebhookSecret != "new-hook-secret" {
		t.Fatal("refresh lost latest credentials/hook", e)
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	if b.f.refreshes != 1 {
		t.Fatal("duplicate refresh", b.f.refreshes)
	}
}
func TestBitbucketPGUnknownRefreshAndRateLimit(t *testing.T) {
	for _, mode := range []string{"unknown", "invalid", "429", "expired-lease"} {
		t.Run(mode, func(t *testing.T) {
			b := newBBPG(t)
			parent := b.connect()
			b.expire(parent.ID)
			b.f.mu.Lock()
			b.f.refreshMode = mode
			b.f.mu.Unlock()
			if mode == "expired-lease" {
				b.exec(`UPDATE openfox.integration_bitbucket_runtime SET refresh_lease=gen_random_uuid(),refresh_lease_until=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND connection_id=$2::uuid`, b.org, parent.ID)
			}
			_, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 1, b.identityRequest)
			var retry *BitbucketRetry
			if mode == "429" {
				if !errors.As(e, &retry) || retry.After < time.Second {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ErrNeedsReauth) {
				t.Fatal(e)
			}
			_, again := b.runtime().Do(b.ctx, b.org, parent.ID, 0, 0, b.identityRequest)
			if mode == "429" {
				if !errors.As(again, &retry) {
					t.Fatal(again)
				}
			} else if !errors.Is(again, ErrNeedsReauth) {
				t.Fatal(again)
			}
			b.f.mu.Lock()
			defer b.f.mu.Unlock()
			want := 1
			if mode == "expired-lease" {
				want = 0
			}
			if b.f.refreshes != want {
				t.Fatal("old refresh replayed", b.f.refreshes)
			}
		})
	}
}
func TestBitbucketPGLateRefreshCannotReviveDisconnectOrReauthorization(t *testing.T) {
	for _, action := range []string{"disconnect", "reauthorize"} {
		t.Run(action, func(t *testing.T) {
			b := newBBPG(t)
			parent := b.connect()
			b.expire(parent.ID)
			entered, release := make(chan struct{}, 1), make(chan struct{})
			b.f.mu.Lock()
			b.f.entered, b.f.release = entered, release
			b.f.mu.Unlock()
			done := make(chan error, 1)
			go func() {
				_, e := b.runtime().Do(b.ctx, b.org, parent.ID, parent.Version, 1, b.identityRequest)
				done <- e
			}()
			<-entered
			if action == "disconnect" {
				if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
					_, e := b.s.Disconnect(ctx, tx, b.org, b.owner, parent.ID, parent.Version, ConfirmPhrase("Bitbucket"))
					return e
				}); e != nil {
					t.Fatal(e)
				}
			} else {
				b.f.mu.Lock()
				b.f.subject = bbSubjectOther
				b.f.mu.Unlock()
				s := b.authorize(b.owner, BitbucketStartInput{Purpose: PurposeReauthorize, ConnectionID: parent.ID, Version: parent.Version})
				target := b.target(b.owner, s)
				if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
					v, e := b.s.GetBitbucketSession(ctx, tx, b.org, b.owner, s.SessionID)
					if e == nil && !v.SubjectChanged {
						t.Error("changed sender not surfaced")
					}
					return e
				}); e != nil {
					t.Fatal(e)
				}
				if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
					_, _, e := b.s.CompleteBitbucketOrganization(ctx, tx, b.org, b.owner, s.SessionID, target)
					return e
				}); e != nil {
					t.Fatal(e)
				}
			}
			close(release)
			if e := <-done; !errors.Is(e, ErrVersionConflict) {
				t.Fatal("late result accepted", e)
			}
			if action == "disconnect" {
				if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND status='DISCONNECTED' AND credentials_sealed IS NULL`, b.org, parent.ID) != 1 {
					t.Fatal("revived")
				}
			} else {
				if b.count(`SELECT count(*) FROM openfox.integration_bitbucket_runtime WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND subject_id=$3::uuid AND credential_generation=2 AND credentials_version=2`, b.org, parent.ID, bbSubjectOther) != 1 {
					t.Fatal("reauthorization overwritten")
				}
			}
		})
	}
}
func TestBitbucketPGGET401RefreshOnceAndWriteNeverReplayed(t *testing.T) {
	b := newBBPG(t)
	parent := b.connect()
	calls := 0
	a, e := b.runtime().Do(b.ctx, b.org, parent.ID, 0, 0, func(token string) error {
		calls++
		if token == "synthetic-access" {
			return gitapi.ErrTokenRevoked
		}
		return b.identityRequest(token)
	})
	if e != nil || calls != 2 || a.CredentialsVersion != 2 {
		t.Fatal(calls, a.CredentialsVersion, e)
	}
	writes := 0
	_, e = b.runtime().DoWrite(b.ctx, b.org, parent.ID, 0, 0, func(string) error { writes++; return gitapi.ErrTokenRevoked })
	if writes != 1 || !errors.Is(e, gitapi.ErrTokenRevoked) {
		t.Fatal(writes, e)
	}
	_, e = b.runtime().Do(b.ctx, b.org, parent.ID, 0, 0, func(string) error { return gitapi.ErrTokenRevoked })
	if !errors.Is(e, gitapi.ErrTokenRevoked) {
		t.Fatal(e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND status='NEEDS_ATTENTION'`, b.org, parent.ID) != 1 {
		t.Fatal("permanent 401 not paused")
	}
}
func TestBitbucketPGSessionExpiryAndParentVersionFence(t *testing.T) {
	b := newBBPG(t)
	parent := b.connect()
	s := b.authorize(b.member, BitbucketStartInput{Purpose: PurposePersonal, ConnectionID: parent.ID})
	b.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID)
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, _, e := b.s.CompleteBitbucketPersonal(ctx, tx, b.org, b.member, s.SessionID)
		return e
	}); !errors.Is(e, ErrVersionConflict) {
		t.Fatal(e)
	}
	b.exec(`UPDATE openfox.integration_oauth_states SET expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, s.SessionID)
	if e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		v, e := b.s.GetBitbucketSession(ctx, tx, b.org, b.member, s.SessionID)
		if v.Status != "EXPIRED" {
			t.Error(v.Status)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE organization_id=$1::uuid AND id=$2::uuid AND status='EXPIRED' AND pending_accounts='[]'::jsonb AND pending_sealed IS NULL`, b.org, s.SessionID) != 1 {
		t.Fatal("expiry not persisted")
	}
	// The database independently rejects personal credentials, even if a caller
	// bypasses Service. Other provider envelope limits remain untouched.
	if _, e := b.admin.Exec(b.ctx, `UPDATE openfox.integration_oauth_states SET pending_sealed=$3 WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, s.SessionID, []byte("synthetic")); e == nil {
		t.Fatal("personal secret accepted")
	}
	if _, e := b.admin.Exec(b.ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$3 WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, parent.ID, []byte(strings.Repeat("x", 32769))); e == nil {
		t.Fatal("oversize ciphertext accepted")
	}
}

func TestBitbucketPGReadBatchLeaseAndNoWholeBatchReplay(t *testing.T) {
	b := newBBPG(t)
	parent := b.connect()
	calls := 0
	ctx, cancel := bitbucket.WithTraversalBudget(b.ctx)
	defer cancel()
	_, e := b.runtime().DoReadBatch(ctx, b.org, parent.ID, 0, 0, func(token string) error {
		calls++
		var seconds float64
		if e := b.admin.QueryRow(ctx, `SELECT extract(epoch FROM request_lease_until-clock_timestamp())::float8 FROM openfox.integration_bitbucket_runtime WHERE organization_id=$1::uuid AND connection_id=$2::uuid`, b.org, parent.ID).Scan(&seconds); e != nil {
			return e
		}
		if seconds < 50 || seconds > 60 {
			t.Error("batch lease does not cover bounded network plus cleanup", seconds)
		}
		var retry *BitbucketRetry
		if _, e := b.runtime().Do(ctx, b.org, parent.ID, 0, 0, b.identityRequest); !errors.As(e, &retry) {
			t.Error("concurrent batch request not serialized", e)
		}
		for range 2 {
			if _, e := b.s.Bitbucket.Identity(ctx, token); e != nil {
				return e
			}
		}
		return gitapi.ErrTokenRevoked
	})
	if !errors.Is(e, gitapi.ErrTokenRevoked) || calls != 1 {
		t.Fatal("whole batch replayed", e, calls)
	}
	if b.count(`SELECT count(*) FROM openfox.integration_bitbucket_runtime WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND request_lease IS NULL AND refresh_lease IS NULL`, b.org, parent.ID) != 1 {
		t.Fatal("batch lease not cleaned")
	}
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	if b.f.refreshes != 0 {
		t.Fatal("batch 401 triggered refresh/replay")
	}
}
