package integrations

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const teambitionPersonalMethod = "teambition_identity"

type TeambitionStartInput struct {
	ConnectionID string `json:"connection_id"`
	Version      uint64 `json:"version"`
}

type teambitionProof struct {
	ParentID       string              `json:"parent_id"`
	ParentVersion  uint64              `json:"parent_version"`
	BindingVersion uint64              `json:"binding_version"`
	CompanyID      string              `json:"company_id"`
	Email          string              `json:"email"`
	Identity       *TeambitionIdentity `json:"identity,omitempty"`
	BindingID      string              `json:"binding_id,omitempty"`
}

type TeambitionSession struct {
	ID        string              `json:"id"`
	Status    string              `json:"status"`
	ErrorCode string              `json:"error_code"`
	ExpiresAt time.Time           `json:"expires_at"`
	Identity  *TeambitionIdentity `json:"identity,omitempty"`
	BindingID string              `json:"binding_id,omitempty"`
}

type TeambitionCallbackTarget struct {
	app   ProviderTokenInput
	proof teambitionProof
}

func checkPersonalActor(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) error {
	// Keep the validated membership stable until this transaction commits.
	var id string
	if err := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	return nil
}

func personalEmail(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) (string, error) {
	var email string
	err := tx.QueryRow(ctx, `SELECT normalized_email FROM openfox.users WHERE id=$1::uuid FOR SHARE`, actor.UserID).Scan(&email)
	if err == nil && strings.TrimSpace(email) == "" {
		err = ErrPersonalIdentity
	}
	return strings.ToLower(strings.TrimSpace(email)), err
}

func (s Service) teambitionParent(ctx context.Context, tx pgx.Tx, org, id string) (ProviderTokenInput, uint64, error) {
	var app ProviderTokenInput
	if !membership.ValidateUUID(id) {
		return app, 0, ErrInvalidRequest
	}
	var sealed []byte
	var company string
	var version uint64
	err := tx.QueryRow(ctx, `SELECT external_id,credentials_sealed,version FROM openfox.integration_connections
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='teambition' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, id).Scan(&company, &sealed, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return app, 0, ErrNotFound
	}
	if err != nil {
		return app, 0, err
	}
	raw, err := s.open(sealed, connectionAAD(org, id))
	if err != nil {
		return app, 0, err
	}
	if json.Unmarshal(raw, &app) != nil || !app.validate("teambition") || app.CompanyID != company {
		return app, 0, ErrNeedsReauth
	}
	return app, version, nil
}

func (s Service) StartTeambitionPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in TeambitionStartInput) (StartResult, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return StartResult{}, err
	}
	app, version, err := s.teambitionParent(ctx, tx, org, in.ConnectionID)
	if err != nil {
		return StartResult{}, err
	}
	email, err := personalEmail(ctx, tx, actor)
	if err != nil {
		return StartResult{}, err
	}
	var existing uint64
	err = tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='teambition' AND scope='PERSONAL' AND owner_user_id=$1::uuid AND parent_connection_id=$2::uuid AND status<>'DISCONNECTED'`, actor.UserID, in.ConnectionID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return StartResult{}, err
	}
	if existing != in.Version {
		return StartResult{}, ErrVersionConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND provider='teambition' AND user_id=$1::uuid AND connection_id=$2::uuid AND status IN ('PENDING','SELECTING')`, actor.UserID, in.ConnectionID); err != nil {
		return StartResult{}, err
	}
	id, err := membership.NewID()
	if err != nil {
		return StartResult{}, err
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return StartResult{}, err
	}
	state := base64.RawURLEncoding.EncodeToString(random)
	proof := teambitionProof{ParentID: in.ConnectionID, ParentVersion: version, BindingVersion: in.Version, CompanyID: app.CompanyID, Email: email}
	raw, _ := json.Marshal([]teambitionProof{proof})
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO openfox.integration_oauth_states(organization_id,id,provider,purpose,user_id,state_hash,connection_id,pending_accounts) VALUES(openfox_private.current_organization_id(),$1::uuid,'teambition','PERSONAL',$2::uuid,$3,$4::uuid,$5::jsonb) RETURNING expires_at`, id, actor.UserID, TokenHash(state), in.ConnectionID, raw).Scan(&expiresAt)
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{SessionID: id, ExpiresAt: expiresAt, AuthorizeURL: teambitionAuthorizeURL(app.Account, strings.TrimRight(s.PublicURL, "/")+"/api/v1/integrations/teambition/callback", state)}, nil
}

func lockTeambitionSession(ctx context.Context, tx pgx.Tx, id, user string) (TeambitionSession, teambitionProof, bool, error) {
	var view TeambitionSession
	var proof teambitionProof
	var raw []byte
	var expired, consumed bool
	if !membership.ValidateUUID(id) {
		return view, proof, false, ErrNotFound
	}
	err := tx.QueryRow(ctx, `SELECT id::text,status,error_code,expires_at,expires_at<=clock_timestamp(),consumed_at IS NOT NULL,pending_accounts
 FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND user_id=$2::uuid AND provider='teambition' AND purpose='PERSONAL' FOR UPDATE`, id, user).Scan(&view.ID, &view.Status, &view.ErrorCode, &view.ExpiresAt, &expired, &consumed, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, proof, false, ErrNotFound
	}
	if err != nil {
		return view, proof, false, err
	}
	var proofs []teambitionProof
	if json.Unmarshal(raw, &proofs) != nil {
		return view, proof, false, ErrOAuthSessionState
	}
	if len(proofs) == 1 {
		proof = proofs[0]
	}
	if expired && (view.Status == statusPending || view.Status == statusSelecting) {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='EXPIRED',pending_accounts='[]' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
		view.Status = statusExpired
		proof = teambitionProof{}
	}
	view.Identity = proof.Identity
	view.BindingID = proof.BindingID
	return view, proof, consumed, err
}

