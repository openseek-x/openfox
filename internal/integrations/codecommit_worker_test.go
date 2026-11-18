package integrations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/tenancy"
)

type ccWorkerRunner struct{ f *codeCommitDBFixture }

func (r ccWorkerRunner) RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	return r.f.run(r.f.org, fn)
}
func (r ccWorkerRunner) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	return r.f.run(org, fn)
}

type ccUpstream struct {
	mu                  sync.Mutex
	pulls               map[string]codecommit.PullRequest
	branches            map[string]string
	commits             map[string]codecommit.Commit
	posts               [][]byte
	saved               map[string][]byte
	unknown             bool
	postFailure         string
	readFailure         string
	calls               map[string]int
	blockAction         string
	blockResponseAction string
	entered, release    chan struct{}
	pageSize            int
}

func ccSHA(n int) string { return fmt.Sprintf("%040x", n) }
func ccCommit(n int, parents ...int) codecommit.Commit {
	p := []string{}
	for _, id := range parents {
		p = append(p, ccSHA(id))
	}
	return codecommit.Commit{ID: ccSHA(n), TreeID: ccSHA(999999), Message: "CCTEST-1 commit", Parents: p}
}
func ccPull(id int, title string, at time.Time) codecommit.PullRequest {
	return codecommit.PullRequest{ID: strconv.Itoa(id), Title: title, Status: "OPEN", AuthorARN: ccFixtureIdentity().Identity.ARN, RevisionID: fmt.Sprint(at.UnixMicro()), CreationDate: float64(at.UnixMicro()) / 1e6, LastActivityDate: float64(at.UnixMicro()) / 1e6, Targets: []codecommit.PullRequestTarget{{RepositoryName: "Repo", SourceCommit: ccSHA(2), DestinationCommit: ccSHA(1), SourceReference: "refs/heads/feature", DestinationReference: "refs/heads/main"}}}
}
func ccProtocol(t *testing.T, f *codeCommitDBFixture) *ccUpstream {
	t.Helper()
	u := &ccUpstream{pulls: map[string]codecommit.PullRequest{}, branches: map[string]string{"main": ccSHA(1)}, commits: map[string]codecommit.Commit{ccSHA(1): ccCommit(1), ccSHA(2): ccCommit(2, 1)}, saved: map[string][]byte{}, calls: map[string]int{}, pageSize: 100}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "codecommit.us-east-1.amazonaws.com" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("fixed signed request required")
			http.Error(w, "denied", 403)
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 128<<10))
		var in map[string]json.RawMessage
		if json.Unmarshal(raw, &in) != nil {
			t.Error("bad request")
		}
		text := func(key string) string { var v string; json.Unmarshal(in[key], &v); return v }
		action := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "CodeCommit_20150413.")
		u.mu.Lock()
		u.calls[action]++
		if u.blockAction == action {
			entered, release := u.entered, u.release
			u.blockAction = ""
			u.mu.Unlock()
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			u.mu.Lock()
		}
		var out any
		code := 200
		unknown := false
		if action == "GetRepository" && u.readFailure != "" {
			failure := u.readFailure
			u.mu.Unlock()
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"__type": failure})
			return
		}
		switch action {
		case "GetRepository":
			out = map[string]any{"repositoryMetadata": codecommit.Repository{ID: "opaque:Repo", Name: "Repo", AccountID: "123456789012", ARN: "arn:aws:codecommit:us-east-1:123456789012:Repo", DefaultBranch: "main"}}
		case "ListPullRequests":
			ids := []string{}
			for id, p := range u.pulls {
				if p.Status == text("pullRequestStatus") {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			start, _ := strconv.Atoi(text("nextToken"))
			end := min(len(ids), start+u.pageSize)
			if start > len(ids) {
				start = len(ids)
			}
			next := ""
			if end < len(ids) {
				next = strconv.Itoa(end)
			}
			out = map[string]any{"pullRequestIds": ids[start:end], "nextToken": next}
		case "GetPullRequest":
			p, ok := u.pulls[text("pullRequestId")]
			if !ok {
				code = 400
				out = map[string]any{"__type": "PullRequestDoesNotExistException"}
			} else {
				out = map[string]any{"pullRequest": p}
			}
		case "ListBranches":
			names := []string{}
			for name := range u.branches {
				names = append(names, name)
			}
			sort.Strings(names)
			out = map[string]any{"branches": names}
		case "GetBranch":
			id, ok := u.branches[text("branchName")]
			if !ok {
				code = 400
				out = map[string]any{"__type": "BranchDoesNotExistException"}
			} else {
				out = map[string]any{"branch": codecommit.Branch{Name: text("branchName"), CommitID: id}}
			}
		case "GetCommit":
			node, ok := u.commits[text("commitId")]
			if !ok {
				code = 400
				out = map[string]any{"__type": "CommitDoesNotExistException"}
			} else {
				out = map[string]any{"commit": node}
			}
		case "BatchGetCommits":
			var ids []string
			json.Unmarshal(in["commitIds"], &ids)
			nodes := []codecommit.Commit{}
			for _, id := range ids {
				if n, ok := u.commits[id]; ok {
					nodes = append(nodes, n)
				}
			}
			out = map[string]any{"commits": nodes, "errors": []any{}}
		case "DescribePullRequestEvents":
			out = map[string]any{"pullRequestEvents": []any{map[string]any{"pullRequestId": text("pullRequestId"), "pullRequestEventType": "PULL_REQUEST_CREATED", "eventDate": 1700000000, "pullRequestCreatedEventMetadata": map[string]any{"repositoryName": "Repo", "destinationCommitId": ccSHA(1), "sourceCommitId": ccSHA(2)}}}}
		case "PostCommentForPullRequest":
			u.posts = append(u.posts, append([]byte(nil), raw...))
			if u.postFailure != "" {
				code = 400
				out = map[string]any{"__type": u.postFailure}
				break
			}
			token := text("clientRequestToken")
			if previous, ok := u.saved[token]; ok && string(previous) != string(raw) {
				code = 400
				out = map[string]any{"__type": "IdempotencyParameterMismatchException"}
			} else {
				u.saved[token] = append([]byte(nil), raw...)
				id := "11111111-1111-4111-8111-111111111111:22222222-2222-4222-8222-222222222222"
				out = map[string]any{"repositoryName": text("repositoryName"), "pullRequestId": text("pullRequestId"), "beforeCommitId": text("beforeCommitId"), "afterCommitId": text("afterCommitId"), "comment": map[string]any{"commentId": id + "-" + strconv.Itoa(len(u.saved)), "clientRequestToken": token, "content": text("content"), "authorArn": ccFixtureIdentity().Identity.ARN, "creationDate": 1700000000}}
				unknown = u.unknown
				u.unknown = false
			}
		default:
			t.Errorf("unexpected action %s", action)
			code = 400
			out = map[string]any{}
		}
		var responseEntered, responseRelease chan struct{}
		if u.blockResponseAction == action {
			responseEntered, responseRelease = u.entered, u.release
			u.blockResponseAction = ""
		}
		u.mu.Unlock()
		if responseEntered != nil {
			close(responseEntered)
			select {
			case <-responseRelease:
			case <-r.Context().Done():
				return
			}
		}
		if unknown {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "codecommit.us-east-1.amazonaws.com:443" {
			return nil, errors.New("no external fallback")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	f.service.CodeCommitTransport = tr
	return u
}
func ccSetupWorker(t *testing.T, initialized bool) (*codeCommitDBFixture, *ccUpstream, *SyncWorker, CodeCommitAccess, CodeCommitBridgeResult) {
	f := newCodeCommitDBFixture(t)
	u := ccProtocol(t, f)
	connection := f.connection()
	a := f.access(connection)
	r, e := f.repository(a, "opaque:Repo", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	if initialized {
		f.exec(`UPDATE openfox.integration_codecommit_repositories SET initialized_at=clock_timestamp(),next_attempt_at=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID)
		now := time.Now()
		r.Repository.InitializedAt = &now
	}
	w := &SyncWorker{Pool: ccWorkerRunner{f}, Service: f.service}
	return f, u, w, a, r
}
func ccTask(f *codeCommitDBFixture, n int) string {
	id := refsTestID(f.t)
	f.exec(`INSERT INTO openfox.tasks(organization_id,space_id,id,sequence,title,type_id,status_id) SELECT $1::uuid,$2::uuid,$3::uuid,$4,'fixture',t.id,s.id FROM openfox.task_types t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id AND s.category='TODO' WHERE t.organization_id=$1::uuid AND t.space_id=$2::uuid`, f.org, f.space, id, n)
	return id
}
func ccQueue(t *testing.T, f *codeCommitDBFixture, a CodeCommitAccess, r CodeCommitRepository, id string) {
	t.Helper()
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return queueCodeCommitPR(ctx, tx, a, r, id, codeCommitDigest(refsTestID(t)), false)
	}); e != nil {
		t.Fatal(e)
	}
}
func ccRun(t *testing.T, w *SyncWorker, org string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if e := w.RunCodeCommitOrganization(context.Background(), org); e != nil {
			t.Fatal(e)
		}
	}
}
func ccScalar(t *testing.T, f *codeCommitDBFixture, q string, args ...any) string {
	t.Helper()
	var v string
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error { return tx.QueryRow(ctx, q, args...).Scan(&v) }); e != nil {
		t.Fatal(e)
	}
	return v
}
func ccJob(t *testing.T, f *codeCommitDBFixture, a CodeCommitAccess) CodeCommitJob {
	t.Helper()
	var page CodeCommitJobsPage
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		page, e = f.service.ListCodeCommitJobs(ctx, tx, f.org, f.actor, a.ID, "")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if len(page.Jobs) != 1 {
		prs := []CodeCommitJob{}
		for _, j := range page.Jobs {
			if j.Kind == "PR" {
				prs = append(prs, j)
			}
		}
		if len(prs) == 1 {
			return prs[0]
		}
		t.Fatalf("jobs=%d", len(page.Jobs))
	}
	return page.Jobs[0]
}
func ccRetry(t *testing.T, f *codeCommitDBFixture, a CodeCommitAccess, job CodeCommitJob, confirm bool) error {
	t.Helper()
	return f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return f.service.RetryCodeCommitWork(ctx, tx, f.org, f.actor, a.ID, job.ID, CodeCommitReconcileInput{a.Version, job.Version, confirm})
	})
}
func TestCodeCommitWorkerInitializationAndNewPullPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, false)
	ccTask(f, 1)
	old := time.Now().Add(-time.Hour)
	u.mu.Lock()
	u.pulls["1"] = ccPull(1, "CCTEST-1 old", old)
	u.pulls["2"] = ccPull(2, "old without reference", old)
	u.pageSize = 1
	u.mu.Unlock()
	ccRun(t, w, f.org, 12)
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "0" {
		t.Fatal("historical association", got)
	}
	if got := ccScalar(t, f, `SELECT (initialized_at IS NOT NULL)::text FROM openfox.integration_codecommit_repositories WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID); got != "true" {
		t.Fatal("not initialized")
	}
	u.mu.Lock()
	if u.calls["ListPullRequests"] < 3 || len(u.posts) != 0 {
		t.Fatal("pagination/history")
	}
	u.pulls["7"] = ccPull(7, "CCTEST-1 new", time.Now().Add(time.Second))
	u.mu.Unlock()
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 3)
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND provider='codecommit' AND backlink_comment_id<>''`, f.org); got != "1" {
		t.Fatal("new PR not linked/sent", got)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 1 {
		t.Fatalf("posts=%d", len(u.posts))
	}
}
func TestCodeCommitWorkerUnknownRetainsIntentAndNewGenerationPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	ccTask(f, 2)
	at := time.Now()
	u.pulls["7"] = ccPull(7, "CCTEST-1", at)
	u.unknown = true
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	job := ccJob(t, f, a)
	if job.BacklinkState != "UNKNOWN" || !job.CanReconcile {
		t.Fatalf("job=%+v", job)
	}
	u.mu.Lock()
	original := append([]byte(nil), u.posts[0]...)
	u.pulls["7"] = ccPull(7, "CCTEST-1 CCTEST-2", at.Add(time.Second))
	u.mu.Unlock()
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	u.mu.Lock()
	if len(u.posts) != 1 {
		t.Fatal("new event bypassed UNKNOWN")
	}
	u.mu.Unlock()
	job = ccJob(t, f, a)
	if e := ccRetry(t, f, a, job, false); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("missing confirmation accepted", e)
	}
	if e := ccRetry(t, f, a, job, true); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 4)
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 3 || string(u.posts[1]) != string(original) {
		t.Fatalf("posts=%d or immutable replay differs", len(u.posts))
	}
	var second map[string]any
	json.Unmarshal(u.posts[2], &second)
	body, _ := second["content"].(string)
	if strings.Contains(body, "CCTEST-1") || !strings.Contains(body, "CCTEST-2") {
		t.Fatal("new generation is not incremental", body)
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND length(backlink_comment_id)=64`, f.org); got != "2" {
		t.Fatal("long comment marker not persisted", got)
	}
}
func TestCodeCommitWorkerTimestampConflictAndRecoveryPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	at := time.Now().Truncate(time.Second)
	u.pulls["7"] = ccPull(7, "CCTEST-1 initial", at)
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 3)
	u.mu.Lock()
	p := u.pulls["7"]
	p.Title = "CCTEST-1 conflicting"
	u.pulls["7"] = p
	u.mu.Unlock()
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	job := ccJob(t, f, a)
	if job.LastError != "version_conflict" {
		t.Fatalf("job=%+v", job)
	}
	if got := ccScalar(t, f, `SELECT title FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "CCTEST-1 initial" {
		t.Fatal("overwrote equal-time copy", got)
	}
	u.mu.Lock()
	p.LastActivityDate = float64(at.Add(time.Second).Unix())
	p.RevisionID = "new"
	u.pulls["7"] = p
	u.mu.Unlock()
	if e := ccRetry(t, f, a, job, false); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 2)
	if got := ccScalar(t, f, `SELECT title FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != p.Title {
		t.Fatal("newer copy did not recover", got)
	}
	u.mu.Lock()
	p.LastActivityDate = float64(at.Add(-time.Second).Unix())
	p.Title = "old"
	u.pulls["7"] = p
	u.mu.Unlock()
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 2)
	if got := ccScalar(t, f, `SELECT title FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "CCTEST-1 conflicting" {
		t.Fatal("older source overwrote newer", got)
	}
}
func TestCodeCommitWorkerRevocationDuringHTTPPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
	u.blockAction = "GetPullRequest"
	u.entered = make(chan struct{})
	u.release = make(chan struct{})
	ccQueue(t, f, a, r.Repository, "7")
	done := make(chan error, 1)
	go func() { done <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
	select {
	case <-u.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no HTTP")
	}
	otherOwner := refsTestID(t)
	f.exec(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, otherOwner, otherOwner+"@example.test")
	f.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, f.org, otherOwner)
	f.exec(`UPDATE openfox.organization_members SET status='REVOKED',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
	close(u.release)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker stuck")
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "0" {
		t.Fatal("late link after revoke")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 0 {
		t.Fatal("sent after revoke")
	}
}
func TestCodeCommitWorkerPushBudgetKeepsCheckpointPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	f.exec(`INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES($1::uuid,$2::uuid,'main',$3,$3,1,1)`, f.org, r.Repository.ID, ccSHA(1))
	u.mu.Lock()
	for i := 2; i <= 102; i++ {
		u.commits[ccSHA(i)] = ccCommit(i, i-1)
	}
	u.branches["main"] = ccSHA(102)
	u.mu.Unlock()
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return queueCodeCommitPush(ctx, tx, a, r.Repository, "event", "main", ccSHA(1), ccSHA(102), codeCommitDigest("push"))
	}); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 1)
	job := ccJob(t, f, a)
	if job.LastError != "commit_limit" || job.State != "FAILED" {
		t.Fatalf("job=%+v", job)
	}
	if got := ccScalar(t, f, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=$1::uuid`, f.org); got != ccSHA(1) {
		t.Fatal("checkpoint advanced on incomplete graph")
	}
}
func TestCodeCommitWorkerConcurrentClaimAndLateDisconnectPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
	u.blockAction = "PostCommentForPullRequest"
	u.entered = make(chan struct{})
	u.release = make(chan struct{})
	ccQueue(t, f, a, r.Repository, "7")
	done := make(chan error, 1)
	go func() { done <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
	select {
	case <-u.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no write")
	}
	other := &SyncWorker{Pool: ccWorkerRunner{f}, Service: f.service}
	if e := other.RunCodeCommitOrganization(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.DisconnectCodeCommit(ctx, tx, f.org, f.actor, a.ID, a.Version, ConfirmPhrase("AWS CodeCommit"))
		return e
	}); e != nil {
		t.Fatal(e)
	}
	close(u.release)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late response stuck")
	}
	if got := ccScalar(t, f, `SELECT status FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, a.ID); got != "DISCONNECTED" {
		t.Fatal("connection resurrected")
	}
	if got := ccScalar(t, f, `SELECT state||'/'||backlink_state FROM openfox.integration_codecommit_work WHERE organization_id=$1::uuid`, f.org); got != "IGNORED/SENT" {
		t.Fatal("late fact lost", got)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 1 {
		t.Fatal("double claim")
	}
}

