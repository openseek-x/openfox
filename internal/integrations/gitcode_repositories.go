package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitcode"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type GitCodeRepository struct {
	ID                 string    `json:"id"`
	ConnectionID       string    `json:"connection_id"`
	SyncLinkID         string    `json:"sync_link_id"`
	RemoteID           string    `json:"remote_repository_id"`
	Path               string    `json:"path"`
	Visibility         string    `json:"visibility"`
	Active             bool      `json:"active"`
	HookGeneration     uint64    `json:"hook_generation"`
	HookID             string    `json:"hook_id"`
	HookState          string    `json:"hook_state"`
	ErrorCode          string    `json:"error_code"`
	IssueWriteStatus   string    `json:"issue_write_status"`
	CommentWriteStatus string    `json:"comment_write_status"`
	Version            uint64    `json:"version"`
	Watermark          time.Time `json:"watermark"`
	hookCallbackHash   []byte
}
type GitCodeWorkView struct {
	ID           string     `json:"id"`
	RepositoryID string     `json:"repository_id"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	RemoteID     string     `json:"remote_id"`
	ErrorCode    string     `json:"error_code"`
	NextAt       *time.Time `json:"next_attempt_at"`
}
type GitCodeConfiguration struct {
	Version             uint64              `json:"version"`
	Subject             string              `json:"subject"`
	Login               string              `json:"login"`
	OrganizationLogin   string              `json:"organization_login"`
	Repositories        []GitCodeRepository `json:"repositories"`
	Work                []GitCodeWorkView   `json:"work"`
	NextRepositoryAfter string              `json:"next_repository_after"`
	NextWorkBefore      string              `json:"next_work_before"`
	Links               []SyncLink          `json:"links"`
}

const gitcodeRepositoryColumns = `id::text,connection_id::text,sync_link_id::text,remote_repository_id,repository_path,visibility,active,hook_generation,hook_id,hook_state,last_error_code,issue_write_status,comment_write_status,version,watermark,hook_callback_hash`

func scanGitCodeRepository(row pgx.Row) (GitCodeRepository, error) {
	var r GitCodeRepository
	e := row.Scan(&r.ID, &r.ConnectionID, &r.SyncLinkID, &r.RemoteID, &r.Path, &r.Visibility, &r.Active, &r.HookGeneration, &r.HookID, &r.HookState, &r.ErrorCode, &r.IssueWriteStatus, &r.CommentWriteStatus, &r.Version, &r.Watermark, &r.hookCallbackHash)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return r, e
}
func loadGitCodeRepository(ctx context.Context, tx pgx.Tx, id string, lock bool) (GitCodeRepository, error) {
	if !membership.ValidateUUID(id) {
		return GitCodeRepository{}, ErrNotFound
	}
	q := `SELECT ` + gitcodeRepositoryColumns + ` FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`
	if lock {
		q += " FOR UPDATE"
	}
	return scanGitCodeRepository(tx.QueryRow(ctx, q, id))
}

type GitCodeConfigurationPage struct{ RepositoryAfter, WorkBefore string }
type gitcodeWorkCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func (s Service) GitCodeConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, page GitCodeConfigurationPage) (GitCodeConfiguration, error) {
	out := GitCodeConfiguration{Links: []SyncLink{}, Repositories: []GitCodeRepository{}, Work: []GitCodeWorkView{}}
	if page.RepositoryAfter != "" && !membership.ValidateUUID(page.RepositoryAfter) {
		return out, ErrInvalidRequest
	}
	var cursor gitcodeWorkCursor
	if page.WorkBefore != "" {
		raw, e := base64.RawURLEncoding.DecodeString(page.WorkBefore)
		if len(page.WorkBefore) > 256 || e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.At.IsZero() || !membership.ValidateUUID(cursor.ID) {
			return out, ErrInvalidRequest
		}
	}
	a, e := s.PrepareGitCode(ctx, tx, org, actor, id)
	if e != nil {
		return out, e
	}
	out.Version, out.Subject, out.Login, out.OrganizationLogin = a.Version, a.settings.Subject, a.settings.Login, a.settings.OrganizationLogin
	rows, e := tx.Query(ctx, `SELECT `+gitcodeRepositoryColumns+` FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT 51`, id, page.RepositoryAfter)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		r, e := scanGitCodeRepository(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Repositories = append(out.Repositories, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Repositories) > 50 {
		out.Repositories = out.Repositories[:50]
		out.NextRepositoryAfter = out.Repositories[49].ID
	}
	linkIDs := make([]string, 0, len(out.Repositories))
	for _, r := range out.Repositories {
		linkIDs = append(linkIDs, r.SyncLinkID)
	}
	rows, e = tx.Query(ctx, `SELECT `+syncLinkColumns+syncLinkFrom+` WHERE l.organization_id=openfox_private.current_organization_id() AND l.connection_id=$2::uuid AND l.id=ANY($3::uuid[]) AND `+collaboration.SpaceReadableSQL+` ORDER BY l.id`, strings.ToUpper(string(actor.Role)), id, linkIDs)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		link, e := scanSyncLink(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Links = append(out.Links, link)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT id::text,repository_id::text,kind,state,remote_id,last_error_code,next_attempt_at,created_at FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND ($2='' OR (created_at,id)<($3::timestamptz,NULLIF($2,'')::uuid)) ORDER BY created_at DESC,id DESC LIMIT 51`, id, cursor.ID, cursor.At)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	var last gitcodeWorkCursor
	for rows.Next() {
		var work GitCodeWorkView
		var at time.Time
		if e = rows.Scan(&work.ID, &work.RepositoryID, &work.Kind, &work.State, &work.RemoteID, &work.ErrorCode, &work.NextAt, &at); e != nil {
			return out, e
		}
		out.Work = append(out.Work, work)
		if len(out.Work) == 50 {
			last = gitcodeWorkCursor{At: at, ID: work.ID}
		}
	}
	if len(out.Work) > 50 {
		out.Work = out.Work[:50]
		out.NextWorkBefore = base64.RawURLEncoding.EncodeToString(mustJSON(last))
	}
	return out, rows.Err()
}

