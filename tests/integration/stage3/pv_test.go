package stage3

// 专项与视图集成测试：真实 HTTP 处理器，权限、规则、并发与图标上传。

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"testing"

	"openfox/internal/api"
	"openfox/internal/artifacts"
)

type pvProject struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Version  int    `json:"version"`
	Progress struct {
		Done  int `json:"done"`
		Total int `json:"total"`
	} `json:"progress"`
	Spaces []struct {
		ID        string `json:"id"`
		IsPrimary bool   `json:"is_primary"`
	} `json:"spaces"`
	Labels       []struct{ ID string } `json:"labels"`
	Distribution *struct {
		Assignees  []pvDist `json:"assignees"`
		Iterations []pvDist `json:"iterations"`
	} `json:"distribution"`
}

type pvDist struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

func (e *s3Env) newProject(client *s3Client, spaceID string, extra map[string]any) pvProject {
	e.t.Helper()
	body := map[string]any{"space_id": spaceID, "name": "Proj " + s3Suffix(e.t)}
	for key, value := range extra {
		body[key] = value
	}
	var project pvProject
	client.must(http.StatusCreated, http.MethodPost, "projects", body, &project)
	return project
}

func (e *s3Env) getProject(client *s3Client, id string) pvProject {
	e.t.Helper()
	var project pvProject
	client.must(http.StatusOK, http.MethodGet, "projects/"+id, nil, &project)
	return project
}

func (e *s3Env) taskProject(taskID string) string {
	var id string
	e.scalar(&id, `SELECT COALESCE(project_id::text, '') FROM openfox.tasks WHERE id = $1::uuid`, taskID)
	return id
}

func (e *s3Env) eventCount(eventType, entityID string) int {
	return e.count(`SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND type = $2 AND entity_id = $3::uuid`,
		e.org, eventType, entityID)
}

func TestProjectPermissions(t *testing.T) {
	env := newS3Env(t)
	owner, manager, member, other, guest := env.owner(), env.manager(), env.member(), env.other(), env.guest()
	spaceA := env.space.ID // manager OWNER，member MEMBER
	spaceB := env.createSpace("private").ID
	env.addSpaceMember(spaceB, s3OtherID, "MEMBER")
	viewerSpace := env.createSpace("private").ID
	env.addSpaceMember(viewerSpace, s3OtherID, "VIEWER")

	project := env.newProject(manager, spaceA, nil)
	owner.must(http.StatusNoContent, http.MethodPost, "projects/"+project.ID+"/spaces", map[string]any{"space_id": spaceB}, nil)
	patch := func(client *s3Client) s3Response {
		return client.do(http.MethodPatch, "projects/"+project.ID, map[string]any{"version": env.getProject(owner, project.ID).Version, "name": "N " + s3Suffix(t)})
	}
	for _, tc := range []struct {
		name   string
		client *s3Client
		get    int
		patch  int
		del    int
	}{
		{"primary manager", manager, 200, 200, -1},
		{"editor of primary", member, 200, 200, 403},
		{"editor of associated space", other, 200, 200, 403},
		{"non-member guest", guest, 404, -1, -1},
		{"cross tenant", env.outsider(), 404, -1, -1},
	} {
		if status := tc.client.do(http.MethodGet, "projects/"+project.ID, nil).Status; status != tc.get {
			t.Errorf("%s GET = %d, want %d", tc.name, status, tc.get)
		}
		if tc.patch > 0 {
			if status := patch(tc.client).Status; status != tc.patch {
				t.Errorf("%s PATCH = %d, want %d", tc.name, status, tc.patch)
			}
		}
		if tc.del > 0 {
			if status := tc.client.do(http.MethodDelete, fmt.Sprintf("projects/%s?version=%d", project.ID, env.getProject(owner, project.ID).Version), nil).Status; status != tc.del {
				t.Errorf("%s DELETE = %d, want %d", tc.name, status, tc.del)
			}
		}
	}
	// 只读成员（关联空间里是 VIEWER）：只有只读空间关联的专项可读不可写。
	readOnly := env.newProject(owner, viewerSpace, nil)
	other.must(http.StatusOK, http.MethodGet, "projects/"+readOnly.ID, nil, nil)
	other.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, "projects/"+readOnly.ID, map[string]any{"version": readOnly.Version, "name": "x"})
	other.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, "projects", map[string]any{"space_id": viewerSpace, "name": "x"})
	// 加空间还要求目标空间的任务写权限。
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "projects/"+project.ID+"/spaces", map[string]any{"space_id": viewerSpace})
	// 列表按空间读权限。
	guest.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "projects?space_id="+spaceA, nil)

	// 停用主空间：专项只读。
	var space SpaceView
	manager.must(http.StatusOK, http.MethodGet, "spaces/"+spaceA, nil, &space)
	manager.must(http.StatusOK, http.MethodPost, spacePath(spaceA)+"/retire", map[string]any{"version": space.Version, "open_tasks": "cancel"}, nil)
	expected := env.getProject(owner, project.ID).Version
	other.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPatch, "projects/"+project.ID, map[string]any{"version": expected, "name": "y"})
	owner.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodDelete, fmt.Sprintf("projects/%s?version=%d", project.ID, expected), nil)
	other.must(http.StatusOK, http.MethodGet, "projects/"+project.ID, nil, nil)
}

