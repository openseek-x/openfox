package stage1

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// 跨租户：其他组织的成员无论走对方组织路径还是自己的组织路径，都只能得到 404。
func TestTasksCrossTenantNotFound(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	task := env.newTask(env.owner, space, nil)

	// outsider 不是本组织成员：组织选择即失败。
	for _, path := range []string{tasksPath(space), taskPath(space, task.ID), tasksPath(space) + "/counts",
		taskPath(space, task.ID) + "/timeline", "tasks/by-display-id/" + task.DisplayID, "my-tasks"} {
		env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path, nil)
	}
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, taskPath(space, task.ID),
		map[string]any{"title": "stolen", "version": task.Version})
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, taskPath(space, task.ID)+"?version=1", nil)

	// outsider 在自己的组织路径下引用本组织的空间与任务 ID：RLS 下不存在。
	other := func(path string) string { return "/v1/organizations/" + env.otherOrg + "/" + path }
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, other(taskPath(space, task.ID)), nil)
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, other("tasks/by-display-id/"+task.DisplayID), nil)
	env.outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, other(tasksPath(space)),
		map[string]any{"title": "x", "type_id": space.TypeID})

	// 关联目标是其他组织的任务：404，不泄露存在性。
	var foreignSpace struct {
		ID string `json:"id"`
	}
	response := env.outsider.doIn(env.otherOrg, http.MethodPost, "spaces", map[string]any{"name": "Foreign " + cfgSuffix(t), "visibility": "shared"})
	if response.Status != http.StatusCreated {
		t.Fatalf("foreign space = %d %s", response.Status, response.Body)
	}
	response.decode(t, &foreignSpace)
	var foreignTask tkTask
	response = env.outsider.doIn(env.otherOrg, http.MethodPost, "spaces/"+foreignSpace.ID+"/tasks", map[string]any{"title": "foreign"})
	if response.Status != http.StatusCreated {
		t.Fatalf("foreign task = %d %s", response.Status, response.Body)
	}
	response.decode(t, &foreignTask)
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, taskPath(space, task.ID)+"/relations",
		map[string]any{"type_id": "builtin:RELATES", "target_task_id": foreignTask.ID})
	// 父任务与排序邻居同样不能指向其他组织。
	env.owner.expectError(http.StatusBadRequest, "INVALID_PARENT", http.MethodPatch, taskPath(space, task.ID),
		map[string]any{"parent_task_id": foreignTask.ID, "version": task.Version})
	env.owner.expectError(http.StatusBadRequest, "INVALID_POSITION", http.MethodPatch, taskPath(space, task.ID),
		map[string]any{"position": map[string]any{"after_task_id": foreignTask.ID}, "version": task.Version})
}