// Parent -> repository/link -> space/member locks. Re-check the same actor and
// policy in the save transaction, not just in the pre-I/O API authorization.
func gitcodeSpace(ctx context.Context, tx pgx.Tx, org, space string, actor collaboration.Actor) error {
	if !membership.ValidateUUID(space) {
		return ErrInvalidRequest
	}
	var id string
	e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL FOR SHARE`, space).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SELECT user_id FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, space, actor.UserID); e != nil {
		return e
	}
	access, e := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, org, space, actor.UserID)
	if e != nil {
		return e
	}
	if e = access.RequireWritable(); e != nil {
		return e
	}
	visibility := policy.VisibilityPrivate
	if access.Space.Visibility == "SHARED" {
		visibility = policy.VisibilityPublic
	}
	req := policy.Request{Object: &policy.Object{OrganizationID: org, SpaceID: space, ID: "gitcode-sync"}, Action: policy.ActionCreateObject, Actor: policy.Actor{OrganizationID: org, ActorID: actor.UserID, Role: actor.Role, Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: org, ID: space, Visibility: visibility}}
	if access.Membership != nil {
		state := policy.MembershipActive
		if access.Membership.Status != "ACTIVE" {
			state = policy.MembershipRevoked
		}
		var role policy.SpaceRole
		switch access.Membership.Role {
		case "OWNER", "EDITOR":
			role = policy.SpaceRoleAdministrator
		case "MEMBER":
			role = policy.SpaceRoleMember
		case "VIEWER":
			role = policy.SpaceRoleVisitor
		}
		req.SpaceMembership = &policy.SpaceMembership{OrganizationID: org, SpaceID: space, ActorID: actor.UserID, Role: role, Membership: state}
	}
	if !policy.Authorize(req).Allowed() {
		return ErrForbidden
	}
	return nil
}

type GitCodeCreateSyncInput struct {
	Version        uint64 `json:"version"`
	RepositoryID   string `json:"repository_id"`
	Repository     string `json:"repository"`
	SpaceID        string `json:"space_id"`
	Direction      string `json:"direction"`
	ImportExisting bool   `json:"import_existing"`
}

func (s Service) InsertGitCodeSync(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, a GitCodeAccess, in GitCodeCreateSyncInput, r gitcode.Repository) (SyncLink, error) {
	current, e := s.loadGitCode(ctx, tx, org, a.ID, true)
	if e != nil {
		return SyncLink{}, e
	}
	if current.Status != StatusActive || current.Version != a.Version || in.Version != a.Version {
		return SyncLink{}, ErrVersionConflict
	}
	if e = s.lockGitCodeManager(ctx, tx, org, actor); e != nil {
		return SyncLink{}, e
	}
	if e = gitcodeSpace(ctx, tx, org, in.SpaceID, actor); e != nil {
		return SyncLink{}, e
	}
	visibility, e := r.Visibility()
	if e != nil || strconv.FormatInt(r.ID, 10) != in.RepositoryID || strconv.FormatInt(r.Namespace.ID, 10) != a.ExternalID || r.FullName != in.Repository || (in.Direction != DirectionInbound && in.Direction != DirectionBidirectional) {
		return SyncLink{}, ErrInvalidRepository
	}
	u, e := url.Parse(s.PublicURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return SyncLink{}, ErrNotConfigured
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND active`, a.ID).Scan(&count); e != nil {
		return SyncLink{}, e
	}
	if count >= 100 {
		return SyncLink{}, ErrInvalidRequest
	}
	id, e := membership.NewID()
	if e != nil {
		return SyncLink{}, e
	}
	rid, e := membership.NewID()
	if e != nil {
		return SyncLink{}, e
	}
	backfill := map[string]any{"status": "none", "imported": 0, "error": ""}
	if in.ImportExisting {
		backfill["status"] = "running"
	}
	settings := mustJSON(map[string]any{"repository_url": r.URL, "repository_private": visibility == "private", "backfill": backfill})
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_sync_links (organization_id,id,connection_id,space_id,external_project_id,external_project_name,direction,status,status_reason,settings,created_by)
	 VALUES (openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,'PAUSED','setting_up_webhook',$7::jsonb,$8::uuid)`, id, a.ID, in.SpaceID, strings.ToLower(r.FullName), r.FullName, in.Direction, settings, actor.UserID)
	if e != nil {
		return SyncLink{}, mapSyncUnique(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_gitcode_repositories (organization_id,id,connection_id,sync_link_id,remote_repository_id,repository_path,visibility,hook_generation)
	 VALUES (openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7)`, rid, a.ID, id, in.RepositoryID, r.FullName, visibility, a.settings.HookGeneration)
	if isUniqueViolation(e) {
		return SyncLink{}, ErrSyncLinkExists
	}
	if e != nil {
		return SyncLink{}, e
	}
	if e = queueGitCodeWork(ctx, tx, a.ID, rid, "HOOK_ENSURE", "hook:"+strconv.FormatUint(a.settings.HookGeneration, 10), a.settings.HookGeneration, map[string]any{"import_existing": in.ImportExisting}); e != nil {
		return SyncLink{}, e
	}
	return loadSyncLink(ctx, tx, id)
}
func queueGitCodeWork(ctx context.Context, tx pgx.Tx, connection, repo, kind, key string, generation uint64, payload any) error {
	_, e := tx.Exec(ctx, `INSERT INTO openfox.integration_gitcode_work (organization_id,connection_id,repository_id,kind,object_key,generation,payload)
	 VALUES (openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6::jsonb) ON CONFLICT (organization_id,repository_id,kind,object_key) DO NOTHING`, connection, repo, kind, key, generation, mustJSON(payload))
	return e
}

