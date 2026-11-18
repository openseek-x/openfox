package artifactreview

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifacts"
	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

type Proposal struct {
	ID                string    `json:"id"`
	ConversationID    string    `json:"conversation_id"`
	RunID             string    `json:"run_id"`
	ContextID         string    `json:"context_id,omitempty"`
	RequestHash       string    `json:"-"`
	Source            Input     `json:"source"`
	Base              File      `json:"base"`
	StorageKey        string    `json:"-"`
	SizeBytes         int64     `json:"size_bytes"`
	SHA256            string    `json:"sha256"`
	Summary           string    `json:"summary"`
	Status            string    `json:"status"`
	Version           int64     `json:"version"`
	CreatedBy         string    `json:"created_by"`
	CreatedAt         time.Time `json:"created_at"`
	DecisionID        string    `json:"decision_id,omitempty"`
	DecidedBy         string    `json:"decided_by,omitempty"`
	ResultArtifactID  string    `json:"result_artifact_id,omitempty"`
	ResultNodeVersion int64     `json:"result_node_version,omitempty"`
}
type ProposalDecision struct {
	OperationID string `json:"operation_id"`
	Version     int64  `json:"version"`
	Action      string `json:"action"`
}
type ProposalPage struct {
	Proposals  []Proposal `json:"proposals"`
	NextCursor string     `json:"next_cursor"`
}

const proposalColumns = `p.id::text,p.conversation_id::text,p.run_id::text,COALESCE(p.context_id::text,''),p.request_hash,p.source_snapshot,p.base_file,p.base_key,p.storage_key,p.size_bytes,p.sha256,p.summary,p.status,p.version,p.created_by::text,p.created_at,COALESCE(p.decision_id::text,''),COALESCE(p.decided_by::text,''),COALESCE(p.result_artifact_id::text,''),COALESCE(p.result_node_version,0)`