// 私有空间对象级越权：不可读空间的人员、他人私有 Agent、不可读的关联目标、跨空间标签。
func TestTasksPrivateSpaceObjectAuthorization(t *testing.T) {
	env := newTkEnv(t)
	private := env.createSpace("private")
	shared := env.createSpace("shared")
	otherShared := env.createSpace("shared")
	privateTask := env.newTask(env.owner, private, nil)
	sharedTask := env.newTask(env.owner, shared, nil)

	// member 不是私有空间成员：读写私有空间任务都是 404。
	env.member.expectError(http.StatusNotFound, "", http.MethodGet, taskPath(private, privateTask.ID), nil)
	env.member.expectError(http.StatusNotFound, "", http.MethodGet, tasksPath(private), nil)
	env.member.expectError(http.StatusNotFound, "", http.MethodGet, "tasks/by-display-id/"+strings.ToUpper(privateTask.DisplayID), nil)
	env.member.expectError(http.StatusNotFound, "", http.MethodGet, taskPath(private, privateTask.ID)+"/timeline", nil)
	env.member.expectError(http.StatusNotFound, "", http.MethodPost, taskPath(private, privateTask.ID)+"/links",
		map[string]any{"url": "https://example.test/a"})
	// 按编号查找大小写不敏感，对能读的人可用。
	env.owner.must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+strings.ToUpper(privateTask.DisplayID), nil, nil)

	// 1. 人员处理人必须能读该空间。
	response := env.patchTask(env.owner, private, privateTask.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}})
	if response.Status != http.StatusBadRequest || response.code() != "ASSIGNEE_NOT_ALLOWED" {
		t.Fatalf("assign unreadable member = %d %s", response.Status, response.Body)
	}
	// 旧字段 assignee_user_id 走同一校验（修复前只检查组织成员）。
	response = env.patchTask(env.owner, private, privateTask.ID, map[string]any{"assignee_user_id": cfgMemberID})
	if response.Status != http.StatusBadRequest {
		t.Fatalf("legacy assign unreadable member = %d %s", response.Status, response.Body)
	}
	env.owner.expectError(http.StatusBadRequest, "ASSIGNEE_NOT_ALLOWED", http.MethodPost, tasksPath(private),
		map[string]any{"title": "x", "assignee_user_id": cfgMemberID})
	// 共享空间里 member 可读，分配成功；被撤销的空间成员不可读。
	env.must200(env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}}))
	revoked := env.addMember(tkUserA, "MEMBER")
	_ = revoked
	env.addSpaceMember(shared.ID, tkUserA, "MEMBER", "REVOKED")
	response = env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": tkUserA}}})
	if response.Status != http.StatusBadRequest || response.code() != "ASSIGNEE_NOT_ALLOWED" {
		t.Fatalf("assign revoked space member = %d %s", response.Status, response.Body)
	}
	// 访客不是共享空间成员时不可读。
	env.addMember(tkGuest, "GUEST")
	response = env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": tkGuest}}})
	if response.Status != http.StatusBadRequest {
		t.Fatalf("assign guest = %d %s", response.Status, response.Body)
	}

	// 2. Agent：他人私有 Agent、其他空间的 Agent、归档 Agent 都不可指派；自己的私有 Agent 与共享 Agent 可以。
	memberPrivateAgent := env.insertAgent(shared.ID, cfgMemberID, "PRIVATE")
	sharedAgent := env.insertAgent(shared.ID, cfgMemberID, "SHARED")
	otherSpaceAgent := env.insertAgent(otherShared.ID, cfgOwnerID, "SHARED")
	archivedAgent := env.insertAgent(shared.ID, cfgOwnerID, "SHARED")
	env.exec(`UPDATE openfox.agents SET archived_at = now() WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, archivedAgent)
	for name, agentID := range map[string]string{"others private": memberPrivateAgent, "other space": otherSpaceAgent, "archived": archivedAgent} {
		response = env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{
			"assignees": []map[string]any{{"kind": "AGENT", "id": agentID}}})
		if response.Status != http.StatusBadRequest || response.code() != "ASSIGNEE_NOT_ALLOWED" {
			t.Fatalf("assign %s agent = %d %s", name, response.Status, response.Body)
		}
		// 旧字段 executor_agent_id 同样校验。
		response = env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{"executor_agent_id": agentID})
		if response.Status != http.StatusBadRequest {
			t.Fatalf("legacy executor %s agent = %d %s", name, response.Status, response.Body)
		}
	}
	env.must200(env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "AGENT", "id": sharedAgent}}}))
	env.must200(env.patchTask(env.member, shared, sharedTask.ID, map[string]any{
		"assignees": []map[string]any{{"kind": "AGENT", "id": memberPrivateAgent}, {"kind": "HUMAN", "id": cfgOwnerID}}}))
	updated := env.getTask(env.owner, shared, sharedTask.ID)
	if len(updated.Assignees) != 2 || updated.Assignees[0].Kind != "HUMAN" || updated.Assignees[1].ID != memberPrivateAgent {
		t.Fatalf("assignees = %+v", updated.Assignees)
	}
	var projection struct {
		Assignee string `json:"assignee_user_id"`
		Executor string `json:"executor_agent_id"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, taskPath(shared, sharedTask.ID), nil, &projection)
	if projection.Assignee != cfgOwnerID || projection.Executor != memberPrivateAgent {
		t.Fatalf("legacy projection = %+v", projection)
	}

	// 3. 关联：目标不可读 → 404；对方看不到不可读的关联对端。
	env.must201(env.owner.do(http.MethodPost, taskPath(shared, sharedTask.ID)+"/relations",
		map[string]any{"type_id": "builtin:BLOCKS", "target_task_id": privateTask.ID}))
	var relations struct {
		Relations []struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
			Direction string `json:"direction"`
		} `json:"relations"`
	}
	env.owner.must(http.StatusOK, http.MethodGet, taskPath(shared, sharedTask.ID)+"/relations", nil, &relations)
	if len(relations.Relations) != 1 || relations.Relations[0].Task.ID != privateTask.ID || relations.Relations[0].Direction != "OUTWARD" {
		t.Fatalf("owner relations = %+v", relations)
	}
	env.member.must(http.StatusOK, http.MethodGet, taskPath(shared, sharedTask.ID)+"/relations", nil, &relations)
	if len(relations.Relations) != 0 {
		t.Fatalf("member sees unreadable relation target: %+v", relations)
	}
	env.member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, taskPath(shared, sharedTask.ID)+"/relations",
		map[string]any{"type_id": "builtin:RELATES", "target_task_id": privateTask.ID})
	// 私有空间任务视角看到的是 INWARD。
	env.owner.must(http.StatusOK, http.MethodGet, taskPath(private, privateTask.ID)+"/relations", nil, &relations)
	if len(relations.Relations) != 1 || relations.Relations[0].Direction != "INWARD" {
		t.Fatalf("inward relations = %+v", relations)
	}

	// 4. 跨空间标签：私有空间的空间标签不能加到共享空间任务上。
	privateLabel := env.createLabel(map[string]any{"space_id": private.ID})
	response = env.patchTask(env.owner, shared, sharedTask.ID, map[string]any{"label_ids": []string{privateLabel.ID}})
	if response.Status != http.StatusBadRequest || response.code() != "INVALID_LABEL" {
		t.Fatalf("cross-space label = %d %s", response.Status, response.Body)
	}
	env.owner.expectError(http.StatusBadRequest, "INVALID_LABEL", http.MethodPost, tasksPath(shared),
		map[string]any{"title": "x", "label_ids": []string{privateLabel.ID}})

	// 5. 关注者：不可读空间的成员、他人私有 Agent 不能被加为关注者；关注自己只需读权限。
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPut,
		taskPath(private, privateTask.ID)+"/followers/human/"+cfgMemberID, nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodPut,
		taskPath(shared, sharedTask.ID)+"/followers/agent/"+memberPrivateAgent, nil)
	env.member.must(http.StatusNoContent, http.MethodPut, taskPath(shared, sharedTask.ID)+"/followers/human/"+cfgMemberID, nil, nil)

	// 6. 只读空间成员（VIEWER）可以读但不能改。
	viewer := env.addMember(tkUserB, "MEMBER")
	env.addSpaceMember(private.ID, tkUserB, "VIEWER", "ACTIVE")
	viewer.must(http.StatusOK, http.MethodGet, taskPath(private, privateTask.ID), nil, nil)
	viewer.expectError(http.StatusForbidden, "", http.MethodPatch, taskPath(private, privateTask.ID),
		map[string]any{"title": "viewer edit", "version": env.getTask(env.owner, private, privateTask.ID).Version})
	viewer.expectError(http.StatusForbidden, "", http.MethodDelete, taskPath(private, privateTask.ID)+"?version=1", nil)
}

