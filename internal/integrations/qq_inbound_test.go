package integrations

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/platform/postgres"
)

func (f *qqPG) worker() *IMWorker {
	pool, e := postgres.New(context.Background(), postgres.Config{URL: f.dsn, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second})
	if e != nil {
		f.t.Fatal(e)
	}
	f.t.Cleanup(pool.Close)
	return &IMWorker{Pool: pool, Service: f.service, Instance: "qq-owned-test-worker"}
}
func (f *qqPG) bindPrivate(b QQBot, route, subject, key string) QQBindingSession {
	v := f.startBinding(b, QQStartBindingInput{Purpose: "PRIVATE_CODE", IdempotencyKey: key})
	if _, e := f.receive(b, route, qqMessage("C2C", key, subject, "", "/openfox-bind "+v.Code, "")); e != nil {
		f.t.Fatal(e)
	}
	out, e := f.confirm(f.binding(v.ID), false)
	if e != nil {
		f.t.Fatal(e)
	}
	return out
}

func TestQQInboundNativeConversationPG(t *testing.T) {
	f, b, route := newQQReady(t)
	f.bindPrivate(b, route, "private-subject", "private-native-binding")
	message := qqMessage("C2C", "real-message", "private-subject", "", "Please inspect this task.", "")
	if _, e := f.receive(b, route, message); e != nil {
		t.Fatal(e)
	}
	w := f.worker()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- w.processQQInbound(context.Background(), f.org) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var conversation, id, source, body, visibility string
	var count int
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT m.id::text,m.conversation_id::text,s.source,m.body,c.visibility FROM openfox.messages m JOIN openfox.qq_inputs s ON (s.organization_id,s.message_id)=(m.organization_id,m.id) JOIN openfox.conversations c ON (c.organization_id,c.id)=(m.organization_id,m.conversation_id)`).Scan(&id, &conversation, &source, &body, &visibility); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs WHERE message_id=$1::uuid`, id).Scan(&count)
	})
	if e != nil {
		t.Fatal(e)
	}
	if source != "REMOTE" || body != "Please inspect this task." || visibility != "PRIVATE" || count != 1 {
		t.Fatal("actual conversation chain", source, body, visibility, count)
	}
	if _, e = f.receive(b, route, message); e != nil {
		t.Fatal(e)
	}
	if e = w.processQQInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.qq_inputs`).Scan(&count)
	})
	if e != nil || count != 1 {
		t.Fatal("duplicate materialized", count, e)
	}
	// The real PostMessage duplicate path may not impersonate the REMOTE source.
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := (conversations.Service{}).PostMessage(ctx, tx, f.org, conversation, slackInboundActor(f.actor), conversations.MessageInput{ID: id, Body: body, ClientMessageID: "qq:" + b.ID + ":" + id, AuthenticatedUserInput: true, Mentions: []conversations.Mention{{AgentID: b.AgentID}}})
		return e
	})
	if !errors.Is(e, conversations.ErrInvalidRequest) {
		t.Fatal("forged source replay", e)
	}
	t.Log("signed receipt → captured identity → actual PRIVATE conversation/PostMessage → immutable REMOTE snapshot → one conversation input; two workers and replay create one")
}

func TestQQInboundRevokeBeforeMaterializePG(t *testing.T) {
	f, b, route := newQQReady(t)
	binding := f.bindPrivate(b, route, "subject", "revoke-before-post")
	if _, e := f.receive(b, route, qqMessage("C2C", "pending-message", "subject", "", "must never execute", "")); e != nil {
		t.Fatal(e)
	}
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.RevokeQQBinding(ctx, tx, f.org, f.actor, binding.BindingID, 1)
	})
	if e != nil {
		t.Fatal(e)
	}
	f.bindPrivate(b, route, "subject", "new-identity-after-revoke")
	w := f.worker()
	if e = w.processQQInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var count int
	var state, reason string
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT disposition,status_reason FROM openfox.qq_message_receipts WHERE remote_message_id='pending-message'`).Scan(&state, &reason); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.messages`).Scan(&count)
	})
	if e != nil || count != 0 || state != "IGNORED" || reason != "authority_changed" {
		t.Fatal("old input revived", count, state, reason, e)
	}
	if _, e = f.receive(b, route, qqMessage("C2C", "new-message", "subject", "", "new binding input", "")); e != nil {
		t.Fatal(e)
	}
	if e = w.processQQInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.qq_inputs`).Scan(&count)
	})
	if e != nil || count != 1 {
		t.Fatal("new binding failed", count, e)
	}
}

func TestQQInboundGroupRoutePG(t *testing.T) {
	f, b, route := newQQReady(t)
	v := f.startBinding(b, QQStartBindingInput{Purpose: "GROUP_DISCOVERY", IdempotencyKey: "group-native-discovery"})
	if _, e := f.receive(b, route, qqMessage("GROUP", "group-admin", "admin-subject", "native-group", "/openfox-group "+v.Code, "admin")); e != nil {
		t.Fatal(e)
	}
	epoch, e := f.confirm(f.binding(v.ID), true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(b, route, qqMessage("GROUP", "candidate", "group-subject", "native-group", "candidate", "member")); e != nil {
		t.Fatal(e)
	}
	var receipt string
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_message_receipts WHERE remote_message_id='candidate'`).Scan(&receipt)
	})
	if e != nil {
		t.Fatal(e)
	}
	v = f.startBinding(b, QQStartBindingInput{Purpose: "GROUP_ASSIGN", OwnerUserID: f.user, GroupEpochID: epoch.GroupEpochID, ReceiptID: receipt, IdempotencyKey: "group-native-assignment"})
	if _, e = f.confirm(v, false); e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(b, route, qqMessage("GROUP", "group-execute", "group-subject", "native-group", "authorized group input", "member")); e != nil {
		t.Fatal(e)
	}
	if e = f.worker().processQQInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var vis, epochID string
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.visibility,r.group_epoch_id::text FROM openfox.qq_routes r JOIN openfox.conversations c ON (c.organization_id,c.id)=(r.organization_id,r.conversation_id)`).Scan(&vis, &epochID)
	})
	if e != nil || vis != "SHARED" || epochID != epoch.GroupEpochID {
		t.Fatal("group route", vis, epochID, e)
	}
}
