package stage3

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/integrations"
)

func (e *shortcutEnv) sync(j imports.Job) imports.Job {
	e.owner().must(200, "GET", "imports/"+j.ID, nil, &j)
	e.owner().must(200, "PUT", "imports/"+j.ID+"/sync", map[string]any{"version": j.Sync.Version, "check_now": true}, &j)
	if err := e.worker().RunOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
	e.owner().must(200, "GET", "imports/"+j.ID, nil, &j)
	return j
}
func (e *shortcutEnv) comment(task, body string) {
	e.member().must(201, "POST", spacePath(e.space.ID)+"/tasks/"+task+"/comments", map[string]any{"body": body, "client_comment_id": s3NewID(e.t)}, nil)
}
func (e *shortcutEnv) posts() int {
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	return len(e.fake.comments)
}

func TestShortcutPersonalWorkerBatchRevocationAndManual(t *testing.T) {
	e := newShortcutEnv(t)
	c := e.bind(false)
	items := []shortcutItem{}
	for i := 0; i < 51; i++ {
		items = append(items, shortcutItem{1000 + i, shortcutUser})
	}
	e.setItems(items...)
	j := e.start(e.importJob(false))
	w := e.worker()
	for i := 0; i < 10 && j.CreatedCount == 0; i++ {
		if err := w.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		e.owner().must(200, "GET", "imports/"+j.ID, nil, &j)
	}
	if j.CreatedCount != 50 {
		t.Fatalf("first batch=%d", j.CreatedCount)
	}
	e.member().must(200, "POST", "integrations/connections/"+c.ID+"/disconnect", map[string]any{"version": c.Version}, nil)
	j = e.drain(j)
	if e.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND t.assignee_id=$2::uuid`, j.ID, s3MemberID) != 50 || e.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id AND i.organization_id=t.organization_id WHERE i.job_id=$1::uuid AND t.external_assignee_id IS NOT NULL`, j.ID) != 1 {
		t.Fatal("revocation did not preserve previous/stop next batch")
	}
	if e.count(`SELECT count(*) FROM openfox.external_accounts WHERE organization_id=$1::uuid AND provider='shortcut' AND linked_user_id IS NOT NULL`, e.org) != 0 {
		t.Fatal("scoped mapping leaked globally")
	}
	e.setItems(shortcutItem{2000, shortcutUser})
	manual := e.importJob(false)
	cfg := *manual.Config
	cfg.Users = []imports.UserMapping{{SourceID: shortcutUser, Mode: "member", UserID: s3MemberID}}
	e.owner().must(200, "PUT", "imports/"+manual.ID+"/config", map[string]any{"config_version": manual.ConfigVersion, "config": cfg}, &manual)
	manual = e.drain(e.start(manual))
	if e.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id WHERE i.job_id=$1::uuid AND t.assignee_id=$2::uuid`, manual.ID, s3MemberID) != 1 {
		t.Fatal("explicit mapping lost")
	}
}

func TestShortcutPersonalCommentsConsentUnknownAndUnlink(t *testing.T) {
	e := newShortcutEnv(t)
	c := e.bind(false)
	e.setItems(shortcutItem{3000, shortcutUser})
	j := e.drain(e.start(e.importJob(true)))
	e.owner().must(200, "PUT", "imports/"+j.ID+"/sync", map[string]any{"version": j.Sync.Version, "direction": "BIDIRECTIONAL"}, &j)
	var task string
	e.scalar(&task, `SELECT task_id::text FROM openfox.import_sync_items WHERE job_id=$1::uuid`, j.ID)
	e.comment(task, "before-consent")
	j = e.sync(j)
	if e.posts() != 0 {
		t.Fatal("identity-only posted")
	}
	e.member().must(200, "PUT", "me/account-bindings/shortcut", e.input("member", true, c.Version), &c)
	e.comment(task, "authorized-new")
	j = e.sync(j)
	if e.posts() != 1 {
		t.Fatalf("posts=%d, sync=%+v", e.posts(), j.Sync)
	}
	j = e.sync(j)
	if e.posts() != 1 {
		t.Fatal("duplicate SENT")
	}
	e.comment(task, "unknown-response")
	j = e.sync(j)
	if e.posts() != 2 || j.Sync.Error != "COMMENT_DELIVERY_UNKNOWN" {
		t.Fatalf("unknown %+v posts=%d", j.Sync, e.posts())
	}
	for i := 0; i < 2; i++ {
		j = e.sync(j)
	}
	if e.posts() != 2 {
		t.Fatal("unknown auto-retried")
	}
	e.member().must(200, "PUT", "me/account-bindings/shortcut", e.input("", false, c.Version), &c)
	e.comment(task, "while-disabled")
	j = e.sync(j)
	if e.posts() != 2 {
		t.Fatal("disabled posted")
	}
	e.member().must(200, "PUT", "me/account-bindings/shortcut", e.input("rotated", true, c.Version), &c)
	e.comment(task, "after-reenabled")
	j = e.sync(j)
	if e.posts() != 3 {
		t.Fatal("re-enable replayed old comments or lost new")
	}
	e.member().must(200, "POST", "integrations/connections/"+c.ID+"/disconnect", map[string]any{"version": c.Version}, nil)
	e.comment(task, "after-unlink")
	j = e.sync(j)
	if e.posts() != 3 {
		t.Fatal("unlinked posted")
	}
	// Rebinding cannot revive a claimed unknown or comments before this consent.
	c = e.bind(true)
	e.comment(task, "after-rebind")
	j = e.sync(j)
	if e.posts() != 4 {
		t.Fatalf("rebind posts=%d sync=%+v", e.posts(), j.Sync)
	}
	if e.count(`SELECT count(*) FROM openfox.import_comment_deliveries WHERE job_id=$1::uuid AND state='SENDING'`, j.ID) != 1 {
		t.Fatal("unknown receipt lost")
	}
	e.setItems(shortcutItem{3000, shortcutUser}, shortcutItem{3001, shortcutOther})
	j = e.sync(j)
	if e.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.import_sync_items i ON i.task_id=t.id WHERE i.job_id=$1::uuid AND t.external_assignee_id IS NOT NULL`, j.ID) != 1 {
		t.Fatal("unbound remote member was merged")
	}
}

