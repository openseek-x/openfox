package feishuint

// Real HTTP/session/CSRF/PG, RS256 Connector JWT, Microsoft-shaped owned TLS and
// actual Run materialization/finalization. Model output is synthetic, never paid.
import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"openfox/internal/api"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/identity"
	"openfox/internal/integrations"
	"openfox/internal/integrations/teams"
	"openfox/internal/tenancy"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type teamsBrowserTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (v teamsBrowserTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || (r.URL.Host != "login.botframework.com" && r.URL.Host != "login.microsoftonline.com" && r.URL.Host != "smba.trafficmanager.net" && r.URL.Host != "graph.microsoft.com") {
		return nil, errors.New("fixture rejected non-Microsoft origin")
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Host = v.target.Host
	u.Scheme = v.target.Scheme
	c.URL = &u
	return v.inner.RoundTrip(c)
}

type teamsBrowserPost struct {
	Path            string `json:"path"`
	Body            string `json:"body"`
	AuthorizationOK bool   `json:"authorization_ok"`
}
type teamsBrowserPeer struct {
	mu               sync.Mutex
	key              *rsa.PrivateKey
	tenant, app, aad string
	posts            []teamsBrowserPost
	tokens, keys     int
	unknownNext      bool
}

func teamsBrowserJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (p *teamsBrowserPeer) jwt() string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "owned-browser"})
	c, _ := json.Marshal(map[string]any{"iss": "https://api.botframework.com", "aud": p.app, "exp": time.Now().Add(5 * time.Minute).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(), "serviceurl": "https://smba.trafficmanager.net/amer"})
	b := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(b))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	return b + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func (p *teamsBrowserPeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/.well-known/openidconfiguration":
		teamsBrowserJSON(w, map[string]any{"issuer": "https://api.botframework.com", "jwks_uri": "https://login.botframework.com/v1/.well-known/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	case r.URL.Path == "/v1/.well-known/keys":
		p.keys++
		teamsBrowserJSON(w, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "owned-browser", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()), "endorsements": []string{"msteams"}}}})
	case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
		if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("client_id") != p.app || r.Form.Get("client_secret") != "synthetic-teams-secret" || r.URL.Path != "/"+p.tenant+"/oauth2/v2.0/token" {
			w.WriteHeader(401)
			return
		}
		p.tokens++
		teamsBrowserJSON(w, map[string]any{"token_type": "Bearer", "access_token": "owned-connector-token", "expires_in": 3600})
	case strings.Contains(r.URL.Path, "/members/"):
		if r.Header.Get("Authorization") != "Bearer owned-connector-token" {
			w.WriteHeader(401)
			return
		}
		teamsBrowserJSON(w, teams.Member{ID: "29:owned-browser-person", AADObjectID: p.aad, TenantID: p.tenant, Name: "Controlled Teams person"})
	case strings.HasPrefix(r.URL.Path, "/v1.0/teams/"):
		teamsBrowserJSON(w, map[string]any{"id": "19:owned-channel;opaque", "membershipType": "standard", "layoutType": "post", "isArchived": false})
	case r.Method == "POST" && strings.Contains(r.URL.Path, "/activities/"):
		var in struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		ok := r.Header.Get("Authorization") == "Bearer owned-connector-token"
		if !ok {
			w.WriteHeader(401)
			return
		}
		p.posts = append(p.posts, teamsBrowserPost{r.URL.Path, in.Text, ok})
		if p.unknownNext {
			p.unknownNext = false
			c, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				_ = c.Close()
			}
			return
		}
		teamsBrowserJSON(w, map[string]string{"id": fmt.Sprintf("owned-reply/%d;opaque", len(p.posts))})
	default:
		w.WriteHeader(404)
	}
}
func teamsBrowserTx(b *botWorld, ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	selectOrg := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: b.org, ActorID: b.ownerID, AuthorizationVersion: 1}, nil
	})
	return b.pool.RunForOrganization(ctx, b.org, tenancy.SourceUserRequest, selectOrg, fn)
}
func TestTeamsBrowserHarness(t *testing.T) {
	file := os.Getenv("OPENFOX_I16_BROWSER_FILE")
	if file == "" {
		t.Skip("owned browser harness not requested")
	}
	dsn, e := url.Parse(os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if e != nil || !strings.HasPrefix(strings.TrimPrefix(dsn.Path, "/"), "i16_") {
		t.Fatal("exclusive i16_ database required")
	}
	web := os.Getenv("OPENFOX_I16_WEB_URL")
	u, e := url.Parse(web)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("owned loopback frontend required")
	}
	b := &botWorld{world: setup(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	b.ctx = ctx
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	peer := &teamsBrowserPeer{key: key, tenant: b.newUUID(), app: b.newUUID(), aad: b.newUUID(), posts: []teamsBrowserPost{}}
	tls := httptest.NewTLSServer(peer)
	defer tls.Close()
	target, _ := url.Parse(tls.URL)
	client := teams.NewClientWithHTTP(&http.Client{Transport: teamsBrowserTransport{target, tls.Client().Transport}})
	b.service = integrations.Service{Box: b.box, PublicURL: "https://openfox.example.test", Teams: client}
	auth := identity.Service{Store: identity.PostgresStore{Pool: b.pool}, PasswordHasher: b.hasher, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now}
	server := httptest.NewServer(mountAPI(api.NewWithDispatch(auth, b.pool, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{Secrets: b.box, PublicURL: b.service.PublicURL, TeamsClient: client})))
	defer server.Close()
	b.server = server
	b.owner = b.login(server.URL+"/api", "fs-owner-"+b.org[:8]+"@example.test", b.ownerID)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at,capabilities) VALUES($1::uuid,$2,4,$3::uuid,'Teams controlled computer',clock_timestamp(),'{"runtimes":{"codex":{"version":"controlled","login_status":"LOGGED_IN","runnable":true}}}'::jsonb)`, b.deviceID, "i16-browser-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	agentID := b.createAgent(b.owner, b.foxSpace, "Teams browser Agent", "SHARED")
	worker := &integrations.IMWorker{Pool: b.pool, Service: b.service, Instance: "i16-browser-" + b.org}

	var eventMu sync.Mutex
	serial := 0
	event := func(botID, kind, command string, bad bool) (int, map[string]any, error) {
		eventMu.Lock()
		defer eventMu.Unlock()
		serial++
		a := map[string]any{"type": "message", "id": fmt.Sprintf("opaque/%s;%d", kind, serial), "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "serviceUrl": "https://smba.trafficmanager.net/amer", "channelId": "msteams", "from": map[string]any{"id": "29:owned-browser-person", "aadObjectId": peer.aad}, "recipient": map[string]string{"id": "28:" + peer.app}, "conversation": map[string]string{"id": "19:owned-personal;different-from-sender", "conversationType": "personal", "tenantId": peer.tenant}, "channelData": map[string]any{"tenant": map[string]string{"id": peer.tenant}}, "text": "Controlled private request", "textFormat": "plain"}
		switch kind {
		case "bind":
			a["text"] = "<p>" + command + "</p>"
			a["textFormat"] = "xml"
		case "group":
			a["conversation"] = map[string]string{"id": "19:owned-group;opaque", "conversationType": "groupChat", "tenantId": peer.tenant}
			a["text"] = "<at>OpenFox</at> Controlled group request"
			a["textFormat"] = "xml"
			a["entities"] = []any{map[string]any{"type": "mention", "text": "<at>OpenFox</at>", "mentioned": map[string]string{"id": "28:" + peer.app}}}
		case "private":
		default:
			return 0, nil, errors.New("invalid owned event")
		}
		raw, _ := json.Marshal(a)
		request, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/hooks/teams/"+botID+"/activities", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		jwt := peer.jwt()
		if bad {
			jwt = "invalid"
		}
		request.Header.Set("Authorization", "Bearer "+jwt)
		response, e := (&http.Client{Timeout: 15 * time.Second}).Do(request)
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		var out map[string]any
		e = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&out)
		return response.StatusCode, out, e
	}
	finish := func() error {
		return teamsBrowserTx(b, ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, _, e := (dispatch.Store{}).MaterializeInputs(ctx, tx, b.org, 10)
			if e != nil {
				return e
			}
			rows, e := tx.Query(ctx, `SELECT DISTINCT r.id::text,r.space_id::text,r.conversation_id::text,r.agent_id::text,r.actor_id::text FROM openfox.runs r JOIN openfox.conversation_inputs ci ON ci.organization_id=r.organization_id AND ci.run_id=r.id JOIN openfox.teams_inputs ti ON ti.organization_id=ci.organization_id AND ti.message_id=ci.message_id WHERE r.organization_id=openfox_private.current_organization_id() AND r.state='QUEUED' ORDER BY 1 LIMIT 10`)
			if e != nil {
				return e
			}
			var runs []execution.Run
			for rows.Next() {
				var run execution.Run
				if e = rows.Scan(&run.ID, &run.SpaceID, &run.ConversationID, &run.AgentID, &run.ActorID); e != nil {
					rows.Close()
					return e
				}
				run.OrganizationID = b.org
				run.State = execution.StateSucceeded
				runs = append(runs, run)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, run := range runs {
				if _, e = tx.Exec(ctx, `UPDATE openfox.runs SET state='SUCCEEDED',finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, run.ID); e != nil {
					return e
				}
				if _, e = dispatch.FinalizeConversation(ctx, tx, run, dispatch.AuthorAgent, "Controlled browser result; no model was called.", false); e != nil {
					return e
				}
			}
			return nil
		})
	}

	state := func() map[string]any {
		values := map[string]any{}
		for key, query := range map[string]string{"identities": "SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='teams' AND scope='PERSONAL' AND status='ACTIVE'", "scopes": "SELECT count(*) FROM openfox.teams_scope_authorizations WHERE organization_id=$1::uuid AND enabled", "inputs": "SELECT count(*) FROM openfox.teams_inputs WHERE organization_id=$1::uuid", "replies_done": "SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND status='DONE'", "replies_unknown": "SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND last_error='delivery_unknown'"} {
			var n int
			b.scan(&n, query, b.org)
			values[key] = n
		}
		peer.mu.Lock()
		values["token_calls"], values["jwks_calls"], values["send_calls"] = peer.tokens, peer.keys, len(peer.posts)
		values["posts"] = append([]teamsBrowserPost{}, peer.posts...)
		peer.mu.Unlock()
		return values
	}
	var controlMu sync.Mutex
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlMu.Lock()
		defer controlMu.Unlock()
		if r.Method == "GET" && r.URL.Path == "/state" {
			teamsBrowserJSON(w, state())
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in struct {
			BotID   string `json:"bot_id"`
			Kind    string `json:"kind"`
			Code    string `json:"code"`
			Unknown bool   `json:"unknown"`
			Bad     bool   `json:"invalid_auth"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		var err error
		switch r.URL.Path {
		case "/pump":
			err = worker.RunOnce(ctx)
		case "/complete":
			err = worker.RunOnce(ctx)
			if err == nil {
				err = finish()
			}
			if err == nil {
				err = worker.RunOnce(ctx)
			}
		case "/event":
			status, out, e := event(in.BotID, in.Kind, in.Code, in.Bad)
			if e != nil {
				http.Error(w, "controlled event failed", 500)
				return
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(out)
			return
		case "/unknown-next":
			peer.mu.Lock()
			peer.unknownNext = in.Unknown
			peer.mu.Unlock()
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			t.Logf("controlled step failed: %T", err)
			http.Error(w, "controlled step failed", 500)
			return
		}
		teamsBrowserJSON(w, map[string]bool{"ok": true})
	}))
	defer control.Close()
	raw, _ := json.Marshal(map[string]string{"api_url": server.URL, "web_url": web, "fixture_url": control.URL, "organization_id": b.org, "space_id": b.foxSpace, "agent_id": agentID, "device_id": b.deviceID, "grant_id": b.grantID, "email": "fs-owner-" + b.org[:8] + "@example.test", "password": password, "secret": "synthetic-teams-secret", "tenant_id": peer.tenant, "app_id": peer.app})
	if e = os.WriteFile(file, raw, 0600); e != nil {
		t.Fatal(e)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("browser harness deadline")
		case <-ticker.C:
			if _, e = os.Stat(file + ".stop"); e == nil {
				controlMu.Lock()
				proof := state()
				controlMu.Unlock()
				proofRaw, _ := json.MarshalIndent(proof, "", "  ")
				if e = os.WriteFile(file+".proof.json", proofRaw, 0600); e != nil {
					t.Fatal(e)
				}
				return
			}
		}
	}
}
