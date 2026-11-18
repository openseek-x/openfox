package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
)

var errGongfengLeaseLost = errors.New("gongfeng: lease lost")
var ErrGongfengQueueFull = errors.New("gongfeng: pending work limit reached")

type GongfengRepository struct {
	ID                       string           `json:"id"`
	ConnectionID             string           `json:"connection_id"`
	SyncLinkID               string           `json:"sync_link_id"`
	ProjectID                int64            `json:"project_id"`
	Path                     string           `json:"path"`
	Private                  *bool            `json:"private"`
	Generation               int64            `json:"generation"`
	Status                   string           `json:"status"`
	StatusReason             string           `json:"status_reason"`
	HookID                   *int64           `json:"hook_id"`
	HookState                string           `json:"hook_state"`
	HookURLHash              []byte           `json:"-"`
	HookTargetHash           []byte           `json:"-"`
	RepositoryURL            string           `json:"repository_url"`
	HookGeneration           int64            `json:"hook_generation"`
	HookCredentialGeneration int64            `json:"hook_credential_generation"`
	MREnabled                bool             `json:"mr_enabled"`
	CommitEnabled            bool             `json:"commit_enabled"`
	BacklinkEnabled          bool             `json:"backlink_enabled"`
	HistoryRequested         bool             `json:"history_requested"`
	ExcludedConfidential     int64            `json:"excluded_confidential"`
	Progress                 GongfengProgress `json:"progress"`
}

type GongfengProgress struct {
	Issues        *GongfengScanProgress `json:"issues,omitempty"`
	MergeRequests *GongfengScanProgress `json:"merge_requests,omitempty"`
}
type GongfengScanProgress struct {
	Status     string     `json:"status"`
	Pass       int        `json:"pass"`
	Page       int        `json:"page"`
	Count      int        `json:"count"`
	FirstCount int        `json:"first_count"`
	From       *time.Time `json:"from,omitempty"`
	To         *time.Time `json:"to,omitempty"`
	History    bool       `json:"history"`
	ErrorCode  string     `json:"error_code"`
}

const gongfengRepoColumns = `id::text,connection_id::text,sync_link_id::text,remote_project_id,current_path,private,generation,status,status_reason,
 hook_id,hook_state,hook_url_hash,hook_target_hash,hook_generation,hook_credential_generation,mr_enabled,commit_enabled,backlink_enabled,history_requested,excluded_confidential,history_cursor`

func scanGongfengRepository(row pgx.Row) (GongfengRepository, error) {
	var r GongfengRepository
	var progress []byte
	err := row.Scan(&r.ID, &r.ConnectionID, &r.SyncLinkID, &r.ProjectID, &r.Path, &r.Private, &r.Generation, &r.Status, &r.StatusReason,
		&r.HookID, &r.HookState, &r.HookURLHash, &r.HookTargetHash, &r.HookGeneration, &r.HookCredentialGeneration, &r.MREnabled, &r.CommitEnabled, &r.BacklinkEnabled, &r.HistoryRequested, &r.ExcludedConfidential, &progress)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	r.RepositoryURL = gongfeng.Origin + "/" + r.Path
	var cursors map[string]gongfengScan
	if err == nil && json.Unmarshal(progress, &cursors) != nil {
		return r, ErrInvalidRequest
	}
	for mode, c := range cursors {
		p := &GongfengScanProgress{Status: c.Status, Pass: c.Pass, Page: c.Page, Count: c.Count, FirstCount: c.FirstCount, History: c.History, ErrorCode: c.Error}
		if !c.From.IsZero() {
			at := c.From
			p.From = &at
		}
		if !c.To.IsZero() {
			at := c.To
			p.To = &at
		}
		switch mode {
		case "issues":
			r.Progress.Issues = p
		case "merge_requests":
			r.Progress.MergeRequests = p
		}
	}
	return r, err
}
func loadGongfengRepository(ctx context.Context, tx pgx.Tx, connection, id string, lock bool) (GongfengRepository, error) {
	if !membership.ValidateUUID(id) {
		return GongfengRepository{}, ErrNotFound
	}
	q := `SELECT ` + gongfengRepoColumns + ` FROM openfox.integration_gongfeng_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid`
	if lock {
		q += " FOR UPDATE"
	}
	return scanGongfengRepository(tx.QueryRow(ctx, q, connection, id))
}

