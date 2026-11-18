package integrations

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
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func slackTestTLS(t *testing.T, handler http.HandlerFunc) http.RoundTripper {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "slack.com:443" {
			return nil, errors.New("external network forbidden")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}
func slackTestReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func slackTestJWT(t *testing.T, key *rsa.PrivateKey, claims any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "fixture"})
	p, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	hash := sha256.Sum256([]byte(body))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if e != nil {
		t.Fatal(e)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func (f *slackPG) claim(session SlackOAuthSession, personal bool) SlackCallbackTarget {
	f.t.Helper()
	u, _ := url.Parse(session.AuthorizeURL)
	var target SlackCallbackTarget
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		target, e = f.service.ClaimSlackCallback(ctx, tx, f.org, session.ID, u.Query().Get("state"), personal, f.user)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return target
}
func TestSlackOAuthTLSInstallationAndIdentityPG(t *testing.T) {
	f := newSlackPG(t)
	teamID, appID := "T"+f.org, "A"+f.org
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var nonce atomic.Value
	nonce.Store("")
	f.service.SlackTransport = slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" && r.URL.Path != "/api/users.info" {
			t.Error("unexpected query parameters")
		}
		switch r.URL.Path {
		case "/api/oauth.v2.access":
			id, secret, ok := r.BasicAuth()
			if !ok || id != f.service.SlackClientID || secret != f.service.SlackClientSecret {
				t.Error("client authentication")
			}
			slackTestReply(w, map[string]any{"ok": true, "access_token": "synthetic-bot-access", "refresh_token": "synthetic-bot-refresh", "expires_in": 43200, "token_type": "bot", "scope": strings.Join(slackCentralScopes, ","), "app_id": appID, "bot_user_id": "UBOT", "team": map[string]string{"id": teamID, "name": "Local workspace"}, "authed_user": map[string]string{"id": "UHUMAN"}})
		case "/api/auth.test":
			slackTestReply(w, map[string]any{"ok": true, "team_id": teamID, "user_id": "UBOT", "bot_id": "B01"})
		case "/api/users.info":
			slackTestReply(w, map[string]any{"ok": true, "user": map[string]any{"id": "UHUMAN", "team_id": teamID, "name": "Synthetic member", "deleted": false, "is_bot": false, "is_restricted": false, "is_ultra_restricted": false}})
		case "/api/openid.connect.token":
			now := time.Now()
			access := sha256.Sum256([]byte("synthetic-user-access"))
			claims := map[string]any{"iss": "https://slack.com", "aud": f.service.SlackClientID, "sub": "independent-subject", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": nonce.Load(), "at_hash": base64.RawURLEncoding.EncodeToString(access[:16]), "https://slack.com/team_id": teamID, "https://slack.com/user_id": "UHUMAN"}
			slackTestReply(w, map[string]any{"ok": true, "access_token": "synthetic-user-access", "token_type": "Bearer", "id_token": slackTestJWT(t, key, claims)})
		case "/.well-known/openid-configuration":
			slackTestReply(w, map[string]any{"issuer": "https://slack.com", "authorization_endpoint": "https://slack.com/openid/connect/authorize", "token_endpoint": "https://slack.com/api/openid.connect.token", "userinfo_endpoint": "https://slack.com/api/openid.connect.userInfo", "jwks_uri": "https://slack.com/openid/connect/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/openid/connect/keys":
			slackTestReply(w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/api/openid.connect.userInfo":
			slackTestReply(w, map[string]any{"ok": true, "sub": "independent-subject", "https://slack.com/team_id": teamID, "https://slack.com/user_id": "UHUMAN", "name": "Synthetic member"})
		default:
			t.Error("unexpected protocol path", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	session, e := f.start("tls-organization-start")
	if e != nil {
		t.Fatal(e)
	}
	target := f.claim(session, false)
	out := f.service.ExchangeSlackCallback(context.Background(), target, "synthetic-code")
	if out.Failure != "" {
		t.Fatal(out.Failure)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		r, e := f.service.FinishSlackCallback(ctx, tx, f.org, target, out, true)
		if e == nil && r.Status != "DONE" {
			t.Fatal(r.Status)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var app SlackApp
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		app, e = loadSlackApp(ctx, tx, session.AppID)
		if e != nil {
			return e
		}
		if app.State != "ACTIVE" || !app.URLVerified || !app.Installed {
			t.Fatal("dual gate", app.State)
		}
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.slack_identities`).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			t.Fatal("installer was auto-bound")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	var identitySession SlackOAuthSession
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		identitySession, e = f.service.StartSlackAuthorization(ctx, tx, f.org, f.actor, SlackOAuthInput{Purpose: "personal", AppID: app.ID, Version: app.Version, IdempotencyKey: "tls-personal-identity"})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	identityURL, _ := url.Parse(identitySession.AuthorizeURL)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := f.service.ClaimSlackCallback(ctx, tx, f.org, identitySession.ID, identityURL.Query().Get("state"), true, refsTestID(t))
		return err
	})
	if !errors.Is(e, ErrOAuthSessionState) {
		t.Fatal("callback from a different OpenFox user was not rejected", e)
	}
	personal := f.claim(identitySession, true)
	nonce.Store(personal.Nonce)
	identityOut := f.service.ExchangeSlackCallback(context.Background(), personal, "synthetic-oidc-code")
	if identityOut.Failure != "" {
		t.Fatal(identityOut.Failure)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.FinishSlackCallback(ctx, tx, f.org, personal, identityOut, false)
		if e != nil {
			return e
		}
		var count, tokenCount, prefs int
		if e = tx.QueryRow(ctx, `SELECT count(*),count(c.credentials_sealed) FROM openfox.slack_identities i JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.active AND i.owner_user_id=$1::uuid AND i.team_id=$2 AND i.slack_user_id='UHUMAN'`, f.user, teamID).Scan(&count, &tokenCount); e != nil {
			return e
		}
		if count != 1 || tokenCount != 0 {
			t.Fatalf("identity count=%d persisted user credentials=%d", count, tokenCount)
		}
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel='slack' AND enabled`, f.user).Scan(&prefs); e != nil {
			return e
		}
		if prefs != 0 {
			t.Fatal("notifications implicitly enabled")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(identitySession.AuthorizeURL)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ClaimSlackCallback(ctx, tx, f.org, identitySession.ID, u.Query().Get("state"), true, f.user)
		return e
	})
	if !errors.Is(e, ErrOAuthSessionState) {
		t.Fatal("callback replay accepted", e)
	}
}
func TestSlackCallbackLateRevocationAndURLGatePG(t *testing.T) {
	f := newSlackPG(t)
	session, e := f.start("late-callback-operation")
	if e != nil {
		t.Fatal(e)
	}
	target := f.claim(session, false)
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	second := refsTestID(t)
	if _, e = c.Exec(context.Background(), `INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, second, second+"@example.test"); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(context.Background(), `INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, f.org, second); e != nil {
		t.Fatal(e)
	}
	if e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET status='REVOKED',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
		return err
	}); e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.FinishSlackCallback(ctx, tx, f.org, target, SlackCallbackOutcome{Failure: "access_denied"}, true)
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("late revoked callback", e)
	}
	var active bool
	if e = c.QueryRow(context.Background(), `SELECT installed_at IS NOT NULL OR state='ACTIVE' FROM openfox.slack_apps WHERE organization_id=$1::uuid AND id=$2::uuid`, f.org, session.AppID).Scan(&active); e != nil || active {
		t.Fatal("late response activated", active, e)
	}
}
