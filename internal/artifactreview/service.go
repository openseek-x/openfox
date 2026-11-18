package artifactreview

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/conversations"
	"openfox/internal/docs"
	"openfox/internal/membership"
	"openfox/internal/office"
	"time"
)

type Service struct{}

// Input is feedback attached to an ordinary chat message. SHA/version are
// conflict guards; proposals separately store validated candidate content.
type Input struct {
	validated        string
	validatedVersion int64
	ID               string     `json:"id"`
	ArtifactID       string     `json:"artifact_id,omitempty"`
	SpaceID          string     `json:"space_id,omitempty"`
	NodeID           string     `json:"node_id,omitempty"`
	NodeVersion      int64      `json:"node_version,omitempty"`
	SHA256           string     `json:"sha256"`
	Comments         []Snapshot `json:"comments"`
	ConversationID   string     `json:"-"`
}
type Context struct {
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	Notice            string `json:"notice,omitempty"`
	Input
	MessageID        string `json:"-"`
	UserID           string `json:"-"`
	File             File   `json:"file"`
	ResultHash       string `json:"-"`
	ResultArtifactID string `json:"-"`
	ResultSHA256     string `json:"-"`
}

// File uses the artifact wire shape; StorageKey is never sent to a client.
type File struct {
	OrganizationID string     `json:"organization_id"`
	ID             string     `json:"id"`
	RunID          string     `json:"run_id,omitempty"`
	Name           string     `json:"name"`
	MIMEType       string     `json:"mime_type"`
	SHA256         string     `json:"sha256"`
	SizeBytes      int64      `json:"size_bytes"`
	State          string     `json:"state"`
	RetentionUntil *time.Time `json:"retention_until,omitempty"`
	Pinned         bool       `json:"pinned"`
	Version        int64      `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	StorageKey     string     `json:"-"`
	Native         bool       `json:"native,omitempty"`
	NativeBody     string     `json:"-"`
}

const fileColumns = `a.organization_id::text,a.id::text,a.run_id::text,a.name,a.mime_type,a.sha256,a.size_bytes,a.state,a.retention_until,a.pinned,a.version,a.created_at,a.storage_key`

func scanFile(row pgx.Row) (f File, err error) {
	err = row.Scan(&f.OrganizationID, &f.ID, &f.RunID, &f.Name, &f.MIMEType, &f.SHA256, &f.SizeBytes, &f.State, &f.RetentionUntil, &f.Pinned, &f.Version, &f.CreatedAt, &f.StorageKey)
	return
}
func missing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return conversations.ErrNotFound
	}
	return err
}
func (f File) Available() bool {
	return f.State == "AVAILABLE" && (f.RetentionUntil == nil || f.RetentionUntil.After(time.Now()))
}
func (f File) Reviewable() bool {
	return f.Available() && ((f.SizeBytes <= MaxFileBytes && TextMIME(f.MIMEType)) || (f.SizeBytes <= MaxVisualBytes && VisualMIME(f.MIMEType)) || (f.SizeBytes <= office.MaxBytes && office.MIME(f.MIMEType)))
}

func (s Service) source(ctx context.Context, tx pgx.Tx, org string, input Input, user string, write bool) (File, error) {
	c, err := (conversations.Service{}).AuthorizeInvocation(ctx, tx, org, input.ConversationID, user)
	if err != nil {
		return File{}, err
	}
	if c.Lifecycle != conversations.LifecycleOpen {
		return File{}, conversations.ErrConversationClosed
	}
	if input.NodeID != "" {
		return s.nativeFile(ctx, tx, org, input, user, write)
	}
	f, err := scanFile(tx.QueryRow(ctx, `SELECT `+fileColumns+` FROM openfox.artifacts a JOIN openfox.runs r ON r.organization_id=a.organization_id AND r.id=a.run_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND r.conversation_id=$2::uuid`, input.ArtifactID, input.ConversationID))
	if err != nil {
		return f, missing(err)
	}
	if !f.Reviewable() {
		return f, ErrUnavailable
	}
	if f.SHA256 != input.SHA256 {
		return f, ErrConflict
	}
	// An old file link must not silently start another edit after it was replaced.
	var replaced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.message_file_contexts WHERE organization_id=openfox_private.current_organization_id() AND source_artifact_id=$1::uuid AND result_artifact_id IS NOT NULL) OR EXISTS(SELECT 1 FROM openfox.artifacts source JOIN openfox.artifacts newer ON newer.organization_id=source.organization_id AND newer.content_id=source.content_id AND newer.content_revision>source.content_revision WHERE source.organization_id=openfox_private.current_organization_id() AND source.id=$1::uuid)`, input.ArtifactID).Scan(&replaced); err != nil {
		return f, err
	}
	if replaced {
		return f, ErrConflict
	}
	return f, nil
}

