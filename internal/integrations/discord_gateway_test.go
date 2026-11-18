package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/integrations/discord"
	"openfox/internal/platform/postgres"
	"testing"
	"time"
)

func (f *discordFixture) manager() *LongConnManager {
	f.t.Helper()
	p, e := postgres.New(context.Background(), postgres.Config{URL: f.dsn, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 5 * time.Second, LockTimeout: 2 * time.Second})
	if e != nil {
		f.t.Fatal(e)
	}
	f.t.Cleanup(p.Close)
	return &LongConnManager{Pool: p, Service: f.service, Instance: "i17-owner"}
}
func (f *discordFixture) lease(b DiscordBot) {
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.im_connection_states SET lease_owner='i17-owner',lease_expires_at=clock_timestamp()+interval '60 seconds' WHERE connection_id=$1::uuid`, b.ConnectionID)
		return e
	})
}
func discordGatewayMessage(bot, kind string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"id": "9000001", "channel_id": "7000002", "guild_id": "5000001", "author": map[string]any{"id": "6000001", "username": "Person"}, "content": "<@" + bot + "> create task", "type": 0, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "mentions": []any{map[string]any{"id": bot, "username": "Bot", "bot": true}}})
	return raw
}
func TestDiscordDurableGatewayPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.bound()
	_ = f.enable(b)
	f.lease(b)
	m := f.manager()
	key := sessionKey{f.org, b.ConnectionID}
	a, e := m.prepareDiscordAttempt(context.Background(), key)
	if e != nil {
		t.Fatal(e)
	}
	f.gateway = func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		send := func(v any) {
			raw, _ := json.Marshal(v)
			if e := c.Write(ctx, websocket.MessageText, raw); e != nil {
				t.Error(e)
			}
		}
		send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 2000}})
		for {
			_, raw, e := c.Read(ctx)
			if e != nil {
				return
			}
			var v struct {
				Op int `json:"op"`
			}
			_ = json.Unmarshal(raw, &v)
			if v.Op == 2 {
				break
			}
			if v.Op == 1 {
				send(map[string]any{"op": 11, "d": nil})
			}
		}
		send(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"v": 10, "user": map[string]any{"id": f.remote, "username": "Bot", "bot": true}, "application": map[string]any{"id": "18446744073709551615"}, "session_id": "fixture_session", "resume_gateway_url": "wss://gateway.discord.gg"}})
		send(map[string]any{"op": 0, "s": 2, "t": "MESSAGE_CREATE", "d": discordGatewayMessage(f.remote, "")})
		for {
			_, raw, e := c.Read(ctx)
			if e != nil {
				return
			}
			var v struct {
				Op int `json:"op"`
			}
			_ = json.Unmarshal(raw, &v)
			if v.Op == 1 {
				send(map[string]any{"op": 11, "d": nil})
			}
		}
	}
	result, e := f.service.discordClient().RunGateway(context.Background(), discord.GatewayConfig{Token: a.token, BotID: b.RemoteBotID, ApplicationID: b.ApplicationID, Gateway: a.gateway, Intents: discord.IntentGuilds | discord.IntentGuildMessages | discord.IntentDirectMessages | discord.IntentMessageContent, OnDispatch: func(ctx context.Context, d discord.Dispatch) error {
		if d.Sequence == 2 {
			return errors.New("fixture persistence unavailable")
		}
		return m.persistDiscordDispatch(ctx, key, a, d)
	}})
	if e == nil || result.ReceivedSequence != 2 || result.DurableSequence != 1 {
		t.Fatal(result, e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var seq int64
		var n int
		if e := tx.QueryRow(ctx, `SELECT durable_seq FROM openfox.discord_gateway_sessions WHERE connection_id=$1::uuid`, b.ConnectionID).Scan(&seq); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE bot_id=$1::uuid`, b.ID).Scan(&n); e != nil {
			return e
		}
		if seq != 1 || n != 0 {
			t.Fatal("received sequence advanced durable checkpoint", seq, n)
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.discord_gateway_sessions SET identify_not_before=NULL WHERE connection_id=$1::uuid`, b.ConnectionID)
		return e
	})
	next, e := m.prepareDiscordAttempt(context.Background(), key)
	if e != nil {
		t.Fatal(e)
	}
	if next.resume == nil || next.resume.Sequence != 1 || next.fence <= a.fence {
		t.Fatal("wrong resume fence")
	}
	event := discord.Dispatch{Sequence: 2, Type: "MESSAGE_CREATE", Data: discordGatewayMessage(f.remote, "")}
	if e = m.persistDiscordDispatch(context.Background(), key, a, event); !errors.Is(e, ErrDiscordSessionState) {
		t.Fatal("old socket could commit", e)
	}
	if e = m.persistDiscordDispatch(context.Background(), key, next, event); e != nil {
		t.Fatal(e)
	}
	event.Sequence = 3
	if e = m.persistDiscordDispatch(context.Background(), key, next, event); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE bot_id=$1::uuid`, b.ID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Fatal("replay duplicated business message", n)
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.im_connection_states SET lease_owner='other-owner' WHERE connection_id=$1::uuid`, b.ConnectionID)
		return e
	})
	event.Sequence = 4
	if e = m.persistDiscordDispatch(context.Background(), key, next, event); !errors.Is(e, ErrDiscordSessionState) {
		t.Fatal("lost lease could persist", e)
	}
}
func TestDiscordGatewayGapPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.connect()
	f.lease(b)
	m := f.manager()
	key := sessionKey{f.org, b.ConnectionID}
	a, e := m.prepareDiscordAttempt(context.Background(), key)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.finishDiscordAttempt(context.Background(), key, a, discord.GatewayResult{IdentifyRequired: true}, &discord.Error{Kind: discord.ErrGateway, Code: "gateway_session_lost"}); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n, seq int
		var resume []byte
		e := tx.QueryRow(ctx, `SELECT gap_count,durable_seq,resume_sealed FROM openfox.discord_gateway_sessions WHERE connection_id=$1::uuid`, b.ConnectionID).Scan(&n, &seq, &resume)
		if e == nil && (n != 1 || seq != -1 || len(resume) != 0) {
			t.Fatal("gap did not invalidate resume")
		}
		return e
	})
}
