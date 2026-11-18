package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

type AzureRepoWorkView struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	Kind         string `json:"kind"`
	ObjectID     int64  `json:"object_id"`
	State        string `json:"state"`
	LastError    string `json:"last_error"`
	Version      uint64 `json:"version"`
}
type AzureBacklinkView struct {
	ID           string `json:"id"`
	LinkID       string `json:"link_id"`
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
	Number       int64  `json:"pr_number"`
	State        string `json:"state"`
	LastError    string `json:"last_error"`
	ThreadID     *int64 `json:"thread_id,omitempty"`
	CommentID    *int64 `json:"comment_id,omitempty"`
}
type AzureReposStatus struct {
	Work              []AzureRepoWorkView `json:"work"`
	Backlinks         []AzureBacklinkView `json:"backlinks"`
	NextWorkAfter     string              `json:"next_work_after"`
	NextBacklinkAfter string              `json:"next_backlink_after"`
}

// Administrative status never includes the remote event payload, local task
// text, comment body, marker, or account credentials. Backlinks are visible
// only through a task in a currently readable space.
func (s Service) GetAzureReposStatus(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, workAfter, backlinkAfter string) (AzureReposStatus, error) {
	out := AzureReposStatus{Work: []AzureRepoWorkView{}, Backlinks: []AzureBacklinkView{}}
	if workAfter != "" && !membership.ValidateUUID(workAfter) || backlinkAfter != "" && !membership.ValidateUUID(backlinkAfter) {
		return out, ErrInvalidRequest
	}
	if _, e := s.GetAzureHooksConfig(ctx, tx, org, actor, connection); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT id::text,project_id::text,coalesce(repository_id::text,''),kind,object_id,state,last_error,version FROM openfox.integration_azure_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND (nullif($2,'')::uuid IS NULL OR id>nullif($2,'')::uuid) ORDER BY id LIMIT 101`, connection, workAfter)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v AzureRepoWorkView
		if e = rows.Scan(&v.ID, &v.ProjectID, &v.RepositoryID, &v.Kind, &v.ObjectID, &v.State, &v.LastError, &v.Version); e != nil {
			rows.Close()
			return out, e
		}
		out.Work = append(out.Work, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Work) > 100 {
		out.Work = out.Work[:100]
		out.NextWorkAfter = out.Work[99].ID
	}
	rows, e = tx.Query(ctx, `SELECT b.id::text,b.link_id::text,b.project_id::text,b.repository_id::text,b.pr_number,b.state,b.last_error,b.thread_id,b.comment_id FROM openfox.integration_azure_backlinks b JOIN openfox.task_git_links l ON l.organization_id=b.organization_id AND l.id=b.link_id JOIN openfox.tasks t ON t.organization_id=l.organization_id AND t.id=l.task_id AND t.space_id=l.space_id JOIN openfox.spaces s ON s.organization_id=t.organization_id AND s.id=t.space_id LEFT JOIN openfox.space_members m ON m.organization_id=s.organization_id AND m.space_id=s.id AND m.user_id=$3::uuid WHERE b.organization_id=openfox_private.current_organization_id() AND b.connection_id=$1::uuid AND (nullif($2,'')::uuid IS NULL OR b.id>nullif($2,'')::uuid) AND t.deleted_at IS NULL AND s.deleted_at IS NULL AND ((s.visibility='SHARED' AND m.user_id IS NULL) OR m.status='ACTIVE') ORDER BY b.id LIMIT 101`, connection, backlinkAfter, actor.UserID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v AzureBacklinkView
		if e = rows.Scan(&v.ID, &v.LinkID, &v.ProjectID, &v.RepositoryID, &v.Number, &v.State, &v.LastError, &v.ThreadID, &v.CommentID); e != nil {
			return out, e
		}
		out.Backlinks = append(out.Backlinks, v)
	}
	if len(out.Backlinks) > 100 {
		out.Backlinks = out.Backlinks[:100]
		out.NextBacklinkAfter = out.Backlinks[99].ID
	}
	return out, rows.Err()
}