func TestProjectLifecycle(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	spaceA := env.createSpace("private").ID
	spaceB := env.createSpace("private").ID
	project := env.newProject(owner, spaceA, map[string]any{"icon": "🚀", "status": "in_progress", "priority": "high", "lead_user_id": s3MemberID})
	if project.Status != "in_progress" || project.Priority != "HIGH" || len(project.Spaces) != 1 || !project.Spaces[0].IsPrimary {
		t.Fatalf("project = %+v", project)
	}
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, "projects", map[string]any{"space_id": spaceA, "name": "  "})
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, "projects", map[string]any{"space_id": spaceA, "name": "x", "status": "nope"})
	if env.eventCount("project.changed", project.ID) == 0 {
		t.Error("project.changed event missing")
	}

	// 乐观锁。
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "projects/"+project.ID, map[string]any{"version": project.Version + 5, "name": "stale"})
	var updated pvProject
	owner.must(http.StatusOK, http.MethodPatch, "projects/"+project.ID, map[string]any{"version": project.Version, "name": "Renamed", "icon": nil, "status": "completed"}, &updated)
	if updated.Name != "Renamed" || updated.Status != "completed" || updated.Version != project.Version+1 {
		t.Fatalf("updated = %+v", updated)
	}

	// 加 / 移空间、主空间。
	space := func(id string) string { return "projects/" + project.ID + "/spaces/" + id }
	owner.expectError(http.StatusConflict, "PRIMARY_SPACE", http.MethodDelete, space(spaceA), nil)
	owner.must(http.StatusNoContent, http.MethodPost, "projects/"+project.ID+"/spaces", map[string]any{"space_id": spaceB}, nil)
	owner.must(http.StatusNoContent, http.MethodPost, "projects/"+project.ID+"/spaces", map[string]any{"space_id": spaceB}, nil) // 幂等
	task := env.createTask(owner, spaceB, map[string]any{"project_id": project.ID})
	other := env.createTask(owner, spaceA, map[string]any{"project_id": project.ID})
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, space(s3NewID(t))+"/primary", nil)
	owner.must(http.StatusNoContent, http.MethodPut, space(spaceB)+"/primary", nil, nil)
	owner.must(http.StatusNoContent, http.MethodPut, space(spaceB)+"/primary", nil, nil)
	if env.count(`SELECT count(*) FROM openfox.project_spaces WHERE project_id = $1::uuid AND is_primary`, project.ID) != 1 {
		t.Fatal("expected exactly one primary space")
	}
	// 移除非主空间：该空间里的任务清空专项并写动态。
	owner.must(http.StatusNoContent, http.MethodDelete, space(spaceA), nil, nil)
	if env.taskProject(other.ID) != "" || env.taskProject(task.ID) != project.ID {
		t.Fatal("removing a space must clear only that space's tasks")
	}
	if env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.project_changed'`, other.ID) != 1 {
		t.Error("task.project_changed activity missing")
	}
	owner.expectError(http.StatusConflict, "PRIMARY_SPACE", http.MethodDelete, space(spaceB), nil) // 最后一个空间

	// 标签：必须来自已关联空间。
	var labelB, labelA string
	env.scalar(&labelB, `INSERT INTO openfox.labels (organization_id, space_id, name) VALUES ($1::uuid, $2::uuid, 'lb') RETURNING id::text`, env.org, spaceB)
	env.scalar(&labelA, `INSERT INTO openfox.labels (organization_id, space_id, name) VALUES ($1::uuid, $2::uuid, 'la') RETURNING id::text`, env.org, spaceA)
	owner.expectError(http.StatusBadRequest, "", http.MethodPut, "projects/"+project.ID+"/labels", map[string]any{"label_ids": []string{labelA}})
	owner.must(http.StatusNoContent, http.MethodPut, "projects/"+project.ID+"/labels", map[string]any{"label_ids": []string{labelB, labelB}}, nil)
	if got := env.getProject(owner, project.ID); len(got.Labels) != 1 || got.Labels[0].ID != labelB {
		t.Fatalf("labels = %+v", got.Labels)
	}
	owner.must(http.StatusNoContent, http.MethodPut, "projects/"+project.ID+"/labels", map[string]any{"label_ids": []string{}}, nil)

	// 列表。
	var list struct {
		Projects   []pvProject `json:"projects"`
		NextCursor string      `json:"next_cursor"`
	}
	second := env.newProject(owner, spaceB, nil)
	owner.must(http.StatusOK, http.MethodGet, "projects?space_id="+spaceB+"&limit=1", nil, &list)
	if len(list.Projects) != 1 || list.Projects[0].ID != second.ID || list.NextCursor == "" {
		t.Fatalf("page 1 = %+v", list)
	}
	owner.must(http.StatusOK, http.MethodGet, "projects?space_id="+spaceB+"&limit=1&cursor="+url.QueryEscape(list.NextCursor), nil, &list)
	if len(list.Projects) != 1 || list.Projects[0].ID != project.ID || list.NextCursor != "" {
		t.Fatalf("page 2 = %+v", list)
	}
	owner.must(http.StatusOK, http.MethodGet, "projects?space_id="+spaceB+"&status=planned", nil, &list)
	if len(list.Projects) != 1 || list.Projects[0].ID != second.ID {
		t.Fatalf("status filter = %+v", list)
	}
	owner.expectError(http.StatusBadRequest, "", http.MethodGet, "projects?space_id="+spaceB+"&cursor=bad", nil)

	// 删除：先看影响，再带版本删除；任务清空并写动态，事件带 deleted。
	var impact struct {
		TaskCount int `json:"task_count"`
	}
	owner.must(http.StatusOK, http.MethodGet, "projects/"+project.ID+"/delete-impact", nil, &impact)
	if impact.TaskCount != 1 {
		t.Fatalf("impact = %+v", impact)
	}
	current := env.getProject(owner, project.ID)
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodDelete, fmt.Sprintf("projects/%s?version=%d", project.ID, current.Version-1), nil)
	owner.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("projects/%s?version=%d", project.ID, current.Version), nil, nil)
	if env.taskProject(task.ID) != "" {
		t.Error("task project_id must be cleared on delete")
	}
	if env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.project_changed'`, task.ID) != 1 {
		t.Error("delete must write task.project_changed")
	}
	if env.count(`SELECT count(*) FROM openfox.organization_events WHERE type = 'project.changed' AND entity_id = $1::uuid AND payload->>'deleted' = 'true'`, project.ID) != 1 {
		t.Error("deleted project.changed event missing")
	}
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "projects/"+project.ID, nil)
}

