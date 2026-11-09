// Package stage10d 是会话产物的 PostgreSQL 集成测试：上传校验（Run 已结束、代次不符、去重、数量上限）、
// 组织事件 run.artifact、会话产物列表的可见性（跨组织、同组织他人的私有会话）。用 run.sh 自建临时集群运行。
package stage10d

import (
	"context"
	"errors"
	"fmt"
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
	orgA    = "00000000-0000-0000-0000-0000000000a1"
	orgB    = "00000000-0000-0000-0000-0000000000b1"
	ownerA  = "00000000-0000-0000-0000-000000000001"
	ownerB  = "00000000-0000-0000-0000-000000000002"
	memberA = "00000000-0000-0000-0000-000000000003"
	spaceA  = "00000000-0000-0000-0000-0000000000aa"
	agentA  = "00000000-0000-0000-0000-0000000000a2"
	deviceA = "00000000-0000-0000-0000-000000000011"
	deviceC = "00000000-0000-0000-0000-000000000022"
	// 与 wp12 不同的 ID，两个套件共用一个库。
	conversationA = "00000000-0000-0000-0000-00000000c10d"
	runLive       = "00000000-0000-0000-0000-00000000d10a"
	runDone       = "00000000-0000-0000-0000-00000000d10b"
)

func artifactID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-0000000a%04d", n) }

func sha(n int) string { return fmt.Sprintf("%064x", n) }

