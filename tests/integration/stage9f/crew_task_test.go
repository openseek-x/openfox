package stage9f

// 小队会话挂任务（快捷指令发给小队）：创建、任务校验、无 @ 时由队长执行且 Run 带 task_id、运行上下文带任务快照；
// Agent 任务会话的行为不变。

import (
	"net/http"
	"testing"
)

type qcConversation struct {
	ID      string  `json:"id"`
	AgentID string  `json:"agent_id"`
	CrewID  string  `json:"crew_id"`
	TaskID  *string `json:"task_id"`
}

func (e *qcEnv) todoTask(client *qcClient, spaceID string) taskView {
	e.t.Helper()
	var status string
	e.scalar(&status, `SELECT id::text FROM openfox.task_statuses WHERE organization_id = $1::uuid AND name = 'Todo'`, e.org)
	return e.createTask(client, spaceID, map[string]any{"status_id": status})
}

// sendAndRun 发一条消息并跑一轮派发，返回会话最新 Run 的 (id, agent_id, task_id)。
func (e *qcEnv) sendAndRun(client *qcClient, conversationID, body string, mentions []map[string]string) (string, string, string) {
	e.t.Helper()
	client.must(http.StatusCreated, http.MethodPost, "conversations/"+conversationID+"/messages",
		map[string]any{"body": body, "client_message_id": qcNewID(e.t), "mentions": mentions}, nil)
	e.dispatchOnce()
	var runID, agentID, taskID string
	e.scalar(&runID, `SELECT id::text FROM openfox.runs WHERE conversation_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, conversationID)
	e.scalar(&agentID, `SELECT agent_id::text FROM openfox.runs WHERE id = $1::uuid`, runID)
	e.scalar(&taskID, `SELECT COALESCE(task_id::text, '') FROM openfox.runs WHERE id = $1::uuid`, runID)
	return runID, agentID, taskID
}

func TestCrewTaskConversation(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member := e.member()
	leader := e.seedAgent(s.id, qcOwnerID, "SHARED")
	worker := e.seedAgent(s.id, qcOwnerID, "SHARED")
	crew := e.seedCrew(s.id, qcOwnerID, "SHARED", leader, worker)
	task := e.todoTask(e.owner(), s.id)
	otherTask := e.todoTask(e.owner(), s.other)

	// 任务校验与 Agent 会话相同：别的空间 400，不存在 404。
	member.expectError(http.StatusBadRequest, "", http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "crew_id": crew, "task_id": otherTask.ID, "visibility": "SHARED"})
	member.expectError(http.StatusNotFound, "", http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "crew_id": crew, "task_id": qcNewID(t), "visibility": "SHARED"})

	var conversation qcConversation
	member.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "crew_id": crew, "task_id": task.ID, "visibility": "SHARED"}, &conversation)
	if conversation.CrewID != crew || conversation.AgentID != leader || conversation.TaskID == nil || *conversation.TaskID != task.ID {
		t.Fatalf("crew task conversation = %+v", conversation)
	}
	// 出现在任务的会话列表里。
	var page struct {
		Conversations []qcConversation `json:"conversations"`
	}
	member.must(http.StatusOK, http.MethodGet, "conversations?task_id="+task.ID, nil, &page)
	if len(page.Conversations) != 1 || page.Conversations[0].ID != conversation.ID {
		t.Fatalf("task conversations = %+v", page.Conversations)
	}
	// 不 @ 任何人：交给队长，Run 带 task_id，运行上下文带任务快照。
	runID, agentID, taskID := e.sendAndRun(member, conversation.ID, "请处理这个任务。", nil)
	if agentID != leader || taskID != task.ID {
		t.Fatalf("leader run agent=%s task=%s", agentID, taskID)
	}
	if snapshot := e.runTaskContext(runID); snapshot == nil || snapshot.ID != task.ID || snapshot.DisplayID != task.DisplayID {
		t.Fatalf("run task context = %+v", snapshot)
	}
	// @ 成员：由该成员执行，同样带 task_id。
	var second qcConversation
	member.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "crew_id": crew, "task_id": task.ID, "visibility": "SHARED"}, &second)
	_, agentID, taskID = e.sendAndRun(member, second.ID, "请评审", []map[string]string{{"agent_id": worker}})
	if agentID != worker || taskID != task.ID {
		t.Fatalf("member run agent=%s task=%s", agentID, taskID)
	}
}

// Agent 任务会话（快捷指令发给 Agent 的路径）行为不变：@ 绑定 Agent 触发 Run 带 task_id；任务校验同前。
func TestAgentTaskConversationUnchanged(t *testing.T) {
	s := newQcSpace(t)
	e := s.env
	member := e.member()
	task := e.todoTask(e.owner(), s.id)
	otherTask := e.todoTask(e.owner(), s.other)
	member.expectError(http.StatusBadRequest, "", http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "agent_id": s.shared, "task_id": otherTask.ID, "visibility": "SHARED"})
	member.expectError(http.StatusNotFound, "", http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "agent_id": s.shared, "task_id": qcNewID(t), "visibility": "SHARED"})
	var conversation qcConversation
	member.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": s.id, "agent_id": s.shared, "task_id": task.ID, "visibility": "SHARED"}, &conversation)
	if conversation.CrewID != "" || conversation.AgentID != s.shared || conversation.TaskID == nil || *conversation.TaskID != task.ID {
		t.Fatalf("agent task conversation = %+v", conversation)
	}
	runID, agentID, taskID := e.sendAndRun(member, conversation.ID, "请澄清", []map[string]string{{"agent_id": s.shared}})
	if agentID != s.shared || taskID != task.ID {
		t.Fatalf("agent run agent=%s task=%s", agentID, taskID)
	}
	if snapshot := e.runTaskContext(runID); snapshot == nil || snapshot.ID != task.ID {
		t.Fatalf("agent run task context = %+v", snapshot)
	}
}
