package stage2

// 快捷回复集成测试：个人 / 组织两级、权限、乐观锁、排序、条数上限（含并发）、幂等创建、组织隔离与审计。

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

type quickReplyView struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	OwnerUserID string `json:"owner_user_id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Position    int    `json:"position"`
	Version     int    `json:"version"`
	CanEdit     bool   `json:"can_edit"`
}

type quickReplyList struct {
	Personal     []quickReplyView `json:"personal"`
	Organization []quickReplyView `json:"organization"`
}

func ids(items []quickReplyView) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}

func TestQuickReplies(t *testing.T) {
	e := newAgEnv(t)
	member, other, admin, outsider := e.client(agMemberID), e.client(agManagerID), e.client(agAdminID), e.client(agOutsiderID)
	create := func(c *agClient, scope, title, body string) quickReplyView {
		t.Helper()
		var item quickReplyView
		c.must(http.StatusCreated, http.MethodPost, "quick-replies", map[string]any{"scope": scope, "title": title, "body": body}, &item)
		return item
	}
	list := func(c *agClient) quickReplyList {
		t.Helper()
		var out quickReplyList
		c.must(http.StatusOK, http.MethodGet, "quick-replies", nil, &out)
		return out
	}

	first := create(member, "PERSONAL", "  谢谢  ", "  收到，谢谢！  ")
	second := create(member, "PERSONAL", "稍后", "我稍后处理")
	if first.Title != "谢谢" || first.Body != "收到，谢谢！" || first.Position != 0 || second.Position != 1 || !first.CanEdit || first.OwnerUserID != agMemberID || first.Version != 1 {
		t.Fatalf("created = %+v / %+v", first, second)
	}

	t.Run("personal replies are private", func(t *testing.T) {
		if got := list(other); len(got.Personal) != 0 {
			t.Fatalf("other sees %+v", got.Personal)
		}
		other.expectError(http.StatusNotFound, "", http.MethodPatch, "quick-replies/"+first.ID, map[string]any{"version": 1, "title": "hack"})
		other.expectError(http.StatusNotFound, "", http.MethodDelete, "quick-replies/"+first.ID, nil)
		outsider.expectError(http.StatusNotFound, "", http.MethodDelete, "quick-replies/"+first.ID, nil)
	})

	t.Run("update uses optimistic version", func(t *testing.T) {
		var updated quickReplyView
		member.must(http.StatusOK, http.MethodPatch, "quick-replies/"+first.ID, map[string]any{"version": 1, "body": "好的，收到"}, &updated)
		if updated.Version != 2 || updated.Body != "好的，收到" || updated.Title != "谢谢" {
			t.Fatalf("updated = %+v", updated)
		}
		member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "quick-replies/"+first.ID, map[string]any{"version": 1, "body": "stale"})
		member.expectError(http.StatusBadRequest, "", http.MethodPatch, "quick-replies/"+first.ID, map[string]any{"version": 2})
		member.expectError(http.StatusBadRequest, "", http.MethodPatch, "quick-replies/"+first.ID, map[string]any{"version": 2, "title": "   "})
	})

	t.Run("validation and idempotent create", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"scope": "PERSONAL", "title": "", "body": "x"},
			{"scope": "PERSONAL", "title": "x", "body": "  "},
			{"scope": "PERSONAL", "title": strings.Repeat("长", 41), "body": "x"},
			{"scope": "PERSONAL", "title": "x", "body": strings.Repeat("a", 2001)},
			{"scope": "TEAM", "title": "x", "body": "x"},
		} {
			member.expectError(http.StatusBadRequest, "", http.MethodPost, "quick-replies", body)
		}
		id := agNewID(t)
		body := map[string]any{"id": id, "scope": "PERSONAL", "title": "重试", "body": "同一次提交"}
		member.must(http.StatusCreated, http.MethodPost, "quick-replies", body, nil)
		member.must(http.StatusCreated, http.MethodPost, "quick-replies", body, nil)
		if n := len(list(member).Personal); n != 3 {
			t.Fatalf("personal = %d, want 3 after idempotent retry", n)
		}
		body["body"] = "内容不同"
		member.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, "quick-replies", body)
		member.must(http.StatusNoContent, http.MethodDelete, "quick-replies/"+id, nil, nil)
	})

	t.Run("reorder", func(t *testing.T) {
		var out struct {
			QuickReplies []quickReplyView `json:"quick_replies"`
		}
		member.must(http.StatusOK, http.MethodPut, "quick-replies/order", map[string]any{"scope": "PERSONAL", "ids": []string{second.ID, first.ID}}, &out)
		if !sameIDs(ids(out.QuickReplies), []string{second.ID, first.ID}) || !sameIDs(ids(list(member).Personal), []string{second.ID, first.ID}) {
			t.Fatalf("order = %+v", out.QuickReplies)
		}
		// 少一个、多一个、重复、别人的 ID 都不接受。
		for _, bad := range [][]string{{first.ID}, {first.ID, second.ID, agNewID(t)}, {first.ID, first.ID}} {
			member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPut, "quick-replies/order", map[string]any{"scope": "PERSONAL", "ids": bad})
		}
		member.expectError(http.StatusForbidden, "", http.MethodPut, "quick-replies/order", map[string]any{"scope": "ORG", "ids": []string{}})
	})

	t.Run("organization replies", func(t *testing.T) {
		member.expectError(http.StatusForbidden, "", http.MethodPost, "quick-replies", map[string]any{"scope": "ORG", "title": "x", "body": "x"})
		org := create(admin, "ORG", "欢迎", "欢迎加入！")
		if org.Scope != "ORG" || !org.CanEdit {
			t.Fatalf("org = %+v", org)
		}
		seen := list(member)
		if len(seen.Organization) != 1 || seen.Organization[0].ID != org.ID || seen.Organization[0].CanEdit {
			t.Fatalf("member sees %+v", seen.Organization)
		}
		member.expectError(http.StatusForbidden, "", http.MethodPatch, "quick-replies/"+org.ID, map[string]any{"version": 1, "title": "x"})
		member.expectError(http.StatusForbidden, "", http.MethodDelete, "quick-replies/"+org.ID, nil)
		var updated quickReplyView
		e.client(agOwnerID).must(http.StatusOK, http.MethodPatch, "quick-replies/"+org.ID, map[string]any{"version": 1, "title": "欢迎你"}, &updated)
		if updated.Title != "欢迎你" {
			t.Fatalf("updated = %+v", updated)
		}
		// 另一个组织看不到。
		if got := outsider.doIn(e.otherOrg, http.MethodGet, "quick-replies", nil); got.Status != http.StatusOK || strings.Contains(string(got.Body), org.ID) {
			t.Fatalf("other org sees %d %s", got.Status, got.Body)
		}
		if got := outsider.doIn(e.otherOrg, http.MethodDelete, "quick-replies/"+org.ID, nil); got.Status != http.StatusNotFound {
			t.Fatalf("cross-org delete = %d", got.Status)
		}
		// 审计：组织级写审计，个人级不写，且不含正文。
		var audited int
		e.scalar(&audited, `SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action LIKE 'quick_reply.%'`, e.org)
		if audited != 2 {
			t.Fatalf("audit events = %d, want 2 (created + updated)", audited)
		}
		admin.must(http.StatusNoContent, http.MethodDelete, "quick-replies/"+org.ID, nil, nil)
		member.expectError(http.StatusNotFound, "", http.MethodDelete, "quick-replies/"+org.ID, nil)
		if n := len(list(member).Organization); n != 0 {
			t.Fatalf("org after delete = %d", n)
		}
	})

	t.Run("limit is enforced under concurrency", func(t *testing.T) {
		c := e.client(agGuestID)
		for i := 0; i < 45; i++ {
			create(c, "PERSONAL", "t", "b")
		}
		var wg sync.WaitGroup
		statuses := make([]int, 10)
		for i := range statuses {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				statuses[i] = c.do(http.MethodPost, "quick-replies", map[string]any{"scope": "PERSONAL", "title": "p", "body": "b"}).Status
			}(i)
		}
		wg.Wait()
		created, limited := 0, 0
		for _, status := range statuses {
			switch status {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				limited++
			default:
				t.Fatalf("status = %d", status)
			}
		}
		if created != 5 || limited != 5 || len(list(c).Personal) != 50 {
			t.Fatalf("created %d limited %d total %d", created, limited, len(list(c).Personal))
		}
		c.expectError(http.StatusConflict, "QUICK_REPLY_LIMIT", http.MethodPost, "quick-replies", map[string]any{"scope": "PERSONAL", "title": "p", "body": "b"})
		// 删除后又能新建。
		c.must(http.StatusNoContent, http.MethodDelete, "quick-replies/"+list(c).Personal[0].ID, nil, nil)
		create(c, "PERSONAL", "again", "b")
	})
}
