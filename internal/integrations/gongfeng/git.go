package gongfeng

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type MergeRequest struct {
	ID              int64     `json:"id"`
	IID             int       `json:"iid"`
	ProjectID       int64     `json:"project_id"`
	TargetProjectID int64     `json:"target_project_id"`
	SourceProjectID int64     `json:"source_project_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	SourceBranch    string    `json:"source_branch"`
	State           string    `json:"state"`
	Draft           *bool     `json:"work_in_progress"`
	Author          User      `json:"author"`
	UpdatedAt       Timestamp `json:"updated_at"`
}

func (m MergeRequest) valid(projectID int64) bool {
	if m.ID <= 0 || m.IID <= 0 || m.IID > math.MaxInt32 || m.TargetProjectID != projectID || m.ProjectID != 0 && m.ProjectID != projectID || m.Draft == nil || m.UpdatedAt.IsZero() {
		return false
	}
	switch m.State {
	case "opened", "reopened", "closed", "merged":
		return true
	}
	return false
}
func (c *Client) MergeRequest(ctx context.Context, token string, projectID, mrID int64) (MergeRequest, error) {
	path, err := projectPath(projectID)
	if err != nil {
		return MergeRequest{}, err
	}
	ref, err := numericID(mrID)
	if err != nil {
		return MergeRequest{}, err
	}
	// Detail is singular; notes below that same global ID are plural.
	_, data, err := c.do(ctx, http.MethodGet, path+"/merge_request/"+ref, token, nil, nil)
	if err != nil {
		return MergeRequest{}, err
	}
	var mr MergeRequest
	if err = decode(data, &mr, false); err != nil {
		return MergeRequest{}, err
	}
	if mr.ID != mrID || !mr.valid(projectID) {
		return MergeRequest{}, ErrInvalid
	}
	return mr, nil
}
func (c *Client) MergeRequests(ctx context.Context, token string, projectID int64, updatedAfter, updatedBefore time.Time, page int) (Page[MergeRequest], error) {
	path, err := projectPath(projectID)
	if err != nil {
		return Page[MergeRequest]{}, err
	}
	// Preserve a caller-owned bounded reconciliation window across pages.
	query := url.Values{"order_by": {"updated_at"}, "sort": {"desc"}}
	for key, value := range map[string]time.Time{"updated_after": updatedAfter, "updated_before": updatedBefore} {
		if !value.IsZero() {
			query.Set(key, value.UTC().Format("2006-01-02T15:04:05-0700"))
		}
	}
	result, err := readPage[MergeRequest](c, ctx, path+"/merge_requests", token, query, page, 100)
	if err != nil {
		return Page[MergeRequest]{}, err
	}
	seen := map[int64]bool{}
	for _, mr := range result.Items {
		if !mr.valid(projectID) || seen[mr.ID] {
			return Page[MergeRequest]{}, ErrInvalid
		}
		seen[mr.ID] = true
	}
	return result, nil
}

type Commit struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	ParentIDs []string  `json:"parent_ids"`
	CreatedAt Timestamp `json:"created_at"`
}

func ValidCommitSHA(value string) bool {
	if len(value) != 40 || value == strings.Repeat("0", 40) {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (c *Client) Commit(ctx context.Context, token string, projectID int64, sha string) (Commit, error) {
	if !ValidCommitSHA(sha) {
		return Commit{}, ErrInvalid
	}
	path, err := projectPath(projectID)
	if err != nil {
		return Commit{}, err
	}
	_, data, err := c.do(ctx, http.MethodGet, path+"/repository/commits/"+sha, token, nil, nil)
	if err != nil {
		return Commit{}, err
	}
	var commit Commit
	if err = decode(data, &commit, false); err != nil {
		return Commit{}, err
	}
	if commit.ID != sha {
		return Commit{}, ErrInvalid
	}
	for _, parent := range commit.ParentIDs {
		if !ValidCommitSHA(parent) {
			return Commit{}, ErrInvalid
		}
	}
	return commit, nil
}

// CompareCommitPage is the documented full DAG set difference from..to; it
// never requests first_parent/no_merges. The worker caps the event at 100 IDs.
func (c *Client) CompareCommitPage(ctx context.Context, token string, projectID int64, from, to string, page int) (Page[Commit], error) {
	if !ValidCommitSHA(from) || !ValidCommitSHA(to) {
		return Page[Commit]{}, ErrInvalid
	}
	path, err := projectPath(projectID)
	if err != nil {
		return Page[Commit]{}, err
	}
	result, err := readPage[Commit](c, ctx, path+"/repository/compare/commits", token, url.Values{"from": {from}, "to": {to}}, page, 2)
	if err != nil {
		return Page[Commit]{}, err
	}
	seen := map[string]bool{}
	for _, commit := range result.Items {
		if !ValidCommitSHA(commit.ID) || seen[commit.ID] {
			return Page[Commit]{}, ErrInvalid
		}
		seen[commit.ID] = true
	}
	return result, nil
}