func scanProposal(row pgx.Row) (p Proposal, err error) {
	var source, base []byte
	var key string
	err = row.Scan(&p.ID, &p.ConversationID, &p.RunID, &p.ContextID, &p.RequestHash, &source, &base, &key, &p.StorageKey, &p.SizeBytes, &p.SHA256, &p.Summary, &p.Status, &p.Version, &p.CreatedBy, &p.CreatedAt, &p.DecisionID, &p.DecidedBy, &p.ResultArtifactID, &p.ResultNodeVersion)
	if err != nil {
		return p, missing(err)
	}
	if err = json.Unmarshal(source, &p.Source); err != nil {
		return
	}
	err = json.Unmarshal(base, &p.Base)
	p.Base.StorageKey = key
	p.Source.ConversationID = p.ConversationID
	return
}
func (p Proposal) CandidateFile() File {
	return File{ID: p.ID, Name: p.Base.Name, MIMEType: p.Base.MIMEType, StorageKey: p.StorageKey, SizeBytes: p.SizeBytes, SHA256: p.SHA256, State: "AVAILABLE"}
}
func proposalAccess(ctx context.Context, tx pgx.Tx, org string, p Proposal, actor conversations.Actor) error {
	if p.Source.NodeID != "" {
		_, err := nativeAccess(ctx, tx, org, p.Source, actor.UserID, false)
		return err
	}
	return nil
}
func (s Service) GetProposal(ctx context.Context, tx pgx.Tx, org, conversation, id string, actor conversations.Actor, lock bool) (Proposal, error) {
	var p Proposal
	if !membership.ValidateUUID(id) {
		return p, conversations.ErrNotFound
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, org, conversation, actor); err != nil {
		return p, err
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF p"
	}
	p, err := scanProposal(tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM openfox.workspace_proposals p WHERE p.organization_id=openfox_private.current_organization_id() AND p.conversation_id=$1::uuid AND p.id=$2::uuid`+suffix, conversation, id))
	if err != nil {
		return p, err
	}
	return p, proposalAccess(ctx, tx, org, p, actor)
}
func (s Service) ListProposals(ctx context.Context, tx pgx.Tx, org, conversation, cursor string, actor conversations.Actor) (ProposalPage, error) {
	page := ProposalPage{Proposals: []Proposal{}}
	if cursor != "" && !membership.ValidateUUID(cursor) {
		return page, ErrInvalid
	}
	if _, err := (conversations.Service{}).Get(ctx, tx, org, conversation, actor); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT `+proposalColumns+` FROM openfox.workspace_proposals p WHERE p.organization_id=openfox_private.current_organization_id() AND p.conversation_id=$1::uuid AND ($2='' OR (p.created_at,p.id)<(SELECT created_at,id FROM openfox.workspace_proposals WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND id=NULLIF($2,'')::uuid)) ORDER BY p.created_at DESC,p.id DESC LIMIT 21`, conversation, cursor)
	if err != nil {
		return page, err
	}
	all := []Proposal{}
	for rows.Next() {
		p, e := scanProposal(rows)
		if e != nil {
			rows.Close()
			return page, e
		}
		all = append(all, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(all) > 20 {
		page.NextCursor = all[19].ID
		all = all[:20]
	}
	for _, p := range all {
		if err = proposalAccess(ctx, tx, org, p, actor); err != nil {
			var rule *collaboration.RuleError
			if errors.Is(err, conversations.ErrForbidden) || errors.Is(err, conversations.ErrNotFound) || (errors.As(err, &rule) && (rule.Status == 403 || rule.Status == 404)) {
				continue
			}
			return page, err
		}
		page.Proposals = append(page.Proposals, p)
	}
	return page, nil
}
func (Service) ProposalReplay(ctx context.Context, tx pgx.Tx, run, contextID, hash string) (Proposal, bool, error) {
	p, err := scanProposal(tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM openfox.workspace_proposals p WHERE p.organization_id=openfox_private.current_organization_id() AND p.run_id=$1::uuid AND p.context_id=$2::uuid AND p.request_hash=$3`, run, contextID, hash))
	if errors.Is(err, conversations.ErrNotFound) {
		return p, false, nil
	}
	return p, err == nil, err
}

// RecordProposal is called only after byte/anchor validation. The caller holds the
// run lock and rechecks current execution authority. No content head is changed.
func (Service) RecordProposal(ctx context.Context, tx pgx.Tx, p Proposal, maxBytes int64) (Proposal, error) {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, p.RunID).Scan(&locked); err != nil {
		return p, missing(err)
	}
	if p.ContextID != "" {
		if prior, found, err := (Service{}).ProposalReplay(ctx, tx, p.RunID, p.ContextID, p.RequestHash); found || err != nil {
			return prior, err
		}
	}
	var count int
	var total int64
	err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(bytes),0) FROM (
 SELECT size_bytes bytes FROM openfox.artifacts WHERE organization_id=openfox_private.current_organization_id() AND run_id=$1::uuid AND state<>'DELETED'
 UNION ALL SELECT size_bytes+octet_length(base_body) FROM openfox.workspace_proposals WHERE organization_id=openfox_private.current_organization_id() AND run_id=$1::uuid AND result_artifact_id IS NULL) usage`, p.RunID).Scan(&count, &total)
	if err != nil {
		return p, err
	}
	if count >= 10 || total > maxBytes-p.SizeBytes-int64(len(p.Base.NativeBody)) {
		return p, ErrUnavailable
	}
	if len(p.Summary) > 8192 || !ValidText(p.Summary) {
		return p, ErrInvalid
	}
	source, _ := json.Marshal(p.Source)
	base, _ := json.Marshal(p.Base)
	_, err = tx.Exec(ctx, `INSERT INTO openfox.workspace_proposals(organization_id,id,conversation_id,run_id,context_id,request_hash,source_snapshot,base_file,base_key,base_body,storage_key,size_bytes,sha256,summary,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::uuid)`, p.ID, p.ConversationID, p.RunID, p.ContextID, p.RequestHash, source, base, p.Base.StorageKey, p.Base.NativeBody, p.StorageKey, p.SizeBytes, p.SHA256, p.Summary, p.CreatedBy)
	if err != nil {
		return p, commentWriteError(err)
	}
	return scanProposal(tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM openfox.workspace_proposals p WHERE p.organization_id=openfox_private.current_organization_id() AND p.id=$1::uuid`, p.ID))
}
func (Service) ProposalBase(ctx context.Context, tx pgx.Tx, p Proposal) (File, error) {
	f := p.Base
	if f.Native {
		err := tx.QueryRow(ctx, `SELECT base_body FROM openfox.workspace_proposals WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.ID).Scan(&f.NativeBody)
		return f, err
	}
	return f, nil
}

// DecideProposal publishes metadata/body and the decision receipt in one short
// transaction. Validated candidate bytes are read before entering this method.
func (s Service) DecideProposal(ctx context.Context, tx pgx.Tx, org, conversation, id string, actor conversations.Actor, d ProposalDecision, body string) (Proposal, error) {
	var empty Proposal
	if !membership.ValidateUUID(d.OperationID) || d.Version < 1 || (d.Action != "accept" && d.Action != "reject") {
		return empty, ErrInvalid
	}
	if _, err := (conversations.Service{}).AuthorizeWrite(ctx, tx, org, conversation, actor); err != nil {
		return empty, err
	}
	p, err := s.GetProposal(ctx, tx, org, conversation, id, actor, true)
	if err != nil {
		return p, err
	}
	desired := "ACCEPTED"
	if d.Action == "reject" {
		desired = "REJECTED"
	}
	if p.Status != "PENDING" {
		if p.Status == desired && p.DecisionID == d.OperationID && p.DecidedBy == actor.UserID && p.Version == d.Version+1 {
			return p, nil
		}
		return p, ErrConflict
	}
	if p.Version != d.Version {
		return p, ErrConflict
	}
	if d.Action == "accept" {
		if int64(len(body)) != p.SizeBytes || Digest(body) != p.SHA256 {
			return p, ErrInvalid
		}
		if p.Source.NodeID != "" {
			q := Context{Input: p.Source, UserID: actor.UserID}
			if err = s.ApplyNative(ctx, tx, org, q, body); err != nil {
				return p, err
			}
			err = tx.QueryRow(ctx, `SELECT version FROM openfox.doc_nodes WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, p.Source.NodeID).Scan(&p.ResultNodeVersion)
			if err != nil {
				return p, err
			}
		} else {
			// The revision trigger locks the content root and requires the exact base head.
			f, e := s.CommentFile(ctx, tx, org, conversation, actor, p.Source)
			if e != nil {
				return p, e
			}
			if f.Version != p.Base.Version {
				return p, ErrConflict
			}
			err = tx.QueryRow(ctx, `INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type,retention_until,parent_artifact_id)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9::uuid) RETURNING id::text`, p.ID, p.RunID, p.Base.Name, p.StorageKey, p.SizeBytes, p.SHA256, p.Base.MIMEType, p.Base.RetentionUntil, p.Source.ArtifactID).Scan(&p.ResultArtifactID)
			if err != nil {
				return p, commentWriteError(err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO openfox.organization_events(organization_id,type,entity,entity_id,space_id,conversation_id,payload) SELECT organization_id,'run.artifact','artifact',$1::uuid,space_id,conversation_id,jsonb_build_object('run_id',id::text) FROM openfox.runs WHERE organization_id=openfox_private.current_organization_id() AND id=$2::uuid`, p.ID, p.RunID)
			if err != nil {
				return p, err
			}
		}
		if p.ContextID != "" {
			if err = s.Applied(ctx, tx, Context{Input: Input{ID: p.ContextID}}, p.RequestHash, p.ResultArtifactID, p.SHA256); err != nil {
				return p, err
			}
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.workspace_proposals SET status=$2,version=version+1,decision_id=$3::uuid,decided_by=$4::uuid,decided_at=clock_timestamp(),result_artifact_id=NULLIF($5,'')::uuid,result_node_version=NULLIF($6,0) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$7 AND status='PENDING'`, p.ID, desired, d.OperationID, actor.UserID, p.ResultArtifactID, p.ResultNodeVersion, d.Version)
	if err != nil {
		return p, commentWriteError(err)
	}
	if tag.RowsAffected() != 1 {
		return p, ErrConflict
	}
	if err = audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{ID: d.OperationID, OrganizationID: org, ActorID: actor.UserID, Action: "workspace.proposal." + d.Action, Decision: "allow", Reason: "allowed", Resource: audit.ResourceRef{Kind: "workspace_proposal", OrganizationID: org, ID: p.ID}, RequestID: d.OperationID, AuthorizationVersion: actor.AuthorizationVersion, CreatedAt: time.Now().UTC()}); err != nil {
		return p, err
	}
	p.Status = desired
	p.Version++
	p.DecisionID = d.OperationID
	p.DecidedBy = actor.UserID
	return p, nil
}
func StoredProposal(id, run string, q Context, hash, summary, key string, stored artifacts.Stored) Proposal {
	return Proposal{ID: id, RunID: run, ConversationID: q.ConversationID, ContextID: q.ID, Source: q.Input, Base: q.File, RequestHash: hash, Summary: summary, StorageKey: key, SizeBytes: stored.Size, SHA256: stored.SHA256, CreatedBy: q.UserID}
}
