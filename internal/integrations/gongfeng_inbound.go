package integrations

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
)

func (s Service) applyGongfengIssue(ctx context.Context, tx pgx.Tx, j *GongfengWork, raw gongfeng.Issue) error {
	if raw.Confidential == nil {
		return gongfeng.ErrInvalid
	}
	if *raw.Confidential {
		var counted bool
		if e := tx.QueryRow(ctx, `SELECT COALESCE((payload->>'excluded')::boolean,false) FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID).Scan(&counted); e != nil {
			return e
		}
		if !counted {
			if _, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET excluded_confidential=excluded_confidential+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.RepositoryID); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET payload=payload||'{"excluded":true}'::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.ID)
		return e
	}
	common, e := raw.PublicIssue(gongfeng.Project{ID: j.Repository.ProjectID, Path: j.Repository.Path})
	if e != nil {
		return e
	}
	in := issueFromREST(common)
	svc := s.ForGit(ProviderGongfeng)
	key := gongfengRepositoryKey(j.Repository.ProjectID)
	existing, found, e := findIssueLink(ctx, tx, j.Link.ID, in.Number)
	if e != nil {
		return e
	}
	if !found {
		if task, ok := ParseTaskMarker(in.Body); ok {
			var valid bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL)`, task, j.Link.SpaceID).Scan(&valid); e != nil {
				return e
			}
			if valid {
				tag, e := tx.Exec(ctx, `INSERT INTO openfox.task_issue_links(organization_id,sync_link_id,space_id,task_id,repository,issue_number,issue_id,url,state,last_hash,remote_updated_at,issue_ref) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`, j.Link.ID, j.Link.SpaceID, task, key, in.Number, in.Ref, in.URL, LinkStateForIssue(in.State), in.hash(), in.UpdatedAt, in.Ref)
				if e != nil {
					return e
				}
				if tag.RowsAffected() != 1 {
					return ErrVersionConflict
				}
				return nil
			}
		}
		link, task, display, e := svc.createTaskFromIssue(ctx, tx, j.Org, j.Link, j.Actor, key, "gongfeng-in-"+j.ID+"-"+in.Ref, in)
		if e != nil {
			return e
		}
		if in.State == "closed" {
			if _, e = (collaboration.Service{}).GitSyncStatus(ctx, tx, task, true, false, j.Actor); e != nil {
				return e
			}
		}
		if j.Repository.BacklinkEnabled && j.Repository.Private != nil {
			body := IssueBacklinkBody(s.PublicURL, j.Org, IssueBacklinkJob{DisplayID: display, SpaceID: j.Link.SpaceID})
			return queueGongfengBacklink(ctx, tx, j, "issue:"+in.Ref, gongfengSendPayload{Kind: "issue", Ref: in.Ref, IssueLinkID: link, TaskID: task}, body)
		}
		return nil
	}
	if history, _ := gongfengPayload(j.Payload)["history"].(bool); history {
		return nil
	}
	var previous time.Time
	var alive bool
	var remoteID string
	e = tx.QueryRow(ctx, `SELECT COALESCE(l.remote_updated_at,'epoch'),t.deleted_at IS NULL AND t.space_id=$2::uuid,l.issue_id FROM openfox.task_issue_links l JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id WHERE l.organization_id=openfox_private.current_organization_id() AND l.id=$1::uuid FOR UPDATE OF l,t`, existing.ID, j.Link.SpaceID).Scan(&previous, &alive, &remoteID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if remoteID != in.Ref {
		return ErrVersionConflict
	}
	if !alive || in.UpdatedAt.Before(previous) {
		return nil
	}
	if in.UpdatedAt.Equal(previous) && in.hash() != existing.LastHash {
		return ErrVersionConflict
	}
	if in.hash() != existing.LastHash {
		if _, e = tx.Exec(ctx, `UPDATE openfox.tasks SET title=$2,description=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, existing.TaskID, truncateText(strings.TrimSpace(in.Title), maxIssueTitle), truncateText(StripTaskMarker(in.Body), maxIssueBody)); e != nil {
			return e
		}
		if LinkStateForIssue(in.State) != existing.State {
			if _, e = (collaboration.Service{}).GitSyncStatus(ctx, tx, existing.TaskID, in.State == "closed", false, j.Actor); e != nil {
				return e
			}
		}
	}
	// A REST refresh is not an assignee-change event. Existing local human and
	// Agent assignments are preserved; initial import may show external users.
	_, e = tx.Exec(ctx, `UPDATE openfox.task_issue_links SET last_hash=$2,state=$3,remote_updated_at=$4,url=$5,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, existing.ID, in.hash(), LinkStateForIssue(in.State), in.UpdatedAt, in.URL)
	return e
}

func (s Service) applyGongfengNote(ctx context.Context, tx pgx.Tx, j *GongfengWork, number int, n gongfeng.Note) error {
	if n.System == nil || *n.System || HasOpenFoxMarker(n.Body) || strings.TrimSpace(n.Body) == "" {
		return nil
	}
	link, found, e := findIssueLink(ctx, tx, j.Link.ID, number)
	if e != nil || !found {
		return e
	}
	var alive bool
	e = tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND space_id=$2::uuid FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, link.TaskID, j.Link.SpaceID).Scan(&alive)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if !alive {
		return nil
	}
	body := "**" + n.Author.Username + "**（来自腾讯工蜂 · " + n.CreatedAt.UTC().Format("2006-01-02 15:04") + "）\n\n" + n.Body
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_user_id,body,client_comment_id,created_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6) ON CONFLICT DO NOTHING RETURNING id::text`, j.Link.SpaceID, link.TaskID, j.Actor.UserID, truncateText(body, 65000), "gongfeng:"+strconv.FormatInt(j.Repository.ProjectID, 10)+":"+strconv.FormatInt(n.ID, 10), n.CreatedAt.Time).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.task_comment_links(organization_id,issue_link_id,comment_id,remote_comment_id,direction) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,'INBOUND')`, link.ID, id, strconv.FormatInt(n.ID, 10))
	return e
}
