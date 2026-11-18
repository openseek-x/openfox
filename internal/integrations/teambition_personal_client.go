package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrPersonalIdentity    = errors.New("integrations: personal identity does not match")
	ErrPersonalInUse       = errors.New("integrations: personal identity is already bound")
	ErrPersonalCapability  = errors.New("integrations: personal directory permission denied")
	ErrPersonalRateLimited = errors.New("integrations: personal identity rate limited")
)

// TeambitionPersonalClient has one fixed official origin. Transport is a
// server-owned test seam; callers cannot choose a URL or inject identity headers.
type TeambitionPersonalClient struct{ Transport http.RoundTripper }

type TeambitionIdentity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (c TeambitionPersonalClient) request(ctx context.Context, app ProviderTokenInput, path string, body any, tenant bool, result any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return ErrInvalidRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providerRoots["teambition"]+path, bytes.NewReader(raw))
	if err != nil {
		return ErrInvalidRequest
	}
	req.Header.Set("Authorization", "Bearer "+teambitionToken(app, time.Now()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if tenant {
		req.Header.Set("X-Tenant-Id", app.CompanyID)
		req.Header.Set("X-Tenant-Type", "organization")
	}
	client := http.Client{Transport: c.Transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrNeedsReauth
	case http.StatusForbidden:
		return ErrPersonalCapability
	case http.StatusTooManyRequests:
		return ErrPersonalRateLimited
	case http.StatusOK:
	default:
		return ErrUpstreamUnavailable
	}
	const limit = 1 << 20
	raw, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return ErrUpstreamUnavailable
	}
	var envelope struct {
		Result       json.RawMessage `json:"result"`
		ErrorMessage string          `json:"errorMessage"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ErrorMessage != "" || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return ErrUpstreamUnavailable
	}
	if json.Unmarshal(envelope.Result, result) != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}

func teambitionAuthorizeURL(appID, redirect, state string) string {
	return providerRoots["teambition"] + "/oauth/sso?" + url.Values{"app_id": {appID}, "redirect_uri": {redirect}, "state": {state}}.Encode()
}

// Verify exchanges the one-time code, verifies the authenticated subject again,
// then reads exactly that subject in the parent enterprise. Tokens never leave
// this call and are not included in the returned grant or any error.
func (c TeambitionPersonalClient) Verify(ctx context.Context, app ProviderTokenInput, code, email string) (TeambitionIdentity, error) {
	var out TeambitionIdentity
	if !app.validate("teambition") || code == "" || len(code) > 4096 || strings.ContainsAny(code, "\r\n") {
		return out, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var token struct {
		AccessToken string `json:"accessToken"`
		UserID      string `json:"userId"`
		TokenType   string `json:"tokenType"`
		Expires     int64  `json:"expires"`
	}
	if err := c.request(ctx, app, "/oauth/userAccessToken", map[string]any{"code": code, "grantType": "authorizationCode", "expires": 600}, false, &token); err != nil {
		return out, err
	}
	if !companyPattern.MatchString(token.UserID) || token.AccessToken == "" || len(token.AccessToken) > 16384 || token.TokenType != "Bearer" || token.Expires <= 0 {
		return out, ErrPersonalIdentity
	}
	var user struct {
		ID   string `json:"userId"`
		Name string `json:"name"`
	}
	err := c.request(ctx, app, "/oauth/userInfo", map[string]string{"userAccessToken": token.AccessToken}, false, &user)
	token.AccessToken = ""
	if err != nil {
		return out, err
	}
	if user.ID != token.UserID {
		return out, ErrPersonalIdentity
	}
	var members []struct {
		ID       string `json:"userId"`
		Disabled *int   `json:"isDisabled"`
		Role     *int   `json:"role"`
		Email    string `json:"email"`
		Name     string `json:"name"`
	}
	if err = c.request(ctx, app, "/org/member/batchGet", map[string]any{"userIds": []string{user.ID}}, true, &members); err != nil {
		return out, err
	}
	if len(members) != 1 {
		return out, ErrPersonalIdentity
	}
	m := members[0]
	if m.ID != user.ID || m.Disabled == nil || *m.Disabled != 0 || m.Role == nil || *m.Role < 0 || *m.Role > 2 || strings.TrimSpace(m.Email) == "" || !strings.EqualFold(strings.TrimSpace(m.Email), strings.TrimSpace(email)) {
		return out, ErrPersonalIdentity
	}
	out = TeambitionIdentity{ID: user.ID, Name: strings.TrimSpace(user.Name), Email: strings.ToLower(strings.TrimSpace(m.Email))}
	if out.Name == "" {
		out.Name = strings.TrimSpace(m.Name)
	}
	if out.Name == "" || len(out.Name) > 200 {
		out.Name = user.ID
	}
	return out, nil
}
