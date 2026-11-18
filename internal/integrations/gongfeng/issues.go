package gongfeng

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"openfox/internal/integrations/gitapi"
)

// Official v3 timestamps include numeric offsets without a colon.
type Timestamp struct{ time.Time }

func (t *Timestamp) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var raw string
	if json.Unmarshal(data, &raw) != nil {
		return ErrInvalid
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700"} {
		if value, err := time.Parse(layout, raw); err == nil {
			t.Time = value
			return nil
		}
	}
	return ErrInvalid
}

type Issue struct {
	ID           int64     `json:"id"`
	IID          int       `json:"iid"`
	ProjectID    int64     `json:"project_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	State        string    `json:"state"`
	Confidential *bool     `json:"confidential"`
	Author       User      `json:"author"`
	Assignees    []User    `json:"assignees"`
	CreatedAt    Timestamp `json:"created_at"`
	UpdatedAt    Timestamp `json:"updated_at"`
}

func (i Issue) Valid(project int64) bool {
	return i.ID > 0 && i.IID > 0 && i.IID <= math.MaxInt32 && i.ProjectID == project && (i.State == "opened" || i.State == "closed") && i.Confidential != nil && i.Author.ID > 0 && !i.CreatedAt.IsZero() && !i.UpdatedAt.IsZero()
}
func (i Issue) PublicIssue(project Project) (gitapi.Issue, error) {
	if !i.Valid(project.ID) || !validProjectPath(project.Path) {
		return gitapi.Issue{}, ErrInvalid
	}
	if *i.Confidential {
		return gitapi.Issue{}, ErrConfidential
	}
	state := "open"
	if i.State == "closed" {
		state = "closed"
	}
	result := gitapi.Issue{ID: i.ID, Number: i.IID, Ref: strconv.FormatInt(i.ID, 10), Title: i.Title, Body: i.Description, State: state, HTMLURL: Origin + "/" + project.Path + "/issues/" + strconv.Itoa(i.IID), User: gitapi.IssueUser{ID: i.Author.ID, Login: i.Author.Username}, CreatedAt: i.CreatedAt.Time, UpdatedAt: i.UpdatedAt.Time}
	result.AssigneesReadOnly = true
	result.AssigneesMissing = i.Assignees == nil
	for _, user := range i.Assignees {
		if user.ID <= 0 {
			return gitapi.Issue{}, ErrInvalid
		}
		result.Assignees = append(result.Assignees, gitapi.IssueUser{ID: user.ID, Login: user.Username})
	}
	return result, nil
}

type IssueQuery struct {
	State                                                    string
	CreatedAfter, CreatedBefore, UpdatedAfter, UpdatedBefore time.Time
}

func (q IssueQuery) values() (url.Values, error) {
	if q.State != "opened" && q.State != "closed" {
		return nil, ErrInvalid
	}
	values := url.Values{"state": {q.State}, "order_by": {"updated_at"}, "sort": {"asc"}}
	for key, t := range map[string]time.Time{"created_after": q.CreatedAfter, "created_before": q.CreatedBefore, "updated_after": q.UpdatedAfter, "updated_before": q.UpdatedBefore} {
		if !t.IsZero() {
			values.Set(key, t.UTC().Format("2006-01-02T15:04:05-0700"))
		}
	}
	return values, nil
}
func (c *Client) Issues(ctx context.Context, token string, projectID int64, q IssueQuery, page int) (Page[Issue], error) {
	path, err := projectPath(projectID)
	if err != nil {
		return Page[Issue]{}, err
	}
	values, err := q.values()
	if err != nil {
		return Page[Issue]{}, err
	}
	result, err := readPage[Issue](c, ctx, path+"/issues", token, values, page, MaxPages)
	if err != nil {
		return Page[Issue]{}, err
	}
	seen := map[int64]bool{}
	for _, issue := range result.Items {
		if !issue.Valid(projectID) || seen[issue.ID] {
			return Page[Issue]{}, ErrInvalid
		}
		seen[issue.ID] = true
	}
	return result, nil
}
func (c *Client) Issue(ctx context.Context, token string, projectID, id int64) (Issue, error) {
	path, err := projectPath(projectID)
	if err != nil {
		return Issue{}, err
	}
	ref, err := numericID(id)
	if err != nil {
		return Issue{}, err
	}
	return c.issueRequest(ctx, http.MethodGet, path+"/issues/"+ref, token, projectID, id, nil)
}
func (c *Client) issueRequest(ctx context.Context, method, path, token string, projectID, id int64, payload any) (Issue, error) {
	var issue Issue
	_, data, err := c.do(ctx, method, path, token, nil, payload)
	if err != nil {
		return issue, err
	}
	mutation := method != http.MethodGet
	if err = decode(data, &issue, mutation); err != nil {
		return Issue{}, err
	}
	if !issue.Valid(projectID) || id > 0 && issue.ID != id {
		if mutation {
			return Issue{}, gitapi.ErrUnknownResult
		}
		return Issue{}, ErrInvalid
	}
	return issue, nil
}
func (c *Client) Create(ctx context.Context, token string, projectID int64, title, body string) (Issue, error) {
	path, err := projectPath(projectID)
	if err != nil || title == "" {
		return Issue{}, ErrInvalid
	}
	// Unsupported fields are absent; an empty assignee list would clear users.
	return c.issueRequest(ctx, http.MethodPost, path+"/issues", token, projectID, 0, map[string]string{"title": title, "description": body})
}
func (c *Client) Update(ctx context.Context, token string, projectID, id int64, update gitapi.IssueUpdate) (Issue, error) {
	path, err := projectPath(projectID)
	if err != nil {
		return Issue{}, err
	}
	ref, err := numericID(id)
	if err != nil {
		return Issue{}, err
	}
	state := ""
	switch update.State {
	case "open":
		state = "reopen"
	case "closed":
		state = "close"
	default:
		return Issue{}, ErrInvalid
	}
	return c.issueRequest(ctx, http.MethodPut, path+"/issues/"+ref, token, projectID, id, map[string]string{"title": update.Title, "description": update.Body, "state_event": state})
}
