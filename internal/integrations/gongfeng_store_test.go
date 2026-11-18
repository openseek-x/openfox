package integrations

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type gongfengDBFixture struct {
	t                     *testing.T
	dsn, org, user, space string
	service               Service
	actor                 collaboration.Actor
}

func newGongfengDBFixture(t *testing.T) *gongfengDBFixture {
	t.Helper()
	dsn := os.Getenv("OPENFOX_I10_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OPENFOX_I10_TEST_DATABASE_URL is not configured")
	}
	f := &gongfengDBFixture{t: t, dsn: dsn, org: refsTestID(t), user: refsTestID(t), space: refsTestID(t)}
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var db string
	if err = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&db); err != nil || !strings.HasPrefix(db, "openfox_i10_") {
		t.Fatal("requires exclusive I10 database", db, err)
	}
	for _, q := range []string{
		`INSERT INTO openfox.users(id,email) VALUES('` + f.user + `','` + f.user + `@example.test')`,
		`INSERT INTO openfox.organizations(id,slug,name) VALUES('` + f.org + `','` + f.org + `','I10 fixture')`,
		`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES('` + f.org + `','` + f.user + `','OWNER')`,
		`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES('` + f.org + `','` + f.space + `','GFTEST','I10','PRIVATE')`,
		`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES('` + f.org + `','` + f.space + `','` + f.user + `','OWNER')`,
		`INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES('` + f.org + `','Todo','TODO',0),('` + f.org + `','Doing','IN_PROGRESS',1),('` + f.org + `','Done','DONE',2)`,
		`INSERT INTO openfox.task_types(organization_id,space_id,name,is_default) VALUES('` + f.org + `','` + f.space + `','Task',true)`,
		`INSERT INTO openfox.workflow_statuses(organization_id,space_id,task_type_id,status_id,position,is_default) SELECT t.organization_id,t.space_id,t.id,s.id,s.position,s.category='TODO' FROM openfox.task_types t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id WHERE t.organization_id='` + f.org + `'`,
	} {
		if _, err = c.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	client := gongfeng.New()
	client.HTTP.Transport = gongfengNoNetwork{t}
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test", Gongfeng: client}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	return f
}
func (f *gongfengDBFixture) run(org string, fn func(context.Context, pgx.Tx) error) error {
	tenant, err := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: org, ActorID: f.user, AuthorizationVersion: 1}, tenancy.SourceUserRequest)
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
	if _, err = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, org, f.user); err != nil {
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
func (f *gongfengDBFixture) connection(identity int64) Connection {
	f.t.Helper()
	var c Connection
	err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		plan, e := f.service.PrepareGongfengToken(ctx, tx, f.org, f.actor, GongfengTokenInput{AccessToken: "fixture-pat"})
		if e != nil {
			return e
		}
		c, e = f.service.SaveGongfengToken(ctx, tx, f.org, f.actor, plan, gongfeng.User{ID: identity, Username: "fixture", State: "active"})
		return e
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}
func (f *gongfengDBFixture) repo(c Connection, project int64) (GongfengRepository, error) {
	var r GongfengRepository
	err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		p, e := f.service.PrepareGongfengRepository(ctx, tx, f.org, f.actor, refsTestID(f.t), GongfengRepositoryInput{ConnectionID: c.ID, ConnectionVersion: c.Version, ProjectID: project, SpaceID: f.space})
		if e != nil {
			return e
		}
		yes, no, private := true, false, 0
		p.project = gongfeng.Project{ID: project, Path: "owner/project", Type: "GIT", Archived: &no, IssuesEnabled: &yes, Visibility: &private, Public: &no}
		p.verified = true
		r, e = f.service.SaveGongfengRepository(ctx, tx, f.org, f.actor, p)
		return e
	})
	return r, err
}

