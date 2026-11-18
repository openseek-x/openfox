package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/membership"
)

const bbRepo = "44444444-4444-4444-8444-444444444444"

type bbWorkerFake struct {
	mu          sync.Mutex
	repo        bitbucket.Repository
	pr          bitbucket.PullRequest
	comment     bitbucket.Comment
	comments    []bitbucket.Comment
	mode        string
	posts, gets int
	commitCount int
	afterPost   func()
}
type bbWorkerPG struct {
	*bbPG
	f                                                                   *bbWorkerFake
	connection, selection, space, task, outside, outTask, secret, token string
	worker                                                              SyncWorker
}

func newBBWorkerPG(t *testing.T) *bbWorkerPG {
	b := newBBPG(t)
	b.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, b.org)
	c := b.connect()
	id := func() string {
		s, e := membership.NewID()
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	x := &bbWorkerPG{bbPG: b, connection: c.ID, space: id(), task: id(), outside: id(), outTask: id()}
	for i, sp := range []string{x.space, x.outside} {
		prefix := []string{"BBC", "OUT"}[i]
		b.exec(`INSERT INTO openfox.spaces(organization_id,id,name,prefix,visibility) VALUES($1::uuid,$2::uuid,'Worker',$3,'PRIVATE')`, b.org, sp, prefix)
		b.exec(`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, b.org, sp, b.owner.UserID)
	}
	b.exec(`INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES($1::uuid,'Todo','TODO',0)`, b.org)
	for i, sp := range []string{x.space, x.outside} {
		b.exec(`INSERT INTO openfox.task_types(organization_id,space_id,name) VALUES($1::uuid,$2::uuid,'Task')`, b.org, sp)
		b.exec(`INSERT INTO openfox.tasks(organization_id,space_id,id,sequence,title,type_id,status_id) SELECT $1::uuid,$2::uuid,$3::uuid,1,'private-title-never-sent',tt.id,st.id FROM openfox.task_types tt JOIN openfox.task_statuses st ON st.organization_id=tt.organization_id WHERE tt.organization_id=$1::uuid AND tt.space_id=$2::uuid LIMIT 1`, b.org, sp, []string{x.task, x.outTask}[i])
	}
	private := true
	body := "BBC-1 OUT-1"
	now := time.Now().UTC().Truncate(time.Second)
	f := &bbWorkerFake{repo: bitbucket.Repository{UUID: bbRepo, FullName: "controlled/repo", Name: "repo", SCM: "git", Private: &private, Workspace: bitbucket.Workspace{UUID: bbWorkspace, Slug: "controlled", Name: "Controlled"}}, pr: bitbucket.PullRequest{ID: 7, Title: "BBC-1 OUT-1", Description: &body, State: "OPEN", Author: bitbucket.User{UUID: bbSubjectOther, DisplayName: "External author"}, UpdatedAt: now}}
	f.pr.Destination.Repository.UUID = bbRepo
	f.pr.Source.Branch.Name = "BBC-1"
	content := "external body must not be copied"
	f.comment = bitbucket.Comment{ID: 9, Content: bitbucket.RawText{Raw: &content}, User: bitbucket.User{UUID: bbSubjectOther, DisplayName: "Comment author"}, CreatedAt: now, UpdatedAt: now}
	f.comment.PullRequest.ID = 7
	x.f = f
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-access" {
			t.Error("synthetic token mismatch")
		}
		f.mu.Lock()
		f.gets++
		repo, pr, comment, mode := f.repo, f.pr, f.comment, f.mode
		comments := append([]bitbucket.Comment(nil), f.comments...)
		count := f.commitCount
		after := f.afterPost
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		switch {
		case strings.HasPrefix(r.URL.Path, "/2.0/workspaces/"):
			reply(repo.Workspace)
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == "POST":
			var in struct {
				Content bitbucket.RawText `json:"content"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			f.posts++
			n := f.posts
			f.mu.Unlock()
			if mode == "429" {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
			if mode == "403" {
				w.WriteHeader(403)
				return
			}
			c := bitbucket.Comment{ID: 100 + int64(n), Content: in.Content, User: bitbucket.User{UUID: bbSubject, DisplayName: "Sender"}, CreatedAt: now, UpdatedAt: now}
			c.PullRequest.ID = 7
			f.mu.Lock()
			f.comments = append(f.comments, c)
			f.mu.Unlock()
			if after != nil {
				after()
			}
			if mode == "unknown" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			w.WriteHeader(201)
			reply(c)
		case strings.HasSuffix(r.URL.Path, "/comments"):
			next := ""
			if mode == "incomplete" {
				next = bitbucket.APIURL + strings.TrimPrefix(r.URL.EscapedPath(), "/2.0") + "?page=2"
			}
			reply(map[string]any{"values": comments, "next": next})
		case strings.HasSuffix(r.URL.Path, "/comments/9"):
			if mode == "comment404" {
				w.WriteHeader(404)
				return
			}
			reply(comment)
		case strings.HasSuffix(r.URL.Path, "/pullrequests/7"):
			reply(pr)
		case strings.Contains(r.URL.Path, "/commits"):
			commits := []map[string]any{}
			for i := 0; i < count; i++ {
				commits = append(commits, map[string]any{"hash": fmt.Sprintf("%040x", i+1), "message": "BBC-1", "date": now, "repository": map[string]any{"uuid": bbRepo}})
			}
			next := ""
			if len(commits) > 100 {
				if r.URL.Query().Get("page") == "2" {
					commits = commits[100:]
				} else {
					commits = commits[:100]
					next = bitbucket.APIURL + strings.TrimPrefix(r.URL.EscapedPath(), "/2.0") + "?page=2"
				}
			}
			reply(map[string]any{"values": commits, "next": next})
		default:
			reply(repo)
		}
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	x.s.Bitbucket = &bitbucket.Client{App: b.s.Bitbucket.App, Transport: azureOAuthTestTransport{base: target, transport: server.Client().Transport}}
	var access BitbucketAccess
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		access, e = x.s.PrepareBitbucketRepositories(ctx, tx, b.org, b.owner, c.ID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	yes, no := true, false
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		selection, e := x.s.SaveBitbucketRepository(ctx, tx, b.org, b.owner, access, BitbucketRepositoryInput{ConnectionVersion: access.ConnectionVersion, RepositoryID: bbRepo, SpaceIDs: []string{x.space}, PullRequests: &yes, Commits: &yes, BacklinkPrivate: &yes, BacklinkPublic: &no, ConfirmMetadata: true, ConfirmSender: true}, f.repo)
		x.selection = selection.ID
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := x.s.RotateBitbucketWebhook(ctx, tx, b.org, b.owner, c.ID, access.ConnectionVersion)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var sealed []byte
		if e := tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.ID).Scan(&sealed); e != nil {
			return e
		}
		creds, e := x.s.openCredentials(b.org, c.ID, sealed)
		x.secret, x.token = creds.WebhookSecret, creds.WebhookToken
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	x.worker = SyncWorker{Pool: b.pool, Service: x.s}
	return x
}
func (x *bbWorkerPG) body(kind string) []byte {
	p := map[string]any{"repository": map[string]any{"uuid": "{" + bbRepo + "}", "workspace": map[string]any{"uuid": "{" + bbWorkspace + "}"}}, "pullrequest": map[string]any{"id": 7}, "comment": map[string]any{"id": 9}}
	if kind == "repo:push" {
		old := map[string]any{"type": "branch", "name": "main", "target": map[string]any{"hash": strings.Repeat("a", 40)}}
		newer := map[string]any{"type": "branch", "name": "main", "target": map[string]any{"hash": strings.Repeat("b", 40)}}
		p["push"] = map[string]any{"changes": []any{map[string]any{"old": old, "new": newer}}}
	}
	raw, _ := json.Marshal(p)
	return raw
}
func (x *bbWorkerPG) receive(kind, request string, body []byte) (GitWebhookOutcome, error) {
	x.t.Helper()
	if request == "" {
		request, _ = membership.NewID()
	}
	mac := hmac.New(sha256.New, []byte(x.secret))
	mac.Write(body)
	headers := http.Header{"Content-Type": []string{"application/json"}, "X-Hub-Signature": []string{"sha256=" + hex.EncodeToString(mac.Sum(nil))}, "X-Hook-Uuid": []string{"{55555555-5555-4555-8555-555555555555}"}, "X-Request-Uuid": []string{request}, "X-Event-Key": []string{kind}}
	var out GitWebhookOutcome
	e := x.worker.inOrganization(x.ctx, x.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = x.s.ReceiveBitbucket(ctx, tx, GitWebhookLookup{OrganizationID: x.org, ConnectionID: x.connection}, x.token, headers, body)
		return e
	})
	return out, e
}
func (x *bbWorkerPG) queue(kind string) {
	x.t.Helper()
	out, e := x.receive(kind, "", x.body(kind))
	if e != nil || out.Status != 200 {
		x.t.Fatalf("queue %+v %v", out, e)
	}
}
func (x *bbWorkerPG) run() {
	x.t.Helper()
	if e := x.worker.RunBitbucketOrganization(x.ctx, x.org); e != nil {
		x.t.Fatal(e)
	}
}
func (x *bbWorkerPG) forceDue() {
	x.exec(`UPDATE openfox.integration_bitbucket_backlinks SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid AND state IN ('PENDING','UNKNOWN','SENDING')`, x.org)
}
func (x *bbWorkerPG) scalar(q string, args ...any) string {
	x.t.Helper()
	var s string
	if e := x.admin.QueryRow(x.ctx, q, args...).Scan(&s); e != nil {
		x.t.Fatal(e)
	}
	return s
}

func TestBitbucketWorkerPRDeliveryAndScopedLinks(t *testing.T) {
	x := newBBWorkerPG(t)
	req, _ := membership.NewID()
	body := x.body("pullrequest:created")
	for i := 0; i < 2; i++ {
		out, e := x.receive("pullrequest:created", req, body)
		if e != nil || out.Status != 200 || out.Duplicate != (i == 1) {
			t.Fatalf("delivery %+v %v", out, e)
		}
	}
	out, e := x.receive("pullrequest:updated", req, append(body, ' '))
	if e != nil || out.Status != 409 {
		t.Fatalf("conflict %+v %v", out, e)
	}
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); n != 1 {
		t.Fatal("scope", n)
	}
	if got := x.scalar(`SELECT repository FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); got != bbWorkspace+"/"+bbRepo {
		t.Fatal(got)
	}
	if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "SENT" {
		t.Fatal(got)
	}
	x.f.mu.Lock()
	posts := x.f.posts
	sent := *x.f.comments[0].Content.Raw
	x.f.mu.Unlock()
	if posts != 1 || strings.Contains(sent, "private-title") || strings.Contains(sent, "OUT-1") {
		t.Fatal("leak/duplicate")
	}
	x.queue("pullrequest:updated")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.pr_linked'`, x.org); n != 1 {
		t.Fatal("duplicate activity", n)
	}
	if got := x.scalar(`SELECT s.category FROM openfox.tasks t JOIN openfox.task_statuses s ON(s.organization_id,s.id)=(t.organization_id,t.status_id) WHERE t.organization_id=$1::uuid AND t.id=$2::uuid`, x.org, x.task); got != "TODO" {
		t.Fatal("workflow changed")
	}
}

