package stage2

import (
	"net/http"
	"strconv"
	"testing"
)

// 复合游标：同一 updated_at 的会话跨页不漏不重；旧版纯数字游标仍接受。
func TestConvListCompositeCursor(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	member := e.client(agMemberID)
	want := map[string]bool{}
	for index := 0; index < 7; index++ {
		want[f.newConversation(member).ID] = true
	}
	// 让 7 个会话的 updated_at 完全相同。
	e.exec(`UPDATE openfox.conversations SET updated_at = '2026-10-29 10:00:00+00'
		 WHERE organization_id = $1::uuid AND agent_id = $2::uuid`, e.org, f.agentID)
	for _, limit := range []int{1, 2, 3} {
		ids := f.listAll(member, "&agent_id="+f.agentID, limit)
		if len(ids) != len(want) {
			t.Fatalf("limit %d: got %d ids, want %d", limit, len(ids), len(want))
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !want[id] || seen[id] {
				t.Fatalf("limit %d: unexpected or duplicate id %s in %v", limit, id, ids)
			}
			seen[id] = true
		}
		// 同 updated_at 时按 id 降序。
		for index := 1; index < len(ids); index++ {
			if ids[index-1] < ids[index] {
				t.Fatalf("limit %d: ids not in id desc order: %v", limit, ids)
			}
		}
	}
	// 旧版纳秒游标：只按 updated_at 严格小于翻页（同一时刻的会话都不再返回）。
	var legacy convPage
	member.must(http.StatusOK, http.MethodGet, "conversations?cursor=1793268000000000000&agent_id="+f.agentID, nil, &legacy)
	if len(legacy.Conversations) != 0 {
		t.Fatalf("legacy cursor returned %d items", len(legacy.Conversations))
	}
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "conversations?cursor=not*base64", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "conversations?archived=all", nil)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "conversations?pinned=yes", nil)
}

// 置顶、已读、重命名、归档都不改 version；已读只增不减并截到最大 seq；unread 不计本人消息；置顶是个人状态。
func TestConvPinReadAndUnread(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, viewer := e.client(agMemberID), e.client(agManagerID)
	conversation := f.newConversation(owner)
	path := conversationPath(conversation.ID)
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	start := f.get(owner, conversation.ID)

	// 本人发的消息不算未读；Agent 回复算未读。
	f.post(owner, conversation.ID, "hello")
	if item := f.get(owner, conversation.ID); item.Unread {
		t.Fatal("own message must not be unread")
	}
	_, agentSeq := f.agentMessage(conversation.ID, "TEXT")
	afterMessages := f.get(owner, conversation.ID)
	if !afterMessages.Unread || afterMessages.LastMessage == nil || afterMessages.LastMessage.Seq != agentSeq ||
		afterMessages.LastMessage.AuthorKind != "AGENT" || afterMessages.LastMessage.Preview != "agent reply" {
		t.Fatalf("after agent reply: %+v", afterMessages)
	}
	// 共享成员看到的 unread 按自己的已读位置：owner 的消息对 viewer 也是未读。
	if item := f.get(viewer, conversation.ID); !item.Unread || item.LastReadSeq != 0 {
		t.Fatalf("viewer: %+v", item)
	}

	owner.must(http.StatusNoContent, http.MethodPut, path+"/pin", nil, nil)
	owner.must(http.StatusNoContent, http.MethodPut, path+"/pin", nil, nil) // 幂等
	owner.must(http.StatusNoContent, http.MethodPost, path+"/read", map[string]any{"seq": 1000}, nil)
	pinned := f.get(owner, conversation.ID)
	if !pinned.Pinned || pinned.Unread || pinned.LastReadSeq != agentSeq {
		t.Fatalf("after pin + read: %+v (agentSeq %d)", pinned, agentSeq)
	}
	// 只增不减。
	owner.must(http.StatusNoContent, http.MethodPost, path+"/read", map[string]any{"seq": 1}, nil)
	if item := f.get(owner, conversation.ID); item.LastReadSeq != agentSeq {
		t.Fatalf("read seq decreased to %d", item.LastReadSeq)
	}
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path+"/read", map[string]any{"seq": -1})
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path+"/read", map[string]any{})
	// 置顶是个人状态：viewer 看不到 owner 的置顶，viewer 也能置顶自己的。
	if item := f.get(viewer, conversation.ID); item.Pinned {
		t.Fatal("pin leaked to another member")
	}
	viewer.must(http.StatusNoContent, http.MethodPut, path+"/pin", nil, nil)
	viewer.must(http.StatusNoContent, http.MethodPost, path+"/read", map[string]any{"seq": 1}, nil)

	// 重命名、归档不改 version；置顶 / 已读也不改。
	var renamed convSummary
	owner.must(http.StatusOK, http.MethodPatch, path, map[string]any{"title": "  周报整理  "}, &renamed)
	if renamed.Title == nil || *renamed.Title != "周报整理" {
		t.Fatalf("title = %v", renamed.Title)
	}
	final := f.get(owner, conversation.ID)
	if final.Version != afterMessages.Version || final.UpdatedAt != afterMessages.UpdatedAt {
		t.Fatalf("version/updated_at changed by pin/read/rename: %d→%d %s→%s",
			afterMessages.Version, final.Version, afterMessages.UpdatedAt, final.UpdatedAt)
	}
	if start.Version >= afterMessages.Version {
		t.Fatal("messages should bump version")
	}
	// viewer 不能改标题。
	viewer.expectError(http.StatusForbidden, "", http.MethodPatch, path, map[string]any{"title": "x"})

	// pinned=only 筛选；取消置顶后消失。
	var onlyPinned convPage
	owner.must(http.StatusOK, http.MethodGet, "conversations?pinned=only", nil, &onlyPinned)
	if len(onlyPinned.Conversations) != 1 || onlyPinned.Conversations[0].ID != conversation.ID {
		t.Fatalf("pinned=only: %+v", onlyPinned.Conversations)
	}
	owner.must(http.StatusNoContent, http.MethodDelete, path+"/pin", nil, nil)
	owner.must(http.StatusOK, http.MethodGet, "conversations?pinned=only", nil, &onlyPinned)
	if len(onlyPinned.Conversations) != 0 {
		t.Fatal("unpinned conversation still listed")
	}
}

