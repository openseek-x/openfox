package gongfeng

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/gitapi"
)

func fixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "git.tencent.com"}, DNSNames: []string{"git.tencent.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	c := New()
	transport := c.HTTP.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "git.tencent.com:443" {
			t.Errorf("unexpected outgoing origin %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	c.HTTP.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	return c
}

func TestPATOriginTLSAndRedirect(t *testing.T) {
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "git.tencent.com" || r.URL.Path != "/api/v3/user" || r.Header.Get("PRIVATE-TOKEN") != "fixture-pat" || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request identity")
		}
		if calls.Load() == 1 {
			json.NewEncoder(w).Encode(map[string]any{"id": 71, "username": "stable-user", "state": "active", "email": "must-not-retain@example.com"})
			return
		}
		w.Header().Set("Location", "https://other.example/capture")
		w.WriteHeader(302)
	})
	user, err := c.User(context.Background(), "fixture-pat")
	if err != nil || user.ID != 71 || user.Login != "stable-user" {
		t.Fatalf("user=%+v error=%v", user, err)
	}
	if _, err = c.Identity(context.Background(), "fixture-pat"); !errors.Is(err, gitapi.ErrRequestFailed) {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("redirect followed: %d", calls.Load())
	}
	for _, token := range []string{"", "white space", "newline\ncanary", strings.Repeat("x", 8193)} {
		if _, err = c.Identity(context.Background(), token); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid token sent")
	}
	transport := c.HTTP.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	c.HTTP.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	if _, err = c.Identity(context.Background(), "fixture-pat"); !errors.Is(err, gitapi.ErrRequestFailed) {
		t.Fatal("untrusted TLS certificate accepted", err)
	}
	if calls.Load() != 2 {
		t.Fatal("untrusted TLS reached handler")
	}
}

func TestHTTPFailuresAndUnknownMutation(t *testing.T) {
	status := http.StatusOK
	body := `{"id":71,"username":"u","state":"active"}`
	retry := ""
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", retry)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	for _, test := range []struct {
		status int
		want   error
	}{{401, gitapi.ErrTokenRevoked}, {423, gitapi.ErrTokenRevoked}, {403, ErrForbidden}, {404, gitapi.ErrNotFound}, {500, gitapi.ErrRequestFailed}} {
		status = test.status
		_, err := c.Identity(context.Background(), "p")
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
	status = 429
	for _, value := range []string{"60", time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), "999999", "garbage"} {
		retry = value
		_, err := c.Identity(context.Background(), "p")
		var limited *gitapi.ErrRateLimited
		if !errors.As(err, &limited) {
			t.Fatal(err)
		}
		if value == "garbage" {
			if !limited.ResetAt.IsZero() {
				t.Fatal("guessed retry")
			}
		} else if limited.ResetAt.Before(time.Now()) || limited.ResetAt.After(time.Now().Add(time.Hour+time.Second)) {
			t.Fatal(limited.ResetAt)
		}
	}
	for _, test := range []struct {
		status int
		body   string
	}{{500, "failure"}, {201, "{"}, {201, strings.Repeat("x", maxResponse+1)}} {
		status, body = test.status, test.body
		_, err := c.Create(context.Background(), "p", 8, "title", "body")
		if !errors.Is(err, gitapi.ErrUnknownResult) {
			t.Fatalf("status=%d err=%v", status, err)
		}
	}
	status, body = 200, strings.Repeat("x", maxResponse+1)
	if _, err := c.Identity(context.Background(), "p"); !errors.Is(err, gitapi.ErrRequestFailed) {
		t.Fatal(err)
	}
}

