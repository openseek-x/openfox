package stage3

// 组织与成员集成测试：组织设置、优先级 4/5 级、离开与删除组织、邀请（加入空间、邮件、重新发送）、邀请链接。
// 复用本目录的夹具（同一套角色与真实 HTTP 处理器）；需要邮件与密钥的用例换用 withMail 构造的服务器。

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"openfox/internal/api"
	"openfox/internal/secrets"
)

type orgSettingsView struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    string  `json:"description"`
	IconKind       *string `json:"icon_kind"`
	Icon           *string `json:"icon"`
	DefaultLocale  string  `json:"default_locale"`
	PriorityLevels int     `json:"priority_levels"`
	Version        int     `json:"version"`
	CanManage      bool    `json:"can_manage"`
	CanDelete      bool    `json:"can_delete"`
	LeaveBlocked   *string `json:"leave_blocked"`
}

type invitationView struct {
	ID             string   `json:"id"`
	Email          string   `json:"email"`
	Role           string   `json:"role"`
	Status         string   `json:"status"`
	SpaceIDs       []string `json:"space_ids"`
	DeliveryStatus string   `json:"delivery_status"`
}

type inviteLinkView struct {
	Enabled  bool     `json:"enabled"`
	Role     string   `json:"role"`
	SpaceIDs []string `json:"space_ids"`
	Token    *string  `json:"token"`
	Version  int      `json:"version"`
}

// recordingMail 记录发出的邀请邮件；fail 为 true 时模拟发送失败。
type recordingMail struct {
	mu     sync.Mutex
	fail   bool
	bodies map[string]string
}

func (m *recordingMail) Send(_ context.Context, to, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return context.DeadlineExceeded
	}
	m.bodies[to] = body
	return nil
}

var inviteLinkPattern = regexp.MustCompile(`/join\?invite=([0-9a-f-]+)&token=([A-Za-z0-9_%-]+)`)

// token 取最近一封发给 to 的邀请邮件里的令牌。
func (m *recordingMail) token(t *testing.T, to string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	match := inviteLinkPattern.FindStringSubmatch(m.bodies[to])
	if match == nil {
		t.Fatalf("no invitation mail to %s: %q", to, m.bodies[to])
	}
	token, _ := url.QueryUnescape(match[2])
	return token
}

// withMail 换用带邮件发送器与密钥的服务器，并清空已登录的客户端（会话在库里，重新登录即可）。
func withMail(env *s3Env) *recordingMail {
	env.t.Helper()
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		env.t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		env.t.Fatal(err)
	}
	mail := &recordingMail{bodies: map[string]string{}}
	auth := env.auth
	auth.EmailSender = mail
	server := httptestServer(env, api.NewWithDispatch(auth, env.database, false, nil, api.DefaultAuthRateLimits,
		api.DispatchOptions{Secrets: box, PublicURL: "https://openfox.example"}))
	env.server = server
	env.clients = map[string]*s3Client{}
	return mail
}

// newUser 建一个只有账号、不属于任何组织的用户。
func (e *s3Env) newUser() string {
	e.t.Helper()
	id := s3NewID(e.t)
	e.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2)`, id, s3Email(id))
	e.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)`, id, s3Hash)
	return id
}

func (e *s3Env) orgSettings(client *s3Client) orgSettingsView {
	e.t.Helper()
	var view orgSettingsView
	client.must(http.StatusOK, http.MethodGet, "settings", nil, &view)
	return view
}

func (e *s3Env) patchOrg(client *s3Client, body map[string]any) s3Response {
	e.t.Helper()
	if _, ok := body["version"]; !ok {
		body["version"] = e.orgSettings(e.owner()).Version
	}
	return client.do(http.MethodPatch, "settings", body)
}

