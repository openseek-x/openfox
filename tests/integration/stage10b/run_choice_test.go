package stage10b

// 指派时选择运行对话：
//   - PATCH 任务 assignment_conversation = new：旧指派会话降级为普通会话（origin = USER），worker 新建指派会话并在新会话里发消息；
//     reuse（或不传）：消息发在原指派会话里；
//   - 再次运行接口 POST …/tasks/{id}/agent-runs：权限（与编辑任务相同）、处理人与状态校验、reuse / new 语义；
//   - 并发两次 new：任何时刻最多一个打开的指派会话，事件都被处理。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"openfox/internal/dispatch"
)

type runSpace struct {
	env   *qcEnv
	id    string
	agent string
	other string // 同空间另一个 Agent（未指派）
}

func newRunSpace(t *testing.T) runSpace {
	env := newQcEnv(t)
	space := env.createSpace(nil)
	env.addSpaceMember(space.ID, qcMemberID, "MEMBER")
	env.addSpaceMember(space.ID, qcAdminID, "EDITOR")
	env.addSpaceMember(space.ID, qcGuestID, "VIEWER")
	return runSpace{env: env, id: space.ID,
		agent: env.seedAgent(space.ID, qcOwnerID, "SHARED"), other: env.seedAgent(space.ID, qcOwnerID, "SHARED")}
}

func (e *qcEnv) statusID(name string) string {
	e.t.Helper()
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = $2`, e.org, name)
	return id
}

// assignedTask 由 member 建一个待办任务并直接指派给 agent，跑一轮派发，返回任务与首个指派会话。
func (s runSpace) assignedTask() (taskView, string) {
	e := s.env
	e.t.Helper()
	task := e.createTask(e.member(), s.id, map[string]any{"status_id": e.statusID("Todo"),
		"assignees": []map[string]string{{"kind": "AGENT", "id": s.agent}}})
	e.dispatchOnce()
	return task, s.openAssignment(task.ID)
}

// openAssignment 返回任务 + Agent 唯一的打开状态指派会话（不是恰好一个则失败）。
func (s runSpace) openAssignment(taskID string) string {
	e := s.env
	e.t.Helper()
	if n := s.openAssignmentCount(taskID); n != 1 {
		e.t.Fatalf("open assignment conversations = %d, want 1", n)
	}
	var id string
	e.scalar(&id, `SELECT id::text FROM openfox.conversations WHERE task_id = $1::uuid AND agent_id = $2::uuid
		AND origin = 'TASK_ASSIGNMENT' AND lifecycle = 'OPEN' AND archived_at IS NULL`, taskID, s.agent)
	return id
}

func (s runSpace) openAssignmentCount(taskID string) int {
	return s.env.count(`SELECT count(*) FROM openfox.conversations WHERE task_id = $1::uuid AND agent_id = $2::uuid
		AND origin = 'TASK_ASSIGNMENT' AND lifecycle = 'OPEN' AND archived_at IS NULL`, taskID, s.agent)
}

// assignMessages 是会话里 worker 发出的「请处理任务 …」消息条数。
func (e *qcEnv) assignMessages(conversationID string) int {
	return e.count(`SELECT count(*) FROM openfox.messages WHERE conversation_id = $1::uuid
		AND author_kind = 'USER' AND body LIKE '请处理任务 %'`, conversationID)
}

func (e *qcEnv) origin(conversationID string) string {
	e.t.Helper()
	var origin string
	e.scalar(&origin, `SELECT origin || ':' || lifecycle || ':' || visibility || ':' || (archived_at IS NULL)::text
		FROM openfox.conversations WHERE id = $1::uuid`, conversationID)
	return origin
}

func (e *qcEnv) pendingEvents(taskID string) int {
	return e.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = $1::uuid AND processed_at IS NULL`, taskID)
}

// patchAssignees 由 member 改处理人；mode 为空时不传 assignment_conversation。
func (s runSpace) patchAssignees(taskID string, agents []string, mode string) taskView {
	e := s.env
	e.t.Helper()
	current := e.getTask(e.member(), s.id, taskID)
	assignees := []map[string]string{}
	for _, id := range agents {
		assignees = append(assignees, map[string]string{"kind": "AGENT", "id": id})
	}
	body := map[string]any{"assignees": assignees, "version": current.Version}
	if mode != "" {
		body["assignment_conversation"] = mode
	}
	var task taskView
	e.member().must(http.StatusOK, http.MethodPatch, taskPath(s.id, taskID), body, &task)
	return task
}

