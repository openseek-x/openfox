package cnb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"openfox/internal/integrations/gitapi"
)

func TestOfficialProtocol(t *testing.T) {
	head, before := strings.Repeat("a", 40), strings.Repeat("b", 40)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Accept") != "application/vnd.cnb.api+json" {
			t.Errorf("missing protocol headers")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(map[string]any{"id": "cnb-string-user", "username": "fixture"})
		case "/user/repos":
			if r.URL.Query().Get("role") != "Reporter" || r.URL.Query().Get("page_size") != "50" || r.URL.Query().Get("search") != "team" {
				t.Errorf("query=%v", r.URL.Query())
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"id": "repo-id", "path": "team/sub/repo", "visibility_level": "Private"}, map[string]any{"id": "secret-id", "path": "team/secrets", "visibility_level": "Secret"}})
		case "/team/sub/repo":
			json.NewEncoder(w).Encode(map[string]any{"id": "repo-id", "path": "team/sub/repo", "visibility_level": "Private"})
		case "/team/sub/repo/-/pulls/11":
			json.NewEncoder(w).Encode(map[string]any{"number": "11", "state": "closed", "base": map[string]any{"repo": map[string]any{"id": "repo-id", "path": "team/sub/repo"}}})
		case "/team/sub/repo/-/git/commits/" + head:
			json.NewEncoder(w).Encode(map[string]any{"sha": head, "commit": map[string]any{"message": "FOX-1"}})
		case "/team/sub/repo/-/git/compare/" + before + "..." + head:
			json.NewEncoder(w).Encode(map[string]any{"base_commit": map[string]any{"sha": before}, "head_commit": map[string]any{"sha": head}, "total_commits": 1, "commits": []any{map[string]any{"sha": head, "commit": map[string]any{"message": "FOX-1"}}}})
		case "/team/sub/repo/-/pulls/11/comments":
			if r.Method == "POST" {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["work_mode"] != false || body["body"] != "fixture body" {
					t.Errorf("comment shape=%v", body)
				}
				w.WriteHeader(201)
				json.NewEncoder(w).Encode(map[string]any{"id": "string-comment-id"})
			} else {
				json.NewEncoder(w).Encode([]any{map[string]any{"id": "string-comment-id", "body": "fixture marker"}})
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := &Client{APIURL: server.URL, HTTP: server.Client()}
	ctx := context.Background()
	if id, _, err := c.Identity(ctx, "fixture-token"); err != nil || id != "cnb-string-user" {
		t.Fatalf("identity %s %v", id, err)
	}
	if rows, more, err := c.Repositories(ctx, "fixture-token", "team", 1); err != nil || more || len(rows) != 1 {
		t.Fatalf("repositories %+v %v %v", rows, more, err)
	}
	if row, err := c.Repository(ctx, "fixture-token", "team/sub/repo"); err != nil || !row.Private() {
		t.Fatalf("repository %+v %v", row, err)
	}
	if row, err := c.Pull(ctx, "fixture-token", "team/sub/repo", 11); err != nil || row.State != "closed" {
		t.Fatalf("pull %+v %v", row, err)
	}
	for _, base := range []string{before, strings.Repeat("0", 40)} {
		if rows, err := c.Commits(ctx, "fixture-token", "team/sub/repo", base, head); err != nil || len(rows) != 1 {
			t.Fatalf("commits %+v %v", rows, err)
		}
	}
	if id, err := c.Comment(ctx, "fixture-token", "team/sub/repo", 11, "fixture body"); err != nil || id != "string-comment-id" {
		t.Fatalf("comment %s %v", id, err)
	}
	if id, err := c.FindComment(ctx, "fixture-token", "team/sub/repo", 11, "fixture marker"); err != nil || id != "string-comment-id" {
		t.Fatalf("reconcile %s %v", id, err)
	}
	if requests != 8 {
		t.Fatalf("requests=%d", requests)
	}
	if PullURL("cnb/plugins/cnbcool/webhook", 11) != "https://cnb.cool/cnb/plugins/cnbcool/webhook/-/pulls/11" {
		t.Fatal("verified web URL changed")
	}
}

func TestErrorsAndNoCredentialRedirect(t *testing.T) {
	for _, check := range []struct {
		name   string
		status int
		want   error
	}{{"unauthorized", 401, gitapi.ErrTokenRevoked}, {"capability", 403, ErrForbidden}, {"missing", 404, gitapi.ErrNotFound}, {"server", 500, gitapi.ErrRequestFailed}} {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(check.status) }))
			defer server.Close()
			c := &Client{APIURL: server.URL, HTTP: server.Client()}
			_, err := c.Repository(context.Background(), "fake", "org/repo")
			if !errors.Is(err, check.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	t.Run("rate limit", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
		defer server.Close()
		c := &Client{APIURL: server.URL, HTTP: server.Client()}
		_, err := c.Repository(context.Background(), "fake", "org/repo")
		var limited *gitapi.ErrRateLimited
		if !errors.As(err, &limited) || limited.ResetAt.IsZero() {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("unknown post", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
		defer server.Close()
		c := &Client{APIURL: server.URL, HTTP: server.Client()}
		_, err := c.Comment(context.Background(), "fake", "org/repo", 1, "x")
		if !errors.Is(err, gitapi.ErrUnknownResult) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		received := false
		target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
		defer target.Close()
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
		defer server.Close()
		c := &Client{APIURL: server.URL, HTTP: server.Client()}
		_, err := c.Repository(context.Background(), "must-not-forward", "org/repo")
		if err == nil || received {
			t.Fatal("redirect followed")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat(" ", maxResponse+1))) }))
		defer server.Close()
		c := &Client{APIURL: server.URL, HTTP: server.Client()}
		_, err := c.Repository(context.Background(), "fake", "org/repo")
		if !errors.Is(err, gitapi.ErrRequestFailed) {
			t.Fatal(err)
		}
	})
}

func TestIncompleteCompareAndPathBoundaries(t *testing.T) {
	for _, total := range []int{101, 2} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"total_commits": total, "commits": []any{map[string]any{"sha": strings.Repeat("a", 40)}}})
			}))
			defer server.Close()
			c := &Client{APIURL: server.URL, HTTP: server.Client()}
			_, err := c.Commits(context.Background(), "fake", "org/repo", strings.Repeat("b", 40), strings.Repeat("a", 40))
			if !errors.Is(err, ErrCommitLimit) {
				t.Fatal(err)
			}
		})
	}
	for _, path := range []string{"single", "org/../repo", "org//repo", "/org/repo", "org/repo\n", "org/repo\\x"} {
		if ValidPath(path) {
			t.Fatalf("accepted %q", path)
		}
	}
	if repoPath("org/nested/repo%2Fname") != "org/nested/repo%252Fname" {
		t.Fatal("path changed meaning")
	}
}
