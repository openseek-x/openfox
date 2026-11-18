package stage9c

import (
	"net/http"
	"testing"
)

// 失败场景：权限（源 / 目标任一方）、目标不可用、跨组织、版本冲突、Idempotency-Key 规则与重放。
func TestMoveTaskRejections(t *testing.T) {
	env := newMvEnv(t)
	owner, member, guest := env.owner(), env.member(), env.guest()
	source, target, hidden := env.createSpace(nil), env.createSpace(nil), env.createSpace(nil)
	env.addSpaceMember(source.ID, mvMemberID, "MEMBER")
	env.addSpaceMember(target.ID, mvMemberID, "MEMBER")
	// guest 在源空间是只读的 VIEWER、在目标空间可写：源空间没有删除权限。
	env.addSpaceMember(source.ID, mvGuestID, "VIEWER")
	env.addSpaceMember(target.ID, mvGuestID, "MEMBER")
	task := env.createTask(owner, source.ID, nil)
	moveCode := func(client *mvClient, targetID string, wantStatus int, wantCode string) {
		t.Helper()
		response := env.move(client, task, targetID, nil, "")
		if response.Status != wantStatus || response.code() != wantCode {
			t.Fatalf("move to %s = %d %s, want %d %s", targetID, response.Status, response.Body, wantStatus, wantCode)
		}
	}
	// 目标空间：看不到（member 不是 hidden 的成员）、等于源空间、不存在、别的组织 → 一律 400 INVALID_TARGET_SPACE。
	moveCode(member, hidden.ID, http.StatusBadRequest, "INVALID_TARGET_SPACE")
	moveCode(owner, source.ID, http.StatusBadRequest, "INVALID_TARGET_SPACE")
	moveCode(owner, mvNewID(t), http.StatusBadRequest, "INVALID_TARGET_SPACE")
	moveCode(owner, "not-a-uuid", http.StatusBadRequest, "INVALID_TARGET_SPACE")
	other := env.outsider().do(http.MethodPost, "/v1/organizations/"+env.otherOrg+"/spaces",
		map[string]any{"name": "Other " + mvSuffix(t), "prefix": mvPrefix(t), "visibility": "private"})
	var otherSpace spaceView
	other.decode(t, &otherSpace)
	moveCode(owner, otherSpace.ID, http.StatusBadRequest, "INVALID_TARGET_SPACE")
	// 源空间无删除权限（VIEWER 只读）→ 403；目标空间只读（member 在 viewOnly 是 VIEWER）→ INVALID_TARGET_SPACE。
	response := env.move(guest, task, target.ID, nil, "")
	if response.Status != http.StatusForbidden {
		t.Fatalf("source read-only = %d %s", response.Status, response.Body)
	}
	viewOnly := env.createSpace(nil)
	env.addSpaceMember(viewOnly.ID, mvMemberID, "VIEWER")
	moveCode(member, viewOnly.ID, http.StatusBadRequest, "INVALID_TARGET_SPACE")
	// 目标空间已停用 → INVALID_TARGET_SPACE；源空间已停用 → 409 SPACE_RETIRED。
	retired := env.createSpace(nil)
	var current spaceView
	owner.must(http.StatusOK, http.MethodGet, spacePath(retired.ID), nil, &current)
	owner.must(http.StatusOK, http.MethodPost, spacePath(retired.ID)+"/retire", map[string]any{"version": current.Version, "open_tasks": "keep"}, nil)
	moveCode(owner, retired.ID, http.StatusBadRequest, "INVALID_TARGET_SPACE")
	// version 过期 → 409 VERSION_CONFLICT。
	stale := task
	stale.Version++
	if response := env.move(owner, stale, target.ID, nil, ""); response.Status != http.StatusConflict || response.code() != "VERSION_CONFLICT" {
		t.Fatalf("stale version = %d %s", response.Status, response.Body)
	}
	// 缺 Idempotency-Key → 400 IDEMPOTENCY_KEY_REQUIRED。
	if response := owner.do(http.MethodPost, taskPath(source.ID, task.ID)+"/move",
		map[string]any{"target_space_id": target.ID, "version": task.Version}); response.code() != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("missing key = %d %s", response.Status, response.Body)
	}
	// 以上都没有改动任务。
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE id = $1::uuid AND space_id = $2::uuid AND version = $3`,
		task.ID, source.ID, task.Version); count != 1 {
		t.Fatalf("rejected moves changed the task")
	}
	// 源空间已停用 → 409 SPACE_RETIRED（停用空间只读）。
	frozen := env.createSpace(nil)
	frozenTask := env.createTask(owner, frozen.ID, nil)
	owner.must(http.StatusOK, http.MethodGet, spacePath(frozen.ID), nil, &current)
	owner.must(http.StatusOK, http.MethodPost, spacePath(frozen.ID)+"/retire", map[string]any{"version": current.Version, "open_tasks": "keep"}, nil)
	if response := env.move(owner, frozenTask, target.ID, nil, ""); response.Status != http.StatusConflict || response.code() != "SPACE_RETIRED" {
		t.Fatalf("retired source = %d %s", response.Status, response.Body)
	}

	// 幂等：同键同参数重放返回首次结果（带 Idempotent-Replayed），同键不同参数 409。
	key := "move-replay-" + mvSuffix(t)
	first := env.move(member, task, target.ID, nil, key)
	if first.Status != http.StatusOK {
		t.Fatalf("first move = %d %s", first.Status, first.Body)
	}
	var firstResult, replayResult moveResult
	first.decode(t, &firstResult)
	replay := env.move(member, task, target.ID, nil, key)
	if replay.Status != http.StatusOK || replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %v %s", replay.Status, replay.Header, replay.Body)
	}
	replay.decode(t, &replayResult)
	if replayResult.DisplayID != firstResult.DisplayID || replayResult.PreviousDisplayID != task.DisplayID ||
		replayResult.MovedCount != 1 || replayResult.Task.SpaceID != target.ID {
		t.Fatalf("replay result = %+v, first = %+v", replayResult, firstResult)
	}
	if conflict := env.move(member, task, target.ID, map[string]any{"cancel_runs": true}, key); conflict.Status != http.StatusConflict ||
		conflict.code() != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("same key different body = %d %s", conflict.Status, conflict.Body)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_display_aliases WHERE task_id = $1::uuid`, task.ID); count != 1 {
		t.Fatalf("replay wrote another alias: %d", count)
	}
}
