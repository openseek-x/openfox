package exports

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/artifacts"
	"openfox/internal/platform/postgres"
	"os"
	"testing"
	"time"
)

func TestReviewExportClaimsAndFence(t *testing.T) {
	dsn := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要临时 PostgreSQL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	pool, err := postgres.New(ctx, postgres.Config{URL: dsn, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	org := "00000000-0000-0000-0000-0000000000a1"
	var id string
	err = admin.QueryRow(ctx, `INSERT INTO openfox.export_jobs(organization_id,format,space_ids,operation_id,created_by) VALUES($1::uuid,'csv',ARRAY['00000000-0000-0000-0000-0000000000aa'::uuid],'op_review1234','00000000-0000-0000-0000-000000000001') RETURNING id::text`, org).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Pool: pool, Instance: "review-old", Storage: artifacts.Local{Root: t.TempDir()}}
	job, err := w.claim(ctx, org)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if job.attempts != 1 {
		t.Errorf("认领未计数: %d", job.attempts)
	}
	first, e := w.generate(ctx, org, job)
	if e != nil {
		t.Fatal(e)
	}
	secondJob := *job
	secondJob.attempts++
	second, e := w.generate(ctx, org, &secondJob)
	if e != nil {
		t.Fatal(e)
	}
	if first.key == second.key {
		t.Error("不同认领使用同一存储键")
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE openfox.export_jobs SET lease_owner='review-new' WHERE id=$1::uuid`, id)
	if err = w.retry(ctx, org, job); err == nil {
		t.Error("旧 worker 可以重排新租约")
	}
	exec(`UPDATE openfox.export_jobs SET lease_owner='review-new',lease_expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1::uuid`, id)
	if err = w.fail(ctx, org, job, "ERROR", "测试"); err == nil {
		t.Error("旧 worker 可以结束新租约")
	}
	var state string
	_ = admin.QueryRow(ctx, `SELECT status FROM openfox.export_jobs WHERE id=$1::uuid`, id).Scan(&state)
	if state != "RUNNING" {
		t.Errorf("新租约状态被修改: %s", state)
	}
	exec(`UPDATE openfox.export_jobs SET status='RUNNING',finished_at=NULL,attempts=3,lease_expires_at=clock_timestamp()-interval '1 second',retry_at=NULL WHERE id=$1::uuid`, id)
	job, err = w.claim(ctx, org)
	if err != nil || job != nil {
		t.Errorf("超过上限仍被认领: %+v %v", job, err)
	}
	_ = admin.QueryRow(ctx, `SELECT status FROM openfox.export_jobs WHERE id=$1::uuid`, id).Scan(&state)
	if state != "FAILED" {
		t.Errorf("未结束崩溃任务: %s", state)
	}
}