func TestCodeCommitWorkerSendingEligibilityAndUnknownRotationPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
	u.unknown = true
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	job := ccJob(t, f, a)
	f.exec(`UPDATE openfox.integration_codecommit_work SET state='FAILED',backlink_state='SENDING',lease_token=NULL,lease_until=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, job.ID)
	job = ccJob(t, f, a)
	if job.CanReconcile {
		t.Fatal("SENDING exposed retry action")
	}
	if e := ccRetry(t, f, a, job, true); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("SENDING accepted", e)
	}
	f.exec(`UPDATE openfox.integration_codecommit_work SET backlink_state='UNKNOWN' WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, job.ID)
	var rotated CodeCommitBridgeResult
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		rotated, e = f.service.ManageCodeCommitRepository(ctx, tx, f.org, f.actor, a.ID, r.Repository.ID, "rotate", CodeCommitVersionInput{a.Version, r.Repository.Version})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	ccQueue(t, f, a, rotated.Repository, "7")
	ccRun(t, w, f.org, 1)
	u.mu.Lock()
	if len(u.posts) != 1 {
		t.Fatal("rotation bypassed UNKNOWN")
	}
	original := append([]byte(nil), u.posts[0]...)
	u.mu.Unlock()
	job = ccJob(t, f, a)
	if !job.CanReconcile || job.BacklinkGeneration != 1 {
		t.Fatalf("job=%+v", job)
	}
	if e := ccRetry(t, f, a, job, true); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 4)
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 2 || string(u.posts[1]) != string(original) {
		t.Fatalf("resumed changed intent/posts=%d", len(u.posts))
	}
	job = ccJob(t, f, a)
	if job.BacklinkGeneration != 1 || job.BacklinkState != "SENT" {
		t.Fatal("send generation changed", job)
	}
}