// Bind runs in the same transaction as PostMessage. Replays compare the complete
// context payload, including the empty case, before any queued input can run.
func (s Service) Bind(ctx context.Context, tx pgx.Tx, org, conversation, message, user string, inputs []Input) error {
	if err := ValidateInputs(inputs); err != nil {
		return err
	}
	if len(inputs) > 10 {
		return ErrInvalid
	}
	if inputs == nil {
		inputs = []Input{}
	}
	canonical, _ := json.Marshal(inputs)
	if len(canonical) > MaxToolBytes {
		return ErrInvalid
	}
	if len(inputs) > 0 {
		var supported bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(c.runtime_override,''),v.runtime)='codex' AND c.crew_id IS NULL FROM openfox.conversations c JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id JOIN openfox.agent_config_versions v ON v.organization_id=a.organization_id AND v.agent_id=a.id AND v.config_version=a.current_config_version WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid`, conversation).Scan(&supported); err != nil {
			return missing(err)
		}
		if !supported {
			return ErrUnsupported
		}
	}
	var boundHash *string
	if err := tx.QueryRow(ctx, `SELECT file_context_hash FROM openfox.messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, message).Scan(&boundHash); err != nil {
		return err
	}
	hash := Digest(string(canonical))
	if boundHash != nil {
		if *boundHash != hash {
			return conversations.ErrDuplicateMessage
		}
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.messages SET file_context_hash=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, message, hash); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,payload_hash FROM openfox.message_file_contexts WHERE organization_id=openfox_private.current_organization_id() AND message_id=$1::uuid ORDER BY id`, message)
	if err != nil {
		return err
	}
	existing := map[string]string{}
	for rows.Next() {
		var id, hash string
		if err = rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return err
		}
		existing[id] = hash
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(existing) > 0 && len(existing) != len(inputs) {
		return conversations.ErrDuplicateMessage
	}
	for _, input := range inputs {
		raw, _ := json.Marshal(input)
		hash := Digest(string(raw))
		if previous, ok := existing[input.ID]; ok {
			if previous != hash {
				return conversations.ErrDuplicateMessage
			}
			continue
		}
		if len(existing) > 0 {
			return conversations.ErrDuplicateMessage
		}
		input.ConversationID = conversation
		var locked string
		if input.NodeID != "" {
			err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.doc_nodes WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid FOR SHARE`, input.NodeID, input.SpaceID).Scan(&locked)
		} else {
			err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.artifacts WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, input.ArtifactID).Scan(&locked)
		}
		if err != nil {
			return missing(err)
		}
		file, e := s.source(ctx, tx, org, input, user, false)
		if e != nil {
			return e
		}
		if input.validated != "" && input.validatedVersion != file.Version {
			return ErrConflict
		}
		if input.validated == "" || input.validated != validationStamp(input, file) {
			return ErrInvalid
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.message_file_contexts(organization_id,id,message_id,source_artifact_id,input_snapshot,payload_hash) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,NULLIF($3,'')::uuid,$4::jsonb,$5)`, input.ID, message, input.ArtifactID, raw, hash)
		var constraint *pgconn.PgError
		if errors.As(err, &constraint) && constraint.Code == "23505" {
			return ErrConflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (s Service) ContextForRun(ctx context.Context, tx pgx.Tx, org, run, id string, lock bool) (Context, error) {
	var q Context
	var raw []byte
	if !membership.ValidateUUID(id) {
		return q, ErrInvalid
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF f"
	}
	err := tx.QueryRow(ctx, `SELECT f.message_id::text,m.conversation_id::text,m.author_user_id::text,f.input_snapshot,COALESCE(f.result_hash,''),COALESCE(f.result_artifact_id::text,''),COALESCE(f.result_sha256,'') FROM openfox.message_file_contexts f JOIN openfox.messages m ON m.organization_id=f.organization_id AND m.id=f.message_id WHERE f.organization_id=openfox_private.current_organization_id() AND f.id=$2::uuid AND EXISTS(SELECT 1 FROM openfox.conversation_inputs i WHERE i.organization_id=f.organization_id AND i.message_id=f.message_id AND i.run_id=$1::uuid)`+suffix, run, id).Scan(&q.MessageID, &q.ConversationID, &q.UserID, &raw, &q.ResultHash, &q.ResultArtifactID, &q.ResultSHA256)
	if err != nil {
		return q, missing(err)
	}
	if err = json.Unmarshal(raw, &q.Input); err != nil {
		return q, err
	}
	// Even replaying a successful tool call requires current conversation access.
	if _, err = (conversations.Service{}).AuthorizeInvocation(ctx, tx, org, q.ConversationID, q.UserID); err != nil {
		return q, err
	}
	if q.NodeID != "" {
		if _, e := nativeAccess(ctx, tx, org, q.Input, q.UserID, false); e != nil {
			return q, e
		}
	}
	return q, nil
}
func (s Service) ForRun(ctx context.Context, tx pgx.Tx, org, run, id string, lock bool) (Context, error) {
	q, err := s.ContextForRun(ctx, tx, org, run, id, lock)
	if err != nil || q.ResultHash != "" {
		return q, err
	}
	q.File, err = s.CurrentSource(ctx, tx, org, q)
	return q, err
}
func (s Service) CurrentSource(ctx context.Context, tx pgx.Tx, org string, q Context) (File, error) {
	return s.source(ctx, tx, org, q.Input, q.UserID, false)
}
func (s Service) ForRunAll(ctx context.Context, tx pgx.Tx, org, run string) ([]Context, error) {
	rows, err := tx.Query(ctx, `SELECT f.id::text FROM openfox.message_file_contexts f WHERE f.organization_id=openfox_private.current_organization_id() AND EXISTS(SELECT 1 FROM openfox.conversation_inputs i WHERE i.organization_id=f.organization_id AND i.message_id=f.message_id AND i.run_id=$1::uuid) ORDER BY f.id LIMIT 21`, run)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 20 {
		return nil, ErrInvalid
	}
	contexts := make([]Context, 0, len(ids))
	for _, id := range ids {
		q, e := s.ForRun(ctx, tx, org, run, id, false)
		if e != nil {
			switch {
			case errors.Is(e, ErrConflict):
				q.UnavailableReason = "CHANGED"
				q.Notice = "该文件已变更，无法应用修改。请向用户说明，并继续处理其他批注与正文。"
			case errors.Is(e, ErrUnavailable), errors.Is(e, conversations.ErrNotFound), errors.Is(e, conversations.ErrForbidden), errors.Is(e, docs.ErrNotFound):
				q.UnavailableReason = "UNAVAILABLE"
				q.Notice = "该文件不可用，无法应用修改。请向用户说明，并继续处理其他批注与正文。"
				q.Comments = nil
			default:
				return nil, e
			}
			q.File = File{}
		}
		contexts = append(contexts, q)
	}
	return contexts, nil
}
func (Service) Applied(ctx context.Context, tx pgx.Tx, q Context, hash, artifact, sha string) error {
	tag, err := tx.Exec(ctx, `UPDATE openfox.message_file_contexts SET result_hash=$2,result_artifact_id=NULLIF($3,'')::uuid,result_sha256=$4 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND result_hash IS NULL`, q.ID, hash, artifact, sha)
	var constraint *pgconn.PgError
	if errors.As(err, &constraint) && constraint.Code == "23505" {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}
