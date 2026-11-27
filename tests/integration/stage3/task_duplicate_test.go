package stage3

// OPE-17 · 通用「标记为重复」接口集成测试：不要求任务在分拣台中；指定状态须是工作流里的重复类别；
// 目标校验与分拣台一致；权限同更新任务；停用空间只读；分拣台接口原有行为不变。

import (
	"net/http"
	"testing"
)

func TestMarkTaskDuplicate(t *testing.T) {
	env := newS3Env(t)
	// triageSpace 会给组织补上「重复」状态，新空间的工作流因此包含它。
	space, _ := triageSpace(t, env)
	todo := env.statusID(env.org, "TODO")
	done := env.statusID(env.org, "DONE")
	duplicate := env.statusID(env.org, "DUPLICATE")
	base := "spaces/" + space.ID + "/tasks/"
	create := func(title string) triageTaskView {
		return triageTask(t, env.member(), space.ID, map[string]any{"submit_to_triage": false, "status_id": todo, "title": title + " " + s3Suffix(t)})
	}
	source, target, other := create("Source"), create("Target"), create("Other")
	path := base + source.ID + "/duplicate"
	body := func(version int, extra map[string]any) map[string]any {
		out := map[string]any{"version": version, "duplicate_of_task_id": target.ID}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// 目标不能是自己，也不能是不存在的任务。
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE", http.MethodPost, path,
		body(source.Version, map[string]any{"duplicate_of_task_id": source.ID}))
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE", http.MethodPost, path,
		body(source.Version, map[string]any{"duplicate_of_task_id": s3NewID(t)}))
	// 指定的状态必须在工作流里且属于重复类别。
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE_STATUS", http.MethodPost, path,
		body(source.Version, map[string]any{"status_id": done}))
	env.member().expectError(http.StatusBadRequest, "INVALID_DUPLICATE_STATUS", http.MethodPost, path,
		body(source.Version, map[string]any{"status_id": s3NewID(t)}))
	// 空间查看者不能改任务；跨租户看不到。
	env.other().expectError(http.StatusForbidden, "", http.MethodPost, path, body(source.Version, nil))
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, path, body(source.Version, nil))
	// 版本不符时冲突，不写任何东西。
	env.member().expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, path, body(source.Version+5, nil))
	if env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND duplicate_of_task_id IS NOT NULL`, source.ID) != 0 {
		t.Fatal("rejected requests wrote a duplicate target")
	}

	// 普通任务（不在分拣台）可以标记为重复：状态改到重复类别、写目标、目标任务记一条动态。
	var marked triageTaskView
	env.member().must(http.StatusOK, http.MethodPost, path, body(source.Version, nil), &marked)
	if marked.StatusCategory != "DUPLICATE" || marked.StatusID != duplicate || marked.DuplicateOf == nil || marked.DuplicateOf.ID != target.ID {
		t.Fatalf("marked = %+v", marked)
	}
	if env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.duplicate_marked'`, target.ID) != 1 ||
		env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.status_changed'`, source.ID) != 1 {
		t.Fatal("duplicate or status activity missing")
	}

	// 已是重复时可以改指向，显式指定重复状态同样有效。
	env.member().must(http.StatusOK, http.MethodPost, path, map[string]any{"version": marked.Version, "duplicate_of_task_id": other.ID, "status_id": duplicate}, &marked)
	if marked.StatusID != duplicate || marked.DuplicateOf == nil || marked.DuplicateOf.ID != other.ID {
		t.Fatalf("remarked = %+v", marked)
	}
	if env.count(`SELECT count(*) FROM openfox.task_activities WHERE task_id = $1::uuid AND kind = 'task.duplicate_marked'`, other.ID) != 1 {
		t.Fatal("duplicate activity missing on the new target")
	}

	// 分拣台接口行为不变：不在分拣台的任务仍然拒绝。
	env.member().expectError(http.StatusConflict, "NOT_IN_TRIAGE", http.MethodPost, base+target.ID+"/triage/duplicate",
		map[string]any{"version": target.Version, "duplicate_of_task_id": other.ID})

	// 停用空间只读。
	env.exec(`UPDATE openfox.spaces SET status = 'ARCHIVED', retired_at = clock_timestamp(), retired_by = $2::uuid WHERE id = $1::uuid`, space.ID, s3OwnerID)
	env.member().expectError(http.StatusConflict, "SPACE_RETIRED", http.MethodPost, base+target.ID+"/duplicate",
		map[string]any{"version": target.Version, "duplicate_of_task_id": other.ID})
}