func TestCodeCommitWorkerSettingsDisableSerializesPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
	ccQueue(t, f, a, r.Repository, "7")
	locked, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		writerDone <- f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, f.org+":codecommit:settings"); e != nil {
				return e
			}
			close(locked)
			<-release
			values := DefaultSettings()
			values.PRBacklinkPrivate = false
			_, e := f.service.PutSettings(ctx, tx, f.org, f.actor, ProviderCodeCommit, 0, values)
			return e
		})
	}()
	<-locked
	finished := make(chan error, 1)
	go func() { finished <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
	deadline := time.After(3 * time.Second)
	for {
		u.mu.Lock()
		got := u.calls["GetPullRequest"] > 0
		u.mu.Unlock()
		if got {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no authoritative read")
		case <-time.After(5 * time.Millisecond):
		}
	}
	select {
	case e := <-finished:
		t.Fatalf("read did not wait for concurrent first settings insert: %v", e)
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	if e := <-writerDone; e != nil {
		t.Fatal(e)
	}
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 0 {
		t.Fatal("sent despite committed disable")
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "1" {
		t.Fatal("association unexpectedly disabled")
	}
}
func TestCodeCommitWorkerCredentialFenceAndCrossTenantPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
	u.blockAction = "GetPullRequest"
	u.entered = make(chan struct{})
	u.release = make(chan struct{})
	ccQueue(t, f, a, r.Repository, "7")
	done := make(chan error, 1)
	go func() { done <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
	<-u.entered
	var newer Connection
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		in := ccFixtureInput()
		in.Version = a.Version
		plan, e := f.service.PrepareCodeCommitCredentials(ctx, tx, f.org, f.actor, a.ID, in)
		if e != nil {
			return e
		}
		newer, e = f.service.SaveCodeCommitCredentials(ctx, tx, f.org, f.actor, plan, ccFixtureIdentity())
		return e
	}); e != nil {
		t.Fatal(e)
	}
	close(u.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "0" {
		t.Fatal("stale credential result applied")
	}
	f.exec(`UPDATE openfox.integration_codecommit_work SET lease_until=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, f.org)
	ccRun(t, w, f.org, 3)
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "1" {
		t.Fatal("new authorized run failed")
	}
	other := newCodeCommitDBFixture(t)
	if e := other.run(other.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ListCodeCommitJobs(ctx, tx, other.org, other.actor, newer.ID, "")
		return e
	}); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross tenant access", e)
	}
}
func TestCodeCommitWorkerPushAppliesCompleteSetPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	values := DefaultSettings()
	values.LinkCommits = true
	raw, _ := json.Marshal(values)
	f.exec(`INSERT INTO openfox.integration_provider_settings(organization_id,provider,settings,updated_by) VALUES($1::uuid,'codecommit',$2::jsonb,$3::uuid)`, f.org, raw, f.user)
	f.exec(`INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES($1::uuid,$2::uuid,'main',$3,$3,1,1)`, f.org, r.Repository.ID, ccSHA(1))
	u.branches["main"] = ccSHA(2)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return queueCodeCommitPush(ctx, tx, a, r.Repository, "success", "main", ccSHA(1), ccSHA(2), codeCommitDigest("ok"))
	}); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 1)
	if got := ccScalar(t, f, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=$1::uuid`, f.org); got != ccSHA(2) {
		t.Fatal("checkpoint not advanced")
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND provider='codecommit' AND kind='COMMIT'`, f.org); got != "1" {
		t.Fatal("commit association missing")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 0 {
		t.Fatal("commit comment outside scope")
	}
}

func TestCodeCommitWorkerQueuedPushSurvivesInitializationPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, false)
	ccTask(f, 1)
	values := DefaultSettings()
	values.LinkCommits = true
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.PutSettings(ctx, tx, f.org, f.actor, ProviderCodeCommit, 0, values)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	u.branches["main"] = ccSHA(2)
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return queueCodeCommitPush(ctx, tx, a, r.Repository, "during-init", "main", ccSHA(1), ccSHA(2), codeCommitDigest("during-init"))
	}); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 7)
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='COMMIT'`, f.org); got != "1" {
		t.Fatal("initial baseline swallowed accepted future push", got)
	}
}
func TestCodeCommitWorkerUnchangedScanAndDuplicateQueuePG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, false)
	ccTask(f, 1)
	u.pulls["7"] = ccPull(7, "CCTEST-1 historical", time.Now().Add(-time.Hour))
	ccRun(t, w, f.org, 6)
	first := ccJob(t, f, a)
	f.exec(`UPDATE openfox.integration_codecommit_repositories SET next_attempt_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID)
	ccRun(t, w, f.org, 6)
	second := ccJob(t, f, a)
	if second.Version != first.Version {
		t.Fatalf("unchanged page unnecessarily requeued work: %d -> %d", first.Version, second.Version)
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != "0" {
		t.Fatal("second scan backfilled baseline")
	}
}

func TestCodeCommitWorkerUnknownCannotBecomeUnsentPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	ccTask(f, 2)
	u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
	u.unknown = true
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	job := ccJob(t, f, a)
	u.mu.Lock()
	u.postFailure = "AccessDeniedException"
	u.mu.Unlock()
	if e := ccRetry(t, f, a, job, true); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 1)
	job = ccJob(t, f, a)
	if job.BacklinkState != "UNKNOWN" || job.LastError != "permission_denied" {
		t.Fatalf("prior unknown became falsely unsent: %+v", job)
	}
	u.mu.Lock()
	u.postFailure = ""
	u.pulls["7"] = ccPull(7, "CCTEST-1 CCTEST-2", time.Now().Add(time.Second))
	u.mu.Unlock()
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 2 || string(u.posts[0]) != string(u.posts[1]) {
		t.Fatal("new generation bypassed unresolved first request")
	}
}

func TestCodeCommitWorkerCurrentSpacePermissionPG(t *testing.T) {
	for _, role := range []string{"VIEWER", "EDITOR"} {
		t.Run(role, func(t *testing.T) {
			f, u, w, a, r := ccSetupWorker(t, true)
			ccTask(f, 1)
			u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now())
			other := refsTestID(t)
			f.exec(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, other, other+"@example.test")
			f.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, f.org, other)
			f.exec(`UPDATE openfox.organization_members SET role='MEMBER',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
			f.exec(`UPDATE openfox.space_members SET role=$3 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user, role)
			ccQueue(t, f, a, r.Repository, "7")
			ccRun(t, w, f.org, 3)
			want := "0"
			if role == "EDITOR" {
				want = "1"
			}
			if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid`, f.org); got != want {
				t.Fatalf("role %s links %s", role, got)
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			if role == "VIEWER" && len(u.posts) != 0 {
				t.Fatal("viewer caused external write")
			}
		})
	}
}
func TestCodeCommitWorkerRateLimitBoundedRetriesPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	u.readFailure = "ThrottlingException"
	u.pulls["7"] = ccPull(7, "none", time.Now())
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	if got := ccScalar(t, f, `SELECT (next_attempt_at>clock_timestamp()+interval '40 seconds')::text FROM openfox.integration_codecommit_work WHERE organization_id=$1::uuid`, f.org); got != "true" {
		t.Fatal("ignored Retry-After")
	}
	for i := 0; i < 2; i++ {
		f.exec(`UPDATE openfox.integration_codecommit_work SET next_attempt_at=clock_timestamp() WHERE organization_id=$1::uuid`, f.org)
		ccRun(t, w, f.org, 1)
	}
	job := ccJob(t, f, a)
	if job.State != "FAILED" || job.NextAttemptAt != nil || job.LastError != "rate_limited" {
		t.Fatalf("unbounded retry %+v", job)
	}
	ccRun(t, w, f.org, 1)
	u.mu.Lock()
	calls := u.calls["GetRepository"]
	u.readFailure = ""
	u.mu.Unlock()
	if calls != 3 {
		t.Fatalf("requests=%d", calls)
	}
	if e := ccRetry(t, f, a, job, false); e != nil {
		t.Fatal(e)
	}
	ccRun(t, w, f.org, 1)
	job = ccJob(t, f, a)
	if job.State != "DONE" {
		t.Fatalf("retry did not recover %+v", job)
	}
}