func TestProjectTasksProgressAndFilters(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	space := env.createSpace("private").ID
	project := env.newProject(owner, space, nil)
	done, todo := env.statusID(env.org, "DONE"), env.statusID(env.org, "TODO")
	a := env.createTask(owner, space, map[string]any{"project_id": project.ID, "status_id": done, "assignee_user_id": s3OwnerID})
	b := env.createTask(owner, space, map[string]any{"project_id": project.ID, "status_id": todo, "assignee_user_id": s3OwnerID})
	env.createTask(owner, space, map[string]any{"project_id": project.ID, "status_id": env.statusID(env.org, "CANCELLED")})
	c := env.createTask(owner, space, map[string]any{"status_id": todo})

	got := env.getProject(owner, project.ID)
	if got.Progress.Done != 1 || got.Progress.Total != 2 {
		t.Fatalf("progress = %+v", got.Progress)
	}
	byKey := map[string]pvDist{}
	for _, item := range got.Distribution.Assignees {
		byKey[item.Key] = item
	}
	if item := byKey[s3OwnerID]; item.Done != 1 || item.Total != 2 || item.Label == "" {
		t.Errorf("assignee distribution = %+v", got.Distribution.Assignees)
	}
	if len(got.Distribution.Iterations) != 1 || got.Distribution.Iterations[0].Key != "" || got.Distribution.Iterations[0].Total != 2 {
		t.Errorf("iteration distribution = %+v", got.Distribution.Iterations)
	}

	ids := func(query string) []string {
		var body struct {
			Tasks []TaskView `json:"tasks"`
		}
		owner.must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks?"+query, nil, &body)
		out := make([]string, 0, len(body.Tasks))
		for _, task := range body.Tasks {
			out = append(out, task.ID)
		}
		return out
	}
	other := env.newProject(owner, space, nil)
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"project_id=" + project.ID, 3},
		{"project_id=none", 1},
		{"project_id=" + other.ID, 0},
		{"project_id_not=" + project.ID, 1}, // 含没有专项的任务（NULL）
		{"project_id_not=none", 3},
		{"project_id=" + project.ID + "&project_id=none", 4},
	} {
		if got := ids(tc.query); len(got) != tc.want {
			t.Errorf("%s = %d tasks, want %d", tc.query, len(got), tc.want)
		}
	}
	owner.expectError(http.StatusBadRequest, "INVALID_FILTER", http.MethodGet, spacePath(space)+"/tasks?project_id=bogus", nil)
	if got := ids("sort=project&order=asc"); got[len(got)-1] != c.ID {
		t.Errorf("sort=project asc must put tasks without project last: %v", got)
	}
	if got := ids("sort=project&order=desc"); got[len(got)-1] != c.ID {
		t.Errorf("sort=project desc must put tasks without project last: %v", got)
	}
	var page struct {
		Next string `json:"next_cursor"`
	}
	owner.must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks?sort=project&limit=2", nil, &page)
	if len(ids("sort=project&limit=2&cursor="+url.QueryEscape(page.Next))) != 2 {
		t.Error("project sort cursor paging failed")
	}

	// 校验：专项必须关联任务所在空间；空间关闭专项后拒绝；PATCH 清空写动态。
	otherSpace := env.createSpace("private").ID
	owner.expectError(http.StatusBadRequest, "INVALID_PROJECT", http.MethodPost, spacePath(otherSpace)+"/tasks", map[string]any{"title": "x", "project_id": project.ID})
	owner.expectError(http.StatusBadRequest, "INVALID_PROJECT", http.MethodPatch, spacePath(space)+"/tasks/"+c.ID, map[string]any{"version": estTask(env, space, c.ID).Version, "project_id": s3NewID(t)})
	var patched TaskView
	owner.must(http.StatusOK, http.MethodPatch, spacePath(space)+"/tasks/"+b.ID, map[string]any{"version": estTask(env, space, b.ID).Version, "project_id": nil}, &patched)
	if env.taskProject(b.ID) != "" || env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.project_changed'`, b.ID) != 1 {
		t.Error("PATCH project_id=null must clear and log")
	}
	_ = a
	var current SpaceView
	owner.must(http.StatusOK, http.MethodGet, "spaces/"+space, nil, &current)
	owner.must(http.StatusOK, http.MethodPatch, "spaces/"+space, map[string]any{"version": current.Version, "features": map[string]any{"projects": false}}, nil)
	owner.expectError(http.StatusBadRequest, "PROJECTS_DISABLED", http.MethodPost, spacePath(space)+"/tasks", map[string]any{"title": "x", "project_id": project.ID})
	owner.expectError(http.StatusBadRequest, "PROJECTS_DISABLED", http.MethodPost, "projects", map[string]any{"space_id": space, "name": "x"})
}

