package integrations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/integrations/telegram"
)

// Actual dispatch assigns the materialized input to a Run. Only the synthetic
// terminal result is supplied locally; no model, device or vendor is contacted.
func tgFinalize(t *testing.T, f *tgFixture, b TelegramBot, p telegramInputProof, body string) string {
	t.Helper()
	var outbox string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		created, rejected, e := (dispatch.Store{}).MaterializeInputs(ctx, tx, f.org, 10)
		if e != nil {
			return e
		}
		if created != 1 || rejected != 0 {
			t.Fatalf("materialize runs %d/%d", created, rejected)
		}
		var r execution.Run
		e = tx.QueryRow(ctx, `SELECT r.id::text,r.space_id::text,r.conversation_id::text,r.agent_id::text,r.actor_id::text FROM openfox.runs r JOIN openfox.conversation_inputs i ON i.organization_id=r.organization_id AND i.run_id=r.id WHERE r.organization_id=openfox_private.current_organization_id() AND i.message_id=$1::uuid`, p.inbound).Scan(&r.ID, &r.SpaceID, &r.ConversationID, &r.AgentID, &r.ActorID)
		if e != nil {
			return e
		}
		r.OrganizationID = f.org
		r.State = execution.StateSucceeded
		if _, e = tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.ID); e != nil {
			return e
		}
		message, e := dispatch.FinalizeConversation(ctx, tx, r, dispatch.AuthorAgent, body, false)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid AND source_run_id=$2::uuid`, message, r.ID).Scan(&outbox)
	})
	return outbox
}

func tgReplyReady(t *testing.T, f *tgFixture, b TelegramBot, number int64) (telegramInputProof, string, *IMWorker) {
	t.Helper()
	p := tgQueued(t, f, b, number)
	w := &IMWorker{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "tg-sender"}
	if e := w.ProcessTelegramInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	return p, tgFinalize(t, f, b, p, "合成测试结果，不调用真实模型。"), w
}

func TestTelegramReplyUsesOriginalRunAndUnknownPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	f.bind(b, 300)
	_, outbox, w := tgReplyReady(t, f, b, 301)
	// A later inbound is intentionally queued before the old result is sent.
	// The SQL generic trigger may point to the latest message; this sender must
	// overwrite that advisory route from the original Run's immutable input.
	tgQueued(t, f, b, 302)
	if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls, reply, chat, text := f.sendCalls, f.sentReply, f.sentChat, f.sentText
	f.mu.Unlock()
	if calls != 1 || reply != 301 || chat != 987654 || !strings.Contains(text, "https://openfox.example.test/o/") {
		t.Fatalf("wrong original reply calls=%d reply=%d chat=%d", calls, reply, chat)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, remote string
		e := tx.QueryRow(ctx, `SELECT status,remote_message_id FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&status, &remote)
		if e == nil && (status != "DONE" || remote != "8001") {
			t.Fatalf("send %s/%s", status, remote)
		}
		return e
	})
	// Materialize and finalize the pending next message, then lose the successful
	// vendor response. Repeated workers and an expired lease cannot resend it.
	if e := w.ProcessTelegramInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var p telegramInputProof
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT inbound_id::text FROM openfox.telegram_update_receipts WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id='302'`, b.ConnectionID).Scan(&id); e != nil {
			return e
		}
		var e error
		p, e = loadTelegramCandidate(ctx, tx, id)
		return e
	})
	outbox = tgFinalize(t, f, b, p, "第二条回复")
	f.mu.Lock()
	f.loseSend = true
	f.mu.Unlock()
	tgClearLimits(f, b)
	for i := 0; i < 3; i++ {
		if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	f.mu.Lock()
	calls = f.sendCalls
	f.mu.Unlock()
	if calls != 2 {
		t.Fatalf("unknown resent %d", calls)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, note string
		e := tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&status, &note)
		if e == nil && (status != "FAILED" || note != "delivery_unknown") {
			t.Fatalf("unknown %s/%s", status, note)
		}
		return e
	})
}

func tgClearLimits(f *tgFixture, b TelegramBot) {
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET send_not_before=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.telegram_chat_limits SET not_before=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID)
		return e
	})
}

func TestTelegramReplyRateLimitAndRevocationPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	identity, _ := f.bind(b, 400)
	_, outbox, w := tgReplyReady(t, f, b, 401)
	f.mu.Lock()
	f.rateSend = 900000
	f.mu.Unlock()
	if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, note string
		var valid bool
		e := tx.QueryRow(ctx, `SELECT status,last_error,next_attempt_at>clock_timestamp()+interval '899990 seconds' FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&status, &note, &valid)
		if e == nil && (status != "PENDING" || note != "rate_limited" || !valid) {
			t.Fatalf("rate %s %s %v", status, note, valid)
		}
		return e
	})
	if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls := f.sendCalls
	f.rateSend = 0
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("rate ignored")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.UnbindTelegram(ctx, tx, f.org, f.actor, identity.ID, identity.Version)
	})
	tgClearLimits(f, b)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET next_attempt_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox)
		return e
	})
	if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls = f.sendCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("revoked identity sent")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, note string
		e := tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&status, &note)
		if e == nil && (status != "SKIPPED" || note != "authorization_changed") {
			t.Fatalf("revoke %s/%s", status, note)
		}
		return e
	})
}

