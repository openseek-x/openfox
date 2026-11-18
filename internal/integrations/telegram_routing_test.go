package integrations

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTelegramPrivateChatDistinctFromUserPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	const chat int64 = 777888
	const user int64 = 987654
	var session TelegramBindingSession
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.IssueTelegramBinding(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	u, _ := url.Parse(session.StartURL)
	r, e := f.receive(b, 1500, chat, 1500, user, "/start "+u.Query().Get("start"), []any{map[string]any{"type": "bot_command", "offset": 0, "length": 6}})
	if e != nil || r.Result != "binding_candidate" {
		t.Fatalf("binding candidate %+v %v", r, e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.GetTelegramBindingSession(ctx, tx, f.org, f.actor, session.ID)
		if e != nil {
			return e
		}
		identity, e := f.service.ConfirmTelegramBinding(ctx, tx, f.org, f.actor, session.ID, session.Version)
		if e != nil {
			return e
		}
		var storedChat, storedUser int64
		e = tx.QueryRow(ctx, `SELECT private_chat_id,remote_user_id FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, identity.ID).Scan(&storedChat, &storedUser)
		if e == nil && (storedChat != chat || storedUser != user) {
			t.Fatalf("identity facts conflated %d/%d", storedChat, storedUser)
		}
		return e
	})
	// The same sender cannot move this binding to another private destination.
	r, e = f.receive(b, 1501, 777889, 1501, user, "wrong chat must not run", nil)
	if e != nil || r.Result != "identity_required" {
		t.Fatalf("wrong chat %+v %v", r, e)
	}
	r, e = f.receive(b, 1502, chat, 1502, user, "current private route", nil)
	if e != nil || r.Result != "queued" {
		t.Fatalf("bound chat %+v %v", r, e)
	}
	var proof telegramInputProof
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			t.Fatalf("unexpected queued messages %d", count)
		}
		var inbound string
		if e := tx.QueryRow(ctx, `SELECT inbound_id::text FROM openfox.telegram_update_receipts WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id='1502'`, b.ConnectionID).Scan(&inbound); e != nil {
			return e
		}
		var e error
		proof, e = loadTelegramCandidate(ctx, tx, inbound)
		return e
	})
	w := &IMWorker{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "tg-distinct-route"}
	if e = w.ProcessTelegramInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	tgFinalize(t, f, b, proof, "controlled private destination result")
	if e = w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls, actualChat, reply := f.sendCalls, f.sentChat, f.sentReply
	f.mu.Unlock()
	if calls != 1 || actualChat != chat || reply != 1502 {
		t.Fatalf("wrong outbound route calls=%d chat=%d reply=%d", calls, actualChat, reply)
	}
}
