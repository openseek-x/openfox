package teams

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testApp = "11111111-1111-4111-8111-111111111111"
const testTenant = "22222222-2222-4222-8222-222222222222"
const testAAD = "33333333-3333-4333-8333-333333333333"
const testGroup = "44444444-4444-4444-8444-444444444444"
const testService = "https://smba.trafficmanager.net/teams"

type tlsPeer struct {
	client      *Client
	key         *rsa.PrivateKey
	mu          sync.Mutex
	counts      map[string]int
	keyID       string
	endorsement bool
	keysFail    bool
	replyMode   string
	channelMode string
	retryHeader string
	lastPath    string
	lastBody    []byte
}

func newPeer(t *testing.T) *tlsPeer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &tlsPeer{key: key, counts: map[string]int{}, keyID: "current", endorsement: true}
	srv := httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, ErrInvalid
		}
		switch host {
		case "login.botframework.com", "login.microsoftonline.com", "graph.microsoft.com", "smba.trafficmanager.net":
		default:
			return nil, ErrInvalid
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	p.client = NewClientWithHTTP(&http.Client{Transport: tr})
	t.Cleanup(p.client.CloseIdleConnections)
	return p
}

func (p *tlsPeer) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts[r.URL.Path]++
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/.well-known/openidconfiguration":
		json.NewEncoder(w).Encode(map[string]any{"issuer": connectorIssuer, "jwks_uri": keysURL, "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/v1/.well-known/keys":
		if p.keysFail {
			w.WriteHeader(503)
			return
		}
		endorse := []string{"emulator"}
		if p.endorsement {
			endorse = []string{"msteams"}
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kid": p.keyID, "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()), "endorsements": endorse}}})
	case "/" + testTenant + "/oauth2/v2.0/token":
		if r.Host != "login.microsoftonline.com" || r.ParseForm() != nil || r.Form.Get("client_id") != testApp || r.Form.Get("client_secret") != "controlled-secret" {
			w.WriteHeader(401)
			return
		}
		scope := r.Form.Get("scope")
		token := "connector-token"
		if scope == GraphScope {
			token = "graph-token"
		} else if scope != ConnectorScope {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token_type": "Bearer", "access_token": token, "expires_in": 3600})
	default:
		if strings.HasPrefix(r.URL.Path, "/v1.0/teams/") {
			if r.Host != "graph.microsoft.com" || r.Header.Get("Authorization") != "Bearer graph-token" {
				w.WriteHeader(401)
				return
			}
			result := map[string]any{"id": "19:channel/@opaque", "membershipType": "standard", "layoutType": "post", "isArchived": false}
			switch p.channelMode {
			case "missing":
				delete(result, "layoutType")
			case "private":
				result["membershipType"] = "private"
			case "chat":
				result["layoutType"] = "chat"
			case "archived":
				result["isArchived"] = true
			case "permission":
				w.WriteHeader(403)
				return
			}
			json.NewEncoder(w).Encode(result)
			return
		}
		if r.Host != "smba.trafficmanager.net" || r.Header.Get("Authorization") != "Bearer connector-token" {
			w.WriteHeader(401)
			return
		}
		if strings.Contains(r.URL.Path, "/members/") {
			json.NewEncoder(w).Encode(Member{ID: "29:person/@opaque", AADObjectID: testAAD, TenantID: testTenant, Name: "Controlled member"})
			return
		}
		if strings.Contains(r.URL.Path, "/v3/teams/") {
			json.NewEncoder(w).Encode(Team{ID: "19:team/@opaque", AADGroupID: testGroup})
			return
		}
		if strings.Contains(r.URL.Path, "/activities/") {
			p.lastPath = r.URL.EscapedPath()
			p.lastBody, _ = io.ReadAll(r.Body)
			switch p.replyMode {
			case "unknown":
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			case "redirect":
				w.Header().Set("Location", "https://example.net/credential-trap")
				w.WriteHeader(307)
				return
			case "rate":
				header := p.retryHeader
				if header == "" {
					header = "7"
				}
				w.Header().Set("Retry-After", header)
				w.WriteHeader(429)
				return
			case "missing":
				io.WriteString(w, `{}`)
				return
			case "server":
				w.WriteHeader(503)
				return
			case "removed":
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"id": "opaque-reply-id"})
			return
		}
		w.WriteHeader(404)
	}
}

