package api

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

func TestReviewArtifactCleanupWaitsForRegistration(t *testing.T) {
	dsn := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要临时 PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	pool, err := postgres.New(ctx, postgres.Config{URL: dsn, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 5 * time.Second, LockTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const org = "00000000-0000-0000-0000-0000000000a1"
	const user = "00000000-0000-0000-0000-000000000001"
	const agent = "00000000-0000-0000-0000-0000000000a2"
	const device = "00000000-0000-0000-0000-000000000011"
	const space = "00000000-0000-0000-0000-0000000000aa"
	const conv = "00000000-0000-0000-0000-00000000c561"
	const run = "00000000-0000-0000-0000-00000000d561"
	const key = "review/pending-commit"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE openfox.agents SET space_id=$2::uuid,owner_user_id=$3::uuid,name='清理回归',kind='CODE',visibility='PRIVATE',status='ACTIVE',archived_at=NULL,current_config_version=1 WHERE organization_id=$1::uuid AND id=$4::uuid`, org, space, user, agent)
	exec(`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,created_by) SELECT $1::uuid,$2::uuid,1,$3::uuid,id,'codex','default',$4::uuid FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$3::uuid ON CONFLICT DO NOTHING`, org, agent, device, user)
	exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'PRIVATE')`, org, conv, space, agent, user)
	exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,generation,runtime,model,approval_mode,reasoning_effort) SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,id,1,1,$7::uuid,'RUNNING',1,'codex','default','READ_ONLY','DEFAULT' FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$6::uuid`, org, run, space, conv, agent, device, user)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM openfox.runs WHERE id=$1::uuid FOR UPDATE`, run); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO openfox.artifacts(organization_id,run_id,name,storage_key,size_bytes,sha256,mime_type,retention_until) VALUES($1::uuid,$2::uuid,'review.txt',$3,1,repeat('a',64),'text/plain',clock_timestamp()+interval '1 day')`, org, run, key); err != nil {
		t.Fatal(err)
	}
	type result struct {
		exists bool
		err    error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		var exists bool
		err := pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: 1}, nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			close(started)
			var err error
			exists, err = artifactStorageRegistered(ctx, tx, run, key)
			return err
		})
		done <- result{exists, err}
	}()
	select {
	case <-started:
	case got := <-done:
		t.Fatalf("核查事务没有启动：%+v", got)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case got := <-done:
		t.Fatalf("登记事务未结束就决定清理：%+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !got.exists {
			t.Fatalf("未保留刚提交的对象：%+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
