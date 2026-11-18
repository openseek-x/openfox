package integrations

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// Selection is deliberately separate from Issue sync links. Repository UUIDs
// route API requests; FullName is display-only and may change after a rename.
type BitbucketRepositorySelection struct {
	ID              string   `json:"id"`
	ConnectionID    string   `json:"connection_id"`
	WorkspaceID     string   `json:"workspace_id"`
	RepositoryID    string   `json:"repository_id"`
	FullName        string   `json:"full_name"`
	URL             string   `json:"url"`
	Private         *bool    `json:"is_private"`
	SpaceIDs        []string `json:"space_ids"`
	PullRequests    bool     `json:"pull_requests"`
	Commits         bool     `json:"commits"`
	BacklinkPrivate bool     `json:"backlink_private"`
	BacklinkPublic  bool     `json:"backlink_public"`
	Status          string   `json:"status"`
	ErrorCode       string   `json:"error_code"`
	HookReady       bool     `json:"hook_ready"`
	Version         uint64   `json:"version"`
}

type BitbucketRepositoryInput struct {
	RepositoryID      string   `json:"repository_id"`
	ConnectionVersion uint64   `json:"connection_version"`
	Version           uint64   `json:"version"` // zero creates a new selection
	SpaceIDs          []string `json:"space_ids"`
	PullRequests      *bool    `json:"pull_requests"`
	Commits           *bool    `json:"commits"`
	BacklinkPrivate   *bool    `json:"backlink_private"`
	BacklinkPublic    *bool    `json:"backlink_public"`
	ConfirmMetadata   bool     `json:"confirm_metadata"`
	ConfirmSender     bool     `json:"confirm_sender"`
	ConfirmPublic     bool     `json:"confirm_public"`
}

func (in BitbucketRepositoryInput) normalized() (BitbucketRepositoryInput, error) {
	id, err := bitbucket.UUID(in.RepositoryID)
	if err != nil || in.ConnectionVersion == 0 || len(in.SpaceIDs) < 1 || len(in.SpaceIDs) > 100 || in.PullRequests == nil || in.Commits == nil || in.BacklinkPrivate == nil || in.BacklinkPublic == nil || !in.ConfirmMetadata {
		return in, ErrInvalidRequest
	}
	if (*in.BacklinkPrivate || *in.BacklinkPublic) && !in.ConfirmSender || *in.BacklinkPublic && !in.ConfirmPublic {
		return in, ErrInvalidRequest
	}
	in.RepositoryID, in.SpaceIDs = id, append([]string(nil), in.SpaceIDs...)
	for i, space := range in.SpaceIDs {
		if !membership.ValidateUUID(space) {
			return in, ErrInvalidRequest
		}
		in.SpaceIDs[i] = strings.ToLower(space)
	}
	sort.Strings(in.SpaceIDs)
	for i := 1; i < len(in.SpaceIDs); i++ {
		if in.SpaceIDs[i] == in.SpaceIDs[i-1] {
			return in, ErrInvalidRequest
		}
	}
	return in, nil
}