func TestShortcutPersonalConcurrentIdentityClaims(t *testing.T) {
	e := newShortcutEnv(t)
	a, b := e.member(), e.other()
	start := make(chan struct{})
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, v := range []struct {
		client *s3Client
		token  string
	}{{a, "member"}, {b, "same-other"}} {
		wg.Add(1)
		go func(c *s3Client, token string) {
			defer wg.Done()
			<-start
			statuses <- c.do("PUT", "me/account-bindings/shortcut", e.input(token, false, 0)).Status
		}(v.client, v.token)
	}
	close(start)
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for v := range statuses {
		if v == 200 {
			ok++
		}
		if v == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", ok, conflict)
	}
}

func TestShortcutPersonalWorkerSerializesEmailChange(t *testing.T) {
	e := newShortcutEnv(t)
	e.bind(false)
	e.setItems(shortcutItem{4000, shortcutUser})
	j := e.start(e.importJob(false))
	w := e.worker()
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
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if e.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF c,m,u%'`) > 0 {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("write batch never waited on user lock")
	}
	if _, err = tx.Exec(e.ctx, `UPDATE openfox.users SET email='shortcut-changed@example.test' WHERE id=$1::uuid`, s3MemberID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, s3Email(s3MemberID)) })
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	j = e.drain(j)
	if e.count(`SELECT count(*) FROM openfox.tasks t JOIN openfox.import_items i ON i.task_id=t.id WHERE i.job_id=$1::uuid AND t.assignee_id IS NULL AND t.external_assignee_id IS NOT NULL`, j.ID) != 1 {
		t.Fatal("email change raced past worker")
	}
}