func TestTelegramReplyTextUTF16(t *testing.T) {
	text := telegramReplyText(strings.Repeat("😀", 3000), "https://openfox.example.test/o/org/chat/id")
	if telegram.UTF16Len(text) > 4096 || !strings.HasPrefix(text, strings.Repeat("😀", 1750)+"\n\n") {
		t.Fatal("UTF16 truncation split or overflow")
	}
}

func TestTelegramReplyConcurrentClaimAndRevocationPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	identity, _ := f.bind(b, 1000)
	_, _, w := tgReplyReady(t, f, b, 1001)
	entered, release := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.sendEntered = entered
	f.sendRelease = release
	f.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	result := make(chan error, 1)
	go func() { result <- w.ProcessTelegramReplies(context.Background(), f.org) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not start")
	}
	other := IMWorker{Pool: tgWorkerRunner{f}, Service: f.service, Instance: "tg-other-worker"}
	if e := other.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	// The external request is already underway. Revocation must not deadlock on
	// a network-held transaction, nor authorize any subsequent message send.
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.UnbindTelegram(ctx, tx, f.org, f.actor, identity.ID, identity.Version)
	})
	close(release)
	select {
	case e := <-result:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send settlement blocked")
	}
	f.mu.Lock()
	calls := f.sendCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("parallel duplicate %d", calls)
	}
}

func TestTelegramExpiredSendingNeverRepostsPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	f.bind(b, 1100)
	_, outbox, w := tgReplyReady(t, f, b, 1101)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		c, e := w.claimTelegramReply(ctx, tx, f.org, b.ID, outbox)
		if e == nil && c == nil {
			t.Fatal("no claim")
		}
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox)
		return e
	})
	for n := 0; n < 2; n++ {
		if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	f.mu.Lock()
	calls := f.sendCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("expired SENDING was sent")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, note string
		e := tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, outbox).Scan(&status, &note)
		if e == nil && (status != "FAILED" || note != "delivery_unknown") {
			t.Fatalf("expiry %s/%s", status, note)
		}
		return e
	})
}

func TestTelegramCurrentAndOldToken401PG(t *testing.T) {
	t.Run("current stops bot", func(t *testing.T) {
		f := newTGFixture(t, true)
		b := f.control(f.connect())
		f.bind(b, 1200)
		p, _, w := tgReplyReady(t, f, b, 1201)
		// A second durable reply is already waiting before the first 401.
		var run execution.Run
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			e := tx.QueryRow(ctx, `SELECT id::text,space_id::text,conversation_id::text,agent_id::text,actor_id::text FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid`, p.conversation).Scan(&run.ID, &run.SpaceID, &run.ConversationID, &run.AgentID, &run.ActorID)
			if e != nil {
				return e
			}
			run.OrganizationID = f.org
			run.State = execution.StateSucceeded
			_, e = dispatch.FinalizeConversation(ctx, tx, run, dispatch.AuthorAgent, "later reply", false)
			return e
		})
		f.mu.Lock()
		f.sendErrorStatus = 401
		f.mu.Unlock()
		if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
		tgClearLimits(f, b)
		if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
		f.mu.Lock()
		calls := f.sendCalls
		f.mu.Unlock()
		if calls != 1 {
			t.Fatalf("401 repeated sends %d", calls)
		}
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			v, e := f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
			if e == nil && (v.State != "ATTENTION" || v.LastError != "credentials_invalid") {
				t.Fatalf("401 state %+v", v)
			}
			return e
		})
	})
	t.Run("old response cannot stop replacement", func(t *testing.T) {
		f := newTGFixture(t, true)
		b := f.control(f.connect())
		f.bind(b, 1300)
		_, _, w := tgReplyReady(t, f, b, 1301)
		entered, release := make(chan struct{}), make(chan struct{})
		f.mu.Lock()
		f.sendEntered = entered
		f.sendRelease = release
		f.sendErrorStatus = 401
		f.mu.Unlock()
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		result := make(chan error, 1)
		go func() { result <- w.ProcessTelegramReplies(context.Background(), f.org) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("old send did not start")
		}
		input := f.input()
		input.BotID = b.ID
		input.Version = b.Version
		var target TelegramConnectTarget
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			target, e = f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, input)
			return e
		})
		proof, e := f.service.VerifyTelegramConnection(context.Background(), target)
		if e != nil {
			t.Fatal(e)
		}
		var prep TelegramPreparation
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			prep, e = f.service.SaveTelegramPreparation(ctx, tx, f.org, f.actor, target, proof)
			return e
		})
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			var e error
			b, e = f.service.ConfirmTelegramPreparation(ctx, tx, f.org, f.actor, prep.ID, TelegramConfirmInput{Version: prep.Version, RemoteBotID: prep.RemoteBotID.String()})
			return e
		})
		b = f.control(b)
		close(release)
		select {
		case e := <-result:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("old settlement blocked")
		}
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			v, e := f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
			if e == nil && (v.State != "ACTIVE" || v.LastError != "") {
				t.Fatalf("old401 stopped new token %+v", v)
			}
			return e
		})
	})
}