// PATCH 标题清除、归档筛选、标题搜索；旧别名 PATCH …/session 仍可用。
func TestConvUpdateArchiveAndSearch(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agMemberID)
	first, second := f.newConversation(owner), f.newConversation(owner)
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(first.ID), map[string]any{"title": "Deploy 100%_plan"}, nil)
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(second.ID)+"/session", map[string]any{"title": "Other"}, nil)

	var found convPage
	owner.must(http.StatusOK, http.MethodGet, "conversations?q=100%25_", nil, &found)
	if len(found.Conversations) != 1 || found.Conversations[0].ID != first.ID {
		t.Fatalf("q search: %+v", found.Conversations)
	}
	owner.must(http.StatusOK, http.MethodGet, "conversations?q=deploy", nil, &found)
	if len(found.Conversations) != 1 {
		t.Fatal("q search must be case-insensitive")
	}
	owner.must(http.StatusOK, http.MethodGet, "conversations?q=1%25%25", nil, &found)
	if len(found.Conversations) != 0 {
		t.Fatal("q must match % literally")
	}

	var archived convSummary
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(first.ID), map[string]any{"archived": true, "title": nil}, &archived)
	if archived.ArchivedAt == nil || archived.Title != nil {
		t.Fatalf("archive + clear title: %+v", archived)
	}
	count := func(query string) int {
		var page convPage
		owner.must(http.StatusOK, http.MethodGet, "conversations?agent_id="+f.agentID+query, nil, &page)
		return len(page.Conversations)
	}
	if count("") != 1 || count("&archived=only") != 1 || count("&archived=include") != 2 {
		t.Fatalf("archived filter counts: %d %d %d", count(""), count("&archived=only"), count("&archived=include"))
	}
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, conversationPath(first.ID), map[string]any{})
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(first.ID), map[string]any{"archived": false}, &archived)
	if archived.ArchivedAt != nil {
		t.Fatal("unarchive failed")
	}
}

