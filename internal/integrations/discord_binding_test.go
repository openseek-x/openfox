package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/discord"
	"strings"
	"sync"
	"testing"
)

func (f *discordFixture) candidate(b DiscordBot) DiscordBindingSession {
	f.t.Helper()
	var session DiscordBindingSession
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.IssueDiscordBinding(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	if !strings.HasPrefix(session.Code, "/bind ") {
		f.t.Fatal("missing one time code")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		r, e := loadDiscordControl(ctx, tx, b.ID, true)
		if e != nil {
			return e
		}
		return consumeDiscordBinding(ctx, tx, r, discord.Message{ID: "8000001", ChannelID: "7000001", Author: discord.User{ID: "6000001", Username: "Person"}}, TokenHash(strings.TrimPrefix(session.Code, "/bind ")))
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		session, e = f.service.GetDiscordBindingSession(ctx, tx, f.org, f.actor, session.ID)
		return e
	})
	if session.Code != "" || session.Status != "CANDIDATE" {
		f.t.Fatal("candidate leaked code or auto confirmed")
	}
	return session
}
func TestDiscordBindingDoubleConfirmationPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.connect()
	session := f.candidate(b)
	var wg sync.WaitGroup
	out := make(chan DiscordIdentity, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var v DiscordIdentity
			e := f.run(func(ctx context.Context, tx pgx.Tx) error {
				var e error
				v, e = f.service.ConfirmDiscordBinding(ctx, tx, f.org, f.actor, session.ID, session.Version)
				return e
			})
			out <- v
			errs <- e
		}()
	}
	wg.Wait()
	for range 2 {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	first, second := <-out, <-out
	if first.ID != second.ID || first.RemoteUserID != "6000001" {
		t.Fatal("duplicate identity")
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.discord_routes WHERE bot_id=$1::uuid`, b.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("task assistant DM created business route")
		}
		return f.service.UnbindDiscord(ctx, tx, f.org, f.actor, first.ID, first.Version)
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		v, e := f.service.DiscordPersonal(ctx, tx, f.org, f.actor)
		if e == nil && len(v) != 0 {
			t.Fatal("identity remains active")
		}
		return e
	})
}
func TestDiscordBindingExpiredAndReplacedPG(t *testing.T) {
	f := newDiscordFixture(t)
	b := f.connect()
	session := f.candidate(b)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.IssueDiscordBinding(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ConfirmDiscordBinding(ctx, tx, f.org, f.actor, session.ID, session.Version)
		return e
	})
	if !errors.Is(e, ErrDiscordSessionState) {
		t.Fatal("old candidate revived", e)
	}
	session = f.candidate(b)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.discord_binding_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, session.ID)
		return e
	})
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ConfirmDiscordBinding(ctx, tx, f.org, f.actor, session.ID, session.Version)
		return e
	})
	if !errors.Is(e, ErrDiscordSessionState) {
		t.Fatal("expired code accepted", e)
	}
}