func TestProjectConcurrency(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	spaces := []string{env.createSpace("private").ID, env.createSpace("private").ID, env.createSpace("private").ID}
	project := env.newProject(owner, spaces[0], nil)
	for _, id := range spaces[1:] {
		owner.must(http.StatusNoContent, http.MethodPost, "projects/"+project.ID+"/spaces", map[string]any{"space_id": id}, nil)
	}
	statuses := parallel(2, func(index int) int {
		return owner.do(http.MethodPut, "projects/"+project.ID+"/spaces/"+spaces[index+1]+"/primary", nil).Status
	})
	if countStatus(statuses, http.StatusNoContent) != 2 || env.count(`SELECT count(*) FROM openfox.project_spaces WHERE project_id = $1::uuid AND is_primary`, project.ID) != 1 {
		t.Fatalf("concurrent set primary: statuses %v", statuses)
	}
	version := env.getProject(owner, project.ID).Version
	statuses = parallel(2, func(int) int {
		return owner.do(http.MethodDelete, fmt.Sprintf("projects/%s?version=%d", project.ID, version), nil).Status
	})
	if countStatus(statuses, http.StatusNoContent) != 1 || countStatus(statuses, http.StatusNotFound) != 1 {
		t.Fatalf("concurrent delete statuses = %v", statuses)
	}
}

