package imports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

var errSyncComments = errors.New("source comments need attention")

type sourceComment struct{ id, author, name, body, created string }

func sourceCommentClientID(instance, issue, comment string) string {
	return collaboration.ImportCommentClientID("source-comment:"+instance+":"+issue, time.Time{}, comment, "")
}

func (j *syncJob) sourceComments(ctx context.Context, external string) ([]sourceComment, error) {
	out := []sourceComment{}
	if j.zen != nil {
		v, e := j.zen.Comments(ctx, external)
		if e != nil {
			return nil, e
		}
		for _, c := range v {
			out = append(out, sourceComment{id: c.ID.String(), name: c.Actor, body: c.Body, created: c.Date})
		}
		return out, nil
	}
	seen := map[string]bool{}
	for offset := 0; offset < 2000; {
		page, total, e := j.jira.Comments(ctx, external, offset)
		if e != nil {
			return nil, e
		}
		if total > 2000 || total < 0 || (len(page) == 0 && offset < total) {
			return nil, ErrInvalidRequest
		}
		for _, c := range page {
			if c.ID == "" || len(c.ID) > 255 || seen[c.ID] || len(c.Body) > 64000 {
				return nil, ErrInvalidRequest
			}
			seen[c.ID] = true
			out = append(out, sourceComment{id: c.ID, author: c.Author.Identity(), name: c.Author.DisplayName, body: c.Body, created: c.Created})
		}
		offset += len(page)
		if offset >= total {
			return out, nil
		}
	}
	return nil, ErrInvalidRequest
}

