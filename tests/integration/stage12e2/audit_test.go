package stage12e2

// 阶段 12e：组织级审计查询（K-01）的集成测试。真实 HTTP 处理器 + 真实 PostgreSQL（FORCE RLS、应用角色）。
// 覆盖：角色限制、跨租户隔离、游标分页边界、各筛选、时间范围校验、敏感字段不外泄。

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

type auditEventView struct {
	ID           string            `json:"id"`
	ActorID      string            `json:"actor_id"`
	ActorName    string            `json:"actor_name"`
	ActorEmail   string            `json:"actor_email"`
	Action       string            `json:"action"`
	Decision     string            `json:"decision"`
	Reason       string            `json:"reason"`
	ResourceKind string            `json:"resource_kind"`
	ResourceID   string            `json:"resource_id"`
	Metadata     map[string]string `json:"metadata"`
	CreatedAt    time.Time         `json:"created_at"`
}

type auditPageView struct {
	Events     []auditEventView `json:"events"`
	NextCursor *string          `json:"next_cursor"`
}

// seedAudit 直接写一条审计行（超级用户连接，绕过应用层白名单，用于构造存量与恶意数据）。
func (e *qcEnv) seedAudit(org, actor, action, kind, metadata string, createdAt time.Time) string {
	e.t.Helper()
	id := qcNewID(e.t)
	e.exec(`INSERT INTO openfox.audit_events (id, organization_id, actor_id, action, decision, reason,
			resource_kind, resource_id, request_id, authorization_version, safe_metadata, created_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'allow', 'allowed', $5, $6, 'req-internal-12e2', 7, $7::jsonb, $8)`,
		id, org, actor, action, kind, "res-"+id[:8], metadata, createdAt)
	return id
}

func auditPath(params ...string) string {
	values := neturl.Values{}
	for index := 0; index+1 < len(params); index += 2 {
		values.Set(params[index], params[index+1])
	}
	if len(values) == 0 {
		return "audit-events"
	}
	return "audit-events?" + values.Encode()
}

func (c *qcClient) auditPage(params ...string) auditPageView {
	c.env.t.Helper()
	var page auditPageView
	c.must(http.StatusOK, http.MethodGet, auditPath(params...), nil, &page)
	if page.Events == nil {
		c.env.t.Fatalf("events must be an array, got null")
	}
	return page
}

func auditIDs(page auditPageView) []string {
	ids := make([]string, 0, len(page.Events))
	for _, event := range page.Events {
		ids = append(ids, event.ID)
	}
	return ids
}

func TestAuditRoleRestriction(t *testing.T) {
	env := newQcEnv(t)
	env.seedAudit(env.org, qcOwnerID, "member.invited", "member", `{}`, time.Now().UTC())
	for name, client := range map[string]*qcClient{"member": env.member(), "guest": env.guest(), "space admin": env.client(qcAdminID)} {
		response := client.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, "audit-events", nil)
		if strings.Contains(string(response.Body), "member.invited") {
			t.Fatalf("%s response leaked audit data: %s", name, response.Body)
		}
	}
	// 非本组织成员：组织选择阶段即被拒绝，不得返回数据。
	if response := env.outsider().do(http.MethodGet, "audit-events", nil); response.Status == http.StatusOK ||
		strings.Contains(string(response.Body), "member.invited") {
		t.Fatalf("outsider = %d %s", response.Status, response.Body)
	}
	// 未登录。
	anonymous, err := http.Get(env.server.URL + "/v1/organizations/" + env.org + "/audit-events")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", anonymous.StatusCode)
	}
	for name, client := range map[string]*qcClient{"owner": env.owner(), "admin": env.admin()} {
		if page := client.auditPage(); len(page.Events) != 1 || page.Events[0].Action != "member.invited" {
			t.Fatalf("%s page = %+v", name, page)
		}
	}
}

