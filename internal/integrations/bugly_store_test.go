package integrations

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type buglyDBFixture struct {
	t                     *testing.T
	dsn, org, user, space string
	service               Service
	actor                 collaboration.Actor
}

func (f *buglyDBFixture) configured() (Connection, BuglyBinding, string) {
	f.t.Helper()
	var c Connection
	var b BuglyBinding
	var secret BuglySecret
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, e = f.service.CreateBuglyConnection(ctx, tx, f.org, f.actor)
		if e != nil {
			return e
		}
		if c.Status != StatusNeedsAttention {
			f.t.Fatal("connection enabled implicitly")
		}
		var kind string
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.task_types WHERE organization_id=$1::uuid AND space_id=$2::uuid AND is_default`, f.org, f.space).Scan(&kind); e != nil {
			return e
		}
		b, e = f.service.CreateBuglyBinding(ctx, tx, f.org, f.actor, c.ID, BuglyBindingInput{ConnectionVersion: c.Version, ProductID: "Product-01", ProjectName: "Bugly test", SpaceID: f.space, TaskTypeID: kind, PriorityMapping: map[string]string{"urgent": "HIGH"}})
		if e != nil {
			return e
		}
		c, e = f.service.ManageBuglyConnection(ctx, tx, f.org, f.actor, c.ID, "enable", c.Version)
		if e != nil {
			return e
		}
		secret, e = f.service.BuglyWebhookSecret(ctx, tx, f.org, f.actor, c.ID, c.Version)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	u, _ := url.Parse(secret.URL)
	parts := strings.Split(u.Path, "/")
	return c, b, parts[len(parts)-1]
}
func (f *buglyDBFixture) receive(c Connection, token string, body []byte) (BuglyReceipt, error) {
	var out BuglyReceipt
	e := f.runIdentity(f.org, "00000000-0000-0000-0000-000000000000", tenancy.SourceOrganizationWorker, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.ReceiveBugly(ctx, tx, GitWebhookLookup{OrganizationID: f.org, ConnectionID: c.ID}, token, body)
		return e
	})
	return out, e
}
func (f *buglyDBFixture) admin(q string, args ...any) {
	f.t.Helper()
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		f.t.Fatal(e)
	}
	defer c.Close(context.Background())
	tx, e := c.Begin(context.Background())
	if e != nil {
		f.t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(context.Background(), `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, f.org, f.user); e != nil {
		f.t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), q, args...); e != nil {
		f.t.Fatal(e)
	}
	if e = tx.Commit(context.Background()); e != nil {
		f.t.Fatal(e)
	}
}
func TestBuglyConcurrentCreationAndDeletion(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, _, token := f.configured()
	const n = 8
	results := make(chan BuglyReceipt, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := f.receive(c, token, buglyBody("SameIssue", "First title"))
			results <- out
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for out := range results {
		if out.Code != 0 {
			t.Fatal(out)
		}
		if first == "" {
			first = out.URL
		}
		if out.URL != first {
			t.Fatal("duplicate created another URL")
		}
	}
	out, e := f.receive(c, token, buglyBody("SameIssue", "Changed external title"))
	if e != nil || out.Code != 0 || out.URL != first {
		t.Fatal(out, e)
	}
	var taskID string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		var title, priority, description string
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid`, f.org).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			t.Fatal("task count", count)
		}
		if e := tx.QueryRow(ctx, `SELECT id::text,title,priority,description FROM openfox.tasks WHERE organization_id=$1::uuid`, f.org).Scan(&taskID, &title, &priority, &description); e != nil {
			return e
		}
		if title != "First title" || priority != "HIGH" || !strings.HasPrefix(description, "```text") {
			t.Fatal(title, priority, description)
		}
		var processed, ignored int
		if e := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='PROCESSED'),count(*) FILTER(WHERE status='IGNORED') FROM openfox.integration_webhook_deliveries WHERE organization_id=$1::uuid`, f.org).Scan(&processed, &ignored); e != nil {
			return e
		}
		if processed != 1 || ignored != n {
			t.Fatal(processed, ignored)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var version uint64
		if e := tx.QueryRow(ctx, `SELECT version FROM openfox.tasks WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, taskID).Scan(&version); e != nil {
			return e
		}
		return (collaboration.Service{}).DeleteTask(ctx, tx, f.org, f.space, taskID, f.actor, version, refsTestID(t))
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e = f.receive(c, token, buglyBody("SameIssue", "Resurrection attempt"))
	if e != nil || out.Code == 0 || out.URL != "" || out.Message != "source_task_unavailable" {
		t.Fatal(out, e)
	}
}
func TestBuglyRotationCASAndTenant(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, b, token := f.configured()
	var rotated Connection
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		rotated, e = f.service.ManageBuglyConnection(ctx, tx, f.org, f.actor, c.ID, "rotate", c.Version)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(c, token, buglyBody("old", "t")); !errors.Is(e, ErrBuglyInactive) {
		t.Fatal("old token accepted", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageBuglyConnection(ctx, tx, f.org, f.actor, c.ID, "disable", c.Version)
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("stale CAS", e)
	}
	other := newBuglyDBFixture(t)
	e = other.run(other.org, func(ctx context.Context, tx pgx.Tx) error { _, e := loadBuglyBinding(ctx, tx, c.ID, b.ID); return e })
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("cross tenant binding visible", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageBuglyConnection(ctx, tx, f.org, f.actor, c.ID, "disconnect", rotated.Version)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		config, e := f.service.BuglyConfiguration(ctx, tx, f.org, f.actor)
		if e != nil {
			return e
		}
		if config.Connection.Status != StatusDisconnected || len(config.Bindings) != 1 {
			t.Fatal(config)
		}
		var cleared bool
		e = tx.QueryRow(ctx, `SELECT credentials_sealed IS NULL AND webhook_token_hash IS NULL FROM openfox.integration_connections WHERE id=$1::uuid`, c.ID).Scan(&cleared)
		if e == nil && !cleared {
			t.Fatal("disconnect retained secret")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestBuglyAuthorityAndFailureReceipt(t *testing.T) {
	for _, reason := range []string{"viewer", "revoked", "type_disabled", "archived"} {
		t.Run(reason, func(t *testing.T) {
			f := newBuglyDBFixture(t)
			c, b, token := f.configured()
			switch reason {
			case "viewer":
				f.admin(`UPDATE openfox.space_members SET role='VIEWER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
			case "revoked":
				other := refsTestID(t)
				f.admin(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, other, other+"@example.test")
				f.admin(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, f.org, other)
				f.admin(`UPDATE openfox.organization_members SET status='REVOKED' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
			case "type_disabled":
				f.admin(`UPDATE openfox.task_types SET enabled=false WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, b.TaskTypeID)
			case "archived":
				f.admin(`UPDATE openfox.spaces SET status='ARCHIVED',retired_at=clock_timestamp(),retired_by=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.space, f.user)
			}
			out, e := f.receive(c, token, buglyBody("forbidden", "title"))
			if e != nil || out.Code == 0 || out.URL != "" {
				t.Fatal(out, e)
			}
			e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				var count int
				var status string
				if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid`, f.org).Scan(&count); e != nil {
					return e
				}
				if count != 0 {
					t.Fatal("unauthorized task created")
				}
				if e := tx.QueryRow(ctx, `SELECT status FROM openfox.integration_bugly_bindings WHERE id=$1::uuid`, b.ID).Scan(&status); e != nil {
					return e
				}
				if status != "NEEDS_ATTENTION" {
					t.Fatal(status)
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
	f := newBuglyDBFixture(t)
	c, _, token := f.configured()
	out, e := f.receive(c, token, []byte(`{"duplicate":1,"duplicate":2}`))
	if e != nil || out.Code == 0 {
		t.Fatal(out, e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var detail string
		return tx.QueryRow(ctx, `SELECT detail FROM openfox.integration_webhook_deliveries WHERE connection_id=$1::uuid`, c.ID).Scan(&detail)
	})
	if e != nil {
		t.Fatal("failure not committed", e)
	}
}

func TestBuglyTriageKeepsBoundSpace(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, b, token := f.configured()
	target := refsTestID(t)
	f.admin(`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES($1::uuid,$2::uuid,'OTHER','Other','SHARED')`, f.org, target)
	f.admin(`INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES($1::uuid,'Triage','TRIAGE',3)`, f.org)
	f.admin(`INSERT INTO openfox.workflow_statuses(organization_id,space_id,task_type_id,status_id,position) SELECT t.organization_id,t.space_id,t.id,s.id,3 FROM openfox.task_types t JOIN openfox.task_statuses s ON t.organization_id=s.organization_id AND s.category='TRIAGE' WHERE t.organization_id=$1::uuid AND t.id=$2::uuid`, f.org, b.TaskTypeID)
	f.admin(`UPDATE openfox.spaces SET triage_enabled=true,triage_action='none' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.space)
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := (collaboration.Service{}).CreateTriageRule(ctx, tx, f.org, f.space, f.actor, collaboration.TriageRuleInput{Name: "Integration priority and attempted move", Conditions: []collaboration.TriageConditionGroup{{Conditions: []collaboration.TriageCondition{{Field: "source", Op: "is", Values: []string{"integration"}}}}}, Actions: []collaboration.TriageAction{{Type: "move_space", Value: target}, {Type: "set_priority", Value: "LOW"}}})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.receive(c, token, buglyBody("triage", "Triage source"))
	if e != nil || out.Code != 0 || !strings.Contains(out.URL, f.space) {
		t.Fatal(out, e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var space, mapped, priority, source, category string
		e := tx.QueryRow(ctx, `SELECT t.space_id::text,src.task_space_id::text,t.priority,t.triage_source,st.category FROM openfox.integration_bugly_sources src JOIN openfox.tasks t ON t.organization_id=src.organization_id AND t.id=src.task_id JOIN openfox.task_statuses st ON st.organization_id=t.organization_id AND st.id=t.status_id WHERE src.organization_id=$1::uuid AND src.binding_id=$2::uuid`, f.org, b.ID).Scan(&space, &mapped, &priority, &source, &category)
		if e == nil && (space != f.space || mapped != f.space || priority != "LOW" || source != "integration" || category != "TRIAGE") {
			t.Fatal(space, mapped, priority, source, category)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestBuglyCreationRollbackAndRebinding(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, b, token := f.configured()
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ManageBuglyBinding(ctx, tx, f.org, f.actor, c.ID, b.ID, "", BuglyBindingUpdate{Version: b.Version, TaskTypeID: b.TaskTypeID, PriorityMapping: map[string]string{"urgent": "LOWEST"}})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.receive(c, token, buglyBody("retry-after-fix", "Requires fifth priority"))
	if e != nil || out.Code == 0 {
		t.Fatal(out, e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var tasks, sources int
		if e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid),(SELECT count(*) FROM openfox.integration_bugly_sources WHERE organization_id=$1::uuid)`, f.org).Scan(&tasks, &sources); e != nil {
			return e
		}
		if tasks != 0 || sources != 0 {
			t.Fatal("partial commit", tasks, sources)
		}
		var e error
		b, e = f.service.ManageBuglyBinding(ctx, tx, f.org, f.actor, c.ID, b.ID, "", BuglyBindingUpdate{Version: b.Version, TaskTypeID: b.TaskTypeID, PriorityMapping: map[string]string{"urgent": "HIGH"}})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	first, e := f.receive(c, token, buglyBody("retry-after-fix", "Created after explicit fix"))
	if e != nil || first.Code != 0 {
		t.Fatal(first, e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := f.service.ManageBuglyBinding(ctx, tx, f.org, f.actor, c.ID, b.ID, "remove", BuglyBindingUpdate{Version: b.Version}); e != nil {
			return e
		}
		_, e := f.service.CreateBuglyBinding(ctx, tx, f.org, f.actor, c.ID, BuglyBindingInput{ConnectionVersion: c.Version, ProductID: b.ProductID, SpaceID: f.space, TaskTypeID: b.TaskTypeID})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.receive(c, token, buglyBody("retry-after-fix", "Explicit new binding"))
	if e != nil || second.Code != 0 || second.URL == first.URL {
		t.Fatal(second, e)
	}
}

func TestBuglySchemaIsolationAndAtomicAbort(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, b, token := f.configured()
	other := newBuglyDBFixture(t)
	aborted := errors.New("simulated caller transaction abort")
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		out, e := f.service.ReceiveBugly(ctx, tx, GitWebhookLookup{OrganizationID: f.org, ConnectionID: c.ID}, token, buglyBody("abort", "Uncommitted"))
		if e != nil {
			return e
		}
		if out.Code != 0 {
			t.Fatal(out)
		}
		return aborted
	})
	if !errors.Is(e, aborted) {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var tasks, sources, receipts int
		e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid),(SELECT count(*) FROM openfox.integration_bugly_sources WHERE organization_id=$1::uuid),(SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE organization_id=$1::uuid)`, f.org).Scan(&tasks, &sources, &receipts)
		if e == nil && (tasks != 0 || sources != 0 || receipts != 0) {
			t.Fatal("aborted transaction persisted", tasks, sources, receipts)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e := f.receive(c, token, buglyBody("abort", "Committed retry"))
	if e != nil || out.Code != 0 {
		t.Fatal(out, e)
	}
	e = other.run(other.org, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range []string{"integration_bugly_bindings", "integration_bugly_sources"} {
			var count int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.`+table+` WHERE organization_id=$1::uuid`, f.org).Scan(&count); e != nil {
				return e
			}
			if count != 0 {
				t.Fatal("cross tenant RLS leak", table)
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_bugly_bindings SET project_name='cross tenant' WHERE id=$1::uuid`, b.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM openfox.integration_bugly_bindings WHERE id=$1::uuid`, b.ID)
		return e
	})
	var pe *pgconn.PgError
	if !errors.As(e, &pe) || pe.Code != "42501" {
		t.Fatal("app can delete provenance", e)
	}
	oc, ob, _ := other.configured()
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.integration_bugly_sources(organization_id,connection_id,binding_id,product_id,issue_key,issue_type,task_space_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'cross-org','crash',$5::uuid)`, f.org, oc.ID, ob.ID, ob.ProductID, f.space)
		return e
	})
	if !errors.As(e, &pe) || pe.Code != "23503" {
		t.Fatal("cross tenant foreign key accepted", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='openfox' AND c.relname IN('integration_bugly_bindings','integration_bugly_sources') AND c.relrowsecurity AND c.relforcerowsecurity`).Scan(&count); e != nil {
			return e
		}
		if count != 2 {
			t.Fatal("RLS not forced")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestBuglyArchivedBindingCanStopButNotResume(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, b, token := f.configured()
	f.admin(`UPDATE openfox.spaces SET status='ARCHIVED',retired_at=clock_timestamp(),retired_by=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, f.space, f.user)
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ManageBuglyBinding(ctx, tx, f.org, f.actor, c.ID, b.ID, "pause", BuglyBindingUpdate{Version: b.Version})
		return e
	})
	if e != nil || b.Status != "PAUSED" {
		t.Fatal(b, e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageBuglyBinding(ctx, tx, f.org, f.actor, c.ID, b.ID, "resume", BuglyBindingUpdate{Version: b.Version})
		return e
	})
	if !errors.Is(e, collaboration.ErrSpaceRetired) {
		t.Fatal("archived space resumed", e)
	}
	out, e := f.receive(c, token, buglyBody("archived-paused", "Denied"))
	if e != nil || out.Code == 0 {
		t.Fatal(out, e)
	}
	member := refsTestID(t)
	f.admin(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, member, member+"@example.test")
	f.admin(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, f.org, member)
	f.admin(`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, f.org, f.space, member)
	e = f.runIdentity(f.org, member, tenancy.SourceUserRequest, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageBuglyBinding(ctx, tx, f.org, collaboration.Actor{UserID: member, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}, c.ID, b.ID, "remove", BuglyBindingUpdate{Version: b.Version})
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("space-only manager removed organization binding", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ManageBuglyBinding(ctx, tx, f.org, f.actor, c.ID, b.ID, "remove", BuglyBindingUpdate{Version: b.Version})
		return e
	})
	if e != nil || b.Status != "REMOVED" {
		t.Fatal(b, e)
	}
}

func TestBuglyTemporaryMemberLockDoesNotRevokeAuthority(t *testing.T) {
	f := newBuglyDBFixture(t)
	c, b, token := f.configured()
	guard, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer guard.Close(context.Background())
	held, e := guard.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer held.Rollback(context.Background())
	if _, e = held.Exec(context.Background(), `SELECT user_id FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, f.org, f.user); e != nil {
		t.Fatal(e)
	}
	var out BuglyReceipt
	e = f.runIdentity(f.org, "00000000-0000-0000-0000-000000000000", tenancy.SourceOrganizationWorker, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='30ms'`); e != nil {
			return e
		}
		var e error
		out, e = f.service.ReceiveBugly(ctx, tx, GitWebhookLookup{OrganizationID: f.org, ConnectionID: c.ID}, token, buglyBody("member-lock", "Retryable"))
		return e
	})
	_ = held.Rollback(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if out.HTTPStatus != 503 || out.Code == 0 {
		t.Fatalf("temporary lock treated as permanent authority loss: %+v", out)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		current, e := loadBuglyBinding(ctx, tx, c.ID, b.ID)
		if e == nil && current.Status != "ACTIVE" {
			t.Fatal("temporary failure paused binding", current.Status)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e = f.receive(c, token, buglyBody("member-lock", "Retryable"))
	if e != nil || out.Code != 0 {
		t.Fatal(out, e)
	}
}

func newBuglyDBFixture(t *testing.T) *buglyDBFixture {
	t.Helper()
	dsn := os.Getenv("OPENFOX_I07_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OPENFOX_I07_TEST_DATABASE_URL is not configured")
	}
	f := &buglyDBFixture{t: t, dsn: dsn, org: refsTestID(t), user: refsTestID(t), space: refsTestID(t)}
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var db string
	if err = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&db); err != nil || !strings.HasPrefix(db, "openfox_i07_") {
		t.Fatal("requires exclusive I07 database", db, err)
	}
	for _, q := range []string{
		`INSERT INTO openfox.users(id,email) VALUES('` + f.user + `','` + f.user + `@example.test')`,
		`INSERT INTO openfox.organizations(id,slug,name) VALUES('` + f.org + `','` + f.org + `','I07 fixture')`,
		`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES('` + f.org + `','` + f.user + `','OWNER')`,
		`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES('` + f.org + `','` + f.space + `','BUGLY','I07','PRIVATE')`,
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
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test"}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	return f
}
func (f *buglyDBFixture) run(org string, fn func(context.Context, pgx.Tx) error) error {
	return f.runIdentity(org, f.user, tenancy.SourceUserRequest, fn)
}
func (f *buglyDBFixture) runIdentity(org, user string, source tenancy.Source, fn func(context.Context, pgx.Tx) error) error {
	tenant, err := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: 1}, source)
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
	if _, err = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,$3)`, org, user, string(source)); err != nil {
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
