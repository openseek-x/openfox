package stage3

// OPE-17 · D1 第二期：绕过 UpdateTask 的原生 SQL 写入点也要守住不变量。
// Git 同步把重复任务重开时清空目标；回收站恢复（类型未归档、状态不变）时保留目标。

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

func TestDuplicateInvariantRawPaths(t *testing.T) {
	env := newS3Env(t)
	// triageSpace 会给组织补上「重复」状态，新空间的工作流因此包含它。
	space, _ := triageSpace(t, env)
	todo := env.statusID(env.org, "TODO")
	base := "spaces/" + space.ID + "/tasks/"
	create := func(title string) triageTaskView {
		return triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "status_id": todo, "title": title + " " + s3Suffix(t)})
	}
	hasTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, id) != 0
	}
	mark := func(task triageTaskView, targetID string) triageTaskView {
		var marked triageTaskView
		env.member().must(http.StatusOK, http.MethodPost, base+task.ID+"/duplicate",
			map[string]any{"version": task.Version, "duplicate_of_task_id": targetID}, &marked)
		if marked.StatusCategory != "DUPLICATE" || !hasTarget(task.ID) {
			t.Fatalf("marked = %+v", marked)
		}
		return marked
	}
	target := create("Target")

	// Git 同步（Issue 重新打开）把重复任务移到 TODO：目标随状态一起清空。
	git := mark(create("Git"), target.ID)
	if err := completionTx(env, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		_, err := (collaboration.Service{}).GitSyncStatus(ctx, tx, git.ID, false, false, actor)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := getTriageTask(t, env.member(), space.ID, git.ID); got.StatusCategory == "DUPLICATE" || got.DuplicateOf != nil || hasTarget(git.ID) {
		t.Fatalf("git reopen kept the duplicate target: %+v", got)
	}

	// 回收站恢复：状态没变，重复目标必须保留。
	trashed := mark(create("Trashed"), target.ID)
	env.member().must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("%s%s?version=%d", base, trashed.ID, trashed.Version), nil, nil)
	var restored triageTaskView
	env.member().must(http.StatusOK, http.MethodPost, base+trashed.ID+"/restore", map[string]any{"version": trashed.Version + 1}, &restored)
	if restored.StatusCategory != "DUPLICATE" || restored.DuplicateOf == nil || restored.DuplicateOf.ID != target.ID || !hasTarget(trashed.ID) {
		t.Fatalf("restore lost the duplicate target: %+v", restored)
	}
}
