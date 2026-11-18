package stage3

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/integrations"
	"openfox/internal/secrets"
)

type codeCommitHTTPUpstream struct {
	mu            sync.Mutex
	requests      int
	revoked       bool
	title         string
	created       float64
	comments      int
	commentTokens map[string]bool
	worker        *integrations.SyncWorker
}

func (f *codeCommitHTTPUpstream) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIA") || r.Header.Get("X-Amz-Security-Token") != "synthetic-session" {
		w.WriteHeader(400)
		return
	}
	if r.Host == "sts.us-east-1.amazonaws.com" {
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/fixture</Arn><UserId>fixture-user</UserId><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`)
		return
	}
	if r.Host != "codecommit.us-east-1.amazonaws.com" {
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if f.revoked {
		w.WriteHeader(400)
		io.WriteString(w, `{"__type":"ExpiredTokenException"}`)
		return
	}
	var raw map[string]json.RawMessage
	if json.NewDecoder(r.Body).Decode(&raw) != nil {
		w.WriteHeader(400)
		return
	}
	in := map[string]string{}
	for k, v := range raw {
		var text string
		_ = json.Unmarshal(v, &text)
		in[k] = text
	}
	switch r.Header.Get("X-Amz-Target") {
	case "CodeCommit_20150413.ListRepositories":
		if in["nextToken"] == "" {
			io.WriteString(w, `{"repositories":[{"repositoryId":"opaque-repo-1","repositoryName":"Repo"}],"nextToken":"second-page"}`)
		} else if in["nextToken"] == "second-page" {
			io.WriteString(w, `{"repositories":[{"repositoryId":"opaque-repo-2","repositoryName":"Other"}]}`)
		} else {
			w.WriteHeader(400)
		}
	case "CodeCommit_20150413.GetRepository":
		if in["repositoryName"] != "Repo" {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"repositoryMetadata":{"repositoryId":"opaque-repo-1","repositoryName":"Repo","accountId":"123456789012","Arn":"arn:aws:codecommit:us-east-1:123456789012:Repo","defaultBranch":"main"}}`)
	default:
		if !f.browserReply(w, r, in) {
			w.WriteHeader(400)
		}
	}
}
func newCodeCommitHTTPEnv(t *testing.T) (*s3Env, *codeCommitHTTPUpstream) {
	raw := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("exclusive I13 database not configured")
	}
	u, e := url.Parse(raw)
	if e != nil || !strings.HasPrefix(u.Path, "/i13_") {
		t.Fatal("exclusive I13 database required")
	}
	env := newS3Env(t)
	f := &codeCommitHTTPUpstream{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(upstream.Close)
	tr := upstream.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = upstream.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "sts.us-east-1.amazonaws.com:443" && address != "codecommit.us-east-1.amazonaws.com:443" {
			t.Errorf("unexpected destination %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, e := secrets.New(key)
	if e != nil {
		t.Fatal(e)
	}
	publicURL := "https://openfox.example.test"
	if raw := os.Getenv("OPENFOX_I13_BROWSER_PUBLIC_URL"); raw != "" {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
			t.Fatal("owned loopback browser origin required")
		}
		publicURL = raw
	}
	f.worker = &integrations.SyncWorker{Pool: env.database, Service: integrations.Service{Box: box, CodeCommitTransport: tr, PublicURL: publicURL}, Instance: "i13-browser-fixture"}
	env.server = httptest.NewServer(api.NewWithDispatch(env.auth, env.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: box, CodeCommitTransport: tr, PublicURL: publicURL}))
	t.Cleanup(env.server.Close)
	env.clients = map[string]*s3Client{}
	env.exec(`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id IN($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, env.org, env.otherOrg)
	return env, f
}
func codeCommitHTTPInput(version uint64) map[string]any {
	return map[string]any{"region": "us-east-1", "access_key_id": "AKIA1234567890123456", "secret_access_key": "synthetic-secret-value", "session_token": "synthetic-session", "version": version}
}
func TestCodeCommitAuthenticatedHTTPControl(t *testing.T) {
	e, f := newCodeCommitHTTPEnv(t)
	owner := e.owner()
	base := "integrations/codecommit"
	e.member().must(403, "POST", base+"/connections", codeCommitHTTPInput(0), nil)
	var regions struct {
		Regions []string `json:"regions"`
	}
	owner.must(200, "GET", base+"/regions", nil, &regions)
	if len(regions.Regions) != 25 {
		t.Fatal("regions", len(regions.Regions))
	}
	var conn integrations.Connection
	r := owner.must(201, "POST", base+"/connections", codeCommitHTTPInput(0), &conn)
	if r.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(r.Body), "synthetic-secret") {
		t.Fatal("credential response disclosure")
	}
	path := "integrations/connections/" + conn.ID + "/codecommit"
	denied := e.outsider().do("GET", path+"/configuration", nil)
	if denied.Status != 403 && denied.Status != 404 {
		t.Fatal("tenant scope", denied.Status)
	}
	var page struct {
		Repositories []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"repositories"`
		Cursor  string `json:"next_cursor"`
		Version uint64 `json:"connection_version"`
	}
	owner.must(200, "GET", path+"/repositories", nil, &page)
	if len(page.Repositories) != 1 || page.Cursor == "" || page.Cursor == "second-page" || page.Version != conn.Version {
		t.Fatal("first opaque page")
	}
	cursor := page.Cursor
	owner.must(200, "GET", path+"/repositories?cursor="+url.QueryEscape(cursor), nil, &page)
	if len(page.Repositories) != 1 || page.Repositories[0].Name != "Other" || page.Cursor != "" {
		t.Fatal("second page")
	}
	var bridge integrations.CodeCommitBridgeResult
	r = owner.must(201, "POST", path+"/repositories", map[string]any{"connection_version": conn.Version, "name": "Repo", "version": 0, "confirm_initialization": true}, &bridge)
	if len(bridge.Token) != 43 || bridge.Repository.InitializedAt != nil || !bridge.Repository.Enabled || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("bridge initialization")
	}
	var config integrations.CodeCommitConfiguration
	r = owner.must(200, "GET", path+"/configuration", nil, &config)
	if strings.Contains(string(r.Body), bridge.Token) || strings.Contains(string(r.Body), "synthetic-session") || len(config.Repositories) != 1 {
		t.Fatal("configuration disclosure")
	}
	event := map[string]any{"version": "0", "id": "controlled-event-1", "source": "aws.codecommit", "account": "123456789012", "region": "us-east-1", "time": time.Now().UTC().Format(time.RFC3339), "detail-type": "CodeCommit Pull Request State Change", "resources": []string{"arn:aws:codecommit:us-east-1:123456789012:Repo"}, "detail": map[string]any{"event": "pullRequestCreated", "repositoryNames": []string{"Repo"}, "pullRequestId": "7"}}
	hook := func(token string, want int, body any) integrations.CodeCommitReceipt {
		t.Helper()
		var out integrations.CodeCommitReceipt
		owner.must(want, "POST", "/v1/hooks/git/codecommit/"+bridge.Repository.BridgeID, body, &out, "Authorization", "Bearer "+token)
		return out
	}
	hook(strings.Repeat("x", 43), 401, event)
	if x := hook(bridge.Token, 202, event); !x.Accepted || x.Duplicate {
		t.Fatal("receipt")
	}
	if x := hook(bridge.Token, 202, event); !x.Duplicate {
		t.Fatal("duplicate")
	}
	event["time"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	hook(bridge.Token, 409, event)
	oldToken := bridge.Token
	owner.must(200, "POST", path+"/repositories/"+bridge.Repository.ID+"/bridge-token", map[string]any{"connection_version": conn.Version, "version": bridge.Repository.Version}, &bridge)
	hook(oldToken, 401, event)
	hook(bridge.Token, 202, event)
	owner.must(200, "PUT", path+"/credentials", codeCommitHTTPInput(conn.Version), &conn)
	owner.must(400, "GET", path+"/repositories?cursor="+url.QueryEscape(cursor), nil, nil)
	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	owner.must(422, "GET", path+"/repositories", nil, nil)
	owner.must(200, "GET", path+"/configuration", nil, &config)
	if config.Connection.Status != integrations.StatusNeedsAttention {
		t.Fatal("revoked credentials not marked")
	}
	owner.must(200, "POST", "integrations/connections/"+conn.ID+"/disconnect", map[string]any{"confirm_phrase": integrations.ConfirmPhrase("AWS CodeCommit"), "version": config.Connection.Version}, &conn)
	if conn.Status != integrations.StatusDisconnected {
		t.Fatal("disconnect status")
	}
	hook(bridge.Token, 404, event)
	if e.count(`SELECT count(*) FROM openfox.integration_codecommit_work WHERE organization_id=$1::uuid`, e.org) != 1 {
		t.Fatal("disconnect lost or duplicated work")
	}
	if e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND id=$2::uuid AND credentials_sealed IS NOT NULL`, e.org, conn.ID) != 0 {
		t.Fatal("disconnect retained credential")
	}
}
