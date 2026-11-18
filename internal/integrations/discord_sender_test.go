package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/integrations/discord"
	"strings"
	"testing"
)

func (f *discordFixture) rejectedReply() (*IMWorker, DiscordBot, string) {
	f.t.Helper()
	b := f.bound()
	ch := f.enable(b)
	input := f.queue(b)
	m := f.manager()
	w := &IMWorker{Pool: m.Pool, Service: f.service}
	if e := w.ProcessDiscordInbound(context.Background(), f.org); e != nil {
		f.t.Fatal(e)
	}
	message := refsTestID(f.t)
	var id string
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var ci string
		if e := tx.QueryRow(ctx, `UPDATE openfox.conversation_inputs SET status='REJECTED',assigned_at=clock_timestamp() WHERE message_id=$1::uuid RETURNING id::text`, input).Scan(&ci); e != nil {
			return e
		}
		var seq int
		if e := tx.QueryRow(ctx, `UPDATE openfox.conversations SET next_message_seq=next_message_seq+1 WHERE id=$1::uuid RETURNING next_message_seq-1`, ch.ConversationID).Scan(&seq); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,body) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'SYSTEM','fixture rejection')`, message, ch.ConversationID, seq); e != nil {
			return e
		}
		if e := conversations.RecordDiscordRejectedInputs(ctx, tx, ch.ConversationID, message, []string{ci}); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_reply_outbox WHERE message_id=$1::uuid`, message).Scan(&id)
	})
	return w, b, id
}
func TestDiscordReplyOriginalAndUnknownPG(t *testing.T) {
	for _, mode := range []string{"success", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiscordFixture(t)
			w, _, id := f.rejectedReply()
			f.sendMode = mode
			if e := w.ProcessDiscordReplies(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			if e := w.ProcessDiscordReplies(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			f.mu.Lock()
			posts := f.posts
			body := f.lastPost
			f.mu.Unlock()
			if posts != 1 {
				t.Fatal("POST count", posts)
			}
			if body["content"] != "fixture rejection" || body["enforce_nonce"] != true || body["message_reference"].(map[string]any)["message_id"] != "9000001" {
				t.Fatal("wrong original reply", body)
			}
			f.must(func(ctx context.Context, tx pgx.Tx) error {
				var state, err string
				e := tx.QueryRow(ctx, `SELECT status,last_error FROM openfox.im_reply_outbox WHERE id=$1::uuid`, id).Scan(&state, &err)
				if e == nil && ((mode == "success" && state != "DONE") || (mode == "unknown" && (state != "FAILED" || err != "delivery_unknown"))) {
					t.Fatal(state, err)
				}
				return e
			})
		})
	}
}
func TestDiscordReply429KeepsNoncePG(t *testing.T) {
	f := newDiscordFixture(t)
	w, b, id := f.rejectedReply()
	f.sendMode = "rate"
	if e := w.ProcessDiscordReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	nonce := f.lastPost["nonce"]
	f.sendMode = "success"
	f.mu.Unlock()
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE openfox.im_reply_outbox SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, id); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.discord_rate_limits SET not_before=clock_timestamp()-interval '1 second' WHERE bot_id=$1::uuid`, b.ID)
		return e
	})
	if e := w.ProcessDiscordReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 2 || f.lastPost["nonce"] != nonce {
		t.Fatal("known rejected retry changed nonce")
	}
}
func TestDiscordReplyUTF16(t *testing.T) {
	s := discordReplyText(strings.Repeat("😀", 2000), "https://openfox.example.test/c/one")
	if discord.UTF16Len(s) > 2000 || !strings.Contains(s, "完整结果") || strings.ContainsRune(s, '\ufffd') {
		t.Fatal("invalid Unicode truncation")
	}
}

func TestDiscordReplyForbiddenPausesEpochPG(t *testing.T) {
	f := newDiscordFixture(t)
	w, b, _ := f.rejectedReply()
	f.sendMode = "forbidden"
	if e := w.ProcessDiscordReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var paused bool
		e := tx.QueryRow(ctx, `SELECT paused FROM openfox.discord_channel_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND enabled`, b.ID).Scan(&paused)
		if e == nil && !paused {
			t.Fatal("POST 403 did not pause scope")
		}
		return e
	})
	if e := w.ProcessDiscordReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatal("denied message retried", f.posts)
	}
}