func TestCodeCommitWorkerMissingSentAuditStopsNewGenerationPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	ccTask(f, 2)
	at := time.Now()
	u.pulls["7"] = ccPull(7, "CCTEST-1", at)
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 3)
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.audit_events WHERE organization_id=$1::uuid AND action='integration.codecommit.backlink_sent'`, f.org); got != "1" {
		t.Fatal("sent proof missing")
	}
	// Exclusive synthetic DB: emulate normal audit retention losing the last proof.
	f.exec(`DELETE FROM openfox.audit_events WHERE organization_id=$1::uuid AND action='integration.codecommit.backlink_sent'`, f.org)
	u.mu.Lock()
	u.pulls["7"] = ccPull(7, "CCTEST-1 CCTEST-2", at.Add(time.Second))
	u.mu.Unlock()
	ccQueue(t, f, a, r.Repository, "7")
	ccRun(t, w, f.org, 1)
	job := ccJob(t, f, a)
	if job.LastError != "version_conflict" || job.BacklinkGeneration != 1 {
		t.Fatalf("missing proof bypassed: %+v", job)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.posts) != 1 {
		t.Fatal("new generation despite unverifiable history")
	}
}

type ccLockTimeoutRunner struct{ ccWorkerRunner }

func (r ccLockTimeoutRunner) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	return r.f.run(org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='40ms'`); e != nil {
			return e
		}
		return fn(ctx, tx)
	})
}

