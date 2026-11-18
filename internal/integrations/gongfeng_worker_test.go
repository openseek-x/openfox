package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
)

type gongfengWorkFixture struct {
	hooks        gongfengHookFixture
	mu           sync.Mutex
	issues       map[int64]map[string]any
	notes        []map[string]any
	posts        int
	commentPosts int
	unknown      bool
}

func gongfengTestIssue(id int64, iid int, confidential bool) map[string]any {
	return map[string]any{"id": id, "iid": iid, "project_id": 81, "title": "Remote issue", "description": "Remote body", "state": "opened", "confidential": confidential, "author": map[string]any{"id": 71, "username": "fixture", "state": "active"}, "assignees": []any{map[string]any{"id": 84, "username": "outside"}}, "created_at": "2026-11-14T10:00:00Z", "updated_at": "2026-11-14T10:00:00Z"}
}
func (r *gongfengWorkFixture) serve(w http.ResponseWriter, q *http.Request) {
	if !strings.Contains(q.URL.Path, "/issues") {
		r.hooks.serve(w, q)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	base := "/api/v3/projects/81/issues"
	if q.URL.Path == base {
		if q.Method == "POST" {
			var p map[string]string
			if json.NewDecoder(q.Body).Decode(&p) != nil {
				w.WriteHeader(400)
				return
			}
			r.posts++
			i := gongfengTestIssue(999, 9, false)
			i["title"], i["description"] = p["title"], p["description"]
			i["created_at"], i["updated_at"] = time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)
			r.issues[999] = i
			if r.unknown {
				w.WriteHeader(502)
				return
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(i)
			return
		}
		rows := []any{}
		for _, i := range r.issues {
			if i["state"] == q.URL.Query().Get("state") {
				rows = append(rows, i)
			}
		}
		json.NewEncoder(w).Encode(rows)
		return
	}
	parts := strings.Split(strings.TrimPrefix(q.URL.Path, base+"/"), "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	i, ok := r.issues[id]
	if !ok {
		w.WriteHeader(404)
		return
	}
	if len(parts) > 1 && parts[1] == "notes" {
		if len(parts) == 2 {
			if q.Method == "POST" {
				var body map[string]string
				_ = json.NewDecoder(q.Body).Decode(&body)
				r.commentPosts++
				note := map[string]any{"id": int64(700 + r.commentPosts), "noteable_id": id, "body": body["body"], "system": false, "author": map[string]any{"id": 71, "username": "fixture", "state": "active"}, "created_at": time.Now().UTC().Format(time.RFC3339)}
				r.notes = append(r.notes, note)
				w.WriteHeader(201)
				json.NewEncoder(w).Encode(note)
				return
			}
			rows := []any{}
			for _, n := range r.notes {
				if n["noteable_id"] == id {
					rows = append(rows, n)
				}
			}
			json.NewEncoder(w).Encode(rows)
		} else {
			for _, n := range r.notes {
				if strconv.FormatInt(n["id"].(int64), 10) == parts[2] {
					json.NewEncoder(w).Encode(n)
					return
				}
			}
			w.WriteHeader(404)
		}
		return
	}
	if q.Method == "PUT" {
		var p map[string]string
		_ = json.NewDecoder(q.Body).Decode(&p)
		i["title"], i["description"] = p["title"], p["description"]
		if p["state_event"] == "close" {
			i["state"] = "closed"
		} else {
			i["state"] = "opened"
		}
		i["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	json.NewEncoder(w).Encode(i)
}
func (f *gongfengDBFixture) activate(c Connection, r GongfengRepository) {
	f.t.Helper()
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET status='ACTIVE',hook_state='ACTIVE',hook_generation=generation,hook_credential_generation=1,hook_next_attempt_at=NULL,next_reconcile_at=NULL,backlink_enabled=false WHERE id=$1::uuid`, r.ID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='ACTIVE',direction='BIDIRECTIONAL' WHERE id=$1::uuid`, r.SyncLinkID)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
}
func (f *gongfengDBFixture) queue(r GongfengRepository, kind, key string, payload any) {
	f.t.Helper()
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.loadGongfeng(ctx, tx, f.org, r.ConnectionID, true)
		if e != nil {
			return e
		}
		r, e = loadGongfengRepository(ctx, tx, r.ConnectionID, r.ID, true)
		if e != nil {
			return e
		}
		return enqueueGongfengWork(ctx, tx, r, kind, key, nil, payload)
	})
	if e != nil {
		f.t.Fatal(e)
	}
}

func TestGongfengDurableIngressAndSourceUpdates(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengWorkFixture{issues: map[int64]map[string]any{901: gongfengTestIssue(901, 7, false), 902: gongfengTestIssue(902, 8, true)}}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	f.activate(c, r)
	// A forged PERSONAL binding cannot map a remote account to a local member.
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,provider,scope,owner_user_id,external_id,display_name,created_by) VALUES(openfox_private.current_organization_id(),'gongfeng','PERSONAL',$1::uuid,'84','forged',$1::uuid)`, f.user)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var a GongfengAccess
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, e = f.service.loadGongfeng(ctx, tx, f.org, c.ID, false)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	body := []byte(`{"object_kind":"issue","project_id":81,"object_attributes":{"id":901,"project_id":81}}`)
	headers := http.Header{"X-Token": {a.credentials.WebhookSecret}, "X-Event": {"Issue Hook"}}
	receive := func(body []byte, h http.Header) (GongfengReceipt, error) {
		var out GongfengReceipt
		e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			out, e = f.service.ReceiveGongfeng(ctx, tx, GitWebhookLookup{OrganizationID: f.org, ConnectionID: c.ID}, a.credentials.WebhookToken, h, body)
			return e
		})
		return out, e
	}
	out, e := receive(body, headers)
	if e != nil || !out.Accepted {
		t.Fatal(out, e)
	}
	out, e = receive(body, headers)
	if e != nil || !out.Duplicate {
		t.Fatal(out, e)
	}
	if _, e = receive(append([]byte(" "), body...), headers); e != nil {
		t.Fatal(e)
	}
	test := headers.Clone()
	test.Set("X-Source-Type", "Test")
	if out, e = receive(nil, test); e != nil || !out.Test {
		t.Fatal(out, e)
	}
	bad := headers.Clone()
	bad.Set("X-Token", "wrong")
	if _, e = receive(body, bad); !errors.Is(e, gongfeng.ErrAuthentication) {
		t.Fatal("bad secret", e)
	}
	w := f.worker()
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var task, title, repo, external string
	var assignee *string
	read := func() {
		t.Helper()
		e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT t.id::text,t.title,l.repository,t.assignee_id::text,COALESCE(x.external_user_id,'') FROM openfox.task_issue_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id LEFT JOIN openfox.external_accounts x ON x.organization_id=t.organization_id AND x.id=t.external_assignee_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.sync_link_id=$1::uuid AND l.issue_id='901'`, r.SyncLinkID).Scan(&task, &title, &repo, &assignee, &external)
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	read()
	if title != "Remote issue" || repo != "git.tencent.com/81" || assignee != nil || external != "84" {
		t.Fatal(title, repo, assignee, external)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gongfeng',true)`)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.tasks SET title='Local edit' WHERE id=$1::uuid`, task)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	f.queue(r, "ISSUE_REFRESH", "901", nil)
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	read()
	if title != "Local edit" {
		t.Fatal("unchanged source overwrote local", title)
	}
	remote.mu.Lock()
	remote.issues[901]["title"] = "New remote"
	remote.issues[901]["updated_at"] = "2026-11-14T10:01:00Z"
	remote.mu.Unlock()
	f.queue(r, "ISSUE_REFRESH", "901", nil)
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	read()
	if title != "New remote" {
		t.Fatal(title)
	}
	for i := 0; i < 2; i++ {
		f.queue(r, "ISSUE_REFRESH", "902", nil)
		if e = w.runGongfengWork(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	r = f.readRepo(c.ID, r.ID)
	if r.ExcludedConfidential != 1 {
		t.Fatal("confidential counted twice", r.ExcludedConfidential)
	}
	remote.hooks.mu.Lock()
	remote.hooks.path = "renamed/project"
	remote.hooks.mu.Unlock()
	f.queue(r, "ISSUE_REFRESH", "901", nil)
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	read()
	if repo != "git.tencent.com/81" {
		t.Fatal("rename changed identity", repo)
	}
	var name, url string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT s.external_project_name,l.url FROM openfox.task_issue_links l JOIN openfox.integration_sync_links s ON s.organization_id=l.organization_id AND s.id=l.sync_link_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.task_id=$1::uuid`, task).Scan(&name, &url)
	})
	if e != nil || name != "renamed/project" || url != "https://git.tencent.com/renamed/project/issues/7" {
		t.Fatal(name, url, e)
	}
}

func TestGongfengOutgoingUnknownCreateAndReconnect(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengWorkFixture{issues: map[int64]map[string]any{}, unknown: true}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	f.activate(c, r)
	var task string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		_, e = tx.Exec(ctx, `SELECT set_config('openfox.sync_source','gongfeng',true)`)
		if e != nil {
			return e
		}
		task, _, e = (collaboration.Service{}).CreateIssueTask(ctx, tx, f.org, f.space, f.actor, collaboration.ImportTaskInput{Title: "Local outbound", Description: "Body", Source: "fixture"})
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,provider,scope,owner_user_id,external_id,display_name,created_by) VALUES(openfox_private.current_organization_id(),'gongfeng','PERSONAL',$1::uuid,'84','must-not-load-personal-token',$1::uuid)`, f.user)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_sync_outbox(organization_id,sync_link_id,task_id,kind,actor_user_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'ISSUE_CREATE',$3::uuid)`, r.SyncLinkID, task, f.user)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	w := f.worker()
	if e = w.RunOrganization(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var state string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT backlink_state FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid AND kind='ISSUE_CREATE'`, r.ID).Scan(&state)
	})
	if e != nil || state != "UNKNOWN" {
		t.Fatal(state, e)
	}
	var disconnected Connection
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		out, e := f.service.Disconnect(ctx, tx, f.org, f.actor, c.ID, c.Version, ConfirmPhrase("腾讯工蜂"))
		disconnected = out.Connection
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var plan GongfengTokenPlan
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		plan, e = f.service.PrepareGongfengToken(ctx, tx, f.org, f.actor, GongfengTokenInput{ConnectionID: c.ID, Version: disconnected.Version, AccessToken: "same-subject-new-pat"})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.SaveGongfengToken(ctx, tx, f.org, f.actor, plan, gongfeng.User{ID: 72, Username: "wrong", State: "active"})
		return e
	})
	if !errors.Is(e, ErrNeedsReauth) {
		t.Fatal("changed sender accepted", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		c, e = f.service.SaveGongfengToken(ctx, tx, f.org, f.actor, plan, gongfeng.User{ID: 71, Username: "renamed", State: "active"})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	// Hook re-verification is covered separately with actual ensure/activate.
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET status='ACTIVE',hook_state='ACTIVE',hook_generation=generation,hook_credential_generation=2,hook_next_attempt_at=NULL WHERE id=$1::uuid`, r.ID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='ACTIVE' WHERE id=$1::uuid`, r.SyncLinkID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET next_attempt_at=clock_timestamp() WHERE repository_id=$1::uuid AND kind='ISSUE_CREATE' AND status='PENDING'`, r.ID)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		if e = w.runGongfengWork(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT backlink_state FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid AND kind='ISSUE_CREATE'`, r.ID).Scan(&state)
	})
	if e != nil || state != "SENT" {
		t.Fatal(state, e)
	}
	remote.mu.Lock()
	posts := remote.posts
	remote.mu.Unlock()
	if posts != 1 {
		t.Fatal("unknown replayed after reconnect", posts)
	}
	var ref string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT issue_ref FROM openfox.task_issue_links WHERE task_id=$1::uuid`, task).Scan(&ref)
	})
	if e != nil || ref != "999" {
		t.Fatal(ref, e)
	}
}

func TestGongfengHistoryTwoPassAndCommentReconcile(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengWorkFixture{issues: map[int64]map[string]any{901: gongfengTestIssue(901, 7, false), 903: gongfengTestIssue(903, 9, false)}}
	remote.issues[903]["state"] = "closed"
	remote.notes = []map[string]any{{"id": int64(501), "project_id": 81, "noteable_type": "Issue", "noteable_id": int64(901), "body": "new remote note", "author": map[string]any{"id": 84, "username": "outside"}, "system": false, "created_at": time.Now().UTC().Add(time.Second).Format(time.RFC3339)}}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	f.activate(c, r)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET history_requested=true,next_reconcile_at=clock_timestamp() WHERE id=$1::uuid`, r.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	w := f.worker()
	for i := 0; i < 4; i++ {
		if e = w.RunOrganization(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	r = f.readRepo(c.ID, r.ID)
	if r.Progress.Issues == nil || r.Progress.Issues.Status != "complete" || r.Progress.Issues.Count != 2 || r.Progress.Issues.FirstCount != 2 {
		t.Fatalf("scan did not finish both states/rounds: %#v", r.Progress.Issues)
	}
	var issues, closed, comments int
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state='CLOSED') FROM openfox.task_issue_links WHERE sync_link_id=$1::uuid`, r.SyncLinkID).Scan(&issues, &closed); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_comment_links WHERE organization_id=openfox_private.current_organization_id()`).Scan(&comments)
	})
	if e != nil || issues != 2 || closed != 1 || comments != 1 {
		t.Fatal(issues, closed, comments, e)
	}
	// A source transition between bounded rounds is surfaced, never truncated as
	// successful history completion. Existing imported copies remain untouched.
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET issue_watermark=NULL,history_cursor='{}',next_reconcile_at=clock_timestamp() WHERE id=$1::uuid`, r.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = w.runGongfengScans(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	remote.mu.Lock()
	delete(remote.issues, 903)
	remote.mu.Unlock()
	for i := 0; i < 2; i++ {
		if e = w.runGongfengScans(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	r = f.readRepo(c.ID, r.ID)
	if r.Progress.Issues.Status != "incomplete" || r.Status != "NEEDS_ATTENTION" {
		t.Fatal(r.Progress.Issues, r.Status)
	}
}

func TestGongfengVisibilityChangeStopsBacklink(t *testing.T) {
	for _, visibility := range []int{20, 10, -1} {
		t.Run(strconv.Itoa(visibility), func(t *testing.T) {
			f := newGongfengDBFixture(t)
			remote := &gongfengWorkFixture{issues: map[int64]map[string]any{901: gongfengTestIssue(901, 7, false)}}
			f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
			c := f.connection(71)
			r, e := f.repo(c, 81)
			if e != nil {
				t.Fatal(e)
			}
			f.activate(c, r)
			e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET backlink_enabled=true WHERE id=$1::uuid`, r.ID)
				return e
			})
			if e != nil {
				t.Fatal(e)
			}
			f.queue(r, "ISSUE_REFRESH", "901", nil)
			w := f.worker()
			if e = w.runGongfengWork(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			remote.hooks.mu.Lock()
			remote.hooks.visibility = &visibility
			remote.hooks.mu.Unlock()
			if e = w.runGongfengWork(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			remote.mu.Lock()
			posts := remote.commentPosts
			remote.mu.Unlock()
			if posts != 0 {
				t.Fatalf("visibility expansion sent %d task backlinks", posts)
			}
			r = f.readRepo(c.ID, r.ID)
			if r.Status != "NEEDS_ATTENTION" || r.StatusReason != "repository_changed" || r.Private == nil || !*r.Private {
				t.Fatalf("lost confirmed privacy or failure: %+v", r)
			}
			paused := false
			e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				_, e := f.service.UpdateGongfengRepository(ctx, tx, f.org, f.actor, c.ID, r.ID, GongfengRepositoryUpdate{ConnectionVersion: c.Version, Generation: r.Generation, Paused: &paused})
				return e
			})
			if !errors.Is(e, ErrInvalidRepository) {
				t.Fatal("resume accepted changed repository", e)
			}
			e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				_, e := f.service.RetryGongfengRepository(ctx, tx, f.org, f.actor, c.ID, r.ID, GongfengRepositoryUpdate{ConnectionVersion: c.Version, Generation: r.Generation})
				return e
			})
			if !errors.Is(e, ErrInvalidRepository) {
				t.Fatal("retry accepted changed repository", e)
			}
		})
	}
}

