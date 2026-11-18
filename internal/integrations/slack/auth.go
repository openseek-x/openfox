package slack

import (
	"context"
	"net/url"
	"strings"
	"time"
)

func AuthorizeURL(clientID, redirectURI, state, team string, botScopes []string) (string, error) {
	if !ID(clientID) || !redirect(redirectURI) || !secret(state) || len(state) < 16 || team != "" && !ID(team) || len(botScopes) == 0 || len(botScopes) > 32 {
		return "", ErrInvalid
	}
	scopes, err := scopes(strings.Join(botScopes, ","))
	if err != nil || len(scopes) != len(botScopes) {
		return "", ErrInvalid
	}
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirectURI}, "state": {state}, "scope": {strings.Join(scopes, ",")}}
	if team != "" {
		q.Set("team", team)
	}
	return Origin + "/oauth/v2/authorize?" + q.Encode(), nil
}
func OIDCAuthorizeURL(clientID, redirectURI, state, nonce, team string) (string, error) {
	if !ID(clientID) || !redirect(redirectURI) || !secret(state) || len(state) < 16 || !secret(nonce) || len(nonce) < 16 || team != "" && !ID(team) {
		return "", ErrInvalid
	}
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "scope": {"openid profile"}, "state": {state}, "nonce": {nonce}}
	if team != "" {
		q.Set("team", team)
	}
	return Origin + "/openid/connect/authorize?" + q.Encode(), nil
}

type oauthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	AppID        string `json:"app_id"`
	BotUserID    string `json:"bot_user_id"`
	Team         struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	AuthedUser struct {
		ID           string `json:"id"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	} `json:"authed_user"`
	EnterpriseInstall bool `json:"is_enterprise_install"`
}

func (c *Client) oauth(ctx context.Context, app AppCredentials, form url.Values) (oauthResponse, error) {
	var out oauthResponse
	e := c.api(ctx, "POST", "oauth.v2.access", "", &app, form, nil, true, &out)
	if e != nil {
		return out, e
	}
	if !secret(out.AccessToken) || !secret(out.RefreshToken) || out.ExpiresIn <= 0 || out.ExpiresIn > 86400 || out.TokenType != "bot" {
		return out, incomplete(true)
	}
	if out.AuthedUser.AccessToken != "" || out.AuthedUser.RefreshToken != "" {
		return out, failure(ErrUnsupported, "user_token_not_supported")
	}
	if out.EnterpriseInstall {
		return out, failure(ErrUnsupported, "enterprise_install_not_supported")
	}
	return out, nil
}
func (c *Client) grant(out oauthResponse) (TokenGrant, error) {
	scope, e := scopes(out.Scope)
	if e != nil {
		return TokenGrant{}, incomplete(true)
	}
	return TokenGrant{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresAt: c.now().Add(time.Duration(out.ExpiresIn) * time.Second), TokenType: out.TokenType, Scope: scope, AppID: out.AppID, TeamID: out.Team.ID, BotUserID: out.BotUserID}, nil
}
func (c *Client) ExchangeOAuth(ctx context.Context, app AppCredentials, code, redirectURI string) (InstallationToken, error) {
	if !secret(code) || !redirect(redirectURI) {
		return InstallationToken{}, ErrInvalid
	}
	out, e := c.oauth(ctx, app, url.Values{"code": {code}, "redirect_uri": {redirectURI}})
	if e != nil {
		return InstallationToken{}, e
	}
	if !ID(out.AppID) || !ID(out.Team.ID) || !ID(out.BotUserID) || !ID(out.AuthedUser.ID) || !bounded(out.Team.Name, 255) {
		return InstallationToken{}, incomplete(true)
	}
	grant, e := c.grant(out)
	if e != nil {
		return InstallationToken{}, e
	}
	return InstallationToken{TokenGrant: grant, InstallerUserID: out.AuthedUser.ID, TeamName: out.Team.Name, EnterpriseInstall: out.EnterpriseInstall}, nil
}
func (c *Client) RefreshOAuth(ctx context.Context, app AppCredentials, refresh string) (TokenGrant, error) {
	if !secret(refresh) {
		return TokenGrant{}, ErrInvalid
	}
	out, e := c.oauth(ctx, app, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if e != nil {
		return TokenGrant{}, e
	}
	for _, v := range []string{out.AppID, out.Team.ID, out.BotUserID} {
		if v != "" && !ID(v) {
			return TokenGrant{}, incomplete(true)
		}
	}
	return c.grant(out)
}
func (c *Client) RotateConfiguration(ctx context.Context, refresh string) (ConfigToken, error) {
	if !secret(refresh) {
		return ConfigToken{}, ErrInvalid
	}
	var out struct {
		Token   string `json:"token"`
		Refresh string `json:"refresh_token"`
		Team    string `json:"team_id"`
		User    string `json:"user_id"`
		IAT     int64  `json:"iat"`
		EXP     int64  `json:"exp"`
	}
	e := c.api(ctx, "POST", "tooling.tokens.rotate", "", nil, url.Values{"refresh_token": {refresh}}, nil, true, &out)
	if e != nil {
		return ConfigToken{}, e
	}
	now := c.now().Unix()
	if !secret(out.Token) || !secret(out.Refresh) || !ID(out.Team) || !ID(out.User) || out.IAT <= 0 || out.IAT > now+60 || out.EXP <= now || out.EXP <= out.IAT || out.EXP-out.IAT > 86400 {
		return ConfigToken{}, incomplete(true)
	}
	return ConfigToken{Token: out.Token, RefreshToken: out.Refresh, TeamID: out.Team, UserID: out.User, IssuedAt: time.Unix(out.IAT, 0).UTC(), ExpiresAt: time.Unix(out.EXP, 0).UTC()}, nil
}
func (c *Client) AuthTest(ctx context.Context, token string) (AuthIdentity, error) {
	var out AuthIdentity
	e := c.api(ctx, "POST", "auth.test", token, nil, url.Values{}, nil, false, &out)
	if e != nil {
		return out, e
	}
	if !ID(out.TeamID) || !ID(out.UserID) || out.BotID != "" && !ID(out.BotID) {
		return AuthIdentity{}, ErrIncomplete
	}
	return out, nil
}
