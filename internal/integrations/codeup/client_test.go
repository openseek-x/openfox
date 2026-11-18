package codeup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"openfox/internal/integrations/gitapi"
	"strings"
	"testing"
	"time"
)

func TestOfficialContract(t *testing.T) {
	var writes int
	h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-yunxiao-token") != "test" || r.Header.Get("Authorization") != "" {
			t.Error("wrong credential header")
		}
		switch {
		case r.URL.Path == "/oapi/v1/platform/user":
			json.NewEncoder(w).Encode(map[string]any{"id": "u1", "username": "name"})
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			if r.URL.Query().Get("perPage") == "20" {
				w.Header().Set("x-next-page", "2")
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"id": 123, "pathWithNamespace": "cloud/repo", "name": "repo", "webUrl": "https://codeup.aliyun.com/cloud/repo", "visibility": "internal"}})
		case strings.HasSuffix(r.URL.Path, "/diffs/patches"):
			json.NewEncoder(w).Encode([]any{map[string]any{"patchSetBizId": "target", "versionNo": 9, "relatedMergeItemType": "MERGE_TARGET"}, map[string]any{"patchSetBizId": "source", "versionNo": 2, "relatedMergeItemType": "MERGE_SOURCE"}})
		case strings.HasSuffix(r.URL.Path, "/comments/list"):
			if r.Method != "POST" {
				t.Error("list must be POST")
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"comment_biz_id": "c1", "content": "marker", "child_comments_list": []any{}}})
		case strings.HasSuffix(r.URL.Path, "/comments"):
			writes++
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["comment_type"] != "GLOBAL_COMMENT" || in["patchset_biz_id"] != "source" || in["draft"] != false || in["resolved"] != false {
				t.Error(in)
			}
			json.NewEncoder(w).Encode(map[string]any{"comment_biz_id": "c1"})
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer h.Close()
	c := &Client{APIURL: h.URL, HTTP: h.Client()}
	ctx := context.Background()
	id, _, e := c.Identity(ctx, "test", "cloud")
	if e != nil || len(id) != 64 {
		t.Fatal(id, e)
	}
	repos, more, e := c.Repositories(ctx, "test", "cloud", "repo", 1)
	if e != nil || !more || len(repos) != 1 || !repos[0].Private() {
		t.Fatal(repos, more, e)
	}
	p, e := c.Patchset(ctx, "test", "cloud", "123", 1)
	if e != nil || p != "source" {
		t.Fatal(p, e)
	}
	if _, e = c.Comment(ctx, "test", "cloud", "123", 1, p, "body"); e != nil {
		t.Fatal(e)
	}
	if id, e = c.FindComment(ctx, "test", "cloud", "123", 1, "marker"); e != nil || id != "c1" || writes != 1 {
		t.Fatal(id, e, writes)
	}
}
func TestHTTPBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
		write  bool
	}{
		{"401", 401, "", gitapi.ErrTokenRevoked, false}, {"403", 403, "", ErrForbidden, false}, {"old_token", 400, `{"code":"UnsupportedCurrentTokenType"}`, ErrUnsupported, false}, {"malformed_read", 200, "{", gitapi.ErrRequestFailed, false}, {"malformed_post", 200, "{", gitapi.ErrUnknownResult, true}, {"unknown_post", 502, "", gitapi.ErrUnknownResult, true}, {"large_read", 200, strings.Repeat("x", maxResponse+1), gitapi.ErrRequestFailed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer h.Close()
			c := &Client{APIURL: h.URL, HTTP: h.Client()}
			method, path := "GET", "/x"
			if tc.write {
				method = "POST"
			}
			var out any
			_, e := c.do(context.Background(), "fake", method, path, nil, &out)
			if !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
		})
	}
	t.Run("redirect credential", func(t *testing.T) {
		hits := 0
		other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
		defer other.Close()
		h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
		defer h.Close()
		c := &Client{APIURL: h.URL, HTTP: h.Client()}
		if _, e := c.do(context.Background(), "secret", "GET", "/x", nil, nil); e == nil || hits != 0 {
			t.Fatal(e, hits)
		}
	})
	t.Run("rate limit", func(t *testing.T) {
		h := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "120"); w.WriteHeader(429) }))
		defer h.Close()
		c := &Client{APIURL: h.URL, HTTP: h.Client()}
		_, e := c.do(context.Background(), "fake", "GET", "/x", nil, nil)
		var limited *gitapi.ErrRateLimited
		if !errors.As(e, &limited) || time.Until(limited.ResetAt) < 119*time.Second {
			t.Fatal(e)
		}
	})
}
func TestIDsAndURL(t *testing.T) {
	for _, raw := range []string{`"123"`, `123`} {
		var id ID
		if json.Unmarshal([]byte(raw), &id) != nil || id != "123" {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{`-1`, `1.5`, `0`, `"1/2"`, `null`} {
		var id ID
		if json.Unmarshal([]byte(raw), &id) == nil {
			t.Fatal(raw)
		}
	}
	for _, u := range []string{"http://codeup.aliyun.com/cloud/repo", "https://evil.test/cloud/repo", "https://user@codeup.aliyun.com/cloud/repo", "https://codeup.aliyun.com/cloud%2Frepo", "https://codeup.aliyun.com/cloud/repo?token=x"} {
		if ValidURL(u, "cloud/repo", "") {
			t.Fatal(u)
		}
	}
}

func TestCommitURLPreservesPath(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, path := range []string{"cloud/nested/repo", "cloud/repo%name", "cloud/代码"} {
		if !ValidPath(path) || !ValidURL(CommitURL(path, sha), path, "/commits/"+sha) {
			t.Fatal(path, CommitURL(path, sha))
		}
	}
	for _, path := range []string{"cloud/a\u0000b", "cloud/a\u00a0b", "cloud/../repo", "cloud//repo"} {
		if ValidPath(path) {
			t.Fatal(path)
		}
	}
}