func TestPaginationIdentityAndMembership(t *testing.T) {
	mode := "members"
	var pages []int
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("unbounded page")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		w.Header().Set("X-Page", strconv.Itoa(page))
		w.Header().Set("X-Per-Page", "100")
		if mode == "members" {
			if r.URL.Path != "/api/v3/projects/81/members/all" || r.URL.Query().Get("query") != "needle" {
				t.Error("incorrect membership request")
			}
			if page == 1 {
				w.Header().Set("X-Next-Page", "2")
				_, _ = w.Write([]byte(`[{"id":72,"username":"needle-neighbor","state":"active","access_level":50,"expires_at":null}]`))
			} else {
				w.Header().Set("X-Next-Page", "")
				_, _ = w.Write([]byte(`[{"id":71,"username":"needle","state":"active","access_level":40,"expires_at":null}]`))
			}
			return
		}
		if mode == "backward" {
			w.Header().Set("X-Next-Page", "1")
		}
		if mode == "wrongpage" {
			w.Header().Set("X-Page", "8")
		}
		_, _ = w.Write([]byte(`[{"id":1}]`))
	})
	yes, err := c.CanManageHooks(context.Background(), "p", 81, User{ID: 71, Username: "needle", State: "active"}, time.Now())
	if err != nil || !yes || len(pages) != 2 {
		t.Fatalf("yes=%t pages=%v error=%v", yes, pages, err)
	}
	for _, state := range []string{"", "blocked"} {
		count := len(pages)
		yes, err := c.CanManageHooks(context.Background(), "p", 81, User{ID: 71, Username: "needle", State: state}, time.Now())
		if err == nil || yes || len(pages) != count {
			t.Fatal("unverified identity accepted", state, err)
		}
	}
	for _, name := range []string{"backward", "wrongpage"} {
		mode = name
		if _, err := c.Projects(context.Background(), "p", "", 1); !errors.Is(err, ErrIncomplete) {
			t.Fatal(name, err)
		}
	}
	for _, m := range []Member{{User: User{State: "blocked"}, AccessLevel: 50}, {User: User{State: "active"}, AccessLevel: 30}, {User: User{State: "active"}, AccessLevel: 40, ExpiresAt: ptr("2000-01-01T00:00:00+0000")}} {
		if activeMembership(m, time.Now()) {
			t.Fatal("invalid membership accepted")
		}
	}
}
func ptr[T any](value T) *T { return &value }

func issueFixture() map[string]any {
	return map[string]any{"id": 901, "iid": 7, "project_id": 81, "title": "title", "description": "body", "state": "opened", "resolve_state": "rejected", "confidential": false, "created_at": "2026-11-01T01:02:03+0800", "updated_at": "2026-11-02T01:02:03+0800", "author": map[string]any{"id": 71, "username": "author"}, "assignees": []any{map[string]any{"id": 72, "username": "assigned"}}}
}
func TestIssueGlobalIDAndRestrictedWrites(t *testing.T) {
	var requested []string
	mode := "issue"
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.Path)
		if mode == "notes" {
			if !strings.Contains(r.URL.Path, "/issues/901/notes") {
				t.Error("iid used for note API")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":44,"body":"note","author":{"id":71},"system":false,"created_at":"2026-11-02T00:00:00+0000"}`))
			return
		}
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid JSON")
			}
			for _, key := range []string{"assignees", "assignee_ids", "resolve_state", "priority", "labels"} {
				if _, exists := body[key]; exists {
					t.Error("unsupported field sent", key)
				}
			}
			if r.Method == http.MethodPut && body["state_event"] != "close" {
				t.Error("incorrect close semantics")
			}
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		json.NewEncoder(w).Encode(issueFixture())
	})
	item, err := c.Issue(context.Background(), "p", 81, 901)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := item.PublicIssue(Project{ID: 81, Path: "team/sub/repo"})
	if err != nil || converted.ID != 901 || converted.Number != 7 || converted.APIRef() != "901" || !strings.HasSuffix(converted.HTMLURL, "/issues/7") {
		t.Fatal(converted, err)
	}
	if _, err = c.Update(context.Background(), "p", 81, 901, gitapi.IssueUpdate{Title: "title", Body: "body", State: "closed", Assignees: []string{"must-not-send"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Create(context.Background(), "p", 81, "title", "body"); err != nil {
		t.Fatal(err)
	}
	mode = "notes"
	if _, err = c.Comment(context.Background(), "p", 81, 901, false, "note"); err != nil {
		t.Fatal(err)
	}
	item.Confidential = ptr(true)
	if _, err = item.PublicIssue(Project{ID: 81, Path: "team/repo"}); !errors.Is(err, ErrConfidential) {
		t.Fatal(err)
	}
	if len(requested) != 4 {
		t.Fatal(requested)
	}
	if _, err = c.Issues(context.Background(), "p", 81, IssueQuery{State: "all"}, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if len(requested) != 4 {
		t.Fatal("unverified state=all sent")
	}
}

func TestHookCreateActivationContract(t *testing.T) {
	var calls []string
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/activate") {
			_, _ = w.Write([]byte("false"))
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad hook body")
		}
		if body["token"] != "hook-secret" || body["git_pre_receive_events"] != false || body["tag_push_events"] != false || body["note_events"] != true {
			t.Error("incorrect hook grants")
		}
		body["id"], body["project_id"], body["active"] = 17, 81, false
		delete(body, "token")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(body)
	})
	hook, err := c.SaveHook(context.Background(), "p", 81, 0, "https://openfox.example/api/v1/hooks/git/gongfeng/token", "hook-secret")
	if err != nil || hook.ID != 17 || *hook.Active {
		t.Fatal(hook, err)
	}
	active, err := c.ActivateHook(context.Background(), "p", 81, 17)
	if err != nil || active {
		t.Fatal(active, err)
	}
	if len(calls) != 2 || calls[1] != "POST /api/v3/projects/81/hooks/17/activate" {
		t.Fatal(calls)
	}
	for _, test := range []struct {
		level          int
		public         bool
		private, known bool
	}{{0, false, true, true}, {10, false, false, true}, {20, true, false, true}, {0, true, false, false}, {30, false, false, false}} {
		private, known := (Project{Visibility: ptr(test.level), Public: ptr(test.public)}).Privacy()
		if private != test.private || known != test.known {
			t.Fatal(test, private, known)
		}
	}
}

