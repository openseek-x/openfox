package artifactreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

// A source is immutable; discussion revisions never change its coordinates.
type CommentSource struct {
	Name    string                        `json:"name"`
	File    *Input                        `json:"file,omitempty"`
	Browser *conversations.BrowserContext `json:"browser,omitempty"`
}
type CommentPost struct {
	ID        string     `json:"id"`
	AuthorID  string     `json:"author_id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	EditedAt  *time.Time `json:"edited_at,omitempty"`
}
type CommentThread struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversation_id"`
	Version        int64         `json:"version"`
	Status         string        `json:"status"`
	Source         CommentSource `json:"source"`
	Comments       []CommentPost `json:"comments"`
	UpdatedAt      time.Time     `json:"updated_at"`
}
type CommentMutation struct {
	OperationID string `json:"operation_id"`
	Version     int64  `json:"version"`
	Action      string `json:"action"`
	CommentID   string `json:"comment_id,omitempty"`
	Body        string `json:"body,omitempty"`
}
type CommentPage struct {
	Threads    []CommentThread `json:"threads"`
	NextCursor string          `json:"next_cursor"`
}

func NormalizeCommentSource(s CommentSource) (CommentSource, error) {
	if len(s.Name) > 1024 || !ValidText(s.Name) || (s.File == nil) == (s.Browser == nil) {
		return s, ErrInvalid
	}
	if s.File != nil {
		if err := ValidateInputs([]Input{*s.File}); err != nil {
			return s, err
		}
		if len(s.File.Comments) != 1 || s.File.Comments[0].Discussion != nil {
			return s, ErrInvalid
		}
	} else {
		if len(s.Browser.Comments) != 1 || s.Browser.Comments[0].Discussion != nil {
			return s, ErrInvalid
		}
		image := s.Browser.Comments[0].ScreenshotAttachmentID
		normalized, err := conversations.NormalizeBrowserContexts([]conversations.BrowserContext{*s.Browser}, []string{image})
		if err != nil {
			return s, err
		}
		s.Browser = &normalized[0]
	}
	return s, nil
}
func commentHash(conversation, user string, input any) string {
	raw, _ := json.Marshal(input)
	return Digest(conversation + "\x00" + user + "\x00" + string(raw))
}
func (s Service) CommentAccess(ctx context.Context, tx pgx.Tx, org string, t CommentThread, actor conversations.Actor) error {
	if _, err := (conversations.Service{}).Get(ctx, tx, org, t.ConversationID, actor); err != nil {
		return err
	}
	return commentSourceAccess(ctx, tx, org, t, actor)
}
func commentSourceAccess(ctx context.Context, tx pgx.Tx, org string, t CommentThread, actor conversations.Actor) error {
	if f := t.Source.File; f != nil && f.NodeID != "" {
		copy := *f
		copy.ConversationID = t.ConversationID
		_, err := nativeAccess(ctx, tx, org, copy, actor.UserID, false)
		return err
	}
	return nil
}
func (s Service) CommentFile(ctx context.Context, tx pgx.Tx, org, conversation string, actor conversations.Actor, input Input) (File, error) {
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conversation, actor); err != nil {
		return File{}, err
	}
	input.ConversationID = conversation
	if input.NodeID != "" {
		return s.nativeFile(ctx, tx, org, input, actor.UserID, false)
	}
	f, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM openfox.artifacts a JOIN openfox.runs r ON r.organization_id=a.organization_id AND r.id=a.run_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND r.conversation_id=$2::uuid`, input.ArtifactID, conversation))
	if err != nil {
		return f, missing(err)
	}
	if !f.Reviewable() {
		return f, ErrUnavailable
	}
	if f.SHA256 != input.SHA256 {
		return f, ErrConflict
	}
	return f, nil
}
func (s Service) CommentReplay(ctx context.Context, tx pgx.Tx, org, conversation, operation, hash string, actor conversations.Actor) (CommentThread, bool, error) {
	var t CommentThread
	var raw []byte
	var previous string
	err := tx.QueryRow(ctx, `SELECT snapshot,request_hash FROM openfox.workspace_comment_revisions WHERE organization_id=openfox_private.current_organization_id() AND operation_id=$1::uuid`, operation).Scan(&raw, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	if previous != hash {
		return t, false, ErrConflict
	}
	if err = json.Unmarshal(raw, &t); err != nil {
		return t, false, err
	}
	if t.ConversationID != conversation {
		return t, false, conversations.ErrNotFound
	}
	err = s.CommentAccess(ctx, tx, org, t, actor)
	return t, true, err
}
func (s Service) CreateComment(ctx context.Context, tx pgx.Tx, org, conversation, id string, actor conversations.Actor, source CommentSource) (CommentThread, error) {
	var empty CommentThread
	if !membership.ValidateUUID(id) {
		return empty, ErrInvalid
	}
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conversation, actor); err != nil {
		return empty, err
	}
	// One short lock also serializes creation limits and same-ID retries.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM openfox.conversations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, conversation); err != nil {
		return empty, err
	}
	hash := commentHash(conversation, actor.UserID, source)
	if t, found, err := s.CommentReplay(ctx, tx, org, conversation, id, hash, actor); found || err != nil {
		return t, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.workspace_comment_threads WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid`, conversation).Scan(&count); err != nil {
		return empty, err
	}
	if count >= 200 {
		return empty, ErrInvalid
	}
	var body string
	if source.File != nil {
		f, err := s.CommentFile(ctx, tx, org, conversation, actor, *source.File)
		if err != nil {
			return empty, err
		}
		if source.File.validated != validationStamp(*source.File, f) || source.File.validatedVersion != f.Version {
			return empty, ErrConflict
		}
		body = source.File.Comments[0].Body
	} else {
		body = source.Browser.Comments[0].Body
	}
	now := time.Now().UTC()
	t := CommentThread{ID: id, ConversationID: conversation, Version: 1, Status: "OPEN", Source: source, Comments: []CommentPost{{ID: id, AuthorID: actor.UserID, Body: body, CreatedAt: now}}, UpdatedAt: now}
	raw, _ := json.Marshal(t)
	if len(raw) > 120<<10 {
		return empty, ErrInvalid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.workspace_comment_threads(organization_id,conversation_id,id,version,snapshot) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,1,$3::jsonb)`, conversation, id, raw); err != nil {
		return empty, commentWriteError(err)
	}
	if source.Browser != nil && source.Browser.Comments[0].ScreenshotAttachmentID != "" {
		tag, err := tx.Exec(ctx, `UPDATE openfox.attachments SET comment_thread_id=$1::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$2::uuid AND conversation_id=$3::uuid AND uploaded_by=$4::uuid AND message_id IS NULL AND comment_thread_id IS NULL AND content_type IN ('image/png','image/jpeg') AND size_bytes<=4194304`, id, source.Browser.Comments[0].ScreenshotAttachmentID, conversation, actor.UserID)
		if err != nil {
			return empty, err
		}
		if tag.RowsAffected() != 1 {
			return empty, ErrInvalid
		}
	}
	return t, saveCommentRevision(ctx, tx, t, id, hash, raw)
}
func saveCommentRevision(ctx context.Context, tx pgx.Tx, t CommentThread, op, hash string, raw []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO openfox.workspace_comment_revisions(organization_id,thread_id,version,operation_id,request_hash,snapshot) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,$5::jsonb)`, t.ID, t.Version, op, hash, raw)
	return commentWriteError(err)
}
func commentWriteError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}
func (s Service) GetComment(ctx context.Context, tx pgx.Tx, org, conversation, id string, actor conversations.Actor) (CommentThread, error) {
	var t CommentThread
	var raw []byte
	if !membership.ValidateUUID(id) {
		return t, conversations.ErrNotFound
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, org, conversation, actor); err != nil {
		return t, err
	}
	err := tx.QueryRow(ctx, `SELECT snapshot FROM openfox.workspace_comment_threads WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND id=$2::uuid`, conversation, id).Scan(&raw)
	if err != nil {
		return t, missing(err)
	}
	if err = json.Unmarshal(raw, &t); err != nil {
		return t, err
	}
	return t, commentSourceAccess(ctx, tx, org, t, actor)
}
func (s Service) ListComments(ctx context.Context, tx pgx.Tx, org, conversation, cursor string, actor conversations.Actor) (CommentPage, error) {
	page := CommentPage{Threads: []CommentThread{}}
	if cursor != "" && !membership.ValidateUUID(cursor) {
		return page, ErrInvalid
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, org, conversation, actor); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT snapshot FROM openfox.workspace_comment_threads WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT 21`, conversation, cursor)
	if err != nil {
		return page, err
	}
	var all []CommentThread
	for rows.Next() {
		var raw []byte
		var t CommentThread
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &t)
		}
		if err != nil {
			rows.Close()
			return page, err
		}
		all = append(all, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(all) > 20 {
		page.NextCursor = all[19].ID
		all = all[:20]
	}
	for _, t := range all {
		if err = commentSourceAccess(ctx, tx, org, t, actor); err != nil {
			// Revoked document access must not expose previously saved quotes.
			var rule *collaboration.RuleError
			if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) || (errors.As(err, &rule) && (rule.Status == 403 || rule.Status == 404)) {
				continue
			}
			return page, err
		}
		page.Threads = append(page.Threads, t)
	}
	return page, nil
}
func (s Service) MutateComment(ctx context.Context, tx pgx.Tx, org, conversation, id string, actor conversations.Actor, m CommentMutation) (CommentThread, error) {
	var empty CommentThread
	if !membership.ValidateUUID(id) || !membership.ValidateUUID(m.OperationID) || m.Version < 1 {
		return empty, ErrInvalid
	}
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conversation, actor); err != nil {
		return empty, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM openfox.workspace_comment_threads WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND id=$2::uuid FOR UPDATE`, conversation, id); err != nil {
		return empty, err
	}
	hash := commentHash(conversation, actor.UserID, struct {
		ID       string
		Mutation CommentMutation
	}{id, m})
	if t, found, err := s.CommentReplay(ctx, tx, org, conversation, m.OperationID, hash, actor); found || err != nil {
		return t, err
	}
	t, err := s.GetComment(ctx, tx, org, conversation, id, actor)
	if err != nil {
		return t, err
	}
	if t.Version != m.Version {
		return t, ErrConflict
	}
	body := strings.TrimSpace(m.Body)
	now := time.Now().UTC()
	switch m.Action {
	case "reply", "edit":
		if body == "" || len(body) > 8192 || !ValidText(body) {
			return t, ErrInvalid
		}
		if m.Action == "reply" {
			if t.Status != "OPEN" || len(t.Comments) >= 50 {
				return t, ErrConflict
			}
			t.Comments = append(t.Comments, CommentPost{ID: m.OperationID, AuthorID: actor.UserID, Body: body, CreatedAt: now})
		} else {
			found := false
			for i := range t.Comments {
				if t.Comments[i].ID == m.CommentID {
					if t.Comments[i].AuthorID != actor.UserID {
						return t, conversations.ErrForbidden
					}
					t.Comments[i].Body = body
					t.Comments[i].EditedAt = &now
					found = true
				}
			}
			if !found {
				return t, conversations.ErrNotFound
			}
		}
	case "resolve":
		t.Status = "RESOLVED"
	case "reopen":
		t.Status = "OPEN"
	default:
		return t, ErrInvalid
	}
	t.Version++
	t.UpdatedAt = now
	raw, _ := json.Marshal(t)
	if len(raw) > 120<<10 {
		return t, ErrInvalid
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.workspace_comment_threads SET version=$3,snapshot=$4::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2`, id, m.Version, t.Version, raw)
	if err != nil {
		return t, err
	}
	if tag.RowsAffected() != 1 {
		return t, ErrConflict
	}
	return t, saveCommentRevision(ctx, tx, t, m.OperationID, hash, raw)
}

// ValidateDiscussionReferences checks the immutable selected revision, not latest
// mutable discussion text. Called again inside the message transaction.
func (s Service) ValidateDiscussionReferences(ctx context.Context, tx pgx.Tx, org, conversation string, actor conversations.Actor, files []Input, browsers []conversations.BrowserContext) error {
	if len(files) > 10 || len(browsers) > 10 {
		return ErrInvalid
	}
	fileCount, browserCount := 0, 0
	for _, f := range files {
		fileCount += len(f.Comments)
	}
	for _, b := range browsers {
		browserCount += len(b.Comments)
	}
	if fileCount > 20 || browserCount > 20 {
		return ErrInvalid
	}
	load := func(ref *conversations.CommentReference) (CommentThread, error) {
		var t CommentThread
		var raw []byte
		if !membership.ValidateUUID(ref.ID) || !membership.ValidateUUID(ref.CommentID) || ref.Version < 1 {
			return t, ErrInvalid
		}
		err := tx.QueryRow(ctx, `SELECT r.snapshot FROM openfox.workspace_comment_revisions r JOIN openfox.workspace_comment_threads t ON t.organization_id=r.organization_id AND t.id=r.thread_id WHERE r.organization_id=openfox_private.current_organization_id() AND t.conversation_id=$1::uuid AND r.thread_id=$2::uuid AND r.version=$3`, conversation, ref.ID, ref.Version).Scan(&raw)
		if err != nil {
			return t, missing(err)
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			return t, err
		}
		return t, s.CommentAccess(ctx, tx, org, t, actor)
	}
	same := func(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return string(x) == string(y) }
	for _, f := range files {
		for _, c := range f.Comments {
			if c.Discussion == nil {
				continue
			}
			t, err := load(c.Discussion)
			if err != nil {
				return err
			}
			derived, e := s.resolvedDiscussionSource(ctx, tx, c.Discussion, t)
			if e != nil {
				return e
			}
			src := derived.File
			if src == nil || src.ArtifactID != f.ArtifactID || src.NodeID != f.NodeID || src.SpaceID != f.SpaceID || src.NodeVersion != f.NodeVersion || src.SHA256 != f.SHA256 || selectedCommentBody(t, c.Discussion.CommentID) != c.Body || !same(src.Comments[0].Anchor, c.Anchor) {
				return ErrConflict
			}
		}
	}
	for _, b := range browsers {
		for _, c := range b.Comments {
			if c.Discussion == nil {
				continue
			}
			t, err := load(c.Discussion)
			if err != nil {
				return err
			}
			derived, e := s.resolvedDiscussionSource(ctx, tx, c.Discussion, t)
			if e != nil {
				return e
			}
			src := derived.Browser
			if src == nil || src.SourceURL != b.SourceURL || src.Title != b.Title || src.CapturedAt != b.CapturedAt || selectedCommentBody(t, c.Discussion.CommentID) != c.Body || !same(src.Comments[0].Anchor, c.Anchor) {
				return ErrConflict
			}
			original := src.Comments[0].ScreenshotAttachmentID
			if original != "" {
				var ok bool
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.attachments original JOIN openfox.attachments copy ON copy.organization_id=original.organization_id AND copy.sha256=original.sha256 AND copy.content_type=original.content_type WHERE original.organization_id=openfox_private.current_organization_id() AND original.id=$1::uuid AND original.comment_thread_id=$2::uuid AND copy.id=$3::uuid AND copy.conversation_id=$4::uuid AND copy.uploaded_by=$5::uuid)`, original, t.ID, c.ScreenshotAttachmentID, conversation, actor.UserID).Scan(&ok)
				if err != nil {
					return err
				}
				if !ok {
					return ErrConflict
				}
			} else if c.ScreenshotAttachmentID != "" {
				return ErrConflict
			}
		}
	}
	return nil
}

func (s Service) ReplayCommentCreation(ctx context.Context, tx pgx.Tx, org, conversation, id string, actor conversations.Actor, source CommentSource) (CommentThread, bool, error) {
	if !membership.ValidateUUID(id) {
		return CommentThread{}, false, ErrInvalid
	}
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conversation, actor); err != nil {
		return CommentThread{}, false, err
	}
	return s.CommentReplay(ctx, tx, org, conversation, id, commentHash(conversation, actor.UserID, source), actor)
}

func selectedCommentBody(t CommentThread, id string) string {
	for _, p := range t.Comments {
		if p.ID == id {
			return p.Body
		}
	}
	return ""
}
