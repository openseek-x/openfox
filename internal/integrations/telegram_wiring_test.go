package integrations

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Use the real shared worker entry, not just provider steps. The fixed-origin
// Telegram client talks only to the fixture's private TLS server.
func TestTelegramSharedWorkerRoutingPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.connect()
	w := &IMWorker{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "telegram-shared-worker"}
	if err := w.RunOrganization(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		b, err = f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
		return err
	})
	if b.State != "ACTIVE" {
		t.Fatalf("shared worker did not complete control: %s", b.State)
	}
	// Generic disconnect must not bypass Telegram's generation and remote-state
	// workflow, even for the currently authorized owner with a correct phrase.
	if err := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.service.Disconnect(ctx, tx, f.org, f.actor, b.ConnectionID, 1, ConfirmPhrase("Telegram"))
		return err
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("generic disconnect: %v", err)
	}
	f.bind(b, 700)
	p := tgQueued(t, f, b, 701)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		items, err := w.claimInbound(ctx, tx)
		if err == nil && len(items) != 0 {
			t.Fatal("generic worker claimed Telegram input")
		}
		return err
	})
	if err := w.RunOrganization(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	outbox := tgFinalize(t, f, b, p, "Shared worker controlled result")
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		items, err := w.claimOutbox(ctx, tx)
		if err == nil && len(items) != 0 {
			t.Fatal("generic sender claimed Telegram reply")
		}
		return err
	})
	if err := w.RunOrganization(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(ctx, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&state)
		if err == nil && state != "DONE" {
			t.Fatalf("shared worker reply=%s", state)
		}
		return err
	})
	f.mu.Lock()
	calls, reply, chat := f.sendCalls, f.sentReply, f.sentChat
	f.mu.Unlock()
	if calls != 1 || reply != 701 || chat != 987654 {
		t.Fatalf("shared worker delivery %d/%d/%d", calls, reply, chat)
	}
}