func TestBitbucketWorkerCommentsSourceAndDeletionAuthority(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:comment_created")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.git_comment'`, x.org); n != 1 {
		t.Fatal(n)
	}
	if got := x.scalar(`SELECT safe_diff::text FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.git_comment'`, x.org); strings.Contains(got, "must not be copied") || !strings.Contains(got, "Comment author") {
		t.Fatal(got)
	}
	x.f.mu.Lock()
	x.f.mode = "comment404"
	x.f.mu.Unlock()
	x.queue("pullrequest:comment_deleted")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_comment_sources WHERE organization_id=$1::uuid AND deleted`, x.org); n != 0 {
		t.Fatal("404 falsely deleted")
	}
	x.f.mu.Lock()
	x.f.mode = ""
	x.f.comment.Deleted = true
	x.f.comment.UpdatedAt = x.f.comment.UpdatedAt.Add(time.Minute)
	x.f.mu.Unlock()
	x.queue("pullrequest:comment_updated")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_comment_sources WHERE organization_id=$1::uuid AND deleted`, x.org); n != 1 {
		t.Fatal("authoritative tombstone", n)
	}
	if n := x.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.git_comment'`, x.org); n != 1 {
		t.Fatal(n)
	}
}

func TestBitbucketWorkerUnknownReconcilesFrozenSender(t *testing.T) {
	x := newBBWorkerPG(t)
	x.f.mu.Lock()
	x.f.mode = "unknown"
	x.f.mu.Unlock()
	x.queue("pullrequest:created")
	x.run()
	if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "UNKNOWN" {
		t.Fatal(got)
	}
	x.exec(`UPDATE openfox.integration_bitbucket_runtime SET subject_id=$2::uuid,credential_generation=credential_generation+1 WHERE organization_id=$1::uuid`, x.org, bbSubjectOther)
	x.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, x.org, x.connection)
	x.forceDue()
	x.run()
	if got := x.scalar(`SELECT state||':'||sender_id::text FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "SENT:"+bbSubject {
		t.Fatal(got)
	}
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	if x.f.posts != 1 {
		t.Fatal("resent", x.f.posts)
	}
}

func TestBitbucketWorkerQueuedBacklinkRotationFenced(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:created")
	if _, e := x.worker.runBitbucketOrganization(x.ctx, x.org, 1); e != nil {
		t.Fatal(e)
	}
	x.exec(`UPDATE openfox.integration_connections SET version=version+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, x.org, x.connection)
	x.exec(`UPDATE openfox.integration_bitbucket_runtime SET hook_generation=hook_generation+1 WHERE organization_id=$1::uuid`, x.org)
	x.exec(`UPDATE openfox.integration_bitbucket_repositories SET version=version+1,hook_generation=0 WHERE organization_id=$1::uuid`, x.org)
	// A later valid hook must not re-enable a backlink admitted under old settings.
	x.queue("pullrequest:updated")
	x.run()
	x.run()
	if got := x.scalar(`SELECT state||':'||last_error FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "SKIPPED:stale_configuration" {
		t.Fatal(got)
	}
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	if x.f.posts != 0 {
		t.Fatal("stale send")
	}
}

func TestBitbucketWorkerCommitLimitAtomic(t *testing.T) {
	x := newBBWorkerPG(t)
	x.f.mu.Lock()
	x.f.commitCount = 101
	x.f.mu.Unlock()
	x.queue("repo:push")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); n != 0 {
		t.Fatal("partial links")
	}
	if got := x.scalar(`SELECT last_error FROM openfox.integration_bitbucket_work WHERE organization_id=$1::uuid`, x.org); got != "commit_limit" {
		t.Fatal(got)
	}
}

func TestBitbucketWorkerDiagnosticsPermissionsAndManualRetry(t *testing.T) {
	x := newBBWorkerPG(t)
	x.f.mu.Lock()
	x.f.mode = "unknown"
	x.f.mu.Unlock()
	x.queue("pullrequest:created")
	x.run()
	x.f.mu.Lock()
	x.f.comments = nil
	x.f.mu.Unlock()
	x.forceDue()
	x.run()
	var jobs []BitbucketJobDiagnostic
	e := x.tx(x.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		jobs, e = x.s.ListBitbucketJobs(ctx, tx, x.org, x.owner, x.connection)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var retry BitbucketRetryJobInput
	for _, job := range jobs {
		if job.Kind == "BACKLINK" {
			if !job.CanRetry || !job.RequiresUnsentConfirmation {
				t.Fatal(job)
			}
			retry = BitbucketRetryJobInput{Kind: job.Kind, ID: job.ID, Version: job.Version}
		}
	}
	e = x.tx(x.owner, func(ctx context.Context, tx pgx.Tx) error {
		return x.s.RetryBitbucketJob(ctx, tx, x.org, x.owner, x.connection, retry)
	})
	if !errors.Is(e, ErrInvalidRequest) {
		t.Fatal(e)
	}
	retry.ConfirmUnsent = true
	e = x.tx(x.member, func(ctx context.Context, tx pgx.Tx) error {
		return x.s.RetryBitbucketJob(ctx, tx, x.org, x.member, x.connection, retry)
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	e = x.tx(x.owner, func(ctx context.Context, tx pgx.Tx) error {
		return x.s.RetryBitbucketJob(ctx, tx, x.org, x.owner, x.connection, retry)
	})
	if e != nil {
		t.Fatal(e)
	}
	e = x.tx(x.owner, func(ctx context.Context, tx pgx.Tx) error {
		return x.s.RetryBitbucketJob(ctx, tx, x.org, x.owner, x.connection, retry)
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatal(e)
	}
}

func (x *bbWorkerPG) claim(resource string) *bitbucketWork {
	x.t.Helper()
	id := x.scalar(`SELECT id::text FROM openfox.integration_bitbucket_work WHERE organization_id=$1::uuid AND resource_key=$2`, x.org, resource)
	j, e := x.worker.claimBitbucketWork(x.ctx, x.org, id, x.connection, x.selection)
	if e != nil || j == nil {
		x.t.Fatalf("claim %v", e)
	}
	return j
}
func (x *bbWorkerPG) hydrate(j *bitbucketWork) bitbucketHydrated {
	x.t.Helper()
	h := bitbucketHydrated{}
	ctx, cancel := bitbucket.WithTraversalBudget(x.ctx)
	defer cancel()
	_, e := (BitbucketRuntime{Pool: x.pool, Service: x.s}).DoReadBatch(ctx, x.org, x.connection, j.ConnectionVersion, j.Generation, func(token string) error {
		var e error
		h.Repo, e = x.s.Bitbucket.Repository(ctx, token, bbWorkspace, bbRepo)
		if e != nil {
			return e
		}
		h.PR, e = x.s.Bitbucket.PullRequest(ctx, token, bbWorkspace, bbRepo, 7)
		return e
	})
	if e != nil {
		x.t.Fatal(e)
	}
	return h
}
func TestBitbucketWorkerSharedPRWatermarkAndLateApply(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:created")
	old := x.claim("PULL_REQUEST:7")
	h := x.hydrate(old)
	x.f.mu.Lock()
	x.f.pr.State = "MERGED"
	x.f.pr.UpdatedAt = x.f.pr.UpdatedAt.Add(time.Minute)
	x.f.mu.Unlock()
	x.queue("pullrequest:comment_created")
	x.run()
	if e := x.worker.finishBitbucketWork(x.ctx, x.org, old, h, nil); e != nil {
		t.Fatal(e)
	}
	if got := x.scalar(`SELECT state FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='PULL_REQUEST'`, x.org); got != "MERGED" {
		t.Fatal("late PR regressed", got)
	}
	x.f.mu.Lock()
	x.f.pr.State = "OPEN"
	x.f.pr.UpdatedAt = h.PR.UpdatedAt
	x.f.comment.UpdatedAt = x.f.comment.UpdatedAt.Add(2 * time.Minute)
	x.f.mu.Unlock()
	x.queue("pullrequest:comment_updated")
	x.run()
	if got := x.scalar(`SELECT state FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='PULL_REQUEST'`, x.org); got != "MERGED" {
		t.Fatal("older comment hydration regressed PR", got)
	}
	if n := x.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.git_comment'`, x.org); n != 1 {
		t.Fatal(n)
	}
}