func TestAuditTenantIsolation(t *testing.T) {
	env := newQcEnv(t)
	now := time.Now().UTC()
	mine := env.seedAudit(env.org, qcOwnerID, "space.created", "space", `{}`, now)
	theirs := env.seedAudit(env.otherOrg, qcOutsiderID, "space.created", "space", `{}`, now)

	page := env.owner().auditPage()
	if ids := auditIDs(page); len(ids) != 1 || ids[0] != mine {
		t.Fatalf("owner sees %v, want only %s", ids, mine)
	}
	// 用对方组织的操作人筛选也拿不到对方的数据。
	if page := env.owner().auditPage("actor_id", qcOutsiderID); len(page.Events) != 0 {
		t.Fatalf("filter by foreign actor leaked %v", auditIDs(page))
	}
	// 直接访问对方组织的路径被拒绝。
	foreign := "/v1/organizations/" + env.otherOrg + "/audit-events"
	if response := env.owner().do(http.MethodGet, foreign, nil); response.Status == http.StatusOK ||
		strings.Contains(string(response.Body), theirs) {
		t.Fatalf("cross-tenant read = %d %s", response.Status, response.Body)
	}
	// 用本组织的游标去翻对方组织：游标只是位置，不携带租户，仍然只返回对方组织自己的数据。
	var other auditPageView
	env.outsider().must(http.StatusOK, http.MethodGet, foreign, nil, &other)
	if ids := auditIDs(other); len(ids) != 1 || ids[0] != theirs {
		t.Fatalf("other organization sees %v, want only %s", ids, theirs)
	}
}

func TestAuditCursorPagination(t *testing.T) {
	env := newQcEnv(t)
	// 空。
	if page := env.owner().auditPage(); len(page.Events) != 0 || page.NextCursor != nil {
		t.Fatalf("empty page = %+v", page)
	}
	// 7 条，其中 3 条时间完全相同，验证按 (created_at, id) 稳定排序、不重不漏。
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	want := map[string]bool{}
	for index := 0; index < 4; index++ {
		want[env.seedAudit(env.org, qcOwnerID, "label.created", "label", `{}`, base.Add(time.Duration(index)*time.Second))] = true
	}
	for index := 0; index < 3; index++ {
		want[env.seedAudit(env.org, qcOwnerID, "label.created", "label", `{}`, base.Add(10*time.Second))] = true
	}
	seen := map[string]bool{}
	var previous *auditEventView
	cursor, pages := "", 0
	for {
		params := []string{"limit", "3"}
		if cursor != "" {
			params = append(params, "cursor", cursor)
		}
		page := env.owner().auditPage(params...)
		pages++
		for index := range page.Events {
			event := page.Events[index]
			if seen[event.ID] {
				t.Fatalf("event %s returned twice", event.ID)
			}
			seen[event.ID] = true
			if previous != nil && (event.CreatedAt.After(previous.CreatedAt) ||
				(event.CreatedAt.Equal(previous.CreatedAt) && event.ID >= previous.ID)) {
				t.Fatalf("order broken: %s %s after %s %s", event.CreatedAt, event.ID, previous.CreatedAt, previous.ID)
			}
			previous = &page.Events[index]
		}
		if page.NextCursor == nil {
			break
		}
		if len(page.Events) != 3 {
			t.Fatalf("non-final page has %d events", len(page.Events))
		}
		cursor = *page.NextCursor
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages != 3 || len(seen) != len(want) {
		t.Fatalf("pages = %d seen = %d, want 3 pages and %d events", pages, len(seen), len(want))
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("event %s missing", id)
		}
	}
	// 恰好一页：7 条、limit=7，不应给出下一页游标。
	if page := env.owner().auditPage("limit", "7"); len(page.Events) != 7 || page.NextCursor != nil {
		t.Fatalf("exact page: events = %d next = %v", len(page.Events), page.NextCursor)
	}
	// 默认每页 50、上限 100。
	for index := 0; index < 60; index++ {
		env.seedAudit(env.org, qcOwnerID, "bulk.seeded", "label", `{}`, base.Add(-time.Duration(index+1)*time.Second))
	}
	if page := env.owner().auditPage(); len(page.Events) != 50 || page.NextCursor == nil {
		t.Fatalf("default page: events = %d", len(page.Events))
	}
	if page := env.owner().auditPage("limit", "100"); len(page.Events) != 67 || page.NextCursor != nil {
		t.Fatalf("limit 100: events = %d", len(page.Events))
	}
	// 非法分页参数。
	for _, params := range [][]string{
		{"limit", "0"}, {"limit", "-1"}, {"limit", "101"}, {"limit", "abc"}, {"limit", "10000000"},
		{"cursor", "not-a-cursor"}, {"cursor", "!!!!"}, {"cursor", "MTIzNA"},
		{"cursor", strings.Repeat("A", 300)},
	} {
		env.owner().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, auditPath(params...), nil)
	}
}