// Page across linked tasks independently of title/status changes. A full page
// schedules a continuation, so old tasks cannot starve newly arriving comments.
func (w *Worker) syncProjectComments(ctx context.Context, j *syncJob) error {
	if j.jira == nil && j.zen == nil {
		return nil
	}
	type target struct{ external, task string }
	var targets []target
	e := w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT i.external_id,i.task_id::text FROM openfox.import_sync_items i
   JOIN openfox.import_sync_links l USING(organization_id,job_id)
   JOIN openfox.tasks t ON t.organization_id=i.organization_id AND t.id=i.task_id
   WHERE i.organization_id=openfox_private.current_organization_id() AND i.job_id=$1::uuid
   AND i.external_id>l.comments_cursor AND t.deleted_at IS NULL AND t.space_id=l.space_id
   ORDER BY i.external_id LIMIT 11`, j.job.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v target
			if e = rows.Scan(&v.external, &v.task); e != nil {
				return e
			}
			targets = append(targets, v)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	more := len(targets) > 10
	if more {
		targets = targets[:10]
	}
	conflict := false
	for _, t := range targets {
		comments, e := j.sourceComments(ctx, t.external)
		if e != nil {
			return errors.Join(errSyncComments, e)
		}
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			var alive bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.tasks WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid AND deleted_at IS NULL)`, t.task, j.job.SpaceID).Scan(&alive); e != nil {
				return e
			}
			if !alive {
				return nil
			}
			for _, c := range comments {
				if strings.TrimSpace(c.body) == "" {
					continue
				}
				changed, e := applySourceComment(ctx, tx, j, t.external, t.task, c)
				if errors.Is(e, errSyncConflict) {
					conflict = true
					continue
				}
				if e != nil {
					return e
				}
				_ = changed
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET comments_cursor=$3 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, j.job.ID, j.lease, t.external)
			return e
		})
		if e != nil {
			return e
		}
	}
	if !more {
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET comments_cursor='' WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND lease=$2::uuid`, j.job.ID, j.lease)
			return e
		})
		if e != nil {
			return e
		}
	}
	if conflict {
		return errSyncConflict
	}
	if more {
		return errSyncBacklog
	}
	return nil
}

func applySourceComment(ctx context.Context, tx pgx.Tx, j *syncJob, external, task string, c sourceComment) (bool, error) {
	var id, oldSource, oldLocal string
	e := tx.QueryRow(ctx, `SELECT comment_id::text,source_body,local_body FROM openfox.import_sync_comments WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND external_id=$2 AND remote_id=$3 FOR UPDATE`, j.job.ID, external, c.id).Scan(&id, &oldSource, &oldLocal)
	exists := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return false, e
	}
 if !exists {
  var pending bool
  if e=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM openfox.import_comment_deliveries WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND external_id=$2 AND state='SENDING' AND body=$3)`,j.job.ID,external,c.body).Scan(&pending);e!=nil{return false,e}
  if pending{return false,nil} // Delivery remains visibly unresolved; never infer authorship from matching text.
 }
	if exists && oldSource == c.body {
		return false, nil
	}
	at, hasAt := ParseDate(c.created, j.settings.location)
	author, name := j.actor.UserID, c.name
	mapped := false
	if u, ok := j.resolved.Users[c.author]; ok && c.author != "" && u.UserID != "" && (collaboration.Service{}).ImportAssignable(ctx, tx, j.job.SpaceID, j.actor, u.UserID) {
		author, mapped = u.UserID, true
	}
	if name == "" {
		name = c.author
	}
	if name == "" {
		name = "未知用户"
	}
	body := strings.TrimSpace(c.body)
	if !mapped {
		source := "Jira"
		if j.zen != nil {
			source = "禅道"
		}
		when := "时间未知"
		if hasAt {
			when = at.Format("2006-01-02 15:04")
		}
		body = fmt.Sprintf("**%s**（来自 %s · %s）\n\n%s", name, source, when, body)
	}
	body, _ = TruncateRunes(body, maxStoredCommentRunes)
	if exists {
		var current string
		e = tx.QueryRow(ctx, `SELECT body FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND task_id=$2::uuid FOR UPDATE`, id, task).Scan(&current)
		if errors.Is(e, pgx.ErrNoRows) {
			return false, nil
		} // Local deletion is retained.
		if e != nil {
			return false, e
		}
		if current != oldLocal && current != body {
			return false, errSyncConflict
		}
		if current != body {
			if _, e = tx.Exec(ctx, `UPDATE openfox.task_comments SET body=$2,edit_version=edit_version+1,edited_at=clock_timestamp(),original_body_hash=COALESCE(original_body_hash,sha256(convert_to(body,'UTF8'))) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, body); e != nil {
				return false, e
			}
		}
	} else {
		stable := sourceCommentClientID(j.job.Preview.Origin.Instance, external, c.id)
		legacy := collaboration.ImportCommentClientID("jira:"+external, at, c.author, strings.TrimSpace(c.body))
		var current string
		e = tx.QueryRow(ctx, `SELECT id::text,body FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1::uuid AND client_comment_id=ANY($2::text[]) ORDER BY created_at LIMIT 1 FOR UPDATE`, task, []string{stable, legacy}).Scan(&id, &current)
		if e == nil {
			body = current
		} else if errors.Is(e, pgx.ErrNoRows) {
			var timestamp any
			if hasAt {
				timestamp = at
			}
			e = tx.QueryRow(ctx, `INSERT INTO openfox.task_comments(organization_id,space_id,task_id,author_user_id,body,client_comment_id,created_at)
    VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,COALESCE($6::timestamptz,clock_timestamp())) ON CONFLICT DO NOTHING RETURNING id::text`, j.job.SpaceID, task, author, body, stable, timestamp).Scan(&id)
			if errors.Is(e, pgx.ErrNoRows) {
				return false, nil
			} // Includes a deleted comment's replay key.
		}
		if e != nil {
			return false, e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.import_sync_comments(organization_id,job_id,external_id,remote_id,comment_id,source_body,local_body)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,$5,$6)
 ON CONFLICT(organization_id,job_id,external_id,remote_id) DO UPDATE SET source_body=excluded.source_body,local_body=excluded.local_body`, j.job.ID, external, c.id, id, c.body, body)
	return e == nil, e
}