func TestBitbucketWorkerNewDeliveryDuringReadAndPermissionRevocation(t *testing.T) {
	t.Run("newer_delivery", func(t *testing.T) {
		x := newBBWorkerPG(t)
		x.queue("pullrequest:created")
		j := x.claim("PULL_REQUEST:7")
		h := x.hydrate(j)
		x.queue("pullrequest:updated")
		if e := x.worker.finishBitbucketWork(x.ctx, x.org, j, h, nil); e != nil {
			t.Fatal(e)
		}
		if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); n != 0 {
			t.Fatal("old request applied")
		}
		x.run()
		if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); n != 1 {
			t.Fatal("new request lost")
		}
	})
	t.Run("space_revoked", func(t *testing.T) {
		x := newBBWorkerPG(t)
		x.queue("pullrequest:created")
		j := x.claim("PULL_REQUEST:7")
		h := x.hydrate(j)
		x.exec(`UPDATE openfox.space_members SET status='REVOKED' WHERE organization_id=$1::uuid AND space_id=$2::uuid`, x.org, x.space)
		if e := x.worker.finishBitbucketWork(x.ctx, x.org, j, h, nil); e != nil {
			t.Fatal(e)
		}
		if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); n != 0 {
			t.Fatal("revoked permission wrote")
		}
	})
}

