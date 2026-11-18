package stage9f

// 排序与改可见性：私有分组整体重排；共享分组里非管理员只能在自己占的位置之间换序；改可见性移到另一分组末尾。

import (
	"net/http"
	"strings"
	"testing"
)

type qcOrder struct {
	QuickCommands []qcCommand `json:"quick_commands"`
}

func joined(ids ...string) string { return strings.Join(ids, ",") }

func TestQuickCommandReorder(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member, peer, admin := e.member(), e.client(qcPeerID), e.client(qcAdminID)
	path := commandsPath(s.id)
	a := member.create(t, s.id, agentBody("A", "PRIVATE", s.shared))
	b := member.create(t, s.id, agentBody("B", "PRIVATE", s.shared))
	c := member.create(t, s.id, agentBody("C", "PRIVATE", s.shared))
	var out qcOrder
	member.must(http.StatusOK, http.MethodPut, path+"/order", map[string]any{"visibility": "PRIVATE", "ids": []string{c.ID, a.ID, b.ID}}, &out)
	if got := joined(commandIDs(member.list(t, s.id).Private)...); got != joined(c.ID, a.ID, b.ID) || len(out.QuickCommands) != 3 {
		t.Fatalf("private order = %s", got)
	}
	for _, bad := range [][]string{{a.ID, b.ID}, {a.ID, a.ID, b.ID}, {a.ID, b.ID, qcNewID(t)}} {
		member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPut, path+"/order", map[string]any{"visibility": "PRIVATE", "ids": bad})
	}
	member.expectError(http.StatusBadRequest, "", http.MethodPut, path+"/order", map[string]any{"visibility": "TEAM", "ids": []string{}})

	// 共享分组：S1、S3 属于 admin，S2、S4 属于 peer。
	s1 := admin.create(t, s.id, agentBody("S1", "SHARED", s.shared))
	s2 := peer.create(t, s.id, agentBody("S2", "SHARED", s.shared))
	s3 := admin.create(t, s.id, agentBody("S3", "SHARED", s.shared))
	s4 := peer.create(t, s.id, agentBody("S4", "SHARED", s.shared))
	peer.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPut, path+"/order",
		map[string]any{"visibility": "SHARED", "ids": []string{s4.ID, s3.ID, s2.ID, s1.ID}})
	peer.must(http.StatusOK, http.MethodPut, path+"/order", map[string]any{"visibility": "SHARED", "ids": []string{s4.ID, s2.ID}}, nil)
	if got := joined(commandIDs(member.list(t, s.id).Shared)...); got != joined(s1.ID, s4.ID, s3.ID, s2.ID) {
		t.Fatalf("shared order after peer = %s", got)
	}
	admin.must(http.StatusOK, http.MethodPut, path+"/order", map[string]any{"visibility": "SHARED", "ids": []string{s2.ID, s1.ID, s4.ID, s3.ID}}, nil)
	if got := joined(commandIDs(member.list(t, s.id).Shared)...); got != joined(s2.ID, s1.ID, s4.ID, s3.ID) {
		t.Fatalf("shared order after admin = %s", got)
	}
	testVisibilityMove(t, s, a)
}
