package stage3

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/secrets"
)

const bbBrowserWorkspace = "22222222-2222-4222-8222-222222222222"
const bbBrowserRepo = "44444444-4444-4444-8444-444444444444"
const bbBrowserPublicRepo = "66666666-6666-4666-8666-666666666666"
const bbBrowserSubject = "11111111-1111-4111-8111-111111111111"
const bbBrowserPersonal = "33333333-3333-4333-8333-333333333333"
const bbBrowserNewSubject = "77777777-7777-4777-8777-777777777777"

type bbBrowserSource struct {
	mu                        sync.Mutex
	t                         *testing.T
	path, taskKey, outsideKey string
	now                       time.Time
	calls                     []map[string]any
	posts                     int
	comments                  []bitbucket.Comment
}

func (f *bbBrowserSource) exists(suffix string) bool {
	_, err := os.Stat(f.path + suffix)
	return err == nil
}
func (f *bbBrowserSource) workspace() bitbucket.Workspace {
	return bitbucket.Workspace{UUID: bbBrowserWorkspace, Slug: "controlled", Name: "Controlled Workspace"}
}
func (f *bbBrowserSource) repo(public bool) bitbucket.Repository {
	id, name := bbBrowserRepo, "private-repo"
	private := true
	if public {
		id, name, private = bbBrowserPublicRepo, "public-repo", false
	}
	return bitbucket.Repository{UUID: id, FullName: "controlled/" + name, Name: name, SCM: "git", Private: &private, Workspace: f.workspace()}
}
func (f *bbBrowserSource) comment(id int64, content string, subject string) bitbucket.Comment {
	c := bitbucket.Comment{ID: id, Content: bitbucket.RawText{Raw: &content}, User: bitbucket.User{UUID: subject, DisplayName: "Fixture Member"}, CreatedAt: f.now, UpdatedAt: f.now}
	c.PullRequest.ID = 7
	return c
}
func (f *bbBrowserSource) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hostOK := r.Host == "bitbucket.org" || r.Host == "api.bitbucket.org"
	f.calls = append(f.calls, map[string]any{"method": r.Method, "host": r.Host, "path": r.URL.Path, "page": r.URL.Query().Get("page"), "origin_allowed": hostOK})
	if !hostOK {
		f.t.Error("fixture rejected non-Bitbucket host")
		http.Error(w, "host", 403)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/site/oauth2/authorize" && r.Method == "GET" && r.Host == "bitbucket.org" {
		app := r.URL.Query().Get("client_id")
		if app != "org-consumer" && app != "personal-consumer" {
			http.Error(w, "consumer", 400)
			return
		}
		q := url.Values{"state": {r.URL.Query().Get("state")}, "code": {"fixture-code-" + app}}
		http.Redirect(w, r, "http://127.0.0.1:4203/api/v1/integrations/bitbucket/callback?"+q.Encode(), http.StatusFound)
		return
	}
	if r.URL.Path == "/site/oauth2/access_token" && r.Method == "POST" && r.Host == "bitbucket.org" {
		app, secret, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || secret != "fixture-secret" || (app != "org-consumer" && app != "personal-consumer") {
			f.t.Error("consumer mismatch")
			http.Error(w, "consumer", 400)
			return
		}
		scope, token := "account pullrequest", "fixture-org-access"
		if app == "personal-consumer" {
			scope, token = "account", "fixture-personal-access"
		}
		reply(map[string]any{"access_token": token, "refresh_token": "fixture-refresh", "token_type": "bearer", "scopes": scope, "expires_in": 3600})
		return
	}
	auth := r.Header.Get("Authorization")
	if r.Host != "api.bitbucket.org" || (auth != "Bearer fixture-org-access" && auth != "Bearer fixture-personal-access") {
		f.t.Error("API token/origin mismatch")
		http.Error(w, "token", 401)
		return
	}
	subject, name := bbBrowserSubject, "Fixture Sender"
	if f.exists(".sender-change") {
		subject, name = bbBrowserNewSubject, "New Fixture Sender"
	}
	if auth == "Bearer fixture-personal-access" {
		subject, name = bbBrowserPersonal, "Fixture Member"
	}
	switch {
	case r.URL.Path == "/2.0/user":
		reply(bitbucket.User{UUID: subject, DisplayName: name})
	case r.URL.Path == "/2.0/user/workspaces":
		reply(map[string]any{"values": []any{map[string]any{"administrator": true, "workspace": f.workspace()}}})
	case r.URL.Path == "/2.0/workspaces/{"+bbBrowserWorkspace+"}":
		reply(f.workspace())
	case r.URL.Path == "/2.0/repositories/{"+bbBrowserWorkspace+"}":
		if r.URL.Query().Get("page") == "2" {
			reply(map[string]any{"values": []any{f.repo(true)}})
		} else {
			reply(map[string]any{"values": []any{f.repo(false)}, "next": "https://api.bitbucket.org" + r.URL.EscapedPath() + "?page=2"})
		}
	case strings.HasSuffix(r.URL.Path, "/pullrequests/7/comments") && r.Method == "POST":
		var in struct {
			Content bitbucket.RawText `json:"content"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Content.Raw == nil {
			f.t.Error("missing backlink content")
			w.WriteHeader(400)
			return
		}
		if strings.Contains(*in.Content.Raw, f.outsideKey) {
			f.t.Error("unselected space leaked into backlink")
		}
		f.posts++
		if f.posts == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				f.t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		c := f.comment(int64(100+f.posts), *in.Content.Raw, subject)
		f.comments = append(f.comments, c)
		w.WriteHeader(201)
		reply(c)
	case strings.HasSuffix(r.URL.Path, "/pullrequests/7/comments"):
		out := append([]bitbucket.Comment{}, f.comments...)
		reply(map[string]any{"values": out})
	case strings.Contains(r.URL.Path, "/pullrequests/7/comments/"):
		id, _ := strconv.ParseInt(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], 10, 64)
		if id != 9 && id != 10 {
			w.WriteHeader(404)
			return
		}
		reply(f.comment(id, "source comment body must never be copied", bbBrowserPersonal))
	case strings.HasSuffix(r.URL.Path, "/pullrequests/7"):
		body := f.taskKey + " " + f.outsideKey
		p := bitbucket.PullRequest{ID: 7, Title: "Relates " + body, Description: &body, State: "OPEN", Author: bitbucket.User{UUID: bbBrowserPersonal, DisplayName: "Fixture Member"}, UpdatedAt: f.now}
		p.Destination.Repository.UUID = bbBrowserRepo
		p.Source.Branch.Name = f.taskKey
		reply(p)
	case r.URL.Path == "/2.0/repositories/{"+bbBrowserWorkspace+"}/{"+bbBrowserRepo+"}":
		reply(f.repo(false))
	case r.URL.Path == "/2.0/repositories/{"+bbBrowserWorkspace+"}/{"+bbBrowserPublicRepo+"}":
		reply(f.repo(true))
	default:
		f.t.Errorf("unexpected fixture request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

// Explicit opt-in: actual UI, OAuth HTTP callback, tenant PostgreSQL, signed
// Receive and SyncWorker. The only upstream is this private TLS server.
func TestBitbucketBrowserHarness(t *testing.T) {
	path := os.Getenv("OPENFOX_BITBUCKET_BROWSER_FILE")
	if path == "" {
		t.Skip("requires isolated Bitbucket browser fixture")
	}
	e := newS3Env(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	e.ctx = ctx
	e.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid)`, e.org, e.otherOrg)
	task := e.createTask(e.owner(), e.space.ID, map[string]any{"title": "Bitbucket browser task"})
	otherSpace := e.createSpace("private")
	outside := e.createTask(e.owner(), otherSpace.ID, map[string]any{"title": "Outside selected scope"})
	f := &bbBrowserSource{t: t, path: path, taskKey: task.DisplayID, outsideKey: outside.DisplayID, now: time.Now().UTC().Truncate(time.Second)}
	upstream := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	defer upstream.Close()
	tr := upstream.Client().Transport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = upstream.Certificate().DNSNames[0]
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "bitbucket.org:443" && address != "api.bitbucket.org:443" {
			return nil, fmt.Errorf("fixture denied origin")
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	key := make([]byte, secrets.KeySize)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	orgClient := &bitbucket.Client{App: bitbucket.App{ClientID: "org-consumer", ClientSecret: "fixture-secret"}, Transport: tr}
	personalClient := &bitbucket.Client{App: bitbucket.App{ClientID: "personal-consumer", ClientSecret: "fixture-secret"}, Transport: tr}
	svc := integrations.Service{Bitbucket: orgClient, BitbucketIdentity: personalClient, Box: box, PublicURL: "http://127.0.0.1:4203"}
	handler := api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, BitbucketClient: orgClient, BitbucketIdentityClient: personalClient, PublicURL: svc.PublicURL})
	server := httptest.NewUnstartedServer(http.StripPrefix("/api", handler))
	listener, err := net.Listen("tcp", "127.0.0.1:18153")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	missingPersonal := httptest.NewServer(api.NewWithDispatch(e.auth, e.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, BitbucketClient: orgClient, PublicURL: svc.PublicURL}))
	defer missingPersonal.Close()
	fixture := map[string]any{"organization_id": e.org, "other_organization_id": e.otherOrg, "space_id": e.space.ID, "space_name": e.space.Name, "outside_space_id": otherSpace.ID, "task_id": task.ID, "task_key": task.DisplayID, "outside_task_id": outside.ID, "owner_email": s3Email(s3OwnerID), "member_email": s3Email(s3MemberID), "member_id": s3MemberID, "password": s3Password, "upstream_address": upstream.Listener.Addr().String(), "workspace_uuid": bbBrowserWorkspace, "repository_uuid": bbBrowserRepo, "public_repository_uuid": bbBrowserPublicRepo}
	fixture["unconfigured_url"] = missingPersonal.URL
	raw, _ := json.Marshal(fixture)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	worker := &integrations.SyncWorker{Pool: e.database, Service: svc}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser deadline")
		case <-ticker.C:
			if f.exists(".stop") {
				f.mu.Lock()
				proof := map[string]any{"requests": f.calls, "comment_posts": f.posts, "accepted_comments": len(f.comments)}
				f.mu.Unlock()
				proof["selected_task_links"] = e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid AND provider='bitbucket'`, e.org, task.ID)
				proof["outside_task_links"] = e.count(`SELECT count(*) FROM openfox.task_git_links WHERE organization_id=$1::uuid AND task_id=$2::uuid AND provider='bitbucket'`, e.org, outside.ID)
				proof["personal_sealed_credentials"] = e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='bitbucket' AND scope='PERSONAL' AND credentials_sealed IS NOT NULL`, e.org)
				proof["verified_personal_uuid"] = e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='bitbucket' AND scope='PERSONAL' AND owner_user_id=$2::uuid AND external_id=$3`, e.org, s3MemberID, bbBrowserPersonal)
				proof["source_comment_activities"] = e.count(`SELECT count(*) FROM openfox.task_activities WHERE organization_id=$1::uuid AND task_id=$2::uuid AND kind='task.git_comment'`, e.org, task.ID)
				body, _ := json.MarshalIndent(proof, "", "  ")
				if err = os.WriteFile(path+".proof.json", body, 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err = worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
}
