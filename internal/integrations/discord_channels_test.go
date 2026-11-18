package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
)

func (f *discordFixture) bound() DiscordBot {
	b := f.connect()
	v := f.candidate(b)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ConfirmDiscordBinding(ctx, tx, f.org, f.actor, v.ID, v.Version)
		return e
	})
	return b
}
func (f *discordFixture) channelPlan(b DiscordBot) (DiscordChannelTarget, DiscordChannelProof) {
	f.t.Helper()
	var p DiscordChannelTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.PrepareDiscordChannel(ctx, tx, f.org, f.actor, b.ID, DiscordChannelInput{GuildID: "5000001", ChannelID: "7000002", PublicReplyConsent: true, HistoryConsent: true})
		return e
	})
	proof, e := f.service.VerifyDiscordChannel(context.Background(), p)
	if e != nil {
		f.t.Fatal(e)
	}
	return p, proof
}
func (f *discordFixture) enable(b DiscordBot) DiscordChannel {
	p, proof := f.channelPlan(b)
	var ch DiscordChannel
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		ch, e = f.service.EnableDiscordChannel(ctx, tx, f.org, f.actor, p, proof)
		return e
	})
	return ch
}
func TestDiscordChannelEpochPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.bound()
	ch := f.enable(b)
	if ch.Version != 1 || ch.ConversationID == "" {
		t.Fatal("missing epoch")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisableDiscordChannel(ctx, tx, f.org, f.actor, b.ID, ch.ID, ch.Version)
	})
	next := f.enable(b)
	if next.ID == ch.ID || next.ConversationID == ch.ConversationID {
		t.Fatal("reopening reused old epoch/history")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var visibility string
		if e := tx.QueryRow(ctx, `SELECT visibility FROM openfox.conversations WHERE id=$1::uuid`, next.ConversationID).Scan(&visibility); e != nil {
			return e
		}
		if visibility != "SHARED" {
			t.Fatal("channel history not SHARED")
		}
		v, e := f.service.DiscordChannels(ctx, tx, f.org, f.actor, b.ID)
		if e == nil && len(v) != 2 {
			t.Fatal("epoch history lost")
		}
		return e
	})
}
func TestDiscordChannelStaleIdentityPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.bound()
	p, proof := f.channelPlan(b)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.UnbindDiscord(ctx, tx, f.org, f.actor, p.identity, p.identityVersion)
	})
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.EnableDiscordChannel(ctx, tx, f.org, f.actor, p, proof)
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("stale identity enabled channel", e)
	}
}
func TestDiscordReplyBudgetPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.connect()
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.im_reply_outbox(organization_id,bot_id,chat_id,body) SELECT openfox_private.current_organization_id(),$1::uuid,'7000002','fixture' FROM generate_series(1,1001)`, b.ID)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var pending, full int
		e := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='PENDING'),count(*) FILTER(WHERE status='FAILED' AND last_error='queue_full' AND finished_at IS NOT NULL) FROM openfox.im_reply_outbox WHERE bot_id=$1::uuid`, b.ID).Scan(&pending, &full)
		if e == nil && (pending != 1000 || full != 1) {
			t.Fatalf("pending=%d failed=%d", pending, full)
		}
		return e
	})
}