// 派生状态：构造各种 Run 状态与生命周期 / 交接。
func TestConvStatusDerivation(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agMemberID)
	status := func(id string) string { return f.get(owner, id).Status }
	fresh := func() string { return f.newConversation(owner).ID }

	idle := fresh()
	if got := status(idle); got != "IDLE" {
		t.Fatalf("no run: %s", got)
	}
	queued := fresh()
	f.insertRun(queued, "QUEUED")
	if got := status(queued); got != "QUEUED" {
		t.Fatalf("queued: %s", got)
	}
	for _, state := range []string{"PREPARING", "RUNNING", "CANCEL_REQUESTED"} {
		id := fresh()
		f.insertRun(id, state)
		item := f.get(owner, id)
		if item.Status != "WORKING" || item.Run == nil || item.Run.State != state {
			t.Fatalf("%s: %+v", state, item)
		}
	}
	failed := fresh()
	f.finishRun(f.insertRun(failed, "RUNNING"), "FAILED")
	f.agentMessage(failed, "TEXT") // 终态消息（Agent / 系统）不清除 ERROR
	if got := status(failed); got != "ERROR" {
		t.Fatalf("failed: %s", got)
	}
	f.post(owner, failed, "retry please")
	if got := status(failed); got != "IDLE" {
		t.Fatalf("failed then user message: %s", got)
	}
	canceled := fresh()
	f.insertRun(canceled, "CANCELED")
	if got := status(canceled); got != "ABORTED" {
		t.Fatalf("canceled: %s", got)
	}
	// 活动 Run 优先于最近终态 Run：先 FAILED 再排队。
	retried := fresh()
	f.insertRun(retried, "FAILED")
	runID := f.insertRun(retried, "RUNNING")
	if item := f.get(owner, retried); item.Status != "WORKING" || item.Run.ID != runID {
		t.Fatalf("active run must win: %+v", item)
	}
	for handoff, want := range map[string]string{"WAITING_REPLY": "WAITING", "WAITING_REVIEW": "REVIEW"} {
		id := fresh()
		f.insertRun(id, "SUCCEEDED")
		item := f.get(owner, id)
		owner.must(http.StatusOK, http.MethodPost, conversationPath(id)+"/handoff",
			map[string]any{"version": item.Version, "handoff": handoff}, nil)
		if got := status(id); got != want {
			t.Fatalf("%s: %s", handoff, got)
		}
	}
	done := fresh()
	f.insertRun(done, "FAILED")
	item := f.get(owner, done)
	owner.must(http.StatusOK, http.MethodPost, conversationPath(done)+"/complete", map[string]any{"version": item.Version}, nil)
	if got := status(done); got != "DONE" {
		t.Fatalf("completed: %s", got)
	}
}

// 消息的 run_id 关联与向上翻页（order=desc&before=）。
func TestConvMessagesRunLinkAndBackwardPaging(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agMemberID)
	conversation := f.newConversation(owner)
	path := conversationPath(conversation.ID) + "/messages"

	// 第一轮：用户消息 → Run1 → Agent 回复；第二轮：用户消息 → Run2 → 系统失败消息 + 已执行卡片。
	userOne := f.post(owner, conversation.ID, "round one")
	runOne := f.insertRun(conversation.ID, "RUNNING")
	e.exec(`INSERT INTO openfox.conversation_inputs (organization_id, conversation_id, message_id, body, status, run_id, assigned_at)
		SELECT organization_id, conversation_id, id, body, 'ASSIGNED', $3::uuid, clock_timestamp()
		  FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND seq = $4`,
		e.org, conversation.ID, runOne, userOne)
	f.finishRun(runOne, "SUCCEEDED")
	f.agentMessage(conversation.ID, "TEXT")
	f.post(owner, conversation.ID, "round two (no input)")
	runTwo := f.insertRun(conversation.ID, "FAILED")
	f.agentMessage(conversation.ID, "TEXT")
	f.agentMessage(conversation.ID, "ACTION_EXECUTED")

	var page convMessagePage
	owner.must(http.StatusOK, http.MethodGet, path, nil, &page)
	if len(page.Messages) != 5 {
		t.Fatalf("messages = %d", len(page.Messages))
	}
	runOf := func(message convMessage) string {
		if message.RunID == nil {
			return ""
		}
		return *message.RunID
	}
	want := []string{runOne, runOne, "", runTwo, ""}
	for index, message := range page.Messages {
		if runOf(message) != want[index] {
			t.Fatalf("message %d (%s/%s) run_id = %q, want %q", message.Seq, message.AuthorKind, message.Kind, runOf(message), want[index])
		}
	}

	// 向上翻页：最新 2 条 → 再早 2 条 → 最后 1 条；页内升序。
	var seqs []int64
	before := int64(0)
	for round := 0; round < 5; round++ {
		query := path + "?order=desc&limit=2"
		if before != 0 {
			query += "&before=" + strconv.FormatInt(before, 10)
		}
		var backward convMessagePage
		owner.must(http.StatusOK, http.MethodGet, query, nil, &backward)
		for index := 1; index < len(backward.Messages); index++ {
			if backward.Messages[index-1].Seq >= backward.Messages[index].Seq {
				t.Fatalf("backward page not ascending: %+v", backward.Messages)
			}
		}
		page := make([]int64, 0, len(backward.Messages))
		for _, message := range backward.Messages {
			page = append(page, message.Seq)
		}
		seqs = append(page, seqs...)
		if backward.NextCursor == 0 {
			break
		}
		if backward.NextCursor != backward.Messages[0].Seq {
			t.Fatalf("next_cursor %d != oldest seq %d", backward.NextCursor, backward.Messages[0].Seq)
		}
		before = backward.NextCursor
	}
	if len(seqs) != 5 {
		t.Fatalf("backward paging collected %v", seqs)
	}
	for index, seq := range seqs {
		if seq != page.Messages[index].Seq {
			t.Fatalf("backward paging order %v", seqs)
		}
	}
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, path+"?order=desc&after=1", nil)
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, path+"?before=3", nil)
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, path+"?order=sideways", nil)
}

