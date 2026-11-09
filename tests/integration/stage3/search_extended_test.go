package stage3

import (
	"net/url"
	"strings"
	"testing"
)

func TestGlobalSearchChatAuthorizationAndPaging(t *testing.T) {
	env := newS3Env(t)
	manager := env.manager()
	task := env.createTask(manager, env.space.ID, map[string]any{"title": "定位对话"})
	agent := env.insertAgent(env.space.ID)
	conv, message := s3NewID(t), s3NewID(t)
	env.exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,task_id,owner_user_id,visibility,title)
		VALUES($1,$2,$3,$4,$5,$6,'SHARED',$7)`, env.org, conv, env.space.ID, agent, task.ID, s3OwnerID, "验收主题")
	env.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id) VALUES($1,$2,$3)`, env.org, conv, s3ManagerID)
	needle := "验收_%命中"
	body := strings.Repeat("背景", 110) + needle + "末尾"
	env.exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_user_id,body,created_at) VALUES($1,$2,$3,1,$4,$5,'2000-01-01Z')`, env.org, message, conv, s3OwnerID, body)
	direct := dmChat(t, manager, s3MemberID)
	dm := dmSend(t, manager, direct.ID, 1, body)
	lookup := func(q, kind string) []searchItemView {
		return env.search(manager, q, "offset=0", "types="+kind).items(t, kind)
	}
	if hits := lookup("验收主题", "conversation"); len(hits) != 1 || hits[0].ConversationID != conv || hits[0].ConversationKind != "agent" || hits[0].TaskDisplayID != task.DisplayID ||
		hits[0].Title != "定位对话" || hits[0].SenderKind != "agent" || hits[0].SenderID != agent || hits[0].SenderName == "" || hits[0].OccurredAt == nil || hits[0].OccurredAt.Year() != 2000 {
		t.Fatalf("task conversation = %+v", hits)
	}
	if hits := lookup(s3Email(s3MemberID), "conversation"); len(hits) != 1 || hits[0].ConversationID != direct.ID || hits[0].ConversationKind != "direct" ||
		hits[0].SenderKind != "user" || hits[0].SenderID != s3MemberID || hits[0].SenderName != s3Email(s3MemberID) || hits[0].OccurredAt == nil || !hits[0].OccurredAt.Equal(dm.CreatedAt) {
		t.Fatalf("direct conversation = %+v", hits)
	}
	for offset, want := range map[string]string{"0": dm.ID, "1": message, "2": ""} {
		page := env.search(manager, needle, "types=message", "limit=1", "offset="+offset, "space_ids="+env.otherSpace.ID)
		g := page.Groups[0]
		if g.Total == nil || *g.Total != 2 {
			t.Fatalf("offset %s total = %+v", offset, g)
		}
		if want == "" {
			if len(g.Items) != 0 {
				t.Fatalf("unexpected tail: %+v", g)
			}
			continue
		}
		if len(g.Items) != 1 {
			t.Fatalf("offset %s = %+v", offset, g)
		}
		hit := g.Items[0]
		if hit.ID != want || hit.MessageID != want || hit.MessageSeq != 1 || !strings.Contains(hit.Subtitle, needle) || len([]rune(hit.Subtitle)) > 160 {
			t.Fatalf("message hit = %+v", hit)
		}
		senderID := s3OwnerID
		if want == dm.ID {
			senderID = s3ManagerID
		}
		if hit.OccurredAt == nil || hit.SenderKind != "user" || hit.SenderID != senderID || hit.SenderName != s3Email(senderID) {
			t.Fatalf("message metadata = %+v", hit)
		}
		if want == dm.ID && (hit.ConversationKind != "direct" || hit.ConversationID != direct.ID) || want == message && (hit.ConversationKind != "agent" || hit.ConversationID != conv || hit.TaskDisplayID != task.DisplayID) {
			t.Fatalf("message target = %+v", hit)
		}
	}
	env.exec(`INSERT INTO openfox.messages(organization_id,conversation_id,seq,author_kind,author_agent_id,body)
		VALUES($1,$2,2,'AGENT',$3,'Agent 原文'), ($1,$2,3,'SYSTEM',NULL,'系统原文')`, env.org, conv, agent)
	if hits := lookup("Agent 原文", "message"); len(hits) != 1 || hits[0].SenderKind != "agent" || hits[0].SenderID != agent || hits[0].SenderName == "" {
		t.Fatalf("agent sender = %+v", hits)
	}
	if hits := lookup("系统原文", "message"); len(hits) != 1 || hits[0].SenderKind != "system" || hits[0].SenderID != "" || hits[0].SenderName != "" {
		t.Fatalf("system sender = %+v", hits)
	}
	if result := env.search(env.other(), needle, "offset=0", "types=message"); result.total() != 0 || *result.Groups[0].Total != 0 {
		t.Fatalf("not participant: %+v", result)
	}
	if legacy := env.search(manager, needle); len(legacy.Groups) != 7 || legacy.Groups[0].Total != nil {
		t.Fatalf("changed legacy group shape: %+v", legacy)
	}
	// Conversations stay behind when a task moves. Later edits in the private
	// destination must not be searchable through a previously shared conversation.
	private := env.createSpace("private")
	env.owner().must(200, "POST", spacePath(env.space.ID)+"/tasks/"+task.ID+"/move", map[string]any{"target_space_id": private.ID, "version": task.Version}, nil, "Idempotency-Key", "search-move-"+task.ID)
	env.exec(`UPDATE openfox.tasks SET title='目标私有标题' WHERE id=$1`, task.ID)
	if hits := lookup("目标私有标题", "conversation"); len(hits) != 0 {
		t.Fatalf("moved task title leaked via search: %+v", hits)
	}
	if hits := lookup("验收主题", "conversation"); len(hits) != 1 || hits[0].TaskDisplayID != "" || hits[0].Title != "验收主题" {
		t.Fatalf("moved task must open retained conversation: %+v", hits)
	}
	var summary struct {
		Task *struct{ Title string } `json:"task"`
	}
	manager.must(200, "GET", "conversations/"+conv, nil, &summary)
	if summary.Task != nil {
		t.Fatalf("moved task metadata leaked on landing: %+v", summary.Task)
	}
	env.exec(`DELETE FROM openfox.conversation_members WHERE organization_id=$1 AND conversation_id=$2 AND user_id=$3`, env.org, conv, s3ManagerID)
	if hits := lookup(needle, "message"); len(hits) != 1 || hits[0].ConversationID != direct.ID {
		t.Fatalf("message after share removed: %+v", hits)
	}
	env.exec(`UPDATE openfox.organization_members SET status='REVOKED' WHERE organization_id=$1 AND user_id=$2`, env.org, s3MemberID)
	if hits := lookup(needle, "message"); len(hits) != 0 {
		t.Fatalf("thread with revoked peer = %+v", hits)
	}
	if hits := lookup(s3Email(s3MemberID), "conversation"); len(hits) != 0 {
		t.Fatalf("revoked peer in title search: %+v", hits)
	}
}

func TestGlobalSearchTaskFacetsAndCounts(t *testing.T) {
	env := newS3Env(t)
	user := env.manager()
	space := env.space.ID
	project := env.newProject(user, space, map[string]any{"name": "查询专项"})
	first := env.createTask(user, space, map[string]any{"title": "授权任务甲", "description": "从描述查找", "priority": "HIGH"})
	second := env.createTask(user, space, map[string]any{"title": "授权任务乙", "priority": "LOW"})
	crew, label, iter, agent := s3NewID(t), s3NewID(t), s3NewID(t), env.insertAgent(space)
	env.exec(`INSERT INTO openfox.agent_crews(organization_id,id,space_id,owner_user_id,name) VALUES($1,$2,$3,$4,'搜索小队')`, env.org, crew, space, s3ManagerID)
	env.exec(`INSERT INTO openfox.task_assignees(organization_id,space_id,task_id,kind,crew_id,created_by) VALUES($1,$2,$3,'CREW',$4,$5)`, env.org, space, first.ID, crew, s3ManagerID)
	env.exec(`INSERT INTO openfox.task_assignees(organization_id,space_id,task_id,kind,user_id,created_by) VALUES($1,$2,$3,'HUMAN',$4,$4)`, env.org, space, first.ID, s3ManagerID)
	env.exec(`INSERT INTO openfox.iterations(organization_id,id,space_id,number,starts_at,ends_at) VALUES($1,$2,$3,100,now()+interval '1 day',now()+interval '4 days')`, env.org, iter, space)
	env.exec(`INSERT INTO openfox.labels(organization_id,space_id,id,name) VALUES($1,$2,$3,'搜索标签')`, env.org, space, label)
	env.exec(`INSERT INTO openfox.task_labels(organization_id,space_id,task_id,label_id) VALUES($1,$2,$3,$4)`, env.org, space, first.ID, label)
	env.exec(`INSERT INTO openfox.task_activities(organization_id,space_id,task_id,actor_id,kind,source_id,safe_diff) VALUES($1,$2,$3,$4,'task.via_agent','agent-created',jsonb_build_object('tool','space_task_create','agent_id',$5::text))`, env.org, space, first.ID, s3ManagerID, agent)
	env.exec(`UPDATE openfox.tasks SET project_id=$2,iteration_id=$3,due_date='2026-11-08' WHERE id=$1`, first.ID, project.ID, iter)
	env.exec(`UPDATE openfox.tasks SET parent_task_id=$2 WHERE id=$1`, second.ID, first.ID)
	addField := func(kind, raw string) string {
		id := s3NewID(t)
		env.exec(`INSERT INTO openfox.custom_fields(organization_id,id,name,kind) VALUES($1,$2,$3,$3)`, env.org, id, kind)
		env.exec(`INSERT INTO openfox.task_type_fields(organization_id,space_id,task_type_id,field_id,created_by) VALUES($1,$2,$3,$4,$5)`, env.org, space, first.TypeID, id, s3ManagerID)
		env.exec(`INSERT INTO openfox.task_field_values(organization_id,space_id,task_id,field_id,updated_by,value) VALUES($1,$2,$3,$4,$5,$6::jsonb)`, env.org, space, first.ID, id, s3ManagerID, raw)
		return "cf_" + id
	}
	number := addField("NUMBER", "12")
	date := addField("DATE", `"2026-11-09"`)
	boolean := addField("BOOLEAN", "false")
	txt := addField("TEXT", `"搜索内容"`)
	result := func(extra ...string) searchResultView {
		return env.search(user, "", "types=task", "limit=1", "offset=0", "space_ids="+space, strings.Join(extra, "&"))
	}
	for _, test := range []struct {
		keys []string
		want string
	}{
		{[]string{"priority=HIGH", "task_q=" + url.QueryEscape("从描述"), "type_id=" + first.TypeID, "status_id=" + first.StatusID, "creator_id=" + s3ManagerID}, first.ID},
		{[]string{"creator_agent_id=" + agent, "creator_id=" + s3OtherID, "label_id=" + label, "assignee=user:" + s3ManagerID, "crew_id=" + crew, "project_id=" + project.ID, "relation=parent", "iteration_id=@rel:upcoming"}, first.ID},
		{[]string{"creator_agent_id_not=" + agent, "relation=subtask"}, second.ID},
		{[]string{"priority_not=HIGH", "due_range=none", "iteration_id=none", "project_id=none"}, second.ID},
		{[]string{"due_range=2026-11-08/2026-11-09", "created_range=2000-01-01T00:00:00Z/"}, first.ID},
		{[]string{"due_range_not=2026-11-08/2026-11-09", "iteration_id_not=@rel:upcoming"}, second.ID},
		{[]string{number + "_op=between", number + "=10", number + "=20", boolean + "_op=is_false", txt + "_op=contains", txt + "=" + url.QueryEscape("内容")}, first.ID},
		{[]string{date + "_op=after", date + "=2026-11-08", number + "=12.0"}, first.ID},
		{[]string{date + "_op=is_empty", number + "_op=not_gt", number + "=11"}, second.ID},
	} {
		r := result(test.keys...)
		g := r.Groups[0]
		if g.Total == nil || *g.Total != 1 || len(g.Items) != 1 || g.Items[0].ID != test.want {
			t.Fatalf("%v = %+v", test.keys, g)
		}
	}
	if r := result("crew_id="+crew, "assignee=user:"+s3OtherID); *r.Groups[0].Total != 0 {
		t.Fatalf("crew and assignee must intersect: %+v", r)
	}
	outside := env.createSpace("shared").ID
	third := env.createTask(env.owner(), outside, map[string]any{"title": "授权任务丙"})
	enlarged := env.search(user, "授权任务", "types=task", "offset=0", "space_ids="+space, "space_ids="+outside)
	if *enlarged.Groups[0].Total != 3 || !enlarged.has("task", third.ID) {
		t.Fatalf("multiple spaces = %+v", enlarged)
	}
	all := result()
	if len(all.Groups[0].Items) != 1 || *all.Groups[0].Total != 2 {
		t.Fatalf("unfiltered dedicated task = %+v", all)
	}
	if projects := env.search(user, "", "offset=0", "types=project", "space_ids="+space); !projects.has("project", project.ID) {
		t.Fatalf("unfiltered projects = %+v", projects)
	}
	for _, path := range []string{"search?types=task", "search?types=message&offset=0", "search?q=x&offset=-1", "search?types=task&offset=0&created_range=yesterday", "search?types=task&offset=0&" + number + "_op=gt&" + number + "=oops"} {
		if resp := user.do("GET", path, nil); resp.Status != 400 {
			t.Fatalf("bad query %s status=%d", path, resp.Status)
		}
	}
}

// Selecting all but one of 52 visible spaces exceeds the old 50-ID cap.
func TestGlobalSearchManySpaces(t *testing.T) {
	env := newS3Env(t)
	all := make([]string, 52)
	for i := range all {
		all[i] = env.createSpace("shared").ID
	}
	search := func(ids []string) searchResultView {
		return env.search(env.owner(), "task search 51", "types=task", "offset=0", "space_ids="+strings.Join(ids, ","))
	}
	shown := env.createTask(env.owner(), all[50], map[string]any{"title": "task search 51 include"})
	hidden := env.createTask(env.owner(), all[51], map[string]any{"title": "task search 51 exclude"})
	chosen := all[:51]
	r := search(chosen)
	if *r.Groups[0].Total != 1 || !r.has("task", shown.ID) || r.has("task", hidden.ID) {
		t.Fatalf("51 selected spaces = %+v", r)
	}
}
