package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/integrations/qq"
)

type qqReplyPG struct {
	*qqPG
	bot                                            QQBot
	callback, conversation, runID, outbox, binding string
	worker                                         *IMWorker
}

func newQQReplyPG(t *testing.T) *qqReplyPG {
	f, b, callback := newQQReady(t)
	binding := f.bindPrivate(b, callback, "original-subject", "reply-fixture-binding")
	x := &qqReplyPG{qqPG: f, bot: b, callback: callback, binding: binding.BindingID, worker: f.worker(), runID: refsTestID(t)}
	for _, id := range []string{"original-one", "original-two"} {
		if _, e := f.receive(b, callback, qqMessage("C2C", id, "original-subject", "", "original request "+id, "")); e != nil {
			t.Fatal(e)
		}
		if e := x.worker.processQQInbound(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT conversation_id::text FROM openfox.qq_routes`).Scan(&x.conversation); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,finished_at) SELECT v.organization_id,$1::uuid,$2::uuid,$3::uuid,v.agent_id,v.computer_id,v.grant_id,v.config_version,g.permission_version,$4::uuid,'SUCCEEDED',clock_timestamp() FROM openfox.agent_config_versions v JOIN openfox.device_grants g ON (g.organization_id,g.id)=(v.organization_id,v.grant_id) WHERE v.organization_id=openfox_private.current_organization_id() AND v.agent_id=$5::uuid AND v.config_version=1`, x.runID, f.space, x.conversation, f.user, f.agent); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE openfox.conversation_inputs SET run_id=$2::uuid,status='ASSIGNED',assigned_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid`, x.conversation, x.runID); e != nil {
			return e
		}
		m, e := dispatch.FinalizeConversation(ctx, tx, execution.Run{ID: x.runID, OrganizationID: f.org, ConversationID: x.conversation, AgentID: f.agent, SpaceID: f.space, ActorID: f.user, State: execution.StateSucceeded}, dispatch.AuthorAgent, "Controlled terminal reply", false)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_reply_outbox WHERE message_id=$1::uuid AND source_run_id=$2::uuid AND source_terminal`, m, x.runID).Scan(&x.outbox)
	})
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func (x *qqReplyPG) queue() {
	x.t.Helper()
	if e := x.worker.processQQReplies(context.Background(), x.org); e != nil {
		x.t.Fatal(e)
	}
}
func (x *qqReplyPG) send() {
	x.t.Helper()
	if e := x.worker.processQQDeliveries(context.Background(), x.org); e != nil {
		x.t.Fatal(e)
	}
}
func (x *qqReplyPG) state() string {
	x.t.Helper()
	var s string
	e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM openfox.qq_replies WHERE outbox_id=$1::uuid`, x.outbox).Scan(&s)
	})
	if e != nil {
		x.t.Fatal(e)
	}
	return s
}

func TestQQTerminalReplyOriginalRunPG(t *testing.T) {
	x := newQQReplyPG(t)
	if _, e := x.receive(x.bot, x.callback, qqMessage("C2C", "later-unassigned", "original-subject", "", "not part of earlier Run", "")); e != nil {
		t.Fatal(e)
	}
	if e := x.worker.processQQInbound(context.Background(), x.org); e != nil {
		t.Fatal(e)
	}
	var posts atomic.Int32
	x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.URL.Path != "/v2/users/original-subject/messages" || r.Header.Get("Authorization") != "QQBot controlled-token" {
			t.Errorf("wrong route/token %s", r.URL.Path)
		}
		var body struct {
			Content  string `json:"content"`
			ID       string `json:"msg_id"`
			Sequence int    `json:"msg_seq"`
			Type     int    `json:"msg_type"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Content != "Controlled terminal reply" || body.ID != "original-two" || body.Sequence != 1 || body.Type != 0 {
			t.Errorf("wrong original Run reply: %+v", body)
		}
		_, _ = w.Write([]byte(`{"id":"sent-owned","timestamp":"2026-11-15T00:00:00Z"}`))
	})
	x.queue()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- x.worker.processQQDeliveries(context.Background(), x.org) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	x.queue()
	x.send()
	if x.state() != "DONE" || posts.Load() != 1 {
		t.Fatal("duplicate or missing terminal", x.state(), posts.Load())
	}
	t.Log("actual FinalizeConversation source_terminal/source_run_id → last original Run input original-two; later unassigned receipt ignored; two workers one TLS POST")
}

