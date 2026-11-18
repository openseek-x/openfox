package supervisor

import (
	"context"
	"net/http"

	"openfox/internal/browser"
)

func (c *Client) ClaimBrowser(ctx context.Context, org, id, secret string, identity browser.Identity) (browser.Binding, error) {
	var result browser.Binding
	input := struct {
		browser.Identity
		ClaimSecret string `json:"claim_secret"`
	}{identity, secret}
	err := c.do(ctx, http.MethodPost, c.devicePath(org, "browser-bindings", id, "claim"), input, &result)
	return result, err
}
func (c *Client) HeartbeatBrowser(ctx context.Context, org, id string, identity browser.Identity) (browser.Binding, error) {
	var result browser.Binding
	err := c.do(ctx, http.MethodPost, c.devicePath(org, "browser-bindings", id, "heartbeat"), identity, &result)
	return result, err
}
func (c *Client) AuthorizeBrowser(ctx context.Context, org, run string, input browser.AuthorizationInput) (browser.Authorization, error) {
	var result browser.Authorization
	err := c.do(ctx, http.MethodPost, c.devicePath(org, "runs", run, "browser-authorize"), input, &result)
	return result, err
}
