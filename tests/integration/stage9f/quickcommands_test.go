package stage9f

// 快捷指令集成测试：CRUD、他人私有 404、共享仅创建人与空间管理员可改、上限（含并发）、乐观锁、
// 目标校验与失效、图标、排序、只读 / 停用空间、跨组织隔离与审计。

import (
	"net/http"
	"strings"
	"testing"
)

type qcSpace struct {
	env    *qcEnv
	id     string
	other  string // 另一个空间（目标校验用）
	shared string // 共享 Agent
	mine   string // member 的私有 Agent
	peers  string // peer 的私有 Agent
	crew   string // 共享小队（成员：shared）
}

// newQcSpace 建一个私有空间：member、peer 为 MEMBER，spaceAdmin 为 EDITOR（空间管理员），guest 为 VIEWER（只读）。
func newQcSpace(t *testing.T) qcSpace {
	env := newQcEnv(t)
	space := env.createSpace(nil)
	other := env.createSpace(nil)
	for _, id := range []string{qcMemberID, qcPeerID} {
		env.addSpaceMember(space.ID, id, "MEMBER")
		env.addSpaceMember(other.ID, id, "MEMBER")
	}
	env.addSpaceMember(space.ID, qcAdminID, "EDITOR")
	env.addSpaceMember(space.ID, qcGuestID, "VIEWER")
	s := qcSpace{env: env, id: space.ID, other: other.ID}
	s.shared = env.seedAgent(space.ID, qcOwnerID, "SHARED")
	s.mine = env.seedAgent(space.ID, qcMemberID, "PRIVATE")
	s.peers = env.seedAgent(space.ID, qcPeerID, "PRIVATE")
	s.crew = env.seedCrew(space.ID, qcOwnerID, "SHARED", s.shared)
	return s
}

func agentBody(name, visibility, agentID string) map[string]any {
	return map[string]any{"name": name, "visibility": visibility, "agent_id": agentID}
}

