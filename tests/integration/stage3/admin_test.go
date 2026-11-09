package stage3

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type admMember struct {
	UserID    string  `json:"user_id"`
	Email     string  `json:"email"`
	Role      string  `json:"role"`
	Status    string  `json:"status"`
	OrgRole   *string `json:"org_role"`
	OnlySpace bool    `json:"only_space"`
}

func admMembers(t *testing.T, client *s3Client, spaceID, query string) []admMember {
	t.Helper()
	var body struct {
		Members []admMember `json:"members"`
	}
	client.must(http.StatusOK, http.MethodGet, spacePath(spaceID)+"/members"+query, nil, &body)
	return body.Members
}

func TestAdminSpaceMembers(t *testing.T) {
	env := newS3Env(t)
	space := env.space
	manager := env.manager()

	// 列表：org_role、only_space、q / role 过滤。member 只在 env.space 里 → only_space。
	members := admMembers(t, env.member(), space.ID, "")
	if len(members) != 3 {
		t.Fatalf("members = %+v", members)
	}
	for _, member := range members {
		switch member.UserID {
		case s3MemberID:
			if !member.OnlySpace || member.OrgRole == nil || *member.OrgRole != "MEMBER" {
				t.Fatalf("member row = %+v", member)
			}
		case s3OwnerID:
			if member.Role != "OWNER" || member.OrgRole == nil || *member.OrgRole != "OWNER" {
				t.Fatalf("owner row = %+v", member)
			}
		}
	}
	if got := admMembers(t, manager, space.ID, "?role=OWNER"); len(got) != 2 {
		t.Fatalf("role filter = %+v", got)
	}
	if got := admMembers(t, manager, space.ID, "?q="+s3Email(s3MemberID)[:7]); len(got) != 1 || got[0].UserID != s3MemberID {
		t.Fatalf("q filter = %+v", got)
	}
	manager.expectError(http.StatusBadRequest, "", http.MethodGet, spacePath(space.ID)+"/members?role=KING", nil)
	env.other().expectError(http.StatusNotFound, "", http.MethodGet, spacePath(space.ID)+"/members", nil)
	env.outsider().expectError(http.StatusNotFound, "", http.MethodGet, "/v1/organizations/"+env.org+"/"+spacePath(space.ID)+"/members", nil)

	// 添加 / 改角色：普通成员不能改别人（403）；空间 OWNER 可以加成员；只有组织 OWNER / ADMIN 能授 OWNER。
	env.member().expectError(http.StatusForbidden, "", http.MethodPut, spacePath(space.ID)+"/members/"+s3OtherID, map[string]any{"role": "MEMBER"})
	var added admMember
	manager.must(http.StatusOK, http.MethodPut, spacePath(space.ID)+"/members/"+s3OtherID, map[string]any{"role": "EDITOR"}, &added)
	if added.Role != "EDITOR" || added.Status != "ACTIVE" {
		t.Fatalf("added = %+v", added)
	}
	manager.expectError(http.StatusForbidden, "", http.MethodPut, spacePath(space.ID)+"/members/"+s3OtherID, map[string]any{"role": "OWNER"})
	// 访客不能当管理员；非组织成员 / 跨租户用户 → 404。
	manager.expectError(http.StatusBadRequest, "", http.MethodPut, spacePath(space.ID)+"/members/"+s3GuestID, map[string]any{"role": "EDITOR"})
	manager.must(http.StatusOK, http.MethodPut, spacePath(space.ID)+"/members/"+s3GuestID, map[string]any{"role": "VIEWER"}, nil)
	manager.expectError(http.StatusNotFound, "", http.MethodPut, spacePath(space.ID)+"/members/"+s3OutsiderID, map[string]any{"role": "MEMBER"})

	// 最后一个 OWNER：先把 owner 降级，manager 成为唯一 OWNER；再降级 / 撤销 / 移出 manager → 409 LAST_OWNER。
	env.owner().must(http.StatusOK, http.MethodPut, spacePath(space.ID)+"/members/"+s3OwnerID, map[string]any{"role": "MEMBER"}, nil)
	manager.expectError(http.StatusConflict, "LAST_OWNER", http.MethodPut, spacePath(space.ID)+"/members/"+s3ManagerID, map[string]any{"role": "EDITOR"})
	env.owner().expectError(http.StatusConflict, "LAST_OWNER", http.MethodPut, spacePath(space.ID)+"/members/"+s3ManagerID,
		map[string]any{"role": "OWNER", "status": "REVOKED"})
	manager.expectError(http.StatusConflict, "LAST_OWNER", http.MethodDelete, spacePath(space.ID)+"/members/"+s3ManagerID, nil)
	// 并发：两个 OWNER 同时把对方降级，只能成功一个（总有一个 OWNER 留下）。
	env.owner().must(http.StatusOK, http.MethodPut, spacePath(space.ID)+"/members/"+s3OwnerID, map[string]any{"role": "OWNER"}, nil)
	statuses := parallel(2, func(index int) int {
		if index == 0 {
			return env.owner().do(http.MethodPut, spacePath(space.ID)+"/members/"+s3ManagerID, map[string]any{"role": "MEMBER"}).Status
		}
		return env.admin().do(http.MethodPut, spacePath(space.ID)+"/members/"+s3OwnerID, map[string]any{"role": "MEMBER"}).Status
	})
	if owners := env.count(`SELECT count(*) FROM openfox.space_members WHERE space_id = $1::uuid AND role = 'OWNER' AND status = 'ACTIVE'`, space.ID); owners != 1 || countStatus(statuses, http.StatusOK) != 1 {
		t.Fatalf("owner race statuses = %v owners = %d", statuses, owners)
	}

	// 离开空间：member 唯一可读空间 → 409 ONLY_SPACE；加入另一个空间后可以离开。
	env.member().expectError(http.StatusConflict, "ONLY_SPACE", http.MethodDelete, spacePath(space.ID)+"/members/"+s3MemberID, nil)
	env.owner().expectError(http.StatusConflict, "ONLY_SPACE", http.MethodDelete, spacePath(space.ID)+"/members/"+s3MemberID, nil)
	shared := env.createSpace("shared")
	env.member().must(http.StatusNoContent, http.MethodDelete, spacePath(space.ID)+"/members/"+s3MemberID, nil, nil)
	env.member().expectError(http.StatusNotFound, "", http.MethodGet, spacePath(space.ID), nil)
	env.owner().expectError(http.StatusNotFound, "", http.MethodDelete, spacePath(space.ID)+"/members/"+s3MemberID, nil)

	// 自助加入：SHARED 空间、role = MEMBER 成功；PRIVATE → 404；访客 → 404（不可读）；自助加 EDITOR → 403。
	var joined admMember
	env.member().expectError(http.StatusForbidden, "", http.MethodPut, spacePath(shared.ID)+"/members/"+s3MemberID, map[string]any{"role": "EDITOR"})
	env.member().must(http.StatusOK, http.MethodPut, spacePath(shared.ID)+"/members/"+s3MemberID, map[string]any{"role": "MEMBER"}, &joined)
	if joined.Role != "MEMBER" {
		t.Fatalf("joined = %+v", joined)
	}
	env.member().expectError(http.StatusNotFound, "", http.MethodPut, spacePath(space.ID)+"/members/"+s3MemberID, map[string]any{"role": "MEMBER"})
	env.guest().expectError(http.StatusNotFound, "", http.MethodPut, spacePath(shared.ID)+"/members/"+s3GuestID, map[string]any{"role": "MEMBER"})
	if events := env.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'space_member.joined'`, env.org); events != 1 {
		t.Fatalf("join audit events = %d", events)
	}
}

type admRepo struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Description string `json:"description"`
	BaseBranch  string `json:"base_branch"`
	Position    int    `json:"position"`
	Version     int    `json:"version"`
}

func TestAdminSpaceRepos(t *testing.T) {
	env := newS3Env(t)
	space := env.space
	manager := env.manager()
	base := spacePath(space.ID) + "/repos"

	for _, bad := range []map[string]any{{"url": "ftp://x/y"}, {"url": "https://x/y z"}, {"url": "https://x/y", "base_branch": "a b"},
		{"url": "https://x/y", "description": strings.Repeat("d", 501)}} {
		manager.expectError(http.StatusBadRequest, "", http.MethodPost, base, bad)
	}
	var repo admRepo
	manager.must(http.StatusCreated, http.MethodPost, base, map[string]any{"url": "https://github.com/acme/app", "base_branch": "main"}, &repo)
	manager.must(http.StatusCreated, http.MethodPost, base, map[string]any{"url": "git@github.com:acme/api.git"}, nil)
	manager.expectError(http.StatusConflict, "REPO_EXISTS", http.MethodPost, base, map[string]any{"url": "https://GITHUB.com/acme/app"})
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, base, map[string]any{"url": "https://github.com/acme/x"})
	env.other().expectError(http.StatusNotFound, "", http.MethodGet, base, nil)
	var list struct {
		Repos []admRepo `json:"repos"`
	}
	env.member().must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Repos) != 2 || list.Repos[0].ID != repo.ID || list.Repos[1].Position != 1 {
		t.Fatalf("repos = %+v", list.Repos)
	}

	var patched admRepo
	manager.must(http.StatusOK, http.MethodPatch, base+"/"+repo.ID, map[string]any{"version": repo.Version, "description": "Main app"}, &patched)
	if patched.Description != "Main app" || patched.BaseBranch != "main" || patched.Version != repo.Version+1 {
		t.Fatalf("patched = %+v", patched)
	}
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, base+"/"+repo.ID, map[string]any{"version": repo.Version, "description": "stale"})
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete, base+"/"+repo.ID+"?version="+itoa(repo.Version), nil)
	manager.must(http.StatusNoContent, http.MethodDelete, base+"/"+repo.ID+"?version="+itoa(patched.Version), nil, nil)
	manager.expectError(http.StatusNotFound, "", http.MethodDelete, base+"/"+repo.ID+"?version="+itoa(patched.Version), nil)

	// 上限 20：并发添加不会突破。
	statuses := parallel(24, func(index int) int {
		return manager.do(http.MethodPost, base, map[string]any{"url": fmt.Sprintf("https://github.com/acme/r%d", index)}).Status
	})
	if total := env.count(`SELECT count(*) FROM openfox.space_repos WHERE space_id = $1::uuid`, space.ID); total != 20 ||
		countStatus(statuses, http.StatusCreated) != 19 {
		t.Fatalf("repo limit statuses = %v total = %d", statuses, total)
	}
	manager.expectError(http.StatusConflict, "LIMIT_REACHED", http.MethodPost, base, map[string]any{"url": "https://github.com/acme/overflow"})
}

func TestAdminTaskTypes(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space
	type taskType struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsDefault  bool   `json:"is_default"`
		Version    int    `json:"version"`
		FieldCount int    `json:"field_count"`
		TaskCount  int    `json:"task_count"`
		ArchivedAt *string
	}
	list := func() map[string]taskType {
		var body struct {
			TaskTypes []struct {
				taskType
				ArchivedAt *string `json:"archived_at"`
			} `json:"task_types"`
		}
		owner.must(http.StatusOK, http.MethodGet, spacePath(space.ID)+"/task-types", nil, &body)
		result := map[string]taskType{}
		for _, item := range body.TaskTypes {
			item.taskType.ArchivedAt = item.ArchivedAt
			result[item.Name] = item.taskType
		}
		return result
	}
	var bug, spike, chore taskType
	owner.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{"name": "Bug"}, &bug)
	owner.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{"name": "Spike"}, &spike)
	owner.must(http.StatusCreated, http.MethodPost, spacePath(space.ID)+"/task-types", map[string]any{"name": "Chore"}, &chore)
	var field struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "custom-fields", map[string]any{"name": "Env " + s3Suffix(t), "kind": "TEXT"}, &field)
	owner.must(http.StatusNoContent, http.MethodPut, spacePath(space.ID)+"/task-types/"+bug.ID+"/fields/"+field.ID, nil, nil)
	live := env.createTask(owner, space.ID, map[string]any{"type_id": bug.ID})
	gone := env.createTask(owner, space.ID, map[string]any{"type_id": spike.ID})
	owner.must(http.StatusNoContent, http.MethodDelete, spacePath(space.ID)+"/tasks/"+gone.ID+"?version=1", nil, nil)
	types := list()
	if types["Bug"].FieldCount != 1 || types["Bug"].TaskCount != 1 || types["Spike"].TaskCount != 0 {
		t.Fatalf("counts = %+v", types)
	}
	deletePath := func(item taskType) string {
		return spacePath(space.ID) + "/task-types/" + item.ID + "?version=" + itoa(item.Version)
	}
	env.member().expectError(http.StatusForbidden, "", http.MethodDelete, deletePath(types["Chore"]), nil)
	owner.expectError(http.StatusConflict, "TYPE_IN_USE", http.MethodDelete, deletePath(types["Bug"]), nil)
	owner.expectError(http.StatusConflict, "DEFAULT_TYPE_REQUIRED", http.MethodDelete, deletePath(types["Task"]), nil)
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete,
		spacePath(space.ID)+"/task-types/"+chore.ID+"?version=99", nil)
	// 默认类型不能停用 / 归档。
	owner.expectError(http.StatusConflict, "DEFAULT_TYPE_REQUIRED", http.MethodPatch, spacePath(space.ID)+"/task-types/"+types["Task"].ID,
		map[string]any{"version": types["Task"].Version, "enabled": false})
	owner.expectError(http.StatusConflict, "DEFAULT_TYPE_REQUIRED", http.MethodPatch, spacePath(space.ID)+"/task-types/"+types["Task"].ID,
		map[string]any{"version": types["Task"].Version, "archived": true})
	// 只被已删除任务引用 → 归档；无任务 → 物理删除。
	owner.must(http.StatusNoContent, http.MethodDelete, deletePath(types["Spike"]), nil, nil)
	owner.must(http.StatusNoContent, http.MethodDelete, deletePath(types["Chore"]), nil, nil)
	types = list()
	if spikeAfter, ok := types["Spike"]; !ok || spikeAfter.ArchivedAt == nil {
		t.Fatalf("spike should be archived: %+v", types)
	}
	if _, ok := types["Chore"]; ok {
		t.Fatalf("chore should be deleted: %+v", types)
	}
	if count := env.count(`SELECT count(*) FROM openfox.workflow_statuses WHERE task_type_id = $1::uuid`, chore.ID); count != 0 {
		t.Fatalf("workflow rows left = %d", count)
	}
	_ = live
	// 停用空间里不能再改类型之外的对象；类型管理不在 3a 的只读范围内，不做断言。
}

func TestAdminSpaceLabels(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space
	manager := env.manager()
	type group struct {
		ID         string  `json:"id"`
		SpaceID    *string `json:"space_id"`
		LabelCount int     `json:"label_count"`
		Version    int     `json:"version"`
	}
	type label struct {
		ID         string  `json:"id"`
		SpaceID    *string `json:"space_id"`
		GroupID    *string `json:"group_id"`
		ArchivedAt *string `json:"archived_at"`
		Version    int     `json:"version"`
	}
	// 空间分组：空间管理员可建；普通成员 403；非成员 404。
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, "label-groups",
		map[string]any{"name": "Nope " + s3Suffix(t), "color": "#111111", "space_id": space.ID})
	env.other().expectError(http.StatusNotFound, "", http.MethodPost, "label-groups",
		map[string]any{"name": "Nope " + s3Suffix(t), "color": "#111111", "space_id": space.ID})
	var spaceGroup, orgGroup group
	manager.must(http.StatusCreated, http.MethodPost, "label-groups",
		map[string]any{"name": "Area " + s3Suffix(t), "color": "#123456", "space_id": space.ID}, &spaceGroup)
	owner.must(http.StatusCreated, http.MethodPost, "label-groups", map[string]any{"name": "Org " + s3Suffix(t), "color": "#654321"}, &orgGroup)
	if spaceGroup.SpaceID == nil || *spaceGroup.SpaceID != space.ID || orgGroup.SpaceID != nil {
		t.Fatalf("groups = %+v %+v", spaceGroup, orgGroup)
	}
	var groups struct {
		LabelGroups []group `json:"label_groups"`
	}
	env.member().must(http.StatusOK, http.MethodGet, "label-groups?space_id="+space.ID, nil, &groups)
	if len(groups.LabelGroups) != 1 || groups.LabelGroups[0].ID != spaceGroup.ID {
		t.Fatalf("space groups = %+v", groups.LabelGroups)
	}
	owner.must(http.StatusOK, http.MethodGet, "label-groups", nil, &groups)
	for _, item := range groups.LabelGroups {
		if item.ID == spaceGroup.ID {
			t.Fatalf("space group in organization list")
		}
	}
	env.other().expectError(http.StatusNotFound, "", http.MethodGet, "label-groups?space_id="+space.ID, nil)

	// 空间标签进同空间分组成功；进组织分组 400；组织标签进空间分组 400。
	var inGroup, loose, orgLabel label
	env.member().must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "fe", "space_id": space.ID, "group_id": spaceGroup.ID}, &inGroup)
	env.member().must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "be", "space_id": space.ID}, &loose)
	env.member().expectError(http.StatusBadRequest, "", http.MethodPost, "labels", map[string]any{"name": "x", "space_id": space.ID, "group_id": orgGroup.ID})
	owner.must(http.StatusCreated, http.MethodPost, "labels", map[string]any{"name": "Org " + s3Suffix(t)}, &orgLabel)
	owner.expectError(http.StatusBadRequest, "", http.MethodPatch, "labels/"+orgLabel.ID, map[string]any{"version": orgLabel.Version, "group_id": spaceGroup.ID})
	manager.expectError(http.StatusBadRequest, "", http.MethodPatch, "labels/"+loose.ID, map[string]any{"version": loose.Version, "group_id": orgGroup.ID})
	// 分组与归档需要空间管理权限；普通成员只能改名称。
	env.member().expectError(http.StatusForbidden, "", http.MethodPatch, "labels/"+loose.ID, map[string]any{"version": loose.Version, "archived": true})
	manager.must(http.StatusOK, http.MethodPatch, "labels/"+loose.ID, map[string]any{"version": loose.Version, "group_id": spaceGroup.ID}, &loose)
	manager.must(http.StatusOK, http.MethodPatch, "labels/"+loose.ID, map[string]any{"version": loose.Version, "archived": true}, &loose)
	if loose.ArchivedAt == nil || loose.GroupID == nil {
		t.Fatalf("loose = %+v", loose)
	}
	var labels struct {
		Labels []label `json:"labels"`
	}
	env.member().must(http.StatusOK, http.MethodGet, "labels?scope=space&space_id="+space.ID, nil, &labels)
	if len(labels.Labels) != 1 || labels.Labels[0].ID != inGroup.ID {
		t.Fatalf("space labels = %+v", labels.Labels)
	}
	env.member().must(http.StatusOK, http.MethodGet, "labels?scope=space_archived&space_id="+space.ID, nil, &labels)
	if len(labels.Labels) != 1 || labels.Labels[0].ID != loose.ID {
		t.Fatalf("archived space labels = %+v", labels.Labels)
	}
	env.other().expectError(http.StatusNotFound, "", http.MethodGet, "labels?scope=space&space_id="+space.ID, nil)
	owner.expectError(http.StatusBadRequest, "", http.MethodGet, "labels?scope=space", nil)

	// 批量（space_id）：只处理该空间标签；组织标签被忽略；普通成员 403。
	var affected struct {
		Affected int `json:"affected"`
	}
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, "labels/bulk",
		map[string]any{"action": "UNARCHIVE", "space_id": space.ID, "label_ids": []string{loose.ID}})
	manager.must(http.StatusOK, http.MethodPost, "labels/bulk",
		map[string]any{"action": "UNARCHIVE", "space_id": space.ID, "label_ids": []string{loose.ID, orgLabel.ID}}, &affected)
	if affected.Affected != 1 {
		t.Fatalf("bulk unarchive = %d", affected.Affected)
	}
	manager.must(http.StatusOK, http.MethodPost, "labels/bulk",
		map[string]any{"action": "MERGE", "space_id": space.ID, "label_ids": []string{loose.ID}, "target_label_id": inGroup.ID}, &affected)
	if affected.Affected != 1 {
		t.Fatalf("bulk merge = %d", affected.Affected)
	}
	manager.expectError(http.StatusNotFound, "", http.MethodPost, "labels/bulk",
		map[string]any{"action": "MERGE", "space_id": space.ID, "label_ids": []string{inGroup.ID}, "target_label_id": orgLabel.ID})
	// 组织级批量仍不碰空间标签。
	owner.must(http.StatusOK, http.MethodPost, "labels/bulk", map[string]any{"action": "ARCHIVE", "label_ids": []string{inGroup.ID}}, &affected)
	if affected.Affected != 0 {
		t.Fatalf("organization bulk touched space label")
	}
	// 删除空间分组：组内标签移出分组；需空间管理权限。
	env.member().expectError(http.StatusForbidden, "", http.MethodDelete, "label-groups/"+spaceGroup.ID, nil)
	var removed struct {
		RemovedLabelIDs []string `json:"removed_label_ids"`
	}
	manager.must(http.StatusOK, http.MethodDelete, "label-groups/"+spaceGroup.ID, nil, &removed)
	if len(removed.RemovedLabelIDs) != 1 || removed.RemovedLabelIDs[0] != inGroup.ID {
		t.Fatalf("removed = %+v", removed)
	}
}

func TestSpaceMembershipEvents(t *testing.T) {
	env := newS3Env(t)
	shared := env.createSpace("shared")
	events := func() int {
		return env.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND space_id = $2::uuid AND type = 'space.changed'`, env.org, shared.ID)
	}
	before := events()
	for i, role := range []string{"VIEWER", "MEMBER"} {
		env.owner().must(http.StatusOK, http.MethodPut, spacePath(shared.ID)+"/members/"+s3MemberID, map[string]any{"role": role}, nil)
		if got := events(); got != before+i+1 {
			t.Fatalf("role %s events = %d, want %d", role, got, before+i+1)
		}
	}
	env.member().expectError(http.StatusForbidden, "", http.MethodPut, spacePath(shared.ID)+"/members/"+s3OtherID, map[string]any{"role": "MEMBER"})
	if got := events(); got != before+2 {
		t.Fatalf("rejected write emitted event: %d", got)
	}
	env.owner().must(http.StatusNoContent, http.MethodDelete, spacePath(shared.ID)+"/members/"+s3MemberID, nil, nil)
	if got := events(); got != before+3 {
		t.Fatalf("remove events = %d, want %d", got, before+3)
	}
}
