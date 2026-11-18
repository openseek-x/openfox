package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestDiscordManagerLeaseTakeoverStopsOwnedSocketPG(t *testing.T) {
	f := newDiscordFixture(t)
	probe := f.manager()
	stopped, cancelProbe := context.WithCancel(context.Background())
	cancelProbe()
	if err := probe.Run(stopped); err != nil {
		t.Fatalf("Discord-only manager Run configuration: %v", err)
	}
	bot := f.bound()
	_ = f.enable(bot)
	ended := make(chan struct{})
	var once sync.Once
	f.gateway = func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		defer once.Do(func() { close(ended) })
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		send := func(v any) error { raw, _ := json.Marshal(v); return c.Write(ctx, websocket.MessageText, raw) }
		if send(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}}) != nil {
			return
		}
		for {
			_, raw, e := c.Read(ctx)
			if e != nil {
				return
			}
			var v struct {
				Op int `json:"op"`
			}
			if json.Unmarshal(raw, &v) != nil {
				return
			}
			if v.Op == 1 {
				if send(map[string]any{"op": 11, "d": nil}) != nil {
					return
				}
			}
			if v.Op == 2 {
				if send(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{"v": 10, "user": map[string]any{"id": f.remote, "username": "Bot", "bot": true}, "application": map[string]any{"id": "18446744073709551615"}, "session_id": "manager-fixture", "resume_gateway_url": "wss://gateway.discord.gg"}}) != nil {
					return
				}
			}
		}
	}
	m := f.manager()
	key := sessionKey{f.org, bot.ConnectionID}
	first, err := m.acquire(context.Background(), key, true)
	if err != nil || !first.owned || first.secret == "" || first.provider != "discord" {
		t.Fatal("manager acquire", first.owned, first.provider, err)
	}
	other := f.manager()
	other.Instance = "i17-other"
	claim, err := other.acquire(context.Background(), key, true)
	if err != nil || claim.owned {
		t.Fatal("double lease", claim.owned, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.startSession(ctx, key, first)
	defer m.stopSession(key)
	until := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(until) {
		f.must(func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT durable_seq=1 FROM openfox.discord_gateway_sessions WHERE connection_id=$1::uuid`, bot.ConnectionID).Scan(&ready)
		})
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("shared manager did not persist READY")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.im_connection_states SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE connection_id=$1::uuid`, bot.ConnectionID)
		return err
	})
	claim, err = other.acquire(context.Background(), key, true)
	if err != nil || !claim.owned {
		t.Fatal("expired lease takeover", claim.owned, err)
	}
	if !m.renewAll(context.Background()) {
		t.Fatal("old instance retained socket")
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("old socket did not close")
	}
	if _, exists := m.local(key); exists {
		t.Fatal("local lease not removed")
	}
	if _, err = m.prepareDiscordAttempt(context.Background(), key); err == nil {
		t.Fatal("old owner prepared another attempt")
	}
}

func TestDiscordManagerDatabaseFaultStopsOnlyDiscordPG(t *testing.T) {
	f := newDiscordFixture(t)
	bot := f.bound()
	m := f.manager()
	key := sessionKey{f.org, bot.ConnectionID}
	res, err := m.acquire(context.Background(), key, true)
	if err != nil || !res.owned {
		t.Fatal(err)
	}
	install := func(provider string, key sessionKey) context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { <-ctx.Done(); close(done) }()
		m.mu.Lock()
		if m.sessions == nil {
			m.sessions = map[sessionKey]*localSession{}
		}
		m.sessions[key] = &localSession{cancel: cancel, done: done, provider: provider, fingerprint: res.fingerprint}
		m.mu.Unlock()
		return ctx
	}
	discordCtx := install("discord", key)
	otherKey := sessionKey{f.org, refsTestID(t)}
	otherCtx := install("feishu", otherKey)
	defer m.stopSession(otherKey)
	c, err := pgx.Connect(context.Background(), f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	locked, err := c.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(context.Background())
	if _, err = locked.Exec(context.Background(), `LOCK TABLE openfox.discord_bot_controls IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	// The real control-role function hits the locked dedicated table. PostgreSQL
	// lock_timeout, rather than a fake runner error, exercises Scan's stop path.
	err = m.Scan(context.Background())
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
		t.Fatalf("expected real DB timeout, got %v", err)
	}
	if discordCtx.Err() == nil || otherCtx.Err() != nil {
		t.Fatal("failure cancellation affected wrong providers")
	}
	if _, ok := m.local(key); ok {
		t.Fatal("Discord local session survived DB failure")
	}
}
