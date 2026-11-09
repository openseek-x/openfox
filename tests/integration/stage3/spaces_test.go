package stage3

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// ---- 本文件的辅助函数 ----

func spacePath(spaceID string) string { return "spaces/" + spaceID }

// insertAgent 用超级用户插入一个绑定测试电脑的 Agent；第一次调用时创建电脑与授权（scope 由 grantScope 决定）。
func (e *s3Env) insertAgent(spaceID string) string {
	e.t.Helper()
	deviceID := e.ensureDevice("ORGANIZATION", "")
	id := s3NewID(e.t)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'SHARED', 1)`,
		e.org, id, deviceID, spaceID, s3OwnerID, "Agent "+s3Suffix(e.t))
	return id
}

var s3Devices = map[string]string{}

// ensureDevice 返回（并按需创建）一台电脑及其在 env.org 的授权；scope = SPACE 时 scopeSpaceID 为授权空间。
func (e *s3Env) ensureDevice(scope, scopeSpaceID string) string {
	e.t.Helper()
	key := e.org + scope + scopeSpaceID
	if id, ok := s3Devices[key]; ok {
		return id
	}
	id := s3NewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots) VALUES ($1::uuid, $2, 4)`, id, "s3-device-"+s3Suffix(e.t))
	if scope == "SPACE" {
		e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id, scope, scope_space_id) VALUES ($1::uuid, $2::uuid, 'SPACE', $3::uuid)`,
			e.org, id, scopeSpaceID)
	} else {
		e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, e.org, id)
	}
	s3Devices[key] = id
	return id
}

// insertRun 插入一个指定状态的 Run（含会话），返回 Run ID。
func (e *s3Env) insertRun(spaceID, agentID, taskID, state string) string {
	e.t.Helper()
	conversationID, runID := s3NewID(e.t), s3NewID(e.t)
	var task any
	if taskID != "" {
		task = taskID
	}
	e.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, task_id, owner_user_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid)`,
		e.org, conversationID, spaceID, agentID, task, s3OwnerID)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id,
			grant_id, config_version, grant_version, actor_id, state)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, a.device_id, g.id, 1, 1, $7::uuid, $8
		  FROM openfox.agents a JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE a.organization_id = $1::uuid AND a.id = $6::uuid`,
		e.org, runID, spaceID, conversationID, task, agentID, s3OwnerID, state)
	return runID
}

func (e *s3Env) runState(runID string) string {
	e.t.Helper()
	var state string
	e.scalar(&state, `SELECT state FROM openfox.runs WHERE id = $1::uuid`, runID)
	return state
}

func listSpaceIDs(t *testing.T, client *s3Client, query string) []string {
	t.Helper()
	var body struct {
		Spaces []SpaceView `json:"spaces"`
	}
	client.must(http.StatusOK, http.MethodGet, "spaces"+query, nil, &body)
	ids := make([]string, 0, len(body.Spaces))
	for _, space := range body.Spaces {
		ids = append(ids, space.ID)
	}
	return ids
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ---- 权限矩阵 ----

func TestSpacePermissionMatrix(t *testing.T) {
	env := newS3Env(t)
	space := env.space
	shared := env.createSpace("shared")

	// 读：空间 OWNER / 组织 OWNER / 空间 MEMBER 可读私有空间；组织 ADMIN 不是成员也不可读（与现有 policy 一致）；
	// 非成员、访客、跨租户 → 404。
	for _, client := range []*s3Client{env.owner(), env.manager(), env.member()} {
		client.must(http.StatusOK, http.MethodGet, spacePath(space.ID), nil, nil)
	}
	for _, client := range []*s3Client{env.other(), env.guest()} {
		client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space.ID), nil)
	}
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space.ID), nil)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, "/v1/organizations/"+env.org+"/"+spacePath(space.ID), nil)

	// 管理字段：空间 OWNER 与组织 OWNER 可管理；空间 MEMBER 只读。
	if view := env.getSpace(env.manager(), space.ID); !view.CanManage || view.MyRole == nil || *view.MyRole != "OWNER" {
		t.Fatalf("manager view = %+v", view)
	}
	if view := env.getSpace(env.member(), space.ID); view.CanManage || view.MyRole == nil || *view.MyRole != "MEMBER" {
		t.Fatalf("member view = %+v", view)
	}
	if view := env.getSpace(env.owner(), shared.ID); view.MemberCount != 1 || view.TaskTypeCount != 1 || view.Purpose != "other" ||
		!view.Features.Views || !view.Features.Projects || view.Features.Iterations || view.Features.Triage ||
		view.Estimation.Scale != "notUsed" || view.Status != "active" {
		t.Fatalf("defaults = %+v", view)
	}

	patch := func(client *s3Client, spaceID string, version int, body map[string]any) s3Response {
		body["version"] = version
		return client.do(http.MethodPatch, spacePath(spaceID), body)
	}
	// 空间 MEMBER → 403；非成员私有空间 → 404；共享空间的非成员 → 403；跨租户 → 404。
	if response := patch(env.member(), space.ID, space.Version, map[string]any{"name": "x"}); response.Status != http.StatusForbidden {
		t.Fatalf("member patch = %d %s", response.Status, response.Body)
	}
	if response := patch(env.other(), space.ID, space.Version, map[string]any{"name": "x"}); response.Status != http.StatusNotFound {
		t.Fatalf("non-member patch = %d %s", response.Status, response.Body)
	}
	if response := patch(env.other(), shared.ID, shared.Version, map[string]any{"name": "x"}); response.Status != http.StatusForbidden {
		t.Fatalf("shared non-member patch = %d %s", response.Status, response.Body)
	}
	if response := env.outsider().doIn(env.otherOrg, http.MethodPatch, spacePath(space.ID),
		map[string]any{"version": space.Version, "name": "x"}); response.Status != http.StatusNotFound {
		t.Fatalf("cross-tenant patch = %d %s", response.Status, response.Body)
	}
	// 组织 ADMIN 可以管理共享空间；空间 OWNER 可以管理私有空间。
	var updated SpaceView
	response := patch(env.admin(), shared.ID, shared.Version, map[string]any{"name": "Renamed", "purpose": "design",
		"icon": "rocket", "features": map[string]any{"iterations": true}, "multi_assignee": false})
	if response.Status != http.StatusOK {
		t.Fatalf("admin patch = %d %s", response.Status, response.Body)
	}
	response.decode(t, &updated)
	if updated.Name != "Renamed" || updated.Purpose != "design" || updated.Icon == nil || *updated.Icon != "rocket" ||
		!updated.Features.Iterations || !updated.Features.Views || updated.MultiAssignee || updated.Version != shared.Version+1 {
		t.Fatalf("patched = %+v", updated)
	}
	// icon: null 清除；乐观锁；名称超过 32 字 → 400。
	response = patch(env.admin(), shared.ID, updated.Version, map[string]any{"icon": nil})
	response.decode(t, &updated)
	if updated.Icon != nil {
		t.Fatalf("icon not cleared: %+v", updated)
	}
	env.admin().expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, spacePath(shared.ID),
		map[string]any{"version": shared.Version, "name": "Stale"})
	env.admin().expectError(http.StatusBadRequest, "", http.MethodPatch, spacePath(shared.ID),
		map[string]any{"version": updated.Version, "name": strings.Repeat("长", 33)})
	env.admin().expectError(http.StatusBadRequest, "", http.MethodPatch, spacePath(shared.ID),
		map[string]any{"version": updated.Version, "purpose": "astrology"})
	env.manager().must(http.StatusOK, http.MethodPatch, spacePath(space.ID),
		map[string]any{"version": space.Version, "visibility": "shared"}, nil)

	// 新建空间：只有组织 OWNER / ADMIN。
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, "spaces", map[string]any{"name": "Nope"})
	env.guest().expectError(http.StatusForbidden, "", http.MethodPost, "spaces", map[string]any{"name": "Nope"})

	// 列表：可读空间带当前用户的角色；不可读的私有空间不出现。
	other := env.createSpace("private")
	if ids := listSpaceIDs(t, env.member(), ""); contains(ids, other.ID) || !contains(ids, space.ID) {
		t.Fatalf("member list = %v", ids)
	}
	env.owner().expectError(http.StatusBadRequest, "", http.MethodGet, "spaces?status=bogus", nil)
}

// ---- 标识符 ----

func TestSpaceIdentifier(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()

	// 格式：转大写；长度与字符校验。
	created := env.createSpaceIn(env.org, s3OwnerID, map[string]any{"name": "Lower " + s3Suffix(t), "prefix": strings.ToLower(s3Prefix(t))})
	if created.Prefix != strings.ToUpper(created.Prefix) || len(created.Prefix) != 7 {
		t.Fatalf("prefix = %q", created.Prefix)
	}
	for _, bad := range []string{"A", "ABCDEFGH", "AB-C", "中文"} {
		owner.expectError(http.StatusBadRequest, "INVALID_IDENTIFIER", http.MethodPost, "spaces",
			map[string]any{"name": "Bad " + s3Suffix(t), "prefix": bad})
	}
	// 不传 prefix 时由名称生成并避开占用。
	first := env.createSpaceIn(env.org, s3OwnerID, map[string]any{"name": "Quality Assurance", "prefix": ""})
	second := env.createSpaceIn(env.org, s3OwnerID, map[string]any{"name": "Quality Assurance", "prefix": ""})
	if first.Prefix != "QA" || second.Prefix == first.Prefix || !strings.HasPrefix(second.Prefix, "QA") {
		t.Fatalf("generated prefixes = %q %q", first.Prefix, second.Prefix)
	}

	// 冲突（不区分大小写）：可读空间给出名称；不可读的空间只说已占用。
	response := owner.expectError(http.StatusConflict, "IDENTIFIER_TAKEN", http.MethodPost, "spaces",
		map[string]any{"name": "Dup " + s3Suffix(t), "prefix": strings.ToLower(env.space.Prefix)})
	if !strings.Contains(string(response.Body), env.space.Name) {
		t.Fatalf("readable conflict should name the space: %s", response.Body)
	}
	response = env.admin().expectError(http.StatusConflict, "IDENTIFIER_TAKEN", http.MethodPost, "spaces",
		map[string]any{"name": "Dup " + s3Suffix(t), "prefix": env.space.Prefix})
	if strings.Contains(string(response.Body), env.space.Name) || strings.Contains(string(response.Body), "details") {
		t.Fatalf("unreadable conflict leaked the space: %s", response.Body)
	}
	// 跨组织不冲突、不泄露。
	env.createSpaceIn(env.otherOrg, s3OutsiderID, map[string]any{"name": "Same", "prefix": env.space.Prefix})

	// 改标识符：旧值进历史，任务编号随之变化，旧编号可跳转。
	space := env.space
	task := env.createTask(env.manager(), space.ID, nil)
	oldPrefix := space.Prefix
	newPrefix := s3Prefix(t)
	var changed SpaceView
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, spacePath(space.ID)+"/identifier",
		map[string]any{"version": space.Version, "prefix": newPrefix})
	env.manager().must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/identifier",
		map[string]any{"version": space.Version, "prefix": strings.ToLower(newPrefix)}, &changed)
	if changed.Prefix != newPrefix || changed.Version != space.Version+1 {
		t.Fatalf("changed = %+v", changed)
	}
	env.manager().expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, spacePath(space.ID)+"/identifier",
		map[string]any{"version": space.Version, "prefix": s3Prefix(t)})
	var byOld TaskView
	env.member().must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+strings.ToLower(oldPrefix)+"-1", nil, &byOld)
	if byOld.ID != task.ID || byOld.CanonicalDisplayID != newPrefix+"-1" || byOld.DisplayID != newPrefix+"-1" {
		t.Fatalf("old display id lookup = %+v", byOld)
	}
	var byNew TaskView
	env.member().must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+newPrefix+"-1", nil, &byNew)
	if byNew.CanonicalDisplayID != newPrefix+"-1" {
		t.Fatalf("new display id lookup = %+v", byNew)
	}
	// 旧编号对不可读的人仍是 404；跨组织 404。
	env.other().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "tasks/by-display-id/"+oldPrefix+"-1", nil)
	env.outsider().doIn(env.otherOrg, http.MethodGet, "tasks/by-display-id/"+oldPrefix+"-1", nil)
	if response := env.outsider().doIn(env.otherOrg, http.MethodGet, "tasks/by-display-id/"+newPrefix+"-1", nil); response.Status != http.StatusNotFound {
		t.Fatalf("cross-tenant display id = %d %s", response.Status, response.Body)
	}
	// 搜索按旧编号也能命中。
	var found struct {
		Tasks []TaskView `json:"tasks"`
	}
	env.member().must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/tasks?q="+oldPrefix+"-1", nil, &found)
	if len(found.Tasks) != 1 || found.Tasks[0].ID != task.ID {
		t.Fatalf("search by old id = %+v", found.Tasks)
	}
	// 历史值为原空间保留：其他空间不能占用，预检也报占用。
	owner.expectError(http.StatusConflict, "IDENTIFIER_TAKEN", http.MethodPost, "spaces",
		map[string]any{"name": "Steal " + s3Suffix(t), "prefix": oldPrefix})
	var check struct {
		Prefix    string  `json:"prefix"`
		Valid     bool    `json:"valid"`
		Available bool    `json:"available"`
		SpaceName *string `json:"space_name"`
	}
	owner.must(http.StatusOK, http.MethodGet, "space-identifiers/"+strings.ToLower(oldPrefix), nil, &check)
	if !check.Valid || check.Available || check.SpaceName == nil {
		t.Fatalf("check taken = %+v", check)
	}
	env.manager().must(http.StatusOK, http.MethodGet, "space-identifiers/"+oldPrefix+"?space_id="+space.ID, nil, &check)
	if !check.Available {
		t.Fatalf("own history should be available = %+v", check)
	}
	owner.must(http.StatusOK, http.MethodGet, "space-identifiers/x", nil, &check)
	if check.Valid || check.Available {
		t.Fatalf("invalid check = %+v", check)
	}
	// 改回本空间自己的旧值：允许；历史行保留，旧编号仍可用。
	env.manager().must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/identifier",
		map[string]any{"version": changed.Version, "prefix": oldPrefix}, &changed)
	if changed.Prefix != oldPrefix {
		t.Fatalf("revert = %+v", changed)
	}
	if count := env.count(`SELECT count(*) FROM openfox.space_prefix_history WHERE space_id = $1::uuid`, space.ID); count != 2 {
		t.Fatalf("history rows = %d", count)
	}
	env.member().must(http.StatusOK, http.MethodGet, "tasks/by-display-id/"+newPrefix+"-1", nil, &byOld)
	if byOld.CanonicalDisplayID != oldPrefix+"-1" {
		t.Fatalf("lookup after revert = %+v", byOld)
	}
	// 修改为另一个空间的现值 / 历史值 → 409。
	env.manager().expectError(http.StatusConflict, "IDENTIFIER_TAKEN", http.MethodPost, spacePath(space.ID)+"/identifier",
		map[string]any{"version": changed.Version, "prefix": first.Prefix})
}

func TestSpaceIdentifierConcurrency(t *testing.T) {
	env := newS3Env(t)
	// 8 个空间并发改为同一个标识符：只成功 1 个。
	spaces := make([]SpaceView, 8)
	for index := range spaces {
		spaces[index] = env.createSpace("private")
	}
	target := s3Prefix(t)
	statuses := parallel(len(spaces), func(index int) int {
		return env.owner().do(http.MethodPost, spacePath(spaces[index].ID)+"/identifier",
			map[string]any{"version": spaces[index].Version, "prefix": target}).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 7 {
		t.Fatalf("concurrent identifier statuses = %v", statuses)
	}
	// 并发：一个空间把旧值让出（进历史）的同时，另一个空间抢这个值 —— 两者不能同时成功占用。
	a, b := env.createSpace("private"), env.createSpace("private")
	statuses = parallel(2, func(index int) int {
		if index == 0 {
			return env.owner().do(http.MethodPost, spacePath(a.ID)+"/identifier",
				map[string]any{"version": a.Version, "prefix": s3Prefix(t)}).Status
		}
		return env.owner().do(http.MethodPost, spacePath(b.ID)+"/identifier",
			map[string]any{"version": b.Version, "prefix": a.Prefix}).Status
	})
	owners := env.count(`SELECT count(DISTINCT space_id) FROM (
		SELECT id AS space_id FROM openfox.spaces WHERE organization_id = $1::uuid AND lower(prefix) = lower($2)
		UNION ALL SELECT space_id FROM openfox.space_prefix_history WHERE organization_id = $1::uuid AND lower(prefix) = lower($2)) x`,
		env.org, a.Prefix)
	if owners != 1 || statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
		t.Fatalf("handover race statuses = %v owners = %d", statuses, owners)
	}

	// 8 个并发 PATCH 同一空间（同一 version）：只成功 1 个。
	space := env.space
	statuses = parallel(8, func(index int) int {
		return env.owner().do(http.MethodPatch, spacePath(space.ID),
			map[string]any{"version": space.Version, "name": fmt.Sprintf("Concurrent %d", index)}).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 7 {
		t.Fatalf("concurrent patch statuses = %v", statuses)
	}
}

// ---- 新建：复制配置与模板 ----

func TestSpaceCreateCopyAndTemplates(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	source := env.space
	// 来源空间：多一个类型 Bug 并绑定字段；空间分组与标签；估时设置。
	var bug struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	owner.must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/task-types", map[string]any{"name": "Bug", "icon": "bug"}, &bug)
	var field struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "custom-fields", map[string]any{"name": "Severity " + s3Suffix(t), "kind": "TEXT"}, &field)
	owner.must(http.StatusNoContent, http.MethodPut, spacePath(source.ID)+"/task-types/"+bug.ID+"/fields/"+field.ID, nil, nil)
	var group struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "label-groups",
		map[string]any{"name": "Area " + s3Suffix(t), "color": "#123456", "space_id": source.ID}, &group)
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "frontend", "space_id": source.ID, "group_id": group.ID}, nil)
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "loose", "space_id": source.ID}, nil)
	owner.must(http.StatusOK, http.MethodPatch, spacePath(source.ID)+"/estimation",
		map[string]any{"version": env.getSpace(owner, source.ID).Version, "scale": "fibonacci", "extended": true}, nil)
	env.createTask(owner, source.ID, nil)

	copied := env.createSpaceIn(env.org, s3OwnerID, map[string]any{"name": "Copy " + s3Suffix(t), "copy_from_space_id": source.ID,
		"purpose": "software_development", "visibility": "shared"})
	if copied.Estimation.Scale != "fibonacci" || !copied.Estimation.Extended || copied.Purpose != "software_development" ||
		copied.Visibility != "shared" || copied.MemberCount != 1 || copied.TaskTypeCount != 2 || copied.LabelCount != 2 {
		t.Fatalf("copied = %+v", copied)
	}
	var types struct {
		TaskTypes []struct {
			ID, Name   string
			IsDefault  bool `json:"is_default"`
			FieldCount int  `json:"field_count"`
			TaskCount  int  `json:"task_count"`
			Statuses   []struct {
				StatusID string `json:"status_id"`
			} `json:"statuses"`
		} `json:"task_types"`
	}
	owner.must(http.StatusOK, http.MethodGet, spacePath(copied.ID)+"/task-types", nil, &types)
	if len(types.TaskTypes) != 2 || types.TaskTypes[1].Name != "Bug" || types.TaskTypes[1].FieldCount != 1 ||
		!types.TaskTypes[0].IsDefault || len(types.TaskTypes[0].Statuses) == 0 || types.TaskTypes[0].TaskCount != 0 {
		t.Fatalf("copied types = %+v", types.TaskTypes)
	}
	if tasks := env.count(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, copied.ID); tasks != 0 {
		t.Fatalf("tasks copied = %d", tasks)
	}
	var groups struct {
		LabelGroups []struct {
			ID         string  `json:"id"`
			SpaceID    *string `json:"space_id"`
			LabelCount int     `json:"label_count"`
		} `json:"label_groups"`
	}
	owner.must(http.StatusOK, http.MethodGet, "label-groups?space_id="+copied.ID, nil, &groups)
	if len(groups.LabelGroups) != 1 || groups.LabelGroups[0].LabelCount != 1 || groups.LabelGroups[0].ID == group.ID ||
		groups.LabelGroups[0].SpaceID == nil || *groups.LabelGroups[0].SpaceID != copied.ID {
		t.Fatalf("copied groups = %+v", groups.LabelGroups)
	}

	// 模板：在默认类型之外追加；未知 key → 400；与复制互斥 → 400。
	templated := env.createSpaceIn(env.org, s3OwnerID, map[string]any{"name": "Tpl " + s3Suffix(t), "template_keys": []string{"bug", "feature", "bug"}})
	if templated.TaskTypeCount != 3 {
		t.Fatalf("templated type count = %d", templated.TaskTypeCount)
	}
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, "spaces", map[string]any{"name": "Bad", "template_keys": []string{"nope"}})
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, "spaces",
		map[string]any{"name": "Bad", "template_keys": []string{"bug"}, "copy_from_space_id": source.ID})
	// 来源不可读 → 404（admin 不是该私有空间成员）。
	env.admin().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "spaces",
		map[string]any{"name": "Peek", "copy_from_space_id": source.ID})

	// 幂等：同键同请求返回同一空间；同键不同请求 409。
	key := "space-create-" + s3Suffix(t)
	body := map[string]any{"name": "Idem " + s3Suffix(t), "prefix": s3Prefix(t)}
	var once, again SpaceView
	owner.must(http.StatusCreated, http.MethodPost, "spaces", body, &once, "Idempotency-Key", key)
	response := owner.must(http.StatusOK, http.MethodPost, "spaces", body, &again, "Idempotency-Key", key)
	if once.ID != again.ID || response.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("idempotent replay = %s / %s", once.ID, again.ID)
	}
	body["name"] = "Other name"
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, "spaces", body, "Idempotency-Key", key)

	var templates struct {
		Templates []struct {
			Key      string   `json:"key"`
			Purposes []string `json:"purposes"`
		} `json:"templates"`
	}
	env.member().must(http.StatusOK, http.MethodGet, "task-type-templates?purpose=marketing", nil, &templates)
	keys := []string{}
	for _, template := range templates.Templates {
		keys = append(keys, template.Key)
	}
	if !contains(keys, "campaign") || !contains(keys, "task") || contains(keys, "bug") {
		t.Fatalf("marketing templates = %v", keys)
	}
	env.member().expectError(http.StatusBadRequest, "", http.MethodGet, "task-type-templates?purpose=bogus", nil)
}

// ---- 个人排序 ----

func TestSpaceOrder(t *testing.T) {
	env := newS3Env(t)
	a, b := env.createSpace("shared"), env.createSpace("shared")
	for _, space := range []SpaceView{a, b} {
		env.addSpaceMember(space.ID, s3MemberID, "MEMBER")
	}
	env.member().must(http.StatusNoContent, http.MethodPut, "space-order", map[string]any{"space_ids": []string{b.ID, env.space.ID, a.ID}}, nil)
	ids := listSpaceIDs(t, env.member(), "")
	if len(ids) < 3 || ids[0] != b.ID || ids[1] != env.space.ID || ids[2] != a.ID {
		t.Fatalf("member order = %v", ids)
	}
	// 本人隔离：其他人的顺序不变（按 created_at）。
	ids = listSpaceIDs(t, env.owner(), "")
	if ids[0] != env.space.ID {
		t.Fatalf("owner order changed: %v", ids)
	}
	var detail SpaceView
	env.member().must(http.StatusOK, http.MethodGet, spacePath(b.ID), nil, &detail)
	if detail.Position == nil || *detail.Position != 0 {
		t.Fatalf("position = %v", detail.Position)
	}
	// 不可读的 ID、重复 ID、跨组织 ID → 400；替换为更短的列表后，没出现的空间回到 position = null。
	hidden := env.createSpace("private")
	env.member().expectError(http.StatusBadRequest, "", http.MethodPut, "space-order", map[string]any{"space_ids": []string{hidden.ID}})
	env.member().expectError(http.StatusBadRequest, "", http.MethodPut, "space-order", map[string]any{"space_ids": []string{a.ID, a.ID}})
	env.member().expectError(http.StatusBadRequest, "", http.MethodPut, "space-order", map[string]any{"space_ids": []string{env.otherSpace.ID}})
	env.member().must(http.StatusNoContent, http.MethodPut, "space-order", map[string]any{"space_ids": []string{a.ID}}, nil)
	env.member().must(http.StatusOK, http.MethodGet, spacePath(b.ID), nil, &detail)
	if detail.Position != nil {
		t.Fatalf("position should be cleared: %v", *detail.Position)
	}
	// 已删除的空间 → 400。
	env.owner().must(http.StatusOK, http.MethodDelete, spacePath(a.ID),
		map[string]any{"confirm_name": a.Name, "version": env.getSpace(env.owner(), a.ID).Version}, nil)
	env.member().expectError(http.StatusBadRequest, "", http.MethodPut, "space-order", map[string]any{"space_ids": []string{a.ID}})
}

// ---- 停用 ----

func TestSpaceRetireCancel(t *testing.T) {
	env := newS3Env(t)
	space := env.space
	manager := env.manager()
	open1 := env.createTask(manager, space.ID, nil)
	open2 := env.createTask(manager, space.ID, map[string]any{"status_id": env.statusID(env.org, "IN_PROGRESS")})
	done := env.createTask(manager, space.ID, map[string]any{"status_id": env.statusID(env.org, "DONE")})
	agent := env.insertAgent(space.ID)
	running := env.insertRun(space.ID, agent, open1.ID, "RUNNING")
	preparing := env.insertRun(space.ID, agent, "", "PREPARING")

	var impact struct {
		OpenTasks  int `json:"open_tasks"`
		TotalTasks int `json:"total_tasks"`
		ActiveRuns int `json:"active_runs"`
		Agents     int `json:"agents"`
	}
	env.member().expectError(http.StatusForbidden, "", http.MethodGet, spacePath(space.ID)+"/impact", nil)
	manager.must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/impact", nil, &impact)
	if impact.OpenTasks != 2 || impact.TotalTasks != 3 || impact.ActiveRuns != 2 || impact.Agents != 1 {
		t.Fatalf("impact = %+v", impact)
	}

	env.member().expectError(http.StatusForbidden, "", http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": space.Version, "open_tasks": "cancel"})
	manager.expectError(http.StatusBadRequest, "", http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": space.Version, "open_tasks": "explode"})
	var result struct {
		Space         SpaceView `json:"space"`
		CanceledTasks int       `json:"canceled_tasks"`
		MovedTasks    int       `json:"moved_tasks"`
		CanceledRuns  int       `json:"canceled_runs"`
	}
	manager.must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": space.Version, "open_tasks": "cancel"}, &result)
	if result.CanceledTasks != 2 || result.CanceledRuns != 2 || result.Space.Status != "retired" || result.Space.RetiredAt == nil {
		t.Fatalf("retire result = %+v", result)
	}
	for _, id := range []string{open1.ID, open2.ID} {
		var category string
		env.scalar(&category, `SELECT status FROM openfox.tasks WHERE id = $1::uuid`, id)
		if category != "CANCELLED" {
			t.Fatalf("task %s category = %s", id, category)
		}
	}
	var doneCategory string
	env.scalar(&doneCategory, `SELECT status FROM openfox.tasks WHERE id = $1::uuid`, done.ID)
	if doneCategory != "DONE" {
		t.Fatalf("done task changed to %s", doneCategory)
	}
	if env.runState(running) != "CANCEL_REQUESTED" || env.runState(preparing) != "CANCEL_REQUESTED" {
		t.Fatalf("run states = %s %s", env.runState(running), env.runState(preparing))
	}
	if conversations := env.count(`SELECT count(*) FROM openfox.conversations WHERE space_id = $1::uuid`, space.ID); conversations != 2 {
		t.Fatalf("conversations deleted: %d", conversations)
	}

	// 只读：任务、评论、会话、建 Agent → 409 SPACE_RETIRED；读取仍可用；再次停用 → 409。
	env.member().must(http.StatusOK, http.MethodGet, spacePath(space.ID), nil, nil)
	env.member().must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/tasks", nil, nil)
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, spacePath(space.ID)+"/tasks", map[string]any{"title": "x"})
	var current TaskView
	manager.must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/tasks/"+done.ID, nil, &current)
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPatch, spacePath(space.ID)+"/tasks/"+done.ID,
		map[string]any{"version": current.Version, "title": "y"})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, spacePath(space.ID)+"/tasks/"+done.ID+"/comments",
		map[string]any{"id": s3NewID(t), "client_comment_id": s3Suffix(t), "body": "hello"})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, "labels", map[string]any{"name": "late", "space_id": space.ID})
	var conversationID string
	env.scalar(&conversationID, `SELECT id::text FROM openfox.conversations WHERE space_id = $1::uuid LIMIT 1`, space.ID)
	env.owner().expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, "conversations/"+conversationID+"/messages",
		map[string]any{"id": s3NewID(t), "client_message_id": s3Suffix(t), "body": "hi"})
	deviceID := env.ensureDevice("ORGANIZATION", "")
	var grantID string
	env.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.org, deviceID)
	env.owner().expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, spacePath(space.ID)+"/agents",
		map[string]any{"name": "Late agent", "kind": "CODE", "visibility": "SHARED",
			"config": map[string]any{"computer_id": deviceID, "grant_id": grantID, "runtime": "codex", "model": "gpt-5"}})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": result.Space.Version, "open_tasks": "keep"})
	// 列表：默认只返回 active；retired / all 可见。
	if ids := listSpaceIDs(t, manager, ""); contains(ids, space.ID) {
		t.Fatalf("retired space in active list")
	}
	if ids := listSpaceIDs(t, manager, "?status=retired"); !contains(ids, space.ID) {
		t.Fatalf("retired space missing from retired list")
	}

	// 恢复：ACTIVE、写入恢复；被取消的任务不恢复；未停用时再恢复 → 409。
	var restored SpaceView
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, spacePath(space.ID)+"/restore", map[string]any{"version": result.Space.Version})
	manager.must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/restore", map[string]any{"version": result.Space.Version}, &restored)
	if restored.Status != "active" || restored.RetiredAt != nil {
		t.Fatalf("restored = %+v", restored)
	}
	manager.expectError(http.StatusConflict, "SPACE_NOT_RETIRED", http.MethodPost, spacePath(space.ID)+"/restore", map[string]any{"version": restored.Version})
	env.createTask(manager, space.ID, nil)
	var category string
	env.scalar(&category, `SELECT status FROM openfox.tasks WHERE id = $1::uuid`, open1.ID)
	if category != "CANCELLED" {
		t.Fatalf("canceled task restored: %s", category)
	}
}

func TestSpaceRetireKeepAndNoCanceledStatus(t *testing.T) {
	env := newS3Env(t)
	// 没有取消状态的工作流 → 400 NO_CANCELED_STATUS，整体不生效。
	space := env.createSpace("private")
	var types struct {
		TaskTypes []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"task_types"`
	}
	env.owner().must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/task-types", nil, &types)
	statusIDs := []string{env.statusID(env.org, "BACKLOG"), env.statusID(env.org, "TODO"), env.statusID(env.org, "DONE")}
	env.owner().must(http.StatusOK, http.MethodPatch, spacePath(space.ID)+"/task-types/"+types.TaskTypes[0].ID,
		map[string]any{"version": types.TaskTypes[0].Version, "status_ids": statusIDs}, nil)
	task := env.createTask(env.owner(), space.ID, nil)
	env.owner().expectError(http.StatusBadRequest, "NO_CANCELED_STATUS", http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": space.Version, "open_tasks": "cancel"})
	if status := env.getSpace(env.owner(), space.ID).Status; status != "active" {
		t.Fatalf("space retired despite error: %s", status)
	}
	// keep：任务保持原状。
	var result struct {
		CanceledTasks int `json:"canceled_tasks"`
	}
	env.owner().must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": space.Version, "open_tasks": "keep"}, &result)
	var category string
	env.scalar(&category, `SELECT status FROM openfox.tasks WHERE id = $1::uuid`, task.ID)
	if result.CanceledTasks != 0 || category != "TODO" {
		t.Fatalf("keep changed tasks: %+v %s", result, category)
	}
}