func (p *tlsPeer) jwt(t *testing.T, claims map[string]any, header map[string]any) string {
	t.Helper()
	if header == nil {
		header = map[string]any{"alg": "RS256", "kid": p.keyID, "typ": "JWT"}
	}
	h, _ := json.Marshal(header)
	b, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + input + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func claims(now time.Time) map[string]any {
	return map[string]any{"iss": connectorIssuer, "aud": testApp, "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(), "serviceurl": testService}
}

func TestTeamsJWTStrictClaimsAndServiceURL(t *testing.T) {
	p := newPeer(t)
	now := time.Now()
	p.client.now = func() time.Time { return now }
	if err := p.client.ValidateJWT(context.Background(), p.jwt(t, claims(now), nil), testApp, testService+"/"); err != nil {
		t.Fatal(err)
	}
	valid := p.jwt(t, claims(now), nil)
	parts := strings.Split(strings.TrimPrefix(valid, "Bearer "), ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	if err := p.client.ValidateJWT(context.Background(), "Bearer "+strings.Join(parts, "."), testApp, testService); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("bad signature accepted", err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"issuer", func(c map[string]any) { c["iss"] = "https://attacker.invalid" }},
		{"audience", func(c map[string]any) { c["aud"] = testTenant }},
		{"array_audience", func(c map[string]any) { c["aud"] = []string{testApp} }},
		{"expired", func(c map[string]any) { c["exp"] = now.Add(-10 * time.Minute).Unix() }},
		{"future", func(c map[string]any) { c["nbf"] = now.Add(10 * time.Minute).Unix() }},
		{"string_exp", func(c map[string]any) { c["exp"] = "9999999999" }},
		{"no_nbf", func(c map[string]any) { delete(c, "nbf") }},
		{"service_mismatch", func(c map[string]any) { c["serviceurl"] = testService + "/other" }},
		{"service_conflict", func(c map[string]any) { c["serviceUrl"] = testService + "/other" }},
		{"missing_service", func(c map[string]any) { delete(c, "serviceurl") }},
		{"private_service", func(c map[string]any) { c["serviceurl"] = "https://127.0.0.1/teams" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := claims(now)
			tc.mutate(v)
			if err := p.client.ValidateJWT(context.Background(), p.jwt(t, v, nil), testApp, testService); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
	for _, header := range []map[string]any{{"alg": "none", "kid": "current"}, {"alg": "HS256", "kid": "current"}, {"alg": "RS256", "kid": "current", "jku": "https://example.net"}, {"alg": "RS256", "kid": "current", "crit": []string{"b64"}}} {
		if err := p.client.ValidateJWT(context.Background(), p.jwt(t, claims(now), header), testApp, testService); !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	v := claims(now)
	delete(v, "serviceurl")
	v["serviceUrl"] = "HTTPS://SMBA.TRAFFICMANAGER.NET:443/teams/"
	v["unrelated_future_claim"] = true
	if err := p.client.ValidateJWT(context.Background(), p.jwt(t, v, nil), testApp, testService); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.counts["/v1/.well-known/keys"] != 1 {
		t.Fatal("claims must not cause unrelated key requests")
	}
}

func TestTeamsKeyRefreshSingleFlightExpiryAndEndorsement(t *testing.T) {
	p := newPeer(t)
	now := time.Now()
	p.client.now = func() time.Time { return now }
	token := p.jwt(t, claims(now), nil)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- p.client.ValidateJWT(context.Background(), token, testApp, testService)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	if p.counts["/v1/.well-known/keys"] != 1 {
		t.Fatal("concurrent refresh not merged")
	}
	p.keyID = "rotated"
	p.mu.Unlock()
	now = now.Add(2 * time.Minute)
	if err := p.client.ValidateJWT(context.Background(), p.jwt(t, claims(now), nil), testApp, testService); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	p.mu.Lock()
	p.keysFail = true
	p.mu.Unlock()
	if err := p.client.ValidateJWT(context.Background(), p.jwt(t, claims(now), nil), testApp, testService); !errors.Is(err, ErrUnavailable) {
		t.Fatal("expired keys accepted", err)
	}
	p2 := newPeer(t)
	p2.endorsement = false
	if err := p2.client.ValidateJWT(context.Background(), p2.jwt(t, claims(time.Now()), nil), testApp, testService); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("missing msteams endorsement", err)
	}
}

func testRoute() Route {
	return Route{TenantID: testTenant, AppID: testApp, ServiceURL: testService, ConversationID: "19:thread/opaque;messageid=123%part", ActivityID: "activity/@raw", FromID: "29:person/@opaque", RecipientID: "28:" + testApp, ScopeKind: "personal"}
}

func TestTeamsRESTAudienceOpaqueRouteAndUnknown(t *testing.T) {
	p := newPeer(t)
	ctx := context.Background()
	creds := Credentials{TenantID: testTenant, AppID: testApp, Secret: "controlled-secret"}
	connector, err := p.client.Token(ctx, creds, ConnectorScope)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := p.client.Token(ctx, creds, GraphScope)
	if err != nil {
		t.Fatal(err)
	}
	route := testRoute()
	m, err := p.client.Member(ctx, connector, route)
	if err != nil || m.AADObjectID != testAAD {
		t.Fatal(m, err)
	}
	if _, err = p.client.Member(ctx, graph, route); !errors.Is(err, ErrInvalid) {
		t.Fatal("Graph token reached connector", err)
	}
	wrongRoute := route
	wrongRoute.TenantID = testApp
	if _, err = p.client.Member(ctx, connector, wrongRoute); !errors.Is(err, ErrInvalid) {
		t.Fatal("token crossed tenant", err)
	}
	wrongRoute = route
	wrongRoute.FromAADObjectID = testTenant
	if _, err = p.client.Member(ctx, connector, wrongRoute); !errors.Is(err, ErrIncomplete) {
		t.Fatal("AAD mismatch accepted", err)
	}
	id, err := p.client.Reply(ctx, connector, route, "controlled reply")
	if err != nil || id != "opaque-reply-id" {
		t.Fatal(id, err)
	}
	p.mu.Lock()
	path, body := p.lastPath, append([]byte(nil), p.lastBody...)
	p.mu.Unlock()
	if path != "/teams/v3/conversations/19:thread%2Fopaque%3Bmessageid=123%25part/activities/activity%2F@raw" {
		t.Fatalf("opaque route changed: %s", path)
	}
	var sent map[string]any
	if json.Unmarshal(body, &sent) != nil || sent["replyToId"] != route.ActivityID || sent["textFormat"] != "plain" {
		t.Fatal("original reply route missing")
	}
	for _, tc := range []struct {
		mode    string
		unknown bool
		status  int
	}{{"unknown", true, 0}, {"missing", true, 0}, {"server", true, 503}, {"removed", false, 404}, {"rate", false, 429}, {"redirect", true, 307}} {
		t.Run(tc.mode, func(t *testing.T) {
			p.mu.Lock()
			p.replyMode = tc.mode
			p.mu.Unlock()
			_, err := p.client.Reply(ctx, connector, route, "controlled reply")
			var got *Error
			if !errors.As(err, &got) || got.Unknown != tc.unknown || tc.status != 0 && got.Status != tc.status {
				t.Fatalf("classification: %v %#v", err, got)
			}
			if tc.mode == "rate" && (got.RetryAfter != 7*time.Second || !got.RetryAfterValid) {
				t.Fatal(got.RetryAfter)
			}
		})
	}
	p.mu.Lock()
	if p.counts["/teams/v3/conversations/"+route.ConversationID+"/activities/"+route.ActivityID] != 7 {
		t.Fatal("Reply retried a non-idempotent POST")
	}
	p.mu.Unlock()
	channel := route
	channel.ScopeKind = "channel"
	channel.TeamID = "19:team/@opaque"
	channel.ChannelID = "19:channel/@opaque"
	team, err := p.client.Team(ctx, connector, channel)
	if err != nil {
		t.Fatal(err)
	}
	channel.AADGroupID = team.AADGroupID
	if _, err = p.client.Channel(ctx, graph, channel); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing", "private", "chat", "archived"} {
		p.mu.Lock()
		p.channelMode = mode
		p.mu.Unlock()
		if _, err = p.client.Channel(ctx, graph, channel); !errors.Is(err, ErrChannelScope) {
			t.Fatal(mode, err)
		}
	}
	if _, err = p.client.Channel(ctx, connector, channel); !errors.Is(err, ErrInvalid) {
		t.Fatal("Connector token reached Graph", err)
	}
}

func TestTeamsServiceURLAndJSONBoundaries(t *testing.T) {
	for _, raw := range []string{"http://smba.trafficmanager.net/teams", "https://evil.example/teams", "https://smba.trafficmanager.net.evil.example/teams", "https://user@smba.trafficmanager.net/teams", "https://smba.trafficmanager.net:444/teams", "https://smba.trafficmanager.net/teams?token=x", "https://smba.trafficmanager.net/a/../teams", "https://smba.trafficmanager.net/%2e%2e/teams", "https://smba.trafficmanager.net/teams#fragment", "https://smba.trafficmanager.net//teams"} {
		if _, err := ServiceURL(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, raw := range [][]byte{[]byte(`{"a":1,"a":2}`), []byte(`{"a":{"b":1,"b":2}}`), []byte(`{} {}`), {0xff}} {
		var v map[string]any
		if decodeJSON(raw, &v) == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	if StableKey("activity", "a", "bc") == StableKey("activity", "ab", "c") {
		t.Fatal("ambiguous key")
	}
}

func TestTeamsRetryAfterPreservesBounds(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	for _, tc := range []struct {
		raw   string
		want  time.Duration
		valid bool
	}{
		{"2147483648", time.Duration(2147483648) * time.Second, true},
		{"9223372036", time.Duration(9223372036) * time.Second, true},
		{"9223372037", 0, false},
		{"18446744073709551616", 0, false},
		{"-1", 0, false}, {"invalid", 0, false}, {"", 0, false},
		{"0", 0, true}, {"Thu, 01 Jan 1970 00:00:07 GMT", 7 * time.Second, true},
		{"Fri, 31 Dec 9999 23:59:59 GMT", 0, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, valid := retryAfter(tc.raw, now)
			if got != tc.want || valid != tc.valid {
				t.Fatalf("got %v valid=%v", got, valid)
			}
		})
	}
	// Verify the public error on an actual TLS HTTP 429, not only the parser.
	p := newPeer(t)
	token, err := p.client.Token(context.Background(), Credentials{TenantID: testTenant, AppID: testApp, Secret: "controlled-secret"}, ConnectorScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"2147483648", "9223372037"} {
		p.mu.Lock()
		p.replyMode = "rate"
		p.retryHeader = header
		p.mu.Unlock()
		_, err := p.client.Reply(context.Background(), token, testRoute(), "controlled reply")
		var result *Error
		if !errors.As(err, &result) || result.Code != "rate_limited" || result.Unknown {
			t.Fatal(err)
		}
		if header == "2147483648" && (!result.RetryAfterValid || result.RetryAfter != time.Duration(2147483648)*time.Second) {
			t.Fatal("long wait reduced")
		}
		if header == "9223372037" && (result.RetryAfterValid || result.RetryAfter != 0) {
			t.Fatal("overflow scheduled an early retry")
		}
	}
}
