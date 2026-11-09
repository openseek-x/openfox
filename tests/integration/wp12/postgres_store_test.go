package wp12

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/executiondelivery"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
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
	eventA        = "00000000-0000-0000-0000-0000000000e1"
	eventB        = "00000000-0000-0000-0000-0000000000e2"
	eventStep     = "00000000-0000-0000-0000-0000000000e3"
	eventBadStep  = "00000000-0000-0000-0000-0000000000e4"
	resultA       = "00000000-0000-0000-0000-0000000000f1"
	artifactA     = "00000000-0000-0000-0000-000000000101"
)

func TestPostgresEventResultArtifactDelivery(t *testing.T) {
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
	deviceSelection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: deviceA, AuthorizationVersion: 1}
	deviceSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSelection, nil })
	principal := deviceauth.Principal{DeviceID: deviceA}

	appendEvent := func(input executiondelivery.EventInput) executiondelivery.EventBatchResult {
		t.Helper()
		var result executiondelivery.EventBatchResult
		if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			result, err = (executiondelivery.Service{Pool: p}).AppendEvents(ctx, tx, orgA, principal, runA, []executiondelivery.EventInput{input})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := appendEvent(executiondelivery.EventInput{ID: eventB, Generation: 1, Sequence: 2, Type: "run.output", Payload: json.RawMessage(`{"text":"second"}`)})
	if first.HighestContiguous != 0 {
		t.Fatalf("out-of-order ACK = %d, want 0", first.HighestContiguous)
	}
	second := appendEvent(executiondelivery.EventInput{ID: eventA, Generation: 1, Sequence: 1, Type: "run.started", Payload: json.RawMessage(`{"text":"first"}`)})
	if second.HighestContiguous != 2 {
		t.Fatalf("contiguous ACK = %d, want 2", second.HighestContiguous)
	}
	duplicate := appendEvent(executiondelivery.EventInput{ID: eventA, Generation: 1, Sequence: 1, Type: "run.started", Payload: json.RawMessage(`{"text":"first"}`)})
	if len(duplicate.Events) != 1 || duplicate.Events[0].ID != eventA {
		t.Fatalf("idempotent event = %+v", duplicate)
	}
	// 合法的执行步骤事件被接受；不合法的 step 使整批 400，且不写入任何事件。
	appendEvent(executiondelivery.EventInput{ID: eventStep, Generation: 1, Sequence: 3, Type: "step",
		Payload: json.RawMessage(`{"data":{"schema":1,"step":{"id":"cmd-1","kind":"exec","state":"done","command":"ls","output":"a"}}}`)})
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{Pool: p}).AppendEvents(ctx, tx, orgA, principal, runA, []executiondelivery.EventInput{
			{ID: eventBadStep, Generation: 1, Sequence: 4, Type: "run.output", Payload: json.RawMessage(`{"text":"ok"}`)},
			{ID: eventBadStep, Generation: 1, Sequence: 5, Type: "step", Payload: json.RawMessage(`{"data":{"schema":1,"step":{"id":"x","kind":"shell","state":"done"}}}`)},
		})
		return err
	}); !errors.Is(err, executiondelivery.ErrInvalidRequest) {
		t.Fatalf("invalid step batch error = %v", err)
	}

	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		// AddArtifact 的 generation 参数要求 Run 未结束且代次一致；created 返回是否新建。
		_, _, err := (executiondelivery.Service{Pool: p}).AddArtifact(ctx, tx, orgA, principal, runA, 1, artifactA, "report.txt", orgA+"/"+runA+"/"+artifactA, "text/plain", artifacts.Stored{Size: 5, SHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var result executiondelivery.Result
	usageA := executiondelivery.Usage{Input: 1200, CachedInput: 300, CacheWriteInput: 10, Output: 450, ReasoningOutput: 120}
	// 负数用量 400，不产生结果。
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{Pool: p}).AppendResult(ctx, tx, orgA, principal, runA, executiondelivery.ResultInput{
			ID: resultA, Generation: 1, Status: "SUCCEEDED", Checks: json.RawMessage(`[]`), Usage: &executiondelivery.Usage{Input: -1},
		})
		return err
	}); !errors.Is(err, executiondelivery.ErrInvalidRequest) {
		t.Fatalf("negative usage error = %v", err)
	}
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (executiondelivery.Service{Pool: p}).AppendResult(ctx, tx, orgA, principal, runA, executiondelivery.ResultInput{
			ID: resultA, Generation: 1, Status: "SUCCEEDED", Summary: "done", Checks: json.RawMessage(`[]`), ArtifactIDs: []string{artifactA},
			Usage: &usageA,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if result.Status != "SUCCEEDED" || result.Usage == nil || *result.Usage != usageA {
		t.Fatalf("result = %+v", result)
	}
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		duplicate, err := (executiondelivery.Service{Pool: p}).AppendResult(ctx, tx, orgA, principal, runA, executiondelivery.ResultInput{
			ID: resultA, Generation: 1, Status: "SUCCEEDED", Summary: "done", Checks: json.RawMessage(`[]`), ArtifactIDs: []string{artifactA},
			Usage: &usageA,
		})
		if err != nil {
			return err
		}
		if duplicate.ID != resultA || duplicate.Usage == nil || *duplicate.Usage != usageA {
			t.Fatalf("idempotent result = %+v", duplicate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	userSelection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: ownerA, AuthorizationVersion: 1}
	userSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return userSelection, nil })
	actor := conversations.Actor{UserID: ownerA, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	if err := p.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, userSelector, func(ctx context.Context, tx pgx.Tx) error {
		service := executiondelivery.Service{Pool: p}
		run, err := service.GetRun(ctx, tx, orgA, runA, actor)
		if err != nil || run.State != "SUCCEEDED" {
			t.Fatalf("visible run = %+v, err=%v", run, err)
		}
		page, err := service.ListRunEvents(ctx, tx, orgA, runA, actor, 0, 10)
		if err != nil || len(page.Items) != 4 {
			t.Fatalf("visible events = %+v, err=%v", page, err)
		}
		stored, err := service.GetResult(ctx, tx, orgA, runA, actor)
		if err != nil || stored.Usage == nil || *stored.Usage != usageA {
			t.Fatalf("stored result usage = %+v, err=%v", stored.Usage, err)
		}
		details, err := service.RunDetails(ctx, tx, []string{runA, "not-a-uuid", "00000000-0000-0000-0000-0000000000d9"})
		if err != nil || len(details) != 1 || details[runA].Usage == nil || *details[runA].Usage != usageA || details[runA].FailureCode != "" {
			t.Fatalf("run details = %+v, err=%v", details, err)
		}
		items, err := service.ListArtifacts(ctx, tx, orgA, runA, actor)
		if err != nil || len(items) != 1 || items[0].ID != artifactA {
			t.Fatalf("visible artifacts = %+v, err=%v", items, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	orgBSelection := tenancy.OrganizationSelection{OrganizationID: orgB, ActorID: ownerB, AuthorizationVersion: 1}
	orgBSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return orgBSelection, nil })
	if err := p.RunForOrganization(ctx, orgB, tenancy.SourceUserRequest, orgBSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{Pool: p}).GetRun(ctx, tx, orgB, runA, conversations.Actor{UserID: ownerB, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1})
		if !errors.Is(err, executiondelivery.ErrNotFound) {
			t.Fatalf("cross-tenant run error = %v", err)
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
		{`UPDATE openfox.agents SET space_id = $2::uuid, owner_user_id = $3::uuid, name = 'WP12 agent', kind = 'CODE', visibility = 'PRIVATE', status = 'ACTIVE', archived_at = NULL, current_config_version = 1 WHERE organization_id = $1::uuid AND id = $4::uuid`, []any{orgA, spaceA, ownerA, agentA}},
		{`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'PRIVATE') ON CONFLICT DO NOTHING`, []any{orgA, conversationA, spaceA, agentA, ownerA}},
		{`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version, actor_id, state, generation) SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, g.id, 1, 1, $7::uuid, 'RUNNING', 1 FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $6::uuid ON CONFLICT DO NOTHING`, []any{orgA, runA, spaceA, conversationA, agentA, deviceA, ownerA}},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
