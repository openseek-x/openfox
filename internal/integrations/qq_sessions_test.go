package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/qqconnector"
)

func TestQQSessionManagerOwnedBound(t *testing.T) {
	m := NewQQSessionManager()
	var started, stopped atomic.Int32
	for i := range 5 {
		if !m.start(context.Background(), string(rune('a'+i)), func(ctx context.Context) { started.Add(1); <-ctx.Done(); stopped.Add(1) }) {
			t.Fatal("slot unavailable")
		}
	}
	if m.start(context.Background(), "sixth", func(context.Context) { t.Error("sixth ran") }) || m.start(context.Background(), "a", func(context.Context) { t.Error("duplicate ran") }) {
		t.Fatal("bound broken")
	}
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	if started.Load() != 5 || stopped.Load() != 5 {
		t.Fatal("owned jobs not joined", started.Load(), stopped.Load())
	}
	if m.start(context.Background(), "closed", func(context.Context) {}) {
		t.Fatal("closed manager restarted")
	}
}
func newQQQRPG(t *testing.T) (*qqPG, QQBot, QQQRSession, *IMWorker) {
	f := newQQPG(t)
	f.service.QQConnectorNodePath = "/controlled/not-executed/node"
	f.service.QQConnectorRoot = "/controlled/not-executed/sdk"
	b, e := f.create("qr-fixture-creation")
	if e != nil {
		t.Fatal(e)
	}
	var session QQQRSession
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.StartQQQRSession(ctx, tx, f.org, f.actor, b.ID, QQQRStartInput{Version: b.Version, IdempotencyKey: "qr-fixture-session"})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	w := f.worker()
	w.QQSessions = NewQQSessionManager()
	t.Cleanup(func() {
		if e := w.QQSessions.Close(); e != nil {
			t.Error(e)
		}
	})
	return f, b, session, w
}
func claimQQQR(t *testing.T, f *qqPG, w *IMWorker, id string) qqQRWork {
	t.Helper()
	var job qqQRWork
	var ok bool
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		job, ok, e = w.claimQQSession(ctx, tx, f.org, id)
		return e
	})
	if e != nil || !ok {
		t.Fatal("claim", ok, e)
	}
	return job
}
func getQQQR(t *testing.T, f *qqPG, id string) QQQRSession {
	t.Helper()
	var v QQQRSession
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		v, e = f.service.GetQQQRSession(ctx, tx, f.org, f.actor, id)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestQQSDKCallbackPersistencePG(t *testing.T) {
	f, b, session, w := newQQQRPG(t)
	var duplicate QQQRSession
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		duplicate, e = f.service.StartQQQRSession(ctx, tx, f.org, f.actor, b.ID, QQQRStartInput{Version: b.Version, IdempotencyKey: "qr-fixture-session"})
		return e
	})
	if e != nil || duplicate.ID != session.ID {
		t.Fatal("same operation not idempotent", e)
	}
	job := claimQQQR(t, f, w, session.ID)
	var ok bool
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, ok, e := w.claimQQSession(ctx, tx, f.org, session.ID)
		if ok {
			t.Error("second claim")
		}
		return e
	})
	if e != nil || ok {
		t.Fatal(e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		b, e := loadQQBot(ctx, tx, b.ID)
		if e != nil {
			return e
		}
		_, e = f.service.PrepareQQConnection(ctx, tx, f.org, f.actor, b.ID, QQConnectInput{Version: b.Version, AppID: "manual-racing-app", AppSecret: "synthetic"})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("manual credentials raced QR", e)
	}
	for _, event := range []qqconnector.Event{{Kind: "qr", URL: "https://q.qq.com/qqbot/openclaw/connect.html?task_id=owned"}, {Kind: "expired"}, {Kind: "qr", URL: "https://q.qq.com/qqbot/openclaw/connect.html?task_id=owned-new"}} {
		if e = f.run(func(ctx context.Context, tx pgx.Tx) error {
			_, e := w.acceptQQSDKEvent(ctx, tx, f.org, job, event)
			return e
		}); e != nil {
			t.Fatal(e)
		}
	}
	var work QQConnectWork
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		work, e = w.acceptQQSDKEvent(ctx, tx, f.org, job, qqconnector.Event{Kind: "success", AppID: "sdk-app-" + f.org, AppSecret: "owned-sdk-secret"})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	v := getQQQR(t, f, session.ID)
	raw, _ := json.Marshal(v)
	if !v.HasCandidate || v.Status != "DONE" || v.QRURL != "" || strings.Contains(string(raw), "owned-sdk-secret") {
		t.Fatal("candidate leaked or not durable")
	}
	var blob []byte
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT candidate_sealed FROM openfox.qq_qr_sessions WHERE id=$1::uuid`, session.ID).Scan(&blob)
	})
	if e != nil || strings.Contains(string(blob), "owned-sdk-secret") {
		t.Fatal("candidate not sealed", e)
	}
	var calls atomic.Int32
	client := qqTLS(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"sdk-validated-token","expires_in":"3600"}`))
	})
	result := ExecuteQQConnection(context.Background(), client, work)
	finished, e := f.finish(work, result)
	if e != nil || finished.State != "WAITING_WEBHOOK" || finished.AppID != "sdk-app-"+f.org || calls.Load() != 1 {
		t.Fatal("candidate token verification", finished.State, e, calls.Load())
	}
	t.Log("controlled SDK callback values commit sealed candidate and validating state; real TLS token verification claims AppID; QR UI state has no secret. Native SDK execution is independently proven in qqconnector suite.")
}