func TestShortcutPersonalCommentsConcurrentWorkersAndRevocation(t *testing.T) {
	e := newShortcutEnv(t)
	c := e.bind(true)
	e.setItems(shortcutItem{5000, shortcutUser})
	j := e.drain(e.start(e.importJob(true)))
	e.owner().must(200, "PUT", "imports/"+j.ID+"/sync", map[string]any{"version": j.Sync.Version, "direction": "BIDIRECTIONAL"}, &j)
	var task string
	e.scalar(&task, `SELECT task_id::text FROM openfox.import_sync_items WHERE job_id=$1::uuid`, j.ID)
	e.comment(task, "concurrent-once")
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- e.worker().RunOrganization(e.ctx, e.org) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if e.posts() != 1 {
		t.Fatalf("competing workers posted %d", e.posts())
	}
	e.comment(task, "role-revoked")
	e.exec(`UPDATE openfox.organization_members SET role='GUEST' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3MemberID)
	j = e.sync(j)
	if e.posts() != 1 {
		t.Fatal("revoked member posted")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NOT NULL`, c.ID) != 1 {
		t.Fatal("unexpected credential mutation")
	}
	// Parent disconnect clears all personal credentials even while the member is revoked.
	e.owner().must(200, "POST", "integrations/connections/"+e.parent.ID+"/disconnect", map[string]any{"version": e.parent.Version, "confirm_phrase": "断开 Shortcut"}, nil)
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NULL AND status='DISCONNECTED'`, c.ID) != 1 {
		t.Fatal("disconnect did not clear revoked member secret")
	}
}

func TestShortcutPersonalUnlinkWhileSaveWaitsForIdentityLock(t *testing.T) {
	e := newShortcutEnv(t)
	in := integrations.ShortcutPersonalInput{ConnectionID: e.parent.ID, Token: "member", CommentsEnabled: true}
	prepare := func() integrations.ShortcutPersonalGrant {
		var g integrations.ShortcutPersonalGrant
		if err := e.tx(s3MemberID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
			var err error
			g, err = e.svc.PrepareShortcutPersonal(ctx, tx, e.org, a, in)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		g, err := integrations.VerifyShortcutPersonal(e.ctx, g)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	aProof, bProof := prepare(), prepare()
	held, proceed := make(chan struct{}), make(chan struct{})
	aEntered := make(chan struct{})
	bDone, aDone := make(chan error, 1), make(chan error, 1)
	go func() {
		bDone <- e.tx(s3MemberID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, e.org+":shortcut-identity:"+shortcutWorkspace); err != nil {
				return err
			}
			close(held)
			<-proceed
			c, err := e.svc.SaveShortcutPersonal(ctx, tx, e.org, a, in, bProof)
			if err != nil {
				return err
			}
			_, err = e.svc.Disconnect(ctx, tx, e.org, a, c.ID, c.Version, "")
			return err
		})
	}()
	<-held
	go func() {
		aDone <- e.tx(s3MemberID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
			close(aEntered)
			_, err := e.svc.SaveShortcutPersonal(ctx, tx, e.org, a, in, aProof)
			return err
		})
	}()
	<-aEntered
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if e.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%SELECT pg_advisory_xact_lock%'`) > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(proceed)
	if !waiting {
		t.Fatal("save did not wait on identity lock")
	}
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	if err := <-aDone; err != integrations.ErrVersionConflict {
		t.Fatalf("late proof after committed unlink: %v", err)
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='shortcut' AND scope='PERSONAL' AND status<>'DISCONNECTED'`, e.org) != 0 {
		t.Fatal("late proof revived binding")
	}
}

func TestShortcutPersonalCommentsRevokedSpaceDoesNotStarveOthers(t *testing.T) {
	e := newShortcutEnv(t)
	e.bind(true)
	e.other().must(200, "PUT", "me/account-bindings/shortcut", e.input("other", true, 0), nil)
	e.addSpaceMember(e.space.ID, s3OtherID, "MEMBER")
	e.setItems(shortcutItem{6000, shortcutUser})
	j := e.drain(e.start(e.importJob(true)))
	e.owner().must(200, "PUT", "imports/"+j.ID+"/sync", map[string]any{"version": j.Sync.Version, "direction": "BIDIRECTIONAL"}, &j)
	var task string
	e.scalar(&task, `SELECT task_id::text FROM openfox.import_sync_items WHERE job_id=$1::uuid`, j.ID)
	for i := 0; i < 21; i++ {
		e.comment(task, "space-access-will-be-revoked")
	}
	e.exec(`UPDATE openfox.space_members SET role='VIEWER' WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, e.org, e.space.ID, s3MemberID)
	e.other().must(201, "POST", spacePath(e.space.ID)+"/tasks/"+task+"/comments", map[string]any{"body": "eligible-other-author", "client_comment_id": s3NewID(t)}, nil)
	j = e.sync(j)
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	if len(e.fake.comments) != 1 || e.fake.comments[0] != "eligible-other-author" {
		t.Fatalf("revoked space backlog blocked eligible comment: posts=%v sync=%+v", e.fake.comments, j.Sync)
	}
}