func TestStage10dArtifacts(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seed(t, ctx, url)
	p, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	service := executiondelivery.Service{Pool: p}
	device := func(fn func(ctx context.Context, tx pgx.Tx) error) error {
		selection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: deviceA, AuthorizationVersion: 1}
		return p.RunForOrganization(ctx, orgA, tenancy.SourceDeviceRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }), fn)
	}
	principal := deviceauth.Principal{DeviceID: deviceA}
	add := func(run string, generation int64, n int, digest string, size int64) (executiondelivery.Artifact, bool, error) {
		var artifact executiondelivery.Artifact
		var created bool
		err := device(func(ctx context.Context, tx pgx.Tx) error {
			var err error
			artifact, created, err = service.AddArtifact(ctx, tx, orgA, principal, run, generation, artifactID(n), "报告‮.md",
				orgA+"/"+run+"/"+artifactID(n), "text/markdown; charset=utf-8", artifacts.Stored{Size: size, SHA256: digest}, nil)
			return err
		})
		return artifact, created, err
	}

	// 1. 正常登记：文件名清洗（去掉 U+202E），MIME 规范化，发组织事件 run.artifact。
	first, created, err := add(runLive, 1, 1, sha(1), 10)
	if err != nil || !created || first.Name != "报告_.md" || first.MIMEType != "text/markdown" || first.State != "AVAILABLE" {
		t.Fatalf("first = %+v created=%v err=%v", first, created, err)
	}
	// 2. 同一 Run 内容相同（sha256）：返回已有产物，created=false，不再发事件。
	again, created, err := add(runLive, 1, 2, sha(1), 10)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("dedup = %+v created=%v err=%v", again, created, err)
	}
	// 3. 代次不符、非执行电脑、已结束的 Run 一律拒绝；上传前检查同样拒绝。
	if _, _, err := add(runLive, 2, 3, sha(3), 10); !errors.Is(err, executiondelivery.ErrGenerationConflict) {
		t.Fatalf("stale generation = %v", err)
	}
	if _, _, err := add(runDone, 1, 4, sha(4), 10); !errors.Is(err, executiondelivery.ErrResultConflict) {
		t.Fatalf("finished run = %v", err)
	}
	if err := device(func(ctx context.Context, tx pgx.Tx) error {
		return service.CheckArtifactUpload(ctx, tx, principal, runDone, 1, artifactID(5))
	}); !errors.Is(err, executiondelivery.ErrResultConflict) {
		t.Fatalf("check finished run = %v", err)
	}
	if err := device(func(ctx context.Context, tx pgx.Tx) error {
		return service.CheckArtifactUpload(ctx, tx, principal, runLive, 2, artifactID(5))
	}); !errors.Is(err, executiondelivery.ErrGenerationConflict) {
		t.Fatalf("check stale generation = %v", err)
	}
	if err := device(func(ctx context.Context, tx pgx.Tx) error {
		return service.CheckArtifactUpload(ctx, tx, principal, runLive, 1, first.ID)
	}); !errors.Is(err, executiondelivery.ErrResultConflict) {
		t.Fatalf("check reused id = %v", err)
	}
	if err := device(func(ctx context.Context, tx pgx.Tx) error {
		return service.CheckArtifactUpload(ctx, tx, deviceauth.Principal{DeviceID: deviceC}, runLive, 1, artifactID(5))
	}); !errors.Is(err, executiondelivery.ErrForbidden) {
		t.Fatalf("check other device = %v", err)
	}
	// 4. 超过 20 MB 绝对上限：400（HTTP 层在读请求体时已按 min(组织策略, 20 MB) 413，这里是服务层兜底）。
	if _, _, err := add(runLive, 1, 6, sha(6), artifacts.MaxArtifactBytes+1); !errors.Is(err, executiondelivery.ErrInvalidRequest) {
		t.Fatalf("too large = %v", err)
	}
	// 5. 每次 Run 最多 10 个：再登记 9 个不同内容后，第 11 个 413。
	for n := 10; n < 19; n++ {
		if _, created, err := add(runLive, 1, n, sha(n), 10); err != nil || !created {
			t.Fatalf("artifact %d: created=%v err=%v", n, created, err)
		}
	}
	if _, _, err := add(runLive, 1, 30, sha(30), 10); !errors.Is(err, executiondelivery.ErrResourceLimit) {
		t.Fatalf("11th artifact = %v", err)
	}

	// 组织事件：每个新登记的产物一条（去重不发），带会话与空间，payload 含 run_id。
	var events int
	var payloadRun string
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if err := conn.QueryRow(ctx, `
		SELECT count(*), min(payload->>'run_id') FROM openfox.organization_events
		 WHERE organization_id = $1::uuid AND type = 'run.artifact' AND conversation_id = $2::uuid AND space_id = $3::uuid AND entity = 'artifact'`,
		orgA, conversationA, spaceA).Scan(&events, &payloadRun); err != nil {
		t.Fatal(err)
	}
	if events != 10 || payloadRun != runLive {
		t.Fatalf("run.artifact events = %d run=%s", events, payloadRun)
	}

	// 6. 会话产物列表：所有者能看到 10 个（created_at 倒序，带 run_id）；同组织其他成员看不到私有会话（404）；跨组织 404。
	user := func(org, userID string, role policy.OrganizationRole, fn func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error) error {
		selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: userID, AuthorizationVersion: 1}
		actor := conversations.Actor{UserID: userID, Role: role, AuthorizationVersion: 1}
		return p.RunForOrganization(ctx, org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
			func(ctx context.Context, tx pgx.Tx) error { return fn(ctx, tx, actor) })
	}
	if err := user(orgA, ownerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		items, err := service.ListConversationArtifacts(ctx, tx, orgA, conversationA, actor)
		if err != nil {
			return err
		}
		if len(items) != 10 || items[len(items)-1].ID != first.ID || items[0].RunID != runLive {
			t.Fatalf("owner list = %d items, last=%s", len(items), items[len(items)-1].ID)
		}
		for i := 1; i < len(items); i++ {
			if items[i].CreatedAt.After(items[i-1].CreatedAt) {
				t.Fatalf("list not in created_at DESC order at %d", i)
			}
		}
		if _, err := service.ListConversationArtifacts(ctx, tx, orgA, "not-a-uuid", actor); !errors.Is(err, executiondelivery.ErrNotFound) {
			t.Fatalf("bad id = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := user(orgA, memberA, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		_, err := service.ListConversationArtifacts(ctx, tx, orgA, conversationA, actor)
		if !errors.Is(err, executiondelivery.ErrNotFound) {
			t.Fatalf("other member private conversation = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := user(orgB, ownerB, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		_, err := service.ListConversationArtifacts(ctx, tx, orgB, conversationA, actor)
		if !errors.Is(err, executiondelivery.ErrNotFound) {
			t.Fatalf("cross-org = %v", err)
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
		{`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'carol@example.test') ON CONFLICT DO NOTHING`, []any{memberA}},
		{`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER') ON CONFLICT DO NOTHING`, []any{orgA, memberA}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3::uuid, 'MEMBER') ON CONFLICT DO NOTHING`, []any{orgA, spaceA, memberA}},
		{`UPDATE openfox.agents SET space_id = $2::uuid, owner_user_id = $3::uuid, name = 'S10D agent', kind = 'CODE', visibility = 'PRIVATE', status = 'ACTIVE', archived_at = NULL, current_config_version = 1 WHERE organization_id = $1::uuid AND id = $4::uuid`, []any{orgA, spaceA, ownerA, agentA}},
		{`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'PRIVATE') ON CONFLICT DO NOTHING`, []any{orgA, conversationA, spaceA, agentA, ownerA}},
		{`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version, actor_id, state, generation, finished_at) SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, g.id, 1, 1, $7::uuid, $8, 1, CASE WHEN $8 = 'RUNNING' THEN NULL ELSE clock_timestamp() END FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $6::uuid ON CONFLICT DO NOTHING`, []any{orgA, runLive, spaceA, conversationA, agentA, deviceA, ownerA, "RUNNING"}},
		{`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version, actor_id, state, generation, finished_at) SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, g.id, 1, 1, $7::uuid, $8, 1, CASE WHEN $8 = 'RUNNING' THEN NULL ELSE clock_timestamp() END FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $6::uuid ON CONFLICT DO NOTHING`, []any{orgA, runDone, spaceA, conversationA, agentA, deviceA, ownerA, "SUCCEEDED"}},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("%s: %v", statement.query, err)
		}
	}
}