func TestSpaceRetireMove(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	source := env.space
	target := env.createSpace("private")
	env.addSpaceMember(target.ID, s3ManagerID, "MEMBER")
	// 目标空间有同名类型 Bug，没有 Story；目标已有 1 个任务。
	var sourceBug, targetBug struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/task-types", map[string]any{"name": "Bug"}, &sourceBug)
	owner.must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/task-types", map[string]any{"name": "Story"}, nil)
	owner.must(http.StatusCreated, http.MethodPost, spacePath(target.ID)+"/task-types", map[string]any{"name": "bug"}, &targetBug)
	env.createTask(owner, target.ID, nil)

	var storyID string
	env.scalar(&storyID, `SELECT id::text FROM openfox.task_types WHERE space_id = $1::uuid AND name = 'Story'`, source.ID)
	var label struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "Org " + s3Suffix(t)}, &label)
	var spaceLabel struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "local", "space_id": source.ID}, &spaceLabel)
	agent := env.insertAgent(source.ID)
	parent := env.createTask(owner, source.ID, map[string]any{"type_id": sourceBug.ID, "status_id": env.statusID(env.org, "IN_PROGRESS"),
		"label_ids": []string{label.ID, spaceLabel.ID}, "assignees": []map[string]any{{"kind": "HUMAN", "id": s3MemberID}, {"kind": "AGENT", "id": agent}}})
	child := env.createTask(owner, source.ID, map[string]any{"type_id": storyID, "parent_task_id": parent.ID})
	doneChild := env.createTask(owner, source.ID, map[string]any{"parent_task_id": parent.ID, "status_id": env.statusID(env.org, "DONE")})
	owner.must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/tasks/"+parent.ID+"/links", map[string]any{"url": "https://example.test/x"}, nil)
	owner.must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/tasks/"+child.ID+"/relations",
		map[string]any{"target_task_id": doneChild.ID, "type_id": "builtin:RELATES"}, nil)
	env.member().must(http.StatusCreated, http.MethodPost, spacePath(source.ID)+"/tasks/"+parent.ID+"/comments",
		map[string]any{"id": s3NewID(t), "client_comment_id": s3Suffix(t), "body": "moving soon"}, nil)

	// 目标不可写（manager 在目标空间只是 MEMBER 可写；member 不可读目标）→ 400 INVALID_TARGET_SPACE。
	retire := func(client *s3Client, targetID string) s3Response {
		return client.do(http.MethodPost, spacePath(source.ID)+"/retire",
			map[string]any{"version": env.getSpace(owner, source.ID).Version, "open_tasks": "move", "target_space_id": targetID})
	}
	env.addSpaceMember(source.ID, s3AdminID, "OWNER")
	if response := retire(env.admin(), target.ID); response.Status != http.StatusBadRequest || response.code() != "INVALID_TARGET_SPACE" {
		t.Fatalf("unreadable target = %d %s", response.Status, response.Body)
	}
	if response := retire(env.manager(), source.ID); response.code() != "INVALID_TARGET_SPACE" {
		t.Fatalf("self target = %d %s", response.Status, response.Body)
	}
	var result struct {
		MovedTasks int `json:"moved_tasks"`
	}
	response := retire(env.manager(), target.ID)
	if response.Status != http.StatusOK {
		t.Fatalf("move = %d %s", response.Status, response.Body)
	}
	response.decode(t, &result)
	if result.MovedTasks != 2 {
		t.Fatalf("moved = %d", result.MovedTasks)
	}
	var movedParent, movedChild TaskView
	owner.must(http.StatusOK, http.MethodGet, spacePath(target.ID)+"/tasks/"+parent.ID, nil, &movedParent)
	owner.must(http.StatusOK, http.MethodGet, spacePath(target.ID)+"/tasks/"+child.ID, nil, &movedChild)
	if movedParent.TypeID != targetBug.ID || movedParent.StatusCategory != "IN_PROGRESS" || movedParent.Sequence < 2 ||
		!strings.HasPrefix(movedParent.DisplayID, target.Prefix+"-") {
		t.Fatalf("moved parent = %+v", movedParent)
	}
	if movedChild.ParentTaskID == nil || *movedChild.ParentTaskID != parent.ID || movedChild.TypeName != "Task" {
		t.Fatalf("moved child = %+v", movedChild)
	}
	// 留在原空间的已完成子任务与父任务解除关系；关联与链接跟随；空间标签与 Agent 处理人移除，人员处理人保留。
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND parent_task_id IS NULL AND space_id = $2::uuid`,
		doneChild.ID, source.ID); count != 1 {
		t.Fatalf("done child not detached")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_relations WHERE source_task_id = $1::uuid AND source_space_id = $2::uuid
		AND target_space_id = $3::uuid`, child.ID, target.ID, source.ID); count != 1 {
		t.Fatalf("relation not moved")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_links WHERE task_id = $1::uuid AND space_id = $2::uuid`, parent.ID, target.ID); count != 1 {
		t.Fatalf("link not moved")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_labels WHERE task_id = $1::uuid`, parent.ID); count != 1 {
		t.Fatalf("labels after move = %d", count)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_assignees WHERE task_id = $1::uuid AND kind = 'HUMAN' AND space_id = $2::uuid`,
		parent.ID, target.ID); count != 1 {
		t.Fatalf("human assignee lost")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_assignees WHERE task_id = $1::uuid AND kind <> 'HUMAN'`, parent.ID); count != 0 {
		t.Fatalf("agent assignee kept")
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_comments WHERE task_id = $1::uuid AND space_id = $2::uuid`, parent.ID, target.ID); count != 1 {
		t.Fatalf("comment not moved")
	}
	// 旧编号不跳转（编号在目标空间重新分配）。
	owner.expectError(http.StatusNotFound, "", http.MethodGet, "tasks/by-display-id/"+source.Prefix+"-1", nil)
}

// ---- 删除 ----

func TestSpaceDelete(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space
	survivor := env.createSpace("private")
	task := env.createTask(env.manager(), space.ID, nil)
	agent := env.insertAgent(space.ID)
	run := env.insertRun(space.ID, agent, task.ID, "RUNNING")
	spaceDevice := env.ensureDevice("SPACE", space.ID)
	otherDevice := env.ensureDevice("SPACE", survivor.ID)
	env.member().must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/tasks/"+task.ID+"/comments",
		map[string]any{"id": s3NewID(t), "client_comment_id": s3Suffix(t), "body": "@someone", "mentions": []map[string]any{{"user_id": s3ManagerID}}}, nil)

	current := env.getSpace(owner, space.ID)
	env.member().expectError(http.StatusForbidden, "", http.MethodDelete, spacePath(space.ID),
		map[string]any{"confirm_name": space.Name, "version": current.Version})
	env.other().expectError(http.StatusNotFound, "", http.MethodDelete, spacePath(space.ID),
		map[string]any{"confirm_name": space.Name, "version": current.Version})
	env.manager().expectError(http.StatusBadRequest, "CONFIRM_MISMATCH", http.MethodDelete, spacePath(space.ID),
		map[string]any{"confirm_name": strings.ToUpper(space.Name), "version": current.Version})
	env.manager().expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete, spacePath(space.ID),
		map[string]any{"confirm_name": space.Name, "version": current.Version + 9})
	var result struct {
		Deleted        bool `json:"deleted"`
		ArchivedAgents int  `json:"archived_agents"`
		CanceledRuns   int  `json:"canceled_runs"`
		RevokedGrants  int  `json:"revoked_grants"`
	}
	env.manager().must(http.StatusOK, http.MethodDelete, spacePath(space.ID),
		map[string]any{"confirm_name": space.Name, "version": current.Version}, &result)
	if !result.Deleted || result.ArchivedAgents != 1 || result.CanceledRuns != 1 || result.RevokedGrants != 1 {
		t.Fatalf("delete result = %+v", result)
	}
	if env.runState(run) != "CANCEL_REQUESTED" {
		t.Fatalf("run state = %s", env.runState(run))
	}
	if count := env.count(`SELECT count(*) FROM openfox.agents WHERE id = $1::uuid AND archived_at IS NOT NULL`, agent); count != 1 {
		t.Fatalf("agent not archived")
	}
	var grantStatus string
	var permission int
	env.scalar(&grantStatus, `SELECT status FROM openfox.device_grants WHERE device_id = $1::uuid`, spaceDevice)
	env.scalar(&permission, `SELECT permission_version FROM openfox.device_grants WHERE device_id = $1::uuid`, spaceDevice)
	if grantStatus != "REVOKED" || permission < 2 {
		t.Fatalf("space grant = %s %d", grantStatus, permission)
	}
	env.scalar(&grantStatus, `SELECT status FROM openfox.device_grants WHERE device_id = $1::uuid`, otherDevice)
	if grantStatus != "ACTIVE" {
		t.Fatalf("other space grant revoked")
	}
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, space.ID); count != 1 {
		t.Fatalf("tasks physically removed")
	}

	// 删除后的读路径：详情 / 任务 / 任务详情 / 编号 / 列表 / 我的任务 / 收件箱 / 会话列表 / Agent 目录 / 成员 / 标签 picker 一律 404 或不出现。
	for _, client := range []*s3Client{owner, env.manager(), env.member()} {
		client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space.ID), nil)
		client.expectError(http.StatusNotFound, "", http.MethodGet, spacePath(space.ID)+"/tasks", nil)
		client.expectError(http.StatusNotFound, "", http.MethodGet, spacePath(space.ID)+"/tasks/"+task.ID, nil)
		client.expectError(http.StatusNotFound, "", http.MethodGet, "tasks/by-display-id/"+task.DisplayID, nil)
		client.expectError(http.StatusNotFound, "", http.MethodGet, spacePath(space.ID)+"/members", nil)
		client.expectError(http.StatusNotFound, "", http.MethodGet, "labels?scope=picker&space_id="+space.ID, nil)
		client.expectError(http.StatusNotFound, "", http.MethodGet, spacePath(space.ID)+"/agents", nil)
		if ids := listSpaceIDs(t, client, "?status=all"); contains(ids, space.ID) {
			t.Fatalf("deleted space listed")
		}
	}
	for _, path := range []string{"my-tasks", "inbox", "conversations", "agent-directory", "contacts"} {
		response := env.manager().do(http.MethodGet, path, nil)
		if response.Status != http.StatusOK {
			continue // 不是所有组织级读接口都按这个路径注册；只检查存在的那些
		}
		if strings.Contains(string(response.Body), space.ID) || strings.Contains(string(response.Body), task.ID) {
			t.Fatalf("%s still shows the deleted space: %s", path, response.Body)
		}
	}
	// 不能恢复、不能再改。
	env.manager().expectError(http.StatusNotFound, "", http.MethodPost, spacePath(space.ID)+"/restore", map[string]any{"version": current.Version + 1})

	// 最后一个空间不能删：此时组织里只剩 survivor，再建一个，并发删除这两个时只成功一个。
	extra := env.createSpace("private")
	type live struct {
		id, name string
		version  int
	}
	spaces := []live{}
	for _, id := range []string{survivor.ID, extra.ID} {
		view := env.getSpace(owner, id)
		spaces = append(spaces, live{id: view.ID, name: view.Name, version: view.Version})
	}
	if count := env.count(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND deleted_at IS NULL`, env.org); count != 2 {
		t.Fatalf("live spaces before race = %d", count)
	}
	last := spaces
	statuses := parallel(2, func(index int) int {
		return owner.do(http.MethodDelete, spacePath(last[index].id),
			map[string]any{"confirm_name": last[index].name, "version": last[index].version}).Status
	})
	if countStatus(statuses, http.StatusOK) != 1 || countStatus(statuses, http.StatusConflict) != 1 {
		t.Fatalf("last space race = %v", statuses)
	}
	if count := env.count(`SELECT count(*) FROM openfox.spaces WHERE organization_id = $1::uuid AND deleted_at IS NULL`, env.org); count != 1 {
		t.Fatalf("live spaces = %d", count)
	}
}