func TestOrganizationSettings(t *testing.T) {
	env := newS3Env(t)
	owner, admin, member := env.owner(), env.admin(), env.member()

	view := env.orgSettings(member)
	if view.CanManage || view.CanDelete || view.PriorityLevels != 4 || view.DefaultLocale != "zh-CN" {
		t.Fatalf("member view = %+v", view)
	}
	if got := env.orgSettings(owner); !got.CanManage || !got.CanDelete || got.LeaveBlocked == nil || *got.LeaveBlocked != "sole_owner" {
		t.Fatalf("owner view = %+v", got)
	}
	if env.outsider().do(http.MethodGet, "settings", nil).Status != http.StatusNotFound {
		t.Fatal("outsider must not read another organization's settings")
	}
	if response := env.patchOrg(member, map[string]any{"name": "x"}); response.Status != http.StatusForbidden {
		t.Fatalf("member patch = %d %s", response.Status, response.Body)
	}

	// 改名、描述、图标、语言；version 递增；旧 version 冲突。
	before := env.orgSettings(owner)
	var saved orgSettingsView
	response := env.patchOrg(admin, map[string]any{"version": before.Version, "name": "  改名团队  ", "description": "做研发",
		"icon": map[string]any{"kind": "emoji", "value": "🦊"}, "default_locale": "en-US"})
	response.decode(t, &saved)
	if response.Status != http.StatusOK || saved.Name != "改名团队" || saved.Description != "做研发" || saved.IconKind == nil ||
		*saved.Icon != "🦊" || saved.DefaultLocale != "en-US" || saved.Version != before.Version+1 {
		t.Fatalf("patch = %d %s", response.Status, response.Body)
	}
	if response := env.patchOrg(admin, map[string]any{"version": before.Version, "name": "y"}); response.code() != "VERSION_CONFLICT" {
		t.Fatalf("stale patch = %d %s", response.Status, response.Body)
	}
	env.patchOrg(admin, map[string]any{"icon": nil})
	if got := env.orgSettings(owner); got.IconKind != nil || got.Icon != nil {
		t.Fatalf("icon not cleared: %+v", got)
	}
	var directoryName string
	env.scalar(&directoryName, `SELECT name FROM openfox.organization_directory WHERE organization_id = $1::uuid`, env.org)
	if directoryName != "改名团队" {
		t.Fatalf("directory name = %q", directoryName)
	}
	if env.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid
		AND type = 'organization.changed'`, env.org) < 2 {
		t.Fatal("organization.changed events missing")
	}

	// slug 校验：格式、保留字、被占用；合法时更新目录。
	var otherSlug string
	env.scalar(&otherSlug, `SELECT slug FROM openfox.organizations WHERE id = $1::uuid`, env.otherOrg)
	for _, tc := range []struct {
		slug, code string
		status     int
	}{
		{"Lin_Team", "SLUG_INVALID", 400}, {"ab", "SLUG_INVALID", 400}, {"admin", "SLUG_RESERVED", 400},
		{otherSlug, "SLUG_TAKEN", 409},
	} {
		if response := env.patchOrg(owner, map[string]any{"slug": tc.slug}); response.Status != tc.status || response.code() != tc.code {
			t.Fatalf("slug %q = %d %s", tc.slug, response.Status, response.Body)
		}
	}
	newSlug := "lin-" + s3Suffix(t)
	if response := env.patchOrg(owner, map[string]any{"slug": newSlug}); response.Status != http.StatusOK {
		t.Fatalf("slug = %d %s", response.Status, response.Body)
	}
	if env.count(`SELECT count(*) FROM openfox.organization_directory WHERE slug = $1`, newSlug) != 1 {
		t.Fatal("directory slug not synced")
	}
	for _, body := range []map[string]any{
		{"default_locale": "fr-FR"}, {"priority_levels": 3}, {"name": "   "},
		{"icon": map[string]any{"kind": "upload", "value": s3NewID(t)}},
	} {
		if response := env.patchOrg(owner, body); response.Status != http.StatusBadRequest {
			t.Fatalf("patch %v = %d %s", body, response.Status, response.Body)
		}
	}
}

func TestOrganizationPriorityLevels(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.createSpace("shared")
	path := "spaces/" + space.ID + "/tasks"
	owner.expectError(http.StatusBadRequest, "PRIORITY_LEVEL_UNAVAILABLE", http.MethodPost, path,
		map[string]any{"title": "lowest", "priority": "LOWEST"})
	if response := env.patchOrg(owner, map[string]any{"priority_levels": 5}); response.Status != http.StatusOK {
		t.Fatalf("to 5 = %d %s", response.Status, response.Body)
	}
	task := env.createTask(owner, space.ID, map[string]any{"priority": "LOWEST"})
	var counted struct{ Count int }
	owner.must(http.StatusOK, http.MethodGet, "settings/lowest-priority-count", nil, &counted)
	if counted.Count != 1 {
		t.Fatalf("lowest count = %d", counted.Count)
	}
	env.member().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodGet, "settings/lowest-priority-count", nil)
	if response := env.patchOrg(owner, map[string]any{"priority_levels": 4}); response.Status != http.StatusOK {
		t.Fatalf("to 4 = %d %s", response.Status, response.Body)
	}
	var priority string
	var version int
	if err := env.superuser.QueryRow(env.ctx, `SELECT priority, version FROM openfox.tasks WHERE id = $1::uuid`, task.ID).
		Scan(&priority, &version); err != nil {
		t.Fatal(err)
	}
	if priority != "LOW" || version <= task.Version {
		t.Fatalf("downgraded task = %s v%d (was v%d)", priority, version, task.Version)
	}

	// 并发：一边切换为 4 级，一边写 LOWEST；结束后不会残留 LOWEST。
	env.patchOrg(owner, map[string]any{"priority_levels": 5})
	statuses := parallel(9, func(index int) int {
		if index == 0 {
			return env.patchOrg(owner, map[string]any{"priority_levels": 4}).Status
		}
		return owner.do(http.MethodPost, path, map[string]any{"title": "race", "priority": "LOWEST"}).Status
	})
	if statuses[0] != http.StatusOK {
		t.Fatalf("switch during race = %d", statuses[0])
	}
	if left := env.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND priority = 'LOWEST'`,
		env.org); left != 0 {
		t.Fatalf("%d LOWEST tasks left after switching to 4 levels (statuses %v)", left, statuses)
	}
}