// 绑定已有任务：权限、同空间、已绑定、活动 Run、删除 / 不可读的任务。
func TestConvBindTask(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, commenter := e.client(agMemberID), e.client(agManagerID)
	task := f.createTask(f.space, "Existing task")

	conversation := f.newConversation(owner)
	path := conversationPath(conversation.ID)
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agManagerID, map[string]any{"access_level": "COMMENTER"}, nil)
	commenter.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, path+"/task", map[string]any{"task_id": task})
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path+"/task", map[string]any{"task_id": "nope"})
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/task", map[string]any{"task_id": agNewID(t)})

	// 其他空间的任务：可读但不同空间 → 400；不可读空间的任务 → 404。
	otherSpace := e.createSpace("shared")
	otherTask := f.createTask(otherSpace, "Elsewhere")
	owner.expectError(http.StatusBadRequest, "TASK_SPACE_MISMATCH", http.MethodPost, path+"/task", map[string]any{"task_id": otherTask})
	privateSpace := e.createSpace("private")
	privateTask := f.createTask(privateSpace, "Secret")
	e.exec(`DELETE FROM openfox.space_members WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`,
		e.org, privateSpace, agMemberID)
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/task", map[string]any{"task_id": privateTask})

	// 已删除的任务 → 404。
	deleted := f.createTask(f.space, "Deleted")
	e.exec(`UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $3::uuid WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, deleted, agOwnerID)
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path+"/task", map[string]any{"task_id": deleted})

	// 活动 Run → 409 RUN_ACTIVE。
	busy := f.newConversation(owner)
	f.insertRun(busy.ID, "RUNNING")
	owner.expectError(http.StatusConflict, "RUN_ACTIVE", http.MethodPost, conversationPath(busy.ID)+"/task", map[string]any{"task_id": task})

	before := f.get(owner, conversation.ID)
	var bound convSummary
	owner.must(http.StatusOK, http.MethodPost, path+"/task", map[string]any{"task_id": task}, &bound)
	if bound.TaskID != task || bound.Task == nil || bound.Task.Title != "Existing task" || bound.Task.DisplayID == "" ||
		bound.Version != before.Version+1 {
		t.Fatalf("bound: %+v", bound)
	}
	owner.expectError(http.StatusConflict, "CONVERSATION_TASK_BOUND", http.MethodPost, path+"/task", map[string]any{"task_id": task})
	var byTask convPage
	owner.must(http.StatusOK, http.MethodGet, "conversations?task_id="+task, nil, &byTask)
	if len(byTask.Conversations) != 1 || byTask.Conversations[0].ID != conversation.ID {
		t.Fatalf("task_id filter: %+v", byTask.Conversations)
	}
}

// 转为任务：默认标题取首条用户消息首行；幂等重放；已绑定冲突；非所有者禁止。
func TestConvConvertToTask(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, other := e.client(agMemberID), e.client(agManagerID)
	conversation := f.newConversation(owner)
	path := conversationPath(conversation.ID) + "/convert-to-task"
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path, map[string]any{}) // 没有标题也没有消息
	f.post(owner, conversation.ID, "\n  整理 Q3 复盘材料  \n第二行细节")
	other.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path, map[string]any{}) // 私有会话对他人不可见

	key := "convert-" + agSuffix(t)
	var converted convSummary
	response := owner.must(http.StatusCreated, http.MethodPost, path, map[string]any{}, &converted, "Idempotency-Key", key)
	if converted.Task == nil || converted.Task.Title != "整理 Q3 复盘材料" || converted.TaskID == "" {
		t.Fatalf("converted: %+v (%s)", converted, response.Body)
	}
	var replay convSummary
	replayed := owner.must(http.StatusOK, http.MethodPost, path, map[string]any{}, &replay, "Idempotency-Key", key)
	if replayed.Header.Get("Idempotent-Replayed") != "true" || replay.TaskID != converted.TaskID {
		t.Fatalf("replay: %+v", replay)
	}
	var tasks int
	e.scalar(&tasks, `SELECT count(*) FROM openfox.tasks WHERE organization_id = $1::uuid AND title = '整理 Q3 复盘材料'`, e.org)
	if tasks != 1 {
		t.Fatalf("tasks created = %d", tasks)
	}
	owner.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path, map[string]any{"title": "x"}, "Idempotency-Key", key)
	owner.expectError(http.StatusConflict, "CONVERSATION_TASK_BOUND", http.MethodPost, path, map[string]any{"title": "again"})

	// 显式标题 + 会话标题优先于首条消息。
	titled := f.newConversation(owner)
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(titled.ID), map[string]any{"title": "会话标题"}, nil)
	var fromTitle convSummary
	owner.must(http.StatusCreated, http.MethodPost, conversationPath(titled.ID)+"/convert-to-task", map[string]any{}, &fromTitle)
	if fromTitle.Task == nil || fromTitle.Task.Title != "会话标题" {
		t.Fatalf("title from conversation: %+v", fromTitle.Task)
	}
}

// 跨租户与他人私有会话：读、置顶、已读、改名、消息、绑定都 404，且不出现在列表里。
func TestConvIsolation(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, other, outsider := e.client(agMemberID), e.client(agManagerID), e.client(agOutsiderID)
	private := f.newConversation(owner)
	path := conversationPath(private.ID)
	task := f.createTask(f.space, "T")
	for _, request := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, path, nil},
		{http.MethodPatch, path, map[string]any{"title": "x"}},
		{http.MethodPut, path + "/pin", nil},
		{http.MethodDelete, path + "/pin", nil},
		{http.MethodPost, path + "/read", map[string]any{"seq": 1}},
		{http.MethodGet, path + "/messages", nil},
		{http.MethodPost, path + "/task", map[string]any{"task_id": task}},
		{http.MethodPost, path + "/convert-to-task", map[string]any{"title": "x"}},
	} {
		other.expectError(http.StatusNotFound, "NOT_FOUND", request.method, request.path, request.body)
		response := outsider.doIn(e.otherOrg, request.method, request.path, request.body)
		if response.Status != http.StatusNotFound {
			t.Fatalf("outsider %s %s = %d %s", request.method, request.path, response.Status, response.Body)
		}
	}
	var page convPage
	other.must(http.StatusOK, http.MethodGet, "conversations?archived=include", nil, &page)
	for _, item := range page.Conversations {
		if item.ID == private.ID {
			t.Fatal("private conversation listed for another member")
		}
	}
	// 他人的个人状态行不可写：直接查表确认没有为 other 生成行。
	var rows int
	e.scalar(&rows, `SELECT count(*) FROM openfox.conversation_user_state WHERE organization_id = $1::uuid AND conversation_id = $2::uuid`,
		e.org, private.ID)
	if rows != 0 {
		t.Fatalf("user state rows = %d", rows)
	}
}

// 运行历史补充 started_at / duration_ms / usage（2b）与 wait_reason（2c）。
func TestConvAgentRunSummaryTiming(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner := e.client(agMemberID)
	conversation := f.newConversation(owner)
	finished := f.insertRun(conversation.ID, "QUEUED")
	e.exec(`UPDATE openfox.runs SET state = 'SUCCEEDED', started_at = '2026-10-29 10:00:00+00',
		       finished_at = '2026-10-29 10:01:18.5+00' WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, finished)
	e.exec(`INSERT INTO openfox.run_results (organization_id, run_id, generation, status, payload_hash, usage)
		VALUES ($1::uuid, $2::uuid, 1, 'SUCCEEDED', repeat('a', 64),
		        '{"input": 1200, "cached_input": 200, "cache_write_input": 0, "output": 300, "reasoning_output": 50}')`, e.org, finished)
	queued := f.insertRun(f.newConversation(owner).ID, "QUEUED")
	// 运行历史带排队原因。
	e.exec(`UPDATE openfox.runs SET wait_reason = 'AGENT_CHAT_CONCURRENCY' WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, queued)

	var page struct {
		Runs []struct {
			ID         string  `json:"id"`
			WaitReason *string `json:"wait_reason"`
			StartedAt  *string `json:"started_at"`
			DurationMS *int64  `json:"duration_ms"`
			Usage      *struct {
				Input  int64 `json:"input"`
				Output int64 `json:"output"`
			} `json:"usage"`
			ConversationTitle *string `json:"conversation_title"`
		} `json:"runs"`
	}
	owner.must(http.StatusOK, http.MethodGet, agentPath(f.space, f.agentID)+"/runs", nil, &page)
	seen := 0
	for _, run := range page.Runs {
		switch run.ID {
		case finished:
			seen++
			if run.StartedAt == nil || run.DurationMS == nil || *run.DurationMS != 78500 || run.Usage == nil ||
				run.Usage.Input != 1200 || run.Usage.Output != 300 || run.WaitReason == nil || *run.WaitReason != "" {
				t.Fatalf("finished run: %+v", run)
			}
		case queued:
			seen++
			if run.StartedAt != nil || run.DurationMS != nil || run.Usage != nil ||
				run.WaitReason == nil || *run.WaitReason != "AGENT_CHAT_CONCURRENCY" {
				t.Fatalf("queued run: %+v", run)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("runs = %+v", page.Runs)
	}
}

// 摘要仅包含最近两条正文；动作载荷不能出现在卡片，且共享/私有可见性保持一致。
func TestConvRecentMessagePreviews(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, viewer := e.client(agMemberID), e.client(agManagerID)
	conversation := f.newConversation(owner)
	if got := f.get(owner, conversation.ID).RecentMessages; got == nil || len(got) != 0 {
		t.Fatalf("empty previews: %+v", got)
	}
	f.post(owner, conversation.ID, "old message")
	userSeq := f.post(owner, conversation.ID, "  latest\n user   message  ")
	_, agentSeq := f.agentMessage(conversation.ID, "TEXT")
	f.agentMessage(conversation.ID, "ACTION_EXECUTED")
	check := func(item convSummary) {
		t.Helper()
		got := item.RecentMessages
		if len(got) != 2 || got[0].Seq != userSeq || got[1].Seq != agentSeq || got[0].Preview != "latest user message" || got[1].AuthorKind != "AGENT" || got[1].AuthorName == "" {
			t.Fatalf("previews: %+v", got)
		}
	}
	check(f.get(owner, conversation.ID))
	var page convPage
	owner.must(http.StatusOK, http.MethodGet, "conversations?limit=1&agent_id="+f.agentID, nil, &page)
	if len(page.Conversations) != 1 {
		t.Fatalf("page: %+v", page)
	}
	check(page.Conversations[0])
	viewer.must(http.StatusOK, http.MethodGet, "conversations?agent_id="+f.agentID, nil, &page)
	if len(page.Conversations) != 0 {
		t.Fatal("private previews visible to non-member")
	}
	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(conversation.ID)+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	check(f.get(viewer, conversation.ID))
}

func TestConvSharedStatusMark(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, viewer := e.client(agMemberID), e.client(agManagerID)
	conv := f.newConversation(owner)
	path := conversationPath(conv.ID)
	mark := func(value string) map[string]any { return map[string]any{"status_mark": value} }
	viewer.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, path, mark("DONE"))
	owner.must(http.StatusNoContent, http.MethodPut, path+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	viewer.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch, path, mark("DISCARDED"))
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, path, mark("ABORTED"))
	base := f.get(owner, conv.ID)
	updateEvents := func() int {
		var n int
		e.scalar(&n, `SELECT count(*) FROM openfox.organization_events
		 WHERE organization_id=$1::uuid AND conversation_id=$2::uuid AND type='conversation.updated'`, e.org, conv.ID)
		return n
	}
	initialEvents := updateEvents()
	for _, value := range []string{"DONE", "DISCARDED", "DISCARDED"} {
		owner.must(http.StatusOK, http.MethodPatch, path, mark(value), nil)
		item := f.get(viewer, conv.ID)
		if item.StatusMark == nil || *item.StatusMark != value || item.Lifecycle != "OPEN" || item.Version != base.Version || item.Status != base.Status {
			t.Fatalf("shared mark changed execution facts: %+v", item)
		}
	}
	if events := updateEvents() - initialEvents; events != 2 {
		t.Fatalf("expected one refresh per change, got %d", events)
	}
	f.post(owner, conv.ID, "still writable")
	owner.must(http.StatusOK, http.MethodPatch, path, mark("NONE"), nil)
	if item := f.get(viewer, conv.ID); item.StatusMark != nil || item.Lifecycle != "OPEN" {
		t.Fatalf("clear: %+v", item)
	}
	owner.must(http.StatusOK, http.MethodPost, path+"/complete", map[string]any{"version": f.get(owner, conv.ID).Version}, nil)
	owner.expectError(http.StatusConflict, "CONVERSATION_CLOSED", http.MethodPatch, path, mark("DONE"))
	owner.must(http.StatusOK, http.MethodPost, path+"/reopen", map[string]any{"version": f.get(owner, conv.ID).Version}, nil)
	f.post(owner, conv.ID, "reopened")
}

func TestConvTaskTotalsAcrossPages(t *testing.T) {
	e := newAgEnv(t)
	f := newConvFixture(e)
	owner, viewer := e.client(agMemberID), e.client(agManagerID)
	task := f.createTask(f.space, "token totals")
	bind := func(c convSummary) {
		owner.must(http.StatusOK, http.MethodPost, conversationPath(c.ID)+"/task", map[string]any{"task_id": task}, nil)
	}
	active, archived, empty := f.newConversation(owner), f.newConversation(owner), f.newConversation(owner)
	f.insertRun(active.ID, "SUCCEEDED") // Before task binding, excluded from task totals.
	owner.must(http.StatusNoContent, http.MethodPut, conversationPath(active.ID)+"/members/"+agManagerID, map[string]any{"access_level": "VIEWER"}, nil)
	for _, c := range []convSummary{active, archived, empty} {
		bind(c)
	}
	writeUsage := func(convID, rt string) {
		id := f.insertRun(convID, "SUCCEEDED")
		e.exec(`UPDATE openfox.runs SET runtime=$3,model='default',approval_mode='FOLLOW_RUNTIME' WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, id, rt)
		e.exec(`INSERT INTO openfox.run_results (organization_id,run_id,generation,status,payload_hash,usage)
		VALUES ($1::uuid,$2::uuid,1,'SUCCEEDED',repeat('a',64),'{"input":300,"cached_input":100,"output":7,"cache_write_input":2}')`, e.org, id)
	}
	writeUsage(active.ID, "codex")
	writeUsage(archived.ID, "claude-code")
	f.insertRun(active.ID, "FAILED") // Terminal run without a usage report.
	owner.must(http.StatusOK, http.MethodPatch, conversationPath(archived.ID), map[string]any{"archived": true}, nil)
	type totals struct {
		Count    int `json:"conversation_count"`
		Runs     int `json:"run_count"`
		Reported int `json:"reported_runs"`
		Input    int `json:"input"`
		Output   int `json:"output"`
		Read     int `json:"cache_read"`
		Write    int `json:"cache_write"`
	}
	get := func(client *agClient, query string) (*totals, *string) {
		var result struct {
			Stats *totals `json:"task_stats"`
			Next  *string `json:"next_cursor"`
		}
		client.must(http.StatusOK, http.MethodGet, "conversations?limit=1&"+query, nil, &result)
		return result.Stats, result.Next
	}
	query := "task_id=" + task
	got, next := get(owner, query)
	if got == nil || *got != (totals{2, 3, 2, 500, 14, 200, 4}) || next == nil {
		t.Fatalf("owner totals %+v, next %v", got, next)
	}
	for _, filter := range []string{"&cursor=" + *next, "&q=other", "&archived=only"} {
		if got, _ := get(owner, query+filter); got != nil {
			t.Fatalf("unexpected totals for %s: %+v", filter, got)
		}
	}
	if got, _ := get(viewer, query); got == nil || *got != (totals{1, 2, 1, 200, 7, 100, 2}) {
		t.Fatalf("viewer totals %+v", got)
	}
	if got, _ := get(owner, "task_id="+agNewID(t)); got == nil || *got != (totals{}) {
		t.Fatalf("empty totals %+v", got)
	}
}