func TestCodeCommitWorkerMemberLockTimeoutRetriesPG(t *testing.T) {
	for _, kind := range []string{"WORK", "SCAN"} {
		t.Run(kind, func(t *testing.T) {
			f, u, w, a, r := ccSetupWorker(t, kind == "WORK")
			ccTask(f, 1)
			u.pulls["7"] = ccPull(7, "CCTEST-1", time.Now().Add(time.Second))
			if kind == "WORK" {
				ccQueue(t, f, a, r.Repository, "7")
			}
			w.Pool = ccLockTimeoutRunner{ccWorkerRunner{f}}
			u.blockAction = "GetRepository"
			u.entered, u.release = make(chan struct{}), make(chan struct{})
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(u.release) }) })
			done := make(chan error, 1)
			go func() { done <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
			select {
			case <-u.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("not claimed before HTTP")
			}
			c, e := pgx.Connect(context.Background(), f.dsn)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close(context.Background())
			tx, e := c.Begin(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(context.Background())
			if _, e = tx.Exec(context.Background(), `SELECT user_id FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, f.org, f.user); e != nil {
				t.Fatal(e)
			}
			release.Do(func() { close(u.release) })
			select {
			case e = <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lock timeout not bounded")
			}
			if e = tx.Rollback(context.Background()); e != nil {
				t.Fatal(e)
			}
			var diagnostic string
			if kind == "WORK" {
				diagnostic = ccScalar(t, f, `SELECT state||':'||last_error||':'||(next_attempt_at IS NOT NULL)::text FROM openfox.integration_codecommit_work WHERE organization_id=$1::uuid`, f.org)
				if diagnostic != "PENDING:database_unavailable:true" {
					t.Fatalf("temporary member lock became permanent: %s", diagnostic)
				}
				f.exec(`UPDATE openfox.integration_codecommit_work SET next_attempt_at=clock_timestamp() WHERE organization_id=$1::uuid`, f.org)
				ccRun(t, w, f.org, 2)
				if ccJob(t, f, a).BacklinkState != "SENT" {
					t.Fatal("work did not recover")
				}
			} else {
				diagnostic = ccScalar(t, f, `SELECT last_error||':'||(next_attempt_at IS NOT NULL)::text FROM openfox.integration_codecommit_repositories WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID)
				if diagnostic != "database_unavailable:true" {
					t.Fatalf("temporary member lock stopped scan: %s", diagnostic)
				}
				f.exec(`UPDATE openfox.integration_codecommit_repositories SET next_attempt_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID)
				ccRun(t, w, f.org, 5)
				if ccScalar(t, f, `SELECT (initialized_at IS NOT NULL)::text FROM openfox.integration_codecommit_repositories WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID) != "true" {
					t.Fatal("scan did not recover")
				}
			}
		})
	}
}

