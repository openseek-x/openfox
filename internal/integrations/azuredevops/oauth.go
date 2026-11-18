package azuredevops

import (
	"context"
	"encoding/base64"
	"net"
	"net/url"
	"strings"
	"time"
)

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresIn    int64     `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func validRedirect(v string) bool {
	u, e := url.Parse(v)
	if e != nil || len(v) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback())
}
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, b := range []byte(v) {
		if !((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || strings.ContainsRune("-._~", rune(b))) {
			return false
		}
	}
	return true
}
func (c *Client) configured() bool {
	return c != nil && ValidUUID(c.App.ClientID) && ValidText(c.App.ClientSecret, 32768)
}
func (c *Client) AuthorizeURL(tenant, state, challenge, redirect string, personal bool) (string, error) {
	if !c.configured() {
		return "", &Error{Code: "not_configured"}
	}
	digest, e := base64.RawURLEncoding.Strict().DecodeString(challenge)
	if !ValidUUID(tenant) || !ValidText(state, 512) || e != nil || len(digest) != 32 || !validRedirect(redirect) {
		return "", invalid()
	}
	scope := ResourceScope
	if !personal {
		scope += " offline_access"
	}
	q := url.Values{"client_id": {c.App.ClientID}, "response_type": {"code"}, "response_mode": {"query"}, "redirect_uri": {redirect}, "scope": {scope}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return "https://login.microsoftonline.com/" + uuid(tenant) + "/oauth2/v2.0/authorize?" + q.Encode(), nil
}
func (c *Client) Exchange(ctx context.Context, tenant, code, verifier, redirect string, personal bool) (Token, error) {
	if !ValidToken(code) || !validVerifier(verifier) || !validRedirect(redirect) {
		return Token{}, invalid()
	}
	scope := ResourceScope
	if !personal {
		scope += " offline_access"
	}
	return c.token(ctx, tenant, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "scope": {scope}}, !personal)
}
func (c *Client) Refresh(ctx context.Context, tenant, refresh string) (Token, error) {
	if !ValidToken(refresh) {
		return Token{}, invalid()
	}
	return c.token(ctx, tenant, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "scope": {ResourceScope + " offline_access"}}, false)
}
func (c *Client) token(ctx context.Context, tenant string, form url.Values, requireRefresh bool) (Token, error) {
	var out Token
	if !c.configured() {
		return out, &Error{Code: "not_configured"}
	}
	if !ValidUUID(tenant) {
		return out, invalid()
	}
	form.Set("client_id", c.App.ClientID)
	form.Set("client_secret", c.App.ClientSecret)
	now := time.Now()
	_, e := c.request(ctx, "POST", "https://login.microsoftonline.com/"+uuid(tenant)+"/oauth2/v2.0/token", "", nil, form, true, &out)
	if e != nil {
		return Token{}, e
	}
	if !ValidToken(out.AccessToken) || !strings.EqualFold(out.TokenType, "Bearer") || out.ExpiresIn <= 0 || out.ExpiresIn > int64((time.Duration(1<<63-1))/time.Second) || len(out.Scope) > 32768 ||
		(requireRefresh && !ValidToken(out.RefreshToken)) || (out.RefreshToken != "" && !ValidToken(out.RefreshToken)) {
		return Token{}, &Error{Code: "invalid_response", Unknown: true}
	}
	out.ExpiresAt = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	return out, nil
}
