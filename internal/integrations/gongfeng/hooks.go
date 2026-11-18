package gongfeng

import (
	"context"
	"net/http"
	"net/url"

	"openfox/internal/integrations/gitapi"
)

type Hook struct {
	ID              int64  `json:"id"`
	ProjectID       int64  `json:"project_id"`
	URL             string `json:"url"`
	Active          *bool  `json:"active"`
	Push            bool   `json:"push_events"`
	Issues          bool   `json:"issues_events"`
	MergeRequests   bool   `json:"merge_requests_events"`
	Notes           bool   `json:"note_events"`
	TagPush         bool   `json:"tag_push_events"`
	Reviews         bool   `json:"review_events"`
	Locks           bool   `json:"git_post_lock_events"`
	PreReceive      bool   `json:"git_pre_receive_events"`
	ProjectSettings bool   `json:"project_setting_events"`
}

func (h Hook) valid(projectID int64) bool {
	return h.ID > 0 && h.ProjectID == projectID && h.URL != "" && h.Active != nil
}
func (h Hook) Matches(url string) bool {
	return h.URL == url && h.Push && h.Issues && h.MergeRequests && h.Notes && !h.TagPush && !h.Reviews && !h.Locks && !h.PreReceive && !h.ProjectSettings
}
func hookPath(projectID, hookID int64) (string, error) {
	path, err := projectPath(projectID)
	if err != nil {
		return "", err
	}
	path += "/hooks"
	if hookID != 0 {
		ref, e := numericID(hookID)
		if e != nil {
			return "", e
		}
		path += "/" + ref
	}
	return path, nil
}
func (c *Client) Hooks(ctx context.Context, token string, projectID int64, page int) (Page[Hook], error) {
	path, err := hookPath(projectID, 0)
	if err != nil {
		return Page[Hook]{}, err
	}
	result, err := readPage[Hook](c, ctx, path, token, nil, page, MaxPages)
	if err != nil {
		return Page[Hook]{}, err
	}
	seen := map[int64]bool{}
	for _, hook := range result.Items {
		if !hook.valid(projectID) || seen[hook.ID] {
			return Page[Hook]{}, ErrInvalid
		}
		seen[hook.ID] = true
	}
	return result, nil
}
func (c *Client) Hook(ctx context.Context, token string, projectID, hookID int64) (Hook, error) {
	path, err := hookPath(projectID, hookID)
	if err != nil || hookID == 0 {
		return Hook{}, ErrInvalid
	}
	_, data, err := c.do(ctx, http.MethodGet, path, token, nil, nil)
	if err != nil {
		return Hook{}, err
	}
	var hook Hook
	if err = decode(data, &hook, false); err != nil {
		return Hook{}, err
	}
	if !hook.valid(projectID) || hook.ID != hookID {
		return Hook{}, ErrInvalid
	}
	return hook, nil
}
func (c *Client) SaveHook(ctx context.Context, token string, projectID, hookID int64, callback, secret string) (Hook, error) {
	path, err := hookPath(projectID, hookID)
	if err != nil {
		return Hook{}, err
	}
	parsed, err := url.Parse(callback)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !validToken(secret) {
		return Hook{}, ErrInvalid
	}
	payload := map[string]any{"url": callback, "token": secret, "push_events": true, "issues_events": true, "merge_requests_events": true, "note_events": true, "tag_push_events": false, "review_events": false, "git_post_lock_events": false, "git_pre_receive_events": false, "project_setting_events": false}
	method := http.MethodPost
	if hookID != 0 {
		method = http.MethodPut
	}
	_, data, err := c.do(ctx, method, path, token, nil, payload)
	if err != nil {
		return Hook{}, err
	}
	var hook Hook
	if err = decode(data, &hook, true); err != nil {
		return Hook{}, err
	}
	if !hook.valid(projectID) || hookID != 0 && hook.ID != hookID || !hook.Matches(callback) {
		return Hook{}, gitapi.ErrUnknownResult
	}
	return hook, nil
}
func (c *Client) ActivateHook(ctx context.Context, token string, projectID, hookID int64) (bool, error) {
	path, err := hookPath(projectID, hookID)
	if err != nil || hookID == 0 {
		return false, ErrInvalid
	}
	_, data, err := c.do(ctx, http.MethodPost, path+"/activate", token, nil, nil)
	if err != nil {
		return false, err
	}
	var active bool
	if err = decode(data, &active, true); err != nil {
		return false, err
	}
	return active, nil
}
func (c *Client) DeleteHook(ctx context.Context, token string, projectID, hookID int64) error {
	path, err := hookPath(projectID, hookID)
	if err != nil || hookID == 0 {
		return ErrInvalid
	}
	_, _, err = c.do(ctx, http.MethodDelete, path, token, nil, nil)
	return err
}