func TestQuickCommandCRUDAndPermissions(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member, peer, admin, owner := e.member(), e.client(qcPeerID), e.client(qcAdminID), e.owner()
	path := commandsPath(s.id)

	private := member.create(t, s.id, map[string]any{"name": "  需求澄清  ", "visibility": "PRIVATE", "agent_id": s.shared,
		"prompt": "  请澄清本任务  ", "icon_kind": "emoji", "icon": "🦊"})
	shared := member.create(t, s.id, map[string]any{"name": "小队评审", "visibility": "SHARED", "crew_id": s.crew})
	if private.Name != "需求澄清" || private.Prompt != "请澄清本任务" || private.Position != 0 || private.Version != 1 || !private.CanEdit ||
		private.OwnerUserID != qcMemberID || private.Target.Kind != "AGENT" || !private.Target.Available || private.Target.Name == "" ||
		private.IconKind == nil || *private.IconKind != "emoji" || private.IconURL != nil {
		t.Fatalf("private = %+v", private)
	}
	if shared.CrewID == nil || *shared.CrewID != s.crew || shared.AgentID != nil || shared.Target.Kind != "CREW" || !shared.Target.Available || shared.Prompt != "" {
		t.Fatalf("shared = %+v", shared)
	}

	t.Run("private commands are owner only", func(t *testing.T) {
		if got := member.list(t, s.id); len(got.Private) != 1 || len(got.Shared) != 1 {
			t.Fatalf("member list = %+v", got)
		}
		got := peer.list(t, s.id)
		if len(got.Private) != 0 || len(got.Shared) != 1 || got.Shared[0].CanEdit {
			t.Fatalf("peer list = %+v", got)
		}
		peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, path+"/"+private.ID, map[string]any{"version": 1, "name": "x"})
		peer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, path+"/"+private.ID, nil)
		owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, path+"/"+private.ID, nil)
	})

	t.Run("cross organization is isolated", func(t *testing.T) {
		outsider := e.outsider()
		if got := outsider.do(http.MethodGet, path, nil); got.Status == http.StatusOK || strings.Contains(string(got.Body), shared.ID) {
			t.Fatalf("outsider list = %d %s", got.Status, got.Body)
		}
		other := outsider.do(http.MethodGet, "/v1/organizations/"+e.otherOrg+"/"+path, nil)
		if other.Status != http.StatusNotFound || strings.Contains(string(other.Body), shared.ID) {
			t.Fatalf("other org list = %d %s", other.Status, other.Body)
		}
		if got := outsider.do(http.MethodDelete, "/v1/organizations/"+e.otherOrg+"/"+path+"/"+shared.ID, nil); got.Status != http.StatusNotFound {
			t.Fatalf("cross-org delete = %d", got.Status)
		}
	})

	t.Run("shared commands are edited by creator and space admins only", func(t *testing.T) {
		peer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 1, "name": "x"})
		peer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodDelete, path+"/"+shared.ID, nil)
		if got := admin.list(t, s.id); len(got.Shared) != 1 || !got.Shared[0].CanEdit {
			t.Fatalf("space admin list = %+v", got.Shared)
		}
		var updated qcCommand
		admin.must(http.StatusOK, http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 1, "prompt": "请评审"}, &updated)
		if updated.Version != 2 || updated.Prompt != "请评审" || updated.Name != "小队评审" || updated.OwnerUserID != qcMemberID {
			t.Fatalf("admin update = %+v", updated)
		}
		member.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 1, "name": "stale"})
		member.expectError(http.StatusBadRequest, "", http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 2})
		member.expectError(http.StatusBadRequest, "", http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 2, "name": "  "})
		// 组织所有者也是空间管理员。
		owner.must(http.StatusOK, http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 2, "name": "评审"}, &updated)
		if updated.Version != 3 || updated.Name != "评审" {
			t.Fatalf("owner update = %+v", updated)
		}
		// 只读成员能看到共享指令，但不能新建或修改。
		guest := e.guest()
		if got := guest.list(t, s.id); len(got.Shared) != 1 || got.Shared[0].CanEdit {
			t.Fatalf("viewer list = %+v", got.Shared)
		}
		guest.expectError(http.StatusForbidden, "", http.MethodPost, path, agentBody("x", "PRIVATE", s.shared))
	})

	t.Run("validation and idempotent create", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"name": "", "visibility": "PRIVATE", "agent_id": s.shared},
			{"name": strings.Repeat("长", 101), "visibility": "PRIVATE", "agent_id": s.shared},
			{"name": "x", "visibility": "PRIVATE", "agent_id": s.shared, "prompt": strings.Repeat("a", 4001)},
			{"name": "x", "visibility": "TEAM", "agent_id": s.shared},
			{"name": "x", "visibility": "PRIVATE"},
			{"name": "x", "visibility": "PRIVATE", "icon_kind": "gif", "icon": "a", "agent_id": s.shared},
			{"name": "x", "visibility": "PRIVATE", "icon_kind": "upload", "icon": qcNewID(t), "agent_id": s.shared},
			{"name": "x", "visibility": "PRIVATE", "icon_kind": "emoji", "agent_id": s.shared},
		} {
			member.expectError(http.StatusBadRequest, "", http.MethodPost, path, body)
		}
		member.expectError(http.StatusBadRequest, "INVALID_QUICK_COMMAND_TARGET", http.MethodPost, path,
			map[string]any{"name": "x", "visibility": "PRIVATE", "agent_id": s.shared, "crew_id": s.crew})
		id := qcNewID(t)
		body := map[string]any{"id": id, "name": "重试", "visibility": "PRIVATE", "agent_id": s.shared, "prompt": "同一次"}
		first := member.create(t, s.id, body)
		again := member.create(t, s.id, body)
		if first.ID != id || again.ID != id || len(member.list(t, s.id).Private) != 2 {
			t.Fatalf("idempotent create = %+v / %+v", first, again)
		}
		body["prompt"] = "不同"
		member.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, body)
		// 别人用同一个 id 也不行（不泄露内容）。
		peer.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, body)
		member.must(http.StatusNoContent, http.MethodDelete, path+"/"+id, nil, nil)
		member.expectError(http.StatusNotFound, "", http.MethodDelete, path+"/"+id, nil)
	})

	t.Run("delete and audit", func(t *testing.T) {
		admin.must(http.StatusNoContent, http.MethodDelete, path+"/"+shared.ID, nil, nil)
		if got := member.list(t, s.id); len(got.Shared) != 0 {
			t.Fatalf("shared after delete = %+v", got.Shared)
		}
		member.expectError(http.StatusNotFound, "", http.MethodPatch, path+"/"+shared.ID, map[string]any{"version": 4, "name": "x"})
		// 共享指令：created + updated×2 + deleted；私有指令不写审计，且审计不含名称和提示词。
		if n := e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action LIKE 'task_quick_command.%'`, e.org); n != 4 {
			t.Fatalf("audit events = %d, want 4", n)
		}
		if n := e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action LIKE 'task_quick_command.%'
			AND (safe_metadata::text LIKE '%评审%' OR safe_metadata::text LIKE '%请澄清%')`, e.org); n != 0 {
			t.Fatalf("audit leaks content: %d", n)
		}
	})
}

