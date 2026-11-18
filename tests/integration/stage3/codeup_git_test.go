package stage3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"net/http"
	"net/http/httptest"
	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/integrations/codeup"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const codeupTestRepo = "cloud/nested/repo"

type codeupFake struct {
	mu                                         sync.Mutex
	title, state, visibility, identity         string
	comments                                   []map[string]any
	posts, total, reads, code                  int
	unknownPost, dropComment                   bool
	postEntered, postRelease, entered, release chan struct{}
	retryAfter                                 string
}

func (f *codeupFake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-yunxiao-token") != "fixture-codeup-token" && r.Header.Get("x-yunxiao-token") != "renewed-codeup-token" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	f.reads++
	title, state, visibility, code := f.title, f.state, f.visibility, f.code
	entered, release := f.entered, f.release
	retry := f.retryAfter
	identity := f.identity
	f.mu.Unlock()
	if code != 0 {
		if entered != nil && strings.HasSuffix(r.URL.Path, "/repositories/123") {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}

		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.WriteHeader(code)
		return
	}
	repo := map[string]any{"id": 123, "name": "repo", "pathWithNamespace": codeupTestRepo, "webUrl": "https://codeup.aliyun.com/" + codeupTestRepo, "visibility": visibility}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/oapi/v1/platform/user":
		if identity == "" {
			identity = "codeup-user"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": identity, "username": "fixture"})
	case r.URL.Path == "/oapi/v1/codeup/organizations/cloud/repositories":
		if r.URL.Query().Get("page") == "1" && r.URL.Query().Get("perPage") == "20" {
			w.Header().Set("x-next-page", "2")
		}
		json.NewEncoder(w).Encode([]any{repo})
	case r.URL.Path == "/oapi/v1/codeup/organizations/cloud/repositories/123":
		json.NewEncoder(w).Encode(repo)
	case strings.HasSuffix(r.URL.Path, "/diffs/patches"):
		json.NewEncoder(w).Encode([]any{map[string]any{"patchSetBizId": "patch-1", "versionNo": 1, "relatedMergeItemType": "MERGE_SOURCE"}})
	case strings.HasSuffix(r.URL.Path, "/comments/list"):
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.comments)
	case strings.HasSuffix(r.URL.Path, "/comments"):
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var input map[string]any
		json.NewDecoder(r.Body).Decode(&input)
		if input["patchset_biz_id"] != "patch-1" || input["comment_type"] != "GLOBAL_COMMENT" || input["draft"] != false || input["resolved"] != false {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.posts++
		row := map[string]any{"comment_biz_id": fmt.Sprintf("comment-%d", f.posts), "content": input["content"]}
		if !f.dropComment {
			f.comments = append(f.comments, row)
		}
		unknown, entered, release := f.unknownPost, f.postEntered, f.postRelease
		f.mu.Unlock()
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if unknown {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(row)
	case strings.Contains(r.URL.Path, "/changeRequests/"):
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		n, _ := strconv.Atoi(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		json.NewEncoder(w).Encode(map[string]any{"localId": n, "projectId": 123, "targetProjectId": 123, "title": title, "status": state, "sourceBranch": "feature", "updateTime": time.Now().UTC().Format(time.RFC3339), "webUrl": fmt.Sprintf("https://codeup.aliyun.com/%s/change/%d", codeupTestRepo, n), "author": map[string]any{"username": "fixture"}})
	case strings.Contains(r.URL.Path, "/commits/"):
		sha := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		json.NewEncoder(w).Encode(map[string]any{"id": sha, "message": title, "webUrl": "https://codeup.aliyun.com/" + codeupTestRepo + "/commits/" + sha})
	default:
		w.WriteHeader(404)
	}
}

type codeupEnv struct {
	*s3Env
	fake       *codeupFake
	client     *codeup.Client
	svc        integrations.Service
	connection integrations.Connection
	hook       integrations.Webhook
}

func newCodeupEnv(t *testing.T, sharedBox ...*secrets.Box) *codeupEnv {
	return newCodeupEnvWithBinding(t, true, sharedBox...)
}
func newCodeupEnvWithBinding(t *testing.T, bind bool, sharedBox ...*secrets.Box) *codeupEnv {
	e := newS3Env(t)
	f := &codeupFake{state: "UNDER_REVIEW", visibility: "private", total: 1, comments: []map[string]any{}}
	upstream := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(upstream.Close)
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(sharedBox) > 0 {
		box = sharedBox[0]
	}
	client := &codeup.Client{APIURL: upstream.URL, HTTP: upstream.Client()}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, CodeupClient: client, PublicURL: "https://center.example"}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	ce := &codeupEnv{s3Env: e, fake: f, client: client, svc: integrations.Service{Box: box, Codeup: client, PublicURL: "https://center.example"}}
	if bind {
		e.owner().must(200, "POST", "integrations/codeup/instance-token", map[string]any{"method": "token", "secret": "fixture-codeup-token", "company_id": "cloud"}, &ce.connection)
	}
	// The global scanner must not inherit another test's due work encrypted with
	// a different fixture Box. Keep rows for assertions/evidence, stop only this org.
	t.Cleanup(func() {
		e.exec(`UPDATE openfox.integration_codeup_mr_work SET next_check_at=NULL WHERE organization_id=$1::uuid`, e.org)
	})
	return ce
}
func (e *codeupEnv) connPath() string { return "integrations/connections/" + e.connection.ID }
func (e *codeupEnv) config() integrations.CodeupConfiguration {
	var cfg integrations.CodeupConfiguration
	e.owner().must(200, "GET", e.connPath()+"/codeup/configuration", nil, &cfg)
	return cfg
}
func (e *codeupEnv) enable() {
	e.owner().must(200, "POST", e.connPath()+"/webhook/rotate", map[string]any{"version": e.connection.Version}, &e.hook)
	cfg := e.config()
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfg.Version, "repositories": []map[string]any{{"id": "123", "path": codeupTestRepo}}}, nil)
}
func (e *codeupEnv) event(number, action string) map[string]any {
	n, _ := strconv.Atoi(number)
	return map[string]any{"version": "new", "object_kind": "merge_request", "object_attributes": map[string]any{"local_id": n, "project_id": 123, "target_project_id": 123, "action": action}}
}
func (e *codeupEnv) postEvent(body any, secret string) s3Response {
	raw, _ := json.Marshal(body)
	path := strings.TrimPrefix(e.hook.PayloadURL, "https://center.example/api")
	req, _ := http.NewRequestWithContext(e.ctx, "POST", e.server.URL+path, bytes.NewReader(raw))
	req.Header.Set("X-Codeup-Token", secret)
	kind := "Merge Request Hook"
	if m, ok := body.(map[string]any); ok && m["object_kind"] == "push" {
		kind = "Push Hook"
	}
	req.Header.Set("Codeup-Event", kind)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return s3Response{Status: resp.StatusCode, Body: data, Header: resp.Header}
}
func (e *codeupEnv) run() {
	e.t.Helper()
	worker := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "codeup-test"}
	if err := worker.RunCodeupOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
}
func (e *codeupEnv) due() {
	e.exec(`UPDATE openfox.integration_codeup_mr_work SET next_check_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
}
func (e *codeupEnv) links(task TaskView) []integrations.TaskGitLink {
	var out struct {
		Links []integrations.TaskGitLink `json:"links"`
	}
	e.owner().must(200, "GET", spacePath(e.space.ID)+"/tasks/"+task.ID+"/git-links", nil, &out)
	return out.Links
}

func TestCodeupConfigurationAndPipeline(t *testing.T) {
	e := newCodeupEnv(t)
	o := e.owner()
	task := e.createTask(o, e.space.ID, map[string]any{"title": "Codeup target"})
	e.fake.title = task.DisplayID + " change"
	t.Run("opt in permissions pagination", func(t *testing.T) {
		cfg := e.config()
		if len(cfg.Repositories) != 0 {
			t.Fatal(cfg)
		}
		var hook integrations.Webhook
		o.must(200, "GET", e.connPath()+"/webhook", nil, &hook)
		if hook.Secret != "" {
			t.Fatal("unexpected hook")
		}
		e.member().expectError(403, "FORBIDDEN", "GET", e.connPath()+"/codeup/repositories", nil)
		if r := e.outsider().doIn(e.otherOrg, "GET", e.connPath()+"/codeup/configuration", nil); r.Status != 404 {
			t.Fatal(r.Status)
		}
		var page struct {
			HasMore bool `json:"has_more"`
		}
		o.must(200, "GET", e.connPath()+"/codeup/repositories?page=1", nil, &page)
		if !page.HasMore {
			t.Fatal("pagination")
		}
		o.must(200, "GET", e.connPath()+"/codeup/repositories?page=2", nil, &page)
		if page.HasMore {
			t.Fatal("pagination end")
		}
		o.expectError(409, "VERSION_CONFLICT", "POST", "integrations/codeup/instance-token", map[string]any{"method": "token", "company_id": "cloud", "secret": "fixture-codeup-token"})
	})
	e.enable()
	t.Run("signature and concurrent duplicates", func(t *testing.T) {
		if r := e.postEvent(e.event("1", "open"), "wrong"); r.Status != 401 {
			t.Fatal(r)
		}
		var wg sync.WaitGroup
		statuses := make(chan int, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); statuses <- e.postEvent(e.event("1", "open"), e.hook.Secret).Status }()
		}
		wg.Wait()
		close(statuses)
		for n := range statuses {
			if n != 200 {
				t.Fatal(n)
			}
		}
		if e.count(`SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid`, e.org) != 1 {
			t.Fatal("duplicate work")
		}
		e.run()
		links := e.links(task)
		if len(links) != 1 || links[0].State != "OPEN" || links[0].Provider != "codeup" {
			t.Fatal(links)
		}
		if e.fake.posts != 1 {
			t.Fatal(e.fake.posts)
		}
		if w := e.config().Work[0]; w.BacklinkState != "SENT" || w.NextCheckAt != nil {
			t.Fatal(w)
		}
		e.postEvent(e.event("1", "update"), e.hook.Secret)
		e.run()
		if e.fake.posts != 1 {
			t.Fatal("reposted")
		}
	})
	t.Run("state close reopen and legacy", func(t *testing.T) {
		e.fake.mu.Lock()
		e.fake.state = "CLOSED"
		e.fake.title = "reference removed"
		e.fake.mu.Unlock()
		e.postEvent(e.event("1", "close"), e.hook.Secret)
		e.run()
		if x := e.links(task); x[0].State != "CLOSED" {
			t.Fatal(x)
		}
		e.fake.state = "UNDER_REVIEW"
		e.postEvent(e.event("1", "reopen"), e.hook.Secret)
		e.run()
		if x := e.links(task); x[0].State != "OPEN" {
			t.Fatal(x)
		}
		old := e.event("1", "old")
		delete(old, "version")
		e.postEvent(old, e.hook.Secret)
		if e.count(`SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE organization_id=$1::uuid AND detail='legacy_merge_request_unsupported'`, e.org) != 1 {
			t.Fatal("old MR not explained")
		}
	})
	t.Run("explicit credentials CAS", func(t *testing.T) {
		cfg := e.config()
		in := map[string]any{"version": cfg.Version, "method": "token", "company_id": "cloud", "secret": "renewed-codeup-token"}
		o.must(200, "PUT", e.connPath()+"/codeup/credentials", in, &e.connection)
		o.expectError(409, "VERSION_CONFLICT", "PUT", e.connPath()+"/codeup/credentials", in)
		if len(e.config().Repositories) != 1 {
			t.Fatal("lost selection")
		}
		e.fake.identity = "different"
		in["version"] = e.connection.Version
		o.expectError(400, "INVALID_REQUEST", "PUT", e.connPath()+"/codeup/credentials", in)
		e.fake.identity = ""
	})
}
func TestCodeupPushBoundaries(t *testing.T) {
	e := newCodeupEnv(t)
	e.enable()
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "commit"})
	e.fake.title = task.DisplayID
	var settings integrations.Settings
	e.owner().must(200, "GET", "integrations/codeup/settings", nil, &settings)
	settings.Settings.LinkCommits = true
	e.owner().must(200, "PUT", "integrations/codeup/settings", map[string]any{"version": settings.Version, "settings": settings.Settings}, nil)
	push := func(before, after string, total int) map[string]any {
		return map[string]any{"object_kind": "push", "project_id": 123, "ref": "refs/heads/feature", "before": before, "after": after, "total_commits_count": total, "commits": []any{map[string]any{"id": after, "message": task.DisplayID, "url": codeup.CommitURL(codeupTestRepo, after)}}}
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	if r := e.postEvent(push(b, a, 1), e.hook.Secret); r.Status != 200 {
		t.Fatal(r.Status, string(r.Body))
	}
	if len(e.links(task)) != 1 {
		t.Fatal("missing commit")
	}
	for _, n := range []int{2, 101} {
		if r := e.postEvent(push(b, strings.Repeat("c", 40), n), e.hook.Secret); r.Status != 422 {
			t.Fatal(r.Status, string(r.Body))
		}
	}
	if len(e.links(task)) != 1 {
		t.Fatal("partial excessive push")
	}
	if r := e.postEvent(push(strings.Repeat("0", 40), strings.Repeat("d", 40), 101), e.hook.Secret); r.Status != 200 {
		t.Fatal(r.Status, string(r.Body))
	}
	if len(e.links(task)) != 2 {
		t.Fatal("new branch tip")
	}
	if r := e.postEvent(push(a, strings.Repeat("0", 40), 0), e.hook.Secret); r.Status != 200 {
		t.Fatal(r.Status, string(r.Body))
	}
	if len(e.links(task)) != 2 {
		t.Fatal("deletion linked")
	}
}
func TestCodeupConcurrentUnknownAndFailureRecovery(t *testing.T) {
	e := newCodeupEnv(t)
	e.enable()
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "unknown"})
	e.fake.title = task.DisplayID
	e.fake.unknownPost = true
	e.postEvent(e.event("3", "open"), e.hook.Secret)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := integrations.SyncWorker{Pool: e.database, Service: e.svc}
			if err := w.RunCodeupOrganization(e.ctx, e.org); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if e.fake.posts != 1 || e.config().Work[0].BacklinkState != "SENT" {
		t.Fatal(e.fake.posts, e.config())
	}
	e.fake.dropComment = true
	e.postEvent(e.event("4", "open"), e.hook.Secret)
	e.run()
	var unknown integrations.CodeupWorkSummary
	for _, w := range e.config().Work {
		if w.Number == 4 {
			unknown = w
		}
	}
	if unknown.BacklinkState != "UNKNOWN" {
		t.Fatal(unknown)
	}
	cfg := e.config()
	e.owner().must(204, "POST", e.connPath()+"/codeup/reconcile", map[string]any{"version": cfg.Version, "repository_id": "123", "number": 4}, nil)
	e.run()
	if e.fake.posts != 2 {
		t.Fatal("unknown repost")
	}
	e.fake.code = 429
	e.fake.retryAfter = "120"
	e.postEvent(e.event("5", "open"), e.hook.Secret)
	e.run()
	cfg = e.config()
	for _, w := range cfg.Work {
		if w.Number == 5 && (w.ErrorCode != "rate_limited" || w.NextCheckAt == nil || time.Until(*w.NextCheckAt) < 110*time.Second) {
			t.Fatal(w)
		}
	}
	beforeReads := e.fake.reads
	var dueBefore, dueAfter time.Time
	e.scalar(&dueBefore, `SELECT next_check_at FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid AND number=5`, e.org)
	e.postEvent(e.event("5", "rate-limit-new-event"), e.hook.Secret)
	e.scalar(&dueAfter, `SELECT next_check_at FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid AND number=5`, e.org)
	if !dueBefore.Equal(dueAfter) {
		t.Fatal("event bypassed cooldown", dueBefore, dueAfter)
	}
	e.run()
	if e.fake.reads != beforeReads {
		t.Fatal("cooldown made external request")
	}
	e.fake.code = 403
	e.due()
	e.run()
	var status string
	e.superuser.QueryRow(e.ctx, `SELECT status FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, e.connection.ID).Scan(&status)
	if status != "ACTIVE" {
		t.Fatal(status)
	}
	e.fake.code = 401
	e.due()
	e.run()
	e.superuser.QueryRow(e.ctx, `SELECT status FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, e.connection.ID).Scan(&status)
	if status != "NEEDS_ATTENTION" {
		t.Fatal(status)
	}
}

type codeupFinishFault struct {
	integrations.SyncRunner
	failed atomic.Bool
}

func (p *codeupFinishFault) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	return p.SyncRunner.RunForOrganization(ctx, org, source, selector, func(ctx context.Context, tx pgx.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		var sent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_codeup_mr_work WHERE organization_id=openfox_private.current_organization_id() AND backlink_state='SENT')`).Scan(&sent); err != nil {
			return err
		}
		if sent && p.failed.CompareAndSwap(false, true) {
			return errors.New("fixture finish transaction rollback")
		}
		return nil
	})
}
func TestCodeupFinishRollbackRecovery(t *testing.T) {
	e := newCodeupEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "finish failure"})
	e.fake.title = task.DisplayID
	e.enable()
	e.postEvent(e.event("17", "finish-fault"), e.hook.Secret)
	fault := &codeupFinishFault{SyncRunner: e.database}
	worker := integrations.SyncWorker{Pool: fault, Service: e.svc, Instance: "finish-fault"}
	if err := worker.RunCodeupOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if !fault.failed.Load() || e.fake.posts != 1 {
		t.Fatalf("fault=%v posts=%d", fault.failed.Load(), e.fake.posts)
	}
	if cfg := e.config(); cfg.Work[0].BacklinkState != "SENDING" {
		t.Fatalf("durable=%+v", cfg.Work)
	}
	e.exec(`UPDATE openfox.integration_codeup_mr_work SET lease_expires_at=clock_timestamp()-interval '1 second',next_check_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	e.run()
	if cfg := e.config(); cfg.Work[0].BacklinkState != "SENT" || e.fake.posts != 1 {
		t.Fatalf("recovery=%+v posts=%d", cfg.Work, e.fake.posts)
	}
	if e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND backlink_comment_id<>''`, e.org) != 1 {
		t.Fatal("missing reconciled id")
	}
}

