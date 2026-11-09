package stage9c

// 复测报告 D09 调研发现的两处缺口（先写失败用例复现，再修复）：
//  1. 停用空间「移动未完成任务」遇到有 task_git_links / task_issue_links 的任务时报外键错误（迁移 000064 改为 ON UPDATE CASCADE）；
//  2. 任务移到别的空间后，附件按附件自己的 space_id 鉴权，下载 / 删除返回 404（改为按任务当前所在空间判断）。

import (
	"bytes"
	"net/http"
	"testing"
)

// retireMove 停用 source 并把未完成任务移到 target。
func (e *mvEnv) retireMove(source, target spaceView) mvResponse {
	e.t.Helper()
	var current spaceView
	e.owner().must(http.StatusOK, http.MethodGet, spacePath(source.ID), nil, &current)
	return e.owner().do(http.MethodPost, spacePath(source.ID)+"/retire",
		map[string]any{"version": current.Version, "open_tasks": "move", "target_space_id": target.ID})
}

func TestRetireMoveTaskWithGitAndIssueLinks(t *testing.T) {
	env := newMvEnv(t)
	source, target := env.createSpace(nil), env.createSpace(nil)
	withPR := env.createTask(env.owner(), source.ID, nil)
	withIssue := env.createTask(env.owner(), source.ID, nil)
	env.insertGitLink(withPR)
	env.insertIssueLink(withIssue)

	response := env.retireMove(source, target)
	if response.Status != http.StatusOK {
		t.Fatalf("retire move = %d %s", response.Status, response.Body)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_git_links WHERE task_id = $1::uuid AND space_id = $2::uuid`,
		withPR.ID, target.ID); count != 1 {
		t.Fatalf("git link did not follow the task: %d", count)
	}
	if count := env.count(`SELECT count(*) FROM openfox.task_issue_links WHERE task_id = $1::uuid AND space_id = $2::uuid`,
		withIssue.ID, target.ID); count != 1 {
		t.Fatalf("issue link did not follow the task: %d", count)
	}
	// 关联页仍能读到 PR（GET .../git-links 按任务当前空间查）。
	if count := env.count(`SELECT count(*) FROM openfox.tasks WHERE id = ANY($1::uuid[]) AND space_id = $2::uuid`,
		[]string{withPR.ID, withIssue.ID}, target.ID); count != 2 {
		t.Fatalf("tasks not moved: %d", count)
	}
}

func TestAttachmentReadableAfterRetireMove(t *testing.T) {
	env := newMvEnv(t)
	source, target := env.createSpace(nil), env.createSpace(nil)
	task := env.createTask(env.owner(), source.ID, nil)
	keep := env.uploadAttachment(env.owner(), task)
	remove := env.uploadAttachment(env.owner(), task)

	if response := env.retireMove(source, target); response.Status != http.StatusOK {
		t.Fatalf("retire move = %d %s", response.Status, response.Body)
	}
	download := env.owner().do(http.MethodGet, "attachments/"+keep, nil)
	if download.Status != http.StatusOK || !bytes.Equal(download.Body, testPNG) {
		t.Fatalf("download after move = %d %s", download.Status, download.Body)
	}
	if response := env.owner().do(http.MethodDelete, "attachments/"+remove, nil); response.Status != http.StatusNoContent {
		t.Fatalf("delete after move = %d %s", response.Status, response.Body)
	}
	// 只读成员：目标空间看不到任务的人仍然 404（不泄露存在性）。
	env.member().expectError(http.StatusNotFound, "", http.MethodGet, "attachments/"+keep, nil)
}
