package stage1

import (
	"net/http"
	"testing"
	"time"
)

func TestTasksCommentActions(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	task := env.newTask(env.owner, space, nil)
	base := taskPath(space, task.ID) + "/comments"
	admin := env.addMember(tkUserA, "ADMIN")
	other := env.addMember(tkUserB, "MEMBER")
	viewer := env.addMember(tkUserC, "MEMBER")
	env.addSpaceMember(space.ID, tkUserC, "VIEWER", "ACTIVE")
	post := func(parent, body string) (string, map[string]any) {
		t.Helper()
		input := map[string]any{"body": body, "client_comment_id": cfgSuffix(t)}
		if parent != "" {
			input["reply_to_id"] = parent
		}
		var result struct{ ID string }
		env.member.must(201, "POST", base, input, &result)
		return result.ID, input
	}
	key := func() []string { return []string{"Idempotency-Key", cfgNewID(t)} }
	root, _ := post("", "root")
	first, _ := post(root, "first")
	nested, original := post(first, "nested")
	path := base + "/" + nested
	edit := map[string]any{"body": "改过的正文", "version": 1}
	env.member.expectError(400, "IDEMPOTENCY_KEY_REQUIRED", "PATCH", path, edit)
	other.expectError(403, "ACTION_FORBIDDEN", "PATCH", path, edit, key()...)
	env.member.expectError(400, "INVALID_REQUEST", "PATCH", path, map[string]any{"body": "  ", "version": 1}, key()...)
	k := key()
	env.member.must(204, "PATCH", path, edit, nil, k...)
	if env.member.must(204, "PATCH", path, edit, nil, k...).Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("edit retry not acknowledged")
	}
	admin.expectError(409, "VERSION_CONFLICT", "PATCH", path, edit, key()...)
	admin.must(204, "PATCH", path, map[string]any{"body": "管理员编辑", "version": 2}, nil, key()...)

	agent := env.insertAgent(space.ID, cfgOwnerID, "SHARED")
	agentComment := cfgNewID(t)
	env.exec("INSERT INTO openfox.task_comments(organization_id,space_id,task_id,id,author_agent_id,run_id,body) VALUES ($1,$2,$3,$4,$5,$6,'agent')", env.org, space.ID, task.ID, agentComment, agent, cfgNewID(t))
	env.owner.expectError(403, "ACTION_FORBIDDEN", "PATCH", base+"/"+agentComment, edit, key()...)

	close := map[string]bool{"resolved": true}
	reopen := map[string]bool{"resolved": false}
	viewer.expectError(403, "", "PUT", path+"/resolution", close, key()...)
	foreignPath := "/v1/organizations/" + env.otherOrg + "/spaces/" + space.ID + "/tasks/" + task.ID + "/comments/" + nested
	env.outsider.expectError(404, "NOT_FOUND", "PATCH", foreignPath, edit, key()...)
	env.outsider.expectError(404, "NOT_FOUND", "PUT", foreignPath+"/resolution", close, key()...)
	unrelatedTask := env.newTask(env.owner, space, nil)
	wrong := taskPath(space, unrelatedTask.ID) + "/comments/" + nested
	env.owner.expectError(404, "NOT_FOUND", "PATCH", wrong, edit, key()...)
	env.owner.expectError(404, "NOT_FOUND", "PUT", wrong+"/resolution", close, key()...)
	resolutionKey := key()
	other.must(204, "PUT", path+"/resolution", close, nil, resolutionKey...)
	other.must(204, "PUT", path+"/resolution", close, nil, key()...)
	var selected string
	env.scalar(&selected, "SELECT resolved_comment_id::text FROM openfox.task_comments WHERE id=$1", root)
	if selected != nested {
		t.Fatal("nested resolution not bound to its root")
	}
	late := map[string]any{"body": "new reply", "client_comment_id": cfgSuffix(t), "reply_to_id": first}
	env.member.expectError(409, "THREAD_RESOLVED", "POST", base, late)
	var replay struct{ ID, Body string }
	env.member.must(201, "POST", base, original, &replay)
	if replay.ID != nested || replay.Body != "管理员编辑" {
		t.Fatalf("create retry returned %+v", replay)
	}
	post(agentComment, "independent thread")
	other.must(204, "PUT", base+"/"+first+"/resolution", close, nil, key()...)
	env.scalar(&selected, "SELECT resolved_comment_id::text FROM openfox.task_comments WHERE id=$1", root)
	if selected != first {
		t.Fatal("resolution not reassigned")
	}
	other.must(204, "PUT", base+"/"+first+"/resolution", reopen, nil, key()...)
	other.must(204, "PUT", base+"/"+first+"/resolution", reopen, nil, key()...)
	other.must(204, "PUT", path+"/resolution", close, nil, resolutionKey...)
	other.expectError(409, "IDEMPOTENCY_CONFLICT", "PUT", path+"/resolution", reopen, resolutionKey...)
	env.member.must(201, "POST", base, late, nil)
	var n int
	env.scalar(&n, "SELECT count(*) FROM openfox.task_activities WHERE task_id=$1 AND kind IN ('comment.resolved','comment.reopened')", task.ID)
	if n != 3 {
		t.Fatalf("resolution activities=%d, want 3", n)
	}
	env.scalar(&n, "SELECT count(*) FROM openfox.task_comments WHERE id=$1 AND resolved_at IS NULL AND resolved_comment_id IS NULL AND resolved_by IS NULL", root)
	if n != 1 {
		t.Fatal("reopen did not clear the resolution")
	}
	env.scalar(&n, "SELECT count(*) FROM openfox.task_comments WHERE id=$1 AND edit_version=3 AND edited_at IS NOT NULL", nested)
	if n != 1 {
		t.Fatal("wrong edit version")
	}
	var taskAfter tkTask
	env.owner.must(http.StatusOK, "GET", taskPath(space, task.ID), nil, &taskAfter)
	if taskAfter.StatusID != task.StatusID || taskAfter.Version != task.Version {
		t.Fatal("comment action changed task")
	}
}

// An older client may already be posting a reply when another writer resolves the
// root. Wait for the actual PostgreSQL lock wait before committing the resolution.
func TestTasksCommentReplyWaitsForResolution(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	task := env.newTask(env.owner, space, nil)
	path := taskPath(space, task.ID) + "/comments"
	var root, child struct{ ID string }
	env.owner.must(201, "POST", path, map[string]any{"body": "root", "client_comment_id": cfgSuffix(t)}, &root)
	env.owner.must(201, "POST", path, map[string]any{"body": "first", "reply_to_id": root.ID, "client_comment_id": cfgSuffix(t)}, &child)
	tx, err := env.superuser.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(env.ctx)
	if _, err := tx.Exec(env.ctx, "UPDATE openfox.task_comments SET resolved_at=now(), resolved_by=$2, resolved_comment_id=$3 WHERE id=$1", root.ID, cfgOwnerID, child.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan cfgResponse, 1)
	input := map[string]any{"body": "racing reply", "reply_to_id": child.ID, "client_comment_id": cfgSuffix(t)}
	go func() { done <- env.owner.do("POST", path, input) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := tx.QueryRow(env.ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pg_backend_pid()=ANY(pg_blocking_pids(pid)))").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("reply completed before resolution committed: %d", r.Status)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("reply never waited for the root lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(env.ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.Status != 409 || r.code() != "THREAD_RESOLVED" {
		t.Fatalf("racing reply: %d %s", r.Status, r.Body)
	}
}
