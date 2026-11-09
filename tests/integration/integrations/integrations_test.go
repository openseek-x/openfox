// Package integrations 在真实 PostgreSQL（FORCE RLS、应用角色）下验证外部应用集成：
// 总览计数、详情可见性（他人的个人连接、无权空间的同步关联）、断开组织连接的权限 / 短语 / 版本 / 联动 / 幂等，以及跨组织隔离。
package integrations

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 与 tests/integration/db/fixture.sql 保持一致；其余数据由本测试自建。
const (
	orgA        = "00000000-0000-0000-0000-0000000000a1"
	orgB        = "00000000-0000-0000-0000-0000000000b1"
	alice       = "00000000-0000-0000-0000-000000000001" // orgA OWNER
	bob         = "00000000-0000-0000-0000-000000000002" // orgB OWNER
	privateA    = "00000000-0000-0000-0000-0000000000aa"
	carol       = "00000000-0000-0000-0000-0000000000c1" // orgA ADMIN
	dave        = "00000000-0000-0000-0000-0000000000c2" // orgA MEMBER
	erin        = "00000000-0000-0000-0000-0000000000c3" // orgA GUEST
	sharedA     = "00000000-0000-0000-0000-0000000000a5"
	jiraOrg     = "00000000-0000-0000-0000-00000000e001"
	jiraAlice   = "00000000-0000-0000-0000-00000000e002"
	jiraDave    = "00000000-0000-0000-0000-00000000e003"
	jiraGone    = "00000000-0000-0000-0000-00000000e004"
	githubOrg   = "00000000-0000-0000-0000-00000000e005"
	jiraOrgB    = "00000000-0000-0000-0000-00000000e0b1"
	linkPrivate = "00000000-0000-0000-0000-00000000f001"
	linkShared  = "00000000-0000-0000-0000-00000000f002"
)

type harness struct {
	t    *testing.T
	ctx  context.Context
	url  string
	pool *postgres.Pool
}

