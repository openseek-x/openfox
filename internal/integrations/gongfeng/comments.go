package gongfeng

import (
	"context"
	"net/http"

	"openfox/internal/integrations/gitapi"
)

type Note struct {
	CommitID     string    `json:"commit_id"`
	ID           int64     `json:"id"`
	Body         string    `json:"body"`
	Author       User      `json:"author"`
	System       *bool     `json:"system"`
	CreatedAt    Timestamp `json:"created_at"`
	NoteableType string    `json:"noteable_type"`
	NoteableID   int64     `json:"noteable_id"`
	ProjectID    int64     `json:"project_id"`
}

func notePath(projectID, objectID int64, mr bool) (string, error) {
	path, err := projectPath(projectID)
	if err != nil {
		return "", err
	}
	ref, err := numericID(objectID)
	if err != nil {
		return "", err
	}
	kind := "issues"
	if mr {
		kind = "merge_requests"
	}
	return path + "/" + kind + "/" + ref + "/notes", nil
}
func (n Note) valid(projectID, objectID int64, mr bool) bool {
	if n.ID <= 0 || n.Author.ID <= 0 || n.System == nil || n.CreatedAt.IsZero() {
		return false
	}
	if n.ProjectID != 0 && n.ProjectID != projectID || n.NoteableID != 0 && n.NoteableID != objectID {
		return false
	}
	kind := "Issue"
	if mr {
		kind = "MergeRequest"
	}
	return n.NoteableType == "" || n.NoteableType == kind
}
func (c *Client) Notes(ctx context.Context, token string, projectID, objectID int64, mr bool, page int) (Page[Note], error) {
	path, err := notePath(projectID, objectID, mr)
	if err != nil {
		return Page[Note]{}, err
	}
	result, err := readPage[Note](c, ctx, path, token, nil, page, MaxPages)
	if err != nil {
		return Page[Note]{}, err
	}
	seen := map[int64]bool{}
	for _, note := range result.Items {
		if !note.valid(projectID, objectID, mr) || seen[note.ID] {
			return Page[Note]{}, ErrInvalid
		}
		seen[note.ID] = true
	}
	return result, nil
}
func (c *Client) Note(ctx context.Context, token string, projectID, objectID, noteID int64, mr bool) (Note, error) {
	path, err := notePath(projectID, objectID, mr)
	if err != nil {
		return Note{}, err
	}
	ref, err := numericID(noteID)
	if err != nil {
		return Note{}, err
	}
	_, data, err := c.do(ctx, http.MethodGet, path+"/"+ref, token, nil, nil)
	if err != nil {
		return Note{}, err
	}
	var note Note
	if err = decode(data, &note, false); err != nil {
		return Note{}, err
	}
	if note.ID != noteID || !note.valid(projectID, objectID, mr) {
		return Note{}, ErrInvalid
	}
	return note, nil
}
func (c *Client) Comment(ctx context.Context, token string, projectID, objectID int64, mr bool, body string) (Note, error) {
	path, err := notePath(projectID, objectID, mr)
	if err != nil || body == "" {
		return Note{}, ErrInvalid
	}
	_, data, err := c.do(ctx, http.MethodPost, path, token, nil, map[string]string{"body": body})
	if err != nil {
		return Note{}, err
	}
	var note Note
	if err = decode(data, &note, true); err != nil {
		return Note{}, err
	}
	if !note.valid(projectID, objectID, mr) {
		return Note{}, gitapi.ErrUnknownResult
	}
	return note, nil
}

func (c *Client) CommitNotes(ctx context.Context, token string, projectID int64, sha string, page int) (Page[Note], error) {
	if !ValidCommitSHA(sha) {
		return Page[Note]{}, ErrInvalid
	}
	path, err := projectPath(projectID)
	if err != nil {
		return Page[Note]{}, err
	}
	result, err := readPage[Note](c, ctx, path+"/repository/commits/"+sha+"/comments", token, nil, page, MaxPages)
	if err != nil {
		return Page[Note]{}, err
	}
	seen := map[int64]bool{}
	for _, note := range result.Items {
		if note.ID <= 0 || note.Author.ID <= 0 || note.System == nil || note.CreatedAt.IsZero() || note.CommitID != sha || seen[note.ID] {
			return Page[Note]{}, ErrInvalid
		}
		seen[note.ID] = true
	}
	return result, nil
}
func (c *Client) CommentCommit(ctx context.Context, token string, projectID int64, sha, body string) (Note, error) {
	if !ValidCommitSHA(sha) || body == "" {
		return Note{}, ErrInvalid
	}
	path, err := projectPath(projectID)
	if err != nil {
		return Note{}, err
	}
	_, data, err := c.do(ctx, http.MethodPost, path+"/repository/commits/"+sha+"/comments", token, nil, map[string]string{"note": body})
	if err != nil {
		return Note{}, err
	}
	var note Note
	if err = decode(data, &note, true); err != nil {
		return Note{}, err
	}
	if note.ID <= 0 || note.Author.ID <= 0 || note.System == nil || note.CreatedAt.IsZero() || note.CommitID != sha {
		return Note{}, gitapi.ErrUnknownResult
	}
	return note, nil
}