func TestCodeupLeaseAndConfigurationFence(t *testing.T) {
	e := newCodeupEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "Fence"})
	e.fake.title = task.DisplayID
	e.enable()
	e.postEvent(e.event("14", "first"), e.hook.Secret)
	e.fake.entered = make(chan struct{}, 1)
	e.fake.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "blocked"}
		done <- w.RunCodeupOrganization(e.ctx, e.org)
	}()
	select {
	case <-e.fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach provider")
	}
	cfg := e.config()
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfg.Version, "repositories": []any{}}, nil)
	close(e.fake.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(e.links(task)) != 0 || e.fake.posts != 0 {
		t.Fatal("stale network response wrote after disable")
	}
	var expires *time.Time
	e.scalar(&expires, `SELECT lease_expires_at FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid`, e.org)
	if expires == nil {
		t.Fatal("expected expired-lease recovery fixture")
	}
	// Explicit re-enable after lease expiry recovers the pending PR, not its stale response.
	e.exec(`UPDATE openfox.integration_codeup_mr_work SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	cfg = e.config()
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfg.Version, "repositories": []map[string]any{{"id": "123", "path": codeupTestRepo}}}, nil)
	e.run()
	if len(e.links(task)) != 1 || e.fake.posts != 1 {
		t.Fatal("lease recovery failed")
	}
}

// Rolls back the actual finish transaction after successful POST, without a
// production fault hook. The durable SENDING row must recover through the read-only comments/list POST.

func TestCodeupExplicitConnectionTakeover(t *testing.T) {
	e := newCodeupEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "takeover"})
	e.fake.title = task.DisplayID
	e.enable()
	e.postEvent(e.event("31", "owner-a"), e.hook.Secret)
	e.run()
	a := e.connection
	cfgA := e.config()
	e.fake.mu.Lock()
	e.fake.identity = "codeup-second-user"
	e.fake.mu.Unlock()
	var b integrations.Connection
	e.owner().must(200, "POST", "integrations/codeup/instance-token", map[string]any{"method": "token", "secret": "fixture-codeup-token", "company_id": "cloud"}, &b)
	e.connection = b
	cfgB := e.config()
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfgB.Version, "repositories": cfgA.Repositories}, nil)
	if len(e.config().Work) != 0 {
		t.Fatal("active selected owner was stolen")
	}
	e.connection = a
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfgA.Version, "repositories": []any{}}, nil)
	e.connection = b
	cfgB = e.config()
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfgB.Version, "repositories": cfgA.Repositories}, nil)
	cfgB = e.config()
	if len(cfgB.Work) != 1 || cfgB.Work[0].BacklinkState != "SENT" {
		t.Fatalf("unselected ACTIVE owner not transferred: %+v", cfgB.Work)
	}
	e.run()
	if e.fake.posts != 1 {
		t.Fatal("takeover duplicated sent backlink")
	}
}

func TestCodeupInFlightConnectionTakeover(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprint(stored), func(t *testing.T) {
			e := newCodeupEnv(t)
			task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "inflight"})
			e.fake.title = task.DisplayID
			e.enable()
			a := e.connection
			cfgA := e.config()
			e.fake.postEntered = make(chan struct{}, 1)
			e.fake.postRelease = make(chan struct{})
			e.fake.unknownPost = true
			e.fake.dropComment = !stored
			e.postEvent(e.event("32", "inflight-a"), e.hook.Secret)
			done := make(chan error, 1)
			go func() {
				w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "old-inflight"}
				done <- w.RunCodeupOrganization(e.ctx, e.org)
			}()
			select {
			case <-e.fake.postEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("no POST")
			}
			e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfgA.Version, "repositories": []any{}}, nil)
			e.fake.mu.Lock()
			e.fake.identity = "inflight-second-user"
			e.fake.mu.Unlock()
			var b integrations.Connection
			e.owner().must(200, "POST", "integrations/codeup/instance-token", map[string]any{"method": "token", "secret": "fixture-codeup-token", "company_id": "cloud"}, &b)
			e.connection = b
			cfgB := e.config()
			e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfgB.Version, "repositories": cfgA.Repositories}, nil)
			cfgB = e.config()
			if len(cfgB.Work) != 1 || cfgB.Work[0].BacklinkState != "UNKNOWN" {
				t.Fatalf("takeover must preserve dispatch uncertainty: %+v", cfgB.Work)
			}
			if e.count(`SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND lease_token IS NULL`, e.org, b.ID) != 1 {
				t.Fatal("old lease not fenced")
			}
			w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "new-owner"}
			if err := w.RunCodeupOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
			close(e.fake.postRelease)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			cfgB = e.config()
			want := "UNKNOWN"
			if stored {
				want = "SENT"
			}
			if cfgB.Work[0].BacklinkState != want || e.fake.posts != 1 {
				t.Fatalf("state=%+v posts=%d", cfgB.Work, e.fake.posts)
			}
			e.connection = a
			if len(e.config().Work) != 0 {
				t.Fatal("old owner retained work")
			}
		})
	}
}
func TestCodeupConcurrentConnectionTakeover(t *testing.T) {
	e := newCodeupEnv(t)
	e.enable()
	a := e.connection
	cfgA := e.config()
	e.postEvent(e.event("33", "a"), e.hook.Secret)
	e.owner().must(200, "PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfgA.Version, "repositories": []any{}}, nil)
	var others []integrations.Connection
	for _, identity := range []string{"candidate-b", "candidate-c"} {
		e.fake.mu.Lock()
		e.fake.identity = identity
		e.fake.mu.Unlock()
		var c integrations.Connection
		e.owner().must(200, "POST", "integrations/codeup/instance-token", map[string]any{"method": "token", "secret": "fixture-codeup-token", "company_id": "cloud"}, &c)
		others = append(others, c)
	}
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, c := range others {
		wg.Add(1)
		go func(c integrations.Connection) {
			defer wg.Done()
			statuses <- e.owner().do("PUT", "integrations/connections/"+c.ID+"/codeup/repositories", map[string]any{"version": c.Version, "repositories": cfgA.Repositories}).Status
		}(c)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Fatalf("parallel select=%d", status)
		}
	}
	var owner string
	e.scalar(&owner, `SELECT connection_id::text FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid`, e.org)
	if owner == a.ID || owner != others[0].ID && owner != others[1].ID {
		t.Fatalf("owner=%s", owner)
	}
	if e.count(`SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("duplicate work")
	}
}