func (s Service) GetTeambitionSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (TeambitionSession, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return TeambitionSession{}, err
	}
	view, _, _, err := lockTeambitionSession(ctx, tx, id, actor.UserID)
	return view, err
}

// Consume before any network request. A crash or unknown exchange outcome cannot
// replay this code; the user starts a new authorization instead.
func (s Service) ConsumeTeambitionCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (TeambitionCallbackTarget, error) {
	var target TeambitionCallbackTarget
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return target, err
	}
	view, proof, consumed, err := lockTeambitionSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return target, err
	}
	if consumed || view.Status != statusPending || proof.ParentID == "" {
		return target, ErrOAuthSessionState
	}
	app, version, err := s.teambitionParent(ctx, tx, org, proof.ParentID)
	if err != nil {
		return target, err
	}
	email, err := personalEmail(ctx, tx, actor)
	if err != nil {
		return target, err
	}
	if version != proof.ParentVersion || app.CompanyID != proof.CompanyID || email != proof.Email {
		return target, ErrVersionConflict
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET consumed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return TeambitionCallbackTarget{app: app, proof: proof}, err
}

func (s Service) VerifyTeambitionCallback(ctx context.Context, target TeambitionCallbackTarget, code string) (TeambitionIdentity, error) {
	client := TeambitionPersonalClient{}
	if s.TeambitionPersonal != nil {
		client = *s.TeambitionPersonal
	}
	return client.Verify(ctx, target.app, code, target.proof.Email)
}

func PersonalFailure(err error) string {
	switch {
	case errors.Is(err, ErrPersonalIdentity):
		return "account_mismatch"
	case errors.Is(err, ErrPersonalCapability):
		return "permission_denied"
	case errors.Is(err, ErrPersonalRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrNeedsReauth):
		return "authentication_failed"
	case errors.Is(err, ErrVersionConflict), errors.Is(err, ErrNotFound):
		return "connection_changed"
	default:
		return "exchange_failed"
	}
}

func (s Service) FinishTeambitionCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, identity TeambitionIdentity, failure string) error {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return err
	}
	view, proof, consumed, err := lockTeambitionSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return err
	}
	if !consumed || view.Status != statusPending {
		return ErrOAuthSessionState
	}
	if failure == "" {
		app, version, e := s.teambitionParent(ctx, tx, org, proof.ParentID)
		email, ee := personalEmail(ctx, tx, actor)
		if e != nil || ee != nil || version != proof.ParentVersion || app.CompanyID != proof.CompanyID {
			failure = "connection_changed"
		} else if identity.Email != email || email != proof.Email || !companyPattern.MatchString(identity.ID) {
			failure = "account_mismatch"
		}
	}
	if failure != "" {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='FAILED',error_code=$2,pending_accounts='[]' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, failure)
		return err
	}
	proof.Identity = &identity
	raw, _ := json.Marshal([]teambitionProof{proof})
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='SELECTING',pending_accounts=$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, raw)
	return err
}

func (s Service) CompleteTeambitionPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (Connection, bool, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return Connection{}, false, err
	}
	view, proof, _, err := lockTeambitionSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	if view.Status == statusCompleted && proof.BindingID != "" {
		item, e := loadConnection(ctx, tx, proof.BindingID)
		return item, false, e
	}
	if view.Status != statusSelecting || proof.Identity == nil {
		return Connection{}, false, ErrOAuthSessionState
	}
	app, version, err := s.teambitionParent(ctx, tx, org, proof.ParentID)
	if err != nil {
		return Connection{}, false, err
	}
	email, err := personalEmail(ctx, tx, actor)
	if err != nil {
		return Connection{}, false, err
	}
	if version != proof.ParentVersion || app.CompanyID != proof.CompanyID {
		return Connection{}, false, ErrVersionConflict
	}
	if email != proof.Email || email != proof.Identity.Email {
		return Connection{}, false, ErrPersonalIdentity
	}
	var bindingID, external string
	var bindingVersion uint64
	err = tx.QueryRow(ctx, `SELECT id::text,external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='teambition' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, proof.ParentID, actor.UserID).Scan(&bindingID, &external, &bindingVersion)
	create := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !create {
		return Connection{}, false, err
	}
	if !create && external != proof.Identity.ID {
		return Connection{}, false, ErrPersonalIdentity
	}
	if bindingVersion != proof.BindingVersion {
		return Connection{}, false, ErrVersionConflict
	}
	if create {
		bindingID, err = membership.NewID()
		if err != nil {
			return Connection{}, false, err
		}
	}
	settings, _ := json.Marshal(map[string]any{"auth_method": teambitionPersonalMethod, "company_id": proof.CompanyID, "parent_version": proof.ParentVersion, "verified_email": email})
	if create {
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'teambition','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$3::uuid)`, bindingID, proof.ParentID, actor.UserID, proof.Identity.ID, proof.Identity.Name, settings)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',settings=$2::jsonb,display_name=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, bindingID, settings, proof.Identity.Name)
	}
	if isUniqueViolation(err) {
		return Connection{}, false, ErrPersonalInUse
	}
	if err != nil {
		return Connection{}, false, err
	}
	proof.BindingID = bindingID
	proof.Identity = nil
	raw, _ := json.Marshal([]teambitionProof{proof})
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='COMPLETED',completed_at=clock_timestamp(),pending_accounts=$2::jsonb WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, raw)
	if err != nil {
		return Connection{}, false, err
	}
	item, err := loadConnection(ctx, tx, bindingID)
	return item, true, err
}