func runsPath(spaceID, taskID string) string { return taskPath(spaceID, taskID) + "/agent-runs" }

func TestAssignmentConversationChoice(t *testing.T) {
	s := newRunSpace(t)
	e := s.env
	task, first := s.assignedTask()
	if got := e.assignMessages(first); got != 1 {
		t.Fatalf("first assignment messages = %d", got)
	}

	// reuse（不传字段，旧客户端的行为）：取消指派后再指派，消息发在原指派会话里。
	s.patchAssignees(task.ID, nil, "")
	e.dispatchOnce()
	s.patchAssignees(task.ID, []string{s.agent}, "")
	e.dispatchOnce()
	if got := s.openAssignment(task.ID); got != first || e.assignMessages(first) != 2 {
		t.Fatalf("reuse: open=%s messages=%d, want %s / 2", got, e.assignMessages(first), first)
	}
	// 显式 reuse 同样复用。
	s.patchAssignees(task.ID, nil, "")
	e.dispatchOnce()
	s.patchAssignees(task.ID, []string{s.agent}, "reuse")
	e.dispatchOnce()
	if got := s.openAssignment(task.ID); got != first || e.assignMessages(first) != 3 {
		t.Fatalf("explicit reuse: open=%s messages=%d", got, e.assignMessages(first))
	}

	// new：PATCH 提交时旧会话已降级为普通会话（同一事务），可见性 / 生命周期 / 版本都不变；worker 新建指派会话并发消息。
	s.patchAssignees(task.ID, nil, "")
	e.dispatchOnce()
	var versionBefore int
	e.scalar(&versionBefore, `SELECT version FROM openfox.conversations WHERE id = $1::uuid`, first)
	s.patchAssignees(task.ID, []string{s.agent}, "new")
	if got := e.origin(first); got != "USER:OPEN:SHARED:true" {
		t.Fatalf("demoted conversation = %s", got)
	}
	var versionAfter int
	e.scalar(&versionAfter, `SELECT version FROM openfox.conversations WHERE id = $1::uuid`, first)
	if versionAfter != versionBefore {
		t.Fatalf("demotion changed version %d -> %d", versionBefore, versionAfter)
	}
	if n := s.openAssignmentCount(task.ID); n != 0 {
		t.Fatalf("open assignment conversations before dispatch = %d", n)
	}
	e.dispatchOnce()
	second := s.openAssignment(task.ID)
	if second == first || e.assignMessages(second) != 1 || e.assignMessages(first) != 3 {
		t.Fatalf("new: second=%s messages(second)=%d messages(first)=%d", second, e.assignMessages(second), e.assignMessages(first))
	}
	// 降级后的旧会话仍挂在任务上，任务会话列表里还能看到。
	var page struct {
		Conversations []struct {
			ID     string `json:"id"`
			Origin string `json:"origin"`
		} `json:"conversations"`
	}
	e.member().must(http.StatusOK, http.MethodGet, "conversations?task_id="+task.ID, nil, &page)
	origins := map[string]string{}
	for _, item := range page.Conversations {
		origins[item.ID] = item.Origin
	}
	if origins[first] != "USER" || origins[second] != "TASK_ASSIGNMENT" {
		t.Fatalf("task conversations = %+v", page.Conversations)
	}

	// new 只影响本次新增的 Agent 处理人：只改标题、或已在处理人里的 Agent 都不降级。
	current := e.getTask(e.member(), s.id, task.ID)
	e.member().must(http.StatusOK, http.MethodPatch, taskPath(s.id, task.ID),
		map[string]any{"title": "改个标题", "assignment_conversation": "new", "version": current.Version}, nil)
	s.patchAssignees(task.ID, []string{s.agent, s.other}, "new")
	if got := s.openAssignment(task.ID); got != second {
		t.Fatalf("existing assignee demoted: open=%s want %s", got, second)
	}
	e.dispatchOnce()

	// 非法取值：400，且不写入。
	current = e.getTask(e.member(), s.id, task.ID)
	for _, value := range []any{"bogus", "", "NEW", 1} {
		e.member().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, taskPath(s.id, task.ID),
			map[string]any{"assignees": []map[string]string{}, "assignment_conversation": value, "version": current.Version})
	}
	if after := e.getTask(e.member(), s.id, task.ID); after.Version != current.Version || len(after.Assignees) != 2 {
		t.Fatalf("invalid PATCH wrote: %+v", after)
	}
}

