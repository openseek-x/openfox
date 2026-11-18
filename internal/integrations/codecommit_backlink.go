package integrations

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/platform/requestid"
)

func codeCommitBacklinkItems(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, ids []string, connection string) ([]BacklinkItem, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, ErrInvalidRequest
	}
	rows, e := tx.Query(ctx, `SELECT l.id::text,t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text FROM openfox.task_git_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.id=ANY($1::uuid[]) AND l.connection_id=$2::uuid AND l.provider='codecommit' AND l.kind='PULL_REQUEST' AND t.deleted_at IS NULL ORDER BY t.space_id,t.id`, ids, connection)
	if e != nil {
		return nil, e
	}
	items := []BacklinkItem{}
	tasks := []matchedTask{}
	for rows.Next() {
		var item BacklinkItem
		var task matchedTask
		if e = rows.Scan(&item.LinkID, &task.TaskID, &task.SpaceID, &task.DisplayID); e != nil {
			rows.Close()
			return nil, e
		}
		item.SpaceID, item.DisplayID = task.SpaceID, task.DisplayID
		items = append(items, item)
		tasks = append(tasks, task)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(items) != len(ids) {
		return nil, ErrForbidden
	}
	checked, e := codeCommitTasks(ctx, tx, org, actor, tasks)
	if e != nil {
		return nil, e
	}
	if len(checked) != len(tasks) {
		return nil, ErrForbidden
	}
	sort.Slice(items, func(i, j int) bool { return items[i].LinkID < items[j].LinkID })
	return items, nil
}
func codeCommitBacklinkPermission(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, ids []string, connection string) error {
	_, e := codeCommitBacklinkItems(ctx, tx, org, actor, ids, connection)
	return e
}
func codeCommitBacklinkAudit(ctx context.Context, tx pgx.Tx, j *codeCommitWork, state string, intent codecommit.CommentIntent, comment string) error {
	result := intent.Fingerprint() + ":" + codeCommitDigest(intent.Token())
	if comment != "" {
		result += ":" + codeCommitDigest(comment)
	}
	return recordIMAudit(ctx, tx, j.Org, j.Actor.UserID, j.Actor.AuthorizationVersion, "integration.codecommit.backlink", "codecommit_work", j.ID, "", map[string]string{"source": "codecommit", "kind": "backlink", "version": strconv.FormatUint(j.BacklinkGeneration, 10), "status": state, "result": result})
}
func (w *SyncWorker) prepareCodeCommitBacklink(ctx context.Context, j *codeCommitWork, p codecommit.PullRequest) error {
	proof, e := j.Access.Client.CreationEvidence(ctx, j.Access.credentials, j.Repository.Name, j.PullID)
	if e != nil {
		return e
	}
	return w.withCodeCommitLease(ctx, j, true, func(ctx context.Context, tx pgx.Tx) error {
		current, e := readCodeCommitWork(ctx, tx, j.ID, false)
		if e != nil {
			return e
		}
		if current.BacklinkState == "SENDING" || current.BacklinkState == "UNKNOWN" {
			return ErrVersionConflict
		}
		settings, e := loadCodeCommitSettingsLocked(ctx, tx, j.Org)
		if e != nil {
			return e
		}
		if !settings.LinkPullRequests || !settings.PRBacklinkPrivate {
			j.LinkIDs = nil
			return nil
		}
		items, e := codeCommitBacklinkItems(ctx, tx, j.Org, j.Actor, j.LinkIDs, j.ConnectionID)
		if e != nil {
			return e
		}
		// A prior success marker is never cleared; only unsent links enter a new generation.
		unsent := []BacklinkItem{}
		for _, item := range items {
			var marker string
			if e = tx.QueryRow(ctx, `SELECT backlink_comment_id FROM openfox.task_git_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, item.LinkID).Scan(&marker); e != nil {
				return e
			}
			if marker == "" {
				unsent = append(unsent, item)
			}
		}
		if len(unsent) == 0 {
			j.LinkIDs = nil
			return nil
		}
		if len(current.Intent) > 0 {
			if current.BacklinkState == "SENT" {
				if e = verifyCodeCommitSentProof(ctx, tx, j, current); e != nil {
					return e
				}
			}
			old, e := codecommit.ParseCommentIntent(current.Intent)
			if e != nil {
				return e
			}
			if e = codeCommitBacklinkAudit(ctx, tx, j, "superseded", old, current.CommentID); e != nil {
				return e
			}
			j.BacklinkGeneration = current.BacklinkGeneration + 1
		} else {
			j.BacklinkGeneration = current.BacklinkGeneration
		}
		base, e := w.Service.codeCommitPublicURL()
		if e != nil {
			return e
		}
		token, e := randomBase64URL(32)
		if e != nil {
			return e
		}
		intent, e := codecommit.NewCommentIntent(j.Repository.Name, j.PullID, proof.BeforeCommitID, p.Targets[0].SourceCommit, token, BacklinkBody(base, j.Org, unsent))
		if e != nil {
			return e
		}
		raw, e := intent.MarshalJSON()
		if e != nil {
			return e
		}
		ids := []string{}
		for _, item := range unsent {
			ids = append(ids, item.LinkID)
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET backlink_generation=$2,backlink_state='PENDING',backlink_intent=$3::jsonb,backlink_link_ids=$4::uuid[],backlink_sender=$5,backlink_comment_id='',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, j.BacklinkGeneration, raw, ids, j.Access.settings.PrincipalARN)
		if e != nil {
			return e
		}
		j.BacklinkState, j.Intent, j.LinkIDs, j.Sender = "PENDING", raw, ids, j.Access.settings.PrincipalARN
		return codeCommitBacklinkAudit(ctx, tx, j, "prepared", intent, "")
	})
}
func (w *SyncWorker) sendCodeCommitBacklink(ctx context.Context, j *codeCommitWork) error {
	intent, e := codecommit.ParseCommentIntent(j.Intent)
	if e != nil {
		return e
	}
	if intent.RepositoryName() != j.Repository.Name || intent.PullRequestID() != j.PullID {
		return codecommit.ErrIdentityMismatch
	}
	e = w.withCodeCommitLease(ctx, j, true, func(ctx context.Context, tx pgx.Tx) error {
		current, e := readCodeCommitWork(ctx, tx, j.ID, false)
		if e != nil {
			return e
		}
		saved, e := codecommit.ParseCommentIntent(current.Intent)
		if e != nil || saved.Fingerprint() != intent.Fingerprint() || current.BacklinkGeneration != j.BacklinkGeneration {
			return ErrVersionConflict
		}
		if j.Sender != j.Access.settings.PrincipalARN {
			return errCodeCommitSenderChanged
		}
		settings, e := loadCodeCommitSettingsLocked(ctx, tx, j.Org)
		if e != nil {
			return e
		}
		if !settings.LinkPullRequests || !settings.PRBacklinkPrivate {
			return ErrForbidden
		}
		if e = codeCommitBacklinkPermission(ctx, tx, j.Org, j.Actor, j.LinkIDs, j.ConnectionID); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET backlink_state='SENDING',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID)
		if e != nil {
			return e
		}
		return codeCommitBacklinkAudit(ctx, tx, j, "sending", intent, "")
	})
	if e != nil {
		return e
	}
	result, cause := j.Access.Client.PostCommentForPullRequest(ctx, j.Access.credentials, intent)
	// An in-flight write cannot be revoked. Store only the fact for this exact intent
	// even when the parent/lease has changed; never re-enable or launch its next generation.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return w.settleCodeCommitBacklink(cleanup, j, intent, result, cause)
}

var errCodeCommitSenderChanged = errors.New("codecommit: sender changed")

func (w *SyncWorker) settleCodeCommitBacklink(ctx context.Context, j *codeCommitWork, intent codecommit.CommentIntent, result codecommit.CommentResult, cause error) error {
	return w.inOrganization(ctx, j.Org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := w.Service.readCodeCommit(ctx, tx, j.Org, j.ConnectionID, true, false)
		if e != nil {
			return e
		}
		r, e := loadCodeCommitRepository(ctx, tx, j.ConnectionID, j.RepositoryID, true)
		if e != nil {
			return e
		}
		current, e := readCodeCommitWork(ctx, tx, j.ID, true)
		if e != nil {
			return e
		}
		saved, e := codecommit.ParseCommentIntent(current.Intent)
		if e != nil || saved.Fingerprint() != intent.Fingerprint() || current.BacklinkGeneration != j.BacklinkGeneration {
			return errCodeCommitLeaseLost
		}
		if current.BacklinkState == "SENT" {
			return nil
		}
		state, code := "SENT", ""
		if cause != nil {
			state = "FAILED"
			code = codeCommitErrorCode(cause)
			if errors.Is(cause, codecommit.ErrUnknownResult) || j.BacklinkState == "UNKNOWN" {
				state = "UNKNOWN"
			}
		}
		active := a.Status == StatusActive && a.Version == j.Access.Version && r.Enabled && r.Generation == j.Generation && r.Version == j.Repository.Version && current.Lease == j.Lease
		if cause == nil {
			marker := codeCommitDigest(result.ID)
			tag, e := tx.Exec(ctx, `UPDATE openfox.task_git_links SET backlink_comment_id=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND provider='codecommit' AND connection_id=$3::uuid AND repository=$4 AND kind='PULL_REQUEST' AND number=$5 AND backlink_comment_id=''`, j.LinkIDs, marker, j.ConnectionID, codeCommitRepositoryKey(j.Access, j.Repository.RepositoryID), codeCommitPRNumber(j))
			if e != nil {
				return e
			}
			_ = tag
		}
		if e = codeCommitBacklinkAudit(ctx, tx, j, state, intent, result.ID); e != nil {
			return e
		}
		if cause == nil {
			if e = recordCodeCommitSentTasks(ctx, tx, j, intent, result.ID); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET backlink_state=$2,backlink_comment_id=$3,state=CASE WHEN NOT $4 THEN state WHEN $2='SENT' THEN 'PENDING' ELSE 'FAILED' END,next_attempt_at=CASE WHEN $4 AND $2='SENT' THEN clock_timestamp() ELSE NULL END,last_error=CASE WHEN $4 THEN $5 ELSE last_error END,lease_token=NULL,lease_until=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID, state, result.ID, active, code)
		return e
	})
}

func codeCommitSentAuditID(work string, generation uint64, task string) string {
	h := codeCommitDigest("codecommit-sent:" + work + ":" + strconv.FormatUint(generation, 10) + ":" + task)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
func recordCodeCommitSentTasks(ctx context.Context, tx pgx.Tx, j *codeCommitWork, intent codecommit.CommentIntent, comment string) error {
	rows, e := tx.Query(ctx, `SELECT task_id::text FROM openfox.task_git_links WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND connection_id=$2::uuid AND provider='codecommit' ORDER BY task_id`, j.LinkIDs, j.ConnectionID)
	if e != nil {
		return e
	}
	tasks := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		tasks = append(tasks, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, task := range tasks {
		event := audit.Event{ID: codeCommitSentAuditID(j.ID, j.BacklinkGeneration, task), OrganizationID: j.Org, ActorID: j.Actor.UserID, AuthorizationVersion: j.Actor.AuthorizationVersion, Action: "integration.codecommit.backlink_sent", Decision: "allow", Reason: "allowed", Resource: audit.ResourceRef{OrganizationID: j.Org, Kind: "task", ID: task}, RequestID: requestid.New(), CreatedAt: time.Now().UTC(), SafeMetadata: map[string]string{"source": "codecommit", "scope": j.ID, "version": strconv.FormatUint(j.BacklinkGeneration, 10), "result": intent.Fingerprint() + ":" + codeCommitDigest(comment), "status": "SENT"}}
		if e = audit.Record(ctx, audit.PGXWriter{Tx: tx}, event); e != nil {
			return e
		}
	}
	return nil
}
func verifyCodeCommitSentProof(ctx context.Context, tx pgx.Tx, j *codeCommitWork, current codeCommitWork) error {
	if len(current.LinkIDs) == 0 || len(current.LinkIDs) > 100 || current.CommentID == "" {
		return ErrVersionConflict
	}
	intent, e := codecommit.ParseCommentIntent(current.Intent)
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT task_id::text,backlink_comment_id FROM openfox.task_git_links WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND connection_id=$2::uuid AND provider='codecommit' ORDER BY task_id`, current.LinkIDs, j.ConnectionID)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var task, marker string
		if e = rows.Scan(&task, &marker); e != nil {
			rows.Close()
			return e
		}
		if marker != codeCommitDigest(current.CommentID) {
			rows.Close()
			return ErrVersionConflict
		}
		ids = append(ids, codeCommitSentAuditID(j.ID, current.BacklinkGeneration, task))
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) != len(current.LinkIDs) {
		return ErrVersionConflict
	}
	var count int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.audit_events WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($1::uuid[]) AND action='integration.codecommit.backlink_sent' AND safe_metadata->>'scope'=$2 AND safe_metadata->>'version'=$3 AND safe_metadata->>'result'=$4`, ids, j.ID, strconv.FormatUint(current.BacklinkGeneration, 10), intent.Fingerprint()+":"+codeCommitDigest(current.CommentID)).Scan(&count)
	if e != nil {
		return e
	}
	if count != len(ids) {
		return ErrVersionConflict
	}
	return nil
}