func TestBitbucketWorkerConcurrentClaimsAndExpiredSend(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:created")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := SyncWorker{Pool: x.pool, Service: x.s}
			errs <- w.RunBitbucketOrganization(x.ctx, x.org)
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	x.run()
	x.f.mu.Lock()
	posts := x.f.posts
	x.f.mu.Unlock()
	if posts != 1 {
		t.Fatal("concurrent POST", posts)
	}
	x.exec(`UPDATE openfox.integration_bitbucket_backlinks SET state='SENDING',comment_id=NULL,next_attempt_at=clock_timestamp(),lease=gen_random_uuid(),lease_until=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, x.org)
	x.run()
	if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "SENT" {
		t.Fatal(got)
	}
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	if x.f.posts != 1 {
		t.Fatal("crash recovery resent")
	}
}

func TestBitbucketWorkerPrivacyChangeAndPostDisconnect(t *testing.T) {
	t.Run("became_public", func(t *testing.T) {
		x := newBBWorkerPG(t)
		x.queue("pullrequest:created")
		if _, e := x.worker.runBitbucketOrganization(x.ctx, x.org, 1); e != nil {
			t.Fatal(e)
		}
		x.f.mu.Lock()
		public := false
		x.f.repo.Private = &public
		x.f.mu.Unlock()
		x.run()
		x.f.mu.Lock()
		defer x.f.mu.Unlock()
		if x.f.posts != 0 {
			t.Fatal("public write")
		}
	})
	t.Run("disconnected_in_flight", func(t *testing.T) {
		x := newBBWorkerPG(t)
		x.f.mu.Lock()
		x.f.afterPost = func() {
			var version uint64
			if e := x.admin.QueryRow(x.ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, x.org, x.connection).Scan(&version); e != nil {
				t.Error(e)
				return
			}
			if e := x.tx(x.owner, func(ctx context.Context, tx pgx.Tx) error {
				_, e := x.s.Disconnect(ctx, tx, x.org, x.owner, x.connection, version, ConfirmPhrase("Bitbucket"))
				return e
			}); e != nil {
				t.Error(e)
			}
		}
		x.f.mu.Unlock()
		x.queue("pullrequest:created")
		x.run()
		if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "UNKNOWN" {
			t.Fatal("late send", got)
		}
	})
}

func TestBitbucketWorkerPersonalAttributionForwardOnly(t *testing.T) {
	x := newBBWorkerPG(t)
	x.bbPG.f.mu.Lock()
	x.bbPG.f.subject = bbSubjectOther
	x.bbPG.f.mu.Unlock()
	x.exec(`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'VIEWER')`, x.org, x.space, x.member.UserID)
	session := x.authorize(x.member, BitbucketStartInput{Purpose: PurposePersonal, ConnectionID: x.connection})
	var child Connection
	e := x.tx(x.member, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		child, _, e = x.s.CompleteBitbucketPersonal(ctx, tx, x.org, x.member, session.SessionID)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	x.queue("pullrequest:comment_created")
	x.run()
	if got := x.scalar(`SELECT author_user_id::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); got != x.member.UserID {
		t.Fatal(got)
	}
	e = x.tx(x.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := x.s.Disconnect(ctx, tx, x.org, x.member, child.ID, child.Version, "")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	x.f.mu.Lock()
	x.f.pr.UpdatedAt = x.f.pr.UpdatedAt.Add(time.Minute)
	x.f.comment.UpdatedAt = x.f.comment.UpdatedAt.Add(time.Minute)
	x.f.mu.Unlock()
	x.queue("pullrequest:comment_updated")
	x.run()
	if got := x.scalar(`SELECT safe_diff->>'author_user_id' FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.git_comment'`, x.org); got != x.member.UserID {
		t.Fatal("historical author lost")
	}
	var author *string
	e = x.worker.inOrganization(x.ctx, x.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := loadBitbucketWorkerSelection(ctx, tx, x.org, x.connection, x.selection)
		if e != nil {
			return e
		}
		author, e = bitbucketAuthor(ctx, tx, x.org, x.connection, bbSubjectOther, x.space)
		return e
	})
	if e != nil || author != nil {
		t.Fatal("unlinked attribution", e)
	}
}