func gitcodeSyncConnection(ctx context.Context, tx pgx.Tx, id string) (string, bool, error) {
	var c string
	e := tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid`, id).Scan(&c)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", false, nil
	}
	return c, e == nil, e
}
func (s Service) updateGitCodeSync(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id string, in UpdateSyncInput) (SyncLink, error) {
	a, e := s.loadGitCode(ctx, tx, org, connection, true)
	if e != nil {
		return SyncLink{}, e
	}
	if e = s.lockGitCodeManager(ctx, tx, org, actor); e != nil {
		return SyncLink{}, e
	}
	var active bool
	var state, reason string
	e = tx.QueryRow(ctx, `SELECT active,hook_state,last_error_code FROM openfox.integration_gitcode_repositories WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid FOR UPDATE`, id).Scan(&active, &state, &reason)
	if e != nil {
		return SyncLink{}, e
	}
	if !active || a.Status != StatusActive {
		return SyncLink{}, ErrNeedsReauth
	}
	if in.Status == "ACTIVE" || in.Direction == DirectionBidirectional {
		if reason == "repository_changed" {
			return SyncLink{}, ErrInvalidRepository
		}
		var space string
		if e = tx.QueryRow(ctx, `SELECT space_id::text FROM openfox.integration_sync_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&space); e != nil {
			return SyncLink{}, e
		}
		if e = gitcodeSpace(ctx, tx, org, space, actor); e != nil {
			return SyncLink{}, e
		}
	}
	if in.Status == "ACTIVE" && state != "ACTIVE" {
		return SyncLink{}, ErrInvalidRequest
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET direction=COALESCE(NULLIF($3,''),direction),status=COALESCE(NULLIF($4,''),status),status_reason=CASE WHEN $4='PAUSED' THEN 'paused_by_user' WHEN $4='ACTIVE' THEN '' ELSE status_reason END,version=version+1,updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND deleted_at IS NULL`, id, in.Version, in.Direction, in.Status)
	if e != nil {
		return SyncLink{}, mapSyncUnique(e)
	}
	if tag.RowsAffected() != 1 {
		return SyncLink{}, ErrVersionConflict
	}
	return loadSyncLink(ctx, tx, id)
}

// Explicit removal may request deletion of only the owned hook. Credentials
// must remain active until deletion completes; disconnect instead exposes IDs.
func (s Service) RemoveGitCodeRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, rid string, version uint64, deleteHook bool) error {
	a, e := s.loadGitCode(ctx, tx, org, connection, true)
	if e != nil {
		return e
	}
	if e = s.lockGitCodeManager(ctx, tx, org, actor); e != nil {
		return e
	}
	r, e := loadGitCodeRepository(ctx, tx, rid, true)
	if e != nil {
		return e
	}
	if r.ConnectionID != a.ID {
		return ErrNotFound
	}
	if r.Version != version {
		return ErrVersionConflict
	}
	if deleteHook {
		var sending bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gitcode_work WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND kind='HOOK_ENSURE' AND state='SENDING' AND lease_expires_at>clock_timestamp())`, rid).Scan(&sending); e != nil {
			return e
		}
		if sending {
			return ErrVersionConflict
		}
	}
	if deleteHook && a.Status != StatusActive {
		return ErrNeedsReauth
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_repositories SET active=false,hook_state=CASE WHEN $2 THEN 'DELETE_PENDING' ELSE hook_state END,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, rid, deleteHook)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason='sync_removed',deleted_at=COALESCE(deleted_at,clock_timestamp()),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.SyncLinkID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_gitcode_work SET state='CANCELLED',next_attempt_at=NULL,last_error_code='sync_removed',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND repository_id=$1::uuid AND state IN ('PENDING','SENDING','UNKNOWN')`, rid); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.integration_sync_outbox SET status='SKIPPED',finished_at=clock_timestamp(),lease_owner='',lease_expires_at=NULL,last_error='sync_removed',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid AND status IN ('PENDING','SENDING')`, r.SyncLinkID); e != nil {
		return e
	}
	if deleteHook {
		return queueGitCodeWork(ctx, tx, a.ID, rid, "HOOK_DELETE", "delete:"+strconv.FormatUint(r.HookGeneration, 10), r.HookGeneration, map[string]any{})
	}
	return nil
}