func TestGongfengConnectionCASAndTenant(t *testing.T) {
	f := newGongfengDBFixture(t)
	c := f.connection(71)
	var plan GongfengTokenPlan
	err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareGongfengToken(ctx, tx, f.org, f.actor, GongfengTokenInput{ConnectionID: c.ID, Version: c.Version, AccessToken: "replacement-pat"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.SaveGongfengToken(ctx, tx, f.org, f.actor, plan, gongfeng.User{ID: 72, Username: "different", State: "active"})
		return e
	})
	if !errors.Is(err, ErrNeedsReauth) {
		t.Fatal("changed identity accepted", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				_, e := f.service.SaveGongfengToken(ctx, tx, f.org, f.actor, plan, gongfeng.User{ID: 71, Username: "renamed", State: "active"})
				return e
			})
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := f.service.loadGongfeng(ctx, tx, f.org, c.ID, false)
		if e != nil {
			return e
		}
		if a.IdentityID != 71 || a.CredentialGeneration != 2 || a.credentials.AccessToken != "replacement-pat" {
			t.Fatal("replacement incorrect")
		}
		var blob []byte
		if e = tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE id=$1::uuid`, c.ID).Scan(&blob); e != nil {
			return e
		}
		_, e = f.service.openCredentials(refsTestID(t), c.ID, blob)
		if e == nil {
			t.Fatal("AAD bypass")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other := newGongfengDBFixture(t)
	err = other.run(other.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.loadGongfeng(ctx, tx, other.org, c.ID, false)
		return e
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("cross tenant connection visible", err)
	}
	// A validation that completed after disconnect cannot recreate credentials.
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		current, e := loadConnection(ctx, tx, c.ID)
		if e != nil {
			return e
		}
		plan, e = f.service.PrepareGongfengToken(ctx, tx, f.org, f.actor, GongfengTokenInput{ConnectionID: c.ID, Version: current.Version, AccessToken: "late-pat"})
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp(),credentials_sealed=NULL,version=version+1 WHERE id=$1::uuid`, c.ID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.SaveGongfengToken(ctx, tx, f.org, f.actor, plan, gongfeng.User{ID: 71, Username: "fixture", State: "active"})
		return e
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatal("late token resurrected", err)
	}
}

func TestGongfengStableProjectUniquenessAndRLS(t *testing.T) {
	f := newGongfengDBFixture(t)
	a, b := f.connection(71), f.connection(72)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, c := range []Connection{a, b} {
		wg.Add(1)
		go func(c Connection) { defer wg.Done(); _, e := f.repo(c, 81); results <- e }(c)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrSyncLinkExists) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	other := newGongfengDBFixture(t)
	oc := other.connection(71)
	if _, err := other.repo(oc, 81); err != nil {
		t.Fatal("cross tenant independent ID", err)
	}
	err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_gongfeng_repositories WHERE organization_id=$1::uuid`, other.org).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			t.Fatal("RLS leaked")
		}
		var forced bool
		if e := tx.QueryRow(ctx, `SELECT bool_and(relforcerowsecurity AND relrowsecurity) FROM pg_class WHERE oid IN ('openfox.integration_gongfeng_repositories'::regclass,'openfox.integration_gongfeng_work'::regclass)`).Scan(&forced); e != nil {
			return e
		}
		if !forced {
			t.Fatal("RLS not forced")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both cross-tenant FK and app DELETE must be rejected by actual PostgreSQL.
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM openfox.integration_gongfeng_work`)
		return e
	})
	if err == nil {
		t.Fatal("app can delete durable sends")
	}
}