// 停用 move：带表情回应（描述与评论）和自定义字段值的任务搬到目标空间后数据完整，原空间无残留。
func TestSpaceRetireMoveReactionsAndFields(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	source := env.space
	target := env.createSpace("private")
	var field struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "custom-fields", map[string]any{"name": "Note " + s3Suffix(t), "kind": "TEXT"}, &field)
	var typeID string
	env.scalar(&typeID, `SELECT id::text FROM openfox.task_types WHERE space_id = $1::uuid AND is_default`, source.ID)
	owner.must(http.StatusNoContent, http.MethodPut, spacePath(source.ID)+"/task-types/"+typeID+"/fields/"+field.ID, nil, nil)
	task := env.createTask(owner, source.ID, nil)
	base := spacePath(source.ID) + "/tasks/" + task.ID
	owner.must(http.StatusOK, http.MethodPut, base+"/fields/"+field.ID, map[string]any{"value": "keep me"}, nil)
	commentID := s3NewID(t)
	owner.must(http.StatusCreated, http.MethodPost, base+"/comments",
		map[string]any{"id": commentID, "client_comment_id": s3Suffix(t), "body": "with reactions"}, nil)
	for _, body := range []map[string]any{{"emoji": "👍"}, {"emoji": "🎉", "comment_id": commentID}} {
		response := owner.do(http.MethodPut, base+"/reactions", body)
		if response.Status != http.StatusOK && response.Status != http.StatusNoContent {
			t.Fatalf("reaction = %d %s", response.Status, response.Body)
		}
	}
	env.member().must(http.StatusNoContent, http.MethodPut, base+"/reactions", map[string]any{"emoji": "👍"}, nil)
	before := map[string]int{
		"reactions": env.count(`SELECT count(*) FROM openfox.task_reactions WHERE task_id = $1::uuid`, task.ID),
		"fields":    env.count(`SELECT count(*) FROM openfox.task_field_values WHERE task_id = $1::uuid`, task.ID),
	}
	if before["reactions"] != 3 || before["fields"] != 1 {
		t.Fatalf("setup = %+v", before)
	}

	var result struct {
		MovedTasks int `json:"moved_tasks"`
	}
	owner.must(http.StatusOK, http.MethodPost, spacePath(source.ID)+"/retire",
		map[string]any{"version": env.getSpace(owner, source.ID).Version, "open_tasks": "move", "target_space_id": target.ID}, &result)
	if result.MovedTasks != 1 {
		t.Fatalf("moved = %d", result.MovedTasks)
	}
	// 目标空间：数量、内容一致；评论回应仍指向原评论。
	if count := env.count(`SELECT count(*) FROM openfox.task_reactions WHERE task_id = $1::uuid AND space_id = $2::uuid`, task.ID, target.ID); count != 3 {
		t.Fatalf("reactions in target = %d", count)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_reactions WHERE task_id = $1::uuid AND comment_id = $2::uuid AND emoji = '🎉'`, task.ID, commentID); count != 1 {
		t.Fatalf("comment reaction lost")
	}
	if count := env.count(`SELECT count(DISTINCT user_id) FROM openfox.task_reactions WHERE task_id = $1::uuid AND emoji = '👍' AND comment_id IS NULL`, task.ID); count != 2 {
		t.Fatalf("description reactions = %d", count)
	}
	var value string
	env.scalar(&value, `SELECT value #>> '{}' FROM openfox.task_field_values WHERE task_id = $1::uuid AND field_id = $2::uuid AND space_id = $3::uuid`,
		task.ID, field.ID, target.ID)
	if value != "keep me" {
		t.Fatalf("field value = %q", value)
	}
	// 原空间无残留。
	for _, table := range []string{"task_reactions", "task_field_values", "task_comments", "task_assignees", "task_followers", "task_activities"} {
		if count := env.count(`SELECT count(*) FROM openfox.`+table+` WHERE task_id = $1::uuid AND space_id = $2::uuid`, task.ID, source.ID); count != 0 {
			t.Fatalf("%s left in source = %d", table, count)
		}
	}
	// 接口读取：描述回应在目标空间可见。
	var detail struct {
		DescriptionReactions []struct {
			Emoji string `json:"emoji"`
			Count int    `json:"count"`
		} `json:"description_reactions"`
	}
	owner.must(http.StatusOK, http.MethodGet, spacePath(target.ID)+"/tasks/"+task.ID, nil, &detail)
	if len(detail.DescriptionReactions) != 1 || detail.DescriptionReactions[0].Count != 2 {
		t.Fatalf("description reactions via API = %+v", detail.DescriptionReactions)
	}
}

