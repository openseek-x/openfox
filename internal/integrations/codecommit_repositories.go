package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/membership"
)

type CodeCommitRepository struct {
	ID            string     `json:"id"`
	RepositoryID  string     `json:"repository_id"`
	Name          string     `json:"name"`
	ARN           string     `json:"arn"`
	Enabled       bool       `json:"enabled"`
	Generation    uint64     `json:"generation"`
	Version       uint64     `json:"version"`
	ConfiguredBy  string     `json:"configured_by"`
	BridgeID      string     `json:"bridge_id"`
	BridgeReady   bool       `json:"bridge_ready"`
	LastEventAt   *time.Time `json:"last_event_at"`
	ActivatedAt   time.Time  `json:"activated_at"`
	InitializedAt *time.Time `json:"initialized_at"`
	ScanKind      string     `json:"scan_kind"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	LastError     string     `json:"last_error"`
}
type CodeCommitRepositoryInput struct {
	ConnectionVersion     uint64 `json:"connection_version"`
	Name                  string `json:"name"`
	Version               uint64 `json:"version"`
	ConfirmInitialization bool   `json:"confirm_initialization"`
}
type CodeCommitVersionInput struct {
	ConnectionVersion uint64 `json:"connection_version"`
	Version           uint64 `json:"version"`
}
type CodeCommitBridgeResult struct {
	Repository CodeCommitRepository `json:"repository"`
	URL        string               `json:"url"`
	Token      string               `json:"token"`
}
type CodeCommitConfiguration struct {
	Connection     Connection             `json:"connection"`
	Account        string                 `json:"account"`
	Region         string                 `json:"region"`
	PrincipalARN   string                 `json:"principal_arn"`
	Repositories   []CodeCommitRepository `json:"repositories"`
	RepositoryNext string                 `json:"repository_next"`
}

const codeCommitRepoColumns = `id::text,repository_id,name,arn,enabled,generation,version,configured_by::text,bridge_id::text,bridge_token_hash IS NOT NULL,last_event_at,activated_at,initialized_at,scan_kind,next_attempt_at,last_error`

func scanCodeCommitRepository(row interface{ Scan(...any) error }) (CodeCommitRepository, error) {
	var r CodeCommitRepository
	e := row.Scan(&r.ID, &r.RepositoryID, &r.Name, &r.ARN, &r.Enabled, &r.Generation, &r.Version, &r.ConfiguredBy, &r.BridgeID, &r.BridgeReady, &r.LastEventAt, &r.ActivatedAt, &r.InitializedAt, &r.ScanKind, &r.NextAttemptAt, &r.LastError)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return r, e
}
func loadCodeCommitRepository(ctx context.Context, tx pgx.Tx, connection, id string, lock bool) (CodeCommitRepository, error) {
	if !membership.ValidateUUID(id) {
		return CodeCommitRepository{}, ErrNotFound
	}
	q := `SELECT ` + codeCommitRepoColumns + ` FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid`
	if lock {
		q += " FOR UPDATE"
	}
	return scanCodeCommitRepository(tx.QueryRow(ctx, q, connection, id))
}
func (s Service) CodeCommitConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, after string) (CodeCommitConfiguration, error) {
	out := CodeCommitConfiguration{Repositories: []CodeCommitRepository{}}
	if after != "" && !membership.ValidateUUID(after) {
		return out, ErrInvalidRequest
	}
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return out, e
	}
	a, e := s.readCodeCommit(ctx, tx, org, id, false, false)
	if e != nil {
		return out, e
	}
	out.Account, out.Region, out.PrincipalARN = a.settings.Account, a.settings.Region, a.settings.PrincipalARN
	out.Connection, e = loadConnection(ctx, tx, id)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT `+codeCommitRepoColumns+` FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT 51`, id, after)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanCodeCommitRepository(rows)
		if e != nil {
			return out, e
		}
		if len(out.Repositories) == 50 {
			out.RepositoryNext = out.Repositories[49].ID
			break
		}
		out.Repositories = append(out.Repositories, r)
	}
	return out, rows.Err()
}
func (a CodeCommitAccess) VerifyRepository(ctx context.Context, name string) (codecommit.Repository, error) {
	if a.Status != StatusActive {
		return codecommit.Repository{}, ErrNeedsReauth
	}
	return a.Client.GetRepository(ctx, a.credentials, a.settings.Account, name)
}
func (s Service) SaveCodeCommitRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, a CodeCommitAccess, in CodeCommitRepositoryInput, verified codecommit.Repository) (CodeCommitBridgeResult, error) {
	var out CodeCommitBridgeResult
	if !in.ConfirmInitialization || in.ConnectionVersion == 0 || in.ConnectionVersion != a.Version || !codecommit.ValidRepositoryName(in.Name) || verified.Name != in.Name || !codecommit.ValidRepositoryID(verified.ID) || verified.AccountID != a.settings.Account || verified.ARN != "arn:aws:codecommit:"+a.settings.Region+":"+a.settings.Account+":"+in.Name {
		return out, ErrInvalidRequest
	}
	base, e := s.codeCommitPublicURL()
	if e != nil {
		return out, e
	}
	if e = s.CheckCodeCommitVersion(ctx, tx, org, actor, a); e != nil {
		return out, e
	}
	var r CodeCommitRepository
	r, e = scanCodeCommitRepository(tx.QueryRow(ctx, `SELECT `+codeCommitRepoColumns+` FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND account_id=$1 AND region=$2 AND repository_id=$3 FOR UPDATE`, a.settings.Account, a.settings.Region, verified.ID))
	existing := e == nil
	if e != nil && !errors.Is(e, ErrNotFound) {
		return out, e
	}
	if existing {
		// A retained row is re-enabled by its own connection; reconnect via credentials CAS.
		var connection string
		if e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.ID).Scan(&connection); e != nil {
			return out, e
		}
		if connection != a.ID || r.Enabled || r.Version != in.Version {
			return out, ErrVersionConflict
		}
	} else if in.Version != 0 {
		return out, ErrVersionConflict
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND enabled`, a.ID).Scan(&count); e != nil {
		return out, e
	}
	if count >= 20 {
		return out, ErrInvalidRequest
	}
	token, e := randomBase64URL(32)
	if e != nil {
		return out, e
	}
	if existing {
		r, e = scanCodeCommitRepository(tx.QueryRow(ctx, `UPDATE openfox.integration_codecommit_repositories SET enabled=true,generation=generation+1,version=version+1,name=$3,arn=$4,configured_by=$5::uuid,bridge_token_hash=$6,last_event_at=NULL,activated_at=clock_timestamp(),initialized_at=NULL,scan_kind='PR_OPEN',scan_generation=scan_generation+1,next_token='',scan_pending='[]',scan_seen_tokens='[]',next_attempt_at=clock_timestamp(),lease_token=NULL,lease_until=NULL,failure_count=0,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid RETURNING `+codeCommitRepoColumns, a.ID, r.ID, verified.Name, verified.ARN, actor.UserID, TokenHash(token)))
	} else {
		r, e = scanCodeCommitRepository(tx.QueryRow(ctx, `INSERT INTO openfox.integration_codecommit_repositories(organization_id,connection_id,account_id,region,repository_id,name,arn,configured_by,bridge_token_hash) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5,$6,$7::uuid,$8) RETURNING `+codeCommitRepoColumns, a.ID, a.settings.Account, a.settings.Region, verified.ID, verified.Name, verified.ARN, actor.UserID, TokenHash(token)))
	}
	if isUniqueViolation(e) {
		e = ErrVersionConflict
	}
	if e != nil {
		return out, e
	}
	return CodeCommitBridgeResult{Repository: r, URL: base + "/api/v1/hooks/git/codecommit/" + r.BridgeID, Token: token}, nil
}

// Stop pending effects without erasing a possible remote write or its intent.
func stopCodeCommitRepository(ctx context.Context, tx pgx.Tx, id string) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_codecommit_work SET state='IGNORED',next_attempt_at=NULL,lease_token=NULL,lease_until=NULL,last_error='repository_generation_changed',version=version+1,backlink_state=CASE WHEN backlink_state='SENDING' THEN 'UNKNOWN' WHEN backlink_state='PENDING' THEN 'SKIPPED' ELSE backlink_state END,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND repository_row_id=$1::uuid`, id)
	return e
}
func (s Service) ManageCodeCommitRepository(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id, action string, in CodeCommitVersionInput) (CodeCommitBridgeResult, error) {
	var out CodeCommitBridgeResult
	a, e := s.readCodeCommit(ctx, tx, org, connection, true, false)
	if e != nil {
		return out, e
	}
	if in.ConnectionVersion == 0 || in.ConnectionVersion != a.Version {
		return out, ErrVersionConflict
	}
	if e = s.lockCodeCommitManager(ctx, tx, org, actor); e != nil {
		return out, e
	}
	r, e := loadCodeCommitRepository(ctx, tx, connection, id, true)
	if e != nil {
		return out, e
	}
	if r.Version != in.Version || in.Version == 0 {
		return out, ErrVersionConflict
	}
	if action != "disable" && action != "rotate" && action != "reconcile" {
		return out, ErrInvalidRequest
	}
	if action != "disable" && (!r.Enabled || a.Status != StatusActive) {
		return out, ErrNeedsReauth
	}
	if action == "reconcile" {
		var busy bool
		if e = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false) FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&busy); e != nil {
			return out, e
		}
		if busy {
			return out, ErrVersionConflict
		}
		if r.LastError == "" {
			return out, ErrInvalidRequest
		}
		out.Repository, e = scanCodeCommitRepository(tx.QueryRow(ctx, `UPDATE openfox.integration_codecommit_repositories SET failure_count=0,last_error='',next_attempt_at=clock_timestamp(),lease_token=NULL,lease_until=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid RETURNING `+codeCommitRepoColumns, id))
		return out, e
	}
	var hash []byte
	token, base := "", ""
	if action == "rotate" {
		base, e = s.codeCommitPublicURL()
		if e != nil {
			return out, e
		}
		token, e = randomBase64URL(32)
		if e != nil {
			return out, e
		}
		hash = TokenHash(token)
	}
	if e = stopCodeCommitRepository(ctx, tx, id); e != nil {
		return out, e
	}
	out.Repository, e = scanCodeCommitRepository(tx.QueryRow(ctx, `UPDATE openfox.integration_codecommit_repositories SET enabled=$3,generation=generation+1,version=version+1,bridge_token_hash=$4,last_event_at=NULL,scan_generation=scan_generation+1,scan_kind='PR_OPEN',next_token='',scan_pending='[]',scan_seen_tokens='[]',next_attempt_at=CASE WHEN $3 THEN clock_timestamp() ELSE NULL END,lease_token=NULL,lease_until=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid RETURNING `+codeCommitRepoColumns, connection, id, action == "rotate", hash))
	if e != nil {
		return out, e
	}
	if action == "rotate" {
		out.URL = base + "/api/v1/hooks/git/codecommit/" + r.BridgeID
		out.Token = token
	}
	return out, nil
}
func (s Service) DisconnectCodeCommit(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64, confirm string) (DisconnectResult, error) {
	a, e := s.readCodeCommit(ctx, tx, org, id, true, false)
	if e != nil {
		return DisconnectResult{}, e
	}
	if e = s.lockCodeCommitManager(ctx, tx, org, actor); e != nil {
		return DisconnectResult{}, e
	}
	if a.Status == StatusDisconnected {
		c, e := loadConnection(ctx, tx, id)
		return DisconnectResult{Connection: c}, e
	}
	if a.Version != version || version == 0 {
		return DisconnectResult{}, ErrVersionConflict
	}
	if confirm != ConfirmPhrase("AWS CodeCommit") {
		return DisconnectResult{}, ErrConfirmationMismatch
	}
	rows, e := tx.Query(ctx, `SELECT id::text FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid ORDER BY id FOR UPDATE`, id)
	if e != nil {
		return DisconnectResult{}, e
	}
	ids := []string{}
	for rows.Next() {
		var x string
		if e = rows.Scan(&x); e != nil {
			rows.Close()
			return DisconnectResult{}, e
		}
		ids = append(ids, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return DisconnectResult{}, e
	}
	for _, x := range ids {
		if e = stopCodeCommitRepository(ctx, tx, x); e != nil {
			return DisconnectResult{}, e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET enabled=false,generation=generation+1,version=version+1,bridge_token_hash=NULL,next_attempt_at=NULL,lease_token=NULL,lease_until=NULL,last_error='connection_disconnected',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, id)
	if e != nil {
		return DisconnectResult{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='disconnected by user',credentials_sealed=NULL,credentials_key_id='',credentials_hint='',disconnected_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if e != nil {
		return DisconnectResult{}, e
	}
	c, e := loadConnection(ctx, tx, id)
	return DisconnectResult{Connection: c, Changed: true}, e
}
