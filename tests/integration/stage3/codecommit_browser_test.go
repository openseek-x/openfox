package stage3

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Caller holds f.mu; these are local TLS responses, never real AWS requests.
func (f *codeCommitHTTPUpstream) browserReply(w http.ResponseWriter, r *http.Request, in map[string]string) bool {
	var out any
	switch r.Header.Get("X-Amz-Target") {
	case "CodeCommit_20150413.ListPullRequests":
		ids := []string{}
		if f.created > 0 && in["pullRequestStatus"] == "OPEN" {
			ids = append(ids, "7")
		}
		out = map[string]any{"pullRequestIds": ids}
	case "CodeCommit_20150413.ListBranches":
		out = map[string]any{"branches": []string{}}
	case "CodeCommit_20150413.GetPullRequest":
		if f.created == 0 {
			f.created = float64(time.Now().UnixMicro()) / 1e6
		}
		out = map[string]any{"pullRequest": map[string]any{"pullRequestId": "7", "title": f.title, "description": "Controlled local TLS PR", "pullRequestStatus": "OPEN", "authorArn": "arn:aws:iam::123456789012:user/fixture", "revisionId": "opaque-browser-revision", "creationDate": f.created, "lastActivityDate": f.created, "pullRequestTargets": []any{map[string]any{"repositoryName": "Repo", "sourceCommit": strings.Repeat("b", 40), "destinationCommit": strings.Repeat("a", 40), "sourceReference": "refs/heads/feature", "destinationReference": "refs/heads/main"}}}}
	case "CodeCommit_20150413.DescribePullRequestEvents":
		out = map[string]any{"pullRequestEvents": []any{map[string]any{"pullRequestId": "7", "pullRequestEventType": "PULL_REQUEST_CREATED", "eventDate": f.created, "pullRequestCreatedEventMetadata": map[string]any{"repositoryName": "Repo", "destinationCommitId": strings.Repeat("a", 40), "sourceCommitId": strings.Repeat("b", 40)}}}}
	case "CodeCommit_20150413.PostCommentForPullRequest":
		if f.commentTokens == nil {
			f.commentTokens = map[string]bool{}
		}
		if !f.commentTokens[in["clientRequestToken"]] {
			f.comments++
			f.commentTokens[in["clientRequestToken"]] = true
		}
		out = map[string]any{"repositoryName": in["repositoryName"], "pullRequestId": in["pullRequestId"], "beforeCommitId": in["beforeCommitId"], "afterCommitId": in["afterCommitId"], "comment": map[string]any{"commentId": "11111111-1111-4111-8111-111111111111:22222222-2222-4222-8222-222222222222", "authorArn": "arn:aws:iam::123456789012:user/fixture", "clientRequestToken": in["clientRequestToken"], "content": in["content"], "creationDate": f.created}}
	default:
		return false
	}
	_ = json.NewEncoder(w).Encode(out)
	return true
}

func TestCodeCommitBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_I13_BROWSER_FILE")
	if file == "" {
		t.Skip("exclusive local browser fixture not requested")
	}
	e, f := newCodeCommitHTTPEnv(t)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "I13 CodeCommit browser task"})
	f.title = task.DisplayID + " controlled CodeCommit PR"
	raw, _ := json.Marshal(map[string]any{"organization_id": e.org, "space_id": e.space.ID, "task_id": task.ID, "task_key": task.DisplayID, "email": s3Email(s3OwnerID), "password": s3Password, "api_url": e.server.URL})
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			t.Fatal("browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(file + ".stop"); err == nil {
				var links, sent int
				e.scalar(&links, `SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid AND provider='codecommit'`, e.org, task.ID)
				e.scalar(&sent, `SELECT count(*) FROM openfox.integration_codecommit_work WHERE organization_id=$1::uuid AND backlink_state='SENT'`, e.org)
				f.mu.Lock()
				requests, comments := f.requests, f.comments
				f.mu.Unlock()
				proof, _ := json.MarshalIndent(map[string]any{"links": links, "sent_work": sent, "upstream_requests": requests, "unique_comments": comments}, "", "  ")
				if err := os.WriteFile(file+".proof.json", proof, 0600); err != nil {
					t.Fatal(err)
				}
				if links != 1 || sent != 1 || comments != 1 {
					t.Fatalf("browser proof links=%d sent=%d uniquecomments=%d", links, sent, comments)
				}
				return
			}
			if err := f.worker.RunOnce(e.ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
}
