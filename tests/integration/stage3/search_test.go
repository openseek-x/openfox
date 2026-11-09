package stage3

// 全局搜索（GET /v1/organizations/{org}/search）：每类都能搜到；权限与各模块自己的读规则一致。
// （看不到的私有空间任务 / 视图 / 文档、私有 Agent 不出现）；跨组织隔离；% _ 转义；limit 上限；参数校验。

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

type searchItemView struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Subtitle         string     `json:"subtitle"`
	SpaceID          string     `json:"space_id"`
	DisplayID        string     `json:"display_id"`
	ConversationID   string     `json:"conversation_id"`
	ConversationKind string     `json:"conversation_kind"`
	TaskDisplayID    string     `json:"task_display_id"`
	MessageID        string     `json:"message_id"`
	MessageSeq       int64      `json:"message_seq"`
	OccurredAt       *time.Time `json:"occurred_at"`
	SenderKind       string     `json:"sender_kind"`
	SenderID         string     `json:"sender_id"`
	SenderName       string     `json:"sender_name"`
}

type searchResultView struct {
	Groups []struct {
		Type  string           `json:"type"`
		Items []searchItemView `json:"items"`
		Total *int             `json:"total"`
	} `json:"groups"`
}

// items 返回某类的结果；缺少该类时让用例失败。
func (r searchResultView) items(t *testing.T, kind string) []searchItemView {
	t.Helper()
	for _, group := range r.Groups {
		if group.Type == kind {
			if group.Items == nil {
				t.Fatalf("group %s items is null, want []", kind)
			}
			return group.Items
		}
	}
	t.Fatalf("group %s missing in %+v", kind, r.Groups)
	return nil
}

func (r searchResultView) has(kind, id string) bool {
	for _, group := range r.Groups {
		if group.Type != kind {
			continue
		}
		for _, item := range group.Items {
			if item.ID == id {
				return true
			}
		}
	}
	return false
}

func (r searchResultView) total() int {
	total := 0
	for _, group := range r.Groups {
		total += len(group.Items)
	}
	return total
}

func searchPath(query string, extra ...string) string {
	path := "search?q=" + neturl.QueryEscape(query)
	for _, part := range extra {
		path += "&" + part
	}
	return path
}

func (e *s3Env) search(client *s3Client, query string, extra ...string) searchResultView {
	e.t.Helper()
	// 默认每类只有 5 条；除非用例自己指定 limit，这里放宽到 20，避免同一关键词的多条数据互相挤掉。
	hasLimit := false
	for _, part := range extra {
		hasLimit = hasLimit || strings.HasPrefix(part, "limit=")
	}
	if !hasLimit {
		extra = append(extra[:len(extra):len(extra)], "limit=20")
	}
	var result searchResultView
	client.must(http.StatusOK, http.MethodGet, searchPath(query, extra...), nil, &result)
	return result
}

