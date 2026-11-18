package stage12g

// 阶段 12g 分享快照集成测试：创建权限、范围与跨组织、撤销与过期、公开开关、脱敏阻止、枚举不泄露、
// 来源事后修改不影响快照、并发撤销幂等、限流。

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type shareEnv struct {
	*qcEnv
	space        string
	agent        string
	conversation string
	task         taskView
	comment      string
}

type created struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
}

type viewBody struct {
	Title string `json:"title"`
	Items []struct {
		Author  string `json:"author"`
		Body    string `json:"body"`
		ReplyTo int    `json:"reply_to"`
	} `json:"items"`
	CreatorName      string `json:"creator_name"`
	OrganizationName string `json:"organization_name"`
}

func newShareEnv(t *testing.T) *shareEnv {
	env := newQcEnv(t)
	space := env.createSpace(nil)
	env.addSpaceMember(space.ID, qcMemberID, "MEMBER")
	env.addSpaceMember(space.ID, qcPeerID, "MEMBER")
	s := &shareEnv{qcEnv: env, space: space.ID}
	s.agent = env.seedAgent(space.ID, qcOwnerID, "SHARED")
	env.exec(`UPDATE openfox.users SET full_name = $2 WHERE id = $1::uuid`, qcMemberID, "成员甲")
	var conv struct {
		ID string `json:"id"`
	}
	env.member().must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": space.ID, "agent_id": s.agent, "visibility": "PRIVATE"}, &conv)
	s.conversation = conv.ID
	env.member().must(http.StatusCreated, http.MethodPost, "conversations/"+conv.ID+"/messages",
		map[string]any{"body": "第一条消息：部署步骤是先构建再发布。", "client_message_id": qcNewID(t)}, nil)
	s.task = env.createTask(env.owner(), space.ID, nil)
	var comment struct {
		ID string `json:"id"`
	}
	env.member().must(http.StatusCreated, http.MethodPost, taskPath(space.ID, s.task.ID)+"/comments",
		map[string]any{"body": "根评论内容", "client_comment_id": qcNewID(t)}, &comment)
	s.comment = comment.ID
	env.member().must(http.StatusCreated, http.MethodPost, taskPath(space.ID, s.task.ID)+"/comments",
		map[string]any{"body": "回复内容", "reply_to_id": comment.ID, "client_comment_id": qcNewID(t)}, nil)
	return s
}

func (s *shareEnv) allowPublic(on bool) {
	s.exec(`UPDATE openfox.organizations SET allow_public_sharing = $2 WHERE id = $1::uuid`, s.org, on)
}

func (s *shareEnv) convBody(scope string) map[string]any {
	return map[string]any{"kind": "CONVERSATION", "scope": scope, "conversation_id": s.conversation}
}

