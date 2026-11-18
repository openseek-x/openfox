package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestQQSharedWorkerRoutingPG(t *testing.T) {
	x := newQQReplyPG(t)
	if _, err := x.receive(x.bot, x.callback, qqMessage("C2C", "pending-after-run", "original-subject", "", "later request", "")); err != nil {
		t.Fatal(err)
	}
	// Prove generic Feishu-family claimers cannot steal either QQ queue before
	// exercising the actual shared organization's pipeline.
	if err := x.run(func(ctx context.Context, tx pgx.Tx) error {
		inbound, err := x.worker.claimInbound(ctx, tx)
		if err != nil {
			return err
		}
		outbox, err := x.worker.claimOutbox(ctx, tx)
		if err != nil {
			return err
		}
		if len(inbound) != 0 || len(outbox) != 0 {
			t.Fatalf("generic workers claimed QQ: inbound=%d outbox=%d", len(inbound), len(outbox))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		var body struct {
			ID string `json:"msg_id"`
		}
		if r.URL.Path != "/v2/users/original-subject/messages" || json.NewDecoder(r.Body).Decode(&body) != nil || body.ID != "original-two" {
			t.Error("shared worker lost the original Run's reply target")
		}
		_, _ = w.Write([]byte(`{"id":"shared-worker-receipt","timestamp":"2026-11-15T00:00:00Z"}`))
	})
	for range 2 {
		if err := x.worker.RunOrganization(context.Background(), x.org); err != nil {
			t.Fatal(err)
		}
	}
	if posts.Load() != 1 || x.state() != "DONE" {
		t.Fatal("shared worker delivery", posts.Load(), x.state())
	}
	if err := x.run(func(ctx context.Context, tx pgx.Tx) error {
		var pending, inputs int
		var title string
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE status='PENDING'`).Scan(&pending); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.qq_inputs`).Scan(&inputs); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT title FROM openfox.conversations WHERE id=$1::uuid`, x.conversation).Scan(&title); err != nil {
			return err
		}
		if pending != 0 || inputs != 3 || title != "QQ单聊" {
			t.Fatalf("shared routing result: pending=%d inputs=%d title=%q", pending, inputs, title)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := x.run(func(ctx context.Context, tx pgx.Tx) error {
		_, err := x.service.Disconnect(ctx, tx, x.org, x.actor, x.bot.ConnectionID, 1, "")
		return err
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("generic disconnect bypassed QQ's generation fence: %v", err)
	}
}