func TestCodeCommitWorkerDelayedActivationPushPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, false)
	ccTask(f, 1)
	settings := DefaultSettings()
	settings.LinkCommits = true
	if err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.service.PutSettings(ctx, tx, f.org, f.actor, ProviderCodeCommit, 0, settings)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Repository creation already recorded activated_at. A push A->B occurs now,
	// but its asynchronous bridge delivery has not reached OpenFox yet.
	u.mu.Lock()
	u.branches["main"] = ccSHA(2)
	u.mu.Unlock()
	ccRun(t, w, f.org, 6)
	if got := ccScalar(t, f, `SELECT (initialized_at IS NOT NULL)::text FROM openfox.integration_codecommit_repositories WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, r.Repository.ID); got != "true" {
		t.Fatal("baseline setup failed", got)
	}
	// The same legitimate push arrives only after the first REST baseline is done.
	if err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		return queueCodeCommitPush(ctx, tx, a, r.Repository, "delayed-after-activation", "main", ccSHA(1), ccSHA(2), codeCommitDigest("delayed-after-activation"))
	}); err != nil {
		t.Fatal(err)
	}
	ccRun(t, w, f.org, 1)
	state := ccScalar(t, f, `SELECT state||':'||linked_count::text FROM openfox.integration_codecommit_work WHERE organization_id=$1::uuid AND kind='PUSH'`, f.org)
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='COMMIT'`, f.org); got != "1" {
		t.Fatalf("activation baseline consumed a later-delivered post-activation push: links=%s work=%s", got, state)
	}
}