func TestStartTaskAgentRun(t *testing.T) {
	s := newRunSpace(t)
	e := s.env
	task, first := s.assignedTask()
	path := runsPath(s.id, task.ID)

	// reuse（默认）：不改处理人与任务版本，消息发在原指派会话里。
	before := e.getTask(e.member(), s.id, task.ID)
	var accepted struct {
		TaskID                 string `json:"task_id"`
		AgentID                string `json:"agent_id"`
		AssignmentConversation string `json:"assignment_conversation"`
	}
	e.member().must(http.StatusAccepted, http.MethodPost, path, map[string]any{"agent_id": s.agent}, &accepted)
	if accepted.TaskID != task.ID || accepted.AgentID != s.agent || accepted.AssignmentConversation != "reuse" {
		t.Fatalf("accepted = %+v", accepted)
	}
	if after := e.getTask(e.member(), s.id, task.ID); after.Version != before.Version {
		t.Fatalf("agent run changed task version %d -> %d", before.Version, after.Version)
	}
	e.dispatchOnce()
	if got := s.openAssignment(task.ID); got != first || e.assignMessages(first) != 2 {
		t.Fatalf("rerun reuse: open=%s messages=%d", got, e.assignMessages(first))
	}
	// 事件的发起人是调用者，处理结果为 started。
	var outcome string
	e.scalar(&outcome, `SELECT outcome FROM openfox.task_agent_events WHERE task_id = $1::uuid ORDER BY created_at DESC LIMIT 1`, task.ID)
	if outcome != "started" {
		t.Fatalf("rerun outcome = %s", outcome)
	}

	// new：旧会话立即降级，worker 新建指派会话。空间管理员同样可以。
	e.client(qcAdminID).must(http.StatusAccepted, http.MethodPost, path,
		map[string]any{"agent_id": s.agent, "assignment_conversation": "new"}, &accepted)
	if accepted.AssignmentConversation != "new" || e.origin(first) != "USER:OPEN:SHARED:true" {
		t.Fatalf("rerun new: accepted=%+v first=%s", accepted, e.origin(first))
	}
	e.dispatchOnce()
	second := s.openAssignment(task.ID)
	if second == first || e.assignMessages(second) != 1 {
		t.Fatalf("rerun new: second=%s messages=%d", second, e.assignMessages(second))
	}

	// 参数校验：400 INVALID_REQUEST。
	for _, body := range []map[string]any{
		{"agent_id": s.agent, "assignment_conversation": "bogus"},
		{"agent_id": s.agent, "assignment_conversation": ""},
		{"agent_id": s.agent, "extra": true},
		{"agent_id": "not-a-uuid"},
		{},
	} {
		e.member().expectError(http.StatusBadRequest, "", http.MethodPost, path, body)
	}
	// 不是处理人：409 TASK_AGENT_NOT_ASSIGNED。
	e.member().expectError(http.StatusConflict, "TASK_AGENT_NOT_ASSIGNED", http.MethodPost, path, map[string]any{"agent_id": s.other})
	// 权限：空间只读成员 403；别的组织的人 / 不存在的任务 / 别的空间路径 404。
	e.guest().expectError(http.StatusForbidden, "", http.MethodPost, path, map[string]any{"agent_id": s.agent})
	outsider := e.outsider().do(http.MethodPost, path, map[string]any{"agent_id": s.agent})
	if outsider.Status != http.StatusForbidden && outsider.Status != http.StatusNotFound {
		t.Fatalf("outsider = %d %s", outsider.Status, outsider.Body)
	}
	e.member().expectError(http.StatusNotFound, "", http.MethodPost, runsPath(s.id, qcNewID(t)), map[string]any{"agent_id": s.agent})
	other := e.createSpace(nil)
	e.owner().expectError(http.StatusNotFound, "", http.MethodPost, runsPath(other.ID, task.ID), map[string]any{"agent_id": s.agent})
	// 拒绝的请求都没有入队事件、没有降级会话。
	if n := e.pendingEvents(task.ID); n != 0 {
		t.Fatalf("rejected requests enqueued %d events", n)
	}
	if got := s.openAssignment(task.ID); got != second {
		t.Fatalf("rejected requests changed assignment conversation: %s", got)
	}

	// 任务状态不允许运行（已完成）：409 TASK_AGENT_RUN_NOT_ALLOWED，new 也不降级。
	current := e.getTask(e.member(), s.id, task.ID)
	e.member().must(http.StatusOK, http.MethodPatch, taskPath(s.id, task.ID),
		map[string]any{"status_id": e.statusID("Done"), "version": current.Version}, nil)
	e.member().expectError(http.StatusConflict, "TASK_AGENT_RUN_NOT_ALLOWED", http.MethodPost, path,
		map[string]any{"agent_id": s.agent, "assignment_conversation": "new"})
	if got := s.openAssignment(task.ID); got != second {
		t.Fatalf("done task demoted: %s", got)
	}
	// 已删除的任务：404（另建一个任务删除；指派过的任务有活跃 Run 时不能删除）。
	removed := e.createTask(e.member(), s.id, map[string]any{"status_id": e.statusID("Todo")})
	e.member().must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s?version=%d", taskPath(s.id, removed.ID), removed.Version), nil, nil)
	e.member().expectError(http.StatusNotFound, "", http.MethodPost, runsPath(s.id, removed.ID), map[string]any{"agent_id": s.agent})
	if n := e.pendingEvents(task.ID) + e.pendingEvents(removed.ID); n != 0 {
		t.Fatalf("rejected requests enqueued %d events", n)
	}
}