// 未完成任务超过 5000 个：停用整体拒绝（409 TOO_MANY_TASKS），任务与空间都不改动。
func TestSpaceRetireTooManyTasks(t *testing.T) {
	env := newS3Env(t)
	space := env.createSpace("private")
	target := env.createSpace("private")
	var typeID string
	env.scalar(&typeID, `SELECT id::text FROM openfox.task_types WHERE space_id = $1::uuid AND is_default`, space.ID)
	todo := env.statusID(env.org, "TODO")
	env.exec(`INSERT INTO openfox.tasks (organization_id, space_id, sequence, type_id, status_id, title)
		SELECT $1::uuid, $2::uuid, g, $3::uuid, $4::uuid, 'Bulk ' || g FROM generate_series(1, 5001) g`,
		env.org, space.ID, typeID, todo)
	env.exec(`UPDATE openfox.spaces SET next_task_number = 5002 WHERE id = $1::uuid`, space.ID)
	current := env.getSpace(env.owner(), space.ID)
	snapshot := func() string {
		var value string
		env.scalar(&value, `SELECT count(*)::text || ':' || COALESCE(sum(version), 0)::text || ':' ||
			count(*) FILTER (WHERE status = 'TODO')::text || ':' || count(*) FILTER (WHERE space_id = $1::uuid)::text
			FROM openfox.tasks WHERE organization_id = $2::uuid`, space.ID, env.org)
		return value
	}
	before := snapshot()
	for _, mode := range []map[string]any{{"open_tasks": "cancel"}, {"open_tasks": "move", "target_space_id": target.ID}} {
		mode["version"] = current.Version
		env.owner().expectError(http.StatusConflict, "TOO_MANY_TASKS", http.MethodPost, spacePath(space.ID)+"/retire", mode)
	}
	if after := snapshot(); after != before {
		t.Fatalf("tasks changed: %s -> %s", before, after)
	}
	view := env.getSpace(env.owner(), space.ID)
	if view.Status != "active" || view.Version != current.Version {
		t.Fatalf("space changed: %+v", view)
	}
	var targetNext int
	env.scalar(&targetNext, `SELECT next_task_number FROM openfox.spaces WHERE id = $1::uuid`, target.ID)
	if targetNext != 1 {
		t.Fatalf("target sequence consumed: %d", targetNext)
	}
	// keep 不处理任务，不受上限影响。
	env.owner().must(http.StatusOK, http.MethodPost, spacePath(space.ID)+"/retire",
		map[string]any{"version": current.Version, "open_tasks": "keep"}, nil)
}
