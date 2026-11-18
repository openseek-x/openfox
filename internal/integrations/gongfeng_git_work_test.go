package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

func TestGongfengMRAndPushThroughDurableWorker(t *testing.T) {
	f := newGongfengDBFixture(t)
	var mu sync.Mutex
	hooks := gongfengHookFixture{}
	mr := map[string]any{"id": int64(501), "iid": 7, "project_id": 81, "target_project_id": 81, "source_project_id": 82, "title": "GFTEST-1 OTHER-1", "description": "", "source_branch": "feature/GFTEST-1", "state": "opened", "work_in_progress": true, "author": map[string]any{"id": 71, "username": "fixture"}, "updated_at": "2026-11-14T10:00:00Z"}
	notes := []any{}
	mrPosts := 0
	compareCalls := 0
	oversized := false
	commits := []any{
		map[string]any{"id": strings.Repeat("b", 40), "message": "GFTEST-1 side branch", "parent_ids": []string{strings.Repeat("a", 40)}},
		map[string]any{"id": strings.Repeat("c", 40), "message": "GFTEST-1 second branch", "parent_ids": []string{strings.Repeat("a", 40)}},
		map[string]any{"id": strings.Repeat("d", 40), "message": "GFTEST-1 merge", "parent_ids": []string{strings.Repeat("b", 40), strings.Repeat("c", 40)}}}
	f.service.Gongfeng = gongfengTLSClient(t, func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/api/v3/projects/82" {
			json.NewEncoder(w).Encode(map[string]any{"id": 82, "path_with_namespace": "owner/project", "archived": false, "issues_enabled": true, "visibility_level": 0, "public": false})
			return
		}
		if q.URL.Path == "/api/v3/projects/82/members/all/71" {
			json.NewEncoder(w).Encode(map[string]any{"id": 71, "username": "fixture", "state": "active", "access_level": 40})
			return
		}
		if !strings.Contains(q.URL.Path, "merge_request") && !strings.Contains(q.URL.Path, "/repository/") {
			hooks.serve(w, q)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch q.URL.Path {
		case "/api/v3/projects/82/merge_request/502":
			copy := map[string]any{}
			for k, v := range mr {
				copy[k] = v
			}
			copy["id"], copy["project_id"], copy["target_project_id"] = int64(502), 82, 82
			json.NewEncoder(w).Encode(copy)
		case "/api/v3/projects/81/merge_request/501":
			json.NewEncoder(w).Encode(mr)
		case "/api/v3/projects/81/merge_requests/501/notes":
			if q.Method == "POST" {
				var p map[string]string
				_ = json.NewDecoder(q.Body).Decode(&p)
				mrPosts++
				notes = append(notes, map[string]any{"id": int64(701), "body": p["body"], "author": map[string]any{"id": 71, "username": "fixture"}, "system": false, "created_at": time.Now().UTC().Format(time.RFC3339), "project_id": 81, "noteable_id": 501, "noteable_type": "MergeRequest"})
				w.WriteHeader(502)
				return
			}
			json.NewEncoder(w).Encode(notes)
		case "/api/v3/projects/81/repository/compare/commits":
			compareCalls++
			if q.URL.Query().Get("first_parent") != "" || q.URL.Query().Get("no_merges") != "" {
				t.Error("DAG truncated by query")
			}
			if oversized {
				rows := []any{}
				if q.URL.Query().Get("page") == "1" {
					for i := 1; i <= 100; i++ {
						rows = append(rows, map[string]any{"id": fmt.Sprintf("%040x", i), "message": "GFTEST-1"})
					}
					w.Header().Set("X-Next-Page", "2")
				} else {
					rows = append(rows, map[string]any{"id": fmt.Sprintf("%040x", 101), "message": "GFTEST-1"})
					w.Header().Set("X-Next-Page", "")
				}
				json.NewEncoder(w).Encode(rows)
				return
			}
			json.NewEncoder(w).Encode(commits)
		default:
			t.Errorf("unexpected Git route %s %s", q.Method, q.URL.Path)
			w.WriteHeader(404)
		}
	})
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	f.activate(c, r)
	var task, other string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gongfeng',true)`); e != nil {
			return e
		}
		var e error
		task, _, e = (collaboration.Service{}).CreateIssueTask(ctx, tx, f.org, f.space, f.actor, collaboration.ImportTaskInput{Title: "Bound task", Description: "", Source: "fixture"})
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES(openfox_private.current_organization_id(),$1::uuid,'OTHER','Other','PRIVATE')`, refsTestID(t)); e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.spaces WHERE prefix='OTHER'`).Scan(&other); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'OWNER')`, other, f.user); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO openfox.task_types(organization_id,space_id,name,is_default) VALUES(openfox_private.current_organization_id(),$1::uuid,'Task',true)`, other); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO openfox.workflow_statuses(organization_id,space_id,task_type_id,status_id,position,is_default) SELECT t.organization_id,t.space_id,t.id,s.id,s.position,s.category='TODO' FROM openfox.task_types t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id WHERE t.space_id=$1::uuid`, other); e != nil {
			return e
		}
		if _, _, e = (collaboration.Service{}).CreateIssueTask(ctx, tx, f.org, other, f.actor, collaboration.ImportTaskInput{Title: "Unbound task", Description: "", Source: "fixture"}); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET backlink_enabled=true,commit_enabled=true WHERE id=$1::uuid`, r.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	f.queue(r, "MR_REFRESH", "501", nil)
	w := f.worker()
	pass := func() {
		t.Helper()
		if e := w.runGongfengWork(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	pass()
	pass() // MR detail then a POST whose result is UNKNOWN.
	var state, ext, repository string
	var number, otherLinks int
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT state,external_id,number,repository FROM openfox.task_git_links WHERE task_id=$1::uuid AND kind='PULL_REQUEST'`, task).Scan(&state, &ext, &number, &repository); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_git_links WHERE space_id=$1::uuid`, other).Scan(&otherLinks); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET next_attempt_at=clock_timestamp() WHERE repository_id=$1::uuid AND kind='BACKLINK'`, r.ID)
		return e
	})
	if e != nil || state != "DRAFT" || ext != "501" || number != 7 || repository != "git.tencent.com/81" || otherLinks != 0 {
		t.Fatal(state, ext, number, repository, otherLinks, e)
	}
	pass()
	mu.Lock()
	posts := mrPosts
	mu.Unlock()
	if posts != 1 {
		t.Fatal("UNKNOWN repeated MR POST", posts)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT backlink_comment_id FROM openfox.task_git_links WHERE task_id=$1::uuid AND kind='PULL_REQUEST'`, task).Scan(&id); e != nil {
			return e
		}
		if id != "701" {
			t.Fatal("MR recovery ID", id)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	mr["state"] = "merged"
	mr["work_in_progress"] = false
	mr["updated_at"] = "2026-11-14T10:01:00Z"
	mu.Unlock()
	f.queue(r, "MR_REFRESH", "501", nil)
	pass()
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT s.category FROM openfox.tasks t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id AND s.id=t.status_id WHERE t.id=$1::uuid`, task).Scan(&state)
	})
	if e != nil || state != "DONE" {
		t.Fatal(state, e)
	}
	mu.Lock()
	mr["state"] = "opened"
	mr["updated_at"] = "2026-11-14T10:00:00Z"
	mu.Unlock()
	f.queue(r, "MR_REFRESH", "501", nil)
	pass()
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT state FROM openfox.task_git_links WHERE task_id=$1::uuid AND kind='PULL_REQUEST'`, task).Scan(&state); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET backlink_enabled=false WHERE id=$1::uuid`, r.ID)
		return e
	})
	if e != nil || state != "MERGED" {
		t.Fatal("late REST regressed", state, e)
	}
	payload := map[string]any{"before": strings.Repeat("a", 40), "after": strings.Repeat("d", 40), "ref": "refs/heads/main", "count": 3}
	f.queue(r, "PUSH", "dag", payload)
	pass()
	f.queue(r, "PUSH", "dag", payload)
	pass()
	var count int
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_git_links WHERE task_id=$1::uuid AND kind='COMMIT'`, task).Scan(&count)
	})
	if e != nil || count != 3 {
		t.Fatal("full DAG/idempotence", count, e)
	}
	mu.Lock()
	oversized = true
	mu.Unlock()
	delete(payload, "count")
	f.queue(r, "PUSH", "over-cap", payload)
	pass()
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT status FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid AND object_key='over-cap' AND kind='PUSH'`, r.ID).Scan(&state); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_git_links WHERE task_id=$1::uuid AND kind='COMMIT'`, task).Scan(&count)
	})
	if e != nil || state != "FAILED" || count != 3 {
		t.Fatal("truncated push was accepted", state, count, e)
	}
	mu.Lock()
	calls := compareCalls
	mu.Unlock()
	if calls != 4 {
		t.Fatal("compare request budget", calls)
	}
	// After project 81 is renamed, another stable project can take its old
	// display path and reuse iid 7 without overwriting either association.
	hooks.mu.Lock()
	hooks.path = "renamed/project"
	hooks.mu.Unlock()
	f.queue(r, "MR_REFRESH", "501", nil)
	pass()
	second, e := f.repo(c, 82)
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET status='ACTIVE',hook_state='ACTIVE',hook_generation=generation,hook_credential_generation=1,hook_next_attempt_at=NULL,next_reconcile_at=NULL,backlink_enabled=false WHERE id=$1::uuid`, second.ID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='ACTIVE',direction='INBOUND' WHERE id=$1::uuid`, second.SyncLinkID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	f.queue(second, "MR_REFRESH", "502", nil)
	pass()
	var keys []string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT array_agg(repository||':'||repository_display ORDER BY repository) FROM openfox.task_git_links WHERE task_id=$1::uuid AND kind='PULL_REQUEST' AND number=7`, task).Scan(&keys)
	})
	if e != nil || strings.Join(keys, ",") != "git.tencent.com/81:renamed/project,git.tencent.com/82:owner/project" {
		t.Fatal("path reuse merged stable identities", keys, e)
	}

}
