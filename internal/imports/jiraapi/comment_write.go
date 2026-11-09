package jiraapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) CreateComment(ctx context.Context, issue, body string) (string, error) {
	if issue == "" || strings.TrimSpace(body) == "" || len(body) > 64000 {
		return "", &Error{Reason: ReasonJQLInvalid}
	}
	payload := map[string]string{"body": body}
	status, headers, data, e := c.do(ctx, http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(issue)+"/comment", nil, payload)
	if e != nil {
		return "", e
	}
	if e = statusError(status, headers, data); e != nil {
		return "", e
	}
	var out Comment
	if json.Unmarshal(data, &out) != nil || out.ID == "" || out.Body != body {
		return "", &Error{Reason: ReasonUnreachable, Status: status}
	}
	return out.ID, nil
}
