package slack

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/url"
	"strings"
	"time"
)

type oidcCache struct {
	Keys  map[string]*rsa.PublicKey
	Until time.Time
}

func (c *Client) oidcKeys(ctx context.Context, force bool) (*oidcCache, bool, error) {
	if c == nil {
		return nil, false, ErrInvalid
	}
	c.oidcMu.Lock()
	cached := c.oidc
	c.oidcMu.Unlock()
	if !force && cached != nil && c.now().Before(cached.Until) {
		return cached, true, nil
	}
	data, e := c.raw(ctx, "GET", "/.well-known/openid-configuration", "", nil, nil, nil, false)
	if e != nil {
		return nil, false, e
	}
	var d struct {
		Issuer     string   `json:"issuer"`
		Authorize  string   `json:"authorization_endpoint"`
		Token      string   `json:"token_endpoint"`
		Info       string   `json:"userinfo_endpoint"`
		Keys       string   `json:"jwks_uri"`
		Algorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if json.Unmarshal(data, &d) != nil || d.Issuer != Origin || d.Authorize != Origin+"/openid/connect/authorize" || d.Token != Origin+"/api/openid.connect.token" || d.Info != Origin+"/api/openid.connect.userInfo" || d.Keys != Origin+"/openid/connect/keys" || !contains(d.Algorithms, "RS256") {
		return nil, false, ErrIdentity
	}
	data, e = c.raw(ctx, "GET", "/openid/connect/keys", "", nil, nil, nil, false)
	if e != nil {
		return nil, false, e
	}
	var jwks struct {
		Keys []struct {
			Kty string   `json:"kty"`
			Kid string   `json:"kid"`
			Use string   `json:"use"`
			Alg string   `json:"alg"`
			N   string   `json:"n"`
			E   string   `json:"e"`
			D   string   `json:"d"`
			Ops []string `json:"key_ops"`
		} `json:"keys"`
	}
	if json.Unmarshal(data, &jwks) != nil || len(jwks.Keys) == 0 || len(jwks.Keys) > 32 {
		return nil, false, ErrIdentity
	}
	keys := map[string]*rsa.PublicKey{}
	for _, j := range jwks.Keys {
		if !ID(j.Kid) || keys[j.Kid] != nil || j.Kty != "RSA" || j.Use != "" && j.Use != "sig" || j.Alg != "" && j.Alg != "RS256" || j.D != "" || len(j.Ops) > 0 && !contains(j.Ops, "verify") {
			return nil, false, ErrIdentity
		}
		n, e := base64.RawURLEncoding.DecodeString(j.N)
		if e != nil || len(n) < 256 || len(n) > 512 || n[0] == 0 {
			return nil, false, ErrIdentity
		}
		exp, e := base64.RawURLEncoding.DecodeString(j.E)
		if e != nil || len(exp) == 0 || len(exp) > 4 || exp[0] == 0 {
			return nil, false, ErrIdentity
		}
		var exponent uint64
		for _, b := range exp {
			exponent = exponent<<8 | uint64(b)
		}
		if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
			return nil, false, ErrIdentity
		}
		number := new(big.Int).SetBytes(n)
		if number.BitLen() < 2048 || number.BitLen() > 4096 {
			return nil, false, ErrIdentity
		}
		keys[j.Kid] = &rsa.PublicKey{N: number, E: int(exponent)}
	}
	cache := &oidcCache{Keys: keys, Until: c.now().Add(15 * time.Minute)}
	c.oidcMu.Lock()
	c.oidc = cache
	c.oidcMu.Unlock()
	return cache, false, nil
}
func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
func equalSecret(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type oidcClaims struct {
	Issuer     string          `json:"iss"`
	Subject    string          `json:"sub"`
	Audience   json.RawMessage `json:"aud"`
	AZP        string          `json:"azp"`
	Exp        int64           `json:"exp"`
	IAT        int64           `json:"iat"`
	AuthTime   int64           `json:"auth_time"`
	Nonce      string          `json:"nonce"`
	AccessHash string          `json:"at_hash"`
	TeamID     string          `json:"https://slack.com/team_id"`
	UserID     string          `json:"https://slack.com/user_id"`
	Name       string          `json:"name"`
}

func (c *Client) verifyIDToken(ctx context.Context, jwt, clientID, nonce, access string) (oidcClaims, error) {
	var out oidcClaims
	if len(jwt) > 32<<10 {
		return out, ErrIdentity
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return out, ErrIdentity
	}
	header, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || !jsonObject(header) {
		return out, ErrIdentity
	}
	claims, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || !jsonObject(claims) {
		return out, ErrIdentity
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil {
		return out, ErrIdentity
	}
	var h struct {
		Alg  string   `json:"alg"`
		Kid  string   `json:"kid"`
		Typ  string   `json:"typ"`
		JKU  string   `json:"jku"`
		X5U  string   `json:"x5u"`
		Crit []string `json:"crit"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "RS256" || !ID(h.Kid) || h.Typ != "" && h.Typ != "JWT" || h.JKU != "" || h.X5U != "" || len(h.Crit) > 0 {
		return out, ErrIdentity
	}
	cache, wasCached, e := c.oidcKeys(ctx, false)
	if e != nil {
		return out, e
	}
	key := cache.Keys[h.Kid]
	if key == nil && wasCached {
		cache, _, e = c.oidcKeys(ctx, true)
		if e != nil {
			return out, e
		}
		key = cache.Keys[h.Kid]
	}
	if key == nil {
		return out, ErrIdentity
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return out, ErrIdentity
	}
	if json.Unmarshal(claims, &out) != nil || out.Issuer != Origin || !ID(out.Subject) || !ID(out.TeamID) || !ID(out.UserID) || !equalSecret(out.Nonce, nonce) || !bounded(out.Name, 255) {
		return oidcClaims{}, ErrIdentity
	}
	audiences := []string{}
	var one string
	if json.Unmarshal(out.Audience, &one) == nil {
		audiences = []string{one}
	} else if json.Unmarshal(out.Audience, &audiences) != nil {
		return oidcClaims{}, ErrIdentity
	}
	if len(audiences) == 0 || len(audiences) > 8 || !contains(audiences, clientID) || out.AZP != "" && out.AZP != clientID || len(audiences) > 1 && out.AZP != clientID {
		return oidcClaims{}, ErrIdentity
	}
	seen := map[string]bool{}
	for _, v := range audiences {
		if !ID(v) || seen[v] {
			return oidcClaims{}, ErrIdentity
		}
		seen[v] = true
	}
	now := c.now().Unix()
	if out.Exp <= now || out.IAT <= 0 || out.IAT > now+60 || out.IAT < now-600 || out.Exp <= out.IAT || out.Exp-out.IAT > 3600 || out.AuthTime > now+60 {
		return oidcClaims{}, ErrIdentity
	}
	if out.AccessHash != "" {
		digest := sha256.Sum256([]byte(access))
		expected := base64.RawURLEncoding.EncodeToString(digest[:len(digest)/2])
		if !equalSecret(expected, out.AccessHash) {
			return oidcClaims{}, ErrIdentity
		}
	}
	return out, nil
}

// VerifyOIDC returns only verified identifiers. Access/ID tokens remain local to
// this call. The caller consumes state and checks its current actor/parent.
func (c *Client) VerifyOIDC(ctx context.Context, app AppCredentials, code, redirectURI, nonce string) (Identity, error) {
	if !secret(code) || !redirect(redirectURI) || !secret(nonce) || len(nonce) < 16 {
		return Identity{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var tokens struct {
		Access  string `json:"access_token"`
		Type    string `json:"token_type"`
		IDToken string `json:"id_token"`
	}
	if e := c.api(ctx, "POST", "openid.connect.token", "", &app, url.Values{"code": {code}, "redirect_uri": {redirectURI}, "grant_type": {"authorization_code"}}, nil, true, &tokens); e != nil {
		return Identity{}, e
	}
	if !secret(tokens.Access) || !strings.EqualFold(tokens.Type, "Bearer") || tokens.IDToken == "" {
		return Identity{}, incomplete(true)
	}
	claims, e := c.verifyIDToken(ctx, tokens.IDToken, app.ClientID, nonce, tokens.Access)
	if e != nil {
		return Identity{}, e
	}
	var info struct {
		Subject string `json:"sub"`
		UserID  string `json:"https://slack.com/user_id"`
		TeamID  string `json:"https://slack.com/team_id"`
		Name    string `json:"name"`
	}
	if e = c.api(ctx, "POST", "openid.connect.userInfo", tokens.Access, nil, url.Values{}, nil, false, &info); e != nil {
		return Identity{}, e
	}
	if info.Subject != claims.Subject || info.UserID != claims.UserID || info.TeamID != claims.TeamID || !bounded(info.Name, 255) {
		return Identity{}, ErrIdentity
	}
	return Identity{Subject: claims.Subject, TeamID: claims.TeamID, UserID: claims.UserID, Name: info.Name}, nil
}
