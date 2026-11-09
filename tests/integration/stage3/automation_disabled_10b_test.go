package stage3

// 停用的自动化规则也可以手动运行一次：停用只拦自动触发（定时、任务状态、评论），
// 手动「立即运行」照常建会话、trigger_kind = manual；规则保持停用，之后的自动触发仍然跳过。

import (
	"net/http"
	"testing"
	"time"
)

func TestDisabledAutomationRunsManuallyOnly(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	owner := env.manager()
	done := env.statusID(env.org, "DONE")
	created := env.createAutomation(agent,
		scheduleTrigger("hourly", map[string]any{"minute": 0}),
		map[string]any{"kind": "task_status", "enabled": true, "config": map[string]any{"status_ids": []string{done}}},
		map[string]any{"kind": "comment", "enabled": true, "config": map[string]any{"contains": "deploy"}})
	id := created.Automation.ID
	base := automationsPath(env.space.ID) + "/" + id
	owner.must(http.StatusOK, http.MethodPatch, base, map[string]any{"version": created.Automation.Version, "enabled": false}, nil)

	// 手动运行：201，排队并建会话，记录为 manual。
	var run runView
	owner.must(http.StatusCreated, http.MethodPost, base+"/run", nil, &run)
	if run.Status != "queued" || run.TriggerKind != "manual" || run.ConversationID == nil {
		t.Fatalf("manual run while disabled = %+v, want queued manual run with a conversation", run)
	}
	// 规则仍是停用状态，手动运行不改它。
	var enabled bool
	env.scalar(&enabled, `SELECT enabled FROM openfox.automations WHERE id = $1::uuid`, id)
	if enabled {
		t.Fatal("manual run re-enabled the automation")
	}
	// 权限不变：空间只读成员不能运行。
	env.addSpaceMember(env.space.ID, s3OtherID, "VIEWER")
	env.other().expectError(http.StatusForbidden, "", http.MethodPost, base+"/run", nil)

	// 自动触发全部跳过：定时到期、任务进入完成、评论命中关键词都不建记录。
	env.exec(`UPDATE openfox.automation_triggers SET next_run_at = clock_timestamp() - interval '2 hours'
		WHERE organization_id = $1::uuid AND automation_id = $2::uuid AND kind = 'schedule'`, env.org, id)
	task := env.createTask(owner, env.space.ID, map[string]any{"title": "Ship it"})
	env.setStatus(owner, env.space.ID, task, "DONE")
	postComment(env, task, "Please deploy tonight")
	env.runWorker(time.Now())
	for _, kind := range []string{"schedule", "task_status", "comment"} {
		if n := env.runCount(id, kind); n != 0 {
			t.Fatalf("%s runs while disabled = %d, want 0", kind, n)
		}
	}
	if n := env.runCount(id, "manual"); n != 1 {
		t.Fatalf("manual runs = %d, want 1", n)
	}
}