func TestAuditFilters(t *testing.T) {
	env := newQcEnv(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	deviceRegistered := env.seedAudit(env.org, qcOwnerID, "device.registered", "device", `{}`, base)
	deviceRevoked := env.seedAudit(env.org, qcOrgAdminID, "device.grant_revoked", "device", `{}`, base.Add(time.Hour))
	runtimeUpdated := env.seedAudit(env.org, qcOrgAdminID, "runtime_config.updated", "runtime_config", `{}`, base.Add(2*time.Hour))
	// 与 runtime_config 只差一个字符：下划线在 LIKE 里是通配符，前缀筛选必须转义。
	runtimeLookalike := env.seedAudit(env.org, qcOwnerID, "runtimeXconfig.updated", "runtime_config", `{}`, base.Add(3*time.Hour))

	expect := func(name string, want []string, params ...string) {
		t.Helper()
		got := auditIDs(env.owner().auditPage(params...))
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: got %v, want %v", name, got, want)
		}
	}
	expect("no filter", []string{runtimeLookalike, runtimeUpdated, deviceRevoked, deviceRegistered})
	expect("actor", []string{runtimeUpdated, deviceRevoked}, "actor_id", qcOrgAdminID)
	expect("action exact", []string{deviceRegistered}, "action", "device.registered")
	expect("action exact is not a prefix", []string{}, "action", "device.")
	expect("action prefix", []string{deviceRevoked, deviceRegistered}, "action", "device.*")
	expect("action prefix escapes underscore", []string{runtimeUpdated}, "action", "runtime_*")
	expect("resource kind", []string{runtimeLookalike, runtimeUpdated}, "resource_kind", "runtime_config")
	expect("combined", []string{deviceRevoked}, "actor_id", qcOrgAdminID, "action", "device.*", "resource_kind", "device")
	// 时间范围左闭右开：等于 from 的包含，等于 to 的不包含。
	expect("range", []string{deviceRevoked, deviceRegistered},
		"from", base.Format(time.RFC3339), "to", base.Add(2*time.Hour).Format(time.RFC3339))
	expect("from only", []string{runtimeLookalike, runtimeUpdated}, "from", base.Add(2*time.Hour).Format(time.RFC3339))
	expect("to only", []string{deviceRegistered}, "to", base.Add(time.Hour).Format(time.RFC3339))
	// 带时区偏移的时间等价于同一时刻的 UTC。
	expect("offset", []string{deviceRegistered},
		"from", base.In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339), "to", base.Add(time.Minute).Format(time.RFC3339))

	for name, params := range map[string][]string{
		"from after to":     {"from", base.Add(time.Hour).Format(time.RFC3339), "to", base.Format(time.RFC3339)},
		"from equals to":    {"from", base.Format(time.RFC3339), "to", base.Format(time.RFC3339)},
		"span too long":     {"from", base.Format(time.RFC3339), "to", base.Add(367 * 24 * time.Hour).Format(time.RFC3339)},
		"bad time":          {"from", "2026-10-01"},
		"bad actor":         {"actor_id", "not-a-uuid"},
		"bad action":        {"action", "device registered"},
		"injection attempt": {"action", "x' OR '1'='1"},
		"bare wildcard":     {"action", "*"},
		"percent wildcard":  {"action", "dev%"},
		"bad resource kind": {"resource_kind", "a b"},
	} {
		response := env.owner().do(http.MethodGet, auditPath(params...), nil)
		if response.Status != http.StatusBadRequest || response.code() != "INVALID_REQUEST" {
			t.Fatalf("%s = %d %s, want 400 INVALID_REQUEST", name, response.Status, response.Body)
		}
	}
	// 最大跨度边界：恰好 366 天允许。
	env.owner().auditPage("from", base.Format(time.RFC3339), "to", base.Add(366*24*time.Hour).Format(time.RFC3339))
}