type GongfengRepositoryInput struct {
	ConnectionID      string `json:"connection_id"`
	ConnectionVersion uint64 `json:"connection_version"`
	ProjectID         int64  `json:"project_id"`
	SpaceID           string `json:"space_id"`
	Direction         string `json:"direction"`
	ImportExisting    bool   `json:"import_existing"`
	MREnabled         *bool  `json:"mr_enabled,omitempty"`
	CommitEnabled     *bool  `json:"commit_enabled,omitempty"`
	BacklinkEnabled   *bool  `json:"backlink_enabled,omitempty"`
}
type GongfengRepositoryPlan struct {
	input    GongfengRepositoryInput
	access   GongfengAccess
	linkID   string
	project  gongfeng.Project
	verified bool
}

func (s Service) PrepareGongfengRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, linkID string, in GongfengRepositoryInput) (GongfengRepositoryPlan, error) {
	out := GongfengRepositoryPlan{input: in, linkID: linkID}
	if in.Direction == "" {
		in.Direction = DirectionInbound
		out.input = in
	}
	if in.ProjectID < 1 || !membership.ValidateUUID(linkID) || !membership.ValidateUUID(in.SpaceID) || in.ConnectionVersion < 1 || in.Direction != DirectionInbound && in.Direction != DirectionBidirectional {
		return out, ErrInvalidRequest
	}
	a, err := s.PrepareGongfeng(ctx, tx, org, actor, in.ConnectionID)
	if err != nil {
		return out, err
	}
	if a.Version != in.ConnectionVersion {
		return out, ErrVersionConflict
	}
	if a.Status != StatusActive {
		return out, ErrNeedsReauth
	}
	out.access = a
	return out, nil
}

func (s Service) ValidateGongfengRepository(ctx context.Context, plan GongfengRepositoryPlan) (GongfengRepositoryPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a := plan.access
	user, err := a.Client.Identity(ctx, a.credentials.AccessToken)
	if err != nil {
		return plan, err
	}
	if user.ID != a.IdentityID {
		return plan, ErrNeedsReauth
	}
	p, err := a.Client.Project(ctx, a.credentials.AccessToken, plan.input.ProjectID)
	if err != nil {
		return plan, err
	}
	// The official detail example omits type. Confirm the stable ID in the
	// documented GIT-only list instead of inventing a detail-field default.
	if p.Type == "" {
		name := p.Path[strings.LastIndex(p.Path, "/")+1:]
		found := false
		for page := 1; page != 0; {
			rows, e := a.Client.Projects(ctx, a.credentials.AccessToken, name, page)
			if e != nil {
				return plan, e
			}
			for _, candidate := range rows.Items {
				if candidate.ID == p.ID {
					if candidate.Type != "GIT" {
						return plan, ErrInvalidRepository
					}
					found = true
				}
			}
			if found {
				break
			}
			page = rows.Next
		}
		if !found {
			return plan, ErrInvalidRepository
		}
		p.Type = "GIT"
	}
	if !p.Selectable() || len(p.Path) > 255 {
		return plan, ErrInvalidRepository
	}
	allowed, err := a.Client.CanManageHooks(ctx, a.credentials.AccessToken, p.ID, user, time.Now())
	if err != nil {
		return plan, err
	}
	if !allowed {
		return plan, ErrForbidden
	}
	plan.project, plan.verified = p, true
	return plan, nil
}

func (s Service) gongfengCallbackURL(token string) (string, error) {
	u, err := url.Parse(strings.TrimRight(s.PublicURL, "/"))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrNotConfigured
	}
	return strings.TrimRight(u.String(), "/") + "/api/v1/hooks/git/gongfeng/" + token, nil
}