func TestBitbucketWorkerApplyFailureIsBoundedAndAtomic(t *testing.T) {
	x := newBBWorkerPG(t)
	x.exec(`INSERT INTO openfox.tasks(organization_id,space_id,id,sequence,title,type_id,status_id) SELECT $1::uuid,$2::uuid,gen_random_uuid(),n,'extra',t.type_id,t.status_id FROM generate_series(2,101) n CROSS JOIN openfox.tasks t WHERE t.organization_id=$1::uuid AND t.id=$3::uuid`, x.org, x.space, x.task)
	x.exec(`INSERT INTO openfox.task_git_links(organization_id,space_id,task_id,connection_id,provider,kind,repository,repository_display,number,url,state) SELECT organization_id,space_id,id,$3::uuid,'bitbucket','PULL_REQUEST',$4,'controlled/repo',7,'https://bitbucket.org/controlled/repo/pull-requests/7','OPEN' FROM openfox.tasks WHERE organization_id=$1::uuid AND space_id=$2::uuid`, x.org, x.space, x.connection, bbWorkspace+"/"+bbRepo)
	x.queue("pullrequest:created")
	x.run()
	if got := x.scalar(`SELECT state||':'||last_error FROM openfox.integration_bitbucket_work WHERE organization_id=$1::uuid`, x.org); got != "PENDING:incomplete_result" {
		t.Fatal(got)
	}
	x.exec(`UPDATE openfox.integration_bitbucket_work SET attempts=4,next_attempt_at=clock_timestamp() WHERE organization_id=$1::uuid`, x.org)
	x.run()
	if got := x.scalar(`SELECT state||':'||attempts::text||':'||(next_attempt_at IS NULL)::text||':'||(lease IS NULL)::text FROM openfox.integration_bitbucket_work WHERE organization_id=$1::uuid`, x.org); got != "FAILED:5:true:true" {
		t.Fatal(got)
	}
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); n != 0 {
		t.Fatal("partial backlink")
	}
}

