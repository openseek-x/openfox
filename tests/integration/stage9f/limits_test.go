package stage9f

// 上限（私有按人按空间、共享按空间各 50，并发下也不突破）与停用空间只读。

import (
	"net/http"
	"sync"
	"testing"
)

// fillCommands 直接写库把分组填到 n 条（只用于上限测试，避免 50 次 HTTP）。
func (e *qcEnv) fillCommands(spaceID, owner, visibility, agentID string, n int) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.task_quick_commands (organization_id, space_id, owner_user_id, visibility, name, agent_id, position)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4, 'fill ' || g, $5::uuid, g FROM generate_series(0, $6 - 1) g`,
		e.org, spaceID, owner, visibility, agentID, n)
}

func TestQuickCommandLimits(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member, peer, admin := e.member(), e.client(qcPeerID), e.client(qcAdminID)
	path := commandsPath(s.id)
	e.fillCommands(s.id, qcMemberID, "PRIVATE", s.shared, 45)
	var wg sync.WaitGroup
	statuses := make([]int, 10)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = member.do(http.MethodPost, path, agentBody("p", "PRIVATE", s.shared)).Status
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
	if created != 5 || limited != 5 || len(member.list(t, s.id).Private) != 50 {
		t.Fatalf("created %d limited %d total %d", created, limited, len(member.list(t, s.id).Private))
	}
	member.expectError(http.StatusConflict, "QUICK_COMMAND_LIMIT", http.MethodPost, path, agentBody("p", "PRIVATE", s.shared))
	// 私有上限按人：peer 不受影响。
	peer.create(t, s.id, agentBody("peer", "PRIVATE", s.shared))
	// 共享上限按空间：填满后任何人都不能再建；私有改共享也受限。
	e.fillCommands(s.id, qcAdminID, "SHARED", s.shared, 50)
	admin.expectError(http.StatusConflict, "QUICK_COMMAND_LIMIT", http.MethodPost, path, agentBody("s", "SHARED", s.shared))
	first := member.list(t, s.id).Private[0]
	member.expectError(http.StatusConflict, "QUICK_COMMAND_LIMIT", http.MethodPatch, path+"/"+first.ID,
		map[string]any{"version": first.Version, "visibility": "SHARED"})
	// 删除后又能新建；position 逼近上限时新建会把分组重排为 0..n-1。
	member.must(http.StatusNoContent, http.MethodDelete, path+"/"+first.ID, nil, nil)
	e.exec(`UPDATE openfox.task_quick_commands SET position = 999 WHERE organization_id = $1::uuid AND space_id = $2::uuid
		AND owner_user_id = $3::uuid AND visibility = 'PRIVATE' AND deleted_at IS NULL AND name = 'p'`, e.org, s.id, qcMemberID)
	again := member.create(t, s.id, agentBody("again", "PRIVATE", s.shared))
	if again.Position > 49 {
		t.Fatalf("position after renumber = %d", again.Position)
	}
}

func TestQuickCommandRetiredSpace(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member := e.member()
	path := commandsPath(s.id)
	item := member.create(t, s.id, agentBody("x", "PRIVATE", s.shared))
	e.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED' WHERE id = $1::uuid`, s.id)
	got := member.list(t, s.id)
	if len(got.Private) != 1 || got.Private[0].CanEdit {
		t.Fatalf("retired list = %+v", got.Private)
	}
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, path, agentBody("y", "PRIVATE", s.shared))
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPatch, path+"/"+item.ID, map[string]any{"version": 1, "name": "z"})
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodDelete, path+"/"+item.ID, nil)
	member.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPut, path+"/order", map[string]any{"visibility": "PRIVATE", "ids": []string{item.ID}})
}
