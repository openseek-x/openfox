package wp14

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/platform/postgres"
	"openfox/internal/resourcepolicy"
	"openfox/internal/tenancy"
)

const (
	orgA          = "00000000-0000-0000-0000-0000000000a1"
	orgB          = "00000000-0000-0000-0000-0000000000b1"
	ownerA        = "00000000-0000-0000-0000-000000000001"
	ownerB        = "00000000-0000-0000-0000-000000000002"
	spaceA        = "00000000-0000-0000-0000-0000000000aa"
	agentA        = "00000000-0000-0000-0000-0000000000a2"
	deviceA       = "00000000-0000-0000-0000-000000000011"
	conversationA = "00000000-0000-0000-0000-0000000000c1"
	runA          = "00000000-0000-0000-0000-0000000000d1"
	resultA       = "00000000-0000-0000-0000-0000000000f1"
	artifactA     = "00000000-0000-0000-0000-000000000101"
	previewA      = "00000000-0000-0000-0000-000000000201"
	reservationA  = "00000000-0000-0000-0000-000000000301"
	reservationB  = "00000000-0000-0000-0000-000000000302"
)

func TestPostgresRuntimePolicyCleanupAndTenantIsolation(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed(t, ctx, url)

	p, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ownerASelector := selector(orgA, ownerA)
	ownerBSelector := selector(orgB, ownerB)
	store := resourcepolicy.Store{}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	var config resourcepolicy.Config
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = store.GetConfig(ctx, tx, orgA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if config.ConfigVersion != 1 || config.Policy != resourcepolicy.DefaultPolicy() {
		t.Fatalf("default config = %+v", config)
	}

	updatedPolicy := config.Policy
	updatedPolicy.ActiveMax = 1
	updatedPolicy.ArtifactRetentionDays = 1
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = store.UpdateConfig(ctx, tx, orgA, resourcepolicy.ConfigUpdate{
			ExpectedVersion: config.ConfigVersion, Policy: updatedPolicy, RestartRequired: true,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if config.ConfigVersion != 2 || config.Policy.ActiveMax != 1 || !config.RestartRequired {
		t.Fatalf("updated config = %+v", config)
	}

	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.UpdateConfig(ctx, tx, orgA, resourcepolicy.ConfigUpdate{
			ExpectedVersion: 1, Policy: updatedPolicy,
		})
		return err
	}); !errors.Is(err, resourcepolicy.ErrConfigConflict) {
		t.Fatalf("stale config update error = %v", err)
	}

	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = store.AcknowledgeConfig(ctx, tx, orgA, resourcepolicy.ConfigAck{
			AppliedVersion: config.ConfigVersion, RestartRequired: false,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if config.AppliedVersion != config.ConfigVersion || config.RestartRequired {
		t.Fatalf("acknowledged config = %+v", config)
	}

	var snapshot resourcepolicy.RunSnapshot
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		snapshot, err = store.SnapshotRunPolicy(ctx, tx, orgA, runA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigVersion != config.ConfigVersion || snapshot.ActiveMax != 1 {
		t.Fatalf("run snapshot = %+v", snapshot)
	}

	var preview resourcepolicy.CleanupPreview
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		preview, err = store.CreateCleanupPreview(ctx, tx, orgA, ownerA, previewA, now, time.Hour, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if preview.CandidateCount != 1 || preview.CandidateBytes != 5 {
		t.Fatalf("cleanup preview = %+v", preview)
	}

	var items []resourcepolicy.CleanupItem
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = store.ConfirmCleanupPreview(ctx, tx, orgA, previewA, ownerA, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ArtifactID != artifactA || items[0].Status != "READY" {
		t.Fatalf("confirmed cleanup items = %+v", items)
	}
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerASelector, func(ctx context.Context, tx pgx.Tx) error {
		return store.MarkCleanupItemDeleted(ctx, tx, orgA, previewA, items[0].ID, items[0].ExpectedVersion)
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.RunForOrganization(ctx, orgB, tenancy.SourceUserRequest, ownerBSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.GetConfig(ctx, tx, orgA)
		return err
	}); err == nil {
		t.Fatal("cross-tenant runtime config read succeeded")
	}

	assertActiveLimit(t, ctx, url)
}

func assertActiveLimit(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	insert := `INSERT INTO openfox.capacity_reservations
		(organization_id, id, run_id, generation, device_id, agent_id, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 1, $4::uuid, $5::uuid, 'RESERVED')`
	first, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Exec(ctx, "SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')", orgA, ownerA); err != nil {
		_ = first.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := first.Exec(ctx, insert, orgA, reservationA, "00000000-0000-0000-0000-0000000003a1", deviceA, agentA); err != nil {
		_ = first.Rollback(ctx)
		t.Fatal(err)
	}
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Rollback(ctx)
	if _, err := second.Exec(ctx, "SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'user_request')", orgA, ownerA); err != nil {
		t.Fatal(err)
	}
	_, err = second.Exec(ctx, insert, orgA, reservationB, "00000000-0000-0000-0000-0000000003a2", deviceA, agentA)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("second active reservation error = %v, want SQLSTATE 55000", err)
	}
}

func seed(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE openfox.agents SET space_id = $2::uuid, owner_user_id = $3::uuid, name = 'WP14 agent', kind = 'CODE', visibility = 'PRIVATE', status = 'ACTIVE', archived_at = NULL, current_config_version = 1 WHERE organization_id = $1::uuid AND id = $4::uuid`, []any{orgA, spaceA, ownerA, agentA}},
		{`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'PRIVATE') ON CONFLICT DO NOTHING`, []any{orgA, conversationA, spaceA, agentA, ownerA}},
		{`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version, actor_id, state, generation, finished_at, created_at, updated_at) SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, g.id, 1, 1, $7::uuid, 'SUCCEEDED', 1, $8::timestamptz, $8::timestamptz - interval '2 days', $8::timestamptz FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $6::uuid ON CONFLICT DO NOTHING`, []any{orgA, runA, spaceA, conversationA, agentA, deviceA, ownerA, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}},
		{`INSERT INTO openfox.run_results (organization_id, id, run_id, generation, status, summary, payload_hash) VALUES ($1::uuid, $2::uuid, $3::uuid, 1, 'SUCCEEDED', 'WP14 result', repeat('a', 64)) ON CONFLICT DO NOTHING`, []any{orgA, resultA, runA}},
		{`INSERT INTO openfox.artifacts (organization_id, id, run_id, result_id, name, storage_key, size_bytes, sha256, mime_type, state, retention_until, created_at) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'old.txt', 'org-a/old.txt', 5, repeat('b', 64), 'text/plain', 'AVAILABLE', $5::timestamptz, $5::timestamptz - interval '2 days') ON CONFLICT DO NOTHING`, []any{orgA, artifactA, runA, resultA, time.Date(2026, 10, 25, 11, 0, 0, 0, time.UTC)}},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func selector(organizationID, actorID string) tenancy.OrganizationSelector {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: actorID, AuthorizationVersion: 1}
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}