func TestBitbucketWorkerSignedReceiveBoundaries(t *testing.T) {
	x := newBBWorkerPG(t)
	body := x.body("pullrequest:created")
	req, _ := membership.NewID()
	sig := hmac.New(sha256.New, []byte(x.secret))
	sig.Write(body)
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Hub-Signature", "sha256="+hex.EncodeToString(sig.Sum(nil)))
	headers.Set("X-Hook-UUID", "{55555555-5555-4555-8555-555555555555}")
	headers.Set("X-Request-UUID", req)
	headers.Set("X-Event-Key", "pullrequest:created")
	call := func(org string, b []byte) (GitWebhookOutcome, error) {
		var out GitWebhookOutcome
		e := x.worker.inOrganization(x.ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			out, e = x.s.ReceiveBitbucket(ctx, tx, GitWebhookLookup{OrganizationID: x.org, ConnectionID: x.connection}, x.token, headers, b)
			return e
		})
		return out, e
	}
	if out, e := call(x.org, append(body, ' ')); e != nil || out.Status != 401 {
		t.Fatal(out, e)
	}
	if _, e := call(x.other, body); e == nil {
		t.Fatal("cross organization")
	}
	headers.Set("X-Hub-Signature", "")
	if out, e := call(x.org, body); e != nil || out.Status != 401 {
		t.Fatal(out, e)
	}
	if n := x.count(`SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE organization_id=$1::uuid`, x.org); n != 0 {
		t.Fatal(n)
	}
	// A body within the existing five MiB limit is accepted; one byte above
	// rejects before parsing/hydration. Padding is valid JSON whitespace.
	large := append(append([]byte(nil), body...), []byte(strings.Repeat(" ", MaxBitbucketWebhookBytes-len(body)))...)
	if out, e := x.receive("pullrequest:created", req, large); e != nil || out.Status != 200 {
		t.Fatal(out, e)
	}
	if out, e := x.receive("pullrequest:created", "", append(large, ' ')); e != nil || out.Status != 400 {
		t.Fatal(out, e)
	}
}

