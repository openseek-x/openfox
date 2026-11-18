package integrations

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func tgQueued(t *testing.T, f *tgFixture, b TelegramBot, number int64) telegramInputProof {
	t.Helper()
	r, err := f.receive(b, number, 987654, number, 987654, "请记录这条测试消息", nil)
	if err != nil || r.Result != "queued" {
		t.Fatalf("receive %+v %v", r, err)
	}
	var p telegramInputProof
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT inbound_id::text FROM openfox.telegram_update_receipts WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id=$2`, b.ConnectionID, formatID(number)).Scan(&id); e != nil {
			return e
		}
		var e error
		p, e = loadTelegramCandidate(ctx, tx, id)
		return e
	})
	return p
}

func TestTelegramInboundImmutableAndConcurrentPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	f.bind(b, 100)
	p := tgQueued(t, f, b, 101)
	workers := []IMWorker{{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "tg-materialize-a"}, {Pool: tgWorkerRunner{f}, Service: f.service, Instance: "tg-materialize-b"}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range workers {
		wg.Add(1)
		go func(w *IMWorker) { defer wg.Done(); errs <- w.ProcessTelegramInbound(context.Background(), f.org) }(&workers[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var count int
		var status, message, author, source string
		if e := tx.QueryRow(ctx, `SELECT q.status,q.message_id::text,m.author_user_id::text,i.source_kind FROM openfox.im_inbound_messages q
 JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.message_id JOIN openfox.telegram_inputs i ON i.organization_id=m.organization_id AND i.message_id=m.id
 WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid`, p.inbound).Scan(&status, &message, &author, &source); e != nil {
			return e
		}
		if status != "DONE" || message != p.inbound || author != f.user || source != "REMOTE" {
			t.Fatalf("wrong materialization %s %s %s %s", status, message, author, source)
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, p.inbound).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			t.Fatalf("inputs %d", count)
		}
		return nil
	})
	// A redelivery after a crash may re-open only the queue; the immutable
	// snapshot and stable message must be reused, never another conversation input.
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET status='PENDING',finished_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.inbound)
		return e
	})
	if err := workers[0].ProcessTelegramInbound(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE openfox.telegram_inputs SET actor_version=actor_version+1`, `DELETE FROM openfox.telegram_inputs`} {
		err := f.run(func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, q+` WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, p.inbound)
			return e
		})
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Fatalf("immutable grant %v", err)
		}
	}
	// Retention removes transport receipts but not authorization of an existing Run.
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_inbound_messages SET finished_at=clock_timestamp()-interval '8 days' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.inbound)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error { return f.service.CleanupTelegram(ctx, tx) })
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var present bool
		e := tx.QueryRow(ctx, `SELECT inbound_id IS NULL FROM openfox.telegram_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, p.inbound).Scan(&present)
		if e == nil && !present {
			t.Fatal("queue FK not released")
		}
		return e
	})
}

func TestTelegramInboundNamespaceConflictAndUnbindPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	identity, _ := f.bind(b, 200)
	p := tgQueued(t, f, b, 201)
	// Deliberate legacy/corrupt namespace occupant. Root's HTTP guard additionally
	// prevents new authenticated UI callers from using this reserved prefix.
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var seq int64
		if e := tx.QueryRow(ctx, `UPDATE openfox.conversations SET next_message_seq=next_message_seq+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid RETURNING next_message_seq-1`, p.conversation).Scan(&seq); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body,client_message_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'USER',$4::uuid,'deliberate corrupt fixture',$5)`, refsTestID(t), p.conversation, seq, f.user, "telegram:"+b.ID+":"+p.inbound)
		return e
	})
	w := IMWorker{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "tg-conflict"}
	if err := w.ProcessTelegramInbound(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, result string
		var count int
		if e := tx.QueryRow(ctx, `SELECT status,result FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.inbound).Scan(&status, &result); e != nil {
			return e
		}
		if status != "IGNORED" || result != "message_conflict" {
			t.Fatalf("conflict %s %s", status, result)
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.telegram_inputs WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid`, p.inbound).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			t.Fatal("dangling snapshot")
		}
		return nil
	})
	p = tgQueued(t, f, b, 202)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.UnbindTelegram(ctx, tx, f.org, f.actor, identity.ID, identity.Version)
	})
	if err := w.ProcessTelegramInbound(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, result string
		e := tx.QueryRow(ctx, `SELECT status,result FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.inbound).Scan(&status, &result)
		if e == nil && (status != "IGNORED" || result != "authorization_changed") {
			t.Fatalf("unbound %s %s", status, result)
		}
		return e
	})
}
