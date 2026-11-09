// Package mcpoauth handles Streamable HTTP authorization without exposing MCP
// credentials to the renderer. No endpoint may redirect a request containing a secret.
package mcpoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"openfox/internal/platform/safehttp"
)

var ErrProtocol = errors.New("MCP authorization server returned an unsupported response")
var challengeMetadata = regexp.MustCompile(`(?i)\bresource_metadata\s*=\s*"([^"]+)"`)

type Client struct{ HTTP *http.Client }

func New(options safehttp.Options) Client {
	c := safehttp.Client(options)
	c.Timeout = 10 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return Client{c}
}

type Pending struct {
	Resource, Issuer, TokenEndpoint, ClientID, ClientSecret, Verifier, RedirectURI, AuthMethod string
	RequireIssuer                                                                              bool
}
type Tokens struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func Random() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (c Client) req(ctx context.Context, method, raw, typ, body string, out any, basic ...string) error {
	if _, err := safehttp.CheckURL(raw); err != nil {
		return ErrProtocol
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, strings.NewReader(body))
	if err != nil {
		return ErrProtocol
	}
	req.Header.Set("Accept", "application/json")
	if typ != "" {
		req.Header.Set("Content-Type", typ)
	}
	if len(basic) == 2 {
		req.SetBasicAuth(url.QueryEscape(basic[0]), url.QueryEscape(basic[1]))
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return ErrProtocol
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return ErrProtocol
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 128*1024+1))
	if err != nil || len(b) > 128*1024 || json.Unmarshal(b, out) != nil {
		return ErrProtocol
	}
	return nil
}
func wellKnown(u *url.URL, kind string, includePath bool) string {
	p := ""
	if includePath {
		p = strings.TrimSuffix(u.EscapedPath(), "/")
	}
	return u.Scheme + "://" + u.Host + "/.well-known/" + kind + p
}
func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
func (c Client) Start(ctx context.Context, resource, redirect, state, id, secret string) (Pending, string, error) {
	no := func() (Pending, string, error) { return Pending{}, "", ErrProtocol }
	u, err := safehttp.CheckURL(resource)
	if err != nil || len(id) > 1024 || len(secret) > 2048 {
		return no()
	}
	// A protected resource can advertise metadata at a non-default URL.
	req, err := http.NewRequestWithContext(ctx, "GET", resource, nil)
	if err != nil {
		return no()
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	var sources []string
	if resp, e := c.HTTP.Do(req); e == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == 401 {
			if m := challengeMetadata.FindStringSubmatch(resp.Header.Get("WWW-Authenticate")); len(m) == 2 {
				sources = append(sources, m[1])
			}
		}
	}
	sources = append(sources, wellKnown(u, "oauth-protected-resource", true), wellKnown(u, "oauth-protected-resource", false))
	var protected struct {
		Resource string
		Servers  []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if seen[s] {
			continue
		}
		seen[s] = true
		protected.Servers = nil
		if c.req(ctx, "GET", s, "", "", &protected) == nil && len(protected.Servers) > 0 {
			break
		}
	}
	pr, err := safehttp.CheckURL(protected.Resource)
	if err != nil || !strings.EqualFold(pr.Host, u.Host) || !(strings.TrimSuffix(pr.Path, "/") == strings.TrimSuffix(u.Path, "/") || strings.HasPrefix(u.Path, strings.TrimSuffix(pr.Path, "/")+"/")) || len(protected.Servers) == 0 {
		return no()
	}
	issuer := protected.Servers[0]
	iss, err := safehttp.CheckURL(issuer)
	if err != nil || iss.RawQuery != "" {
		return no()
	}
	var meta struct {
		Issuer        string
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Registration  string   `json:"registration_endpoint"`
		Methods       []string `json:"token_endpoint_auth_methods_supported"`
		PKCE          []string `json:"code_challenge_methods_supported"`
		RequireIssuer bool     `json:"authorization_response_iss_parameter_supported"`
	}
	for _, s := range []string{wellKnown(iss, "oauth-authorization-server", true), wellKnown(iss, "openid-configuration", true), strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"} {
		if c.req(ctx, "GET", s, "", "", &meta) == nil && meta.Issuer == issuer {
			break
		}
	}
	auth, err := safehttp.CheckURL(meta.Authorization)
	if err != nil || meta.Issuer != issuer || !contains(meta.PKCE, "S256") {
		return no()
	}
	if _, err = safehttp.CheckURL(meta.Token); err != nil {
		return no()
	}
	method := "none"
	if secret != "" && id == "" {
		return no()
	}
	if id == "" {
		payload, _ := json.Marshal(map[string]any{"client_name": "OpenFox", "redirect_uris": []string{redirect}, "grant_types": []string{"authorization_code"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
		var registered struct {
			ID string `json:"client_id"`
		}
		if c.req(ctx, "POST", meta.Registration, "application/json", string(payload), &registered) != nil || registered.ID == "" || len(registered.ID) > 1024 {
			return no()
		}
		id = registered.ID
	} else if secret != "" {
		if len(meta.Methods) == 0 || contains(meta.Methods, "client_secret_basic") {
			method = "client_secret_basic"
		} else if contains(meta.Methods, "client_secret_post") {
			method = "client_secret_post"
		} else {
			return no()
		}
	}
	p := Pending{resource, issuer, meta.Token, id, secret, Random(), redirect, method, meta.RequireIssuer}
	q := auth.Query()
	q.Set("response_type", "code")
	q.Set("client_id", id)
	q.Set("redirect_uri", redirect)
	q.Set("resource", resource)
	q.Set("state", state)
	sum := sha256.Sum256([]byte(p.Verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	if len(protected.Scopes) > 0 {
		q.Set("scope", strings.Join(protected.Scopes, " "))
	}
	auth.RawQuery = q.Encode()
	return p, auth.String(), nil
}
func (c Client) Exchange(ctx context.Context, p Pending, code, issuer string) (Tokens, error) {
	var t Tokens
	if code == "" || len(code) > 4096 || (issuer != "" || p.RequireIssuer) && issuer != p.Issuer {
		return t, ErrProtocol
	}
	q := url.Values{"grant_type": {"authorization_code"}, "redirect_uri": {p.RedirectURI}, "resource": {p.Resource}, "code": {code}, "code_verifier": {p.Verifier}}
	var basic []string
	if p.AuthMethod == "client_secret_basic" {
		basic = []string{p.ClientID, p.ClientSecret}
	} else {
		q.Set("client_id", p.ClientID)
		if p.AuthMethod == "client_secret_post" {
			q.Set("client_secret", p.ClientSecret)
		}
	}
	if c.req(ctx, "POST", p.TokenEndpoint, "application/x-www-form-urlencoded", q.Encode(), &t, basic...) != nil || !strings.EqualFold(t.TokenType, "bearer") || t.AccessToken == "" || len(t.AccessToken) > 4096 || strings.ContainsAny(t.AccessToken, "\r\n") || t.ExpiresIn < 0 || t.ExpiresIn > 10*365*86400 {
		return Tokens{}, ErrProtocol
	}
	return t, nil
}
