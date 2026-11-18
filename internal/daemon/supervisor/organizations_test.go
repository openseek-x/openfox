package supervisor

import (
	"sort"
	"testing"
)

// 阶段 12e（D-09）：中心刷新后的 ACTIVE 组织列表替换领取轮询列表；被移出的组织不再轮询，空列表不生效；
// 本机并发上限（MaxConcurrent）不随组织数量变化，跨组织共用。
func TestSupervisorSetOrganizationsReplacesPollList(t *testing.T) {
	h := newHarness(t, newCenter(), &fakeAdapter{}, "")
	listed := func() []string {
		h.supervisor.mu.Lock()
		defer h.supervisor.mu.Unlock()
		names := make([]string, 0, len(h.supervisor.organizations))
		for name := range h.supervisor.organizations {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	limit := h.supervisor.MaxConcurrent()
	h.supervisor.SetOrganizations([]string{"org-b", " org-c ", ""})
	if got := listed(); len(got) != 2 || got[0] != "org-b" || got[1] != "org-c" {
		t.Fatalf("organizations after replace = %v", got)
	}
	h.supervisor.SetOrganizations(nil)
	if got := listed(); len(got) != 2 {
		t.Fatalf("empty list must not clear organizations: %v", got)
	}
	if h.supervisor.MaxConcurrent() != limit || cap(h.supervisor.slots) != limit {
		t.Fatalf("local limit changed with organizations: %d / %d, want %d", h.supervisor.MaxConcurrent(), cap(h.supervisor.slots), limit)
	}
}
