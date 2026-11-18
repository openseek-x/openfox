package integrations

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/qq"
)

func qqSigned(app string, raw []byte) http.Header {
	seed := "synthetic-secret"
	for len(seed) < 32 {
		seed += seed
	}
	key := ed25519.NewKeyFromSeed([]byte(seed[:32]))
	ts := "1794691234"
	h := http.Header{}
	h.Set("X-Bot-Appid", app)
	h.Set("X-Signature-Timestamp", ts)
	h.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(key, append([]byte(ts), raw...))))
	return h
}
func (f *qqPG) receive(b QQBot, route string, v any) (json.RawMessage, error) {
	raw, _ := json.Marshal(v)
	var out json.RawMessage
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.ReceiveQQWebhook(ctx, tx, f.org, b.ID, TokenHash(route), qqSigned(b.AppID, raw), raw)
		return e
	})
	return out, e
}
func qqMessage(scope, id, sender, chat, text, role string) map[string]any {
	typ := "C2C_MESSAGE_CREATE"
	author := map[string]any{"user_openid": sender, "bot": false}
	if scope == "GROUP" {
		typ = "GROUP_AT_MESSAGE_CREATE"
		author = map[string]any{"member_openid": sender, "member_role": role, "bot": false}
	}
	return map[string]any{"op": 0, "id": "event-" + id, "t": typ, "d": map[string]any{"id": id, "message_type": 0, "content": text, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "author": author, "group_openid": chat}}
}
func newQQReady(t *testing.T) (*qqPG, QQBot, string) {
	f := newQQPG(t)
	b, e := f.create("ready-operation-key")
	if e != nil {
		t.Fatal(e)
	}
	w, e := f.prepare(b, "appid-"+f.org)
	if e != nil {
		t.Fatal(e)
	}
	b, e = f.finish(w, QQConnectResult{Token: qq.Token{AccessToken: "controlled-token", ExpiresAt: time.Now().Add(time.Hour)}})
	if e != nil {
		t.Fatal(e)
	}
	var route string
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		u, e := f.service.RevealQQWebhook(ctx, tx, f.org, f.actor, b.ID, b.Version)
		route = u[strings.LastIndex(u, "/")+1:]
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(b, route, map[string]any{"op": 13, "d": map[string]string{"plain_token": "test-challenge", "event_ts": "1794691234"}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(b, route, qqMessage("C2C", "ready-message", "unbound-probe", "", "hello", "")); e != nil {
		t.Fatal(e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.GetQQBot(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	if e != nil || b.State != "ACTIVE" {
		t.Fatal("not ready", b, e)
	}
	return f, b, route
}
func (f *qqPG) startBinding(b QQBot, in QQStartBindingInput) QQBindingSession {
	f.t.Helper()
	in.Version = b.Version
	var v QQBindingSession
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		v, e = f.service.StartQQBinding(ctx, tx, f.org, f.actor, b.ID, in)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return v
}
func (f *qqPG) binding(id string) QQBindingSession {
	f.t.Helper()
	var v QQBindingSession
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		v, e = f.service.GetQQBindingSession(ctx, tx, f.org, f.actor, id)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return v
}
func (f *qqPG) confirm(v QQBindingSession, consent bool) (QQBindingSession, error) {
	var out QQBindingSession
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.ConfirmQQBinding(ctx, tx, f.org, f.actor, v.ID, QQConfirmBindingInput{Version: v.Version, PublicReplyConsent: consent})
		return e
	})
	return out, e
}
func (f *qqPG) counts() (int, int) {
	f.t.Helper()
	var receipts, inputs int
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.qq_message_receipts`).Scan(&receipts); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages`).Scan(&inputs)
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return receipts, inputs
}

func TestQQPrivateCandidateConfirmationAndReplayPG(t *testing.T) {
	f, b, route := newQQReady(t)
	old := qqMessage("C2C", "before-bind", "private-subject", "", "old unbound", "")
	if _, e := f.receive(b, route, old); e != nil {
		t.Fatal(e)
	}
	v := f.startBinding(b, QQStartBindingInput{Purpose: "PRIVATE_CODE", IdempotencyKey: "private-binding-code"})
	if len(v.Code) != 32 {
		t.Fatal("code not cryptographic expected length")
	}
	if _, e := f.confirm(v, false); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("unobserved confirmation", e)
	}
	if _, e := f.receive(b, route, qqMessage("C2C", "binding-command", "private-subject", "", "/openfox-bind "+v.Code, "")); e != nil {
		t.Fatal(e)
	}
	v = f.binding(v.ID)
	if v.Status != "OBSERVED" || v.Code != "" {
		t.Fatal("candidate/secret view", v)
	}
	_, inputs := f.counts()
	if inputs != 0 {
		t.Fatal("binding command executed")
	}
	v, e := f.confirm(v, false)
	if e != nil || v.BindingID == "" {
		t.Fatal(v, e)
	}
	if _, e = f.receive(b, route, old); e != nil {
		t.Fatal(e)
	}
	_, inputs = f.counts()
	if inputs != 0 {
		t.Fatal("old unbound event resurrected")
	}
	message := qqMessage("C2C", "actual-message", "private-subject", "", "actual command", "")
	if _, e = f.receive(b, route, message); e != nil {
		t.Fatal(e)
	}
	message["id"] = "another-transport-id"
	if _, e = f.receive(b, route, message); e != nil {
		t.Fatal(e)
	}
	_, inputs = f.counts()
	if inputs != 1 {
		t.Fatal("business deduplication", inputs)
	}
	message["d"].(map[string]any)["content"] = "conflicting change"
	if _, e = f.receive(b, route, message); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("duplicate parameter conflict", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.RevokeQQBinding(ctx, tx, f.org, f.actor, v.BindingID, 1)
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.confirm(v, false); e != nil {
		t.Fatal("completed acceptance view", e)
	}
	if _, e = f.receive(b, route, qqMessage("C2C", "after-revoke", "private-subject", "", "must not execute", "")); e != nil {
		t.Fatal(e)
	}
	_, inputs = f.counts()
	if inputs != 1 {
		t.Fatal("replay confirmation revived identity")
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var clean bool
		err := tx.QueryRow(ctx, `SELECT bool_and(text='') FROM openfox.qq_message_receipts WHERE disposition IN ('BINDING','IGNORED')`).Scan(&clean)
		if !clean {
			t.Error("binding secret/unbound text retained")
		}
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestQQGroupConsentAndTypedManualBindingPG(t *testing.T) {
	f, b, route := newQQReady(t)
	v := f.startBinding(b, QQStartBindingInput{Purpose: "GROUP_DISCOVERY", IdempotencyKey: "group-discovery-code"})
	if _, e := f.receive(b, route, qqMessage("GROUP", "member-not-admin", "group-owner", "group-one", "/openfox-group "+v.Code, "member")); e != nil {
		t.Fatal(e)
	}
	if got := f.binding(v.ID); got.Status != "PENDING" {
		t.Fatal("member granted group admin")
	}
	if _, e := f.receive(b, route, qqMessage("GROUP", "admin-discovery", "group-owner", "group-one", "/openfox-group "+v.Code, "admin")); e != nil {
		t.Fatal(e)
	}
	v = f.binding(v.ID)
	if _, e := f.confirm(v, false); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("group consent optional", e)
	}
	group, e := f.confirm(v, true)
	if e != nil || group.GroupEpochID == "" {
		t.Fatal(group, e)
	}
	if _, e = f.receive(b, route, qqMessage("GROUP", "member-discovery", "same-subject", "group-one", "discover only", "member")); e != nil {
		t.Fatal(e)
	}
	var receipt string
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_message_receipts WHERE remote_message_id='member-discovery'`).Scan(&receipt)
	})
	if e != nil {
		t.Fatal(e)
	}
	v = f.startBinding(b, QQStartBindingInput{Purpose: "GROUP_ASSIGN", OwnerUserID: f.user, GroupEpochID: group.GroupEpochID, ReceiptID: receipt, IdempotencyKey: "group-member-assign"})
	_, inputs := f.counts()
	if inputs != 0 {
		t.Fatal("mapping before acceptance executed")
	}
	v, e = f.confirm(v, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(b, route, qqMessage("C2C", "same-c2c-subject", "same-subject", "", "must not merge scopes", "")); e != nil {
		t.Fatal(e)
	}
	_, inputs = f.counts()
	if inputs != 0 {
		t.Fatal("group ID merged into C2C identity")
	}
	if _, e = f.receive(b, route, qqMessage("GROUP", "group-command", "same-subject", "group-one", "real group command", "member")); e != nil {
		t.Fatal(e)
	}
	_, inputs = f.counts()
	if inputs != 1 {
		t.Fatal("accepted group not queued", inputs)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisableQQGroup(ctx, tx, f.org, f.actor, group.GroupEpochID, 1)
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(b, route, qqMessage("GROUP", "disabled-command", "same-subject", "group-one", "must not execute", "member")); e != nil {
		t.Fatal(e)
	}
	_, inputs = f.counts()
	if inputs != 1 {
		t.Fatal("disabled group queued")
	}
}
func TestQQRawSignatureChallengeAndRollbackPG(t *testing.T) {
	f, b, route := newQQReady(t)
	raw, _ := json.Marshal(qqMessage("C2C", "raw-test", "other", "", "safe", ""))
	h := qqSigned(b.AppID, raw)
	for _, mode := range []string{"wrong-app", "mutated-body", "wrong-route"} {
		t.Run(mode, func(t *testing.T) {
			body := append([]byte(nil), raw...)
			header := h.Clone()
			hash := TokenHash(route)
			if mode == "wrong-app" {
				header.Set("X-Bot-Appid", "another-app")
			}
			if mode == "mutated-body" {
				body = append(body, ' ')
			}
			if mode == "wrong-route" {
				hash = TokenHash("other-route")
			}
			e := f.run(func(ctx context.Context, tx pgx.Tx) error {
				_, e := f.service.ReceiveQQWebhook(ctx, tx, f.org, b.ID, hash, header, body)
				return e
			})
			if e == nil {
				t.Fatal("untrusted callback accepted")
			}
		})
	}
	before, _ := f.counts()
	stop := errors.New("simulated commit prevention")
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ReceiveQQWebhook(ctx, tx, f.org, b.ID, TokenHash(route), h, raw)
		if e != nil {
			return e
		}
		return stop
	})
	if !errors.Is(e, stop) {
		t.Fatal(e)
	}
	after, _ := f.counts()
	if before != after {
		t.Fatal("rollback left a receipt")
	}
	if _, e = f.receive(b, route, qqMessage("C2C", "after-failure", "other", "", "retry after rollback", "")); e != nil {
		t.Fatal(e)
	}
}