// view 匿名访问（不带 cookie）。
func (s *shareEnv) view(token string) qcResponse {
	s.t.Helper()
	anonymous := &qcClient{env: s.qcEnv, http: &http.Client{}}
	return anonymous.do(http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": token})
}

func (s *shareEnv) create(c *qcClient, body map[string]any) created {
	s.t.Helper()
	var out created
	c.must(http.StatusCreated, http.MethodPost, "share-snapshots", body, &out)
	return out
}

func TestCreateRequiresReadPermission(t *testing.T) {
	s := newShareEnv(t)
	s.allowPublic(true)
	// 同组织但不是会话成员（私有会话）与空间外成员：一律 404，不泄露来源存在。
	s.client(qcPeerID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "share-snapshots", s.convBody("ORGANIZATION"))
	s.outsider().expectError(http.StatusNotFound, "", http.MethodPost, "share-snapshots", s.convBody("ORGANIZATION"))
	s.guest().expectError(http.StatusNotFound, "", http.MethodPost, "share-snapshots", map[string]any{
		"kind": "COMMENT", "scope": "ORGANIZATION", "space_id": s.space, "task_id": s.task.ID, "comment_id": s.comment})
	// 有读权限的创建人成功，评论线程含回复。
	out := s.create(s.member(), s.convBody("ORGANIZATION"))
	if out.Token == "" || out.Status != "ACTIVE" {
		t.Fatalf("created = %+v", out)
	}
	s.create(s.member(), map[string]any{"kind": "COMMENT", "scope": "ORGANIZATION", "space_id": s.space,
		"task_id": s.task.ID, "comment_id": s.comment})
}

func TestOrganizationScopeRejectsOtherOrganizationAndAnonymous(t *testing.T) {
	s := newShareEnv(t)
	out := s.create(s.member(), s.convBody("ORGANIZATION"))
	if s.view(out.Token).Status != http.StatusNotFound {
		t.Fatal("匿名访问限组织快照应为 404")
	}
	view := func(c *qcClient) qcResponse {
		return c.do(http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": out.Token})
	}
	if r := view(s.outsider()); r.Status != http.StatusNotFound {
		t.Fatalf("跨组织成员 = %d", r.Status)
	}
	var body viewBody
	r := view(s.client(qcPeerID))
	if r.Status != http.StatusOK {
		t.Fatalf("同组织成员 = %d %s", r.Status, r.Body)
	}
	r.decode(t, &body)
	if body.CreatorName != "成员甲" || len(body.Items) != 1 || !strings.Contains(body.Items[0].Body, "部署步骤") {
		t.Fatalf("view = %+v", body)
	}
	// 成员被移出后立即不可读。
	s.exec(`UPDATE openfox.organization_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, s.org, qcPeerID)
	if r := view(s.client(qcPeerID)); r.Status != http.StatusNotFound {
		t.Fatalf("被移出成员 = %d", r.Status)
	}
	if strings.Contains(string(r.Body), "@example.test") || strings.Contains(string(s.view("x").Body), "部署") {
		t.Fatal("响应不应包含邮箱")
	}
}

func TestPublicViewHeadersAndNoEmail(t *testing.T) {
	s := newShareEnv(t)
	s.allowPublic(true)
	out := s.create(s.member(), s.convBody("PUBLIC"))
	r := s.view(out.Token)
	if r.Status != http.StatusOK {
		t.Fatalf("公开访问 = %d %s", r.Status, r.Body)
	}
	if r.Header.Get("X-Robots-Tag") != "noindex, nofollow" || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", r.Header)
	}
	if strings.Contains(string(r.Body), "@example.test") || strings.Contains(string(r.Body), qcMemberID) {
		t.Fatalf("响应泄露邮箱或用户 ID：%s", r.Body)
	}
	// 404 响应同样带头。
	if nf := s.view("nope"); nf.Header.Get("X-Robots-Tag") == "" || nf.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("404 headers = %v", nf.Header)
	}
}

func TestRevokeAndExpiryReturn404(t *testing.T) {
	s := newShareEnv(t)
	s.allowPublic(true)
	a := s.create(s.member(), s.convBody("PUBLIC"))
	s.member().must(http.StatusOK, http.MethodDelete, "share-snapshots/"+a.ID, nil, nil)
	if s.view(a.Token).Status != http.StatusNotFound {
		t.Fatal("撤销后应 404")
	}
	// 撤销不可恢复：库层面也拒绝清除 revoked_at。
	if _, err := s.superuser.Exec(s.ctx, `UPDATE openfox.share_snapshots SET revoked_at = NULL, revoked_by = NULL WHERE id = $1::uuid`, a.ID); err == nil {
		t.Fatal("已撤销的快照不应能被恢复")
	}
	b := s.create(s.member(), s.convBody("PUBLIC"))
	if s.view(b.Token).Status != http.StatusOK {
		t.Fatal("未过期应可读")
	}
	s.exec(`UPDATE openfox.share_snapshots SET created_at = clock_timestamp() - interval '2 days', expires_at = clock_timestamp() - interval '1 day' WHERE id = $1::uuid`, b.ID)
	if s.view(b.Token).Status != http.StatusNotFound {
		t.Fatal("过期后应 404")
	}
	// 永不过期选项。
	c := s.create(s.member(), map[string]any{"kind": "CONVERSATION", "scope": "PUBLIC", "conversation_id": s.conversation, "expiry": "never"})
	var expires *time.Time
	s.scalar(&expires, `SELECT expires_at FROM openfox.share_snapshots WHERE id = $1::uuid`, c.ID)
	if expires != nil {
		t.Fatalf("never 应无过期时间：%v", expires)
	}
	s.member().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "share-snapshots",
		map[string]any{"kind": "CONVERSATION", "scope": "PUBLIC", "conversation_id": s.conversation, "expiry": "1y"})
}

func TestRevokePermissions(t *testing.T) {
	s := newShareEnv(t)
	out := s.create(s.member(), s.convBody("ORGANIZATION"))
	// 其他普通成员既看不到也撤不了。
	s.client(qcPeerID).expectError(http.StatusNotFound, "", http.MethodDelete, "share-snapshots/"+out.ID, nil)
	var list struct {
		Items []struct{ ID string } `json:"share_snapshots"`
	}
	s.client(qcPeerID).must(http.StatusOK, http.MethodGet, "share-snapshots", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("普通成员不应看到他人快照：%+v", list)
	}
	// Owner 看得到并可撤销。
	s.owner().must(http.StatusOK, http.MethodGet, "share-snapshots", nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("owner 应看到全部：%+v", list)
	}
	s.owner().must(http.StatusOK, http.MethodDelete, "share-snapshots/"+out.ID, nil, nil)
	// 审计：创建与撤销各一条。
	if n := s.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'share_snapshot.created'`, s.org); n != 1 {
		t.Fatalf("created 审计 = %d", n)
	}
	if n := s.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'share_snapshot.revoked'`, s.org); n != 1 {
		t.Fatalf("revoked 审计 = %d", n)
	}
	// 审计里不含令牌或正文。
	if n := s.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action LIKE 'share_snapshot.%' AND safe_metadata::text LIKE '%部署%'`, s.org); n != 0 {
		t.Fatal("审计不应含正文")
	}
}

func TestPublicSharingSwitch(t *testing.T) {
	s := newShareEnv(t)
	// 默认关闭：不能创建公开快照，限组织不受影响。
	var value bool
	s.scalar(&value, `SELECT allow_public_sharing FROM openfox.organizations WHERE id = $1::uuid`, s.org)
	if value {
		t.Fatal("默认应关闭")
	}
	s.member().expectError(http.StatusForbidden, "PUBLIC_SHARING_DISABLED", http.MethodPost, "share-snapshots", s.convBody("PUBLIC"))
	org := s.create(s.member(), s.convBody("ORGANIZATION"))
	// 经设置接口打开（仅 Owner / Admin）。
	var settings struct {
		Version            int  `json:"version"`
		AllowPublicSharing bool `json:"allow_public_sharing"`
	}
	s.owner().must(http.StatusOK, http.MethodGet, "settings", nil, &settings)
	s.member().expectError(http.StatusForbidden, "", http.MethodPatch, "settings", map[string]any{"version": settings.Version, "allow_public_sharing": true})
	s.owner().must(http.StatusOK, http.MethodPatch, "settings", map[string]any{"version": settings.Version, "allow_public_sharing": true}, &settings)
	if !settings.AllowPublicSharing {
		t.Fatal("开关未打开")
	}
	pub := s.create(s.member(), s.convBody("PUBLIC"))
	if s.view(pub.Token).Status != http.StatusOK {
		t.Fatal("打开后公开可读")
	}
	// 关闭：公开快照 404，限组织不受影响；重新打开后恢复。
	s.owner().must(http.StatusOK, http.MethodPatch, "settings", map[string]any{"version": settings.Version, "allow_public_sharing": false}, &settings)
	if s.view(pub.Token).Status != http.StatusNotFound {
		t.Fatal("关闭后公开快照应 404")
	}
	if r := s.client(qcPeerID).do(http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": org.Token}); r.Status != http.StatusOK {
		t.Fatalf("限组织快照不受影响 = %d", r.Status)
	}
	s.member().expectError(http.StatusForbidden, "PUBLIC_SHARING_DISABLED", http.MethodPost, "share-snapshots", s.convBody("PUBLIC"))
	s.allowPublic(true)
	if s.view(pub.Token).Status != http.StatusOK {
		t.Fatal("重新打开后应恢复")
	}
	if n := s.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'organization.public_sharing_changed'`, s.org); n != 2 {
		t.Fatalf("开关审计 = %d", n)
	}
}

func TestRedactionBlocksCreation(t *testing.T) {
	s := newShareEnv(t)
	s.member().must(http.StatusCreated, http.MethodPost, "conversations/"+s.conversation+"/messages",
		map[string]any{"body": "line one\n密钥 AKIAIOSFODNN7EXAMPLE 不要外传", "client_message_id": qcNewID(t)}, nil)
	response := s.member().expectError(http.StatusUnprocessableEntity, "SENSITIVE_CONTENT_DETECTED", http.MethodPost, "share-snapshots", s.convBody("ORGANIZATION"))
	body := string(response.Body)
	if !strings.Contains(body, `"rule":"aws_access_key_id"`) || !strings.Contains(body, `"item":2`) || !strings.Contains(body, `"line":2`) {
		t.Fatalf("应指出位置：%s", body)
	}
	if strings.Contains(body, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatal("响应不应回显命中原文")
	}
	if n := s.count(`SELECT count(*) FROM openfox.share_snapshots WHERE organization_id = $1::uuid`, s.org); n != 0 {
		t.Fatalf("命中后不应创建，实际 %d", n)
	}
}

func TestTokenEnumerationIndistinguishable(t *testing.T) {
	s := newShareEnv(t)
	s.allowPublic(true)
	revoked := s.create(s.member(), s.convBody("PUBLIC"))
	s.member().must(http.StatusOK, http.MethodDelete, "share-snapshots/"+revoked.ID, nil, nil)
	missing := s.view(strings.Repeat("A", 43))
	gone := s.view(revoked.Token)
	garbage := s.view("")
	for _, r := range []qcResponse{missing, gone, garbage} {
		if r.Status != http.StatusNotFound {
			t.Fatalf("status = %d", r.Status)
		}
	}
	strip := func(r qcResponse) string {
		var b struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		r.decode(t, &b)
		return b.Error.Code + "|" + b.Error.Message + "|" + r.Header.Get("Cache-Control") + "|" + r.Header.Get("X-Robots-Tag")
	}
	if strip(missing) != strip(gone) || strip(missing) != strip(garbage) {
		t.Fatalf("不存在与已撤销的响应可区分：%s / %s / %s", strip(missing), strip(gone), strip(garbage))
	}
}

func TestSnapshotIsImmutableAfterSourceChanges(t *testing.T) {
	s := newShareEnv(t)
	s.allowPublic(true)
	out := s.create(s.member(), s.convBody("PUBLIC"))
	s.exec(`UPDATE openfox.messages SET body = '已被改写' WHERE conversation_id = $1::uuid`, s.conversation)
	var body viewBody
	r := s.view(out.Token)
	r.decode(t, &body)
	if r.Status != http.StatusOK || !strings.Contains(body.Items[0].Body, "部署步骤") {
		t.Fatalf("来源改写不应影响快照：%s", r.Body)
	}
	// 来源整体删除（含空间与会话）后依然可读：快照不引用来源。
	s.exec(`DELETE FROM openfox.messages WHERE conversation_id = $1::uuid`, s.conversation)
	if r := s.view(out.Token); r.Status != http.StatusOK {
		t.Fatalf("来源删除后 = %d %s", r.Status, r.Body)
	}
	// 应用角色不能改快照内容。
	if n := s.count(`SELECT count(*) FROM information_schema.column_privileges WHERE table_schema='openfox' AND table_name='share_snapshots'
		AND grantee='openfox_app' AND privilege_type='UPDATE' AND column_name NOT IN ('revoked_at','revoked_by')`); n != 0 {
		t.Fatalf("应用角色不应能更新内容列：%d", n)
	}
	if n := s.count(`SELECT count(*) FROM information_schema.role_table_grants WHERE table_schema='openfox' AND table_name='share_snapshots'
		AND grantee='openfox_app' AND privilege_type='DELETE'`); n != 0 {
		t.Fatal("应用角色不应有 DELETE")
	}
}

func TestCommentThreadSnapshotContent(t *testing.T) {
	s := newShareEnv(t)
	out := s.create(s.member(), map[string]any{"kind": "COMMENT", "scope": "ORGANIZATION", "space_id": s.space,
		"task_id": s.task.ID, "comment_id": s.comment})
	var body viewBody
	r := s.client(qcPeerID).do(http.MethodPost, "/v1/share-snapshots/view", map[string]any{"token": out.Token})
	r.decode(t, &body)
	if len(body.Items) != 2 || body.Items[1].ReplyTo != 1 || body.Items[0].Author != "成员甲" {
		t.Fatalf("评论线程 = %+v", body)
	}
}

func TestConcurrentRevokeIsIdempotent(t *testing.T) {
	s := newShareEnv(t)
	out := s.create(s.member(), s.convBody("ORGANIZATION"))
	var wg sync.WaitGroup
	statuses := make([]int, 8)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := s.owner()
			if i%2 == 0 {
				c = s.member()
			}
			statuses[i] = c.do(http.MethodDelete, "share-snapshots/"+out.ID, nil).Status
		}(i)
	}
	wg.Wait()
	for _, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("并发撤销应全部成功：%v", statuses)
		}
	}
	if n := s.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'share_snapshot.revoked'`, s.org); n != 1 {
		t.Fatalf("并发撤销只应写一条审计，实际 %d", n)
	}
}

func TestPublicViewRateLimited(t *testing.T) {
	s := newShareEnv(t)
	limited := false
	for i := 0; i < 80; i++ {
		if s.view("probe").Status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("连续探测应触发限流")
	}
}

func TestQuotaPerUser(t *testing.T) {
	s := newShareEnv(t)
	// 预先灌满该用户的有效快照，再创建应被拒绝。
	s.exec(`INSERT INTO openfox.share_snapshots (organization_id, id, kind, scope, title, content, item_count, token_hash, created_by, token_ciphertext, token_nonce, token_key_id)
		SELECT $1::uuid, gen_random_uuid(), 'CONVERSATION', 'ORGANIZATION', 't', '{"items":[]}'::jsonb, 1, sha256(($1::text||g::text)::bytea), $2::uuid, decode(repeat('00',59),'hex'), decode(repeat('00',12),'hex'),'0000000000000000'
		  FROM generate_series(1, 100) g`, s.org, qcMemberID)
	s.member().expectError(http.StatusConflict, "SHARE_QUOTA_EXCEEDED", http.MethodPost, "share-snapshots", s.convBody("ORGANIZATION"))
}