func TestGlobalSearch(t *testing.T) {
	env := newS3Env(t)
	owner, manager, member, other, guest, outsider := env.owner(), env.manager(), env.member(), env.other(), env.guest(), env.outsider()
	token := "Zq" + s3Suffix(t)
	privateSpace := env.space.ID // manager OWNER、member MEMBER；other / guest 不是成员
	sharedSpace := env.createSpace("shared").ID
	env.exec(`UPDATE openfox.spaces SET name = $2 WHERE id = $1::uuid`, privateSpace, "Private "+token)
	env.exec(`UPDATE openfox.spaces SET name = $2 WHERE id = $1::uuid`, sharedSpace, "Shared "+token)

	// ---- 任务 ----
	secretTask := env.createTask(owner, privateSpace, map[string]any{"title": token + " secret task"})
	openTask := env.createTask(owner, sharedSpace, map[string]any{"title": token + " open task"})
	pctTask := env.createTask(owner, sharedSpace, map[string]any{"title": token + " 100% sure"})
	env.createTask(owner, sharedSpace, map[string]any{"title": token + " 100x sure"})
	underTask := env.createTask(owner, sharedSpace, map[string]any{"title": token + " a_b"})
	env.createTask(owner, sharedSpace, map[string]any{"title": token + " axb"})
	slashTask := env.createTask(owner, sharedSpace, map[string]any{"title": token + ` back\slash`})
	// 另一个组织里有同样的关键词：任何人都不能从本组织搜到。
	if response := outsider.doIn(env.otherOrg, http.MethodPost, "spaces/"+env.otherSpace.ID+"/tasks",
		map[string]any{"title": token + " foreign task"}); response.Status != http.StatusCreated {
		t.Fatalf("create foreign task = %d %s", response.Status, response.Body)
	}

	// ---- 专项 / 视图 ----
	secretProject := env.newProject(owner, privateSpace, map[string]any{"name": token + " secret project"})
	openProject := env.newProject(owner, sharedSpace, map[string]any{"name": token + " open project"})
	var spaceView, personalView struct {
		ID string `json:"id"`
	}
	manager.must(http.StatusCreated, http.MethodPost, spacePath(privateSpace)+"/views",
		map[string]any{"name": token + " space view", "visibility": "space", "filters": map[string]any{}}, &spaceView)
	member.must(http.StatusCreated, http.MethodPost, spacePath(privateSpace)+"/views",
		map[string]any{"name": token + " personal view", "visibility": "personal", "filters": map[string]any{}}, &personalView)
	var openView struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, spacePath(sharedSpace)+"/views",
		map[string]any{"name": token + " open view", "visibility": "space", "filters": map[string]any{}}, &openView)

	// ---- 文档 ----
	secretSet := env.newDocSet(manager, privateSpace, "set")
	secretDoc := env.docNodeIn(manager, privateSpace, docInSet(secretSet.ID), "", "doc", token+" secret doc")
	openSet := env.newDocSet(owner, sharedSpace, "open set")
	openDoc := env.docNodeIn(owner, sharedSpace, docInSet(openSet.ID), "", "doc", token+" open doc")
	folder := env.docNodeIn(owner, sharedSpace, docInSet(openSet.ID), "", "folder", token+" folder")
	// 我的文档只对本人可见
	myDoc := env.docNodeIn(other, sharedSpace, docPersonal(), "", "doc", token+" my private doc")

	// ---- Agent：共享空间里一个 SHARED、一个 PRIVATE（owner 名下）、一个已归档、一个在私有空间 ----
	deviceID := env.ensureDevice("ORGANIZATION", "")
	newAgent := func(space, visibility, name string) string {
		id := s3NewID(t)
		env.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, 1)`,
			env.org, id, deviceID, space, s3OwnerID, name, visibility)
		return id
	}
	sharedAgent := newAgent(sharedSpace, "SHARED", token+" shared agent")
	privateAgent := newAgent(sharedSpace, "PRIVATE", token+" private agent")
	secretSpaceAgent := newAgent(privateSpace, "SHARED", token+" secret space agent")
	archivedAgent := newAgent(sharedSpace, "SHARED", token+" archived agent")
	env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE id = $1::uuid`, archivedAgent)

	t.Run("每一类都能搜到", func(t *testing.T) {
		result := env.search(owner, token)
		for kind, id := range map[string]string{
			"task": openTask.ID, "project": openProject.ID, "view": openView.ID, "agent": sharedAgent, "doc": openDoc.ID,
		} {
			if !result.has(kind, id) {
				t.Fatalf("%s %s missing: %+v", kind, id, result)
			}
		}
		// 空间：按名称命中，标识符放在 subtitle，空间的 space_id 是自己
		spaces := env.search(owner, token, "types=space").items(t, "space")
		if len(spaces) != 2 || spaces[0].SpaceID != spaces[0].ID {
			t.Fatalf("spaces = %+v", spaces)
		}
		// 人员：按邮箱、不分大小写
		people := env.search(owner, strings.ToUpper(s3Email(s3MemberID)), "types=person").items(t, "person")
		if len(people) != 1 || people[0].ID != s3MemberID || people[0].Subtitle != s3Email(s3MemberID) {
			t.Fatalf("people = %+v", people)
		}
		// 任务条目带编号、空间与空间名
		task := env.search(owner, "open task", "types=task").items(t, "task")
		if len(task) != 1 || task[0].DisplayID != openTask.DisplayID || task[0].SpaceID != sharedSpace || task[0].Subtitle != "Shared "+token {
			t.Fatalf("task item = %+v", task)
		}
		// 文件夹不出现在文档类里
		if result.has("doc", folder.ID) {
			t.Fatalf("folder leaked into docs: %+v", result)
		}
	})

	t.Run("权限与读规则一致", func(t *testing.T) {
		// other：组织成员，不在私有空间；可读共享空间
		result := env.search(other, token)
		for _, hidden := range []struct{ kind, id string }{
			{"task", secretTask.ID}, {"project", secretProject.ID}, {"view", spaceView.ID}, {"view", personalView.ID},
			{"doc", secretDoc.ID}, {"agent", privateAgent}, {"agent", secretSpaceAgent}, {"agent", archivedAgent},
		} {
			if result.has(hidden.kind, hidden.id) {
				t.Fatalf("other can see %s %s: %+v", hidden.kind, hidden.id, result)
			}
		}
		for _, shown := range []struct{ kind, id string }{
			{"task", openTask.ID}, {"project", openProject.ID}, {"view", openView.ID}, {"doc", openDoc.ID}, {"agent", sharedAgent}, {"doc", myDoc.ID},
		} {
			if !result.has(shown.kind, shown.id) {
				t.Fatalf("other cannot see %s %s: %+v", shown.kind, shown.id, result)
			}
		}
		if spaces := result.items(t, "space"); len(spaces) != 1 || spaces[0].ID != sharedSpace {
			t.Fatalf("other spaces = %+v", spaces)
		}
		// 他人的「我的文档」、他人的私人视图不可见
		if env.search(member, token).has("doc", myDoc.ID) {
			t.Fatalf("member can see other's personal doc")
		}
		if env.search(manager, token).has("view", personalView.ID) {
			t.Fatalf("manager can see member's personal view")
		}
		if !env.search(member, token).has("view", personalView.ID) {
			t.Fatalf("member cannot see own personal view")
		}
		// 私有空间成员可以看到私有空间里的内容
		result = env.search(member, token)
		for _, shown := range []struct{ kind, id string }{
			{"task", secretTask.ID}, {"project", secretProject.ID}, {"view", spaceView.ID}, {"doc", secretDoc.ID}, {"agent", secretSpaceAgent},
		} {
			if !result.has(shown.kind, shown.id) {
				t.Fatalf("member cannot see %s %s: %+v", shown.kind, shown.id, result)
			}
		}
		// 私有 Agent 只有本人（owner）可见
		if !env.search(owner, token).has("agent", privateAgent) || env.search(manager, token).has("agent", privateAgent) {
			t.Fatalf("private agent visibility wrong")
		}
		// 访客：非私有空间成员，共享空间对访客不可读 → 空间内容都不出现
		result = env.search(guest, token)
		for _, group := range result.Groups {
			if group.Type != "person" && len(group.Items) != 0 {
				t.Fatalf("guest sees %s: %+v", group.Type, group.Items)
			}
		}
		// 专项：关联空间都不可读时不出现；关联了可读空间就出现
		env.must200Project(t, owner, secretProject.ID, sharedSpace)
		if !env.search(other, token).has("project", secretProject.ID) {
			t.Fatalf("project linked to a readable space should be visible")
		}
	})

	t.Run("空间筛选", func(t *testing.T) {
		result := env.search(member, token, "space_id="+privateSpace)
		if !result.has("task", secretTask.ID) || result.has("task", openTask.ID) || result.has("doc", openDoc.ID) || result.has("agent", sharedAgent) {
			t.Fatalf("space filter result = %+v", result)
		}
		if spaces := result.items(t, "space"); len(spaces) != 1 || spaces[0].ID != privateSpace {
			t.Fatalf("space group = %+v", spaces)
		}
		// 不可读的空间：全部为空，也不通过人员类泄露该空间的成员
		hidden := env.search(other, "s3-", "space_id="+privateSpace)
		if hidden.total() != 0 {
			t.Fatalf("unreadable space filter leaked: %+v", hidden)
		}
	})

	t.Run("编号精确匹配排最前", func(t *testing.T) {
		// 一个标题里写着另一个任务编号的任务，更新更晚，但编号精确命中的任务仍排最前。
		env.createTask(owner, sharedSpace, map[string]any{"title": "ref " + openTask.DisplayID + " in title"})
		for _, query := range []string{openTask.DisplayID, strings.ToLower(openTask.DisplayID)} {
			tasks := env.search(owner, query, "types=task").items(t, "task")
			if len(tasks) < 2 || tasks[0].ID != openTask.ID {
				t.Fatalf("q=%s tasks = %+v", query, tasks)
			}
		}
	})

	t.Run("通配符按字面量", func(t *testing.T) {
		tasks := env.search(owner, token+" 100%", "types=task").items(t, "task")
		if len(tasks) != 1 || tasks[0].ID != pctTask.ID {
			t.Fatalf("%% escape: %+v", tasks)
		}
		if tasks := env.search(owner, "%", "types=task").items(t, "task"); len(tasks) != 1 || tasks[0].ID != pctTask.ID {
			t.Fatalf("bare %% should only match literal percent: %+v", tasks)
		}
		tasks = env.search(owner, token+" a_b", "types=task").items(t, "task")
		if len(tasks) != 1 || tasks[0].ID != underTask.ID {
			t.Fatalf("_ escape: %+v", tasks)
		}
		tasks = env.search(owner, `back\slash`, "types=task").items(t, "task")
		if len(tasks) != 1 || tasks[0].ID != slashTask.ID {
			t.Fatalf("backslash escape: %+v", tasks)
		}
		// 不区分大小写，首尾空白被去掉
		if tasks := env.search(owner, "  "+strings.ToLower(token)+" OPEN task  ", "types=task").items(t, "task"); len(tasks) != 1 {
			t.Fatalf("case-insensitive: %+v", tasks)
		}
	})

	t.Run("跨组织隔离", func(t *testing.T) {
		result := env.search(owner, token)
		for _, group := range result.Groups {
			for _, item := range group.Items {
				if strings.Contains(item.Title, "foreign") {
					t.Fatalf("foreign task leaked: %+v", item)
				}
			}
		}
		// 外组织用户只能搜自己的组织；搜本组织返回 404
		outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, searchPath(token), nil)
		var foreign searchResultView
		outsider.must(http.StatusOK, http.MethodGet, "/v1/organizations/"+env.otherOrg+"/"+searchPath(token), nil, &foreign)
		if foreign.has("task", openTask.ID) || len(foreign.items(t, "task")) != 1 || len(foreign.items(t, "person")) != 0 {
			t.Fatalf("foreign org result = %+v", foreign)
		}
		// 外组织用户的邮箱在本组织搜不到
		if people := env.search(owner, s3Email(s3OutsiderID), "types=person").items(t, "person"); len(people) != 0 {
			t.Fatalf("outsider visible as person: %+v", people)
		}
	})

	t.Run("limit 与总量上限", func(t *testing.T) {
		limitToken := "Lm" + s3Suffix(t)
		for i := 0; i < 22; i++ {
			env.createTask(owner, sharedSpace, map[string]any{"title": fmt.Sprintf("%s item %02d", limitToken, i)})
		}
		count := func(extra ...string) int {
			var result searchResultView
			owner.must(http.StatusOK, http.MethodGet, searchPath(limitToken, append([]string{"types=task"}, extra...)...), nil, &result)
			return len(result.items(t, "task"))
		}
		if got := count(); got != 5 {
			t.Fatalf("default limit = %d, want 5", got)
		}
		if got := count("limit=3"); got != 3 {
			t.Fatalf("limit=3 = %d", got)
		}
		if got := count("limit=100"); got != 20 {
			t.Fatalf("limit=100 = %d, want clamp to 20", got)
		}
		if total := env.search(owner, limitToken, "limit=20").total(); total > 100 {
			t.Fatalf("total = %d exceeds cap", total)
		}
	})

	t.Run("参数校验", func(t *testing.T) {
		for name, path := range map[string]string{
			"空 q":              "search",
			"纯空白 q":            "search?q=%20%20",
			"101 个字符":          "search?q=" + strings.Repeat("a", 101),
			"未知类型":             searchPath(token, "types=task,bogus"),
			"limit 为 0":        searchPath(token, "limit=0"),
			"limit 不是数字":       searchPath(token, "limit=abc"),
			"space_id 不是 UUID": searchPath(token, "space_id=nope"),
		} {
			if response := owner.do(http.MethodGet, path, nil); response.Status != http.StatusBadRequest || response.code() != "INVALID_REQUEST" {
				t.Fatalf("%s: %d %s", name, response.Status, response.Body)
			}
		}
		// 100 个字符刚好可以；types 可以逗号分隔或重复，按固定顺序返回且只含所选类型
		env.search(owner, strings.Repeat("a", 100))
		result := env.search(owner, token, "types=doc,task", "types=task")
		if len(result.Groups) != 2 || result.Groups[0].Type != "task" || result.Groups[1].Type != "doc" {
			t.Fatalf("groups = %+v", result.Groups)
		}
		// 未登录
		anonymous := &s3Client{env: env, http: &http.Client{}, server: env.server}
		if response := anonymous.do(http.MethodGet, searchPath(token), nil); response.Status != http.StatusUnauthorized {
			t.Fatalf("anonymous = %d", response.Status)
		}
	})
}

// must200Project 把专项也关联到另一个空间（用于验证「任一关联空间可读」）。
func (e *s3Env) must200Project(t *testing.T, client *s3Client, projectID, spaceID string) {
	t.Helper()
	client.must(http.StatusNoContent, http.MethodPost, "projects/"+projectID+"/spaces", map[string]any{"space_id": spaceID}, nil)
}
