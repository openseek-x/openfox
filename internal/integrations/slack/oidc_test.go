package slack

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func jwtSign(t *testing.T, key *rsa.PrivateKey, header, claims any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	digest := sha256.Sum256([]byte(encoded))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if e != nil {
		t.Fatal(e)
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func testClaims() map[string]any {
	access := sha256.Sum256([]byte("oidc-access"))
	return map[string]any{"iss": Origin, "sub": "subject-independent", "aud": testApp.ClientID, "exp": testNow.Add(5 * time.Minute).Unix(), "iat": testNow.Unix(), "nonce": "0123456789abcdef", "at_hash": base64.RawURLEncoding.EncodeToString(access[:16]), "https://slack.com/team_id": "T1", "https://slack.com/user_id": "W1", "name": "Profile"}
}
func discovery() map[string]any {
	return map[string]any{"issuer": Origin, "authorization_endpoint": Origin + "/openid/connect/authorize", "token_endpoint": Origin + "/api/openid.connect.token", "userinfo_endpoint": Origin + "/api/openid.connect.userInfo", "jwks_uri": Origin + "/openid/connect/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}
}
func jwk(key *rsa.PrivateKey, kid string) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}
func TestOIDCVerifiedIdentityAndBoundedRotation(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	second, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var phase atomic.Int32
	var keysCalls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/openid.connect.token":
			id, secret, ok := r.BasicAuth()
			r.ParseForm()
			if !ok || id != testApp.ClientID || secret != testApp.ClientSecret || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != "https://openfox.example.test/identity" {
				t.Error("OIDC confidential exchange")
			}
			k, kid := key, "one"
			if phase.Load() > 0 {
				k, kid = second, "two"
			}
			reply(w, map[string]any{"ok": true, "access_token": "oidc-access", "token_type": "Bearer", "id_token": jwtSign(t, k, map[string]any{"alg": "RS256", "kid": kid}, testClaims())})
		case "/.well-known/openid-configuration":
			reply(w, discovery())
		case "/openid/connect/keys":
			keysCalls.Add(1)
			k, kid := key, "one"
			if phase.Load() > 0 {
				k, kid = second, "two"
			}
			reply(w, map[string]any{"keys": []any{jwk(k, kid)}})
		case "/api/openid.connect.userInfo":
			if r.Header.Get("Authorization") != "Bearer oidc-access" {
				t.Error("OIDC bearer")
			}
			reply(w, map[string]any{"ok": true, "sub": "subject-independent", "https://slack.com/team_id": "T1", "https://slack.com/user_id": "W1", "name": "User"})
		default:
			t.Error("unexpected OIDC URL")
		}
	})
	for i := 0; i < 3; i++ {
		if i == 2 {
			phase.Store(1)
		}
		identity, e := c.VerifyOIDC(context.Background(), testApp, "code", "https://openfox.example.test/identity", "0123456789abcdef")
		if e != nil || identity.Subject != "subject-independent" || identity.UserID != "W1" || identity.TeamID != "T1" {
			t.Fatal("OIDC verify", e)
		}
	}
	if keysCalls.Load() != 2 {
		t.Fatal("JWK cache/unknown kid bound", keysCalls.Load())
	}
}
func TestOIDCRejectsForgedAndMismatchedProof(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	wrong, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name           string
		mutate         func(map[string]any, map[string]any)
		wrongKey       bool
		userinfo       string
		discoveryWrong bool
		duplicateKey   bool
	}{
		{name: "bad-signature", wrongKey: true}, {name: "alg-none", mutate: func(h, c map[string]any) { h["alg"] = "none" }}, {name: "alg-HS256", mutate: func(h, c map[string]any) { h["alg"] = "HS256" }}, {name: "jku", mutate: func(h, c map[string]any) { h["jku"] = "https://attacker.example/key" }}, {name: "critical", mutate: func(h, c map[string]any) { h["crit"] = []string{"b64"} }}, {name: "kid", mutate: func(h, c map[string]any) { h["kid"] = "absent" }},
		{name: "issuer", mutate: func(h, c map[string]any) { c["iss"] = "https://slack.com.attacker.example" }}, {name: "audience", mutate: func(h, c map[string]any) { c["aud"] = "other" }}, {name: "multi-audience-no-azp", mutate: func(h, c map[string]any) { c["aud"] = []string{testApp.ClientID, "other"} }}, {name: "wrong-azp", mutate: func(h, c map[string]any) { c["azp"] = "other" }}, {name: "expired", mutate: func(h, c map[string]any) { c["exp"] = testNow.Unix() }}, {name: "future-issued", mutate: func(h, c map[string]any) { c["iat"] = testNow.Add(2 * time.Minute).Unix() }}, {name: "too-old-issued", mutate: func(h, c map[string]any) { c["iat"] = testNow.Add(-11 * time.Minute).Unix() }}, {name: "nonce", mutate: func(h, c map[string]any) { c["nonce"] = "wrong" }}, {name: "missing-team", mutate: func(h, c map[string]any) { delete(c, "https://slack.com/team_id") }}, {name: "access-hash", mutate: func(h, c map[string]any) { c["at_hash"] = "wrong" }}, {name: "userinfo-sub", userinfo: "sub"}, {name: "userinfo-user", userinfo: "https://slack.com/user_id"}, {name: "userinfo-team", userinfo: "https://slack.com/team_id"}, {name: "foreign-jwks", discoveryWrong: true}, {name: "duplicate-kid", duplicateKey: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := testClaims()
			header := map[string]any{"alg": "RS256", "kid": "one"}
			if tc.mutate != nil {
				tc.mutate(header, claims)
			}
			signer := key
			if tc.wrongKey {
				signer = wrong
			}
			signed := jwtSign(t, signer, header, claims)
			var keysCount atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/openid.connect.token":
					reply(w, map[string]any{"ok": true, "access_token": "oidc-access", "token_type": "Bearer", "id_token": signed})
				case "/.well-known/openid-configuration":
					v := discovery()
					if tc.discoveryWrong {
						v["jwks_uri"] = "https://attacker.example/keys"
					}
					reply(w, v)
				case "/openid/connect/keys":
					keysCount.Add(1)
					keys := []any{jwk(key, "one")}
					if tc.duplicateKey {
						keys = append(keys, jwk(wrong, "one"))
					}
					reply(w, map[string]any{"keys": keys})
				case "/api/openid.connect.userInfo":
					v := map[string]any{"ok": true, "sub": "subject-independent", "https://slack.com/team_id": "T1", "https://slack.com/user_id": "W1", "name": "User"}
					if tc.userinfo != "" {
						v[tc.userinfo] = "other"
					}
					reply(w, v)
				default:
					t.Fatal("untrusted URL followed")
				}
			})
			_, e := c.VerifyOIDC(context.Background(), testApp, "code", "https://openfox.example.test/identity", "0123456789abcdef")
			if !errors.Is(e, ErrIdentity) {
				t.Fatal("bad proof accepted", e)
			}
			if keysCount.Load() > 1 {
				t.Fatal("unbounded unknown-kid refetch")
			}
			if strings.Contains(e.Error(), signed) || strings.Contains(e.Error(), "oidc-access") {
				t.Fatal("JWT/token leaked")
			}
		})
	}
}
func TestOIDCMultiAudienceAndOptionalAccessHash(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			reply(w, discovery())
		} else {
			reply(w, map[string]any{"keys": []any{jwk(key, "one")}})
		}
	})
	claims := testClaims()
	claims["aud"] = []string{testApp.ClientID, "another-relying-party"}
	claims["azp"] = testApp.ClientID
	delete(claims, "at_hash")
	token := jwtSign(t, key, map[string]any{"alg": "RS256", "kid": "one"}, claims)
	if _, e = c.verifyIDToken(context.Background(), token, testApp.ClientID, "0123456789abcdef", "oidc-access"); e != nil {
		t.Fatal(e)
	}
}

func TestOIDCDuplicateClaimsNeverUseLastValue(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	claims, _ := json.Marshal(testClaims())
	raw := string(claims[:len(claims)-1]) + `,"iss":"https://slack.com"}`
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "one"})
	encoded := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString([]byte(raw))
	digest := sha256.Sum256([]byte(encoded))
	sig, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	if _, e = c.verifyIDToken(context.Background(), encoded+"."+base64.RawURLEncoding.EncodeToString(sig), testApp.ClientID, "0123456789abcdef", "oidc-access"); !errors.Is(e, ErrIdentity) || calls.Load() != 0 {
		t.Fatal("ambiguous JWT accepted")
	}
}
