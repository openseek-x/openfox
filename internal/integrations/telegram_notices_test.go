package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
)

func TestTelegramBindingNoticeRateAndTopicPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	for n := int64(800); n < 802; n++ {
		out, e := f.receive(b, n, 987654, n, 987654, "hello", nil)
		if e != nil || out.Result != "identity_required" {
			t.Fatalf("unbound %+v %v", out, e)
		}
	}
	w := IMWorker{Pool: tgWorkerRunner{f}, Service: f.service}
	if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls, text, reply := f.sendCalls, f.sentText, f.sentReply
	f.mu.Unlock()
	if calls != 1 || text != telegramBindingNotice || strings.Contains(text, f.org) || reply != 800 {
		t.Fatalf("notice %d %s %d", calls, text, reply)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID).Scan(&n)
		if n != 0 {
			t.Fatal("unbound entered runtime queue")
		}
		return e
	})
	var coordinates telegramWebhookCoordinates
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadTelegramControl(ctx, tx, b.ID, false)
		if e != nil {
			return e
		}
		coordinates, e = f.service.telegramCoordinates(f.org, r)
		return e
	})
	raw, _ := json.Marshal(map[string]any{"update_id": 802, "message": map[string]any{"message_id": 802, "date": 1, "message_thread_id": 12, "chat": map[string]any{"id": -100123, "type": "supergroup", "is_forum": true}, "from": map[string]any{"id": 987654, "is_bot": false, "first_name": "Person"}, "text": "topic"}})
	for n := 0; n < 2; n++ {
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			out, e := f.service.ReceiveTelegram(ctx, tx, TelegramWebhookLookup{f.org, b.ID}, coordinates.Route, http.Header{"X-Telegram-Bot-Api-Secret-Token": []string{coordinates.Secret}}, raw)
			if e == nil && out.Result != "unsupported_topic" {
				t.Fatalf("topic %+v", out)
			}
			return e
		})
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		v, e := f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
		if e == nil && v.UnsupportedTopicCount != 1 {
			t.Fatalf("topic count %d", v.UnsupportedTopicCount)
		}
		return e
	})
}

func tgEnableGroup(t *testing.T, f *tgFixture, b TelegramBot) TelegramGroup {
	t.Helper()
	cmd := "/openfox_connect@Current_Bot"
	out, e := f.receive(b, 900, -1001234567890, 900, 987654, cmd, []any{map[string]any{"type": "bot_command", "offset": 0, "length": len(cmd)}})
	if e != nil || out.Result != "group_discovered" {
		t.Fatalf("discover %+v %v", out, e)
	}
	var target TelegramGroupTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		d, e := f.service.TelegramGroupDiscoveries(ctx, tx, f.org, f.actor, b.ID)
		if e != nil {
			return e
		}
		if len(d) != 1 {
			t.Fatal("missing discovery")
		}
		target, e = f.service.PrepareTelegramGroup(ctx, tx, f.org, f.actor, b.ID, TelegramGroupInput{DiscoveryID: d[0].ID, ConnectionVersion: b.Version, Enabled: true, PublicReplyConsent: true, BackgroundEnabled: true})
		return e
	})
	target, e = f.service.VerifyTelegramGroup(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	var g TelegramGroup
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		g, e = f.service.SaveTelegramGroup(ctx, tx, f.org, f.actor, target)
		return e
	})
	return g
}

func TestTelegramGroupContextRunAndNativeNoSendPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	f.bind(b, 899)
	group := tgEnableGroup(t, f, b)
	out, e := f.receive(b, 901, int64(group.ChatID), 901, 777777, "untrusted background", nil)
	if e != nil || out.Result != "background_cached" {
		t.Fatalf("context %+v %v", out, e)
	}
	out, e = f.receive(b, 902, int64(group.ChatID), 902, 987654, "@Current_Bot summarize", []any{map[string]any{"type": "mention", "offset": 0, "length": 12}})
	if e != nil || out.Result != "queued" {
		t.Fatalf("mention %+v %v", out, e)
	}
	var p telegramInputProof
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT inbound_id::text FROM openfox.telegram_update_receipts WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id='902'`, b.ConnectionID).Scan(&id); e != nil {
			return e
		}
		var e error
		p, e = loadTelegramCandidate(ctx, tx, id)
		return e
	})
	w := IMWorker{Pool: tgWorkerRunner{f}, Service: f.service}
	if e = w.ProcessTelegramInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var body string
		e := tx.QueryRow(ctx, `SELECT body FROM openfox.messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.inbound).Scan(&body)
		if e == nil && (!strings.Contains(body, "未可信第三方内容") || !strings.Contains(body, "untrusted background")) {
			t.Fatal("background missing untrusted boundary")
		}
		return e
	})
	tgFinalize(t, f, b, p, "群回复")
	if e = w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls, chat, reply := f.sendCalls, f.sentChat, f.sentReply
	f.mu.Unlock()
	if calls != 1 || chat != int64(group.ChatID) || reply != 902 {
		t.Fatalf("group route %d %d %d", calls, chat, reply)
	}
	// Native UI input has its own immutable OPENFOX authorization and an actual
	// separate Run. It must not become an unsolicited message to the remote group.
	native := refsTestID(t)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := (conversations.Service{}).PostMessage(ctx, tx, f.org, p.conversation, conversations.Actor(f.actor), conversations.MessageInput{ID: native, Body: "local UI input", ClientMessageID: "native-" + native, AuthenticatedUserInput: true, Mentions: []conversations.Mention{{AgentID: b.Agent.ID}}})
		return e
	})
	nativeProof := p
	nativeProof.inbound = native
	id := tgFinalize(t, f, b, nativeProof, "仅本地回复")
	tgClearLimits(f, b)
	if e = w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls = f.sendCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("native UI posted to Telegram")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, note string
		e := tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&status, &note)
		if e == nil && (status != "SKIPPED" || note != "source_unavailable") {
			t.Fatalf("UI %s/%s", status, note)
		}
		return e
	})
}
