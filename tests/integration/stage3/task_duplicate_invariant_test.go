package stage3

// OPE-17 · D1 第二期：服务端不变量「duplicate_of_task_id 非空，当且仅当状态类别为 DUPLICATE」。
// PATCH、批量接口、创建任务都不能在没有目标的情况下进入重复类别；离开重复类别时同一条 UPDATE 清空目标。

import (
	"net/http"
	"testing"
)

func TestDuplicateInvariant(t *testing.T) {
	env := newS3Env(t)
	// triageSpace 会给组织补上「重复」状态，新空间的工作流因此包含它。
	space, _ := triageSpace(t, env)
	todo := env.statusID(env.org, "TODO")
	duplicate := env.statusID(env.org, "DUPLICATE")
	base := "spaces/" + space.ID + "/tasks/"
	create := func(title string) triageTaskView {
		return triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "status_id": todo, "title": title + " " + s3Suffix(t)})
	}
	hasTarget := func(id string) bool {
		return env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, id) != 0
	}
	batch := func(id string, version int, statusID string) map[string]any {
		return map[string]any{"tasks": []map[string]any{{"id": id, "version": version}}, "patch": map[string]any{"status_id": statusID}}
	}
	source, target := create("Source"), create("Target")

	// 三个入口直接选重复状态且没有目标：一律 400 INVALID_DUPLICATE，任务不变。
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE", http.MethodPost, "spaces/"+space.ID+"/tasks",
		map[string]any{"submit_to_triage": false, "status_id": duplicate, "title": "Born duplicate " + s3Suffix(t)})
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE", http.MethodPatch, base+source.ID,
		map[string]any{"version": source.Version, "status_id": duplicate})
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE", http.MethodPost, "spaces/"+space.ID+"/tasks/batch-update",
		batch(source.ID, source.Version, duplicate))
	if got := getTriageTask(t, env.member(), space.ID, source.ID); got.StatusCategory != "TODO" || got.Version != source.Version {
		t.Fatalf("rejected requests changed the task: %+v", got)
	}

	// 经 POST /duplicate 标记：状态与目标一起写入。
	var marked triageTaskView
	env.member().must(http.StatusOK, http.MethodPost, base+source.ID+"/duplicate",
		map[string]any{"version": source.Version, "duplicate_of_task_id": target.ID}, &marked)
	if marked.StatusCategory != "DUPLICATE" || !hasTarget(source.ID) {
		t.Fatalf("marked = %+v", marked)
	}

	// PATCH 离开重复类别：目标被清空。
	var left triageTaskView
	env.member().must(http.StatusOK, http.MethodPatch, base+source.ID, map[string]any{"version": marked.Version, "status_id": todo}, &left)
	if left.StatusCategory != "TODO" || left.DuplicateOf != nil || hasTarget(source.ID) {
		t.Fatalf("left via PATCH = %+v", left)
	}

	// 批量离开重复类别：同样清空目标。
	env.member().must(http.StatusOK, http.MethodPost, base+source.ID+"/duplicate",
		map[string]any{"version": left.Version, "duplicate_of_task_id": target.ID}, &marked)
	if !hasTarget(source.ID) {
		t.Fatal("second mark did not write the target")
	}
	env.member().must(http.StatusOK, http.MethodPost, "spaces/"+space.ID+"/tasks/batch-update", batch(source.ID, marked.Version, todo), nil)
	if hasTarget(source.ID) {
		t.Fatal("batch leave kept the duplicate target")
	}

	// 分拣台标记重复仍成功：状态与目标同一次写入。新空间默认没启用分拣台，先启用，任务才会进入分拣队列。
	patchTriage(t, env, space.ID, map[string]any{"enabled": true, "action": "notify"})
	queued := triageTask(t, env.member(), space.ID, map[string]any{"title": "Queued " + s3Suffix(t)})
	if queued.StatusCategory != "TRIAGE" {
		t.Fatalf("task did not enter triage: %+v", queued)
	}
	env.manager().must(http.StatusOK, http.MethodPost, base+queued.ID+"/triage/duplicate",
		map[string]any{"version": queued.Version, "duplicate_of_task_id": target.ID}, nil)
	if !hasTarget(queued.ID) {
		t.Fatal("triage duplicate lost the target")
	}
}