func TestIntegrations(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := &harness{t: t, ctx: ctx, url: url, pool: pool}
	h.seed()
	svc := integrations.Service{GitHubConfigured: true}

	owner := h.actor(orgA, alice, policy.OrganizationRoleOwner)
	admin := h.actor(orgA, carol, policy.OrganizationRoleAdmin)
	member := h.actor(orgA, dave, policy.OrganizationRoleMember)
	guest := h.actor(orgA, erin, policy.OrganizationRoleGuest)

	t.Run("overview counts and status", func(t *testing.T) {
		var ov integrations.Overview
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			ov, err = svc.Overview(ctx, tx, orgA, owner)
			return err
		})
		if !ov.CanManage {
			t.Fatal("owner must be able to manage")
		}
		byID := map[string]integrations.ProviderView{}
		for _, p := range ov.Providers {
			byID[p.ID] = p
		}
		// jira：一个 ACTIVE 一个已断开（不计）→ 1 / ok；github：NEEDS_ATTENTION → attention；个人连接不计入。
		if got := byID["jira"].Summary; got.Connections != 1 || got.Status != "ok" {
			t.Fatalf("jira summary = %+v", got)
		}
		if got := byID["github"].Summary; got.Connections != 1 || got.Status != "attention" {
			t.Fatalf("github summary = %+v", got)
		}
		if got := byID["feishu"].Summary; got.Connections != 0 || got.Status != "none" {
			t.Fatalf("feishu summary = %+v", got)
		}
		if !byID["github"].Configured {
			t.Fatal("github should be configured")
		}
		var ovMember integrations.Overview
		h.run(orgA, member, func(ctx context.Context, tx pgx.Tx) (err error) {
			ovMember, err = svc.Overview(ctx, tx, orgA, member)
			return err
		})
		if ovMember.CanManage {
			t.Fatal("member must not manage")
		}
	})

	t.Run("detail visibility", func(t *testing.T) {
		detail := func(actor collaboration.Actor, provider string) (d integrations.Detail) {
			h.run(orgA, actor, func(ctx context.Context, tx pgx.Tx) (err error) {
				d, err = svc.Detail(ctx, tx, orgA, actor, provider)
				return err
			})
			return d
		}
		d := detail(owner, "jira")
		if len(d.Connections) != 2 || d.Connections[0].ID != jiraOrg || d.Connections[1].Status != "DISCONNECTED" {
			t.Fatalf("owner org connections = %+v", d.Connections)
		}
		if d.Connections[0].CreatedByEmail != "alice@example.test" {
			t.Fatalf("created_by_email = %q", d.Connections[0].CreatedByEmail)
		}
		if len(d.PersonalConnections) != 1 || d.PersonalConnections[0].ID != jiraAlice {
			t.Fatalf("owner personal = %+v", d.PersonalConnections)
		}
		if len(d.SyncLinks) != 2 {
			t.Fatalf("owner sync links = %+v", d.SyncLinks)
		}
		// 普通成员：只看到自己的个人连接，和共享空间的同步关联（私有空间的不可见）。
		d = detail(member, "jira")
		if len(d.PersonalConnections) != 1 || d.PersonalConnections[0].ID != jiraDave {
			t.Fatalf("member personal = %+v", d.PersonalConnections)
		}
		if len(d.SyncLinks) != 1 || d.SyncLinks[0].ID != linkShared || d.SyncLinks[0].SpaceName != "Shared space" {
			t.Fatalf("member sync links = %+v", d.SyncLinks)
		}
		if d.CanManage {
			t.Fatal("member can_manage must be false")
		}
		// GUEST 可读详情，但看不到共享空间（非成员）。
		d = detail(guest, "jira")
		if len(d.SyncLinks) != 0 || len(d.PersonalConnections) != 0 {
			t.Fatalf("guest detail = %+v", d)
		}
		// 另一个服务商没有同步关联；未知服务商 404。
		if d := detail(owner, "github"); len(d.SyncLinks) != 0 || len(d.Connections) != 1 {
			t.Fatalf("github detail = %+v", d)
		}
		err := h.try(orgA, owner, func(ctx context.Context, tx pgx.Tx) error {
			_, err := svc.Detail(ctx, tx, orgA, owner, "nope")
			return err
		})
		if !errors.Is(err, integrations.ErrNotFound) {
			t.Fatalf("unknown provider err = %v", err)
		}
	})

	t.Run("cross organization isolation", func(t *testing.T) {
		bobActor := h.actor(orgB, bob, policy.OrganizationRoleOwner)
		var d integrations.Detail
		h.run(orgB, bobActor, func(ctx context.Context, tx pgx.Tx) (err error) {
			d, err = svc.Detail(ctx, tx, orgB, bobActor, "jira")
			return err
		})
		if len(d.Connections) != 1 || d.Connections[0].ID != jiraOrgB || len(d.SyncLinks) != 0 {
			t.Fatalf("org B detail = %+v", d)
		}
		// 组织 B 的所有者不能断开组织 A 的连接（RLS 下不可见）。
		err := h.try(orgB, bobActor, func(ctx context.Context, tx pgx.Tx) error {
			_, err := svc.Disconnect(ctx, tx, orgB, bobActor, jiraOrg, 1, "断开 Jira")
			return err
		})
		if !errors.Is(err, integrations.ErrNotFound) {
			t.Fatalf("cross-org disconnect err = %v", err)
		}
	})

	t.Run("disconnect checks", func(t *testing.T) {
		disconnect := func(actor collaboration.Actor, id string, version uint64, phrase string) (integrations.DisconnectResult, error) {
			var res integrations.DisconnectResult
			err := h.try(orgA, actor, func(ctx context.Context, tx pgx.Tx) (err error) {
				res, err = svc.Disconnect(ctx, tx, orgA, actor, id, version, phrase)
				return err
			})
			return res, err
		}
		if _, err := disconnect(member, jiraOrg, 1, "断开 Jira"); !errors.Is(err, integrations.ErrForbidden) {
			t.Fatalf("member disconnect org err = %v, want forbidden", err)
		}
		if _, err := disconnect(admin, jiraOrg, 1, "断开 GitHub"); !errors.Is(err, integrations.ErrConfirmationMismatch) {
			t.Fatalf("wrong phrase err = %v", err)
		}
		if _, err := disconnect(admin, jiraOrg, 1, ""); !errors.Is(err, integrations.ErrConfirmationMismatch) {
			t.Fatalf("empty phrase err = %v", err)
		}
		if _, err := disconnect(admin, jiraOrg, 7, "断开 Jira"); !errors.Is(err, integrations.ErrVersionConflict) {
			t.Fatalf("stale version err = %v", err)
		}
		// 他人的个人连接按不存在处理；个人连接的所有者不需要短语。
		if _, err := disconnect(member, jiraAlice, 1, ""); !errors.Is(err, integrations.ErrNotFound) {
			t.Fatalf("other's personal err = %v", err)
		}
		h.expectRow(`SELECT status FROM openfox.integration_connections WHERE id = $1::uuid`, jiraOrg, "ACTIVE")

		// 成功：管理员 + 去首尾空白的短语。
		res, err := disconnect(admin, jiraOrg, 1, "  断开 Jira \n")
		if err != nil || !res.Changed || res.Connection.Status != "DISCONNECTED" || res.Connection.Version != 2 || res.Connection.DisconnectedAt == nil {
			t.Fatalf("disconnect = %+v, %v", res, err)
		}
		// 联动：个人连接被断开，同步关联 PAUSED，凭据保留（不返回给调用方）。
		h.expectRow(`SELECT status FROM openfox.integration_connections WHERE id = $1::uuid`, jiraAlice, "DISCONNECTED")
		h.expectRow(`SELECT status FROM openfox.integration_connections WHERE id = $1::uuid`, jiraDave, "DISCONNECTED")
		h.expectRow(`SELECT status || ':' || status_reason FROM openfox.integration_sync_links WHERE id = $1::uuid`, linkPrivate, "PAUSED:connection disconnected")
		h.expectRow(`SELECT status FROM openfox.integration_sync_links WHERE id = $1::uuid`, linkShared, "PAUSED")
		h.expectRow(`SELECT (credentials_sealed IS NOT NULL)::text FROM openfox.integration_connections WHERE id = $1::uuid`, jiraOrg, "true")
		// 其他服务商不受影响。
		h.expectRow(`SELECT status FROM openfox.integration_connections WHERE id = $1::uuid`, githubOrg, "NEEDS_ATTENTION")

		// 重复断开幂等：不报错、不改版本、Changed=false（调用方据此不重复写审计）。
		res, err = disconnect(admin, jiraOrg, 1, "")
		if err != nil || res.Changed || res.Connection.Version != 2 {
			t.Fatalf("repeat disconnect = %+v, %v", res, err)
		}
		// 总览：jira 不再计数。
		var ov integrations.Overview
		h.run(orgA, owner, func(ctx context.Context, tx pgx.Tx) (err error) {
			ov, err = svc.Overview(ctx, tx, orgA, owner)
			return err
		})
		for _, p := range ov.Providers {
			if p.ID == "jira" && (p.Summary.Connections != 0 || p.Summary.Status != "none") {
				t.Fatalf("jira summary after disconnect = %+v", p.Summary)
			}
		}
	})

	t.Run("personal disconnect by owner", func(t *testing.T) {
		// 先给 dave 重新建一条个人连接（上面的联动已断开旧的），再由本人断开，不需要短语。
		h.exec(`INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, owner_user_id, external_id, display_name, created_by)
		        VALUES ($1::uuid, '00000000-0000-0000-0000-00000000e0d1', 'github', 'PERSONAL', $2::uuid, 'dave-gh', 'dave', $2::uuid)`, orgA, dave)
		var res integrations.DisconnectResult
		h.run(orgA, member, func(ctx context.Context, tx pgx.Tx) (err error) {
			res, err = svc.Disconnect(ctx, tx, orgA, member, "00000000-0000-0000-0000-00000000e0d1", 1, "")
			return err
		})
		if !res.Changed || res.Connection.Status != "DISCONNECTED" {
			t.Fatalf("personal disconnect = %+v", res)
		}
		h.expectRow(`SELECT status FROM openfox.integration_connections WHERE id = $1::uuid`, githubOrg, "NEEDS_ATTENTION")
	})

	t.Run("site comment credentials removed on disconnect", func(t *testing.T) {
		create := func(provider, parent, scope, method string) string {
			var id string
			h.queryRow(`INSERT INTO openfox.integration_connections
			 (organization_id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,
			 credentials_sealed,credentials_key_id,settings,created_by)
			 VALUES($1::uuid,$2,$3,NULLIF($4,'')::uuid,CASE WHEN $3='PERSONAL' THEN $5::uuid END,
			 gen_random_uuid()::text,'comment fixture',decode('010203','hex'),'fixture-key',
			 jsonb_build_object('auth_method',$6::text),$5::uuid) RETURNING id::text`,
				[]any{orgA, provider, scope, parent, alice, method}, &id)
			return id
		}
		disconnect := func(actor collaboration.Actor, id string, version uint64, phrase string) (integrations.DisconnectResult, error) {
			var result integrations.DisconnectResult
			err := h.try(orgA, actor, func(ctx context.Context, tx pgx.Tx) (err error) {
				result, err = svc.Disconnect(ctx, tx, orgA, actor, id, version, phrase)
				return err
			})
			return result, err
		}
		cleared := func(id string) {
			h.expectRow(`SELECT (status='DISCONNECTED' AND credentials_sealed IS NULL AND credentials_key_id='')::text
			 FROM openfox.integration_connections WHERE id=$1::uuid`, id, "true")
		}
		for _, provider := range []string{"jira", "zentao"} {
			parent := create(provider, "", "ORGANIZATION", "import_site")
			personal := create(provider, parent, "PERSONAL", "site_comments")
			if _, err := disconnect(member, personal, 1, ""); !errors.Is(err, integrations.ErrNotFound) {
				t.Fatalf("another member disconnected personal account: %v", err)
			}
			if _, err := disconnect(owner, personal, 9, ""); !errors.Is(err, integrations.ErrVersionConflict) {
				t.Fatalf("stale disconnect accepted: %v", err)
			}
			h.expectRow(`SELECT (credentials_sealed IS NOT NULL)::text FROM openfox.integration_connections WHERE id=$1::uuid`, personal, "true")
			result, err := disconnect(owner, personal, 1, "")
			if err != nil || !result.Changed {
				t.Fatalf("disconnect: %+v %v", result, err)
			}
			cleared(personal)
			repeated, err := disconnect(owner, personal, 1, "")
			if err != nil || repeated.Changed || repeated.Connection.Version != result.Connection.Version {
				t.Fatalf("repeat disconnect: %+v %v", repeated, err)
			}
			h.exec(`UPDATE openfox.integration_connections SET credentials_sealed=decode('010203','hex'),credentials_key_id='old-key' WHERE id=$1::uuid`, personal)
			if _, err := disconnect(member, personal, 1, ""); !errors.Is(err, integrations.ErrNotFound) {
				t.Fatalf("unauthorized legacy cleanup: %v", err)
			}
			result, err = disconnect(owner, personal, 1, "")
			if err != nil || !result.Changed {
				t.Fatalf("legacy cleanup: %+v %v", result, err)
			}
			cleared(personal)
			child := create(provider, parent, "PERSONAL", "site_comments")
			h.exec(`UPDATE openfox.integration_connections SET credentials_sealed=decode('010203','hex'),credentials_key_id='old-key' WHERE id=$1::uuid`, personal)
			catalog, _ := integrations.Lookup(provider)
			if _, err = disconnect(owner, parent, 1, integrations.ConfirmPhrase(catalog.Name)); err != nil {
				t.Fatal(err)
			}
			cleared(parent)
			cleared(child)
			cleared(personal)
		}
	})
}