// Parent then runtime then current member is also the authorization/disconnect
// lock order. Network validation is performed after this short transaction.
func (s Service) bitbucketRepositoryAccess(ctx context.Context, tx pgx.Tx, org, id string, actor collaboration.Actor, active, lock bool) (BitbucketAccess, error) {
	var a BitbucketAccess
	if !membership.ValidateUUID(id) {
		return a, ErrNotFound
	}
	q := `SELECT external_id,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION'`
	if lock {
		q += ` FOR UPDATE`
	}
	var status string
	if err := tx.QueryRow(ctx, q, id).Scan(&a.WorkspaceID, &a.ConnectionVersion, &status); err != nil {
		return a, bitbucketMissing(err)
	}
	q = `SELECT subject_id::text,consumer_id,credential_generation,credentials_version FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, q, id).Scan(&a.SubjectID, &a.ConsumerID, &a.CredentialGeneration, &a.CredentialsVersion); err != nil {
		return a, bitbucketMissing(err)
	}
	var member string
	if err := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&member); err != nil {
		return a, ErrForbidden
	}
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return a, err
	}
	if active && status != StatusActive {
		return a, ErrNeedsReauth
	}
	a.ConnectionID = strings.ToLower(id)
	return a, nil
}

func (s Service) PrepareBitbucketRepositories(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string) (BitbucketAccess, error) {
	a, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, true, false)
	if err == nil && !s.bitbucketConfigured(false) {
		return a, ErrNotConfigured
	}
	return a, err
}

func (s Service) PrepareBitbucketRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string, input BitbucketRepositoryInput) (BitbucketAccess, error) {
	in, err := input.normalized()
	if err != nil {
		return BitbucketAccess{}, err
	}
	a, err := s.PrepareBitbucketRepositories(ctx, tx, org, actor, connection)
	if err != nil {
		return a, err
	}
	if a.ConnectionVersion != in.ConnectionVersion {
		return a, ErrVersionConflict
	}
	for _, id := range in.SpaceIDs {
		if err := bitbucketSpacePermission(ctx, tx, org, actor, id, policy.ActionCreateObject); err != nil {
			return a, err
		}
	}
	return a, nil
}

const bitbucketSelectionColumns = `r.id::text,r.connection_id::text,r.workspace_id::text,r.repository_id::text,r.full_name,r.is_private,
 ARRAY(SELECT rs.space_id::text FROM openfox.integration_bitbucket_repository_spaces rs WHERE rs.organization_id=r.organization_id AND rs.repository_id=r.id ORDER BY rs.space_id),
 r.pull_requests,r.commits,r.backlink_private,r.backlink_public,r.status,r.last_error,
 (r.hook_generation=rt.hook_generation AND r.hook_generation>0),r.version`

func scanBitbucketSelection(row interface{ Scan(...any) error }) (BitbucketRepositorySelection, error) {
	var out BitbucketRepositorySelection
	err := row.Scan(&out.ID, &out.ConnectionID, &out.WorkspaceID, &out.RepositoryID, &out.FullName, &out.Private, &out.SpaceIDs,
		&out.PullRequests, &out.Commits, &out.BacklinkPrivate, &out.BacklinkPublic, &out.Status, &out.ErrorCode, &out.HookReady, &out.Version)
	out.URL = bitbucket.RepositoryURL(out.FullName)
	return out, err
}

func loadBitbucketSelection(ctx context.Context, tx pgx.Tx, id string) (BitbucketRepositorySelection, error) {
	return scanBitbucketSelection(tx.QueryRow(ctx, `SELECT `+bitbucketSelectionColumns+` FROM openfox.integration_bitbucket_repositories r JOIN openfox.integration_bitbucket_runtime rt ON (rt.organization_id,rt.connection_id)=(r.organization_id,r.connection_id) WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1::uuid`, id))
}

func (s Service) ListBitbucketSelections(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string) ([]BitbucketRepositorySelection, error) {
	if _, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, false, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+bitbucketSelectionColumns+` FROM openfox.integration_bitbucket_repositories r JOIN openfox.integration_bitbucket_runtime rt ON (rt.organization_id,rt.connection_id)=(r.organization_id,r.connection_id) WHERE r.organization_id=openfox_private.current_organization_id() AND r.connection_id=$1::uuid AND r.status<>'REMOVED' ORDER BY r.full_name,r.id LIMIT 100`, connection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BitbucketRepositorySelection, 0)
	for rows.Next() {
		item, e := scanBitbucketSelection(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func bitbucketSpacePermission(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, space string, action policy.Action) error {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL FOR SHARE`, space).Scan(&locked); err != nil {
		return ErrNotFound
	}
	// An existing membership must remain stable while deciding visibility. A
	// missing membership does not grant private-space access through org admin.
	if _, err := tx.Exec(ctx, `SELECT user_id FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, space, actor.UserID); err != nil {
		return err
	}
	a, err := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, org, space, actor.UserID)
	if err != nil {
		return err
	}
	visibility := policy.VisibilityPrivate
	if a.Space.Visibility == "SHARED" {
		visibility = policy.VisibilityPublic
	}
	r := policy.Request{Action: action, Actor: policy.Actor{OrganizationID: org, ActorID: actor.UserID, Role: actor.Role, Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: org, ID: space, Visibility: visibility}}
	if a.Membership != nil {
		var role policy.SpaceRole
		switch a.Membership.Role {
		case "OWNER", "EDITOR":
			role = policy.SpaceRoleAdministrator
		case "MEMBER":
			role = policy.SpaceRoleMember
		case "VIEWER":
			role = policy.SpaceRoleVisitor
		}
		r.SpaceMembership = &policy.SpaceMembership{OrganizationID: org, SpaceID: space, ActorID: actor.UserID, Role: role, Membership: policy.MembershipState(strings.ToLower(a.Membership.Status))}
	}
	if action != policy.ActionReadSpace {
		r.Object = &policy.Object{OrganizationID: org, SpaceID: space, ID: space}
	}
	decision := policy.Authorize(r)
	if !decision.Allowed() {
		if decision.HTTPStatus() == 404 {
			return ErrNotFound
		}
		return ErrForbidden
	}
	if collaboration.IsObjectWrite(action) {
		return a.RequireWritable()
	}
	return nil
}

// Save consumes a fresh REST proof made under target's request fence. Callers
// must use Runtime.DoReadBatch to obtain verified, never a request-body object.
func (s Service) SaveBitbucketRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, target BitbucketAccess, in BitbucketRepositoryInput, verified bitbucket.Repository) (BitbucketRepositorySelection, error) {
	var out BitbucketRepositorySelection
	in, err := in.normalized()
	if err != nil {
		return out, err
	}
	a, err := s.bitbucketRepositoryAccess(ctx, tx, org, target.ConnectionID, actor, true, true)
	if err != nil {
		return out, err
	}
	if a.ConnectionVersion != target.ConnectionVersion || a.ConnectionVersion != in.ConnectionVersion || a.CredentialGeneration != target.CredentialGeneration {
		return out, ErrVersionConflict
	}
	if verified.UUID != in.RepositoryID || verified.Workspace.UUID != a.WorkspaceID || verified.Private == nil || bitbucket.RepositoryURL(verified.FullName) == "" {
		return out, ErrInvalidRepository
	}
	for _, space := range in.SpaceIDs {
		if err := bitbucketSpacePermission(ctx, tx, org, actor, space, policy.ActionCreateObject); err != nil {
			return out, err
		}
	}
	var id, status string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT id::text,status,version FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND repository_id=$2::uuid FOR UPDATE`, a.ConnectionID, in.RepositoryID).Scan(&id, &status, &version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	exists := err == nil
	if exists && version != in.Version && !(status == "REMOVED" && in.Version == 0) || !exists && in.Version != 0 {
		return out, ErrVersionConflict
	}
	if !exists || status == "REMOVED" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND status<>'REMOVED'`, a.ConnectionID).Scan(&count); err != nil {
			return out, err
		}
		if count >= 100 {
			return out, ErrInvalidRequest
		}
	}
	if !exists {
		id, err = membership.NewID()
		if err != nil {
			return out, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_repositories(organization_id,id,connection_id,workspace_id,repository_id,full_name,is_private,pull_requests,commits,backlink_private,backlink_public,configured_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11::uuid)`, id, a.ConnectionID, a.WorkspaceID, in.RepositoryID, verified.FullName, *verified.Private, *in.PullRequests, *in.Commits, *in.BacklinkPrivate, *in.BacklinkPublic, actor.UserID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_repositories SET full_name=$2,is_private=$3,pull_requests=$4,commits=$5,backlink_private=$6,backlink_public=$7,status='ACTIVE',last_error='',version=version+1,hook_generation=CASE WHEN status='REMOVED' THEN 0 ELSE hook_generation END,configured_by=$8::uuid,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, verified.FullName, *verified.Private, *in.PullRequests, *in.Commits, *in.BacklinkPrivate, *in.BacklinkPublic, actor.UserID)
	}
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM openfox.integration_bitbucket_repository_spaces WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid`, id); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_repository_spaces(organization_id,repository_id,space_id) SELECT openfox_private.current_organization_id(),$1::uuid,unnest($2::uuid[])`, id, in.SpaceIDs); err != nil {
		return out, err
	}
	return loadBitbucketSelection(ctx, tx, id)
}

func (s Service) RemoveBitbucketRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, version uint64) (BitbucketRepositorySelection, error) {
	if !membership.ValidateUUID(id) || version == 0 {
		return BitbucketRepositorySelection{}, ErrInvalidRequest
	}
	if _, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, false, true); err != nil {
		return BitbucketRepositorySelection{}, err
	}
	var current uint64
	var status string
	if err := tx.QueryRow(ctx, `SELECT version,status FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND connection_id=$2::uuid FOR UPDATE`, id, connection).Scan(&current, &status); err != nil {
		return BitbucketRepositorySelection{}, bitbucketMissing(err)
	}
	if status == "REMOVED" {
		return loadBitbucketSelection(ctx, tx, id)
	}
	if current != version {
		return BitbucketRepositorySelection{}, ErrVersionConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_repositories SET status='REMOVED',version=version+1,hook_generation=0,last_error='repository_removed',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); err != nil {
		return BitbucketRepositorySelection{}, err
	}
	return loadBitbucketSelection(ctx, tx, id)
}
