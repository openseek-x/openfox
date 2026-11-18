package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlackConfigurationProofAttemptFencePG(t *testing.T) {
	f, a := newSlackDynamicPG(t)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET state='MANIFEST_UNKNOWN',next_attempt_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tooling.tokens.rotate":
			n := calls.Add(1)
			if n == 1 {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			token := "new-access"
			if n == 1 {
				token = "old-access"
			}
			slackTestReply(w, map[string]any{"ok": true, "token": token, "refresh_token": "discard", "team_id": a.TeamID, "user_id": a.OwnerSlackUserID, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
		case "/api/apps.manifest.export":
			slackTestReply(w, map[string]any{"ok": true, "manifest": f.service.slackManifest(a, true)})
		default:
			t.Error(r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	input := SlackRecoverAppInput{Version: a.Version, ExternalAppID: a.ExternalAppID, ClientID: "456.789", ClientSecret: "candidate-secret", SigningSecret: "candidate-signing", ConfigurationRefreshToken: "synthetic-first"}
	var first, second SlackApp
	prepare := func(in SlackRecoverAppInput) *SlackApp {
		var out SlackApp
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			out, e = f.service.PrepareSlackReconfiguration(ctx, tx, f.org, f.actor, a.ID, in, true)
			return e
		}); e != nil {
			t.Fatal(e)
		}
		return &out
	}
	first = *prepare(input)
	result := make(chan SlackProofResult, 1)
	go func() { result <- f.service.VerifySlackRecovery(context.Background(), input) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first HTTP not entered")
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	worker := f.worker()
	if e := worker.processSlackOperations(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error { var e error; a, e = loadSlackApp(ctx, tx, a.ID); return e }); e != nil {
		t.Fatal(e)
	}
	if a.State != "PROOF_UNKNOWN" {
		t.Fatal(a.State)
	}
	input.Version = a.Version
	input.ConfigurationRefreshToken = "synthetic-second"
	second = *prepare(input)
	newer := f.service.VerifySlackRecovery(context.Background(), input)
	close(release)
	older := <-result
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.FinishSlackRecovery(ctx, tx, f.org, f.actor, a.ID, first.Version, input, older)
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("old proof borrowed new lease", e)
	}
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		out, e := f.service.FinishSlackRecovery(ctx, tx, f.org, f.actor, a.ID, second.Version, input, newer)
		if e == nil && out.State != "APP_CREATED" {
			t.Fatal(out.State)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSlackRefreshClaimsOnlyExecutingJobPG(t *testing.T) {
	f, child := newSlackDynamicPG(t)
	var parent SlackApp
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		parent, e = loadSlackApp(ctx, tx, child.ParentAppID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			n := calls.Add(1)
			if n == 1 {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			client, _, _ := r.BasicAuth()
			a := parent
			scopes := slackCentralScopes
			if client != "123.456" {
				a = child
				scopes = slackAgentScopes
			}
			slackTestReply(w, map[string]any{"ok": true, "access_token": "next-" + a.BotUserID, "refresh_token": "next-refresh-" + a.BotUserID, "expires_in": 43200, "token_type": "bot", "scope": strings.Join(scopes, ",")})
		case "/api/auth.test":
			a := parent
			bot := "BBOT"
			if r.Header.Get("Authorization") == "Bearer next-"+child.BotUserID {
				a = child
				bot = "BDBOT"
			}
			slackTestReply(w, map[string]any{"ok": true, "team_id": a.TeamID, "user_id": a.BotUserID, "bot_id": bot})
		default:
			t.Error(r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	var before int64
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT sum(version) FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[])`, []string{parent.ConnectionID, child.ConnectionID}).Scan(&before); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET token_expires_at=clock_timestamp()+interval '1 minute' WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[])`, []string{parent.ID, child.ID})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	worker := f.worker()
	done := make(chan error, 1)
	go func() { done <- worker.processSlackRefresh(context.Background(), f.org) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh not entered")
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND refresh_lease_owner<>''`).Scan(&n)
		if n != 1 {
			t.Errorf("leased waiting jobs: %d", n)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := worker.processSlackRefresh(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var after int64
		if e := tx.QueryRow(ctx, `SELECT sum(version) FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[])`, []string{parent.ConnectionID, child.ConnectionID}).Scan(&after); e != nil {
			return e
		}
		if after != before {
			t.Fatal("ordinary refresh changed semantic connection version", before, after)
		}
		for _, old := range []SlackApp{parent, child} {
			a, e := loadSlackApp(ctx, tx, old.ID)
			if e != nil {
				return e
			}
			if a.State != "ACTIVE" || a.Generation != old.Generation || a.Version != old.Version {
				t.Fatal("refresh invalidated installation", a.State, a.Version, old.Version)
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSlackRefreshConsumedTokenNeverRetriesOnLate429PG(t *testing.T) {
	f := newSlackPG(t)
	a := f.installedCentral(true)
	var calls atomic.Int32
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			calls.Add(1)
			slackTestReply(w, map[string]any{"ok": true, "access_token": "next", "refresh_token": "next-refresh", "expires_in": 43200, "token_type": "bot", "scope": strings.Join(slackCentralScopes, ",")})
		case "/api/auth.test":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET token_expires_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	worker := f.worker()
	for range 2 {
		if e := worker.processSlackRefresh(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("consumed refresh replayed", calls.Load())
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		v, e := loadSlackApp(ctx, tx, a.ID)
		if e == nil && v.State != "NEEDS_REAUTH" {
			t.Fatal(v.State)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
