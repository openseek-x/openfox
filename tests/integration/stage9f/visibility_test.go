package stage9f

import (
	"net/http"
	"testing"
)

// testVisibilityMove：私有改共享移到共享分组末尾并写审计；空间管理员把它改回私有后，只有创建人能看到。
func testVisibilityMove(t *testing.T, s qcSpace, a qcCommand) {
	t.Helper()
	e := s.env
	member, admin := e.member(), e.client(qcAdminID)
	path := commandsPath(s.id)
	var moved qcCommand
	member.must(http.StatusOK, http.MethodPatch, path+"/"+a.ID, map[string]any{"version": 1, "visibility": "SHARED"}, &moved)
	shared := member.list(t, s.id).Shared
	if moved.Visibility != "SHARED" || shared[len(shared)-1].ID != a.ID || len(member.list(t, s.id).Private) != 2 {
		t.Fatalf("visibility change = %+v / %v", moved, commandIDs(shared))
	}
	if n := e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'task_quick_command.updated'
		AND resource_id = $2`, e.org, a.ID); n != 1 {
		t.Fatalf("visibility change audit = %d", n)
	}
	var hidden qcCommand
	admin.must(http.StatusOK, http.MethodPatch, path+"/"+a.ID, map[string]any{"version": 2, "visibility": "PRIVATE"}, &hidden)
	if hidden.Visibility != "PRIVATE" || hidden.CanEdit || hidden.Version != 3 {
		t.Fatalf("admin made private = %+v", hidden)
	}
	admin.expectError(http.StatusNotFound, "", http.MethodDelete, path+"/"+a.ID, nil)
	if got := member.list(t, s.id).Private; len(got) != 3 || got[2].ID != a.ID {
		t.Fatalf("creator private after admin change = %v", commandIDs(got))
	}
}
