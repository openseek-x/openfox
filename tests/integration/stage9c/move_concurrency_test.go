package stage9c

import (
	"net/http"
	"sync"
	"testing"
)

// 两个方向的移动同时执行（A→B 与 B→A，各 6 个任务），以及停用 A（移到 B）与 B→A 的移动同时执行：
// 不死锁、不出 500；每个请求要么成功，要么得到明确的业务错误。
func TestMoveTaskConcurrentOppositeDirections(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	a, b := env.createSpace(nil), env.createSpace(nil)
	const n = 6
	tasks := make([]taskView, 0, 2*n)
	targets := make([]string, 0, 2*n)
	for index := 0; index < n; index++ {
		tasks, targets = append(tasks, env.createTask(owner, a.ID, nil)), append(targets, b.ID)
		tasks, targets = append(tasks, env.createTask(owner, b.ID, nil)), append(targets, a.ID)
	}
	statuses := runParallel(len(tasks), func(index int) int {
		return env.move(owner, tasks[index], targets[index], nil, "").Status
	})
	for index, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("move %d = %d", index, status)
		}
	}
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid`, a.ID); count != n {
		t.Fatalf("tasks in A = %d, want %d", count, n)
	}

	// 停用 A（未完成任务移到 B）与把 B 的任务移到 A 同时进行。
	extra := env.createTask(owner, b.ID, nil)
	var current spaceView
	owner.must(http.StatusOK, http.MethodGet, spacePath(a.ID), nil, &current)
	results := runParallel(2, func(index int) int {
		if index == 0 {
			return owner.do(http.MethodPost, spacePath(a.ID)+"/retire",
				map[string]any{"version": current.Version, "open_tasks": "move", "target_space_id": b.ID}).Status
		}
		return env.move(owner, extra, a.ID, nil, "").Status
	})
	if results[0] != http.StatusOK {
		t.Fatalf("retire during move = %d", results[0])
	}
	if results[1] != http.StatusOK && results[1] != http.StatusBadRequest {
		t.Fatalf("move during retire = %d", results[1])
	}
	// 无论谁先：停用完成后 A 里没有未完成任务。
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE space_id = $1::uuid AND deleted_at IS NULL`, a.ID); count != 0 {
		t.Fatalf("open tasks left in retired A = %d", count)
	}
}

// D6：已绑定 Issue 同步的任务移动时解除同步链接（远端 Issue 不动），未发送的出站项跳过，移动本身不再入队同步；
// D2：单独移动子任务时移动它自己的子树，并与原父任务解除关系（预览给出 detached_parent）。
func TestMoveTaskIssueUnlinkAndChildAlone(t *testing.T) {
	env := newMvEnv(t)
	owner := env.owner()
	source, target := env.createSpace(nil), env.createSpace(nil)
	parent := env.createTask(owner, source.ID, nil)
	child := env.createTask(owner, source.ID, map[string]any{"parent_task_id": parent.ID})
	grandchild := env.createTask(owner, source.ID, map[string]any{"parent_task_id": child.ID})
	issueLink := env.insertIssueLink(child)

	var preview struct {
		TasksCount     int `json:"tasks_count"`
		DetachedParent *struct {
			ID string `json:"id"`
		} `json:"detached_parent"`
		UnlinkedIssues []struct {
			TaskID string `json:"task_id"`
			Number int    `json:"number"`
		} `json:"unlinked_issues"`
	}
	owner.must(http.StatusOK, http.MethodPost, taskPath(source.ID, child.ID)+"/move-preview", map[string]any{"target_space_id": target.ID}, &preview)
	if preview.TasksCount != 2 || preview.DetachedParent == nil || preview.DetachedParent.ID != parent.ID ||
		len(preview.UnlinkedIssues) != 1 || preview.UnlinkedIssues[0].TaskID != child.ID {
		t.Fatalf("preview = %+v", preview)
	}
	result := env.mustMove(owner, child, target.ID, nil)
	if result.MovedCount != 2 || result.Task.ParentTaskID != nil {
		t.Fatalf("move child = %+v", result)
	}
	if moved := env.getTask(owner, target.ID, grandchild.ID); moved.ParentTaskID == nil || *moved.ParentTaskID != child.ID {
		t.Fatalf("grandchild = %+v", moved)
	}
	if left := env.getTask(owner, source.ID, parent.ID); left.SpaceID != source.ID {
		t.Fatalf("parent moved too: %+v", left)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_issue_links WHERE id = $1::uuid AND unlinked_at IS NOT NULL
		AND space_id = $2::uuid`, issueLink, target.ID); count != 1 {
		t.Fatalf("issue link not unlinked")
	}
	if count := env.count(`SELECT count(*) FROM openfox.integration_sync_outbox WHERE task_id = $1::uuid
		AND status IN ('PENDING', 'SENDING')`, child.ID); count != 0 {
		t.Fatalf("pending sync outbox rows = %d", count)
	}
}

func runParallel(n int, fn func(index int) int) []int {
	statuses := make([]int, n)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < n; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			statuses[index] = fn(index)
		}(index)
	}
	close(start)
	wait.Wait()
	return statuses
}