func TestListNullAndMutationUnexpectedSuccess(t *testing.T) {
	status, body := 200, "null"
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); _, _ = w.Write([]byte(body)) })
	if _, err := c.Projects(context.Background(), "p", "", 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("null was treated as complete empty list", err)
	}
	for _, code := range []int{200, 202, 204, 206} {
		status = code
		raw, _ := json.Marshal(issueFixture())
		body = string(raw)
		if _, err := c.Create(context.Background(), "p", 81, "title", "body"); !errors.Is(err, gitapi.ErrUnknownResult) {
			t.Fatal("unexpected POST success must reconcile", code, err)
		}
	}
}

func TestPaginationContradictionAndLimits(t *testing.T) {
	mode := "total-conflict"
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "total-conflict":
			w.Header().Set("X-Total-Pages", "2")
			w.Header().Set("X-Next-Page", "")
			_, _ = w.Write([]byte(`[{"id":1}]`))
		case "empty-next":
			w.Header().Set("X-Next-Page", "2")
			_, _ = w.Write([]byte(`[]`))
		case "cap", "total-full", "pages-full", "both-full":
			if mode == "total-full" || mode == "both-full" {
				w.Header().Set("X-Total", "100")
			}
			if mode == "pages-full" || mode == "both-full" {
				w.Header().Set("X-Total-Pages", "1")
			}
			rows := make([]map[string]int, 100)
			for i := range rows {
				rows[i] = map[string]int{"id": i + 1}
			}
			json.NewEncoder(w).Encode(rows)
		}
	})
	for _, test := range []struct {
		mode string
		page int
	}{{"total-conflict", 1}, {"empty-next", 1}, {"cap", 50}} {
		mode = test.mode
		if _, err := c.Projects(context.Background(), "p", "", test.page); !errors.Is(err, ErrIncomplete) {
			t.Fatal(test, err)
		}
	}
	for _, name := range []string{"total-full", "pages-full", "both-full"} {
		mode = name
		page, err := c.Projects(context.Background(), "p", "", 1)
		if err != nil || page.Next != 0 || len(page.Items) != 100 {
			t.Fatal(name, page.Next, err)
		}
	}
}