func TestOrganizationLeaveAndDelete(t *testing.T) {
	env := newS3Env(t)
	owner, member := env.owner(), env.member()

	owner.expectError(http.StatusConflict, "OWNER_REQUIRED", http.MethodPost, "leave", nil)
	env.outsider().doIn(env.otherOrg, http.MethodPost, "leave", nil)
	if response := env.outsider().doIn(env.otherOrg, http.MethodPost, "leave", nil); response.code() != "SOLE_MEMBER" {
		t.Fatalf("sole member leave = %d %s", response.Status, response.Body)
	}
	member.must(http.StatusNoContent, http.MethodPost, "leave", nil, nil)
	if member.do(http.MethodGet, "settings", nil).Status != http.StatusNotFound {
		t.Fatal("member can still read the organization after leaving")
	}
	var status string
	env.scalar(&status, `SELECT status FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`,
		env.org, s3MemberID)
	if status != "REVOKED" {
		t.Fatalf("left member status = %s", status)
	}

	deviceID, agentID := s3NewID(t), s3NewID(t)
	env.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots) VALUES ($1::uuid, $2, 1)`,
		deviceID, "s4-device-"+s3Suffix(t))
	env.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, env.org, deviceID)
	env.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'Agent', 'PRIVATE', 1)`, env.org, agentID, deviceID, env.space.ID, s3OwnerID)

	name := env.orgSettings(owner).Name
	env.admin().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodDelete, "/v1/organizations/"+env.org,
		map[string]any{"confirm_name": name})
	for _, confirm := range []string{strings.ToUpper(name), name + " ", ""} {
		owner.expectError(http.StatusBadRequest, "CONFIRM_MISMATCH", http.MethodDelete, "/v1/organizations/"+env.org,
			map[string]any{"confirm_name": confirm})
	}
	owner.must(http.StatusNoContent, http.MethodDelete, "/v1/organizations/"+env.org, map[string]any{"confirm_name": name}, nil)
	for _, client := range []*s3Client{owner, env.admin(), env.other()} {
		if response := client.do(http.MethodGet, "spaces", nil); response.Status != http.StatusNotFound {
			t.Fatalf("deleted organization still readable: %d", response.Status)
		}
	}
	var listed struct {
		Organizations []struct{ ID string } `json:"organizations"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/v1/me/organizations", nil, &listed)
	for _, item := range listed.Organizations {
		if item.ID == env.org {
			t.Fatal("deleted organization is still listed")
		}
	}
	if env.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'organization.deleted'`,
		env.org) != 1 {
		t.Fatal("organization.deleted audit missing")
	}
	if env.count(`SELECT count(*) FROM openfox.agents WHERE id = $1::uuid AND archived_at IS NOT NULL`, agentID) != 1 ||
		env.count(`SELECT count(*) FROM openfox.device_grants WHERE organization_id = $1::uuid AND status = 'ACTIVE'`, env.org) != 0 {
		t.Fatal("deleting the organization must archive agents and revoke computer grants")
	}
	// 数据留库。
	if env.count(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid`, env.org) == 0 {
		t.Fatal("soft delete must keep the data")
	}
}

func TestInvitationsJoinSpaces(t *testing.T) {
	env := newS3Env(t)
	mail := withMail(env)
	owner, admin := env.owner(), env.admin()
	invites := "invites"

	admin.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, invites,
		map[string]any{"email": "a@example.test", "role": "ADMIN"})
	// 管理员看不到私密空间 env.space，不能把人邀请进去。
	admin.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, invites,
		map[string]any{"email": "a@example.test", "role": "MEMBER", "space_ids": []string{env.space.ID}})
	owner.expectError(http.StatusConflict, "ALREADY_MEMBER", http.MethodPost, invites,
		map[string]any{"email": s3Email(s3MemberID), "role": "MEMBER"})

	newcomer := env.newUser()
	var invited invitationView
	owner.must(http.StatusCreated, http.MethodPost, invites,
		map[string]any{"email": s3Email(newcomer), "role": "GUEST", "space_ids": []string{env.space.ID, env.space.ID}}, &invited)
	if invited.DeliveryStatus != "SENT" || len(invited.SpaceIDs) != 1 || invited.SpaceIDs[0] != env.space.ID {
		t.Fatalf("invitation = %+v", invited)
	}
	firstToken := mail.token(t, s3Email(newcomer))

	// 重新发送：冷却期内 429；过了冷却期换新令牌，旧令牌失效。
	owner.expectError(http.StatusTooManyRequests, "RESEND_COOLDOWN", http.MethodPost, invites+"/"+invited.ID+"/resend", nil)
	env.exec(`UPDATE openfox.invitations SET last_sent_at = last_sent_at - interval '2 minutes' WHERE id = $1::uuid`, invited.ID)
	owner.must(http.StatusOK, http.MethodPost, invites+"/"+invited.ID+"/resend", nil, nil)
	token := mail.token(t, s3Email(newcomer))
	if token == firstToken {
		t.Fatal("resend must rotate the token")
	}
	joiner := env.client(newcomer)
	joiner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "/v1/invites/"+invited.ID+"/accept",
		map[string]any{"token": firstToken})
	joiner.must(http.StatusOK, http.MethodPost, "/v1/invites/"+invited.ID+"/accept", map[string]any{"token": token}, nil)
	if env.count(`SELECT count(*) FROM openfox.space_members WHERE space_id = $1::uuid AND user_id = $2::uuid AND status = 'ACTIVE'`,
		env.space.ID, newcomer) != 1 {
		t.Fatal("accepted invitation did not join the space")
	}
	joiner.must(http.StatusOK, http.MethodGet, "spaces/"+env.space.ID, nil, nil)

	// 发送失败记为 FAILED；被移除的成员可以凭新邀请重新加入。
	mail.fail = true
	owner.must(http.StatusOK, http.MethodPatch, "members/"+s3OtherID, map[string]any{"status": "REVOKED"}, nil)
	var failed invitationView
	owner.must(http.StatusCreated, http.MethodPost, invites, map[string]any{"email": s3Email(s3OtherID), "role": "MEMBER"}, &failed)
	if failed.DeliveryStatus != "FAILED" {
		t.Fatalf("delivery = %s", failed.DeliveryStatus)
	}
	mail.fail = false
	env.exec(`UPDATE openfox.invitations SET last_sent_at = last_sent_at - interval '2 minutes' WHERE id = $1::uuid`, failed.ID)
	owner.must(http.StatusOK, http.MethodPost, invites+"/"+failed.ID+"/resend", nil, nil)
	env.client(s3OtherID).must(http.StatusOK, http.MethodPost, "/v1/invites/"+failed.ID+"/accept",
		map[string]any{"token": mail.token(t, s3Email(s3OtherID))}, nil)
	env.clients = map[string]*s3Client{}
	env.other().must(http.StatusOK, http.MethodGet, "settings", nil, nil)

	// 成员列表的空间只包含当前用户能读到的。
	type memberRow struct {
		UserID string `json:"user_id"`
		Spaces []struct {
			ID string `json:"id"`
		} `json:"spaces"`
	}
	spacesOf := func(client *s3Client, userID string) int {
		var page struct{ Members []memberRow }
		client.must(http.StatusOK, http.MethodGet, "members?limit=100", nil, &page)
		for _, row := range page.Members {
			if row.UserID == userID {
				return len(row.Spaces)
			}
		}
		t.Fatalf("member %s not listed", userID)
		return 0
	}
	if spacesOf(env.owner(), s3MemberID) != 1 || spacesOf(env.admin(), s3MemberID) != 0 {
		t.Fatal("member spaces must only include spaces readable by the viewer")
	}
}

func TestInviteLinks(t *testing.T) {
	env := newS3Env(t)
	// 没有密钥时不能生成链接。
	env.owner().expectError(http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", http.MethodPut, "invite-link",
		map[string]any{"enabled": true, "role": "MEMBER", "space_ids": []string{}, "version": 0})
	withMail(env)
	owner := env.owner()
	env.member().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodGet, "invite-link", nil)
	var link inviteLinkView
	owner.must(http.StatusOK, http.MethodGet, "invite-link", nil, &link)
	if link.Enabled || link.Token != nil || link.Version != 0 || link.Role != "MEMBER" {
		t.Fatalf("default link = %+v", link)
	}
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, "invite-link",
		map[string]any{"enabled": true, "role": "ADMIN", "space_ids": []string{}, "version": 0})
	env.admin().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, "invite-link",
		map[string]any{"enabled": true, "role": "GUEST", "space_ids": []string{env.space.ID}, "version": 0})
	owner.must(http.StatusOK, http.MethodPut, "invite-link",
		map[string]any{"enabled": true, "role": "GUEST", "space_ids": []string{env.space.ID}, "version": 0}, &link)
	if link.Token == nil || link.Version != 1 || !link.Enabled {
		t.Fatalf("saved link = %+v", link)
	}
	owner.expectError(http.StatusConflict, "INVITATION_CONFLICT", http.MethodPut, "invite-link",
		map[string]any{"enabled": true, "role": "GUEST", "space_ids": []string{}, "version": 0})
	var again inviteLinkView
	owner.must(http.StatusOK, http.MethodGet, "invite-link", nil, &again)
	if again.Token == nil || *again.Token != *link.Token {
		t.Fatal("the link must be shown again with the same token")
	}
	if env.count(`SELECT count(*) FROM openfox.invite_links WHERE organization_id = $1::uuid
		AND position(convert_to($2, 'UTF8') IN token_ciphertext) > 0`, env.org, *link.Token) != 0 {
		t.Fatal("the token must not be stored in plain text")
	}

	joinerID := env.newUser()
	joiner := env.client(joinerID)
	var preview struct {
		Name          string `json:"name"`
		Role          string `json:"role"`
		MemberCount   int    `json:"member_count"`
		AlreadyMember bool   `json:"already_member"`
		InviterEmail  string `json:"inviter_email"`
	}
	joiner.must(http.StatusOK, http.MethodPost, "/v1/invite-links/preview", map[string]any{"token": *link.Token}, &preview)
	if preview.Role != "GUEST" || preview.MemberCount != len(s3Roles) || preview.AlreadyMember || preview.InviterEmail != s3Email(s3OwnerID) {
		t.Fatalf("preview = %+v", preview)
	}
	var joined struct{ Status, Role string }
	joiner.must(http.StatusOK, http.MethodPost, "/v1/invite-links/join", map[string]any{"token": *link.Token}, &joined)
	if joined.Status != "JOINED" || joined.Role != "GUEST" {
		t.Fatalf("join = %+v", joined)
	}
	joiner.must(http.StatusOK, http.MethodPost, "/v1/invite-links/join", map[string]any{"token": *link.Token}, &joined)
	if joined.Status != "ALREADY_MEMBER" {
		t.Fatalf("second join = %+v", joined)
	}
	joiner.must(http.StatusOK, http.MethodGet, "spaces/"+env.space.ID, nil, nil)

	// 重置：旧链接立即失效；停用后新链接也不能用。
	var reset inviteLinkView
	owner.must(http.StatusOK, http.MethodPost, "invite-link/reset", map[string]any{"version": link.Version}, &reset)
	if *reset.Token == *link.Token {
		t.Fatal("reset must rotate the token")
	}
	outsider := env.outsider()
	outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "/v1/invite-links/preview", map[string]any{"token": *link.Token})
	outsider.must(http.StatusOK, http.MethodPost, "/v1/invite-links/preview", map[string]any{"token": *reset.Token}, nil)
	owner.must(http.StatusOK, http.MethodPut, "invite-link",
		map[string]any{"enabled": false, "role": "MEMBER", "space_ids": []string{}, "version": reset.Version}, nil)
	outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "/v1/invite-links/join", map[string]any{"token": *reset.Token})
	if env.count(`SELECT count(*) FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`,
		env.org, s3OutsiderID) != 0 {
		t.Fatal("a disabled link must not add members")
	}
}

func httptestServer(env *s3Env, handler http.Handler) *httptest.Server {
	server := httptest.NewServer(handler)
	env.t.Cleanup(server.Close)
	return server
}
