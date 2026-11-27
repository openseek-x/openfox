package stage1

// OPE-17 · D1 第二期：结束会话并同时完成任务（complete_task=true）走 UpdateTask 写 DONE，
// 重复任务由此离开重复类别时，重复目标必须一并清空。

import (
	"net/http"
	"testing"
)

func TestDuplicateInvariantConversationComplete(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	todo := e.statuses["Todo"]
	target := e.newTask(e.owner, sp, map[string]any{"status_id": todo})
	source := e.newTask(e.owner, sp, map[string]any{"status_id": todo})

	var marked tkTask
	e.owner.must(http.StatusOK, http.MethodPost, taskPath(sp, source.ID)+"/duplicate",
		map[string]any{"version": source.Version, "duplicate_of_task_id": target.ID}, &marked)
	var category string
	var targets int
	state := func() {
		t.Helper()
		e.scalar(&category, `SELECT ts.category FROM openfox.tasks t
			JOIN openfox.task_statuses ts ON ts.organization_id = t.organization_id AND ts.id = t.status_id
			 WHERE t.id = $1::uuid`, source.ID)
		e.scalar(&targets, `SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, source.ID)
	}
	state()
	if category != "DUPLICATE" || targets != 1 {
		t.Fatalf("setup: category=%s targets=%d", category, targets)
	}

	agent := e.insertAgent(sp.ID, cfgOwnerID, "SHARED")
	var conversation struct{ ID string }
	e.owner.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]string{"space_id": sp.ID, "task_id": source.ID, "agent_id": agent, "visibility": "PRIVATE"}, &conversation)
	var version int
	e.scalar(&version, `SELECT version FROM openfox.conversations WHERE id = $1::uuid`, conversation.ID)

	// 结束会话并完成任务：任务进入 DONE，重复目标随之清空。
	e.owner.must(http.StatusOK, http.MethodPost, "conversations/"+conversation.ID+"/complete",
		map[string]any{"version": version, "complete_task": true}, nil)
	state()
	if category != "DONE" || targets != 0 {
		t.Fatalf("after complete: category=%s targets=%d", category, targets)
	}
}