func TestMRAndCommitPaths(t *testing.T) {
	from, to := strings.Repeat("a", 40), strings.Repeat("b", 40)
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/projects/81/merge_request/902":
			_, _ = w.Write([]byte(`{"id":902,"iid":8,"project_id":81,"target_project_id":81,"source_project_id":82,"state":"reopened","work_in_progress":true,"updated_at":"2026-11-02T00:00:00+0000"}`))
		case "/api/v3/projects/81/merge_requests":
			if r.URL.Query().Get("updated_after") == "" || r.URL.Query().Get("updated_before") == "" {
				t.Error("missing reconciliation window")
			}
			_, _ = w.Write([]byte(`[]`))
		case "/api/v3/projects/81/repository/compare/commits":
			if r.URL.Query().Get("from") != from || r.URL.Query().Get("to") != to || r.URL.Query().Has("first_parent") || r.URL.Query().Has("no_merges") {
				t.Error("comparison changed DAG semantics")
			}
			json.NewEncoder(w).Encode([]Commit{{ID: to, Message: "TASK-1"}})
		case "/api/v3/projects/81/merge_requests/902/notes":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":42,"body":"link","author":{"id":71},"system":false,"created_at":"2026-11-02T00:00:00+0000"}`))
		default:
			t.Error("unexpected API path", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	mr, err := c.MergeRequest(context.Background(), "p", 81, 902)
	if err != nil || mr.IID != 8 || mr.State != "reopened" || !*mr.Draft {
		t.Fatal(mr, err)
	}
	if _, err = c.Comment(context.Background(), "p", 81, 902, true, "link"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.MergeRequests(context.Background(), "p", 81, time.Now().Add(-time.Hour), time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	diff, err := c.CompareCommitPage(context.Background(), "p", 81, from, to, 1)
	if err != nil || len(diff.Items) != 1 || diff.Items[0].ID != to {
		t.Fatal(diff, err)
	}
	if _, err = c.CompareCommitPage(context.Background(), "p", 81, strings.Repeat("0", 40), to, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestCancellationAndTwoInFlight(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var calls atomic.Int32
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"id":71,"username":"u","state":"active"}`))
		case <-r.Context().Done():
		}
	})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := c.Identity(context.Background(), "p"); results <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not enter")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Identity(ctx, "p"); !errors.Is(err, gitapi.ErrRequestFailed) {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("third request escaped concurrency gate")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	disconnected, postEntered := make(chan struct{}), make(chan struct{})
	other := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(postEntered)
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-time.After(3 * time.Second):
			t.Error("cancelled request remained active")
		}
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	postResult := make(chan error, 1)
	go func() { _, err := other.Create(ctx2, "p", 81, "title", "body"); postResult <- err }()
	select {
	case <-postEntered:
	case <-time.After(time.Second):
		t.Fatal("POST did not enter")
	}
	cancel2()
	if err := <-postResult; !errors.Is(err, gitapi.ErrUnknownResult) {
		t.Fatal("sent POST cancellation was not UNKNOWN", err)
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("cancel did not close TLS request")
	}
}

func TestNumericEffectiveMemberAndImmutableScope(t *testing.T) {
	for _, tc := range []struct {
		name           string
		id             int64
		state, expires string
		want           bool
	}{{"active", 71, "active", "", true}, {"other numeric id", 72, "active", "", false}, {"blocked", 71, "blocked", "", false}, {"expired", 71, "active", "2020-01-01T00:00:00+0000", false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v3/projects/81/members/all/71" {
					t.Error(r.URL.Path)
				}
				var expiry any
				if tc.expires != "" {
					expiry = tc.expires
				}
				json.NewEncoder(w).Encode(map[string]any{"id": tc.id, "state": tc.state, "username": "renamed", "access_level": 40, "expires_at": expiry})
			})
			ok, e := c.CanManageProject(context.Background(), "pat", 81, User{ID: 71, Username: "old", State: "active"}, time.Now())
			if tc.want != (ok && e == nil) {
				t.Fatal(ok, e)
			}
			scoped, e := c.Scope(81, "owner/project")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = scoped.GetRepo(context.Background(), "pat", "other", "project"); !errors.Is(e, ErrInvalid) {
				t.Fatal("caller changed routing", e)
			}
			if _, e = scoped.CommentPullRequest(context.Background(), "pat", "owner", "project", 7, "body"); !errors.Is(e, ErrUnsupported) {
				t.Fatal("iid used as global MR", e)
			}
			if calls != 1 {
				t.Fatal("invalid scope reached upstream", calls)
			}
		})
	}
}
