package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/slack"
	"openfox/internal/platform/postgres"
)

func (f *slackPG) worker() *IMWorker {
	pool, e := postgres.New(context.Background(), postgres.Config{URL: f.dsn, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second})
	if e != nil {
		f.t.Fatal(e)
	}
	f.t.Cleanup(pool.Close)
	return &IMWorker{Pool: pool, Service: f.service, Instance: "slack-fixture-worker"}
}
func (f *slackPG) installedCentral(verified bool) SlackApp {
	f.t.Helper()
	session, e := f.start("central-fixture-start")
	if e != nil {
		f.t.Fatal(e)
	}
	target := f.claim(session, false)
	outcome := SlackCallbackOutcome{Installation: slack.InstallationToken{TokenGrant: slack.TokenGrant{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour), TokenType: "bot", Scope: slackCentralScopes, AppID: "A" + f.org, TeamID: "T" + f.org, BotUserID: "UBOT"}, InstallerUserID: "UHUMAN", TeamName: "Fixture workspace"}, Auth: slack.AuthIdentity{TeamID: "T" + f.org, UserID: "UBOT", BotID: "BBOT"}}
	var a SlackApp
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.FinishSlackCallback(ctx, tx, f.org, target, outcome, verified)
		if e != nil {
			return e
		}
		a, e = loadSlackApp(ctx, tx, session.AppID)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return a
}
func slackSigned(secret string, body []byte) (string, string) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("v0:" + ts + ":"))
	m.Write(body)
	return ts, "v0=" + hex.EncodeToString(m.Sum(nil))
}
func TestSlackEventSignedDurabilityPG(t *testing.T) {
	f := newSlackPG(t)
	a := f.installedCentral(true)
	event := SlackEnvelope{Type: "event_callback", AppID: a.ExternalAppID, TeamID: a.TeamID, EventID: "Ev1", EventTime: time.Now().Unix(), Event: SlackEvent{Type: "message", Channel: "D01", User: "UHUMAN", ChannelType: "im", TS: strconv.FormatInt(time.Now().Unix(), 10) + ".000001", Text: "synthetic"}}
	send := func(v SlackEnvelope, bad bool) error {
		raw, _ := json.Marshal(v)
		ts, sig := slackSigned(f.service.SlackSigningSecret, raw)
		if bad {
			sig = "v0=" + string(make([]byte, 64))
		}
		return f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			_, e := f.service.ReceiveSlackEvent(ctx, tx, f.org, a.ID, ts, sig, raw)
			return e
		})
	}
	if e := send(event, true); !errors.Is(e, slack.ErrSignature) {
		t.Fatal("forged signature", e)
	}
	if e := send(event, false); e != nil {
		t.Fatal(e)
	}
	if e := send(event, false); e != nil {
		t.Fatal("duplicate", e)
	}
	changed := event
	changed.Event.Text = "changed"
	if e := send(changed, false); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("payload conflict", e)
	}
	mention := event
	mention.EventID = "Ev2"
	mention.Event.Type = "app_mention"
	if e := send(mention, false); e != nil {
		t.Fatal("same message alternate envelope", e)
	}
	old := event
	old.EventID = "EvOld"
	old.Event.TS = "1.000001"
	old.EventTime -= 60
	if e := send(old, false); e != nil {
		t.Fatal(e)
	}
	delayed := event
	delayed.EventID = "EvNewEnvelopeOldMessage"
	delayed.Event.TS = "1.000002"
	if e := send(delayed, false); e != nil {
		t.Fatal(e)
	}
	shared := event
	shared.EventID = "EvShared"
	shared.IsExtSharedChannel = true
	if e := send(shared, false); e != nil {
		t.Fatal(e)
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id()`).Scan(&count)
		if count != 1 {
			t.Fatalf("durable distinct inputs=%d", count)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSlackDeploymentFingerprintAndURLGatePG(t *testing.T) {
	f := newSlackPG(t)
	a := f.installedCentral(false)
	w := f.worker()
	if a.State != "WAITING_URL_VERIFICATION" {
		t.Fatal(a.State)
	}
	if e := w.processSlackMaintenance(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e := w.Pool.RunControlPlane(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox_private.slack_deployment_verifications(fingerprint,verified_at) VALUES($1,clock_timestamp()) ON CONFLICT DO NOTHING`, f.service.SlackDeploymentFingerprint())
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := w.processSlackMaintenance(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	read := func() SlackApp {
		var x SlackApp
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error { var e error; x, e = loadSlackApp(ctx, tx, a.ID); return e }); e != nil {
			t.Fatal(e)
		}
		return x
	}
	before := read()
	if before.State != "ACTIVE" {
		t.Fatal(before.State)
	}
	if e := f.service.SyncSlackDeployment(context.Background(), w.Pool); e != nil {
		t.Fatal(e)
	}
	if read().Generation != before.Generation {
		t.Fatal("same configuration invalidated")
	}
	changed := f.service
	changed.SlackSigningSecret = "different-synthetic-secret"
	if e := changed.SyncSlackDeployment(context.Background(), w.Pool); e != nil {
		t.Fatal(e)
	}
	after := read()
	if after.State != "NEEDS_REAUTH" || after.Generation != before.Generation+1 || after.URLVerified {
		t.Fatal("stale deployment remained active", after.State, after.Generation)
	}
	if e := changed.SyncSlackDeployment(context.Background(), w.Pool); e != nil {
		t.Fatal(e)
	}
	if read().Generation != after.Generation {
		t.Fatal("restart repeatedly invalidated")
	}
}
