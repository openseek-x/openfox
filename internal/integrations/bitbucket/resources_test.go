package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/gitapi"
)

const repoUUID = "0855551c-8a74-492d-b1df-004735dca190"
const otherUUID = "0ffd8055-70ef-4a6d-b4aa-b2126656b40e"

func repoFixture() map[string]any {
	return map[string]any{"uuid": "{" + repoUUID + "}", "full_name": "renamed/repository", "name": "Repository", "scm": "git", "is_private": true}
}
func workspaceFixture() map[string]any {
	return map[string]any{"uuid": "{" + fixtureUUID + "}", "slug": "renamed", "name": "Workspace"}
}
func TestRepositoryUUIDRenameAndFreshWorkspaceBinding(t *testing.T) {
	base, _ := repoURL(fixtureUUID, repoUUID)
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/workspaces/") {
			respond(w, workspaceFixture())
			return
		}
		if APIURL+strings.TrimPrefix(r.URL.EscapedPath(), "/2.0") != base {
			t.Error("repository must be UUID addressed")
		}
		respond(w, repoFixture())
	})
	out, err := c.Repository(context.Background(), "access-fixture", fixtureUUID, repoUUID)
	if err != nil || out.UUID != repoUUID || out.Workspace.UUID != fixtureUUID || out.FullName != "renamed/repository" || tr.calls.Load() != 2 {
		t.Fatalf("repo=%+v err=%v calls=%d", out, err, tr.calls.Load())
	}
	if RepositoryURL(out.FullName) != "https://bitbucket.org/renamed/repository" {
		t.Fatal("navigation")
	}
}
func TestRepositoryRejectsChangedScopeIdentityAndUnknownPrivacy(t *testing.T) {
	for _, name := range []string{"transfer_path", "transfer_uuid", "rebuilt_slug", "unknown_privacy", "unknown_scm", "host_path", "wrong_workspace"} {
		t.Run(name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/workspaces/") {
					item := workspaceFixture()
					if name == "wrong_workspace" {
						item["uuid"] = otherUUID
					}
					respond(w, item)
					return
				}
				item := repoFixture()
				switch name {
				case "transfer_path":
					item["full_name"] = "different/repository"
				case "transfer_uuid":
					item["workspace"] = map[string]any{"uuid": otherUUID}
				case "rebuilt_slug":
					item["uuid"] = otherUUID
				case "unknown_privacy":
					delete(item, "is_private")
				case "unknown_scm":
					item["scm"] = "unknown"
				case "host_path":
					item["full_name"] = "renamed/repository?redirect=outside"
				}
				respond(w, item)
			})
			if _, err := c.Repository(context.Background(), "access-fixture", fixtureUUID, repoUUID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted %s: %v", name, err)
			}
		})
	}
}
func TestRepositoryCollectionOpaqueNextAndNoCredentialRedirect(t *testing.T) {
	path, _ := uuidPath(fixtureUUID)
	collection := APIURL + "/repositories/" + path
	var pages atomic.Int32
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/workspaces/") {
			respond(w, workspaceFixture())
			return
		}
		if pages.Add(1) == 1 {
			respond(w, map[string]any{"values": []any{repoFixture()}, "next": collection + "?opaque=next"})
			return
		}
		if r.URL.Query().Get("opaque") != "next" {
			t.Error("opaque cursor not retained")
		}
		respond(w, map[string]any{"values": []any{}})
	})
	first, err := c.Repositories(context.Background(), "access-fixture", fixtureUUID, "")
	if err != nil || len(first.Values) != 1 {
		t.Fatal(err)
	}
	last, err := c.Repositories(context.Background(), "access-fixture", fixtureUUID, first.Next)
	if err != nil || len(last.Values) != 0 || last.Next != "" {
		t.Fatal(err)
	}
	before := tr.calls.Load()
	_, err = c.Repositories(context.Background(), "access-fixture", fixtureUUID, "https://outside.invalid/leak")
	if err == nil || tr.calls.Load() != before+1 {
		t.Fatal("unsafe pagination followed")
	}
}
func pullFixture() map[string]any {
	return map[string]any{"id": 7, "title": "ABC-1 title", "summary": map[string]any{"raw": "ABC-2 body"}, "state": "OPEN", "draft": true, "updated_on": "2026-11-14T01:00:00Z", "author": map[string]any{"uuid": fixtureUUID}, "source": map[string]any{"branch": map[string]any{"name": "ABC-3"}}, "destination": map[string]any{"repository": map[string]any{"uuid": repoUUID}}}
}
func TestPullRequestStateBodyAndScopedRepository(t *testing.T) {
	for _, state := range []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED", "FUTURE"} {
		t.Run(state, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				out := pullFixture()
				out["state"] = state
				respond(w, out)
			})
			out, err := c.PullRequest(context.Background(), "access-fixture", fixtureUUID, repoUUID, 7)
			if err != nil || out.Body() != "ABC-2 body" || out.Author.UUID != fixtureUUID {
				t.Fatal(err)
			}
			link, e := out.LinkState()
			want := map[string]string{"OPEN": "DRAFT", "MERGED": "MERGED", "DECLINED": "CLOSED"}[state]
			if link != want || (want == "") != (e != nil) {
				t.Fatalf("state=%s link=%s err=%v", state, link, e)
			}
		})
	}
	for _, field := range []string{"id", "destination", "updated_on", "summary"} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			out := pullFixture()
			delete(out, field)
			respond(w, out)
		})
		if _, err := c.PullRequest(context.Background(), "access-fixture", fixtureUUID, repoUUID, 7); err == nil {
			t.Fatalf("accepted missing %s", field)
		}
	}
}
func commentFixture(id int64, body string) map[string]any {
	return map[string]any{"id": id, "pullrequest": map[string]any{"id": 7}, "content": map[string]any{"raw": body}, "user": map[string]any{"uuid": fixtureUUID}, "created_on": "2026-11-14T01:00:00Z", "updated_on": "2026-11-14T01:00:00Z", "deleted": false}
}
func TestCommentReadAndCreateOnlyContentRaw(t *testing.T) {
	var posted atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posted.Add(1)
			var body map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 {
				t.Error("unexpected comment fields")
			}
			var raw map[string]string
			_ = json.Unmarshal(body["content"], &raw)
			if len(raw) != 1 || raw["raw"] != "ABC-1 link marker" {
				t.Error("comment payload")
			}
			w.WriteHeader(201)
			respond(w, commentFixture(9, raw["raw"]))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/9") {
			respond(w, commentFixture(9, "text"))
			return
		}
		respond(w, map[string]any{"values": []any{commentFixture(9, "text")}})
	})
	out, err := c.CreateComment(context.Background(), "access-fixture", fixtureUUID, repoUUID, 7, "ABC-1 link marker")
	if err != nil || out.ID != 9 || posted.Load() != 1 {
		t.Fatal(err)
	}
	if _, err = c.Comment(context.Background(), "access-fixture", fixtureUUID, repoUUID, 7, 9); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Comments(context.Background(), "access-fixture", fixtureUUID, repoUUID, 7, "")
	if err != nil || len(rows.Values) != 1 {
		t.Fatal(err)
	}
}
func TestCommentUncertainSendNeverRetries(t *testing.T) {
	for _, name := range []string{"server_error", "wrong_pr", "missing_author", "missing_id", "unexpected_200", "truncated", "lost_response"} {
		t.Run(name, func(t *testing.T) {
			c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				out := commentFixture(9, "marker")
				status := 201
				switch name {
				case "server_error":
					status = 502
				case "wrong_pr":
					out["pullrequest"] = map[string]any{"id": 8}
				case "missing_author":
					delete(out, "user")
				case "missing_id":
					delete(out, "id")
				case "unexpected_200":
					status = 200
				case "truncated":
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{"id":9`))
					return
				case "lost_response":
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(status)
				respond(w, out)
			})
			_, err := c.CreateComment(context.Background(), "access-fixture", fixtureUUID, repoUUID, 7, "marker")
			if !errors.Is(err, gitapi.ErrUnknownResult) || tr.calls.Load() != 1 {
				t.Fatalf("unknown=%v requests=%d", err, tr.calls.Load())
			}
		})
	}
}
func commitFixture(n int) map[string]any {
	return map[string]any{"hash": fmt.Sprintf("%040x", n), "message": "ABC-1 commit", "date": "2026-11-14T01:00:00Z", "author": map[string]any{"raw": "outside <unmapped@example.invalid>"}}
}
func TestCommitDifferenceCompletesPagesBeforeReturning(t *testing.T) {
	base, _ := repoURL(fixtureUUID, repoUUID)
	collection := base + "/commits"
	var pages atomic.Int32
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if pages.Add(1) == 1 {
			if r.URL.Query().Get("include") != fmt.Sprintf("%040x", 3) || r.URL.Query().Get("exclude") != fmt.Sprintf("%040x", 1) || r.URL.Query().Get("pagelen") != "100" {
				t.Error("difference contract")
			}
			respond(w, map[string]any{"values": []any{commitFixture(2)}, "next": collection + "?cursor=opaque"})
			return
		}
		if r.URL.Query().Get("cursor") != "opaque" {
			t.Error("must follow opaque next")
		}
		respond(w, map[string]any{"values": []any{commitFixture(2), commitFixture(3)}})
	})
	out, err := c.CommitDifference(context.Background(), "access-fixture", fixtureUUID, repoUUID, fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 3))
	if err != nil || len(out) != 2 || tr.calls.Load() != 2 || out[0].Author.User != nil {
		t.Fatalf("result=%+v err=%v", out, err)
	}
}
func TestCommitDifferenceBudgetAndNoPartialSuccess(t *testing.T) {
	for _, name := range []string{"100", "101", "loop", "cross_repo", "failed_page", "bad_sha", "bytes"} {
		t.Run(name, func(t *testing.T) {
			base, _ := repoURL(fixtureUUID, repoUUID)
			collection := base + "/commits"
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				items := []any{commitFixture(n + 1)}
				next := ""
				switch name {
				case "100", "101":
					if n == 1 {
						items = []any{}
						for i := 1; i <= 100; i++ {
							items = append(items, commitFixture(i))
						}
						if name == "101" {
							next = collection + "?page=2"
						}
					} else {
						items = []any{commitFixture(101)}
					}
				case "loop":
					next = collection + "?page=" + strconv.Itoa(n%2+1)
				case "cross_repo":
					next = APIURL + "/repositories/other/another/commits?page=2"
				case "failed_page":
					if n == 2 {
						w.WriteHeader(500)
						return
					}
					next = collection + "?page=2"
				case "bad_sha":
					items = []any{map[string]any{"hash": "short", "date": "2026-11-14T01:00:00Z"}}
				case "bytes":
					next = collection + "?page=" + strconv.Itoa(n+1)
				}
				out := map[string]any{"values": items, "next": next}
				if name == "bytes" {
					out["ignored_extension"] = strings.Repeat("x", 1900<<10)
				}
				respond(w, out)
			})
			out, err := c.CommitDifference(context.Background(), "access-fixture", fixtureUUID, repoUUID, fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 101))
			if name == "100" {
				if err != nil || len(out) != 100 {
					t.Fatalf("100: count=%d err=%v", len(out), err)
				}
				return
			}
			if err == nil || out != nil {
				t.Fatalf("partial accepted: %d %v", len(out), err)
			}
			if name == "101" && !errors.Is(err, ErrCommitLimit) {
				t.Fatal(err)
			}
			if name == "bytes" && (!errors.Is(err, ErrIncomplete) || calls.Load() != 6) {
				t.Fatalf("raw byte budget %v requests=%d", err, calls.Load())
			}
		})
	}
}
func TestSharedTraversalBudgetCannotMultiplyAcrossBranches(t *testing.T) {
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) { respond(w, map[string]any{"values": []any{}}) })
	ctx, cancel := WithTraversalBudget(context.Background())
	defer cancel()
	for i := 0; i < 21; i++ {
		_, err := c.CommitDifference(ctx, "access-fixture", fixtureUUID, repoUUID, fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 2))
		if (i == 20) != (err != nil) {
			t.Fatalf("iteration=%d err=%v", i, err)
		}
	}
	if tr.calls.Load() != 20 {
		t.Fatalf("requests=%d", tr.calls.Load())
	}
}
func TestRateLimitBoundMatchesApprovedOneHour(t *testing.T) {
	for _, value := range []string{"99999999999", "0", time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat), time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)} {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", value); w.WriteHeader(429) })
		_, err := c.Identity(context.Background(), "access-fixture")
		var rate *gitapi.ErrRateLimited
		if !errors.As(err, &rate) || rate.ResetAt.Before(time.Now()) || rate.ResetAt.After(time.Now().Add(time.Hour)) {
			t.Fatalf("retry-after=%s err=%v", value, err)
		}
	}
}
