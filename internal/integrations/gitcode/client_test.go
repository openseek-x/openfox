package gitcode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/gitapi"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	up, _ := url.Parse(server.URL)
	return New(roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.atomgit.com" || !strings.HasPrefix(r.URL.Path, "/api/v5/") || r.URL.Query().Has("access_token") {
			t.Errorf("unexpected origin: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing fixture authorization")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = up.Scheme
		u.Host = up.Host
		clone.URL = &u
		return server.Client().Transport.RoundTrip(clone)
	}))
}

const issueJSON = `{"id":300,"number":7,"title":"Issue","body":"body","state":"opened","html_url":"https://api.atomgit.com/team/repo/issues/7","user":{"id":"opaque-A","login":"alice"},"repository":{"id":9,"full_name":"team/repo"},"created_at":"2026-11-14T00:00:00Z","updated_at":"2026-11-14T00:00:01Z"}`

func TestPrincipalAndOrganization(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/user":
			w.Write([]byte(`{"id":"opaque-A","login":"alice"}`))
		case "/api/v5/users/orgs":
			if r.URL.Query().Get("admin") != "true" {
				t.Error("missing admin filter")
			}
			w.Write([]byte(`[{"id":8,"login":"team"}]`))
		case "/api/v5/user/memberships/orgs/team":
			w.Write([]byte(`{"active":true,"role":"admin","user":{"id":"opaque-A","login":"alice"},"organization":{"id":8,"login":"team"}}`))
		default:
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	p, e := c.Principal(t.Context(), "fixture-token")
	if e != nil || p.ID != "opaque-A" {
		t.Fatal(p, e)
	}
	org, e := c.Organizations(t.Context(), "fixture-token")
	if e != nil || len(org) != 1 || org[0].ID != 8 {
		t.Fatal(org, e)
	}
	m, e := c.Membership(t.Context(), "fixture-token", "team")
	if e != nil || !m.Active || m.User.ID != p.ID {
		t.Fatal(m, e)
	}
	if _, e := c.User(t.Context(), "fixture-token"); !errors.Is(e, ErrUnsupported) {
		t.Fatal("generic numeric identity accepted", e)
	}
}
func TestIssueWritesUseDocumentedPathsAndReadBack(t *testing.T) {
	var posts, patches, gets atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST", "PATCH":
			var b map[string]any
			if json.NewDecoder(r.Body).Decode(&b) != nil {
				t.Fatal("body")
			}
			if _, ok := b["assignee"]; ok {
				t.Error("assignee must not be sent")
			}
			if _, ok := b["state_reason"]; ok {
				t.Error("unsupported field")
			}
			if b["repo"] != "repo" || b["title"] != "Issue" {
				t.Errorf("body %#v", b)
			}
			if r.Method == "POST" {
				posts.Add(1)
				if r.URL.Path != "/api/v5/repos/team/issues" {
					t.Error(r.URL.Path)
				}
				w.Write([]byte(issueJSON))
			} else {
				patches.Add(1)
				if r.URL.Path != "/api/v5/repos/team/issues/7" || b["state"] != "close" {
					t.Error(r.URL.Path, b)
				}
				w.WriteHeader(200)
			}
		case "GET":
			gets.Add(1)
			if r.URL.Path != "/api/v5/repos/team/repo/issues/7" {
				t.Error(r.URL.Path)
			}
			w.Write([]byte(strings.Replace(issueJSON, `"opened"`, `"closed"`, 1)))
		}
	})
	v, e := c.CreateIssue(t.Context(), "fixture-token", "team", "repo", "Issue", "body", nil)
	if e != nil || v.User.ExternalKey != "opaque-A" || !v.AssigneesMissing {
		t.Fatal(v, e)
	}
	v, e = c.UpdateIssue(t.Context(), "fixture-token", "team", "repo", "7", gitapi.IssueUpdate{Title: "Issue", State: "closed"})
	if e != nil || v.State != "closed" || posts.Load() != 1 || patches.Load() != 1 || gets.Load() != 1 {
		t.Fatal(v, e)
	}
	if gitapi.SupportsAssignees(c) {
		t.Fatal("capability")
	}
}
func TestWriteUnknownAndReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want error
	}{{"401", 401, "", gitapi.ErrTokenRevoked}, {"403", 403, "", ErrForbidden}, {"404", 404, "", gitapi.ErrNotFound}, {"500", 500, "", gitapi.ErrUnknownResult}, {"bad_json", 200, "{}", gitapi.ErrUnknownResult}, {"unexpected_success", 201, "{}", gitapi.ErrUnknownResult}, {"unexpected_no_content", 204, "", gitapi.ErrUnknownResult}} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			})
			_, e := c.CreateIssue(t.Context(), "fixture-token", "team", "repo", "Issue", "body", nil)
			if !errors.Is(e, tc.want) || n.Load() != 1 {
				t.Fatal(e, n.Load())
			}
		})
	}
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "36000")
		w.WriteHeader(429)
	})
	_, e := c.Principal(t.Context(), "fixture-token")
	var limited *gitapi.ErrRateLimited
	if !errors.As(e, &limited) || time.Until(limited.ResetAt) > time.Hour {
		t.Fatal(e)
	}
	c = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://untrusted.invalid")
		w.WriteHeader(302)
	})
	_, e = c.Principal(t.Context(), "fixture-token")
	if !errors.Is(e, gitapi.ErrRequestFailed) {
		t.Fatal(e)
	}
}
func TestVisibilityNumberAndDisplayURL(t *testing.T) {
	var opaque CommentID
	if e := json.Unmarshal([]byte(`"97219c08d421e55cfa841deca16a30f5d7269e10"`), &opaque); e != nil || opaque == "" {
		t.Fatal(opaque, e)
	}
	zero, one, two := 0, 1, 2
	for _, tc := range []struct {
		a, b, i *int
		want    string
	}{{&one, &zero, &zero, "private"}, {&zero, &one, &zero, "public"}, {nil, &one, &zero, ""}, {&one, &one, &zero, ""}, {&zero, &zero, &one, ""}, {&two, &zero, &zero, ""}} {
		v, e := (Repository{Private: tc.a, Public: tc.b, Internal: tc.i}).Visibility()
		if v != tc.want || (tc.want == "") != (e != nil) {
			t.Fatal(v, e)
		}
	}
	for _, raw := range []string{`1`, `"7"`} {
		var v NumberID
		if json.Unmarshal([]byte(raw), &v) != nil {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{`0`, `-1`, `1.1`, `1e2`, `"01"`, `"+1"`, `null`} {
		var v NumberID
		if json.Unmarshal([]byte(raw), &v) == nil {
			t.Fatal(raw)
		}
	}
	if _, e := NumberID("2147483648").Number(); e == nil {
		t.Fatal("overflow")
	}
	for _, host := range []string{"atomgit.com", "gitcode.com", "api.atomgit.com", "api.gitcode.com"} {
		if !WebURL("https://"+host+"/team/repo/issues/7", "team/repo", "/issues/7") {
			t.Fatal(host)
		}
	}
	for _, raw := range []string{"https://atomgit.com.evil.invalid/team/repo/issues/7", "https://x@atomgit.com/team/repo/issues/7", "https://atomgit.com:443/team/repo/issues/7", "https://atomgit.com/team/other/issues/7", "https://atomgit.com/team/repo/issues/8", "http://atomgit.com/team/repo/issues/7", "https://atomgit.com/team/repo/issues/7?token=x"} {
		if WebURL(raw, "team/repo", "/issues/7") {
			t.Fatal(raw)
		}
	}
}
func TestHookContractAndTwoRequestCredentialLimit(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		var b map[string]any
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			t.Error("json")
		}
		if b["encryption_type"] != float64(0) || b["password"] != "fixture-secret" || b["tag_push_events"] != false {
			t.Error(b)
		}
		w.Write([]byte(`{"id":1,"project_id":9,"url":"https://openfox.invalid/hooks/x","active":true,"push_events":true,"issues_events":true,"note_events":true,"merge_requests_events":true,"tag_push_events":false}`))
	})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			h, e := c.PutHook(context.Background(), "fixture-token", "team", "repo", 0, "https://openfox.invalid/hooks/x", "fixture-secret")
			if e == nil && !h.Matches(9, "https://openfox.invalid/hooks/x") {
				e = ErrInvalid
			}
			results <- e
		}()
	}
	<-started
	<-started
	_, e := c.Principal(t.Context(), "fixture-token")
	var limited *gitapi.ErrRateLimited
	if !errors.As(e, &limited) {
		t.Fatal(e)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
}

func TestListRequiresJSONArray(t *testing.T) {
	for _, body := range []string{"null", "{}", `{"items":[]}`, "[]"} {
		t.Run(body, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
			calls := map[string]func() error{
				"organizations": func() error { _, e := c.Organizations(t.Context(), "fixture-token"); return e },
				"repositories":  func() error { _, _, e := c.Repositories(t.Context(), "fixture-token", "", 1); return e },
				"issues": func() error {
					_, _, e := c.Issues(t.Context(), "fixture-token", "team", "repo", 1, IssueWindow{})
					return e
				},
				"comments": func() error {
					_, _, e := c.Comments(t.Context(), "fixture-token", "team", "repo", "issues", "7", 1)
					return e
				},
				"hooks": func() error { _, _, e := c.Hooks(t.Context(), "fixture-token", "team", "repo", 1); return e },
			}
			for name, call := range calls {
				t.Run(name, func(t *testing.T) {
					e := call()
					if (e == nil) != (body == "[]") {
						t.Fatalf("body %s: %v", body, e)
					}
				})
			}
		})
	}
}
