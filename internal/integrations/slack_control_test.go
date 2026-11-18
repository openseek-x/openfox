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

type slackPG struct {
	t              *testing.T
	dsn, org, user string
	service        Service
	actor          collaboration.Actor
}

func newSlackPG(t *testing.T) *slackPG {
	t.Helper()
	dsn := os.Getenv("OPENFOX_I15_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("exclusive I15 PostgreSQL not configured")
	}
	f := &slackPG{t: t, dsn: dsn, org: refsTestID(t), user: refsTestID(t)}
	c, e := pgx.Connect(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	var db string
	if e = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&db); e != nil || !strings.HasPrefix(db, "openfox_i15_") {
		t.Fatal("requires exclusive I15 database", db, e)
	}
	for _, q := range []string{
		`INSERT INTO openfox.users(id,email) VALUES('` + f.user + `','` + f.user + `@example.test')`,
		`INSERT INTO openfox.organizations(id,slug,name) VALUES('` + f.org + `','` + f.org + `','I15 fixture')`,
		`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id='` + f.org + `'`,
		`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES('` + f.org + `','` + f.user + `','OWNER')`,
	} {
		if _, e = c.Exec(context.Background(), q); e != nil {
			t.Fatal(e)
		}
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		t.Fatal(e)
	}
	box, e := secrets.New(key)
	if e != nil {
		t.Fatal(e)
	}
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test", SlackClientID: "123.456", SlackClientSecret: "local-client-secret", SlackSigningSecret: "local-signing-secret"}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	return f
}
func (f *slackPG) run(org string, fn func(context.Context, pgx.Tx) error) error {
	tenant, e := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: org, ActorID: f.user, AuthorizationVersion: 1}, tenancy.SourceUserRequest)
	if e != nil {
		return e
	}
	ctx := tenancy.WithTenantContext(context.Background(), tenant)
	c, e := pgx.Connect(ctx, f.dsn)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, org, f.user); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SET LOCAL ROLE openfox_app`); e != nil {
		return e
	}
	if e = fn(ctx, tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (f *slackPG) start(key string) (SlackOAuthSession, error) {
	var out SlackOAuthSession
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.StartSlackAuthorization(ctx, tx, f.org, f.actor, SlackOAuthInput{Purpose: "organization", IdempotencyKey: key})
		return e
	})
	return out, e
}
func TestSlackOAuthStartConcurrentIdempotencyPG(t *testing.T) {
	f := newSlackPG(t)
	const n = 6
	results := make(chan SlackOAuthSession, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); v, e := f.start("same-operation-123456"); results <- v; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first SlackOAuthSession
	for v := range results {
		if first.ID == "" {
			first = v
		}
		if v.ID != first.ID || v.AppID != first.AppID || v.AuthorizeURL != first.AuthorizeURL {
			t.Fatal("same operation created another app/session")
		}
	}
	u, e := url.Parse(first.AuthorizeURL)
	if e != nil || u.Scheme != "https" || u.Host != "slack.com" || u.Query().Get("redirect_uri") != "https://openfox.example.test/api/v1/integrations/slack/callback" {
		t.Fatal("invalid fixed authorization URL")
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.StartSlackAuthorization(ctx, tx, f.org, f.actor, SlackOAuthInput{Purpose: "personal", AppID: first.AppID, Version: 1, IdempotencyKey: "same-operation-123456"})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("changed parameters: %v", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var apps, sessions int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_apps`).Scan(&apps); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_oauth_sessions`).Scan(&sessions); e != nil {
			return e
		}
		if apps != 1 || sessions != 1 {
			t.Fatalf("apps=%d sessions=%d", apps, sessions)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestSlackRoutingControlRoleAndTenantRLSPG(t *testing.T) {
	f := newSlackPG(t)
	session, e := f.start("routing-operation-12345")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(session.AuthorizeURL)
	hash := TokenHash(u.Query().Get("state"))
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	tx, e := c.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(context.Background(), `SET LOCAL ROLE openfox_context`); e != nil {
		t.Fatal(e)
	}
	var org, id string
	if e = tx.QueryRow(context.Background(), `SELECT organization_id::text FROM openfox_private.slack_app_route($1::uuid)`, session.AppID).Scan(&org); e != nil || org != f.org {
		t.Fatal("context app route", org, e)
	}
	if e = tx.QueryRow(context.Background(), `SELECT organization_id::text,session_id::text FROM openfox_private.slack_oauth_route($1)`, hash).Scan(&org, &id); e != nil || org != f.org || id != session.ID {
		t.Fatal("context OAuth route", e)
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	other := refsTestID(t)
	e = f.run(other, func(ctx context.Context, tx pgx.Tx) error {
		for _, table := range []string{"slack_apps", "slack_identities", "slack_oauth_sessions", "slack_channels", "slack_routes", "slack_events", "slack_deliveries", "slack_rate_limits"} {
			var n int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.`+table).Scan(&n); e != nil {
				return e
			}
			if n != 0 {
				t.Fatalf("tenant leak %s: %d", table, n)
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET name='changed' WHERE id=$1::uuid`, session.AppID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `SELECT * FROM openfox_private.slack_deployment_verifications`)
		return e
	})
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "42501" {
		t.Fatalf("tenant app role read deployment verification: %v", e)
	}
}
func TestSlackMissingConfigAndStaleActorPG(t *testing.T) {
	f := newSlackPG(t)
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		s := f.service
		s.SlackSigningSecret = ""
		_, e := s.StartSlackAuthorization(ctx, tx, f.org, f.actor, SlackOAuthInput{Purpose: "organization", IdempotencyKey: "missing-config-operation"})
		return e
	})
	if !errors.Is(e, ErrNotConfigured) {
		t.Fatal(e)
	}
	f.actor.AuthorizationVersion = 2
	_, e = f.start("stale-actor-operation")
	if !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
}