type pvView struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Visibility     string         `json:"visibility"`
	IconKind       *string        `json:"icon_kind"`
	Icon           *string        `json:"icon"`
	IconURL        *string        `json:"icon_url"`
	Filters        map[string]any `json:"filters"`
	Pinned         bool           `json:"pinned"`
	Position       *int           `json:"position"`
	MemberViewMode *string        `json:"member_view_mode"`
	CanEdit        bool           `json:"can_edit"`
	Version        int            `json:"version"`
}

func (e *s3Env) viewList(client *s3Client, spaceID string) []pvView {
	e.t.Helper()
	var body struct {
		Views []pvView `json:"views"`
	}
	client.must(http.StatusOK, http.MethodGet, spacePath(spaceID)+"/views", nil, &body)
	return body.Views
}

func TestViews(t *testing.T) {
	env := newS3Env(t)
	owner, manager, member := env.owner(), env.manager(), env.member()
	space := env.space.ID // manager OWNER，member MEMBER
	viewer := env.other()
	env.addSpaceMember(space, s3OtherID, "VIEWER")
	todo, done := env.statusID(env.org, "TODO"), env.statusID(env.org, "DONE")
	t1 := env.createTask(owner, space, map[string]any{"status_id": todo, "priority": "HIGH", "title": "alpha"})
	t2 := env.createTask(owner, space, map[string]any{"status_id": todo, "priority": "LOW", "title": "beta"})
	t3 := env.createTask(owner, space, map[string]any{"status_id": done, "priority": "HIGH", "title": "gamma"})

	var mine pvView
	member.must(http.StatusCreated, http.MethodPost, spacePath(space)+"/views", map[string]any{
		"name": "Mine", "filters": map[string]any{"priority": []string{"HIGH"}}, "display": map[string]any{"layout": "board"},
		"icon_kind": "emoji", "icon": "🔥"}, &mine)
	if mine.Visibility != "personal" || !mine.CanEdit || mine.IconKind == nil || *mine.IconKind != "emoji" {
		t.Fatalf("view = %+v", mine)
	}
	var shared pvView
	manager.must(http.StatusCreated, http.MethodPost, spacePath(space)+"/views", map[string]any{"name": "Shared", "visibility": "space", "filters": map[string]any{"status_id": []string{todo}}}, &shared)
	viewer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, spacePath(space)+"/views", map[string]any{"name": "x"})

	// 校验：非法 filters / 图标 / 可见性。
	for _, body := range []map[string]any{
		{"name": "x", "filters": map[string]any{"bogus": "1"}},
		{"name": "x", "filters": map[string]any{"status_id": []string{"not-a-uuid"}}},
		{"name": "x", "filters": map[string]any{"sort": "created"}},
	} {
		member.expectError(http.StatusBadRequest, "INVALID_VIEW_FILTERS", http.MethodPost, spacePath(space)+"/views", body)
	}
	member.expectError(http.StatusBadRequest, "", http.MethodPost, spacePath(space)+"/views", map[string]any{"name": "x", "icon_kind": "upload", "icon": "nope"})
	member.expectError(http.StatusBadRequest, "", http.MethodPost, spacePath(space)+"/views", map[string]any{"name": "x", "visibility": "public"})

	// 可见性：personal 对他人不可见（列表、GET、tasks?view_id= 都 404）。
	if got := env.viewList(manager, space); len(got) != 1 || got[0].ID != shared.ID {
		t.Fatalf("manager list = %+v", got)
	}
	if got := env.viewList(member, space); len(got) != 2 {
		t.Fatalf("member list = %+v", got)
	}
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "views/"+mine.ID, nil)
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space)+"/tasks?view_id="+mine.ID, nil)
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, "views/"+mine.ID, map[string]any{"version": mine.Version, "name": "x"})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, "views/"+mine.ID+"/pin-mine", map[string]any{"pinned": true})
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "views/"+shared.ID, nil)
	env.guest().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "views/"+shared.ID, nil)

	// 编辑权限：space 视图创建者或管理者；可见性只有创建者能改。
	member.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version, "name": "x"})
	owner.must(http.StatusOK, http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version, "name": "By admin"}, &shared)
	owner.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version, "visibility": "personal"})
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version - 1, "name": "x"})
	manager.must(http.StatusOK, http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version, "visibility": "personal", "icon_kind": nil}, &shared)
	if shared.Visibility != "personal" || shared.IconKind != nil {
		t.Fatalf("shared = %+v", shared)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "views/"+shared.ID, nil)
	manager.must(http.StatusOK, http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version, "visibility": "space"}, &shared)
	if owner.do(http.MethodGet, "views/"+shared.ID, nil).Status != http.StatusOK {
		t.Fatal("space view must be visible to others")
	}
	if got := env.viewList(owner, space); len(got) != 1 || !got[0].CanEdit {
		t.Fatalf("admin list = %+v", got)
	}
	for _, item := range env.viewList(member, space) {
		if item.CanEdit != (item.ID == mine.ID) {
			t.Fatalf("member can_edit flags wrong: %+v", item)
		}
	}

	// 打开视图：view_id 与请求参数合并（AND）。
	ids := func(query string) map[string]bool {
		var body struct {
			Tasks []TaskView `json:"tasks"`
		}
		member.must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks?"+query, nil, &body)
		out := map[string]bool{}
		for _, task := range body.Tasks {
			out[task.ID] = true
		}
		return out
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"view_id=" + mine.ID, []string{t1.ID, t3.ID}},
		{"view_id=" + shared.ID, []string{t1.ID, t2.ID}},
		{"view_id=" + mine.ID + "&status_id=" + done, []string{t3.ID}},
	} {
		got := ids(tc.query)
		if len(got) != len(tc.want) {
			t.Errorf("%s = %v, want %v", tc.query, got, tc.want)
			continue
		}
		for _, id := range tc.want {
			if !got[id] {
				t.Errorf("%s missing %s", tc.query, id)
			}
		}
	}
	var counts struct {
		Total int `json:"total"`
	}
	member.must(http.StatusOK, http.MethodGet, spacePath(space)+"/tasks/counts?view_id="+mine.ID, nil, &counts)
	if counts.Total != 2 {
		t.Errorf("counts with view = %d", counts.Total)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, spacePath(space)+"/tasks?view_id="+s3NewID(t), nil)

	// 个人偏好只影响本人。
	member.must(http.StatusNoContent, http.MethodPut, "views/"+shared.ID+"/pin-mine", map[string]any{"pinned": true}, nil)
	member.must(http.StatusNoContent, http.MethodPut, "views/"+shared.ID+"/view-mode-mine", map[string]any{"view_mode": "table"}, nil)
	member.expectError(http.StatusBadRequest, "", http.MethodPut, "views/"+shared.ID+"/view-mode-mine", map[string]any{"view_mode": "grid"})
	for _, item := range env.viewList(member, space) {
		if item.ID == shared.ID && (!item.Pinned || item.MemberViewMode == nil || *item.MemberViewMode != "table") {
			t.Fatalf("member prefs = %+v", item)
		}
	}
	if item := env.viewList(owner, space)[0]; item.Pinned || item.MemberViewMode != nil {
		t.Fatalf("prefs leaked to another user: %+v", item)
	}
	member.must(http.StatusNoContent, http.MethodPut, "views/"+shared.ID+"/view-mode-mine", map[string]any{"view_mode": nil}, nil)

	// 排序：只写本人的 position；列表按 position 排。
	member.must(http.StatusNoContent, http.MethodPut, spacePath(space)+"/views/reorder-mine", map[string]any{"view_ids": []string{shared.ID, mine.ID}}, nil)
	if got := env.viewList(member, space); got[0].ID != shared.ID || got[0].Position == nil || *got[0].Position != 0 || *got[1].Position != 1 {
		t.Fatalf("reordered = %+v", got)
	}
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, spacePath(space)+"/views/reorder-mine", map[string]any{"view_ids": []string{shared.ID, s3NewID(t)}})
	member.expectError(http.StatusBadRequest, "", http.MethodPut, spacePath(space)+"/views/reorder-mine", map[string]any{"view_ids": []string{mine.ID, mine.ID}})
	if got := env.viewList(owner, space); got[0].Position != nil {
		t.Fatalf("position leaked to another user: %+v", got[0])
	}

	// 停用空间只读；关闭视图功能后写入 400。
	member.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodDelete, fmt.Sprintf("views/%s?version=%d", shared.ID, shared.Version), nil)
	member.must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("views/%s?version=%d", mine.ID, mine.Version), nil, nil)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "views/"+mine.ID, nil)
	var current SpaceView
	owner.must(http.StatusOK, http.MethodGet, "spaces/"+space, nil, &current)
	owner.must(http.StatusOK, http.MethodPatch, "spaces/"+space, map[string]any{"version": current.Version, "features": map[string]any{"views": false}}, nil)
	member.expectError(http.StatusBadRequest, "VIEWS_DISABLED", http.MethodPost, spacePath(space)+"/views", map[string]any{"name": "x"})
	owner.must(http.StatusOK, http.MethodPatch, "spaces/"+space, map[string]any{"version": current.Version + 1, "features": map[string]any{"views": true}}, nil)
	owner.must(http.StatusOK, http.MethodGet, "spaces/"+space, nil, &current)
	manager.must(http.StatusOK, http.MethodPost, spacePath(space)+"/retire", map[string]any{"version": current.Version, "open_tasks": "cancel"}, nil)
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPatch, "views/"+shared.ID, map[string]any{"version": shared.Version, "name": "late"})
	manager.expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, spacePath(space)+"/views", map[string]any{"name": "late"})
	if env.eventCount("view.changed", shared.ID) == 0 {
		t.Error("view.changed event missing")
	}
}

var pvPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func pvUpload(client *s3Client, server *httptest.Server, org, filename, partType string, data []byte) s3Response {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", partType)
	part, _ := writer.CreatePart(header)
	_, _ = part.Write(data)
	_ = writer.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/organizations/"+org+"/icons", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", client.csrf)
	response, err := client.http.Do(request)
	if err != nil {
		client.env.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return s3Response{Status: response.StatusCode, Header: response.Header, Body: payload}
}

func TestViewIcons(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	// 未配置存储（env.server 没有存储）：503。
	if response := pvUpload(owner, env.server, env.org, "a.png", "image/png", pvPNG); response.Status != http.StatusServiceUnavailable || response.code() != "STORAGE_UNAVAILABLE" {
		t.Fatalf("no storage = %d %s", response.Status, response.Body)
	}
	server := httptest.NewServer(api.NewWithDispatch(env.auth, env.database, false, artifacts.Local{Root: t.TempDir()}, api.DefaultAuthRateLimits, api.DispatchOptions{}))
	t.Cleanup(server.Close)
	large := append(append([]byte(nil), pvPNG...), make([]byte, 1<<20)...)
	for _, tc := range []struct {
		name, filename, partType string
		data                     []byte
		status                   int
		code                     string
	}{
		{"png", "a.png", "image/png", pvPNG, http.StatusCreated, ""},
		{"svg", "a.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`), http.StatusBadRequest, "INVALID_ICON"},
		{"disguised svg", "a.png", "image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), http.StatusBadRequest, "INVALID_ICON"},
		{"html", "a.png", "image/png", []byte("<html><script>1</script></html>"), http.StatusBadRequest, "INVALID_ICON"},
		{"too large", "a.png", "image/png", large, http.StatusRequestEntityTooLarge, "ICON_TOO_LARGE"},
		{"empty", "a.png", "image/png", nil, http.StatusBadRequest, "INVALID_ICON"},
	} {
		response := pvUpload(owner, server, env.org, tc.filename, tc.partType, tc.data)
		if response.Status != tc.status || (tc.code != "" && response.code() != tc.code) {
			t.Errorf("%s = %d %s", tc.name, response.Status, response.Body)
		}
	}
	var icon struct {
		ID          string `json:"id"`
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
	}
	pvUpload(owner, server, env.org, "b.png", "text/plain", pvPNG).decode(t, &icon)
	if icon.ContentType != "image/png" {
		t.Fatalf("content type must come from sniffing: %+v", icon)
	}

	get := func(client *s3Client, org string) s3Response {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/organizations/"+org+"/icons/"+icon.ID, nil)
		response, err := client.http.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		return s3Response{Status: response.StatusCode, Header: response.Header, Body: payload}
	}
	response := get(env.member(), env.org)
	if response.Status != http.StatusOK || !bytes.Equal(response.Body, pvPNG) {
		t.Fatalf("get icon = %d", response.Status)
	}
	for key, want := range map[string]string{"Content-Type": "image/png", "X-Content-Type-Options": "nosniff",
		"Cache-Control": "private, max-age=86400", "Content-Security-Policy": "default-src 'none'"} {
		if got := response.Header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if status := get(env.outsider(), env.org).Status; status != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d", status)
	}
	if status := get(env.outsider(), env.otherOrg).Status; status != http.StatusNotFound {
		t.Errorf("other org lookup = %d", status)
	}
	if env.client(s3OutsiderID).do(http.MethodGet, "/v1/organizations/"+env.otherOrg+"/icons/"+s3NewID(t), nil).Status != http.StatusNotFound {
		t.Error("unknown icon must be 404")
	}

	// 视图引用上传的图标：icon_url 指向图标，引用不存在的图标 400。
	var view pvView
	owner.must(http.StatusCreated, http.MethodPost, spacePath(env.space.ID)+"/views", map[string]any{"name": "Icon", "icon_kind": "upload", "icon": icon.ID}, &view)
	if view.IconURL == nil || *view.IconURL != icon.URL {
		t.Fatalf("icon_url = %v", view.IconURL)
	}
	owner.expectError(http.StatusBadRequest, "", http.MethodPost, spacePath(env.space.ID)+"/views", map[string]any{"name": "Bad", "icon_kind": "upload", "icon": s3NewID(t)})
}

func TestProjectDeletedSpaceAndOrgLabel(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()
	spaceA, spaceB, spaceC := env.createSpace("private"), env.createSpace("private"), env.createSpace("private")
	shared := env.newProject(owner, spaceA.ID, nil)
	owner.must(http.StatusNoContent, http.MethodPost, "projects/"+shared.ID+"/spaces", map[string]any{"space_id": spaceB.ID}, nil)
	only := env.newProject(owner, spaceC.ID, nil)

	remove := func(space SpaceView) {
		current := env.getSpace(owner, space.ID)
		owner.must(http.StatusOK, http.MethodDelete, spacePath(space.ID), map[string]any{"confirm_name": current.Name, "version": current.Version}, nil)
	}
	remove(spaceA)
	remove(spaceC)

	// 已删除空间从关联里消失（详情、列表、spaces 都不返回），专项仍可经其他空间访问。
	if got := env.getProject(owner, shared.ID); len(got.Spaces) != 1 || got.Spaces[0].ID != spaceB.ID {
		t.Fatalf("detail spaces = %+v", got.Spaces)
	}
	var spaces struct {
		Spaces []struct{ ID string } `json:"spaces"`
	}
	owner.must(http.StatusOK, http.MethodGet, "projects/"+shared.ID+"/spaces", nil, &spaces)
	if len(spaces.Spaces) != 1 || spaces.Spaces[0].ID != spaceB.ID {
		t.Fatalf("spaces = %+v", spaces.Spaces)
	}
	var list struct {
		Projects []pvProject `json:"projects"`
	}
	owner.must(http.StatusOK, http.MethodGet, "projects?space_id="+spaceB.ID, nil, &list)
	if len(list.Projects) != 1 || len(list.Projects[0].Spaces) != 1 {
		t.Fatalf("list = %+v", list.Projects)
	}
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "projects?space_id="+spaceA.ID, nil)
	// 只关联到已删除空间的专项：对任何人 404。
	for _, client := range []*s3Client{owner, env.admin(), env.manager()} {
		client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "projects/"+only.ID, nil)
		client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, "projects/"+only.ID, map[string]any{"version": only.Version, "name": "x"})
	}

	// 组织级标签（space_id 为空）：按现有实现不属于任何已关联空间的限制，允许加到专项上。
	var orgLabel string
	env.scalar(&orgLabel, `INSERT INTO openfox.labels (organization_id, space_id, name) VALUES ($1::uuid, NULL, 'org-wide') RETURNING id::text`, env.org)
	owner.must(http.StatusNoContent, http.MethodPut, "projects/"+shared.ID+"/labels", map[string]any{"label_ids": []string{orgLabel}}, nil)
	if got := env.getProject(owner, shared.ID); len(got.Labels) != 1 || got.Labels[0].ID != orgLabel {
		t.Fatalf("labels = %+v", got.Labels)
	}
}