func TestQQUnknownAndRevokeDeliveryPG(t *testing.T) {
	for _, mode := range []string{"unknown", "revoke", "expired", "not_terminal"} {
		t.Run(mode, func(t *testing.T) {
			x := newQQReplyPG(t)
			var posts atomic.Int32
			x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) { posts.Add(1); _, _ = w.Write([]byte(`null`)) })
			if mode == "not_terminal" {
				if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
					_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET source_terminal=false WHERE id=$1::uuid`, x.outbox)
					return e
				}); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "expired" {
				if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
					_, e := tx.Exec(ctx, `UPDATE openfox.qq_message_receipts SET sent_at=clock_timestamp()-interval '6 minutes' WHERE remote_message_id='original-two'`)
					return e
				}); e != nil {
					t.Fatal(e)
				}
			}
			x.queue()
			if mode == "revoke" {
				if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
					return x.service.RevokeQQBinding(ctx, tx, x.org, x.actor, x.binding, 1)
				}); e != nil {
					t.Fatal(e)
				}
			}
			x.send()
			x.send()
			if mode == "unknown" {
				if posts.Load() != 1 || x.state() != "UNKNOWN" {
					t.Fatal("unknown retried", posts.Load(), x.state())
				}
				return
			}
			if posts.Load() != 0 {
				t.Fatal("unapproved business send", posts.Load())
			}
			var state, reason string
			if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE id=$1::uuid`, x.outbox).Scan(&state, &reason)
			}); e != nil {
				t.Fatal(e)
			}
			if state != "SKIPPED" {
				t.Fatal(mode, state, reason)
			}
		})
	}
}

func TestQQLateToken401FencePG(t *testing.T) {
	x := newQQReplyPG(t)
	x.queue()
	var job qqSend
	var claimed bool
	if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		var id string
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_replies`).Scan(&id); e != nil {
			return e
		}
		job, claimed, e = x.worker.prepareQQSend(ctx, tx, x.org, id)
		return e
	}); e != nil || !claimed {
		t.Fatal(e, claimed)
	}
	var refreshes atomic.Int32
	x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "getAppAccessToken") {
			t.Error("unexpected refresh path")
		}
		refreshes.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"new-controlled-token","expires_in":3600}`))
	})
	if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET refresh_after=clock_timestamp() WHERE bot_id=$1::uuid`, x.bot.ID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := x.worker.processQQTokens(context.Background(), x.org); e != nil {
		t.Fatal(e)
	}
	if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		return x.worker.finishQQSend(ctx, tx, x.org, job, qq.SentMessage{}, qq.ErrAuthentication)
	}); e != nil {
		t.Fatal(e)
	}
	var token qq.Token
	var version uint64
	if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		token, e = x.service.qqStoredToken(ctx, tx, x.org, x.bot.ID)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT credential_version FROM openfox.qq_bot_controls WHERE bot_id=$1::uuid`, x.bot.ID).Scan(&version)
	}); e != nil {
		t.Fatal(e)
	}
	if token.AccessToken != "new-controlled-token" || version <= job.CredentialVersion || refreshes.Load() != 1 {
		t.Fatal("late old 401 erased refreshed token", version, job.CredentialVersion, refreshes.Load())
	}
}

func TestQQLatePositiveAfterDisconnectPG(t *testing.T) {
	x := newQQReplyPG(t)
	x.queue()
	entered, release := make(chan struct{}), make(chan struct{})
	var posts atomic.Int32
	x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		close(entered)
		<-release
		_, _ = w.Write([]byte(`{"id":"late-positive","timestamp":"2026-11-15T00:00:00Z"}`))
	})
	done := make(chan error, 1)
	go func() { done <- x.worker.processQQDeliveries(context.Background(), x.org) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("send did not start")
	}
	e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		b, e := loadQQBot(ctx, tx, x.bot.ID)
		if e != nil {
			return e
		}
		_, e = x.service.DisconnectQQBot(ctx, tx, x.org, x.actor, b.ID, b.Version)
		return e
	})
	close(release)
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if x.state() != "DONE" || posts.Load() != 1 {
		t.Fatal("lost actual late receipt")
	}
	var state string
	if e = x.run(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT state FROM openfox.qq_bot_controls WHERE bot_id=$1::uuid`, x.bot.ID).Scan(&state)
	}); e != nil || state != "DISCONNECTED" {
		t.Fatal("late receipt restored bot", state, e)
	}
}

func TestQQReplyOneBoundedTextPG(t *testing.T) {
	x := newQQReplyPG(t)
	if e := x.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.messages SET body=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=(SELECT message_id FROM openfox.im_reply_outbox WHERE id=$1::uuid)`, x.outbox, strings.Repeat("界", 9000))
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var posts atomic.Int32
	x.worker.Service.QQ = qqTLS(t, func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		var v struct {
			Content string `json:"content"`
		}
		if json.NewDecoder(r.Body).Decode(&v) != nil || utf8.RuneCountInString(v.Content) != 8000 || !strings.HasSuffix(v.Content, "…（QQ 回复已截断）") {
			t.Error("unbounded terminal text")
		}
		_, _ = w.Write([]byte(`{"id":"bounded","timestamp":"2026-11-15T00:00:00Z"}`))
	})
	x.queue()
	x.send()
	if posts.Load() != 1 || x.state() != "DONE" {
		t.Fatal("reply fragmented or missing")
	}
}