func TestCodeupWorkPagingAndCAS(t *testing.T) {
	e := newCodeupEnv(t)
	e.enable()
	cfg := e.config()
	e.exec(`INSERT INTO openfox.integration_codeup_mr_work(organization_id,connection_id,cloud_organization_id,repository_id,repository_path,number,created_at) SELECT $1::uuid,$2::uuid,'cloud','123',$3,n,clock_timestamp()-n*interval '1 second' FROM generate_series(1,52) n`, e.org, e.connection.ID, codeupTestRepo)
	first := e.config()
	if len(first.Work) != 50 || !first.HasMore || first.NextCursor == "" {
		t.Fatal(first)
	}
	e.exec(`UPDATE openfox.integration_codeup_mr_work SET updated_at=clock_timestamp() WHERE organization_id=$1::uuid AND number=52`, e.org)
	var second integrations.CodeupWorkPage
	e.owner().must(200, "GET", e.connPath()+"/codeup/work?after="+first.NextCursor+"&limit=2", nil, &second)
	if second.HasMore || len(second.Work) != 2 || second.Work[0].Number != 51 || second.Work[1].Number != 52 {
		t.Fatal(second)
	}
	e.member().expectError(403, "FORBIDDEN", "GET", e.connPath()+"/codeup/work", nil)
	e.owner().expectError(400, "INVALID_REQUEST", "GET", e.connPath()+"/codeup/work?after=invalid", nil)
	e.owner().expectError(400, "INVALID_REQUEST", "GET", e.connPath()+"/codeup/work?limit=51", nil)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- e.owner().do("PUT", e.connPath()+"/codeup/repositories", map[string]any{"version": cfg.Version, "repositories": cfg.Repositories}).Status
		}()
	}
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for status := range statuses {
		if status == 200 {
			ok++
		} else if status == 409 {
			conflict++
		} else {
			t.Fatal(status)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	e.owner().expectError(409, "VERSION_CONFLICT", "POST", e.connPath()+"/codeup/reconcile", map[string]any{"version": cfg.Version, "repository_id": "123", "number": 52})
}
func TestCodeupInflightRateLimitAndPause(t *testing.T) {
	e := newCodeupEnv(t)
	e.enable()
	e.postEvent(e.event("71", "first"), e.hook.Secret)
	e.fake.code = 429
	e.fake.retryAfter = "180"
	e.fake.entered = make(chan struct{}, 1)
	e.fake.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		w := integrations.SyncWorker{Pool: e.database, Service: e.svc}
		done <- w.RunCodeupOrganization(e.ctx, e.org)
	}()
	select {
	case <-e.fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not entered")
	}
	if r := e.postEvent(e.event("71", "during-request"), e.hook.Secret); r.Status != 200 {
		t.Fatal(r.Status)
	}
	close(e.fake.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	work := e.config().Work[0]
	if work.NextCheckAt == nil || time.Until(*work.NextCheckAt) < 170*time.Second || work.ErrorCode != "rate_limited" {
		t.Fatal(work)
	}
	e.fake.entered = nil
	e.exec(`UPDATE openfox.integration_codeup_mr_work SET consecutive_failures=7,next_check_at=clock_timestamp() WHERE organization_id=$1::uuid`, e.org)
	e.run()
	if e.config().Work[0].NextCheckAt != nil {
		t.Fatal("eighth failure not paused")
	}
	e.postEvent(e.event("71", "after-pause"), e.hook.Secret)
	if e.config().Work[0].NextCheckAt != nil {
		t.Fatal("new event restarted paused failure")
	}
}

func TestCodeupOrganizationBudgetFairness(t *testing.T) {
	key := make([]byte, secrets.KeySize)
	rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	envs := make([]*codeupEnv, 0, 11)
	for i := 0; i < 11; i++ {
		e := newCodeupEnv(t, box)
		e.enable()
		for n := 1; n <= 3; n++ {
			e.postEvent(e.event(fmt.Sprint(n), fmt.Sprintf("budget-%d-%d", i, n)), e.hook.Secret)
		}
		envs = append(envs, e)
	}
	worker := integrations.SyncWorker{Pool: envs[0].database, Service: envs[0].svc, Instance: "budget"}
	if err := worker.RunOnce(envs[0].ctx); err != nil {
		t.Fatal(err)
	}
	total := 0
	var unseen []*codeupEnv
	for _, e := range envs {
		n := e.count(`SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid AND last_checked_at IS NOT NULL`, e.org)
		if n > 2 {
			t.Fatalf("per-org budget=%d", n)
		}
		total += n
		if n == 0 {
			unseen = append(unseen, e)
		}
		e.due()
	}
	if total != 20 || len(unseen) != 1 {
		t.Fatalf("global=%d unseen=%d", total, len(unseen))
	}
	if err := worker.RunOnce(envs[0].ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range unseen {
		if e.count(`SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid AND last_checked_at IS NOT NULL`, e.org) == 0 {
			t.Fatal("continuously busy earlier orgs starved later org")
		}
	}
	// An empty (disabled) and a failed organization do not prevent later due work.
	envs[0].exec(`UPDATE openfox.integration_connections SET settings='{}' WHERE organization_id=$1::uuid AND id=$2::uuid`, envs[0].org, envs[0].connection.ID)
	envs[1].exec(`UPDATE openfox.integration_connections SET credentials_sealed=decode('00','hex') WHERE organization_id=$1::uuid AND id=$2::uuid`, envs[1].org, envs[1].connection.ID)
	for _, e := range envs {
		e.due()
	}
	if err := worker.RunOnce(envs[0].ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range envs[2:] {
		if e.count(`SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid AND last_checked_at IS NOT NULL`, e.org) == 0 {
			t.Fatal("bad org blocked later org")
		}
	}
}

func TestCodeupPolicyIsolationAndDisconnect(t *testing.T) {
	e := newCodeupEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "private task"})
	e.fake.title = task.DisplayID
	e.enable()
	var settings integrations.Settings
	e.owner().must(200, "GET", "integrations/codeup/settings", nil, &settings)
	settings.Settings.PRBacklinkPrivate = false
	e.owner().must(200, "PUT", "integrations/codeup/settings", map[string]any{"version": settings.Version, "settings": settings.Settings}, nil)
	e.postEvent(e.event("81", "skip"), e.hook.Secret)
	e.run()
	if len(e.links(task)) != 1 || e.fake.posts != 0 || e.config().Work[0].BacklinkState != "SKIPPED" {
		t.Fatal("skip policy")
	}
	e.owner().must(200, "GET", "integrations/codeup/settings", nil, &settings)
	settings.Settings.PRBacklinkPrivate = true
	e.owner().must(200, "PUT", "integrations/codeup/settings", map[string]any{"version": settings.Version, "settings": settings.Settings}, nil)
	cfg := e.config()
	e.owner().must(204, "POST", e.connPath()+"/codeup/reconcile", map[string]any{"version": cfg.Version, "repository_id": "123", "number": 81}, nil)
	e.run()
	if e.fake.posts != 0 || e.config().Work[0].BacklinkState != "SKIPPED" {
		t.Fatal("unexpected historical backlink")
	}
	e.guest().expectError(404, "NOT_FOUND", "GET", spacePath(e.space.ID)+"/tasks/"+task.ID+"/git-links", nil)
	e.member().expectError(403, "FORBIDDEN", "POST", e.connPath()+"/codeup/reconcile", map[string]any{"version": cfg.Version, "repository_id": "123", "number": 81})
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: e.otherOrg, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}, nil
	})
	err := e.database.RunForOrganization(e.ctx, e.otherOrg, tenancy.SourceOrganizationWorker, selector, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_codeup_mr_work WHERE organization_id=$1::uuid`, e.org).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("cross tenant read")
		}
		_, err := tx.Exec(ctx, `INSERT INTO openfox.integration_codeup_mr_work(organization_id,connection_id,cloud_organization_id,repository_id,repository_path,number) VALUES($1::uuid,$2::uuid,'cloud','123','cloud/repo',99)`, e.org, e.connection.ID)
		return err
	})
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("RLS=%v", err)
	}
	e.owner().expectError(400, "CONFIRMATION_MISMATCH", "POST", e.connPath()+"/disconnect", map[string]any{"version": cfg.Version, "confirm_phrase": "wrong"})
	e.owner().must(200, "POST", e.connPath()+"/disconnect", map[string]any{"version": cfg.Version, "confirm_phrase": integrations.ConfirmPhrase("云效 Codeup")}, nil)
	if r := e.postEvent(e.event("82", "after-disconnect"), e.hook.Secret); r.Status != 404 {
		t.Fatal(r.Status)
	}
	e.run()
	if e.fake.posts != 0 || len(e.links(task)) != 1 {
		t.Fatal("disconnect changed history")
	}
}
