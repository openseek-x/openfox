package wp13

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
	"openfox/internal/reconciliation"
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
	caseA         = "00000000-0000-0000-0000-0000000000c2"
	runningFact   = "00000000-0000-0000-0000-0000000000e1"
	exitFact      = "00000000-0000-0000-0000-0000000000e2"
)

func TestPostgresReconciliationRequiresExitEvidenceAndPreservesTenantBoundary(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed(t, ctx, url)
	p, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	manifest := reconciliation.Manifest{
		OrganizationID: orgA, RunID: runA, DeviceID: deviceA, Generation: 1,
		DeviceGeneration: 1, CenterEpoch: 1,
		SupervisorNonce: "supervisor-nonce-wp13-0001", ProcessPID: 1234,
		ProcessStartedAt: now, LastEventSequence: 2,
	}
	deviceSelection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: deviceA, AuthorizationVersion: 1}
	deviceSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSelection, nil })
	store := reconciliation.PGStore{}

	var fact reconciliation.SupervisorFact
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		fact, err = store.UpsertFact(ctx, tx, reconciliation.FactRequest{Manifest: manifest, ProcessState: reconciliation.ProcessRunning, ObservedAt: now})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if fact.Version != 1 || fact.ProcessState != reconciliation.ProcessRunning {
		t.Fatalf("initial fact = %+v", fact)
	}

	var item reconciliation.Case
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = store.OpenCase(ctx, tx, reconciliation.OpenRequest{
			OrganizationID: orgA, RunID: runA, Generation: 1,
			ReasonCode: reconciliation.ReasonUnknownProcess, FailureCode: reconciliation.FailureUnknownProcess,
			SafeDetail: "heartbeat lease expired", Manifest: manifest, Now: now,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if item.Status != reconciliation.CaseOpen || item.ID == "" {
		t.Fatalf("open case = %+v", item)
	}

	running := reconciliation.Evidence{
		OrganizationID: orgA, ID: runningFact, CaseID: item.ID, RunID: runA, Generation: 1,
		DeviceGeneration: 1, CenterEpoch: 1,
		Kind: reconciliation.EvidenceProcessRunning, Source: reconciliation.SourceDaemon, DeviceID: deviceA,
		SupervisorNonce: manifest.SupervisorNonce, ProcessPID: manifest.ProcessPID,
		ProcessStartedAt: manifest.ProcessStartedAt, ProcessState: reconciliation.ProcessRunning,
		PayloadHash: hash("running"), ObservedAt: now,
	}
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.SubmitEvidence(ctx, tx, running)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ownerSelection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: ownerA, AuthorizationVersion: 1}
	ownerSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return ownerSelection, nil })
	err = p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.ResolveCase(ctx, tx, reconciliation.ResolveRequest{
			OrganizationID: orgA, CaseID: item.ID, RunID: runA, ExpectedVersion: item.Version,
			Resolution: reconciliation.ResolutionProcessExitedFailed, EvidenceID: runningFact, ActorID: ownerA, Now: now,
		})
		return err
	})
	if !errors.Is(err, reconciliation.ErrEvidenceNotAccepted) {
		t.Fatalf("live-process resolution error = %v", err)
	}

	exitCode := 1
	exited := running
	exited.ID = exitFact
	exited.Kind = reconciliation.EvidenceProcessExited
	exited.ProcessState = reconciliation.ProcessExited
	exited.ProcessExited = true
	exited.ExitCode = &exitCode
	exited.PayloadHash = hash("exited")
	exited.ObservedAt = now.Add(time.Second)
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.SubmitEvidence(ctx, tx, exited)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, ownerSelector, func(ctx context.Context, tx pgx.Tx) error {
		resolved, err := store.ResolveCase(ctx, tx, reconciliation.ResolveRequest{
			OrganizationID: orgA, CaseID: item.ID, RunID: runA, ExpectedVersion: item.Version,
			Resolution: reconciliation.ResolutionProcessExitedFailed, EvidenceID: exitFact, ActorID: ownerA,
			SafeDetail: "process exit confirmed", Now: now.Add(2 * time.Second),
		})
		if err != nil {
			return err
		}
		if resolved.Status != reconciliation.CaseResolved {
			t.Fatalf("resolved case = %+v", resolved)
		}
		var state string
		var reconciliationRequired bool
		if err := tx.QueryRow(ctx, `SELECT state, reconciliation_required FROM openfox.runs WHERE id = $1::uuid`, runA).Scan(&state, &reconciliationRequired); err != nil {
			return err
		}
		if state != "FAILED" || reconciliationRequired {
			t.Fatalf("run after resolution = state %s, reconciliation=%v", state, reconciliationRequired)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	orgBSelection := tenancy.OrganizationSelection{OrganizationID: orgB, ActorID: ownerB, AuthorizationVersion: 1}
	orgBSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return orgBSelection, nil })
	if err := p.RunForOrganization(ctx, orgB, tenancy.SourceUserRequest, orgBSelector, func(ctx context.Context, tx pgx.Tx) error {
		cases, err := store.ListCases(ctx, tx, orgB, runA)
		if err != nil {
			return err
		}
		if len(cases) != 0 {
			t.Fatalf("cross-tenant cases = %+v", cases)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
		{`UPDATE openfox.agents SET space_id = $2::uuid, owner_user_id = $3::uuid, name = 'WP13 agent', kind = 'CODE', visibility = 'PRIVATE', status = 'ACTIVE', archived_at = NULL, current_config_version = 1 WHERE organization_id = $1::uuid AND id = $4::uuid`, []any{orgA, spaceA, ownerA, agentA}},
		{`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'PRIVATE') ON CONFLICT DO NOTHING`, []any{orgA, conversationA, spaceA, agentA, ownerA}},
		{`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version, actor_id, state, generation) SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, g.id, 1, 1, $7::uuid, 'RUNNING', 1 FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $6::uuid ON CONFLICT DO NOTHING`, []any{orgA, runA, spaceA, conversationA, agentA, deviceA, ownerA}},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func hash(value string) string {
	result, _ := reconciliation.JournalPayloadHash(value)
	return result
}
