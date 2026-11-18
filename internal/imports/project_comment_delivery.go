package imports

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations/mentiontext"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/integrations"
	"openfox/internal/platform/safehttp"
)

var errCommentIdentity = errors.New("personal comment account required")
var errCommentDeliveryUnknown = errors.New("comment delivery requires reconciliation")
var errCommentFallback = errors.New("comment used integration account")

// New comments are an at-most-once delivery. Unlike field updates, a comment
// POST is not idempotent: a lost response must never cause an automatic repost.
// Sent records and source IDs suppress loops. Local/remote deletions stay local.
func (w *Worker) sendProjectComments(ctx context.Context, j *syncJob) error {
	if j.direction == "INBOUND" || (j.jira == nil && j.zen == nil && j.src.Provider != "pingcode" && j.src.Provider != "shortcut") {
		return nil
	}
	type delivery struct{ id, task, external, body, author, state string }
	var all []delivery
	e := w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
		if e := w.checkProjectSync(ctx, tx, j); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT c.id::text,c.task_id::text,i.external_id,c.body,c.author_user_id::text,COALESCE(d.state,'')
 FROM openfox.import_sync_items i JOIN openfox.import_sync_links l USING(organization_id,job_id)
 JOIN openfox.task_comments c ON c.organization_id=i.organization_id AND c.task_id=i.task_id
 JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id
 LEFT JOIN openfox.import_comment_deliveries d ON d.organization_id=i.organization_id AND d.job_id=i.job_id AND d.comment_id=c.id
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.job_id=$1::uuid
 AND t.deleted_at IS NULL AND t.space_id=l.space_id AND c.author_user_id IS NOT NULL
 AND c.created_at>l.comments_outbound_after AND COALESCE(c.client_comment_id,'') NOT LIKE 'import:%'
 AND (d.state IS NULL OR d.state <> 'SENT')
 AND ($2<>'shortcut' OR d.state IS NOT NULL OR EXISTS (
 SELECT 1 FROM openfox.integration_connections personal
 JOIN openfox.organization_members member ON member.organization_id=personal.organization_id AND member.user_id=personal.owner_user_id
 JOIN openfox.users identity_user ON identity_user.id=member.user_id
 JOIN openfox.integration_connections parent ON parent.organization_id=personal.organization_id AND parent.id=personal.parent_connection_id
 JOIN openfox.spaces space ON space.organization_id=personal.organization_id AND space.id=l.space_id
 WHERE personal.organization_id=i.organization_id AND personal.provider='shortcut' AND personal.scope='PERSONAL'
 AND personal.account_kind='identity' AND personal.status='ACTIVE' AND personal.parent_connection_id=l.connection_id
 AND personal.owner_user_id=c.author_user_id AND personal.credentials_sealed IS NOT NULL
 AND personal.settings->>'auth_method'='shortcut_identity' AND personal.settings->>'comments_enabled'='true'
 AND personal.settings->>'company_id'=parent.external_id AND personal.settings->>'parent_version'=parent.version::text
 AND personal.settings->>'verified_email'=identity_user.normalized_email AND parent.status='ACTIVE'
 AND member.status='ACTIVE' AND member.role<>'GUEST'
 AND space.deleted_at IS NULL AND space.retired_at IS NULL
 AND (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=space.organization_id AND sm.space_id=space.id AND sm.user_id=member.user_id AND sm.status='ACTIVE' AND sm.role IN('OWNER','EDITOR','MEMBER'))
      OR (space.visibility='SHARED' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=space.organization_id AND sm.space_id=space.id AND sm.user_id=member.user_id)))
 AND c.created_at>(personal.settings->>'comments_authorized_at')::timestamptz))
 AND NOT EXISTS(SELECT 1 FROM openfox.import_sync_comments m WHERE m.organization_id=c.organization_id AND m.job_id=i.job_id AND m.comment_id=c.id)
 ORDER BY CASE WHEN d.state IS NOT NULL THEN 1
               WHEN $2 = 'pingcode' AND NOT EXISTS (
     SELECT 1 FROM openfox.integration_connections personal
     JOIN openfox.organization_members member
       ON member.organization_id=personal.organization_id AND member.user_id=personal.owner_user_id
     JOIN openfox.spaces space
       ON space.organization_id=personal.organization_id AND space.id=l.space_id
     WHERE personal.organization_id=i.organization_id AND personal.provider='pingcode'
       AND personal.scope='PERSONAL' AND personal.account_kind='sync_comments'
       AND personal.status='ACTIVE' AND personal.parent_connection_id=l.connection_id
       AND personal.owner_user_id=c.author_user_id AND personal.credentials_sealed IS NOT NULL
       AND personal.settings->>'auth_method'='pingcode_personal'
       AND member.status='ACTIVE' AND member.role<>'GUEST'
       AND space.deleted_at IS NULL AND space.retired_at IS NULL
       AND (EXISTS (SELECT 1 FROM openfox.space_members sm
                    WHERE sm.organization_id=space.organization_id AND sm.space_id=space.id
                      AND sm.user_id=member.user_id AND sm.status='ACTIVE'
                      AND sm.role IN ('OWNER','EDITOR','MEMBER'))
         OR (space.visibility='SHARED' AND NOT EXISTS (
             SELECT 1 FROM openfox.space_members sm
              WHERE sm.organization_id=space.organization_id AND sm.space_id=space.id
                AND sm.user_id=member.user_id)))
   ) THEN 2 ELSE 0 END, c.created_at,c.id LIMIT 21`, j.job.ID, j.src.Provider)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var d delivery
			if e = rows.Scan(&d.id, &d.task, &d.external, &d.body, &d.author, &d.state); e != nil {
				return e
			}
			all = append(all, d)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	fallback, attention, identity, progressed := false, false, false, false
	more := len(all) > 20
	if more {
		all = all[:20]
	}
	for _, d := range all {
		if d.state != "" {
			attention = true
			continue
		}
		var personal integrations.SitePersonalGrant
		var pingPersonal integrations.PingCodePersonalGrant
		var shortcutPersonal integrations.ShortcutPersonalGrant
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			// Both the original commenter and importer must still have access. Never
			// select another member's credentials or use a removed member's identity.
			var allowed bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.organization_members m JOIN openfox.spaces s ON s.organization_id=m.organization_id AND s.id=$1::uuid WHERE m.organization_id=openfox_private.current_organization_id() AND m.user_id=$2::uuid AND m.status='ACTIVE' AND m.role<>'GUEST' AND s.deleted_at IS NULL AND s.retired_at IS NULL AND (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id AND sm.status='ACTIVE' AND sm.role IN('OWNER','EDITOR','MEMBER')) OR (s.visibility='SHARED' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id))))`, j.job.SpaceID, d.author).Scan(&allowed); e != nil {
				return e
			}
			if !allowed {
				return errCommentIdentity
			}
			var e error
			if j.src.Provider == "shortcut" {
				shortcutPersonal, e = (integrations.Service{Box: w.Secrets, ProjectTransport: w.ProjectTransport}).LoadShortcutPersonal(ctx, tx, j.orgID, j.connection, d.author)
				if e != nil {
					return errCommentIdentity
				}
				return nil
			}
			if j.src.Provider == "pingcode" {
				pingPersonal, e = (integrations.Service{Box: w.Secrets}).LoadPingCodePersonal(ctx, tx, j.orgID, j.connection, d.author)
				if e != nil {
					return errCommentIdentity
				}
				return nil
			}
			personal, e = (integrations.Service{Box: w.Secrets}).LoadSitePersonal(ctx, tx, j.orgID, j.connection, d.author)
			if errors.Is(e, pgx.ErrNoRows) && j.jira != nil {
				fallback = true
				return nil
			}
			if e != nil {
				return errCommentIdentity
			}
			return nil
		})
		if e != nil {
			if errors.Is(e, errCommentIdentity) {
				identity = true
				continue
			}
			return e
		}
		var jira *jiraapi.Client
		var zen *zentao.PersonalCommentClient
		if j.jira != nil {
			jira = j.jira
			if personal.Jira != nil {
				client := safehttp.Client(w.Safe)
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				jira, e = jiraapi.New(jiraapi.Config{Deployment: personal.Deployment, SiteURL: personal.Site, Auth: *personal.Jira}, client)
			}
		} else if personal.Zen != nil {
			zen, e = zentao.ConnectPersonal(ctx, *personal.Zen, w.Safe)
		} else if j.src.Provider != "pingcode" && j.src.Provider != "shortcut" {
			identity = true
			continue
		}
		if e != nil {
			identity = true
			continue
		}
		claimed := false
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			if e := w.checkProjectSync(ctx, tx, j); e != nil {
				return e
			}
			if personal.ConnectionID != "" {
				fresh, e := (integrations.Service{Box: w.Secrets}).LoadSitePersonal(ctx, tx, j.orgID, j.connection, d.author)
				if e != nil || fresh.ConnectionID != personal.ConnectionID || fresh.Version != personal.Version {
					return errCommentIdentity
				}
			}
			if pingPersonal.ConnectionID != "" {
				fresh, e := (integrations.Service{Box: w.Secrets}).LoadPingCodePersonal(ctx, tx, j.orgID, j.connection, d.author)
				if e != nil || fresh.ConnectionID != pingPersonal.ConnectionID || fresh.Version != pingPersonal.Version {
					return errCommentIdentity
				}
			}
			if shortcutPersonal.ConnectionID != "" {
				fresh, e := (integrations.Service{Box: w.Secrets, ProjectTransport: w.ProjectTransport}).LoadShortcutPersonal(ctx, tx, j.orgID, j.connection, d.author)
				if e != nil || fresh.ConnectionID != shortcutPersonal.ConnectionID || fresh.Version != shortcutPersonal.Version {
					return errCommentIdentity
				}
			}
			var active bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.organization_members m JOIN openfox.spaces s ON s.organization_id=m.organization_id AND s.id=$1::uuid WHERE m.organization_id=openfox_private.current_organization_id() AND m.user_id=$2::uuid AND m.status='ACTIVE' AND m.role<>'GUEST' AND s.deleted_at IS NULL AND s.retired_at IS NULL AND (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id AND sm.status='ACTIVE' AND sm.role IN('OWNER','EDITOR','MEMBER')) OR (s.visibility='SHARED' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=m.user_id))))`, j.job.SpaceID, d.author).Scan(&active); e != nil {
				return e
			}
			if !active {
				return errCommentIdentity
			}
			var authorizedAfter *time.Time
			if shortcutPersonal.ConnectionID != "" {
				authorizedAfter = &shortcutPersonal.CommentsAuthorizedAt
			}
			tag, e := tx.Exec(ctx, `INSERT INTO openfox.import_comment_deliveries(organization_id,job_id,comment_id,external_id,body,author_user_id,state)
 SELECT openfox_private.current_organization_id(),$1::uuid,c.id,$3,c.body,c.author_user_id,'SENDING' FROM openfox.task_comments c
 JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id
 WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$2::uuid AND c.body=$4 AND c.author_user_id=$5::uuid AND t.deleted_at IS NULL AND t.space_id=$6::uuid AND ($7::timestamptz IS NULL OR c.created_at>$7::timestamptz)
 ON CONFLICT DO NOTHING`, j.job.ID, d.id, d.external, d.body, d.author, j.job.SpaceID, authorizedAfter)
			claimed = tag.RowsAffected() == 1
			return e
		})
		if e != nil {
			if errors.Is(e, errCommentIdentity) {
				identity = true
				continue
			}
			return e
		}
		if !claimed {
			continue
		}
		// 外部系统不认识提及令牌：出站时转成 @名字（本地记录与去重仍用原始正文）。
		outboundBody := mentiontext.Plain(d.body)
		var remote string
		if j.src.Provider == "shortcut" {
			remote, e = shortcutPersonal.CreateComment(ctx, d.external, outboundBody)
		} else if j.src.Provider == "pingcode" {
			remote, e = pingPersonal.CreateComment(ctx, d.external, outboundBody)
		} else if jira != nil {
			remote, e = jira.CreateComment(ctx, d.external, outboundBody)
		} else {
			parts := strings.Split(d.external, "/")
			if len(parts) != 3 {
				return ErrInvalidRequest
			}
			remote, e = zen.CreateComment(ctx, parts[1], parts[2], outboundBody)
		}
		if e != nil {
			return errCommentDeliveryUnknown
		}
		e = w.inOrganization(ctx, j.orgID, func(ctx context.Context, tx pgx.Tx) error {
			// Acknowledge even when a concurrent pause has revoked the lease: the
			// network side effect already happened. This never authorizes another send.
			_, e := tx.Exec(ctx, `UPDATE openfox.import_comment_deliveries SET state='SENT',remote_id=$3 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND comment_id=$2::uuid AND state='SENDING'`, j.job.ID, d.id, remote)
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO openfox.import_sync_comments(organization_id,job_id,external_id,remote_id,comment_id,source_body,local_body) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,$5,$5) ON CONFLICT DO NOTHING`, j.job.ID, d.external, remote, d.id, d.body)
			return e
		})
		if e != nil {
			return e
		}
		progressed = true
	}
	if more && progressed {
		return errSyncBacklog
	}
	if identity {
		return errCommentIdentity
	}
	if attention {
		return errCommentDeliveryUnknown
	}
	if fallback {
		return errCommentFallback
	}
	if more {
		return errSyncBacklog
	}
	return nil
}