// concurrentPost 在 goroutine 里发请求（不调用 t.Fatal），返回状态码。
func (c *qcClient) concurrentPost(path string, body any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(c.env.ctx, http.MethodPost,
		c.env.server.URL+"/v1/organizations/"+c.env.org+"/"+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", c.csrf)
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusAccepted {
		return response.StatusCode, fmt.Errorf("status %d: %s", response.StatusCode, payload)
	}
	return response.StatusCode, nil
}

func (e *qcEnv) tryDispatch(ctx context.Context) error {
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		return err
	}
	worker := &dispatch.Worker{Pool: e.database, Store: store, Hub: dispatch.NewHub()}
	_, err = worker.RunOrganization(ctx, e.org)
	return err
}

// 并发两次 new（以及与 worker 交错）：任务行锁让两次降级串行，部分唯一索引 + worker 撞索引后改用已有会话，
// 保证任何时候最多一个打开的指派会话；所有事件都被处理（started），没有失败。
func TestConcurrentNewAssignmentConversation(t *testing.T) {
	s := newRunSpace(t)
	e := s.env
	task, _ := s.assignedTask()
	path := runsPath(s.id, task.ID)
	clients := []*qcClient{e.member(), e.owner(), e.client(qcAdminID)}
	for round := 0; round < 4; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, len(clients)+1)
		for _, client := range clients {
			wg.Add(1)
			go func(client *qcClient) {
				defer wg.Done()
				if _, err := client.concurrentPost(path, map[string]any{"agent_id": s.agent, "assignment_conversation": "new"}); err != nil {
					errs <- err
				}
			}(client)
		}
		// 偶数轮让 worker 与请求同时跑。
		if round%2 == 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := e.tryDispatch(e.ctx); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: %v", round, err)
		}
		if n := s.openAssignmentCount(task.ID); n > 1 {
			t.Fatalf("round %d: open assignment conversations = %d", round, n)
		}
		e.dispatchOnce()
		s.openAssignment(task.ID)
		if n := e.pendingEvents(task.ID); n != 0 {
			t.Fatalf("round %d: pending events = %d", round, n)
		}
	}
	if failed := e.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = $1::uuid AND outcome <> 'started'`, task.ID); failed != 0 {
		var outcomes string
		e.scalar(&outcomes, `SELECT string_agg(outcome, ',') FROM openfox.task_agent_events WHERE task_id = $1::uuid`, task.ID)
		t.Fatalf("events not started: %s", outcomes)
	}
	// 每次 ASSIGNED 事件都发了一条消息（首次指派 + 4 轮 × 3 次）。
	total := e.count(`SELECT count(*) FROM openfox.messages m JOIN openfox.conversations c ON c.id = m.conversation_id
		WHERE c.task_id = $1::uuid AND m.author_kind = 'USER' AND m.body LIKE '请处理任务 %'`, task.ID)
	if total != 1+4*len(clients) {
		t.Fatalf("assignment messages = %d, want %d", total, 1+4*len(clients))
	}
}