// The API authorizes the chosen space both before upstream validation and
// inside this saving transaction, using the same link ID as the created object.
func (s Service) SaveGongfengRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, plan GongfengRepositoryPlan) (GongfengRepository, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return GongfengRepository{}, err
	}
	if !plan.verified || plan.project.ID != plan.input.ProjectID {
		return GongfengRepository{}, ErrInvalidRequest
	}
	a, err := s.loadGongfeng(ctx, tx, org, plan.access.ID, true)
	if err != nil {
		return GongfengRepository{}, err
	}
	if a.Version != plan.access.Version || a.Status != StatusActive {
		return GongfengRepository{}, ErrVersionConflict
	}
	callback, err := s.gongfengCallbackURL(a.credentials.WebhookToken)
	if err != nil {
		return GongfengRepository{}, err
	}
	// Removed bindings with unresolved side effects must first be reconciled;
	// disconnecting or deleting a row is not an escape from UNKNOWN.
	var unresolved bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gongfeng_repositories r
 JOIN openfox.integration_gongfeng_work w ON w.organization_id=r.organization_id AND w.repository_id=r.id
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.remote_project_id=$1
 AND w.backlink_state IN ('SENDING','UNKNOWN'))`, plan.project.ID).Scan(&unresolved)
	if err != nil {
		return GongfengRepository{}, err
	}
	if unresolved {
		return GongfengRepository{}, ErrVersionConflict
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_gongfeng_repositories WHERE organization_id=openfox_private.current_organization_id() AND remote_project_id=$1 AND removed_at IS NOT NULL AND hook_state<>'DELETED')`, plan.project.ID).Scan(&unresolved)
	if err != nil {
		return GongfengRepository{}, err
	}
	if unresolved {
		return GongfengRepository{}, ErrVersionConflict
	}
	p, in := plan.project, plan.input
	private, known := p.Privacy()
	var privacy *bool
	if known {
		privacy = &private
	}
	mr, commit, backlink := true, false, known && private
	if in.MREnabled != nil {
		mr = *in.MREnabled
	}
	if in.CommitEnabled != nil {
		commit = *in.CommitEnabled
	}
	if in.BacklinkEnabled != nil {
		backlink = *in.BacklinkEnabled
	}
	if backlink && !known {
		return GongfengRepository{}, ErrInvalidRepository
	}
	settings, _ := json.Marshal(map[string]any{"repository_url": gongfeng.Origin + "/" + p.Path, "repository_private": private, "gongfeng_project_id": p.ID})
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_sync_links(organization_id,id,connection_id,space_id,external_project_id,external_project_name,direction,status,status_reason,settings,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,'PAUSED','hook_pending',$7::jsonb,$8::uuid)`, plan.linkID, a.ID, in.SpaceID, gongfengRepositoryKey(p.ID), p.Path, in.Direction, settings, actor.UserID)
	if err != nil {
		return GongfengRepository{}, mapSyncUnique(err)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO openfox.integration_gongfeng_repositories(organization_id,connection_id,sync_link_id,remote_project_id,current_path,private,hook_url_hash,mr_enabled,commit_enabled,backlink_enabled,history_requested)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`, a.ID, plan.linkID, p.ID, p.Path, privacy, TokenHash(callback), mr, commit, backlink, in.ImportExisting).Scan(&id)
	if isUniqueViolation(err) {
		return GongfengRepository{}, ErrSyncLinkExists
	}
	if err != nil {
		return GongfengRepository{}, err
	}
	return loadGongfengRepository(ctx, tx, a.ID, id, false)
}

type GongfengWork struct {
	Org                                                     string
	ID, ConnectionID, RepositoryID, Kind, ObjectKey, Status string
	ParentID                                                *int64
	Payload                                                 json.RawMessage
	RequestedGeneration                                     int64
	LeaseToken                                              string
	Access                                                  GongfengAccess
	Repository                                              GongfengRepository
	Link                                                    syncLinkRow
	Actor                                                   collaboration.Actor
	Failures                                                int
}