func TestGongfengDeletedQueuedCommentIsNotSent(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengWorkFixture{issues: map[int64]map[string]any{901: gongfengTestIssue(901, 7, false)}}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	f.activate(c, r)
	f.queue(r, "ISSUE_REFRESH", "901", nil)
	w := f.worker()
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var task, comment string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT task_id::text FROM openfox.task_issue_links WHERE sync_link_id=$1::uuid AND issue_id='901'`, r.SyncLinkID).Scan(&task); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_user_id,body) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'Queued confidential comment') RETURNING id::text`, f.space, task, f.user).Scan(&comment)
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.processOutbox(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	var queued int
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid AND kind='ISSUE_COMMENT'`, r.ID).Scan(&queued); e != nil {
			return e
		}
		_, e := (collaboration.Service{}).DeleteTaskComment(ctx, tx, f.space, task, comment, f.actor, refsTestID(t))
		return e
	})
	if e != nil || queued != 1 {
		t.Fatal("queue/delete", queued, e)
	}
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	remote.mu.Lock()
	posts := remote.commentPosts
	remote.mu.Unlock()
	if posts != 0 {
		t.Fatalf("deleted queued comment was sent: %d", posts)
	}
	var state string
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT backlink_state FROM openfox.integration_gongfeng_work WHERE repository_id=$1::uuid AND kind='ISSUE_COMMENT'`, r.ID).Scan(&state)
	})
	if e != nil || state != "SKIPPED" {
		t.Fatal(state, e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_user_id,body) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,'Live comment')`, f.space, task, f.user)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.processOutbox(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e = w.runGongfengWork(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	remote.mu.Lock()
	posts = remote.commentPosts
	remote.mu.Unlock()
	if posts != 1 {
		t.Fatal("live comment blocked", posts)
	}
}
