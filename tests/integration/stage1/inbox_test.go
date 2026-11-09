package stage1

import (
	"net/http"
	"sort"
	"testing"
)

type inboxGroupView struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	TaskTypeIcon *string `json:"task_type_icon"`
	Unread       bool    `json:"unread"`
}

type inboxPageView struct {
	Groups []inboxGroupView `json:"groups"`
	Counts struct {
		Inbox                int `json:"inbox"`
		InboxUnread          int `json:"inbox_unread"`
		InboxImportantUnread int `json:"inbox_important_unread"`
		InboxOtherUnread     int `json:"inbox_other_unread"`
	} `json:"counts"`
}

func inboxKinds(page inboxPageView) []string {
	kinds := make([]string, 0, len(page.Groups))
	for _, group := range page.Groups {
		kinds = append(kinds, group.Kind)
	}
	sort.Strings(kinds)
	return kinds
}

// 收件箱「重要 / 其他」分类、task_type_icon，以及会话交接「等你审阅」写 HANDOFF_REVIEW。
func TestInboxImportanceAndHandoffReview(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	env.exec(`UPDATE openfox.task_types SET icon = 'bug' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, space.TypeID)

	// Exercise all seven kinds with the recipient's explicit choices (Agent alerts default off).
	for key, level := range map[string]string{"task_needs_human_input_participant": "important", "task_failed_participant": "important", "task_review_requested_participant": "normal", "chat_replied_participant": "normal"} {
		env.member.must(200, "PATCH", "/v1/me/inbox-preferences", map[string]string{"key": key, "level": level}, nil)
	}
	// 每种通知挂在各自的任务上（同任务会聚成一组）。
	kinds := []string{"HANDOFF", "RUN_FAILED", "MENTION", "TASK_ASSIGNED", "HANDOFF_REVIEW", "REPLY", "TASK_COMMENT"}
	for _, kind := range kinds {
		task := env.newTask(env.owner, space, nil)
		env.exec(`INSERT INTO openfox.notifications (organization_id, recipient_user_id, kind, task_id, dedupe_key, safe_summary)
			VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, 'summary')`, env.org, cfgMemberID, kind, task.ID, "inbox-test:"+task.ID)
	}
	list := func(query string) inboxPageView {
		t.Helper()
		var page inboxPageView
		path := "inbox"
		if query != "" {
			path += "?" + query
		}
		env.member.must(http.StatusOK, http.MethodGet, path, nil, &page)
		return page
	}
	primary := list("importance=primary")
	if got := inboxKinds(primary); !sameStrings(got, []string{"HANDOFF", "MENTION", "RUN_FAILED", "TASK_ASSIGNED"}) {
		t.Fatalf("primary kinds = %v", got)
	}
	other := list("importance=other")
	if got := inboxKinds(other); !sameStrings(got, []string{"HANDOFF_REVIEW", "REPLY", "TASK_COMMENT"}) {
		t.Fatalf("other kinds = %v", got)
	}
	all := list("")
	if len(all.Groups) != 7 || all.Counts.Inbox != 7 || all.Counts.InboxUnread != 7 || all.Counts.InboxImportantUnread != 4 ||
		all.Counts.InboxOtherUnread != 3 {
		t.Fatalf("all = %d groups, counts %+v", len(all.Groups), all.Counts)
	}
	for _, group := range all.Groups {
		if group.TaskTypeIcon == nil || *group.TaskTypeIcon != "bug" {
			t.Fatalf("task_type_icon = %+v", group)
		}
	}
	var stats struct {
		Counts struct {
			InboxImportantUnread int `json:"inbox_important_unread"`
		} `json:"counts"`
	}
	env.member.must(http.StatusOK, http.MethodGet, "inbox/stats", nil, &stats)
	if stats.Counts.InboxImportantUnread != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	// 批量「全部已读」只作用于筛选范围：只读掉「其他」。
	var affected struct {
		Affected int `json:"affected"`
	}
	env.member.must(http.StatusOK, http.MethodPost, "inbox/bulk",
		map[string]any{"operation": "mark_read", "section": "inbox", "importance": "other"}, &affected)
	if affected.Affected != 3 {
		t.Fatalf("bulk affected = %d", affected.Affected)
	}
	if counts := list("").Counts; counts.InboxUnread != 4 || counts.InboxImportantUnread != 4 || counts.InboxOtherUnread != 0 {
		t.Fatalf("after bulk = %+v", counts)
	}
	// 按组操作：清理一个重要组。
	env.member.must(http.StatusOK, http.MethodPost, "inbox/actions",
		map[string]any{"action": "clear", "section": "inbox", "group_ids": []string{primary.Groups[0].ID}}, &affected)
	if affected.Affected != 1 || list("").Counts.Inbox != 6 || len(list("section=cleared").Groups) != 1 {
		t.Fatalf("clear affected = %d", affected.Affected)
	}

	// 会话交接：等你回复 → HANDOFF（重要），等你审阅 → HANDOFF_REVIEW（其他）。
	agentID := env.insertAgent(space.ID, cfgOwnerID, "SHARED")
	// 对话与收件箱都按协作策略判断空间可读：SHARED 空间的非访客成员即使不是空间成员，
	// 也能打开共享给他的对话并看到对应通知；成为空间成员后仍可见。
	var conversation struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	env.owner.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": space.ID, "agent_id": agentID, "visibility": "SHARED"}, &conversation)
	env.owner.must(http.StatusNoContent, http.MethodPut, "conversations/"+conversation.ID+"/members/"+cfgMemberID,
		map[string]any{"access_level": "COMMENTER"}, nil)
	env.owner.must(http.StatusOK, http.MethodGet, "conversations/"+conversation.ID, nil, &conversation)
	env.owner.must(http.StatusOK, http.MethodPost, "conversations/"+conversation.ID+"/handoff",
		map[string]any{"handoff": "WAITING_REVIEW", "version": conversation.Version}, &conversation)
	var kind string
	env.scalar(&kind, `SELECT kind FROM openfox.notifications
		WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND conversation_id = $3::uuid`, env.org, cfgMemberID, conversation.ID)
	if kind != "HANDOFF_REVIEW" {
		t.Fatalf("review handoff kind = %s", kind)
	}
	if got := inboxKinds(list("importance=other")); !sameStrings(got, []string{"HANDOFF_REVIEW", "HANDOFF_REVIEW", "REPLY", "TASK_COMMENT"}) {
		t.Fatalf("other before space membership = %v", got)
	}
	env.member.must(http.StatusOK, http.MethodGet, "conversations/"+conversation.ID, nil, nil)
	env.addSpaceMember(space.ID, cfgMemberID, "MEMBER", "ACTIVE")
	if got := inboxKinds(list("importance=other")); !sameStrings(got, []string{"HANDOFF_REVIEW", "HANDOFF_REVIEW", "REPLY", "TASK_COMMENT"}) {
		t.Fatalf("other after review handoff = %v", got)
	}
	env.owner.must(http.StatusOK, http.MethodPost, "conversations/"+conversation.ID+"/handoff",
		map[string]any{"handoff": "WAITING_REPLY", "version": conversation.Version}, &conversation)
	var replyKinds int
	env.scalar(&replyKinds, `SELECT count(*) FROM openfox.notifications
		WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND conversation_id = $3::uuid AND kind = 'HANDOFF'`,
		env.org, cfgMemberID, conversation.ID)
	if replyKinds != 1 {
		t.Fatalf("reply handoff notifications = %d", replyKinds)
	}

	// SHARED 空间里被撤销的空间成员：任务通知与对话通知都不再可见。
	env.addSpaceMember(space.ID, cfgMemberID, "MEMBER", "REVOKED")
	if page := list(""); len(page.Groups) != 0 || page.Counts.Inbox != 0 {
		t.Fatalf("revoked inbox = %+v", page)
	}
}

// 私有空间里的任务通知：收件人失去空间访问后不再显示。
func TestInboxHidesUnreadableSpace(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("private")
	env.addSpaceMember(space.ID, cfgMemberID, "MEMBER", "ACTIVE")
	task := env.newTask(env.owner, space, map[string]any{"assignees": []map[string]any{{"kind": "HUMAN", "id": cfgMemberID}}})
	var page inboxPageView
	env.member.must(http.StatusOK, http.MethodGet, "inbox", nil, &page)
	if len(page.Groups) != 1 || page.Groups[0].ID != task.ID || page.Groups[0].Kind != "TASK_ASSIGNED" {
		t.Fatalf("inbox = %+v", page)
	}
	env.exec(`DELETE FROM openfox.space_members WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`,
		env.org, space.ID, cfgMemberID)
	env.member.must(http.StatusOK, http.MethodGet, "inbox", nil, &page)
	if len(page.Groups) != 0 || page.Counts.Inbox != 0 {
		t.Fatalf("inbox after losing access = %+v", page)
	}
}

func TestInboxPolicySnapshotAndChannels(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	const path = "/v1/me/inbox-preferences"
	type pref struct{ Key, Group, Level string }
	var rows []pref
	e.member.must(200, "GET", path, nil, &rows)
	if len(rows) != 49 {
		t.Fatalf("catalog has %d rows", len(rows))
	}
	patch := func(k, v string) { e.member.must(200, "PATCH", path, map[string]string{"key": k, "level": v}, nil) }
	get := func(who *cfgClient, key string) string {
		who.must(200, "GET", path, nil, &rows)
		for _, r := range rows {
			if r.Key == key {
				return r.Level
			}
		}
		return ""
	}
	e.member.must(400, "PATCH", path, map[string]string{"key": "not_an_event", "level": "off"}, nil)
	e.member.must(400, "PATCH", path, map[string]string{"key": "mentions", "level": "important"}, nil)
	create := func() string {
		return e.newTask(e.owner, sp, map[string]any{"assignees": []map[string]string{{"kind": "HUMAN", "id": cfgMemberID}}}).ID
	}
	important := create()
	patch("task_assigned_assignee", "normal")
	other := create()
	patch("assignments", "off")
	hidden := create()
	e.member.must(200, "DELETE", path, nil, nil)
	list := func(query string) inboxPageView {
		var page inboxPageView
		e.member.must(200, "GET", "inbox?"+query, nil, &page)
		return page
	}
	for query, id := range map[string]string{"importance=primary": important, "importance=other": other} {
		p := list(query)
		if len(p.Groups) != 1 || p.Groups[0].ID != id || p.Counts.InboxUnread != 2 {
			t.Fatalf("%s %+v", query, p)
		}
	}
	found := false
	for _, g := range list("delivery=banner").Groups {
		found = found || g.ID == hidden
	}
	if !found {
		t.Fatal("inbox mute silenced independent banner feed")
	}
	patch("task_assigned_assignee", "off")
	if get(e.owner, "task_assigned_assignee") != "important" {
		t.Fatal("preferences leaked between accounts")
	}
	if get(e.member, "task_failed_participant") != "off" {
		t.Fatal("run failure default")
	}
	e.exec(`INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,task_id,dedupe_key,safe_summary) VALUES ($1,$2,'RUN_FAILED',$3,'silent-run','failed')`, e.org, cfgMemberID, important)
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.notifications WHERE dedupe_key='silent-run'`)
	if count != 0 {
		t.Fatal("muted Agent failure generated delivery")
	}
}

func TestInboxChangedTaskAudience(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	tsk := e.newTask(e.owner, sp, map[string]any{"assignees": []map[string]string{{"kind": "HUMAN", "id": cfgMemberID}}})
	watcher := e.addMember(tkUserC, "MEMBER")
	watcher.must(204, "PUT", taskPath(sp, tsk.ID)+"/followers/human/"+tkUserC, nil, nil)
	e.owner.must(200, "PATCH", taskPath(sp, tsk.ID), map[string]any{"version": tsk.Version, "priority": "HIGH", "due_date": "2026-11-30"}, nil)
	for _, pair := range []struct{ user, role, level string }{{cfgMemberID, "assignee", "important"}, {tkUserC, "follower", "normal"}} {
		var count int
		e.scalar(&count, `SELECT count(*) FROM openfox.notifications WHERE task_id=$1 AND recipient_user_id=$2 AND inbox_level=$3 AND inbox_event IN ($4,$5)`, tsk.ID, pair.user, pair.level, "task_priority_updated_"+pair.role, "task_due_date_updated_"+pair.role)
		if count != 2 {
			t.Fatalf("%s got %d change alerts", pair.role, count)
		}
	}
}