func TestQuickCommandTargets(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member, peer := e.member(), e.client(qcPeerID)
	path := commandsPath(s.id)
	archived := e.seedAgent(s.id, qcOwnerID, "SHARED")
	e.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE id = $1::uuid`, archived)
	elsewhere := e.seedAgent(s.other, qcOwnerID, "SHARED")
	builtin := e.seedAgent(s.id, qcOwnerID, "SHARED")
	e.exec(`UPDATE openfox.agents SET builtin_kind = 'TASK_ASSISTANT' WHERE id = $1::uuid`, builtin)
	peerCrew := e.seedCrew(s.id, qcPeerID, "PRIVATE", s.peers)
	archivedCrew := e.seedCrew(s.id, qcOwnerID, "SHARED", s.shared)
	e.exec(`UPDATE openfox.agent_crews SET archived_at = clock_timestamp() WHERE id = $1::uuid`, archivedCrew)
	otherCrew := e.seedCrew(s.other, qcOwnerID, "SHARED", elsewhere)

	t.Run("create rejects unusable targets without revealing them", func(t *testing.T) {
		for _, body := range []map[string]any{
			agentBody("x", "PRIVATE", s.peers), agentBody("x", "PRIVATE", elsewhere), agentBody("x", "PRIVATE", archived),
			agentBody("x", "PRIVATE", builtin), agentBody("x", "PRIVATE", qcNewID(t)),
			{"name": "x", "visibility": "PRIVATE", "crew_id": peerCrew}, {"name": "x", "visibility": "PRIVATE", "crew_id": archivedCrew},
			{"name": "x", "visibility": "PRIVATE", "crew_id": otherCrew}, {"name": "x", "visibility": "PRIVATE", "crew_id": qcNewID(t)},
		} {
			member.expectError(http.StatusBadRequest, "INVALID_QUICK_COMMAND_TARGET", http.MethodPost, path, body)
		}
		// 自己的私有 Agent 可以。
		own := member.create(t, s.id, agentBody("mine", "PRIVATE", s.mine))
		if !own.Target.Available {
			t.Fatalf("own private target = %+v", own.Target)
		}
		member.expectError(http.StatusBadRequest, "INVALID_QUICK_COMMAND_TARGET", http.MethodPatch, path+"/"+own.ID,
			map[string]any{"version": 1, "agent_id": s.peers})
		member.expectError(http.StatusBadRequest, "INVALID_QUICK_COMMAND_TARGET", http.MethodPatch, path+"/"+own.ID,
			map[string]any{"version": 1, "agent_id": nil, "crew_id": nil})
		var switched qcCommand
		member.must(http.StatusOK, http.MethodPatch, path+"/"+own.ID, map[string]any{"version": 1, "agent_id": nil, "crew_id": s.crew}, &switched)
		if switched.AgentID != nil || switched.CrewID == nil || switched.Target.Kind != "CREW" {
			t.Fatalf("switched = %+v", switched)
		}
	})

	t.Run("invalidated targets are reported, not errors", func(t *testing.T) {
		agentA := e.seedAgent(s.id, qcOwnerID, "SHARED")
		agentB := e.seedAgent(s.id, qcOwnerID, "SHARED")
		agentC := e.seedAgent(s.id, qcPeerID, "SHARED")
		crewMember := e.seedAgent(s.id, qcOwnerID, "SHARED")
		crew := e.seedCrew(s.id, qcOwnerID, "SHARED", crewMember)
		byArchive := member.create(t, s.id, agentBody("a", "SHARED", agentA))
		byMove := member.create(t, s.id, agentBody("b", "SHARED", agentB))
		byPrivate := member.create(t, s.id, agentBody("c", "SHARED", agentC))
		byCrew := member.create(t, s.id, map[string]any{"name": "d", "visibility": "SHARED", "crew_id": crew})
		e.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE id = $1::uuid`, agentA)
		e.exec(`UPDATE openfox.agents SET space_id = $2::uuid WHERE id = $1::uuid`, agentB, s.other)
		e.exec(`UPDATE openfox.agents SET visibility = 'PRIVATE' WHERE id = $1::uuid`, agentC)
		e.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE id = $1::uuid`, crewMember)
		want := map[string]string{byArchive.ID: "ARCHIVED", byMove.ID: "OUTSIDE_SPACE", byPrivate.ID: "NOT_VISIBLE", byCrew.ID: "NO_MEMBERS"}
		for _, item := range member.list(t, s.id).Shared {
			reason, ok := want[item.ID]
			if !ok {
				continue
			}
			if item.Target.Available || item.Target.UnavailableReason != reason {
				t.Fatalf("%s target = %+v, want %s", item.Name, item.Target, reason)
			}
			if reason == "NOT_VISIBLE" && (item.Target.Name != "" || (len(item.Target.Avatar) > 0 && string(item.Target.Avatar) != "null")) {
				t.Fatalf("not visible target leaks %+v", item.Target)
			}
			delete(want, item.ID)
		}
		if len(want) != 0 {
			t.Fatalf("missing commands %v", want)
		}
		// 目标的所有者仍能看到并使用。
		for _, item := range peer.list(t, s.id).Shared {
			if item.ID == byPrivate.ID && (!item.Target.Available || item.Target.Name == "") {
				t.Fatalf("owner view = %+v", item.Target)
			}
		}
		// 创建人改名不受目标失效影响（目标没变就不重新校验）。
		member.must(http.StatusOK, http.MethodPatch, commandsPath(s.id)+"/"+byArchive.ID, map[string]any{"version": 1, "name": "renamed"}, nil)
	})
}

func TestQuickCommandIcons(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member := e.member()
	path := commandsPath(s.id)
	response := member.uploadIcon("icon.png", testPNG)
	if response.Status != http.StatusCreated {
		t.Fatalf("upload icon = %d %s", response.Status, response.Body)
	}
	var icon struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	response.decode(t, &icon)
	item := member.create(t, s.id, map[string]any{"name": "图", "visibility": "PRIVATE", "agent_id": s.shared, "icon_kind": "upload", "icon": icon.ID})
	if item.IconKind == nil || *item.IconKind != "upload" || item.Icon == nil || *item.Icon != icon.ID || item.IconURL == nil || *item.IconURL != icon.URL {
		t.Fatalf("upload icon item = %+v", item)
	}
	// 另一个组织的图标 ID 不能用。
	var foreign string
	e.scalar(&foreign, `INSERT INTO openfox.uploaded_icons (organization_id, storage_key, content_type, size_bytes, sha256, created_by)
		VALUES ($1::uuid, $2, 'image/png', 1, repeat('a', 64), $3::uuid) RETURNING id::text`, e.otherOrg, "icons/x/"+qcSuffix(t), qcOutsiderID)
	member.expectError(http.StatusBadRequest, "", http.MethodPatch, path+"/"+item.ID, map[string]any{"version": 1, "icon_kind": "upload", "icon": foreign})
	var cleared qcCommand
	member.must(http.StatusOK, http.MethodPatch, path+"/"+item.ID, map[string]any{"version": 1, "icon_kind": nil}, &cleared)
	if cleared.IconKind != nil || cleared.Icon != nil || cleared.IconURL != nil || cleared.Version != 2 {
		t.Fatalf("cleared = %+v", cleared)
	}
}
