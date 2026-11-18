package codecommit

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func validEpoch(v float64) bool {
	return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) && v < 253402300800
}
func validNext(in, out string) bool { return validCursor(out) && (out == "" || out != in) }
func (c *Client) GetRepository(ctx context.Context, creds Credentials, account, name string) (Repository, error) {
	if !ValidAccount(account) || !ValidRepositoryName(name) {
		return Repository{}, ErrInvalid
	}
	var out struct {
		Repository *Repository `json:"repositoryMetadata"`
	}
	if e := c.json(ctx, creds, "GetRepository", map[string]string{"repositoryName": name}, &out, false); e != nil {
		return Repository{}, e
	}
	if out.Repository == nil {
		return Repository{}, ErrIncomplete
	}
	r := *out.Repository
	if r.AccountID != account || r.Name != name || r.ARN != "arn:aws:codecommit:"+c.region+":"+account+":"+name || !ValidRepositoryID(r.ID) {
		return Repository{}, ErrIdentityMismatch
	}
	if r.DefaultBranch != "" && !validBranch(r.DefaultBranch) {
		return Repository{}, ErrIncomplete
	}
	return r, nil
}
func (c *Client) ListRepositories(ctx context.Context, creds Credentials, next string) (RepositoryPage, error) {
	if !validCursor(next) {
		return RepositoryPage{}, ErrInvalid
	}
	var out RepositoryPage
	in := map[string]string{}
	if next != "" {
		in["nextToken"] = next
	}
	if e := c.json(ctx, creds, "ListRepositories", in, &out, false); e != nil {
		return RepositoryPage{}, e
	}
	if out.Repositories == nil || len(out.Repositories) > 1000 || !validNext(next, out.NextToken) {
		return RepositoryPage{}, ErrIncomplete
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, r := range out.Repositories {
		if !ValidRepositoryID(r.ID) || !ValidRepositoryName(r.Name) || seen[r.ID] || names[r.Name] {
			return RepositoryPage{}, ErrIncomplete
		}
		seen[r.ID], names[r.Name] = true, true
	}
	return out, nil
}
func (c *Client) ListPullRequests(ctx context.Context, creds Credentials, repo, status, next string) (PullRequestPage, error) {
	if !ValidRepositoryName(repo) || (status != "OPEN" && status != "CLOSED") || !validCursor(next) {
		return PullRequestPage{}, ErrInvalid
	}
	var out PullRequestPage
	in := map[string]any{"repositoryName": repo, "pullRequestStatus": status, "maxResults": 100}
	if next != "" {
		in["nextToken"] = next
	}
	if e := c.json(ctx, creds, "ListPullRequests", in, &out, false); e != nil {
		return PullRequestPage{}, e
	}
	if out.IDs == nil || len(out.IDs) > 100 || !validNext(next, out.NextToken) {
		return PullRequestPage{}, ErrIncomplete
	}
	seen := map[string]bool{}
	for _, id := range out.IDs {
		if _, err := PullRequestNumber(id); err != nil || seen[id] {
			return PullRequestPage{}, ErrIncomplete
		}
		seen[id] = true
	}
	return out, nil
}
func (c *Client) GetPullRequest(ctx context.Context, creds Credentials, repo, id string) (PullRequest, error) {
	if !ValidRepositoryName(repo) {
		return PullRequest{}, ErrInvalid
	}
	if _, err := PullRequestNumber(id); err != nil {
		return PullRequest{}, err
	}
	var out struct {
		PullRequest *PullRequest `json:"pullRequest"`
	}
	if e := c.json(ctx, creds, "GetPullRequest", map[string]string{"pullRequestId": id}, &out, false); e != nil {
		return PullRequest{}, e
	}
	if out.PullRequest == nil {
		return PullRequest{}, ErrIncomplete
	}
	p := *out.PullRequest
	if p.ID != id || p.Title == "" || len(p.Title) > 4096 || len(p.Description) > 256<<10 || !opaque(p.RevisionID, 1024) || !validEpoch(p.CreationDate) || !validEpoch(p.LastActivityDate) || !validPrincipal(p.AuthorARN, "") || (p.Status != "OPEN" && p.Status != "CLOSED") || len(p.Targets) != 1 {
		return PullRequest{}, ErrIncomplete
	}
	target := p.Targets[0]
	if target.RepositoryName != repo || !ValidSHA(target.SourceCommit) || !ValidSHA(target.DestinationCommit) || !validBranch(target.SourceReference) || !validBranch(target.DestinationReference) || (target.MergeMetadata.MergeCommitID != "" && !ValidSHA(target.MergeMetadata.MergeCommitID)) {
		return PullRequest{}, ErrIdentityMismatch
	}
	return p, nil
}

// CreationEvidence scans all filtered pages before accepting exactly one
// creation proof. Current destination tip or merge base is never substituted.
func (c *Client) CreationEvidence(ctx context.Context, creds Credentials, repo, id string) (CreatedEvidence, error) {
	if !ValidRepositoryName(repo) {
		return CreatedEvidence{}, ErrInvalid
	}
	if _, err := PullRequestNumber(id); err != nil {
		return CreatedEvidence{}, err
	}
	ctx, cancel := WithReadBudget(ctx)
	defer cancel()
	next := ""
	seen := map[string]bool{}
	var found *CreatedEvidence
	count := 0
	for page := 0; page < 20; page++ {
		var out struct {
			NextToken string `json:"nextToken"`
			Events    []struct {
				ID      string  `json:"pullRequestId"`
				Type    string  `json:"pullRequestEventType"`
				Date    float64 `json:"eventDate"`
				Created *struct {
					Repository  string `json:"repositoryName"`
					Destination string `json:"destinationCommitId"`
					Source      string `json:"sourceCommitId"`
				} `json:"pullRequestCreatedEventMetadata"`
			} `json:"pullRequestEvents"`
		}
		in := map[string]any{"pullRequestId": id, "pullRequestEventType": "PULL_REQUEST_CREATED", "maxResults": 100}
		if next != "" {
			in["nextToken"] = next
		}
		if e := c.json(ctx, creds, "DescribePullRequestEvents", in, &out, false); e != nil {
			return CreatedEvidence{}, e
		}
		count += len(out.Events)
		if out.Events == nil || len(out.Events) > 100 || count > 2000 || !validNext(next, out.NextToken) {
			return CreatedEvidence{}, ErrIncomplete
		}
		for _, e := range out.Events {
			if e.ID != id || e.Type != "PULL_REQUEST_CREATED" || e.Created == nil || e.Created.Repository != repo || !ValidSHA(e.Created.Destination) || !ValidSHA(e.Created.Source) || !validEpoch(e.Date) || found != nil {
				return CreatedEvidence{}, ErrIncomplete
			}
			found = &CreatedEvidence{repo, id, e.Created.Destination, e.Created.Source, e.Date}
		}
		if out.NextToken == "" {
			if found == nil {
				return CreatedEvidence{}, ErrIncomplete
			}
			return *found, nil
		}
		if seen[out.NextToken] {
			return CreatedEvidence{}, ErrIncomplete
		}
		seen[out.NextToken] = true
		next = out.NextToken
	}
	return CreatedEvidence{}, ErrIncomplete
}
func (c *Client) ListBranches(ctx context.Context, creds Credentials, repo, next string) (BranchPage, error) {
	if !ValidRepositoryName(repo) || !validCursor(next) {
		return BranchPage{}, ErrInvalid
	}
	var out BranchPage
	in := map[string]string{"repositoryName": repo}
	if next != "" {
		in["nextToken"] = next
	}
	if e := c.json(ctx, creds, "ListBranches", in, &out, false); e != nil {
		return BranchPage{}, e
	}
	if out.Branches == nil || len(out.Branches) > 1000 || !validNext(next, out.NextToken) {
		return BranchPage{}, ErrIncomplete
	}
	seen := map[string]bool{}
	for _, b := range out.Branches {
		if !validBranch(b) || seen[b] {
			return BranchPage{}, ErrIncomplete
		}
		seen[b] = true
	}
	return out, nil
}
func (c *Client) GetBranch(ctx context.Context, creds Credentials, repo, name string) (Branch, error) {
	if !ValidRepositoryName(repo) || !validBranch(name) {
		return Branch{}, ErrInvalid
	}
	var out struct {
		Branch *Branch `json:"branch"`
	}
	if e := c.json(ctx, creds, "GetBranch", map[string]string{"repositoryName": repo, "branchName": name}, &out, false); e != nil {
		return Branch{}, e
	}
	if out.Branch == nil || out.Branch.Name != name || !ValidSHA(out.Branch.CommitID) {
		return Branch{}, ErrIncomplete
	}
	return *out.Branch, nil
}
func validCommit(c Commit) bool {
	if !ValidSHA(c.ID) || !ValidSHA(c.TreeID) || c.Parents == nil || len(c.Parents) > MaxGraphObjects || len(c.Message) > 4<<20 || len(c.Author.Name) > 8192 || len(c.Author.Email) > 8192 || len(c.Committer.Name) > 8192 || len(c.Committer.Email) > 8192 || len(c.Author.Date) > 128 || len(c.Committer.Date) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range c.Parents {
		if !ValidSHA(p) || p == c.ID || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}
func (c *Client) GetCommit(ctx context.Context, creds Credentials, repo, id string) (Commit, error) {
	if !ValidRepositoryName(repo) || !ValidSHA(id) {
		return Commit{}, ErrInvalid
	}
	var out struct {
		Commit *Commit `json:"commit"`
	}
	if e := c.json(ctx, creds, "GetCommit", map[string]string{"repositoryName": repo, "commitId": id}, &out, false); e != nil {
		return Commit{}, e
	}
	if out.Commit == nil || out.Commit.ID != id || !validCommit(*out.Commit) {
		return Commit{}, ErrIncomplete
	}
	return *out.Commit, nil
}

// BatchGetCommits rejects every per-item error, omission, duplicate and unasked
// object. An unsuccessful batch never returns a usable partial result.
func (c *Client) BatchGetCommits(ctx context.Context, creds Credentials, repo string, ids []string) ([]Commit, error) {
	if !ValidRepositoryName(repo) || len(ids) == 0 || len(ids) > 100 {
		return nil, ErrInvalid
	}
	asked := map[string]bool{}
	for _, id := range ids {
		if !ValidSHA(id) || asked[id] {
			return nil, ErrInvalid
		}
		asked[id] = true
	}
	var out struct {
		Commits []Commit          `json:"commits"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if e := c.json(ctx, creds, "BatchGetCommits", map[string]any{"repositoryName": repo, "commitIds": ids}, &out, false); e != nil {
		return nil, e
	}
	if out.Commits == nil || len(out.Commits) != len(ids) || len(out.Errors) > 0 {
		return nil, ErrIncomplete
	}
	seen := map[string]bool{}
	for _, item := range out.Commits {
		if !asked[item.ID] || seen[item.ID] || !validCommit(item) {
			return nil, ErrIncomplete
		}
		seen[item.ID] = true
	}
	return out.Commits, nil
}
func (c *Client) PostCommentForPullRequest(ctx context.Context, creds Credentials, intent CommentIntent) (CommentResult, error) {
	if _, err := validatedIntent(intent.request); err != nil {
		return CommentResult{}, err
	}
	r := intent.request
	var out struct {
		Repository string `json:"repositoryName"`
		ID         string `json:"pullRequestId"`
		Before     string `json:"beforeCommitId"`
		After      string `json:"afterCommitId"`
		Comment    *struct {
			ID      string  `json:"commentId"`
			Author  string  `json:"authorArn"`
			Token   string  `json:"clientRequestToken"`
			Content string  `json:"content"`
			Created float64 `json:"creationDate"`
			Deleted bool    `json:"deleted"`
		} `json:"comment"`
	}
	if err := c.json(ctx, creds, "PostCommentForPullRequest", r, &out, true); err != nil {
		return CommentResult{}, err
	}
	if out.Repository != r.RepositoryName || out.ID != r.PullRequestID || out.Before != r.BeforeCommitID || out.After != r.AfterCommitID || out.Comment == nil {
		return CommentResult{}, ErrUnknownResult
	}
	x := out.Comment
	// Comment IDs are opaque; official events include UUID:UUID (73 characters).
	if !opaque(x.ID, 1024) || !validPrincipal(x.Author, "") || x.Deleted || x.Content != r.Content || !validEpoch(x.Created) || (x.Token != "" && x.Token != r.ClientRequestToken) {
		return CommentResult{}, ErrUnknownResult
	}
	return CommentResult{x.ID, x.Author, x.Created}, nil
}

// CommitTime parses CodeCommit's git identity date (Unix seconds and offset).
// The offset describes the author timezone; Unix seconds already identify UTC.
func CommitTime(date string) (time.Time, error) {
	parts := strings.Fields(date)
	if len(parts) != 2 || !regexp.MustCompile(`^[+-][0-9]{4}$`).MatchString(parts[1]) {
		return time.Time{}, ErrInvalid
	}
	seconds, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || seconds <= 0 || seconds >= 253402300800 {
		return time.Time{}, ErrInvalid
	}
	return time.Unix(seconds, 0).UTC(), nil
}
