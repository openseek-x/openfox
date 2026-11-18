package stage3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"net/http"
	"net/http/httptest"
	"openfox/internal/tenancy"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/integrations/cnb"
	"openfox/internal/secrets"
)

const cnbTestRepo = "team/nested/repo"

type cnbFake struct {
	mu                        sync.Mutex
	title, state, visibility  string
	identity                  string
	comments                  []map[string]any
	posts, total, reads, code int
	unknownPost               bool
	dropComment               bool
	postEntered, postRelease  chan struct{}
	retryAfter                string
	entered                   chan struct{}
	release                   chan struct{}
}

func (f *cnbFake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer fixture-cnb-token" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	f.reads++
	title, state, visibility, total, code := f.title, f.state, f.visibility, f.total, f.code
	entered, release := f.entered, f.release
	f.mu.Unlock()
	if code != 0 {
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		w.WriteHeader(code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	repo := map[string]any{"id": "repository-string-id", "path": cnbTestRepo, "name": "repo", "visibility_level": visibility, "access": "Owner"}
	switch {
	case r.URL.Path == "/user":
		json.NewEncoder(w).Encode(map[string]any{"id": func() string {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.identity != "" {
				return f.identity
			}
			return "cnb-user-string-id"
		}(), "username": "fixture"})
	case r.URL.Path == "/user/repos":
		json.NewEncoder(w).Encode([]any{repo})
	case r.URL.Path == "/"+cnbTestRepo:
		json.NewEncoder(w).Encode(repo)
	case strings.HasSuffix(r.URL.Path, "/comments"):
		if r.Method == "POST" {
			var input map[string]any
			json.NewDecoder(r.Body).Decode(&input)
			if input["work_mode"] != false {
				w.WriteHeader(400)
				return
			}
			f.mu.Lock()
			f.posts++
			row := map[string]any{"id": fmt.Sprintf("comment-%d", f.posts), "body": input["body"]}
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
		} else {
			f.mu.Lock()
			defer f.mu.Unlock()
			json.NewEncoder(w).Encode(f.comments)
		}
	case strings.Contains(r.URL.Path, "/-/pulls/"):
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
		number := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		json.NewEncoder(w).Encode(map[string]any{"number": number, "title": title, "state": state, "is_merged": state == "merged", "base": map[string]any{"repo": repo}, "head": map[string]any{"ref": "feature"}, "author": map[string]any{"username": "fixture"}})
	case strings.Contains(r.URL.Path, "/-/git/commits/"):
		json.NewEncoder(w).Encode(map[string]any{"sha": strings.Repeat("a", 40), "commit": map[string]any{"message": title}})
	case strings.Contains(r.URL.Path, "/-/git/compare/"):
		json.NewEncoder(w).Encode(map[string]any{"base_commit": map[string]any{"sha": strings.Repeat("b", 40)}, "head_commit": map[string]any{"sha": strings.Repeat("a", 40)}, "total_commits": total, "commits": []any{map[string]any{"sha": strings.Repeat("a", 40), "commit": map[string]any{"message": title}}}})
	default:
		w.WriteHeader(404)
	}
}

type cnbEnv struct {
	*s3Env
	fake       *cnbFake
	client     *cnb.Client
	svc        integrations.Service
	connection integrations.Connection
	hook       integrations.Webhook
}

func newCNBEnv(t *testing.T, sharedBox ...*secrets.Box) *cnbEnv {
	e := newS3Env(t)
	f := &cnbFake{state: "open", visibility: "Private", total: 1, comments: []map[string]any{}}
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
	client := &cnb.Client{APIURL: upstream.URL, HTTP: upstream.Client()}
	e.server = httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, CNBClient: client, PublicURL: "https://center.example"}))
	t.Cleanup(e.server.Close)
	e.clients = map[string]*s3Client{}
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	ce := &cnbEnv{s3Env: e, fake: f, client: client, svc: integrations.Service{Box: box, CNB: client, PublicURL: "https://center.example"}}
	e.owner().must(200, "POST", "integrations/cnb/instance-token", map[string]any{"method": "token", "secret": "fixture-cnb-token"}, &ce.connection)
	return ce
}
func (e *cnbEnv) connPath() string { return "integrations/connections/" + e.connection.ID }
func (e *cnbEnv) config() integrations.CNBConfiguration {
	var cfg integrations.CNBConfiguration
	e.owner().must(200, "GET", e.connPath()+"/cnb/repositories", nil, &cfg)
	return cfg
}
func (e *cnbEnv) enable() {
	e.owner().must(200, "POST", e.connPath()+"/webhook/rotate", map[string]any{"version": e.connection.Version}, &e.hook)
	cfg := e.config()
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfg.Version, "repositories": []map[string]any{{"id": "repository-string-id", "path": cnbTestRepo}}}, nil)
}
func (e *cnbEnv) event(number, build string) map[string]any {
	return map[string]any{"event": "pull_request.target", "repository": cnbTestRepo, "repository_id": "repository-string-id", "build_id": build, "number": number}
}
func (e *cnbEnv) postEvent(body any, secret string) s3Response {
	raw, _ := json.Marshal(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	path := strings.TrimPrefix(e.hook.PayloadURL, "https://center.example/api")
	req, _ := http.NewRequestWithContext(e.ctx, "POST", e.server.URL+path, bytes.NewReader(raw))
	req.Header.Set("X-CNB-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return s3Response{Status: resp.StatusCode, Body: data, Header: resp.Header}
}
func (e *cnbEnv) run() {
	e.t.Helper()
	worker := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "cnb-test"}
	if err := worker.RunOnce(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}
func (e *cnbEnv) due() {
	e.exec(`UPDATE openfox.integration_cnb_pr_work SET next_check_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
}
func (e *cnbEnv) links(task TaskView) []integrations.TaskGitLink {
	var out struct {
		Links []integrations.TaskGitLink `json:"links"`
	}
	e.owner().must(200, "GET", spacePath(e.space.ID)+"/tasks/"+task.ID+"/git-links", nil, &out)
	return out.Links
}

func TestCNBConfigurationAndGitPipeline(t *testing.T) {
	e := newCNBEnv(t)
	o := e.owner()
	task := e.createTask(o, e.space.ID, map[string]any{"title": "CNB source task"})
	e.fake.title = task.DisplayID + " from CNB"
	t.Run("old token opt-in and permissions", func(t *testing.T) {
		cfg := e.config()
		if len(cfg.Repositories) != 0 || len(cfg.Work) != 0 {
			t.Fatal(cfg)
		}
		var hook integrations.Webhook
		o.must(200, "GET", e.connPath()+"/webhook", nil, &hook)
		if hook.Secret != "" || hook.PayloadURL != "" {
			t.Fatal("old connection silently initialized")
		}
		e.member().expectError(403, "FORBIDDEN", "GET", e.connPath()+"/webhook", nil)
		e.member().expectError(403, "FORBIDDEN", "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfg.Version, "repositories": []any{}})
		if r := e.outsider().doIn(e.otherOrg, "GET", e.connPath()+"/cnb/repositories", nil); r.Status != 404 {
			t.Fatalf("cross tenant=%d %s", r.Status, r.Body)
		}
		if r := o.do("POST", "integrations/cnb/authorize", map[string]any{}); r.Status != 404 && r.Status != 405 {
			t.Fatalf("CNB OAuth unexpectedly accepted: %d %s", r.Status, r.Body)
		}
		o.must(200, "GET", e.connPath()+"/repositories?page=1&q=team", nil, nil)
		o.expectError(400, "INVALID_REPOSITORY", "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfg.Version, "repositories": []map[string]any{{"id": "wrong", "path": cnbTestRepo}}})
	})
	e.enable()
	t.Run("signature, replay and durable PR", func(t *testing.T) {
		if r := e.postEvent(e.event("11", "build-1"), "wrong"); r.Status != 401 {
			t.Fatalf("signature=%d %s", r.Status, r.Body)
		}
		if n := e.count(`SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid`, e.org); n != 0 {
			t.Fatal("invalid signature wrote work")
		}
		var wg sync.WaitGroup
		statuses := make(chan int, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); statuses <- e.postEvent(e.event("11", "build-1"), e.hook.Secret).Status }()
		}
		wg.Wait()
		close(statuses)
		for status := range statuses {
			if status != 200 {
				t.Fatalf("delivery=%d", status)
			}
		}
		if n := e.count(`SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE organization_id=$1::uuid`, e.org); n != 1 {
			t.Fatalf("deliveries=%d", n)
		}
		if len(e.links(task)) != 0 {
			t.Fatal("accepted webhook pretended already linked")
		}
		e.run()
		links := e.links(task)
		if len(links) != 1 || links[0].Provider != "cnb" || links[0].State != "OPEN" || links[0].URL != "https://cnb.cool/"+cnbTestRepo+"/-/pulls/11" {
			t.Fatalf("links=%+v", links)
		}
		if e.fake.posts != 1 {
			t.Fatalf("comments=%d", e.fake.posts)
		}
		cfg := e.config()
		if cfg.Work[0].BacklinkState != "SENT" || cfg.Work[0].LastCheckedAt == nil || cfg.Work[0].NextCheckAt == nil {
			t.Fatalf("work=%+v", cfg.Work)
		}
		if r := e.postEvent(e.event("11", "different-build"), e.hook.Secret); r.Status != 200 {
			t.Fatal(r)
		}
		e.run()
		if e.fake.posts != 1 {
			t.Fatal("business replay duplicated backlink")
		}
	})
	t.Run("close without CI event and reopen", func(t *testing.T) {
		e.fake.mu.Lock()
		e.fake.state = "closed"
		e.fake.title = "reference removed"
		e.fake.mu.Unlock()
		e.due()
		e.run()
		if got := e.links(task); len(got) != 1 || got[0].State != "CLOSED" {
			t.Fatalf("close=%+v", got)
		}
		if cfg := e.config(); !cfg.Work[0].Terminal || cfg.Work[0].NextCheckAt != nil {
			t.Fatalf("terminal=%+v", cfg.Work)
		}
		e.fake.mu.Lock()
		e.fake.state = "open"
		e.fake.title = task.DisplayID
		e.fake.mu.Unlock()
		e.postEvent(e.event("11", "reopen"), e.hook.Secret)
		e.run()
		if got := e.links(task); got[0].State != "OPEN" {
			t.Fatal(got)
		}
		e.fake.mu.Lock()
		e.fake.state = "merged"
		e.fake.mu.Unlock()
		e.due()
		e.run()
		if got := e.links(task); got[0].State != "MERGED" {
			t.Fatal(got)
		}
	})
	t.Run("commit data, zero-before and explicit overflow", func(t *testing.T) {
		settings := integrations.DefaultSettings()
		settings.LinkCommits = true
		o.must(200, "PUT", "integrations/cnb/settings", map[string]any{"version": 0, "settings": settings}, nil)
		push := map[string]any{"event": "push", "repository": cnbTestRepo, "repository_id": "repository-string-id", "build_id": "push-1", "before": strings.Repeat("0", 40), "head": strings.Repeat("a", 40)}
		if r := e.postEvent(push, e.hook.Secret); r.Status != 200 {
			t.Fatalf("push=%d %s", r.Status, r.Body)
		}
		if got := e.links(task); len(got) != 2 {
			t.Fatalf("commit links=%+v", got)
		}
		e.fake.mu.Lock()
		e.fake.total = 101
		e.fake.mu.Unlock()
		push["build_id"] = "push-2"
		push["before"] = strings.Repeat("b", 40)
		if r := e.postEvent(push, e.hook.Secret); r.Status != 422 || r.code() != "CNB_COMMIT_LIMIT" {
			t.Fatalf("overflow=%d %s", r.Status, r.Body)
		}
		if got := e.links(task); len(got) != 2 {
			t.Fatal("overflow partially linked")
		}
	})
	t.Run("disconnect confirmation and stop", func(t *testing.T) {
		cfg := e.config()
		o.expectError(400, "CONFIRMATION_MISMATCH", "POST", e.connPath()+"/disconnect", map[string]any{"version": cfg.Version, "confirm_phrase": "wrong"})
		o.must(200, "POST", e.connPath()+"/disconnect", map[string]any{"version": cfg.Version, "confirm_phrase": integrations.ConfirmPhrase("CNB")}, nil)
		if r := e.postEvent(e.event("12", "after-disconnect"), e.hook.Secret); r.Status != 404 {
			t.Fatalf("disconnected=%d", r.Status)
		}
		e.fake.mu.Lock()
		before := e.fake.reads
		e.fake.mu.Unlock()
		e.due()
		e.run()
		e.fake.mu.Lock()
		after := e.fake.reads
		e.fake.mu.Unlock()
		if before != after {
			t.Fatal("disconnected worker called upstream")
		}
		if got := e.links(task); len(got) != 2 {
			t.Fatal("disconnect deleted historical links")
		}
	})
}

func TestCNBConcurrentWorkersAndUnknownComment(t *testing.T) {
	e := newCNBEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "Unknown response"})
	e.fake.title = task.DisplayID
	e.fake.unknownPost = true
	e.enable()
	e.postEvent(e.event("13", "unknown"), e.hook.Secret)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: fmt.Sprintf("concurrent-%d", i)}
			errs <- w.RunCNBOrganization(e.ctx, e.org)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.fake.posts != 1 {
		t.Fatalf("posts=%d", e.fake.posts)
	}
	if cfg := e.config(); len(cfg.Work) != 1 || cfg.Work[0].BacklinkState != "SENT" {
		t.Fatalf("unknown result not reconciled=%+v", cfg.Work)
	}
	if n := e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND provider='cnb' AND backlink_comment_id<>''`, e.org); n != 1 {
		t.Fatalf("comment link count=%d", n)
	}
}

func TestCNBLeaseAndConfigurationFence(t *testing.T) {
	e := newCNBEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "Fence"})
	e.fake.title = task.DisplayID
	e.enable()
	e.postEvent(e.event("14", "first"), e.hook.Secret)
	e.fake.entered = make(chan struct{}, 1)
	e.fake.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "blocked"}
		done <- w.RunCNBOrganization(e.ctx, e.org)
	}()
	select {
	case <-e.fake.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach provider")
	}
	cfg := e.config()
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfg.Version, "repositories": []any{}}, nil)
	close(e.fake.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(e.links(task)) != 0 || e.fake.posts != 0 {
		t.Fatal("stale network response wrote after disable")
	}
	var expires *time.Time
	e.scalar(&expires, `SELECT lease_expires_at FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid`, e.org)
	if expires == nil {
		t.Fatal("expected expired-lease recovery fixture")
	}
	// Explicit re-enable after lease expiry recovers the pending PR, not its stale response.
	e.exec(`UPDATE openfox.integration_cnb_pr_work SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	cfg = e.config()
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfg.Version, "repositories": []map[string]any{{"id": "repository-string-id", "path": cnbTestRepo}}}, nil)
	e.run()
	if len(e.links(task)) != 1 || e.fake.posts != 1 {
		t.Fatal("lease recovery failed")
	}
}

// Rolls back the actual finish transaction after successful POST, without a
// production fault hook. The durable SENDING row must recover through GET.
type cnbFinishFault struct {
	integrations.SyncRunner
	failed atomic.Bool
}

func (p *cnbFinishFault) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	return p.SyncRunner.RunForOrganization(ctx, org, source, selector, func(ctx context.Context, tx pgx.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		var sent bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_cnb_pr_work WHERE organization_id=openfox_private.current_organization_id() AND backlink_state='SENT')`).Scan(&sent); err != nil {
			return err
		}
		if sent && p.failed.CompareAndSwap(false, true) {
			return errors.New("fixture finish transaction rollback")
		}
		return nil
	})
}
func TestCNBFinishRollbackRecovery(t *testing.T) {
	e := newCNBEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "finish failure"})
	e.fake.title = task.DisplayID
	e.enable()
	e.postEvent(e.event("17", "finish-fault"), e.hook.Secret)
	fault := &cnbFinishFault{SyncRunner: e.database}
	worker := integrations.SyncWorker{Pool: fault, Service: e.svc, Instance: "finish-fault"}
	if err := worker.RunCNBOrganization(e.ctx, e.org); err != nil {
		t.Fatal(err)
	}
	if !fault.failed.Load() || e.fake.posts != 1 {
		t.Fatalf("fault=%v posts=%d", fault.failed.Load(), e.fake.posts)
	}
	if cfg := e.config(); cfg.Work[0].BacklinkState != "SENDING" {
		t.Fatalf("durable=%+v", cfg.Work)
	}
	e.exec(`UPDATE openfox.integration_cnb_pr_work SET lease_expires_at=clock_timestamp()-interval '1 second',next_check_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	e.run()
	if cfg := e.config(); cfg.Work[0].BacklinkState != "SENT" || e.fake.posts != 1 {
		t.Fatalf("recovery=%+v posts=%d", cfg.Work, e.fake.posts)
	}
	if e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND backlink_comment_id<>''`, e.org) != 1 {
		t.Fatal("missing reconciled id")
	}
}
func TestCNBFailureClassificationAndBackoff(t *testing.T) {
	for _, code := range []int{401, 403, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newCNBEnv(t)
			e.enable()
			e.postEvent(e.event("18", fmt.Sprint(code)), e.hook.Secret)
			e.fake.mu.Lock()
			e.fake.code = code
			e.fake.retryAfter = "600"
			e.fake.mu.Unlock()
			e.run()
			var status string
			e.scalar(&status, `SELECT status FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, e.org, e.connection.ID)
			cfg := e.config()
			w := cfg.Work[0]
			if code == 401 {
				if status != integrations.StatusNeedsAttention || w.NextCheckAt != nil || w.ErrorCode != "token_revoked" {
					t.Fatalf("401=%s %+v", status, w)
				}
			} else {
				if status != "ACTIVE" || w.NextCheckAt == nil {
					t.Fatalf("capability/rate falsely revoked=%s %+v", status, w)
				}
				if code == 403 && w.ErrorCode != "capability_denied" {
					t.Fatal(w)
				}
				if code == 429 && (w.ErrorCode != "rate_limited" || time.Until(*w.NextCheckAt) < 590*time.Second) {
					t.Fatal(w)
				}
			}
			if e.fake.posts != 0 {
				t.Fatal("failed reads posted comment")
			}
		})
	}
}
func TestCNBConfigurationCASAndUnknownAbsent(t *testing.T) {
	e := newCNBEnv(t)
	e.enable()
	cfg := e.config()
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- e.owner().do("PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfg.Version, "repositories": cfg.Repositories}).Status
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for s := range statuses {
		counts[s]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "absent unknown"})
	e.fake.title = task.DisplayID
	e.postEvent(e.event("19", "absent"), e.hook.Secret)
	e.exec(`UPDATE openfox.integration_cnb_pr_work SET backlink_state='SENDING',backlink_body='fixture already dispatched',lease_owner='crashed',lease_token=gen_random_uuid(),lease_expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	e.run()
	cfg = e.config()
	if cfg.Work[0].BacklinkState != "UNKNOWN" || cfg.Work[0].ErrorCode != "comment_result_unknown" || e.fake.posts != 0 {
		t.Fatalf("unknown=%+v posts=%d", cfg.Work, e.fake.posts)
	}
	e.due()
	e.run()
	e.owner().must(204, "POST", e.connPath()+"/cnb/reconcile", map[string]any{"repository_id": "repository-string-id", "number": 19}, nil)
	e.run()
	if e.fake.posts != 0 {
		t.Fatal("unknown was blindly resent")
	}
	old := e.hook
	e.owner().must(200, "POST", e.connPath()+"/webhook/rotate", map[string]any{"version": e.config().Version}, &e.hook)
	current := e.hook
	e.hook = old
	if r := e.postEvent(e.event("19", "stale-token"), old.Secret); r.Status != 404 {
		t.Fatalf("stale token=%d", r.Status)
	}
	e.hook = current
	if r := e.postEvent(e.event("19", "stale-secret"), old.Secret); r.Status != 401 {
		t.Fatalf("stale secret=%d", r.Status)
	}
}

func TestCNBOrganizationBudgetFairness(t *testing.T) {
	key := make([]byte, secrets.KeySize)
	rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	envs := make([]*cnbEnv, 0, 11)
	for i := 0; i < 11; i++ {
		e := newCNBEnv(t, box)
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
	var unseen []*cnbEnv
	for _, e := range envs {
		n := e.count(`SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid AND last_checked_at IS NOT NULL`, e.org)
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
		if e.count(`SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid AND last_checked_at IS NOT NULL`, e.org) == 0 {
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
		if e.count(`SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid AND last_checked_at IS NOT NULL`, e.org) == 0 {
			t.Fatal("bad org blocked later org")
		}
	}
}

func TestCNBIsolationPublicPolicyAndFailureBudget(t *testing.T) {
	e := newCNBEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "private task"})
	e.fake.title = task.DisplayID
	e.fake.visibility = "Public"
	e.enable()
	bad := e.event("21", "unselected")
	bad["repository_id"] = "not-selected"
	if r := e.postEvent(bad, e.hook.Secret); r.Status != 400 {
		t.Fatalf("unselected=%d", r.Status)
	}
	e.postEvent(e.event("21", "public"), e.hook.Secret)
	e.run()
	if e.fake.posts != 0 || len(e.links(task)) != 1 {
		t.Fatal("public-default-off was not honored")
	}
	e.member().expectError(403, "FORBIDDEN", "POST", e.connPath()+"/cnb/reconcile", map[string]any{"repository_id": "repository-string-id", "number": 21})
	e.guest().expectError(404, "NOT_FOUND", "GET", spacePath(e.space.ID)+"/tasks/"+task.ID+"/git-links", nil)
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: e.otherOrg, ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}, nil
	})
	err := e.database.RunForOrganization(e.ctx, e.otherOrg, tenancy.SourceOrganizationWorker, selector, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid`, e.org).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("cross tenant read")
		}
		_, err := tx.Exec(ctx, `INSERT INTO openfox.integration_cnb_pr_work(organization_id,connection_id,repository_id,repository_path,number) VALUES($1::uuid,$2::uuid,'other','team/repo',1)`, e.org, e.connection.ID)
		return err
	})
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("RLS insert=%v", err)
	}
	e.fake.mu.Lock()
	e.fake.code = 403
	e.fake.mu.Unlock()
	for i := 0; i < 8; i++ {
		e.due()
		e.run()
	}
	cfg := e.config()
	if cfg.Work[0].NextCheckAt != nil || cfg.Work[0].ErrorCode != "capability_denied" {
		t.Fatalf("8 failure pause=%+v", cfg.Work)
	}
	e.fake.mu.Lock()
	e.fake.code = 0
	e.fake.mu.Unlock()
	e.owner().must(204, "POST", e.connPath()+"/cnb/reconcile", map[string]any{"repository_id": "repository-string-id", "number": 21}, nil)
	e.run()
	cfg = e.config()
	if cfg.Work[0].ErrorCode != "" || cfg.Work[0].NextCheckAt == nil {
		t.Fatalf("manual recovery=%+v", cfg.Work)
	}
}

func TestCNBExplicitConnectionTakeover(t *testing.T) {
	e := newCNBEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "takeover"})
	e.fake.title = task.DisplayID
	e.enable()
	e.postEvent(e.event("31", "owner-a"), e.hook.Secret)
	e.run()
	a := e.connection
	cfgA := e.config()
	e.fake.mu.Lock()
	e.fake.identity = "cnb-second-user"
	e.fake.mu.Unlock()
	var b integrations.Connection
	e.owner().must(200, "POST", "integrations/cnb/instance-token", map[string]any{"method": "token", "secret": "fixture-cnb-token"}, &b)
	e.connection = b
	cfgB := e.config()
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfgB.Version, "repositories": cfgA.Repositories}, nil)
	if len(e.config().Work) != 0 {
		t.Fatal("active selected owner was stolen")
	}
	e.connection = a
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfgA.Version, "repositories": []any{}}, nil)
	e.connection = b
	cfgB = e.config()
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfgB.Version, "repositories": cfgA.Repositories}, nil)
	cfgB = e.config()
	if len(cfgB.Work) != 1 || cfgB.Work[0].BacklinkState != "SENT" {
		t.Fatalf("unselected ACTIVE owner not transferred: %+v", cfgB.Work)
	}
	e.run()
	if e.fake.posts != 1 {
		t.Fatal("takeover duplicated sent backlink")
	}
}

func TestCNBInFlightConnectionTakeover(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprint(stored), func(t *testing.T) {
			e := newCNBEnv(t)
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
				done <- w.RunCNBOrganization(e.ctx, e.org)
			}()
			select {
			case <-e.fake.postEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("no POST")
			}
			e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfgA.Version, "repositories": []any{}}, nil)
			e.fake.mu.Lock()
			e.fake.identity = "inflight-second-user"
			e.fake.mu.Unlock()
			var b integrations.Connection
			e.owner().must(200, "POST", "integrations/cnb/instance-token", map[string]any{"method": "token", "secret": "fixture-cnb-token"}, &b)
			e.connection = b
			cfgB := e.config()
			e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfgB.Version, "repositories": cfgA.Repositories}, nil)
			cfgB = e.config()
			if len(cfgB.Work) != 1 || cfgB.Work[0].BacklinkState != "UNKNOWN" {
				t.Fatalf("takeover must preserve dispatch uncertainty: %+v", cfgB.Work)
			}
			if e.count(`SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND lease_token IS NULL`, e.org, b.ID) != 1 {
				t.Fatal("old lease not fenced")
			}
			w := integrations.SyncWorker{Pool: e.database, Service: e.svc, Instance: "new-owner"}
			if err := w.RunCNBOrganization(e.ctx, e.org); err != nil {
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
func TestCNBConcurrentConnectionTakeover(t *testing.T) {
	e := newCNBEnv(t)
	e.enable()
	a := e.connection
	cfgA := e.config()
	e.postEvent(e.event("33", "a"), e.hook.Secret)
	e.owner().must(200, "PUT", e.connPath()+"/cnb/repositories", map[string]any{"version": cfgA.Version, "repositories": []any{}}, nil)
	var others []integrations.Connection
	for _, identity := range []string{"candidate-b", "candidate-c"} {
		e.fake.mu.Lock()
		e.fake.identity = identity
		e.fake.mu.Unlock()
		var c integrations.Connection
		e.owner().must(200, "POST", "integrations/cnb/instance-token", map[string]any{"method": "token", "secret": "fixture-cnb-token"}, &c)
		others = append(others, c)
	}
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, c := range others {
		wg.Add(1)
		go func(c integrations.Connection) {
			defer wg.Done()
			statuses <- e.owner().do("PUT", "integrations/connections/"+c.ID+"/cnb/repositories", map[string]any{"version": c.Version, "repositories": cfgA.Repositories}).Status
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
	e.scalar(&owner, `SELECT connection_id::text FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid`, e.org)
	if owner == a.ID || owner != others[0].ID && owner != others[1].ID {
		t.Fatalf("owner=%s", owner)
	}
	if e.count(`SELECT count(*) FROM openfox.integration_cnb_pr_work WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("duplicate work")
	}
}