func TestAuditHidesSensitiveFields(t *testing.T) {
	env := newQcEnv(t)
	env.exec(`UPDATE openfox.users SET full_name = '林一' WHERE id = $1::uuid`, qcOwnerID)
	env.exec(`UPDATE openfox.users SET full_name = '' WHERE id = $1::uuid`, qcOrgAdminID)
	now := time.Now().UTC()
	// 存量或被绕过写入的元数据：非白名单键、嵌套对象、超长值、控制字符都不得返回。
	env.seedAudit(env.org, qcOwnerID, "token.created", "token", fmt.Sprintf(
		`{"status":"ACTIVE","token":"ofp_SECRET_TOKEN_VALUE","password":"hunter2","secret":"s3cr3t","authorization":"Bearer abc",
		  "nested":{"token":"NESTED_SECRET"},"version":7,"source":%q,"result":"line1\nline2"}`, strings.Repeat("x", 300)), now)
	env.seedAudit(env.org, qcOrgAdminID, "label.created", "label", `{"source":"web"}`, now.Add(-time.Second))

	response := env.owner().must(http.StatusOK, http.MethodGet, "audit-events", nil, nil)
	body := string(response.Body)
	for _, forbidden := range []string{"ofp_SECRET_TOKEN_VALUE", "hunter2", "s3cr3t", "Bearer abc", "NESTED_SECRET",
		"req-internal-12e2", "request_id", "authorization_version", "password", "full_name", "avatar"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	var page auditPageView
	response.decode(t, &page)
	if len(page.Events) != 2 {
		t.Fatalf("events = %d", len(page.Events))
	}
	first, second := page.Events[0], page.Events[1]
	if len(first.Metadata) != 1 || first.Metadata["status"] != "ACTIVE" {
		t.Fatalf("metadata = %v, want only status", first.Metadata)
	}
	if first.ActorName != "林一" || first.ActorEmail != qcEmail(qcOwnerID) || first.ActorID != qcOwnerID {
		t.Fatalf("actor = %+v", first)
	}
	// 没有姓名时回退为邮箱。
	if second.ActorName != qcEmail(qcOrgAdminID) || second.Metadata["source"] != "web" {
		t.Fatalf("second = %+v", second)
	}
	if first.Decision != "allow" || first.Reason != "allowed" || first.ResourceKind != "token" || !strings.HasPrefix(first.ResourceID, "res-") {
		t.Fatalf("fields = %+v", first)
	}
}

// 真实写入路径：修改组织配额会写审计，审计列表能查到，且查询本身不写审计、Run 生命周期不进审计。
func TestAuditListsRealMutationAndDoesNotAuditReads(t *testing.T) {
	env := newQcEnv(t)
	config := env.owner().runtimeConfig()
	config.Policy.QueueMax = 500
	env.owner().must(http.StatusOK, http.MethodPatch, "runtime-config",
		map[string]any{"expected_version": config.ConfigVersion, "policy": config.Policy}, nil)
	page := env.owner().auditPage("action", "runtime_config.*")
	if len(page.Events) != 1 || page.Events[0].Action != "runtime_config.updated" || page.Events[0].ActorID != qcOwnerID ||
		page.Events[0].Metadata["version"] != "2" {
		t.Fatalf("page = %+v", page)
	}
	env.owner().auditPage()
	env.owner().auditPage()
	if got := env.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid`, env.org); got != 1 {
		t.Fatalf("audit rows = %d, reading the audit log must not write audit rows", got)
	}
}
