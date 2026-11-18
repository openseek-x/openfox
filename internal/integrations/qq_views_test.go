package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

func qqOtherMember(t *testing.T, f *qqPG) *qqPG {
	t.Helper()
	x := *f
	x.user = refsTestID(t)
	x.actor = collaboration.Actor{UserID: x.user, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	if _, e = c.Exec(context.Background(), `INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, x.user, x.user+"@example.test"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(context.Background(), `INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, f.org, x.user); e != nil {
		t.Fatal(e)
	}
	return &x
}

func TestQQConfigurationAndManagerViewsPG(t *testing.T) {
	f, b, route := newQQReady(t)
	bound := f.bindPrivate(b, route, "view-owner", "view-owner-binding")
	discovery := f.startBinding(b, QQStartBindingInput{Purpose: "GROUP_DISCOVERY", IdempotencyKey: "view-group-discovery"})
	if _, e := f.receive(b, route, qqMessage("GROUP", "view-group-discovery", "group-admin", "candidate-chat", "/openfox-group "+discovery.Code, "admin")); e != nil {
		t.Fatal(e)
	}
	if _, e := f.confirm(f.binding(discovery.ID), true); e != nil {
		t.Fatal(e)
	}
	session := f.startBinding(b, QQStartBindingInput{Purpose: "PRIVATE_CODE", IdempotencyKey: "view-pending-session"})
	if _, e := f.receive(b, route, qqMessage("GROUP", "view-group-member", "candidate-subject", "candidate-chat", "private-not-returned", "member")); e != nil {
		t.Fatal(e)
	}
	var config QQConfiguration
	var detail QQBotDetail
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		config, e = f.service.QQConfiguration(ctx, tx, f.org, f.actor)
		if e != nil {
			return e
		}
		detail, e = f.service.GetQQBotDetail(ctx, tx, f.org, f.actor, b.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if len(config.Bots) != 1 || !config.Bots[0].CanManage || len(config.Bindings) != 1 || config.Bindings[0].ID != bound.BindingID || len(config.BindingSessions) != 1 || config.BindingSessions[0].Code != "" {
		t.Fatal("configuration scope", config)
	}
	if len(detail.Candidates) != 1 || detail.Candidates[0].SubjectID != "candidate-subject" {
		t.Fatal("manager candidate missing", detail.Candidates)
	}
	raw, _ := json.Marshal([]any{config, detail})
	for _, secret := range []string{session.Code, "private-not-returned", "synthetic-secret", "controlled-token"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("read model leaked secret/content")
		}
	}
	other := qqOtherMember(t, f)
	e = other.run(func(ctx context.Context, tx pgx.Tx) error {
		v, e := other.service.QQConfiguration(ctx, tx, other.org, other.actor)
		if e != nil {
			return e
		}
		if len(v.Bots) != 1 || v.Bots[0].CanManage || len(v.Bindings) != 0 || len(v.BindingSessions) != 0 {
			t.Error("member sees manager/private state")
		}
		_, e = other.service.GetQQBotDetail(ctx, tx, other.org, other.actor, b.ID)
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("member manager detail", e)
	}
	e = other.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := other.service.CancelQQBindingSession(ctx, tx, other.org, other.actor, session.ID, session.Version)
		return e
	})
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("other member cancelled proof", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		v, e := f.service.CancelQQBindingSession(ctx, tx, f.org, f.actor, session.ID, session.Version)
		if e == nil && v.Status != "CANCELLED" {
			t.Error(v.Status)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	// After Agent privacy changes, a former visible member loses directory access.
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.agents SET visibility='PRIVATE' WHERE id=$1::uuid`, f.agent)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = other.run(func(ctx context.Context, tx pgx.Tx) error {
		v, e := other.service.QQConfiguration(ctx, tx, other.org, other.actor)
		if e == nil && len(v.Bots) != 0 {
			t.Error("private Agent directory leaked")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestQQReleasePreservesAppClaimPG(t *testing.T) {
	f, b, _ := newQQReady(t)
	e := f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ReleaseQQBot(ctx, tx, f.org, f.actor, b.ID, QQQRActionInput{Version: b.Version, RemoteChecked: true})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("active release", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.DisconnectQQBot(ctx, tx, f.org, f.actor, b.ID, b.Version)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ReleaseQQBot(ctx, tx, f.org, f.actor, b.ID, QQQRActionInput{Version: b.Version})
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal("remote warning missing", e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var e error
		b, e = f.service.ReleaseQQBot(ctx, tx, f.org, f.actor, b.ID, QQQRActionInput{Version: b.Version, RemoteChecked: true})
		return e
	})
	if e != nil || b.State != "CANCELLED" {
		t.Fatal(b.State, e)
	}
	e = f.run(func(ctx context.Context, tx pgx.Tx) error {
		var count int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.qq_bot_controls WHERE app_id=$1`, b.AppID).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			t.Error("verified claim was released")
		}
		v, e := f.service.QQConfiguration(ctx, tx, f.org, f.actor)
		if e == nil && len(v.Bots) != 0 {
			t.Error("released bot still in active directory")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.create("new-slot-after-release"); e != nil {
		t.Fatal("local Agent slot not released", e)
	}
}