func (h *harness) seed() {
	h.t.Helper()
	h.exec(`
INSERT INTO openfox.users (id, email) VALUES
    ('` + carol + `', 'carol@example.test'), ('` + dave + `', 'dave@example.test'), ('` + erin + `', 'erin@example.test');
INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES
    ('` + orgA + `', '` + carol + `', 'ADMIN'), ('` + orgA + `', '` + dave + `', 'MEMBER'), ('` + orgA + `', '` + erin + `', 'GUEST');
INSERT INTO openfox.spaces (organization_id, id, name, visibility) VALUES ('` + orgA + `', '` + sharedA + `', 'Shared space', 'SHARED');
INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, status, status_reason, disconnected_at, credentials_sealed, created_by, created_at) VALUES
    ('` + orgA + `', '` + jiraOrg + `', 'jira', 'ORGANIZATION', NULL, NULL, 'acme.atlassian.net', 'Acme Jira', 'ACTIVE', '', NULL, '\x010203', '` + alice + `', now() - interval '3 hours'),
    ('` + orgA + `', '` + jiraGone + `', 'jira', 'ORGANIZATION', NULL, NULL, 'old.atlassian.net', 'Old Jira', 'DISCONNECTED', '', now(), NULL, '` + alice + `', now() - interval '2 hours'),
    ('` + orgA + `', '` + githubOrg + `', 'github', 'ORGANIZATION', NULL, NULL, 'acme-org', 'acme-org', 'NEEDS_ATTENTION', 'token expired', NULL, NULL, '` + alice + `', now()),
    ('` + orgB + `', '` + jiraOrgB + `', 'jira', 'ORGANIZATION', NULL, NULL, 'b.atlassian.net', 'B Jira', 'ACTIVE', '', NULL, NULL, '` + bob + `', now());
INSERT INTO openfox.integration_connections (organization_id, id, provider, scope, parent_connection_id, owner_user_id, external_id, display_name, created_by) VALUES
    ('` + orgA + `', '` + jiraAlice + `', 'jira', 'PERSONAL', '` + jiraOrg + `', '` + alice + `', 'alice-jira', 'alice', '` + alice + `'),
    ('` + orgA + `', '` + jiraDave + `', 'jira', 'PERSONAL', '` + jiraOrg + `', '` + dave + `', 'dave-jira', 'dave', '` + dave + `');
INSERT INTO openfox.integration_sync_links (organization_id, id, connection_id, space_id, external_project_id, external_project_name, direction, created_by, created_at) VALUES
    ('` + orgA + `', '` + linkPrivate + `', '` + jiraOrg + `', '` + privateA + `', 'PRJ', 'Private project', 'INBOUND', '` + alice + `', now() - interval '2 hours'),
    ('` + orgA + `', '` + linkShared + `', '` + jiraOrg + `', '` + sharedA + `', 'SHR', 'Shared project', 'BIDIRECTIONAL', '` + alice + `', now() - interval '1 hours');`)
}

