package slack

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// validateManifest checks only the dynamically created Agent/space App contract.
// The separately deployed central App has two redirect URLs and is not validated
// by CreateManifest or UpdateManifest.
func validateManifest(m Manifest) error {
	if !bounded(m.DisplayInformation.Name, 35) || strings.TrimSpace(m.DisplayInformation.Name) == "" || !bounded(m.DisplayInformation.Description, 140) || !bounded(m.Features.BotUser.DisplayName, 80) || strings.TrimSpace(m.Features.BotUser.DisplayName) == "" {
		return ErrInvalid
	}
	if m.Settings.OrgDeployEnabled || m.Settings.SocketModeEnabled || !m.Settings.TokenRotationEnabled || len(m.OAuthConfig.RedirectURLs) != 1 || !redirect(m.OAuthConfig.RedirectURLs[0]) || !m.Features.AppHome.MessagesTabEnabled || m.Features.AppHome.MessagesTabReadOnlyEnabled {
		return ErrInvalid
	}
	if len(m.OAuthConfig.Scopes.Bot) < 1 || len(m.OAuthConfig.Scopes.Bot) > 32 {
		return ErrInvalid
	}
	if _, e := scopes(strings.Join(m.OAuthConfig.Scopes.Bot, ",")); e != nil {
		return ErrInvalid
	}
	if len(m.Features.UnfurlDomains) > 1 {
		return ErrInvalid
	}
	for _, host := range m.Features.UnfurlDomains {
		u, e := url.Parse("https://" + host)
		if e != nil || u.Host != host || u.Hostname() != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return ErrInvalid
		}
	}
	if sub := m.Settings.EventSubscriptions; sub != nil {
		if !redirect(sub.RequestURL) || len(sub.BotEvents) == 0 || len(sub.BotEvents) > 16 {
			return ErrInvalid
		}
		for _, event := range sub.BotEvents {
			if !ID(event) || len(event) > 100 {
				return ErrInvalid
			}
		}
	}
	return nil
}
func manifestForm(m Manifest) (url.Values, error) {
	if e := validateManifest(m); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(m)
	if e != nil || len(raw) > 64<<10 {
		return nil, ErrInvalid
	}
	return url.Values{"manifest": {string(raw)}}, nil
}
func (c *Client) CreateManifest(ctx context.Context, token string, m Manifest) (CreatedApp, error) {
	form, e := manifestForm(m)
	if e != nil {
		return CreatedApp{}, e
	}
	var out struct {
		AppID       string `json:"app_id"`
		Credentials struct {
			ClientID      string `json:"client_id"`
			ClientSecret  string `json:"client_secret"`
			SigningSecret string `json:"signing_secret"`
		} `json:"credentials"`
	}
	e = c.api(ctx, "POST", "apps.manifest.create", token, nil, form, nil, true, &out)
	if e != nil {
		return CreatedApp{}, e
	}
	if !ID(out.AppID) || !ID(out.Credentials.ClientID) || !secret(out.Credentials.ClientSecret) || !secret(out.Credentials.SigningSecret) {
		return CreatedApp{}, incomplete(true)
	}
	return CreatedApp{AppID: out.AppID, Credentials: AppCredentials{ClientID: out.Credentials.ClientID, ClientSecret: out.Credentials.ClientSecret}, SigningSecret: out.Credentials.SigningSecret}, nil
}
func (c *Client) UpdateManifest(ctx context.Context, token, appID string, m Manifest) error {
	if !ID(appID) {
		return ErrInvalid
	}
	form, e := manifestForm(m)
	if e != nil {
		return e
	}
	form.Set("app_id", appID)
	var out struct {
		AppID string `json:"app_id"`
	}
	if e = c.api(ctx, "POST", "apps.manifest.update", token, nil, form, nil, true, &out); e != nil {
		return e
	}
	if out.AppID != appID {
		return incomplete(true)
	}
	return nil
}
func (c *Client) ExportManifest(ctx context.Context, token, appID string) (json.RawMessage, error) {
	if !ID(appID) {
		return nil, ErrInvalid
	}
	var out struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if e := c.api(ctx, "POST", "apps.manifest.export", token, nil, url.Values{"app_id": {appID}}, nil, false, &out); e != nil {
		return nil, e
	}
	if !jsonObject(out.Manifest) || len(out.Manifest) > 64<<10 {
		return nil, ErrIncomplete
	}
	return out.Manifest, nil
}
