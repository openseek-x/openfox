package integrations

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/policy"
)

type bitbucketSelection struct {
	BitbucketRepositorySelection
	Access         BitbucketAccess
	Actor          collaboration.Actor
	HookGeneration uint64
}

// The parent serializes selection changes, identity changes and disconnect.
// Lock order is parent, runtime, member, spaces, selection, then work/outbox.
func loadBitbucketWorkerSelection(ctx context.Context, tx pgx.Tx, org, connection, selection string) (bitbucketSelection, error) {
	var out bitbucketSelection
	var status string
	err := tx.QueryRow(ctx, `SELECT external_id,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION' FOR UPDATE`, connection).Scan(&out.Access.WorkspaceID, &out.Access.ConnectionVersion, &status)
	if err != nil {
		return out, bitbucketMissing(err)
	}
	if status != StatusActive {
		return out, ErrNeedsReauth
	}
	out.Access.ConnectionID = connection
	err = tx.QueryRow(ctx, `SELECT subject_id::text,consumer_id,credential_generation,credentials_version,hook_generation FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, connection).Scan(&out.Access.SubjectID, &out.Access.ConsumerID, &out.Access.CredentialGeneration, &out.Access.CredentialsVersion, &out.HookGeneration)
	if err != nil {
		return out, err
	}
	var actor string
	err = tx.QueryRow(ctx, `SELECT configured_by::text FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND connection_id=$2::uuid`, selection, connection).Scan(&actor)
	if err != nil {
		return out, bitbucketMissing(err)
	}
	out.Actor, err = bitbucketCurrentActor(ctx, tx, actor)
	if err != nil {
		return out, err
	}
	out.BitbucketRepositorySelection, err = loadBitbucketSelection(ctx, tx, selection)
	if err != nil {
		return out, err
	}
	if out.Status != "ACTIVE" || out.WorkspaceID != out.Access.WorkspaceID || len(out.SpaceIDs) == 0 {
		return out, ErrVersionConflict
	}
	for _, space := range out.SpaceIDs {
		if err = bitbucketSpacePermission(ctx, tx, org, out.Actor, space, policy.ActionCreateObject); err != nil {
			return out, err
		}
	}
	_, err = tx.Exec(ctx, `SELECT id FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, selection)
	return out, err
}

func bitbucketCurrentActor(ctx context.Context, tx pgx.Tx, id string) (collaboration.Actor, error) {
	var role, status string
	var actor collaboration.Actor
	err := tx.QueryRow(ctx, `SELECT role,status,authorization_version FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, id).Scan(&role, &status, &actor.AuthorizationVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return actor, ErrForbidden
	}
	if err != nil {
		return actor, err
	}
	if status != "ACTIVE" {
		return actor, ErrForbidden
	}
	actor.UserID = id
	actor.Role = policy.OrganizationRole(strings.ToLower(role))
	return actor, nil
}

// Attribution is optional, uses UUID only, and never changes an existing link.
// The caller already holds the parent lock, matching identity unlink's order.
func bitbucketAuthor(ctx context.Context, tx pgx.Tx, org, connection, external, space string) (*string, error) {
	if external == "" {
		return nil, nil
	}
	var owner, id string
	err := tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='bitbucket' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND external_id=$2 AND status='ACTIVE'`, connection, external).Scan(&id, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	actor, err := bitbucketCurrentActor(ctx, tx, owner)
	if errors.Is(err, ErrForbidden) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = bitbucketSpacePermission(ctx, tx, org, actor, space, policy.ActionReadSpace); err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, collaboration.ErrSpaceNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT status='ACTIVE' FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, id).Scan(&active)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	return &owner, nil
}

func bitbucketRepositoryKey(workspace, repo string) string { return workspace + "/" + repo }
func bitbucketPRURL(repo bitbucket.Repository, id int64) string {
	return bitbucket.RepositoryURL(repo.FullName) + "/pull-requests/" + fmt.Sprint(id)
}

func bitbucketFailureCode(err error) string {
	var rate *gitapi.ErrRateLimited
	var wait *BitbucketRetry
	switch {
	case errors.Is(err, ErrVersionConflict):
		return "stale_configuration"
	case errors.Is(err, ErrNeedsReauth), errors.Is(err, gitapi.ErrTokenRevoked), errors.Is(err, ErrBitbucketAppChanged):
		return "reauthorization_required"
	case errors.Is(err, ErrForbidden), errors.Is(err, bitbucket.ErrForbidden):
		return "permission_denied"
	case errors.Is(err, ErrNotFound), errors.Is(err, gitapi.ErrNotFound), errors.Is(err, collaboration.ErrSpaceNotFound):
		return "resource_unavailable"
	case errors.Is(err, bitbucket.ErrCommitLimit):
		return "commit_limit"
	case errors.Is(err, bitbucket.ErrIncomplete):
		return "incomplete_result"
	case errors.Is(err, bitbucket.ErrInvalid):
		return "invalid_upstream_response"
	case errors.As(err, &rate):
		return "rate_limited"
	case errors.As(err, &wait):
		return "request_busy"
	default:
		return "upstream_unavailable"
	}
}

func bitbucketRetryAt(err error, attempt int) *time.Time {
	if attempt >= 5 {
		return nil
	}
	code := bitbucketFailureCode(err)
	if code == "stale_configuration" || code == "permission_denied" || code == "resource_unavailable" || code == "reauthorization_required" || code == "commit_limit" || code == "invalid_upstream_response" {
		return nil
	}
	d := 5 * time.Second * time.Duration(1<<min(attempt, 5))
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	var wait *BitbucketRetry
	if errors.As(err, &wait) && wait.After > d {
		d = wait.After
	}
	var rate *gitapi.ErrRateLimited
	if errors.As(err, &rate) && time.Until(rate.ResetAt) > d {
		d = time.Until(rate.ResetAt)
	}
	if d > time.Hour {
		d = time.Hour
	}
	if d < time.Second {
		d = time.Second
	}
	d += time.Duration(rand.Int63n(int64(d/10 + 1)))
	if d > time.Hour {
		d = time.Hour
	}
	at := time.Now().Add(d)
	return &at
}

