package stage3

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (e *tapdPersonalEnv) waitDBLock(pattern string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		e.scalar(&n, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1`, pattern)
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("worker did not wait for expected database lock: %s", pattern)
}

func TestTAPDPersonalWorkerSerializesEmailChange(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	e.bind()
	e.setItems(tapdFixtureItem{"9000", "member.nick"})
	job := e.start(e.importJob("101", false))
	w := e.worker()
	// Finish validation before testing the real write batch's user-row lock.
	if err := w.RunOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(e.ctx, os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(e.ctx, `SELECT id FROM openfox.users WHERE id=$1::uuid FOR UPDATE`, s3MemberID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.RunOrganization(e.ctx, e.org) }()
	e.waitDBLock("%c.settings->>'nick'%")
	if _, err = tx.Exec(e.ctx, `UPDATE openfox.users SET email='changed-tapd-member@example.test' WHERE id=$1::uuid`, s3MemberID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, s3Email(s3MemberID)) })
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	job = e.drain(w, job)
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND t.assignee_id IS NULL AND t.external_assignee_id IS NOT NULL`, job.ID)
	if count != 1 {
		t.Fatal("email change raced past batch validation")
	}
}

func TestTAPDPersonalSyncAndParentDisconnectLockOrder(t *testing.T) {
	e := newTAPDPersonalEnv(t)
	e.bind()
	e.setItems(tapdFixtureItem{"9100", "member.nick"})
	w := e.worker()
	job := e.drain(w, e.start(e.importJob("101", true)))
	e.setItems(tapdFixtureItem{"9100", "member.nick"}, tapdFixtureItem{"9101", "member.nick"})
	e.exec(`UPDATE openfox.import_sync_links SET next_check=clock_timestamp() WHERE job_id=$1::uuid`, job.ID)
	locked, proceed := make(chan struct{}), make(chan struct{})
	disconnected := make(chan error, 1)
	go func() {
		disconnected <- e.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
			if _, err := tx.Exec(ctx, `SELECT id FROM openfox.integration_connections WHERE id=$1::uuid FOR UPDATE`, e.parent.ID); err != nil {
				return err
			}
			close(locked)
			<-proceed
			_, err := e.svc.Disconnect(ctx, tx, e.org, actor, e.parent.ID, e.parent.Version, integrations.ConfirmPhrase("TAPD"))
			return err
		})
	}()
	<-locked
	synced := make(chan error, 1)
	go func() { synced <- w.RunOrganization(e.ctx, e.org) }()
	e.waitDBLock("%scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE%")
	close(proceed)
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent disconnect blocked on sync link: lock order regression")
	}
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.import_sync_items WHERE job_id=$1::uuid`, job.ID)
	if count != 1 {
		t.Fatal("sync wrote after parent disconnect")
	}
}