// Caller holds the parent connection then repository lock. One shared parent
// lock serializes the pending cap with coalescing and concurrent receivers.
func enqueueGongfengWork(ctx context.Context, tx pgx.Tx, repo GongfengRepository, kind, key string, parent *int64, payload any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	if kind != "ISSUE_REFRESH" && kind != "NOTE_REFRESH" && kind != "NOTES_SCAN" && kind != "MR_REFRESH" && kind != "PUSH" && kind != "BACKLINK" && kind != "ISSUE_CREATE" && kind != "ISSUE_UPDATE" && kind != "ISSUE_COMMENT" {
		return ErrInvalidRequest
	}
	data, err := json.Marshal(payload)
	if err != nil || len(data) > 16384 || len(key) < 1 || len(key) > 180 {
		return ErrInvalidRequest
	}
	var existingStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id()
 AND repository_id=$1::uuid AND kind=$2 AND object_key=$3 FOR UPDATE`, repo.ID, kind, key).Scan(&existingStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if existingStatus != "PENDING" && existingStatus != "RUNNING" {
		var count int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id()
   AND connection_id=$1::uuid AND status IN ('PENDING','RUNNING') LIMIT 10000) q`, repo.ConnectionID).Scan(&count)
		if err != nil {
			return err
		}
		if count >= 10000 {
			return ErrGongfengQueueFull
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_gongfeng_work(organization_id,connection_id,repository_id,kind,object_key,parent_object_id,payload)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6::jsonb)
 ON CONFLICT(organization_id,repository_id,kind,object_key) DO UPDATE SET
 requested_generation=openfox.integration_gongfeng_work.requested_generation+1,payload=openfox.integration_gongfeng_work.payload||EXCLUDED.payload,parent_object_id=EXCLUDED.parent_object_id,
 status=CASE WHEN openfox.integration_gongfeng_work.status='RUNNING' THEN 'RUNNING' ELSE 'PENDING' END,
 next_attempt_at=clock_timestamp(),updated_at=clock_timestamp()`, repo.ConnectionID, repo.ID, kind, key, parent, data)
	return err
}

// Claim takes locks in the same connection -> repository -> work order used
// by updates, disconnect and completion. Claim expiry never grants a late
// response permission to write under a replacement lease.
func (s Service) ClaimGongfengWork(ctx context.Context, tx pgx.Tx, org, connection, repository, id string) (GongfengWork, error) {
	job := GongfengWork{Org: org}
	a, err := s.loadGongfeng(ctx, tx, org, connection, true)
	if err != nil {
		return job, err
	}
	if a.Status != StatusActive {
		return job, ErrNeedsReauth
	}
	repo, err := loadGongfengRepository(ctx, tx, connection, repository, true)
	if err != nil {
		return job, err
	}
	if repo.Status != StatusActive || repo.HookGeneration != repo.Generation || repo.HookCredentialGeneration != a.CredentialGeneration {
		return job, ErrVersionConflict
	}
	job.Link, job.Actor, err = gongfengLinkAuthority(ctx, tx, org, a, repo)
	if err != nil {
		return job, err
	}
	if job.Link.Status != StatusActive {
		return job, ErrVersionConflict
	}
	err = tx.QueryRow(ctx, `SELECT id::text,connection_id::text,repository_id::text,kind,object_key,status,parent_object_id,payload,requested_generation,consecutive_failures
 FROM openfox.integration_gongfeng_work WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND repository_id=$2::uuid
 AND status IN ('PENDING','RUNNING') AND next_attempt_at<=clock_timestamp() AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
 FOR UPDATE SKIP LOCKED`, id, repository).Scan(&job.ID, &job.ConnectionID, &job.RepositoryID, &job.Kind, &job.ObjectKey, &job.Status, &job.ParentID, &job.Payload, &job.RequestedGeneration, &job.Failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, ErrNotFound
	}
	if err != nil {
		return job, err
	}
	job.LeaseToken, err = membership.NewID()
	if err != nil {
		return job, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status='RUNNING',lease_token=$2::uuid,lease_expires_at=clock_timestamp()+interval '90 seconds',
 backlink_state=CASE WHEN backlink_state='SENDING' THEN 'UNKNOWN' ELSE backlink_state END,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, job.LeaseToken)
	if err != nil {
		return job, err
	}
	if tag.RowsAffected() != 1 {
		return job, errGongfengLeaseLost
	}
	job.Access, job.Repository = a, repo
	return job, nil
}

func (s Service) checkGongfengWork(ctx context.Context, tx pgx.Tx, org string, job GongfengWork) error {
	a, err := s.loadGongfeng(ctx, tx, org, job.ConnectionID, true)
	if err != nil {
		return err
	}
	if a.Status != StatusActive || a.Version != job.Access.Version || a.CredentialGeneration != job.Access.CredentialGeneration {
		return errGongfengLeaseLost
	}
	r, err := loadGongfengRepository(ctx, tx, job.ConnectionID, job.RepositoryID, true)
	if err != nil {
		return err
	}
	if r.Status != StatusActive || r.Generation != job.Repository.Generation {
		return errGongfengLeaseLost
	}
	link, _, err := gongfengLinkAuthority(ctx, tx, org, a, r)
	if err != nil {
		return err
	}
	if link.Status != StatusActive {
		return errGongfengLeaseLost
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT requested_generation=$3 FROM openfox.integration_gongfeng_work
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='RUNNING' AND lease_token=$2::uuid
 AND lease_expires_at>clock_timestamp() FOR UPDATE`, job.ID, job.LeaseToken, job.RequestedGeneration).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return errGongfengLeaseLost
	}
	if err != nil {
		return err
	}
	if !current {
		return errGongfengLeaseLost
	}
	return nil
}

func (s Service) CompleteGongfengWork(ctx context.Context, tx pgx.Tx, org string, job GongfengWork) error {
	if err := s.checkGongfengWork(ctx, tx, org, job); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_work SET status='DONE',completed_generation=requested_generation,next_attempt_at=NULL,
 lease_token=NULL,lease_expires_at=NULL,consecutive_failures=0,last_error_code='',updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_token=$2::uuid AND requested_generation=$3`, job.ID, job.LeaseToken, job.RequestedGeneration)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errGongfengLeaseLost
	}
	return nil
}

func gongfengRepositoryKey(projectID int64) string {
	return "git.tencent.com/" + strconv.FormatInt(projectID, 10)
}