// 软删除：任务与各子资源接口返回 404，列表、计数、我的任务、收件箱都不再出现。
func TestTasksSoftDelete(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	parent := env.newTask(env.owner, space, map[string]any{
		"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}})
	child := env.newTask(env.owner, space, map[string]any{"parent_task_id": parent.ID})
	var link struct {
		ID string `json:"id"`
	}
	env.owner.must(http.StatusCreated, http.MethodPost, taskPath(space, parent.ID)+"/links",
		map[string]any{"url": "https://example.test/doc"}, &link)
	env.owner.must(http.StatusCreated, http.MethodPost, taskPath(space, parent.ID)+"/comments",
		map[string]any{"body": "before delete", "client_comment_id": "c-" + cfgSuffix(t)}, nil)

	// 删除前：member 收件箱里有这条分配通知。
	inboxGroups := func() (ids []string, total int) {
		var page struct {
			Groups []struct {
				ID string `json:"id"`
			} `json:"groups"`
			Counts struct {
				Inbox int `json:"inbox"`
			} `json:"counts"`
		}
		env.member.must(http.StatusOK, http.MethodGet, "inbox", nil, &page)
		for _, group := range page.Groups {
			ids = append(ids, group.ID)
		}
		return ids, page.Counts.Inbox
	}
	if ids, total := inboxGroups(); len(ids) != 1 || ids[0] != parent.ID || total != 1 {
		t.Fatalf("inbox before delete = %v %d", ids, total)
	}

	current := env.getTask(env.owner, space, parent.ID)
	env.owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete,
		taskPath(space, parent.ID)+"?version="+itoa(current.Version+5), nil)
	env.owner.expectError(http.StatusBadRequest, "", http.MethodDelete, taskPath(space, parent.ID), nil)
	env.owner.must(http.StatusNoContent, http.MethodDelete, taskPath(space, parent.ID)+"?version="+itoa(current.Version), nil, nil)

	base := taskPath(space, parent.ID)
	for _, path := range []string{base, base + "/activities", base + "/timeline", base + "/followers", base + "/relations",
		base + "/links", base + "/fields", base + "/comments", "tasks/by-display-id/" + parent.DisplayID} {
		env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path, nil)
	}
	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPatch, base, map[string]any{"title": "after", "version": current.Version + 1}},
		{http.MethodDelete, base + "?version=" + itoa(current.Version+1), nil},
		{http.MethodPost, base + "/links", map[string]any{"url": "https://example.test/x"}},
		{http.MethodPatch, base + "/links/" + link.ID, map[string]any{"title": "x", "version": 1}},
		{http.MethodDelete, base + "/links/" + link.ID, nil},
		{http.MethodPost, base + "/relations", map[string]any{"type_id": "builtin:RELATES", "target_task_id": child.ID}},
		{http.MethodPut, base + "/followers/human/" + cfgOwnerID, nil},
		{http.MethodPut, base + "/reactions", map[string]any{"emoji": "👍"}},
		{http.MethodPost, base + "/comments", map[string]any{"body": "after delete", "client_comment_id": "c-" + cfgSuffix(t)}},
	}
	for _, write := range writes {
		env.owner.expectError(http.StatusNotFound, "NOT_FOUND", write.method, write.path, write.body)
	}
	// 以已删除任务为父任务、排序邻居或关联目标：同样不可用。
	env.owner.expectError(http.StatusBadRequest, "INVALID_PARENT", http.MethodPost, tasksPath(space),
		map[string]any{"title": "x", "parent_task_id": parent.ID})
	env.owner.expectError(http.StatusBadRequest, "INVALID_POSITION", http.MethodPost, tasksPath(space),
		map[string]any{"title": "x", "after_task_id": parent.ID})
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, taskPath(space, child.ID)+"/relations",
		map[string]any{"type_id": "builtin:RELATES", "target_task_id": parent.ID})

	// 列表、计数、我的任务不再包含；子任务脱离父任务。
	if ids := env.listIDs(env.owner, tasksPath(space), ""); len(ids) != 1 || ids[0] != child.ID {
		t.Fatalf("list after delete = %v", ids)
	}
	if counts := env.counts(env.owner, tasksPath(space)+"/counts", ""); counts.Total != 1 {
		t.Fatalf("counts after delete = %+v", counts)
	}
	if ids := env.listIDs(env.member, "my-tasks", "scope=assigned"); len(ids) != 0 {
		t.Fatalf("my-tasks after delete = %v", ids)
	}
	if after := env.getTask(env.owner, space, child.ID); after.ParentTaskID != nil {
		t.Fatalf("child still points at deleted parent: %+v", after)
	}
	// 收件箱不再显示该任务的通知（通知行保留在库里）。
	if ids, total := inboxGroups(); len(ids) != 0 || total != 0 {
		t.Fatalf("inbox after delete = %v %d", ids, total)
	}
	var kept int
	env.scalar(&kept, `SELECT count(*) FROM openfox.notifications WHERE organization_id = $1::uuid AND task_id = $2::uuid`, env.org, parent.ID)
	if kept == 0 {
		t.Fatal("notification rows should be kept")
	}
	var deletedBy, lastKind string
	env.scalar(&deletedBy, `SELECT deleted_by::text FROM openfox.tasks WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, parent.ID)
	env.scalar(&lastKind, `SELECT kind FROM openfox.task_activities WHERE organization_id = $1::uuid AND task_id = $2::uuid
		ORDER BY created_at DESC, id DESC LIMIT 1`, env.org, parent.ID)
	if deletedBy != cfgOwnerID || lastKind != "task.deleted" {
		t.Fatalf("tombstone = %q %q", deletedBy, lastKind)
	}

	// 对话不能绑定已删除任务。
	agentID := env.insertAgent(space.ID, cfgOwnerID, "SHARED")
	env.owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "conversations",
		map[string]any{"space_id": space.ID, "agent_id": agentID, "task_id": parent.ID})
	env.owner.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": space.ID, "agent_id": agentID, "task_id": child.ID}, nil)
}

func (e *tkEnv) must200(response cfgResponse) {
	e.t.Helper()
	if response.Status != http.StatusOK {
		e.t.Fatalf("status = %d %s, want 200", response.Status, response.Body)
	}
}

func (e *tkEnv) must201(response cfgResponse) {
	e.t.Helper()
	if response.Status != http.StatusCreated {
		e.t.Fatalf("status = %d %s, want 201", response.Status, response.Body)
	}
}

func itoa(value int) string { return strconv.Itoa(value) }