func TestGongfengWorkGenerationLeaseAndCap(t *testing.T) {
	f := newGongfengDBFixture(t)
	c := f.connection(71)
	r, err := f.repo(c, 81)
	if err != nil {
		t.Fatal(err)
	}
	var workID string
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET status='ACTIVE',hook_state='ACTIVE',hook_generation=generation,hook_credential_generation=1 WHERE id=$1::uuid`, r.ID)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='ACTIVE' WHERE id=$1::uuid`, r.SyncLinkID); e != nil {
			return e
		}
		_, e = f.service.loadGongfeng(ctx, tx, f.org, c.ID, true)
		if e != nil {
			return e
		}
		r, e = loadGongfengRepository(ctx, tx, c.ID, r.ID, true)
		if e != nil {
			return e
		}
		if e = enqueueGongfengWork(ctx, tx, r, "ISSUE_REFRESH", "901", nil, map[string]any{}); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid`, r.ID).Scan(&workID)
	})
	if err != nil {
		t.Fatal(err)
	}
	var first GongfengWork
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		first, e = f.service.ClaimGongfengWork(ctx, tx, f.org, c.ID, r.ID, workID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ClaimGongfengWork(ctx, tx, f.org, c.ID, r.ID, workID)
		return e
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("duplicate active lease", err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.loadGongfeng(ctx, tx, f.org, c.ID, true)
		if e != nil {
			return e
		}
		_, e = loadGongfengRepository(ctx, tx, c.ID, r.ID, true)
		if e != nil {
			return e
		}
		return enqueueGongfengWork(ctx, tx, r, "ISSUE_REFRESH", "901", nil, map[string]any{})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return f.service.CompleteGongfengWork(ctx, tx, f.org, first)
	})
	if !errors.Is(err, errGongfengLeaseLost) {
		t.Fatal("stale generation committed", err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, workID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var second GongfengWork
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		second, e = f.service.ClaimGongfengWork(ctx, tx, f.org, c.ID, r.ID, workID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.LeaseToken == first.LeaseToken || second.RequestedGeneration != 2 {
		t.Fatal("lease not fenced")
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return f.service.CompleteGongfengWork(ctx, tx, f.org, first)
	})
	if !errors.Is(err, errGongfengLeaseLost) {
		t.Fatal("old lease committed", err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return f.service.CompleteGongfengWork(ctx, tx, f.org, second)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.integration_gongfeng_work(organization_id,connection_id,repository_id,kind,object_key) SELECT openfox_private.current_organization_id(),$1::uuid,$2::uuid,'ISSUE_REFRESH',x::text FROM generate_series(10000,19999) x`, c.ID, r.ID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.loadGongfeng(ctx, tx, f.org, c.ID, true)
		if e != nil {
			return e
		}
		_, e = loadGongfengRepository(ctx, tx, c.ID, r.ID, true)
		if e != nil {
			return e
		}
		return enqueueGongfengWork(ctx, tx, r, "ISSUE_REFRESH", "99999", nil, map[string]any{})
	})
	if !errors.Is(err, ErrGongfengQueueFull) {
		t.Fatal("cap bypass", err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.loadGongfeng(ctx, tx, f.org, c.ID, true)
		if e != nil {
			return e
		}
		_, e = loadGongfengRepository(ctx, tx, c.ID, r.ID, true)
		if e != nil {
			return e
		}
		return enqueueGongfengWork(ctx, tx, r, "ISSUE_REFRESH", "10000", nil, map[string]any{})
	})
	if err != nil {
		t.Fatal("existing pending cannot coalesce", err)
	}
}

func TestGongfengConnectionLinkForeignKey(t *testing.T) {
	f := newGongfengDBFixture(t)
	a, b := f.connection(71), f.connection(72)
	r, e := f.repo(a, 81)
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET connection_id=$2::uuid WHERE id=$1::uuid`, r.ID, b.ID)
		return e
	})
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "23503" {
		t.Fatal("cross-connection sync_link accepted", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var body string
		if e := tx.QueryRow(ctx, `SELECT pg_get_functiondef('openfox_private.list_integration_sync_organizations(uuid,integer)'::regprocedure)`).Scan(&body); e != nil {
			return e
		}
		if !strings.Contains(body, "integration_gongfeng_work") || !strings.Contains(body, "integration_gitcode_work") {
			t.Fatal("lost shared scanner provider")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestGongfengReceiptRetentionKeepsBusinessWork(t *testing.T) {
	f := newGongfengDBFixture(t)
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.integration_gongfeng_work(organization_id,connection_id,repository_id,kind,object_key,status,next_attempt_at,created_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'RECEIPT','expired','DONE',NULL,clock_timestamp()-interval '8 days'),(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'RECEIPT','recent','DONE',NULL,clock_timestamp()),(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'ISSUE_REFRESH','old-business','DONE',NULL,clock_timestamp()-interval '8 days')`, c.ID, r.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET hook_next_attempt_at=NULL WHERE id=$1::uuid`, r.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.worker().RunGongfeng(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var keys []string
		if e := tx.QueryRow(ctx, `SELECT array_agg(object_key ORDER BY object_key) FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid`, r.ID).Scan(&keys); e != nil {
			return e
		}
		if strings.Join(keys, ",") != "old-business,recent" {
			t.Fatal(keys)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

type gongfengNoNetwork struct{ t *testing.T }

func (d gongfengNoNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	d.t.Errorf("test attempted unconfigured upstream: %s %s", r.Method, r.URL.Path)
	return nil, errors.New("test network is disabled")
}