func TestBitbucketWorkerUnknownWakeOnlyNewHookGeneration(t *testing.T) {
	x := newBBWorkerPG(t)
	x.f.mu.Lock()
	x.f.mode = "unknown"
	x.f.mu.Unlock()
	x.queue("pullrequest:created")
	x.run()
	x.f.mu.Lock()
	x.f.comments = nil
	x.f.mu.Unlock()
	x.forceDue()
	x.run()
	x.queue("pullrequest:updated")
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid AND state='UNKNOWN' AND next_attempt_at IS NULL`, x.org); n != 1 {
		t.Fatal("ordinary event reopened exhausted reconciliation")
	}
	x.exec(`UPDATE openfox.integration_bitbucket_runtime SET hook_generation=hook_generation+1 WHERE organization_id=$1::uuid`, x.org)
	x.exec(`UPDATE openfox.integration_bitbucket_repositories SET hook_generation=0,version=version+1 WHERE organization_id=$1::uuid`, x.org)
	x.queue("pullrequest:updated")
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid AND state='UNKNOWN' AND next_attempt_at IS NOT NULL AND sender_id=$2::uuid`, x.org, bbSubject); n != 1 {
		t.Fatal("new hook did not schedule read-only reconciliation")
	}
}

func TestBitbucketWorkerAdmissionLocksCurrentTask(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:created")
	if _, e := x.worker.runBitbucketOrganization(x.ctx, x.org, 1); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- x.worker.inOrganization(x.ctx, x.org, func(ctx context.Context, tx pgx.Tx) error {
			b, e := scanBitbucketBacklink(tx.QueryRow(ctx, `SELECT `+bitbucketBacklinkColumns+` FROM openfox.integration_bitbucket_backlinks WHERE organization_id=openfox_private.current_organization_id()`))
			if e != nil {
				return e
			}
			if e = lockBitbucketBacklinkTasks(ctx, tx, b, []string{x.space}); e != nil {
				return e
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	update := make(chan error, 1)
	go func() {
		_, e := x.admin.Exec(x.ctx, `UPDATE openfox.tasks SET deleted_at=clock_timestamp(),deleted_by=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, x.org, x.task, x.owner.UserID)
		update <- e
	}()
	select {
	case e := <-update:
		close(release)
		t.Fatalf("task changed before admission committed: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if e := <-update; e != nil {
		t.Fatal(e)
	}
}

func TestBitbucketWorkerUnknownNeverResendsAmbiguousResults(t *testing.T) {
	for _, mode := range []string{"conflict", "incomplete", "marker_changed"} {
		t.Run(mode, func(t *testing.T) {
			x := newBBWorkerPG(t)
			x.f.mu.Lock()
			x.f.mode = "unknown"
			x.f.mu.Unlock()
			x.queue("pullrequest:created")
			x.run()
			x.f.mu.Lock()
			x.f.mode = mode
			if mode == "conflict" {
				copy := x.f.comments[0]
				copy.ID++
				x.f.comments = append(x.f.comments, copy)
			}
			if mode == "marker_changed" {
				changed := "modified by platform"
				x.f.comments[0].Content.Raw = &changed
			}
			x.f.mu.Unlock()
			x.forceDue()
			x.run()
			if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "UNKNOWN" {
				t.Fatal(got)
			}
			x.f.mu.Lock()
			defer x.f.mu.Unlock()
			if x.f.posts != 1 {
				t.Fatal("unsafe resend")
			}
		})
	}
}

func TestBitbucketWorkerCommitHundredBoundaryAndNoBacklink(t *testing.T) {
	x := newBBWorkerPG(t)
	x.f.mu.Lock()
	x.f.commitCount = 100
	x.f.mu.Unlock()
	x.queue("repo:push")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='COMMIT'`, x.org); n != 100 {
		t.Fatal(n)
	}
	x.queue("repo:push")
	x.run()
	if n := x.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id=$1::uuid AND kind='task.commit_linked'`, x.org); n != 100 {
		t.Fatal("duplicate commit activities", n)
	}
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); n != 0 {
		t.Fatal("commit backlink")
	}
}

func TestBitbucketWorkerDisconnectStopsPendingPreservesUnknown(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:created")
	if _, e := x.worker.runBitbucketOrganization(x.ctx, x.org, 1); e != nil {
		t.Fatal(e)
	}
	x.queue("repo:push")
	var version uint64
	if e := x.admin.QueryRow(x.ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, x.org, x.connection).Scan(&version); e != nil {
		t.Fatal(e)
	}
	if e := x.tx(x.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := x.s.Disconnect(ctx, tx, x.org, x.owner, x.connection, version, ConfirmPhrase("Bitbucket"))
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if n := x.count(`SELECT count(*) FROM openfox.integration_bitbucket_work WHERE organization_id=$1::uuid AND (next_attempt_at IS NOT NULL OR lease IS NOT NULL)`, x.org); n != 0 {
		t.Fatal("pending work remains")
	}
	if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "SKIPPED" {
		t.Fatal(got)
	}
	if n := x.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid`, x.org); n != 1 {
		t.Fatal("history removed")
	}
	x.run()
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	if x.f.posts != 0 {
		t.Fatal("disconnected sent")
	}
}

func TestBitbucketWorkerStage3RunOnceScansOrganization(t *testing.T) {
	x := newBBWorkerPG(t)
	x.queue("pullrequest:created")
	for i := 0; i < 10; i++ {
		if e := x.worker.RunOnce(x.ctx); e != nil {
			t.Fatal(e)
		}
		if x.count(`SELECT count(*) FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid AND state='SENT'`, x.org) == 1 {
			break
		}
	}
	if got := x.scalar(`SELECT state FROM openfox.integration_bitbucket_backlinks WHERE organization_id=$1::uuid`, x.org); got != "SENT" {
		t.Fatal(got)
	}
}
