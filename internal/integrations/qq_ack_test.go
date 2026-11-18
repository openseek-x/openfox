package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestQQVerifiedPersistenceRetryMarkerPG(t *testing.T) {
	f, b, route := newQQReady(t)
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	locked, e := c.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer locked.Rollback(context.Background())
	if _, e = locked.Exec(context.Background(), `LOCK TABLE openfox.qq_message_receipts IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(qqMessage("C2C", "locked-event", "unbound-subject", "", "controlled text", ""))
	var response json.RawMessage
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='30ms'`); e != nil {
			return e
		}
		var e error
		response, e = f.service.ReceiveQQWebhook(ctx, tx, f.org, b.ID, TokenHash(route), qqSigned(b.AppID, raw), raw)
		return e
	})
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "55P03" || string(response) != string(qqAck) {
		t.Fatal("verified persistence did not retain retry marker", e, string(response))
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		h := qqSigned(b.AppID, raw)
		h.Set("X-Signature-Ed25519", "invalid")
		var e error
		response, e = f.service.ReceiveQQWebhook(ctx, tx, f.org, b.ID, TokenHash(route), h, raw)
		return e
	})
	if e == nil || response != nil {
		t.Fatal("invalid signature received retry acknowledgment")
	}
	t.Log("actual receipt table lock timeout after valid signature retains ACK marker; invalid signature remains unacknowledged; both transactions rolled back")
}
