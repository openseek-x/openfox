package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/integrations/discord"
	"testing"
)

func (f *discordFixture) queue(b DiscordBot) string {
	f.t.Helper()
	var id string
	var message discord.Message
	if e := json.Unmarshal(discordGatewayMessage(f.remote, ""), &message); e != nil {
		f.t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadDiscordControl(ctx, tx, b.ID, true)
		if e != nil {
			return e
		}
		if e = receiveDiscordMessage(ctx, tx, r, message); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT id::text FROM openfox.discord_message_receipts WHERE bot_id=$1::uuid AND remote_message_id='9000001'`, b.ID).Scan(&id)
	})
	return id
}
func TestDiscordInboundMaterializationPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.bound()
	ch := f.enable(b)
	id := f.queue(b)
	m := f.manager()
	w := &IMWorker{Pool: m.Pool, Service: f.service, Instance: "i17-worker"}
	if e := w.ProcessDiscordInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, result, body, source string
		var n int
		e := tx.QueryRow(ctx, `SELECT q.status,p.result,m.body,i.source_kind FROM openfox.im_inbound_messages q JOIN openfox.discord_message_receipts p ON p.id=q.id AND p.organization_id=q.organization_id JOIN openfox.messages m ON m.id=q.message_id AND m.organization_id=q.organization_id JOIN openfox.discord_inputs i ON i.message_id=m.id AND i.organization_id=m.organization_id WHERE q.id=$1::uuid`, id).Scan(&status, &result, &body, &source)
		if e != nil {
			return e
		}
		if status != "DONE" || result != "MATERIALIZED" || body != "create task" || source != "REMOTE" {
			t.Fatal(status, result, body, source)
		}
		e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid`, ch.ConversationID).Scan(&n)
		if e == nil && n != 1 {
			t.Fatal("duplicate input")
		}
		return e
	})
	if e := w.ProcessDiscordInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := (conversations.Service{}).PostMessage(ctx, tx, f.org, ch.ConversationID, conversations.Actor(f.actor), conversations.MessageInput{ID: id, Body: "create task", ClientMessageID: "discord:" + b.ID + ":" + id, AuthenticatedUserInput: true, Mentions: []conversations.Mention{{AgentID: b.Agent.ID}}})
		return e
	})
	if !errors.Is(e, conversations.ErrInvalidRequest) {
		t.Fatal("UI could impersonate remote input", e)
	}
}
func TestDiscordInboundEpochChangedDuringHTTPPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.bound()
	ch := f.enable(b)
	id := f.queue(b)
	m := f.manager()
	w := &IMWorker{Pool: m.Pool, Service: f.service}
	var p *discordInbound
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = w.claimDiscordInbound(ctx, tx, f.org, id)
		return e
	})
	message, body, e := f.service.verifyDiscordInbound(context.Background(), *p)
	if e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisableDiscordChannel(ctx, tx, f.org, f.actor, b.ID, ch.ID, ch.Version)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return w.finishDiscordInbound(ctx, tx, f.org, *p, message, body, nil)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var n int
		if e := tx.QueryRow(ctx, `SELECT status FROM openfox.im_inbound_messages WHERE id=$1::uuid`, id).Scan(&status); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.discord_inputs WHERE message_id=$1::uuid`, id).Scan(&n); e != nil {
			return e
		}
		if status != "IGNORED" || n != 0 {
			t.Fatal("revoked HTTP proof materialized", status, n)
		}
		return nil
	})
}
