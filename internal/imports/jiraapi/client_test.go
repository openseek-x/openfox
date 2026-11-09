package jiraapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openfox/internal/platform/safehttp"
)

func newTestClient(t *testing.T, deployment string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	auth := Auth{Kind: AuthAPIToken, Email: "a@b.c", Secret: "tok"}
	if deployment == DeploymentServer {
		auth = Auth{Kind: AuthPAT, Secret: "tok"}
	}
	c, err := New(Config{Deployment: deployment, SiteURL: srv.URL + "/jira/", Auth: auth},
		safehttp.Client(safehttp.Options{RootCAs: pool, AllowAddr: func(net.IP) bool { return true }}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wantReason(t *testing.T, err error, reason string) *Error {
	t.Helper()
	var e *Error
	if err == nil {
		t.Fatalf("期望 %s，实际无错误", reason)
	}
	if got := ReasonOf(err); got != reason {
		t.Fatalf("reason = %q, want %q (%v)", got, reason, err)
	}
	e, _ = err.(*Error)
	return e
}

func TestErrorMapping(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, DeploymentCloud, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jira/rest/api/2/myself":
			switch r.Header.Get("X-Case") {
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/jira/rest/api/2/project/NOPE":
			w.WriteHeader(http.StatusNotFound)
		case "/jira/rest/api/2/search/jql":
			if strings.Contains(r.URL.Query().Get("jql"), "bad") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errorMessages":["Error in the JQL Query: bad"],"errors":{}}`))
				return
			}
			w.Header().Set("Retry-After", "9999")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	_, err := c.Myself(ctx)
	if e := wantReason(t, err, ReasonAuthFailed); e.Status != 401 {
		t.Fatalf("status = %d", e.Status)
	}
	_, err = c.Project(ctx, "NOPE")
	wantReason(t, err, ReasonProjectNotFound)
	_, err = c.Search(ctx, "bad query", Cursor{}, nil, 1)
	wantReason(t, err, ReasonJQLInvalid)
	_, err = c.Search(ctx, "project = A", Cursor{}, nil, 1)
	if e := wantReason(t, err, ReasonRateLimited); e.RetryAfter != MaxRetryAfter {
		t.Fatalf("retry_after = %d", e.RetryAfter)
	}
}

func TestMyselfSiteNotFound(t *testing.T) {
	notFound := newTestClient(t, DeploymentCloud, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	_, err := notFound.Myself(context.Background())
	wantReason(t, err, ReasonSiteNotFound)
	html := newTestClient(t, DeploymentCloud, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) })
	_, err = html.Myself(context.Background())
	wantReason(t, err, ReasonSiteNotFound)
}

func TestTransportErrors(t *testing.T) {
	// 默认严格配置：回环地址被拦截。
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	auth := Auth{Kind: AuthAPIToken, Email: "a@b.c", Secret: "tok"}
	c, _ := New(Config{Deployment: DeploymentCloud, SiteURL: srv.URL, Auth: auth}, safehttp.Client(safehttp.Options{RootCAs: pool}))
	_, err := c.Myself(context.Background())
	wantReason(t, err, ReasonBlockedAddress)
	// 证书不被信任。
	c, _ = New(Config{Deployment: DeploymentCloud, SiteURL: srv.URL, Auth: auth},
		safehttp.Client(safehttp.Options{AllowAddr: func(net.IP) bool { return true }}))
	_, err = c.Myself(context.Background())
	wantReason(t, err, ReasonTLSFailed)
	// DNS 失败。
	c, _ = New(Config{Deployment: DeploymentCloud, SiteURL: "https://nope.example.com", Auth: auth},
		safehttp.Client(safehttp.Options{LookupIP: func(_ context.Context, h string) ([]net.IP, error) {
			return nil, &net.DNSError{Err: "no such host", Name: h, IsNotFound: true}
		}}))
	_, err = c.Myself(context.Background())
	wantReason(t, err, ReasonDNSFailed)
	// 连接被拒绝。
	closed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := closed.URL
	closed.Close()
	c, _ = New(Config{Deployment: DeploymentCloud, SiteURL: url, Auth: auth},
		safehttp.Client(safehttp.Options{AllowAddr: func(net.IP) bool { return true }}))
	_, err = c.Myself(context.Background())
	wantReason(t, err, ReasonUnreachable)
	// 非 https 与带用户信息。
	for _, raw := range []string{"http://jira.example.com", "https://u:p@jira.example.com", "https://jira.example.com?x=1"} {
		_, err := New(Config{Deployment: DeploymentCloud, SiteURL: raw, Auth: auth}, nil)
		wantReason(t, err, ReasonInvalidURL)
	}
}

func TestErrorHasNoCredentials(t *testing.T) {
	c := newTestClient(t, DeploymentCloud, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	_, err := c.Myself(context.Background())
	if strings.Contains(err.Error(), "tok") || strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("错误信息含凭据: %v", err)
	}
}

func TestAuthHeaders(t *testing.T) {
	var got string
	c := newTestClient(t, DeploymentServer, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"name":"admin"}`))
	})
	if _, err := c.Myself(context.Background()); err != nil || got != "Bearer tok" {
		t.Fatalf("auth = %q err = %v", got, err)
	}
}

func TestSearchPaging(t *testing.T) {
	cloud := newTestClient(t, DeploymentCloud, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("nextPageToken") == "" {
			_, _ = w.Write([]byte(`{"issues":[{"id":"1","key":"A-1","fields":{}}],"nextPageToken":"t2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"issues":[{"id":"2","key":"A-2","fields":{}}]}`))
	})
	page, err := cloud.Search(context.Background(), "project = A", Cursor{}, nil, 0)
	if err != nil || page.Done || page.Next.Token != "t2" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	page, err = cloud.Search(context.Background(), "project = A", page.Next, nil, 0)
	if err != nil || !page.Done || len(page.Issues) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	server := newTestClient(t, DeploymentServer, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"startAt":%s,"total":3,"issues":[{"id":"1","key":"A-1"},{"id":"2","key":"A-2"}]}`, r.URL.Query().Get("startAt"))
	})
	page, _ = server.Search(context.Background(), "x", Cursor{}, nil, 0)
	if page.Done || page.Next.StartAt != 2 || page.Total != 3 {
		t.Fatalf("page=%+v", page)
	}
	page, _ = server.Search(context.Background(), "x", page.Next, nil, 0)
	if !page.Done {
		t.Fatalf("page=%+v", page)
	}
	if n, err := server.Count(context.Background(), "x"); err != nil || n != 3 {
		t.Fatalf("count=%d err=%v", n, err)
	}
}

func TestCountFallback(t *testing.T) {
	c := newTestClient(t, DeploymentCloud, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		issues := make([]string, 100)
		for i := range issues {
			issues[i] = `{"id":"1","key":"A-1"}`
		}
		_, _ = fmt.Fprintf(w, `{"issues":[%s],"nextPageToken":"more"}`, strings.Join(issues, ","))
	})
	n, err := c.Count(context.Background(), "project = A")
	if err != nil || n != CountCap {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestParseSprints(t *testing.T) {
	modern := ParseSprints(json.RawMessage(`[{"id":7,"name":"S7","state":"closed","startDate":"2026-01-01T00:00:00.000Z","endDate":"2026-01-14T00:00:00.000Z","completeDate":"2026-01-14T01:00:00.000Z"}]`))
	if len(modern) != 1 || modern[0].ID != "7" || modern[0].State != "CLOSED" || modern[0].Complete == "" {
		t.Fatalf("modern = %+v", modern)
	}
	legacy := ParseSprints(json.RawMessage(`["com.atlassian.greenhopper.service.sprint.Sprint@1a2b[id=1,rapidViewId=2,state=ACTIVE,name=Sprint 1, beta,startDate=2026-02-01T00:00:00.000+08:00,endDate=2026-02-14T00:00:00.000+08:00,completeDate=<null>,sequence=1]"]`))
	if len(legacy) != 1 {
		t.Fatalf("legacy = %+v", legacy)
	}
	s := legacy[0]
	if s.ID != "1" || s.State != "ACTIVE" || s.Name != "Sprint 1, beta" || s.Start == "" || s.Complete != "" {
		t.Fatalf("legacy = %+v", s)
	}
	if ParseSprints(nil) != nil || ParseSprints(json.RawMessage(`null`)) != nil || ParseSprints(json.RawMessage(`["junk"]`)) != nil {
		t.Fatal("空与无效输入应返回 nil")
	}
}

func TestDiscoverFields(t *testing.T) {
	var fields []Field
	_ = json.Unmarshal([]byte(`[
		{"id":"customfield_10020","name":"Sprint","custom":true,"schema":{"type":"array","custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
		{"id":"customfield_10016","name":"Story point estimate","custom":true,"schema":{"type":"number"}},
		{"id":"customfield_10015","name":"Start date","custom":true,"schema":{"type":"date"}}]`), &fields)
	ids := DiscoverFields(fields)
	if ids.Sprint != "customfield_10020" || ids.Points != "customfield_10016" || ids.Start != "customfield_10015" || len(ids.Ids()) != 3 {
		t.Fatalf("ids = %+v", ids)
	}
}

func TestNormalizeSiteURL(t *testing.T) {
	got, err := NormalizeSiteURL(" https://Jira.example.com/jira/ ")
	if err != nil || got != "https://Jira.example.com/jira" {
		t.Fatalf("got %q err %v", got, err)
	}
	for _, bad := range []string{"http://jira.example.com", "https://jira.example.com/?a=1", "", "https://u:p@jira.example.com"} {
		if _, err := NormalizeSiteURL(bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}

func TestWorkflowTransitionsRequireUnambiguousAvailableTarget(t *testing.T) {
	for _, mode := range []string{"ok", "missing", "ambiguous", "required"} {
		t.Run(mode, func(t *testing.T) {
			posts := 0
			c := newTestClient(t, DeploymentServer, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/jira/rest/api/2/issue/12/transitions" {
					t.Fatal(r.URL.Path)
				}
				if r.Method == "GET" {
					if r.URL.Query().Get("expand") != "transitions.fields" {
						t.Error("missing workflow metadata")
					}
					tr := []any{}
					item := map[string]any{"id": "31", "to": map[string]string{"name": "Done"}}
					if mode == "required" {
						item["fields"] = map[string]any{"resolution": map[string]bool{"required": true}}
					}
					if mode != "missing" {
						tr = append(tr, item)
					}
					if mode == "ambiguous" {
						tr = append(tr, item)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"transitions": tr})
					return
				}
				posts++
				var body struct{ Transition struct{ ID string } }
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Transition.ID != "31" {
					t.Error("wrong transition")
				}
				w.WriteHeader(204)
			})
			e := c.UpdateStatus(context.Background(), "12", "done")
			if (e == nil) != (mode == "ok") || posts != map[bool]int{true: 1, false: 0}[mode == "ok"] {
				t.Fatalf("err=%v posts=%d", e, posts)
			}
		})
	}
}
