package artifactreview

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/docs"
	"openfox/internal/membership"
)

type AnchorResolution struct {
	ID            string `json:"id"`
	ThreadID      string `json:"thread_id"`
	ThreadVersion int64  `json:"thread_version"`
	Resolution
	Target     *CommentSource `json:"target,omitempty"`
	TargetFile *File          `json:"target_file,omitempty"`
}
type ResolveRequest struct {
	Version int64          `json:"version"`
	Browser *CommentSource `json:"browser,omitempty"`
}

func ResolutionHash(conversation, user, thread string, input ResolveRequest) string {
	return commentHash(conversation, user, struct {
		Thread string
		Input  ResolveRequest
	}{thread, input})
}

func (s Service) ResolutionReplay(ctx context.Context, tx pgx.Tx, org, conversation, thread, id, hash string, actor conversations.Actor) (AnchorResolution, bool, error) {
	var result AnchorResolution
	if !membership.ValidateUUID(id) {
		return result, false, ErrInvalid
	}
	if _, err := s.GetComment(ctx, tx, org, conversation, thread, actor); err != nil {
		return result, false, err
	}
	var raw []byte
	var previous string
	err := tx.QueryRow(ctx, `SELECT snapshot,request_hash FROM openfox.workspace_anchor_resolutions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&raw, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if previous != hash {
		return result, false, ErrConflict
	}
	err = json.Unmarshal(raw, &result)
	if result.ThreadID != thread {
		return result, false, ErrConflict
	}
	return result, true, err
}

// Original native revisions are read only after current space/conversation ACL.
// Blob bytes are read and parsed by the handler outside this transaction.
func (s Service) ResolutionFiles(ctx context.Context, tx pgx.Tx, org, conversation, thread string, actor conversations.Actor) (CommentThread, File, File, Input, error) {
	t, err := s.GetComment(ctx, tx, org, conversation, thread, actor)
	var base, head File
	var target Input
	if err != nil {
		return t, base, head, target, err
	}
	if t.Source.File == nil {
		return t, base, head, target, ErrInvalid
	}
	target = *t.Source.File
	target.Comments = append([]Snapshot(nil), target.Comments...)
	target.ConversationID = conversation
	if target.NodeID != "" {
		_, err = nativeAccess(ctx, tx, org, target, actor.UserID, false)
		if err != nil {
			return t, base, head, target, err
		}
		detail, e := docs.GetNodeDetail(ctx, tx, target.SpaceID, target.NodeID)
		if err == nil {
			err = e
		}
		if err != nil {
			return t, base, head, target, err
		}
		if detail.Body == nil {
			return t, base, head, target, ErrUnavailable
		}
		ref, e := docs.LoadRef(ctx, tx, target.SpaceID, target.NodeID)
		if e != nil {
			return t, base, head, target, e
		}
		head = File{ID: target.NodeID, Version: int64(ref.Version), Native: true, NativeBody: *detail.Body, Name: detail.Title + ".md", MIMEType: "text/markdown", SizeBytes: int64(len(*detail.Body)), SHA256: Digest(*detail.Body), State: "AVAILABLE"}
		base = head
		if target.NodeVersion != head.Version {
			err = tx.QueryRow(ctx, `SELECT body FROM openfox.doc_revisions WHERE organization_id=openfox_private.current_organization_id() AND node_id=$1::uuid AND node_version<=$2 AND body IS NOT NULL ORDER BY node_version DESC LIMIT 1`, target.NodeID, target.NodeVersion).Scan(&base.NativeBody)
			base.Version = target.NodeVersion
			base.SHA256 = Digest(base.NativeBody)
			base.SizeBytes = int64(len(base.NativeBody))
		}
		if err == nil && base.SHA256 != target.SHA256 {
			err = ErrUnavailable
		}
		target.NodeVersion = head.Version
		target.SHA256 = head.SHA256
	} else {
		base, err = scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM openfox.artifacts a JOIN openfox.runs r ON r.organization_id=a.organization_id AND r.id=a.run_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND r.conversation_id=$2::uuid`, target.ArtifactID, conversation))
		if err == nil {
			head, err = scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM openfox.artifacts a JOIN openfox.artifacts source ON source.organization_id=a.organization_id AND source.content_id=a.content_id JOIN openfox.runs r ON r.organization_id=a.organization_id AND r.id=a.run_id WHERE a.organization_id=openfox_private.current_organization_id() AND source.id=$1::uuid AND r.conversation_id=$2::uuid ORDER BY a.content_revision DESC LIMIT 1`, target.ArtifactID, conversation))
		}
		if err == nil && base.SHA256 != target.SHA256 {
			err = ErrConflict
		}
		target.ArtifactID = head.ID
		target.SHA256 = head.SHA256
	}
	if err == nil && (!base.Reviewable() || !head.Reviewable() || base.MIMEType != head.MIMEType) {
		err = ErrUnavailable
	}
	return t, base, head, target, missing(err)
}

func (s Service) SaveResolution(ctx context.Context, tx pgx.Tx, org, conversation string, actor conversations.Actor, hash string, r AnchorResolution) (AnchorResolution, error) {
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conversation, actor); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM openfox.workspace_comment_threads WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, r.ThreadID); err != nil {
		return r, err
	}
	if prior, ok, err := s.ResolutionReplay(ctx, tx, org, conversation, r.ThreadID, r.ID, hash, actor); ok || err != nil {
		return prior, err
	}
	t, err := s.GetComment(ctx, tx, org, conversation, r.ThreadID, actor)
	if err != nil {
		return r, err
	}
	if t.Version != r.ThreadVersion {
		return r, ErrConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.workspace_anchor_resolutions WHERE organization_id=openfox_private.current_organization_id() AND thread_id=$1::uuid`, r.ThreadID).Scan(&count); err != nil {
		return r, err
	}
	if count >= 100 {
		return r, ErrInvalid
	}
	if r.TargetFile != nil {
		_, _, head, _, e := s.ResolutionFiles(ctx, tx, org, conversation, r.ThreadID, actor)
		if e != nil {
			return r, e
		}
		if head.ID != r.TargetFile.ID || head.Version != r.TargetFile.Version || head.SHA256 != r.TargetFile.SHA256 {
			return r, ErrConflict
		}
	}
	if r.Target != nil && r.Target.Browser != nil {
		image := r.Target.Browser.Comments[0].ScreenshotAttachmentID
		if image != "" {
			tag, e := tx.Exec(ctx, `UPDATE openfox.attachments SET comment_thread_id=$1::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=$2::uuid AND conversation_id=$3::uuid AND uploaded_by=$4::uuid AND message_id IS NULL AND comment_thread_id IS NULL AND content_type IN ('image/png','image/jpeg') AND size_bytes<=4194304`, r.ThreadID, image, conversation, actor.UserID)
			if e != nil {
				return r, e
			}
			if tag.RowsAffected() != 1 {
				return r, ErrInvalid
			}
		}
	}
	raw, _ := json.Marshal(r)
	if len(raw) > 120<<10 {
		return r, ErrInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.workspace_anchor_resolutions(organization_id,id,thread_id,thread_version,request_hash,snapshot) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::jsonb)`, r.ID, r.ThreadID, r.ThreadVersion, hash, raw)
	return r, commentWriteError(err)
}

func (s Service) resolvedDiscussionSource(ctx context.Context, tx pgx.Tx, ref *conversations.CommentReference, t CommentThread) (CommentSource, error) {
	if ref.ResolutionID == "" {
		return t.Source, nil
	}
	if !membership.ValidateUUID(ref.ResolutionID) {
		return CommentSource{}, ErrInvalid
	}
	var raw []byte
	var r AnchorResolution
	err := tx.QueryRow(ctx, `SELECT snapshot FROM openfox.workspace_anchor_resolutions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND thread_id=$2::uuid AND thread_version=$3`, ref.ResolutionID, t.ID, ref.Version).Scan(&raw)
	if err != nil {
		return CommentSource{}, missing(err)
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return CommentSource{}, err
	}
	if r.Status != "exact" || r.Target == nil {
		return CommentSource{}, ErrConflict
	}
	return *r.Target, nil
}