func (h *harness) actor(org, user string, role policy.OrganizationRole) collaboration.Actor {
	h.t.Helper()
	var version int64
	h.queryRow(`SELECT authorization_version FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`,
		[]any{org, user}, &version)
	return collaboration.Actor{UserID: user, Role: role, AuthorizationVersion: uint64(version)}
}

func (h *harness) try(org string, actor collaboration.Actor, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: org, ActorID: actor.UserID, AuthorizationVersion: actor.AuthorizationVersion}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
	return h.pool.RunForOrganization(h.ctx, org, tenancy.SourceUserRequest, selector, fn)
}

func (h *harness) run(org string, actor collaboration.Actor, fn func(context.Context, pgx.Tx) error) {
	h.t.Helper()
	if err := h.try(org, actor, fn); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if _, err := conn.Exec(h.ctx, query, args...); err != nil {
		h.t.Fatalf("%v", err)
	}
}

func (h *harness) queryRow(query string, args []any, dest ...any) {
	h.t.Helper()
	conn, err := pgx.Connect(h.ctx, h.url)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close(h.ctx)
	if err := conn.QueryRow(h.ctx, query, args...).Scan(dest...); err != nil {
		h.t.Fatalf("%s: %v", query, err)
	}
}

func (h *harness) expectRow(query, arg, want string) {
	h.t.Helper()
	var got string
	h.queryRow(query, []any{arg}, &got)
	if got != want {
		h.t.Fatalf("%s [%s] = %q, want %q", query, arg, got, want)
	}
}
