package azuredevops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPushCommitsSuccessfulRetryAfterStopsBeforeNextPage(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "30")
		page := []Commit{}
		for i := 0; i < 50; i++ {
			page = append(page, Commit{ID: fmt.Sprintf("%040x", i+1)})
		}
		jsonReply(w, map[string]any{"value": page})
	})
	commits, err := c.PushCommits(context.Background(), "token", "org", projectID, repoID, 15)
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Code != "rate_limited" || upstream.HTTPStatus != 200 || upstream.RetryAfter != 30*time.Second || commits != nil || calls.Load() != 1 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
}

func repoFixture() Repository {
	return Repository{ID: repoID, Name: "repo", Project: Project{ID: projectID, Name: "Project"}}
}
func actorFixture() IdentityRef { return IdentityRef{ID: subjectID, DisplayName: "Verified user"} }
func TestPRAndPushVerifyRepositoryProjectAndIdentity(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Origin") != "https://dev.azure.com" || r.Header.Get("Authorization") != "Bearer token" || r.URL.Query().Get("api-version") != "7.1" {
			t.Error("origin/auth/version")
		}
		switch r.URL.Path {
		case repoPath("org", projectID, repoID) + "/pullRequests/12":
			jsonReply(w, PullRequest{ID: 12, Repository: repoFixture(), CreatedBy: actorFixture(), Title: "Task ABC-1", Description: "plain\nbody", Status: "active", SourceRefName: "refs/heads/branch", TargetRefName: "refs/heads/main"})
		case repoPath("org", projectID, repoID) + "/pushes/15":
			if r.URL.Query().Get("includeRefUpdates") != "true" || r.URL.Query().Get("includeCommits") != "0" {
				t.Error("push query")
			}
			jsonReply(w, Push{ID: 15, Repository: repoFixture(), PushedBy: actorFixture(), Date: time.Now(), RefUpdates: []RefUpdate{{Name: "refs/heads/main", OldObjectID: strings.Repeat("0", 40), NewObjectID: strings.Repeat("a", 40)}}})
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	p, e := c.PullRequest(context.Background(), "token", "org", projectID, repoID, 12)
	if e != nil || p.ID != 12 || p.CreatedBy.ID != subjectID {
		t.Fatal(p, e)
	}
	push, e := c.Push(context.Background(), "token", "org", projectID, repoID, 15)
	if e != nil || push.PushedBy.ID != subjectID || len(push.RefUpdates) != 1 {
		t.Fatal(push, e)
	}
	for _, bad := range []string{"repository", "project", "creator", "id", "title", "status"} {
		t.Run(bad, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				p := PullRequest{ID: 12, Repository: repoFixture(), CreatedBy: actorFixture(), Title: "Task", Status: "active", SourceRefName: "refs/heads/x", TargetRefName: "refs/heads/main"}
				switch bad {
				case "repository":
					p.Repository.ID = accountID
				case "project":
					p.Repository.Project.ID = accountID
				case "creator":
					p.CreatedBy.ID = "email@example.test"
				case "id":
					p.ID = 13
				case "title":
					p.Title = strings.Repeat("x", MaxCellBytes+1)
				case "status":
					p.Status = "future"
				}
				jsonReply(w, p)
			})
			_, e := c.PullRequest(context.Background(), "token", "org", projectID, repoID, 12)
			errorCode(t, e, "invalid_response", false)
		})
	}
}
func TestPushCommitsComplete100AndReject101DuplicateOrMissingPage(t *testing.T) {
	for _, n := range []int{0, 1, 49, 50, 100, 101} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			var calls atomic.Int32
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				q := r.URL.Query()
				top, _ := strconv.Atoi(q.Get("top"))
				skip, _ := strconv.Atoi(q.Get("skip"))
				if q.Get("$top") != "" || q.Get("$skip") != "" || q.Get("pushId") != "15" || top < 1 || top > 50 || skip < 0 {
					t.Error("wrong paging query")
				}
				page := []Commit{}
				for i := skip; i < min(skip+top, n); i++ {
					page = append(page, Commit{ID: fmt.Sprintf("%040x", i+1), Comment: "fix ABC-1", Author: CommitAuthor{Email: "spoof@example.test"}})
				}
				jsonReply(w, map[string]any{"value": page})
			})
			out, e := c.PushCommits(context.Background(), "token", "org", projectID, repoID, 15)
			if n > 100 {
				errorCode(t, e, "source_limit", false)
				if out != nil {
					t.Fatal("partial commits returned")
				}
			} else if e != nil || len(out) != n {
				t.Fatal(len(out), e)
			}
			if calls.Load() > 3 {
				t.Fatal("unbounded requests")
			}
		})
	}
	for _, mode := range []string{"duplicate", "missing", "oversized", "bad_sha"} {
		t.Run(mode, func(t *testing.T) {
			var calls int
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "missing" {
					jsonReply(w, map[string]any{})
					return
				}
				if mode == "bad_sha" {
					jsonReply(w, map[string]any{"value": []Commit{{ID: "1"}}})
					return
				}
				size := 50
				if mode == "oversized" {
					size = 51
				}
				page := []Commit{}
				for i := 0; i < size; i++ {
					page = append(page, Commit{ID: fmt.Sprintf("%040x", i+1)})
				}
				jsonReply(w, map[string]any{"value": page})
			})
			out, e := c.PushCommits(context.Background(), "token", "org", projectID, repoID, 15)
			errorCode(t, e, "invalid_response", false)
			if out != nil || calls > 2 {
				t.Fatal("accepted partial/repeated page")
			}
		})
	}
}
func TestThreadReconciliationAndUnknownWrites(t *testing.T) {
	content := "ABC-1 https://openfox.example/tasks/ABC-1\n[operation:123]"
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		thread := Thread{ID: 5, Status: "active", Comments: []ThreadComment{{ID: 1, Author: actorFixture(), Content: content, CommentType: "text"}}}
		if r.Method == "POST" {
			var body map[string]any
			decodeFixture(t, r, &body)
			if _, ok := body["threadContext"]; ok {
				t.Error("unexpected file context")
			}
			jsonReply(w, thread)
		} else {
			jsonReply(w, map[string]any{"value": []Thread{thread}})
		}
	})
	thread, e := c.CreateThread(context.Background(), "token", "org", projectID, repoID, 12, content)
	if e != nil || thread.ID != 5 || thread.Comments[0].ID != 1 {
		t.Fatal(thread, e)
	}
	list, e := c.Threads(context.Background(), "token", "org", projectID, repoID, 12)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	for _, mode := range []string{"malformed", "invalid_id", "server_error", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "malformed":
					fmt.Fprint(w, "{")
				case "invalid_id":
					jsonReply(w, Thread{})
				case "server_error":
					w.WriteHeader(500)
					fmt.Fprint(w, "secret upstream body")
				case "redirect":
					w.Header().Set("Location", "https://evil.example/secret")
					w.WriteHeader(302)
				}
			})
			_, e := c.CreateThread(context.Background(), "token", "org", projectID, repoID, 12, content)
			var expected string
			switch mode {
			case "server_error", "redirect":
				expected = "upstream_error"
			default:
				expected = "invalid_response"
			}
			errorCode(t, e, expected, mode != "redirect")
			if strings.Contains(e.Error(), "secret") {
				t.Fatal("secret leak")
			}
		})
	}
	for _, mode := range []string{"cursor", "too_many", "missing", "count_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "count_mismatch" {
					jsonReply(w, map[string]any{"count": 1, "value": []Thread{}})
					return
				}
				if mode == "missing" {
					jsonReply(w, map[string]any{})
					return
				}
				list := []Thread{}
				if mode == "cursor" {
					w.Header().Set("x-ms-continuationtoken", "more")
				}
				if mode == "too_many" {
					list = make([]Thread, MaxItems+1)
				}
				jsonReply(w, map[string]any{"value": list})
			})
			out, e := c.Threads(context.Background(), "token", "org", projectID, repoID, 12)
			if e == nil || out != nil {
				t.Fatal("partial thread list looks complete")
			}
		})
	}
}
