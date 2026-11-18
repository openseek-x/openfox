package bitbucket

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const MaxWebhookBytes = 5 << 20

// WebhookEvents is the manual-hook allowlist. No Issue, clone, merge or approval
// operations are enabled by this integration.
var WebhookEvents = []string{
	"pullrequest:created", "pullrequest:updated", "pullrequest:fulfilled", "pullrequest:rejected",
	"pullrequest:comment_created", "pullrequest:comment_updated", "pullrequest:comment_deleted", "repo:push",
}

func VerifySignature(secret string, body []byte, signature string) bool {
	if len(secret) < 32 || len(secret) > 512 || len(body) > MaxWebhookBytes || !strings.HasPrefix(signature, "sha256=") || len(signature) != 7+64 {
		return false
	}
	provided, err := hex.DecodeString(signature[7:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

type PushRef struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Target struct {
		Hash string `json:"hash"`
	} `json:"target"`
}
type PushChange struct {
	Old     *PushRef `json:"old"`
	New     *PushRef `json:"new"`
	Created bool     `json:"created"`
	Closed  bool     `json:"closed"`
	Forced  bool     `json:"forced"`
}
type Event struct {
	Kind           string       `json:"kind"`
	WorkspaceID    string       `json:"workspace_id"`
	RepositoryID   string       `json:"repository_id"`
	PullRequestID  int64        `json:"pull_request_id,omitempty"`
	CommentID      int64        `json:"comment_id,omitempty"`
	CommentDeleted bool         `json:"comment_deleted,omitempty"`
	Changes        []PushChange `json:"changes,omitempty"`
}

// ParseEvent keeps only routing facts. User content, actor and supplied commit
// lists are not trusted resource snapshots; the worker hydrates with REST.
func ParseEvent(key string, body []byte) (Event, error) {
	var out Event
	known := false
	for _, v := range WebhookEvents {
		if key == v {
			known = true
			break
		}
	}
	if !known || len(body) == 0 || len(body) > MaxWebhookBytes {
		return out, ErrInvalid
	}
	var payload struct {
		Repository struct {
			UUID      string    `json:"uuid"`
			Workspace Workspace `json:"workspace"`
		} `json:"repository"`
		PullRequest struct {
			ID int64 `json:"id"`
		} `json:"pullrequest"`
		Comment struct {
			ID int64 `json:"id"`
		} `json:"comment"`
		Push struct {
			Changes []PushChange `json:"changes"`
		} `json:"push"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return out, ErrInvalid
	}
	var err error
	out.WorkspaceID, err = UUID(payload.Repository.Workspace.UUID)
	if err != nil {
		return Event{}, err
	}
	out.RepositoryID, err = UUID(payload.Repository.UUID)
	if err != nil {
		return Event{}, err
	}
	if key == "repo:push" {
		if len(payload.Push.Changes) == 0 || len(payload.Push.Changes) > 10 {
			return Event{}, ErrIncomplete
		}
		out.Kind = "PUSH"
		out.Changes = payload.Push.Changes
		for _, change := range out.Changes {
			if validatePushChange(change) != nil {
				return Event{}, ErrInvalid
			}
		}
		return out, nil
	}
	if payload.PullRequest.ID < 1 {
		return Event{}, ErrInvalid
	}
	out.PullRequestID = payload.PullRequest.ID
	out.Kind = "PULL_REQUEST"
	if strings.HasPrefix(key, "pullrequest:comment_") {
		if payload.Comment.ID < 1 {
			return Event{}, ErrInvalid
		}
		out.Kind = "COMMENT"
		out.CommentID = payload.Comment.ID
		out.CommentDeleted = key == "pullrequest:comment_deleted"
	}
	return out, nil
}
func validatePushChange(change PushChange) error {
	if change.Old == nil && change.New == nil || change.Created != (change.Old == nil) || change.Closed != (change.New == nil) {
		return ErrInvalid
	}
	for _, ref := range []*PushRef{change.Old, change.New} {
		if ref == nil {
			continue
		}
		if ref.Type != "branch" && ref.Type != "tag" || ref.Name == "" || len(ref.Name) > 1024 || strings.ContainsAny(ref.Name, "\x00\r\n") || !ValidSHA(ref.Target.Hash) {
			return ErrInvalid
		}
	}
	if change.Old != nil && change.New != nil && (change.Old.Type != change.New.Type || change.Old.Name != change.New.Name) {
		return ErrInvalid
	}
	return nil
}

func (c *Client) PushCommits(ctx context.Context, token string, event Event) ([]Commit, error) {
	if event.Kind != "PUSH" || len(event.Changes) == 0 || len(event.Changes) > 10 {
		return nil, ErrInvalid
	}
	ctx, cancel := WithTraversalBudget(ctx)
	defer cancel()
	seen := map[string]bool{}
	out := []Commit{}
	for _, change := range event.Changes {
		if validatePushChange(change) != nil {
			return nil, ErrInvalid
		}
		if change.New == nil || change.New.Type != "branch" {
			continue
		}
		var commits []Commit
		var err error
		if change.Old == nil {
			// Bitbucket's new-branch payload can include pre-existing commits.
			// Only the verified tip is eligible; never traverse repository history.
			if seen[change.New.Target.Hash] {
				continue
			}
			var tip Commit
			tip, err = c.Commit(ctx, token, event.WorkspaceID, event.RepositoryID, change.New.Target.Hash)
			commits = []Commit{tip}
		} else {
			commits, err = c.CommitDifference(ctx, token, event.WorkspaceID, event.RepositoryID, change.Old.Target.Hash, change.New.Target.Hash)
		}
		if err != nil {
			return nil, err
		}
		for _, commit := range commits {
			if seen[commit.Hash] {
				continue
			}
			seen[commit.Hash] = true
			out = append(out, commit)
			if len(out) > MaxCommits {
				return nil, ErrCommitLimit
			}
		}
	}
	return out, nil
}