func TestCodeCommitWorkerRapidForcePushPreservesEachEventPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	ccTask(f, 2)
	settings := DefaultSettings()
	settings.LinkCommits = true
	if err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.service.PutSettings(ctx, tx, f.org, f.actor, ProviderCodeCommit, 0, settings)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES($1::uuid,$2::uuid,'main',$3,$3,1,1)`, f.org, r.Repository.ID, ccSHA(1))
	// A->B then a force-push B->C, where B and C are sibling commits over A.
	u.mu.Lock()
	u.commits[ccSHA(2)] = ccCommit(2, 1)
	c := ccCommit(3, 1)
	c.Message = "CCTEST-2"
	u.commits[ccSHA(3)] = c
	u.branches["main"] = ccSHA(3)
	u.mu.Unlock()
	for i, pair := range [][2]int{{1, 2}, {2, 3}} {
		if err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return queueCodeCommitPush(ctx, tx, a, r.Repository, fmt.Sprint("rapid-", i), "main", ccSHA(pair[0]), ccSHA(pair[1]), codeCommitDigest(fmt.Sprint("rapid-", i)))
		}); err != nil {
			t.Fatal(err)
		}
	}
	ccRun(t, w, f.org, 3)
	got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='COMMIT'`, f.org)
	if got != "2" {
		t.Fatalf("current-tip shortcut lost an accepted force-push side branch: linked=%s want=2", got)
	}
}

