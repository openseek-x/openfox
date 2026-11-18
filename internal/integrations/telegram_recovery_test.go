package integrations

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

func TestTelegramExplicitWebhookRecoveryPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.connect()
	f.mu.Lock()
	f.loseSet = true
	f.mu.Unlock()
	b = f.control(b)
	if b.ControlStatus != "UNKNOWN" {
		t.Fatal(b.ControlStatus)
	}
	input := f.input()
	input.BotID = b.ID
	input.Version = b.Version
	err := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, input)
		return e
	})
	if !errors.Is(err, ErrTelegramSessionState) {
		t.Fatalf("implicit recovery allowed: %v", err)
	}
	input.RecoverWebhook = true
	var target TelegramConnectTarget
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, input)
		return e
	})
	// A third-party URL is not overwritten even with the explicit recovery flag.
	f.mu.Lock()
	owned := f.hook
	f.hook = "https://third.example/webhook"
	f.mu.Unlock()
	if _, e := f.service.VerifyTelegramConnection(context.Background(), target); !errors.Is(e, ErrTelegramWebhookConflict) {
		t.Fatalf("foreign webhook %v", e)
	}
	f.mu.Lock()
	f.hook = owned
	f.loseSet = false
	f.mu.Unlock()
	proof, e := f.service.VerifyTelegramConnection(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	var preparation TelegramPreparation
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		preparation, e = f.service.SaveTelegramPreparation(ctx, tx, f.org, f.actor, target, proof)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ConfirmTelegramPreparation(ctx, tx, f.org, f.actor, preparation.ID, TelegramConfirmInput{Version: preparation.Version, RemoteBotID: preparation.RemoteBotID.String()})
		return e
	})
	b = f.control(b)
	if b.State != "ACTIVE" {
		t.Fatal(b.State)
	}
	f.mu.Lock()
	calls := f.setCalls
	f.mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected explicit second SET, got %d", calls)
	}
	// Local disconnect erases the token; the same reserved Bot can be recovered
	// with a freshly verified token, without reviving old identities/routes.
	identity, _ := f.bind(b, 600)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		return f.service.DisconnectTelegramBot(ctx, tx, f.org, f.actor, b.ID, TelegramDisconnectInput{Version: b.Version, Mode: "local"})
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.GetTelegramBot(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	input = f.input()
	input.BotID = b.ID
	input.Version = b.Version
	input.RecoverWebhook = true
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = f.service.PrepareTelegramConnection(ctx, tx, f.org, f.actor, input)
		return e
	})
	proof, e = f.service.VerifyTelegramConnection(context.Background(), target)
	if e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		preparation, e = f.service.SaveTelegramPreparation(ctx, tx, f.org, f.actor, target, proof)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ConfirmTelegramPreparation(ctx, tx, f.org, f.actor, preparation.ID, TelegramConfirmInput{Version: preparation.Version, RemoteBotID: preparation.RemoteBotID.String()})
		return e
	})
	b = f.control(b)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var active bool
		e := tx.QueryRow(ctx, `SELECT active FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, identity.ID).Scan(&active)
		if active {
			t.Fatal("recovered old identity")
		}
		return e
	})
}

func TestTelegramPersonalTargetsForMemberPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	user := refsTestID(t)
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		// Fixture setup is deliberately privileged; public endpoints cannot write users/membership.
		if _, e := tx.Exec(ctx, `RESET ROLE`); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO openfox.users(id,email)VALUES($1::uuid,$2)`, user, user+"@example.test"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO openfox.organization_members(organization_id,user_id,role)VALUES(openfox_private.current_organization_id(),$1::uuid,'MEMBER')`, user); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO openfox.space_members(organization_id,space_id,user_id,role)VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'MEMBER')`, f.space, user)
		return e
	})
	member := &tgFixture{t: t, dsn: f.dsn, org: f.org, user: user, service: f.service, actor: collaboration.Actor{UserID: user, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}}
	member.must(func(ctx context.Context, tx pgx.Tx) error {
		bots, e := f.service.TelegramBots(ctx, tx, f.org, member.actor)
		if e != nil {
			return e
		}
		if len(bots.Bots) != 0 {
			t.Fatal("member received manager metadata")
		}
		targets, e := f.service.TelegramPersonalTargets(ctx, tx, f.org, member.actor)
		if e != nil {
			return e
		}
		if len(targets) != 1 || targets[0].BotID != b.ID || !targets[0].CanBind {
			t.Fatalf("targets %+v", targets)
		}
		_, e = f.service.IssueTelegramBinding(ctx, tx, f.org, member.actor, b.ID)
		return e
	})
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid`, f.space, user)
		return e
	})
	member.must(func(ctx context.Context, tx pgx.Tx) error {
		targets, e := f.service.TelegramPersonalTargets(ctx, tx, f.org, member.actor)
		if e == nil && len(targets) != 0 {
			t.Fatal("lost space still visible")
		}
		return e
	})
	err := member.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.IssueTelegramBinding(ctx, tx, f.org, member.actor, b.ID)
		return e
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("lost space issued %v", err)
	}
}

func TestTelegramReplyQueueBudgetKeepsTerminalPG(t *testing.T) {
	f := newTGFixture(t, true)
	b := f.control(f.connect())
	f.bind(b, 700)
	p := tgQueued(t, f, b, 701)
	w := IMWorker{Pool: tgWorkerRunner{f}, Service: f.service}
	if e := w.ProcessTelegramInbound(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.im_reply_outbox(organization_id,bot_id,chat_id,body,reply_to_remote_id)
 SELECT openfox_private.current_organization_id(),$1::uuid,'987654','synthetic quota fixture','701' FROM generate_series(1,1000)`, b.ID)
		return e
	})
	id := tgFinalize(t, f, b, p, "任务终态必须保留")
	f.must(func(ctx context.Context, tx pgx.Tx) error {
		var status, note, body string
		e := tx.QueryRow(ctx, `SELECT q.status,q.last_error,m.body FROM openfox.im_reply_outbox q JOIN openfox.messages m ON m.organization_id=q.organization_id AND m.id=q.message_id WHERE q.organization_id=openfox_private.current_organization_id() AND q.id=$1::uuid`, id).Scan(&status, &note, &body)
		if e == nil && (status != "FAILED" || note != "queue_full" || body != "任务终态必须保留") {
			t.Fatalf("budget %s/%s/%s", status, note, body)
		}
		return e
	})
	if e := w.ProcessTelegramReplies(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls := f.sendCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("quota or unknown-source item sent")
	}
}