func TestQQQRLeaseUnknownAndCancellationPG(t *testing.T) {
	f, b, session, w := newQQQRPG(t)
	job := claimQQQR(t, f, w, session.ID)
	if e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, session.ID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := w.processQQMaintenance(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	v := getQQQR(t, f, session.ID)
	if v.Status != "UNKNOWN" {
		t.Fatal(v.Status)
	}
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := w.acceptQQSDKEvent(ctx, tx, f.org, job, qqconnector.Event{Kind: "success", AppID: "late-app", AppSecret: "late-secret"})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("late QR result completed", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		b, e := loadQQBot(ctx, tx, b.ID)
		if e != nil {
			return e
		}
		_, e = f.service.StartQQQRSession(ctx, tx, f.org, f.actor, b.ID, QQQRStartInput{Version: b.Version, IdempotencyKey: "unknown-must-not-restart"})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("unknown restarted SDK", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.CancelQQQRSession(ctx, tx, f.org, f.actor, session.ID, QQQRActionInput{Version: v.Version, RemoteChecked: true})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("unknown child was not proven stopped", e)
	}
	// A missing server cannot provide a joined-child proof; only after the
	// immutable native deadline plus hard-stop grace may a manager close it.
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET expires_at=clock_timestamp()-interval '9 seconds' WHERE id=$1::uuid`, session.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.CancelQQQRSession(ctx, tx, f.org, f.actor, session.ID, QQQRActionInput{Version: v.Version, RemoteChecked: true})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	v = getQQQR(t, f, session.ID)
	if v.Status != "CANCELLED" {
		t.Fatal("acknowledgment missing")
	}
}

func TestQQQRUnverifiedDeploymentNoStartPG(t *testing.T) {
	f, _, session, w := newQQQRPG(t)
	if e := w.runQQSession(context.Background(), f.org, session.ID); e != nil {
		t.Fatal(e)
	}
	v := getQQQR(t, f, session.ID)
	if v.Status != "FAILED" || v.StatusReason != "connector_not_verified" {
		t.Fatal("unverified deployment attempted", v.Status, v.StatusReason)
	}
}

func TestQQSendLeaseUnknownNoRetryPG(t *testing.T) {
	x := newQQReplyPG(t)
	x.queue()
	var claimed bool
	e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_replies`).Scan(&id); e != nil {
			return e
		}
		var e error
		_, claimed, e = x.worker.prepareQQSend(ctx, tx, x.org, id)
		return e
	})
	if e != nil || !claimed {
		t.Fatal(e, claimed)
	}
	e = x.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.qq_replies SET lease_expires_at=clock_timestamp()-interval '1 second'`)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = x.worker.processQQMaintenance(context.Background(), x.org); e != nil {
		t.Fatal(e)
	}
	if x.state() != "UNKNOWN" {
		t.Fatal(x.state())
	}
	var calls atomic.Int32
	x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	x.send()
	if calls.Load() != 0 {
		t.Fatal("expired send lease retried")
	}
}