// Resolve candidates only in the explicitly selected spaces, including old
// prefixes/aliases. A moved task outside those spaces is never returned.
func bitbucketResolveRefs(ctx context.Context, tx pgx.Tx, refs []TaskRef, spaces []string) (map[string]resolvedTask, error) {
	out := map[string]resolvedTask{}
	if len(refs) == 0 {
		return out, nil
	}
	prefix := make([]string, len(refs))
	seq := make([]int64, len(refs))
	for i, r := range refs {
		prefix[i] = r.Prefix
		seq[i] = r.Sequence
	}
	rows, err := tx.Query(ctx, `WITH c AS (SELECT DISTINCT lower(prefix) AS prefix,seq FROM unnest($1::text[],$2::bigint[]) u(prefix,seq)),
 sp AS (SELECT c.prefix,c.seq,s.id space_id,0 rank FROM c JOIN openfox.spaces s ON s.organization_id=openfox_private.current_organization_id() AND s.id=ANY($3::uuid[]) AND lower(s.prefix)=c.prefix AND s.deleted_at IS NULL
 UNION ALL SELECT c.prefix,c.seq,s.id,1 FROM c JOIN openfox.space_prefix_history h ON h.organization_id=openfox_private.current_organization_id() AND lower(h.prefix)=c.prefix JOIN openfox.spaces s ON (s.organization_id,s.id)=(h.organization_id,h.space_id) AND s.id=ANY($3::uuid[]) AND s.deleted_at IS NULL),
 hit AS (SELECT sp.prefix,sp.seq,t.id,sp.rank FROM sp JOIN openfox.tasks t ON t.organization_id=openfox_private.current_organization_id() AND t.space_id=sp.space_id AND t.sequence=sp.seq AND t.deleted_at IS NULL
 UNION ALL SELECT sp.prefix,sp.seq,a.task_id,sp.rank+2 FROM sp JOIN openfox.task_display_aliases a ON a.organization_id=openfox_private.current_organization_id() AND a.space_id=sp.space_id AND a.sequence=sp.seq),
 best AS (SELECT DISTINCT ON(prefix,seq) prefix,seq,id FROM hit ORDER BY prefix,seq,rank,id)
 SELECT best.prefix,best.seq,t.id::text,t.space_id::text,s.prefix||'-'||t.sequence::text FROM best JOIN openfox.tasks t ON t.organization_id=openfox_private.current_organization_id() AND t.id=best.id AND t.space_id=ANY($3::uuid[]) AND t.deleted_at IS NULL JOIN openfox.spaces s ON (s.organization_id,s.id)=(t.organization_id,t.space_id) AND s.deleted_at IS NULL ORDER BY t.id FOR UPDATE OF t`, prefix, seq, spaces)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var n int64
		var t resolvedTask
		if err = rows.Scan(&p, &n, &t.TaskID, &t.SpaceID, &t.DisplayID); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s-%d", p, n)] = t
	}
	return out, rows.Err()
}

func lockBitbucketBacklinkTasks(ctx context.Context, tx pgx.Tx, b bitbucketBacklink, spaces []string) error {
	rows, err := tx.Query(ctx, `SELECT l.id::text FROM openfox.task_git_links l JOIN openfox.tasks t ON (t.organization_id,t.id)=(l.organization_id,l.task_id) WHERE l.organization_id=openfox_private.current_organization_id() AND l.id=ANY($1::uuid[]) AND l.provider='bitbucket' AND l.kind='PULL_REQUEST' AND l.repository=$2 AND l.number=$3 AND t.deleted_at IS NULL AND t.space_id=ANY($4::uuid[]) ORDER BY t.id,l.id FOR SHARE OF t,l`, b.LinkIDs, bitbucketRepositoryKey(b.WorkspaceID, b.RepositoryID), b.PR, spaces)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != len(b.LinkIDs) {
		return ErrVersionConflict
	}
	return nil
}

func stopBitbucketConnectionWork(ctx context.Context, tx pgx.Tx, connection string) error {
	statements := []string{
		`UPDATE openfox.integration_bitbucket_repositories SET hook_generation=0,version=version+1,last_error='connection_disconnected',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND status<>'REMOVED'`,
		`UPDATE openfox.integration_bitbucket_work SET state='IGNORED',requested_version=requested_version+1,next_attempt_at=NULL,lease=NULL,lease_until=NULL,last_error='connection_disconnected',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state IN ('PENDING','RUNNING')`,
		`UPDATE openfox.integration_bitbucket_backlinks SET state=CASE WHEN state='PENDING' THEN 'SKIPPED' ELSE 'UNKNOWN' END,next_attempt_at=NULL,lease=NULL,lease_until=NULL,last_error=CASE WHEN state='SENDING' THEN 'send_result_unknown' ELSE 'connection_disconnected' END,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state IN ('PENDING','SENDING','UNKNOWN')`,
		`UPDATE openfox.integration_webhook_deliveries SET status='IGNORED',detail='connection_disconnected' WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND status='QUEUED'`,
	}
	for _, q := range statements {
		if _, err := tx.Exec(ctx, q, connection); err != nil {
			return err
		}
	}
	return nil
}
