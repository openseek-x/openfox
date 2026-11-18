package stage3

import (
	"testing"
)

// 空间开关 multi_assignee 关闭时：拒绝写入多人；已有多人集合不截断，只减不增。
func TestMultiAssigneeSwitch(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.space.ID
	env.addSpaceMember(space, s3AdminID, "MEMBER")
	env.addSpaceMember(space, s3OtherID, "MEMBER")
	human := func(id string) map[string]string { return map[string]string{"kind": "HUMAN", "id": id} }
	path := spacePath(space) + "/tasks"
	type taskResp struct {
		ID        string
		Version   int
		Assignees []struct{ Kind, ID string }
	}

	// 开关打开：可建多人任务
	owner.must(200, "PATCH", "spaces/"+space, map[string]any{"multi_assignee": true, "version": env.getSpace(owner, space).Version}, nil)
	var multi taskResp
	owner.must(201, "POST", path, map[string]any{"title": "多人", "assignees": []any{human(s3MemberID), human(s3AdminID), human(s3OtherID)}}, &multi)

	// 关闭开关
	owner.must(200, "PATCH", "spaces/"+space, map[string]any{"multi_assignee": false, "version": env.getSpace(owner, space).Version}, nil)

	// 新建多人被拒；单人仍可
	owner.expectError(400, "MULTI_ASSIGNEE_DISABLED", "POST", path, map[string]any{"title": "多人2", "assignees": []any{human(s3MemberID), human(s3AdminID)}})
	owner.must(201, "POST", path, map[string]any{"title": "单人", "assignees": []any{human(s3MemberID)}}, nil)

	item := path + "/" + multi.ID
	// 已有三人：不能新增（换成另一组两人也不行）
	owner.expectError(400, "MULTI_ASSIGNEE_DISABLED", "PATCH", item, map[string]any{"version": multi.Version,
		"assignees": []any{human(s3MemberID), human(s3AdminID), human(s3OtherID), human(s3ManagerID)}})
	owner.expectError(400, "MULTI_ASSIGNEE_DISABLED", "PATCH", item, map[string]any{"version": multi.Version,
		"assignees": []any{human(s3MemberID), human(s3ManagerID)}})
	// 减少到两人可以（不截断到一人）
	var reduced taskResp
	owner.must(200, "PATCH", item, map[string]any{"version": multi.Version, "assignees": []any{human(s3MemberID), human(s3AdminID)}}, &reduced)
	if len(reduced.Assignees) != 2 {
		t.Fatalf("assignees = %+v", reduced.Assignees)
	}
	// 原样保持两人（无新增）不被拒绝
	owner.must(200, "PATCH", item, map[string]any{"version": reduced.Version, "title": "改标题", "assignees": []any{human(s3MemberID), human(s3AdminID)}}, &reduced)
	// 分拣/批量路径同语义：批量把人员处理人替换为一人属于减少
	var got taskResp
	owner.must(200, "GET", item, nil, &got)
	if len(got.Assignees) != 2 {
		t.Fatalf("after noop = %+v", got.Assignees)
	}
}