func TestCodeCommitWorkerConcurrentPushKeepsNewerCheckpointPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	ccTask(f, 2)
	settings := DefaultSettings()
	settings.LinkCommits = true
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.PutSettings(ctx, tx, f.org, f.actor, ProviderCodeCommit, 0, settings)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	f.exec(`INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES($1::uuid,$2::uuid,'main',$3,$3,1,1)`, f.org, r.Repository.ID, ccSHA(1))
	u.branches["main"] = ccSHA(2)
	c := ccCommit(3, 2)
	c.Message = "CCTEST-2"
	u.commits[ccSHA(3)] = c
	u.blockAction = "GetCommit"
	u.entered, u.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(u.release) }) })
	queue := func(key string, old, next int) {
		t.Helper()
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return queueCodeCommitPush(ctx, tx, a, r.Repository, key, "main", ccSHA(old), ccSHA(next), codeCommitDigest(key))
		}); e != nil {
			t.Fatal(e)
		}
	}
	queue("first", 1, 2)
	done := make(chan error, 1)
	go func() { done <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
	select {
	case <-u.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first push did not reach graph HTTP")
	}
	u.mu.Lock()
	u.branches["main"] = ccSHA(3)
	u.mu.Unlock()
	queue("second", 2, 3)
	other := &SyncWorker{Pool: ccWorkerRunner{f}, Service: f.service}
	ccRun(t, other, f.org, 1)
	if got := ccScalar(t, f, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=$1::uuid`, f.org); got != ccSHA(3) {
		t.Fatal("second push did not advance checkpoint", got)
	}
	release.Do(func() { close(u.release) })
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first push did not settle")
	}
	if got := ccScalar(t, f, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=$1::uuid`, f.org); got != ccSHA(3) {
		t.Fatal("late first push regressed checkpoint", got)
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='COMMIT'`, f.org); got != "2" {
		t.Fatal("accepted push association lost", got)
	}
}

func TestCodeCommitWorkerLateBranchResponseKeepsNewerCheckpointPG(t *testing.T) {
	f, u, w, a, r := ccSetupWorker(t, true)
	ccTask(f, 1)
	ccTask(f, 2)
	settings := DefaultSettings()
	settings.LinkCommits = true
	if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.PutSettings(ctx, tx, f.org, f.actor, ProviderCodeCommit, 0, settings)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	f.exec(`INSERT INTO openfox.integration_codecommit_references(organization_id,repository_row_id,reference_name,observed_sha,applied_sha,generation,scan_generation) VALUES($1::uuid,$2::uuid,'main',$3,$3,1,1)`, f.org, r.Repository.ID, ccSHA(1))
	u.branches["main"] = ccSHA(2)
	c := ccCommit(3, 2)
	c.Message = "CCTEST-2"
	u.commits[ccSHA(3)] = c
	u.blockResponseAction = "GetBranch"
	u.entered, u.release = make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(u.release) }) })
	queue := func(key string, old, next int) {
		t.Helper()
		if e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			return queueCodeCommitPush(ctx, tx, a, r.Repository, key, "main", ccSHA(old), ccSHA(next), codeCommitDigest(key))
		}); e != nil {
			t.Fatal(e)
		}
	}
	queue("first", 1, 2)
	done := make(chan error, 1)
	go func() { done <- w.RunCodeCommitOrganization(context.Background(), f.org) }()
	select {
	case <-u.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first push did not prepare branch response")
	}
	u.mu.Lock()
	u.branches["main"] = ccSHA(3)
	u.mu.Unlock()
	queue("second", 2, 3)
	other := &SyncWorker{Pool: ccWorkerRunner{f}, Service: f.service}
	ccRun(t, other, f.org, 1)
	if got := ccScalar(t, f, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=$1::uuid`, f.org); got != ccSHA(3) {
		t.Fatal("second push did not advance checkpoint", got)
	}
	release.Do(func() { close(u.release) })
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first push did not settle")
	}
	if got := ccScalar(t, f, `SELECT applied_sha FROM openfox.integration_codecommit_references WHERE organization_id=$1::uuid`, f.org); got != ccSHA(3) {
		t.Fatal("late first push regressed checkpoint", got)
	}
	if got := ccScalar(t, f, `SELECT count(*)::text FROM openfox.task_git_links WHERE organization_id=$1::uuid AND kind='COMMIT'`, f.org); got != "2" {
		t.Fatal("accepted push association lost", got)
	}
}
