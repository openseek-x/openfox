package integrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/asana"
	"openfox/internal/membership"
)

var ErrAsanaAlreadyConnected = errors.New("integrations: Asana workspace is already connected")
var ErrAsanaAppChanged = errors.New("integrations: Asana application changed; disconnect and reconnect the workspace")

type AsanaStartInput struct {
	Purpose      string `json:"purpose"`
	ConnectionID string `json:"connection_id"`
	Version      uint64 `json:"version"`
}
type AsanaCompleteInput struct {
	WorkspaceID string `json:"workspace_id"`
}
type asanaProof struct {
	Purpose        string      `json:"purpose"`
	ParentID       string      `json:"parent_id,omitempty"`
	ParentVersion  uint64      `json:"parent_version,omitempty"`
	BindingVersion uint64      `json:"binding_version,omitempty"`
	Workspace      string      `json:"workspace,omitempty"`
	Email          string      `json:"email,omitempty"`
	Identity       *asana.User `json:"identity,omitempty"`
	BindingID      string      `json:"binding_id,omitempty"`
	AppID          string      `json:"app_id"`
}
type AsanaSession struct {
	WorkspaceID        string      `json:"workspace_id,omitempty"`
	ID                 string      `json:"id"`
	Purpose            string      `json:"purpose"`
	Status             string      `json:"status"`
	ErrorCode          string      `json:"error_code"`
	ExpiresAt          time.Time   `json:"expires_at"`
	Identity           *asana.User `json:"identity,omitempty"`
	BindingID          string      `json:"binding_id,omitempty"`
	AffectedWorkspaces []string    `json:"affected_workspaces"`
}
type AsanaCallbackTarget struct {
	proof    asanaProof
	verifier string
}
type AsanaCallbackResult struct {
	identity asana.User
	token    asana.Token
}
type AsanaCompleteTarget struct {
	proof     asanaProof
	token     asana.Token
	workspace asana.Named
}

func (s Service) asanaClient(personal bool) *asana.Client {
	if personal {
		return s.AsanaIdentity
	}
	return s.Asana
}
func (s Service) asanaRedirect(personal bool) string {
	suffix := "/api/v1/integrations/asana/callback"
	if personal {
		suffix = "/api/v1/integrations/asana/identity/callback"
	}
	return strings.TrimRight(s.PublicURL, "/") + suffix
}
func (s Service) asanaConfigured(personal bool) bool {
	c := s.asanaClient(personal)
	return c != nil && c.App.ClientID != "" && c.App.ClientSecret != "" && s.Box != nil && s.PublicURL != "" && (!personal || s.Asana != nil && s.Asana.App.ClientID != c.App.ClientID)
}
func asanaParent(ctx context.Context, tx pgx.Tx, id string, active bool) (string, uint64, error) {
	if !membership.ValidateUUID(id) {
		return "", 0, ErrInvalidRequest
	}
	var workspace, status string
	var version uint64
	err := tx.QueryRow(ctx, `SELECT external_id,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='asana' AND scope='ORGANIZATION' AND status<>'DISCONNECTED' FOR SHARE`, id).Scan(&workspace, &version, &status)
	if err != nil {
		return "", 0, asanaMissing(err)
	}
	if active && status != StatusActive {
		return "", 0, ErrNeedsReauth
	}
	return workspace, version, nil
}
func (s Service) StartAsana(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in AsanaStartInput) (StartResult, error) {
	personal := in.Purpose == PurposePersonal
	if in.Purpose != PurposeOrganization && in.Purpose != PurposeReauthorize && !personal {
		return StartResult{}, ErrInvalidRequest
	}
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return StartResult{}, err
	}
	if !personal && !CanManage(actor.Role) {
		return StartResult{}, ErrForbidden
	}
	if !s.asanaConfigured(personal) {
		return StartResult{}, ErrNotConfigured
	}
	proof := asanaProof{Purpose: in.Purpose, AppID: s.asanaClient(personal).App.ClientID, ParentID: in.ConnectionID}
	if personal || in.Purpose == PurposeReauthorize {
		workspace, version, err := asanaParent(ctx, tx, in.ConnectionID, personal)
		if err != nil {
			return StartResult{}, err
		}
		proof.Workspace, proof.ParentVersion = workspace, version
		var app string
		if err := tx.QueryRow(ctx, `SELECT g.app_client_id FROM openfox.integration_connections c JOIN openfox.integration_asana_grants g ON g.organization_id=c.organization_id AND g.id=c.asana_grant_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid`, in.ConnectionID).Scan(&app); err != nil {
			return StartResult{}, err
		}
		if app != s.Asana.App.ClientID {
			return StartResult{}, ErrAsanaAppChanged
		}
		if personal {
			proof.Email, err = personalEmail(ctx, tx, actor)
			if err != nil {
				return StartResult{}, err
			}
			var current uint64
			err = tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='asana' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED'`, in.ConnectionID, actor.UserID).Scan(&current)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return StartResult{}, err
			}
			if current != in.Version {
				return StartResult{}, ErrVersionConflict
			}
			proof.BindingVersion = current
		} else if in.Version != version {
			return StartResult{}, ErrVersionConflict
		}
	} else if in.ConnectionID != "" || in.Version != 0 {
		return StartResult{}, ErrInvalidRequest
	}
	id, err := membership.NewID()
	if err != nil {
		return StartResult{}, err
	}
	random := make([]byte, 64)
	if _, err = rand.Read(random); err != nil {
		return StartResult{}, err
	}
	state := base64.RawURLEncoding.EncodeToString(random[:32])
	verifier := base64.RawURLEncoding.EncodeToString(random[32:])
	sum := sha256.Sum256([]byte(verifier))
	authorize, err := s.asanaClient(personal).AuthorizeURL(state, base64.RawURLEncoding.EncodeToString(sum[:]), s.asanaRedirect(personal), personal)
	if err != nil {
		return StartResult{}, err
	}
	sealed, _, err := s.seal([]byte(verifier), oauthAAD(org, id))
	if err != nil {
		return StartResult{}, err
	}
	raw, _ := json.Marshal([]asanaProof{proof})
	var parent *string
	if proof.ParentID != "" {
		parent = &proof.ParentID
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO openfox.integration_oauth_states(organization_id,id,provider,purpose,user_id,state_hash,code_verifier_sealed,connection_id,pending_accounts) VALUES(openfox_private.current_organization_id(),$1::uuid,'asana',$2,$3::uuid,$4,$5,$6::uuid,$7::jsonb) RETURNING expires_at`, id, strings.ToUpper(in.Purpose), actor.UserID, TokenHash(state), sealed, parent, raw).Scan(&expires)
	return StartResult{SessionID: id, AuthorizeURL: authorize, ExpiresAt: expires}, err
}
func lockAsanaSession(ctx context.Context, tx pgx.Tx, id, user string) (AsanaSession, asanaProof, []byte, []byte, bool, error) {
	var view AsanaSession
	var proof asanaProof
	var raw, verifier, pending []byte
	var expired, consumed bool
	var purpose string
	if !membership.ValidateUUID(id) {
		return view, proof, nil, nil, false, ErrNotFound
	}
	err := tx.QueryRow(ctx, `SELECT id::text,status,error_code,expires_at,expires_at<=clock_timestamp(),consumed_at IS NOT NULL,pending_accounts,code_verifier_sealed,pending_sealed,lower(purpose) FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND user_id=$2::uuid AND provider='asana' FOR UPDATE`, id, user).Scan(&view.ID, &view.Status, &view.ErrorCode, &view.ExpiresAt, &expired, &consumed, &raw, &verifier, &pending, &purpose)
	if err != nil {
		return view, proof, nil, nil, false, asanaMissing(err)
	}
	var proofs []asanaProof
	if json.Unmarshal(raw, &proofs) != nil {
		return view, proof, nil, nil, false, ErrOAuthSessionState
	}
	if len(proofs) == 1 {
		proof = proofs[0]
	}
	proof.Purpose = purpose
	if expired && (view.Status == statusPending || view.Status == statusSelecting) {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
		view.Status = statusExpired
		proof = asanaProof{Purpose: proof.Purpose}
		verifier = nil
		pending = nil
	}
	view.Purpose, view.Identity, view.BindingID = proof.Purpose, proof.Identity, proof.BindingID
	view.WorkspaceID = proof.Workspace
	view.AffectedWorkspaces = []string{}
	return view, proof, verifier, pending, consumed, err
}
func (s Service) GetAsanaSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AsanaSession, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return AsanaSession{}, err
	}
	view, proof, _, _, _, err := lockAsanaSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return view, err
	}
	if proof.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return AsanaSession{}, ErrForbidden
	}
	if proof.Purpose != PurposePersonal && proof.Identity != nil {
		rows, e := tx.Query(ctx, `SELECT c.display_name FROM openfox.integration_connections c JOIN openfox.integration_asana_grants g ON g.organization_id=c.organization_id AND g.id=c.asana_grant_id WHERE c.organization_id=openfox_private.current_organization_id() AND g.app_client_id=$1 AND g.subject_id=$2 AND c.status<>'DISCONNECTED' ORDER BY c.id LIMIT 500`, proof.AppID, proof.Identity.GID)
		if e != nil {
			return view, e
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				return view, e
			}
			view.AffectedWorkspaces = append(view.AffectedWorkspaces, name)
		}
		err = rows.Err()
	}
	return view, err
}
func (s Service) ConsumeAsanaCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, personal bool) (AsanaCallbackTarget, error) {
	var out AsanaCallbackTarget
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return out, err
	}
	view, proof, sealed, _, consumed, err := lockAsanaSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return out, err
	}
	if view.Status != statusPending || consumed || personal != (proof.Purpose == PurposePersonal) {
		return out, ErrOAuthSessionState
	}
	if !personal && !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	if !s.asanaConfigured(personal) || proof.AppID != s.asanaClient(personal).App.ClientID {
		return out, ErrNotConfigured
	}
	if proof.ParentID != "" {
		workspace, version, e := asanaParent(ctx, tx, proof.ParentID, personal)
		if e != nil {
			return out, e
		}
		if workspace != proof.Workspace || version != proof.ParentVersion {
			return out, ErrVersionConflict
		}
	}
	if personal {
		email, e := personalEmail(ctx, tx, actor)
		if e != nil {
			return out, e
		}
		if email != proof.Email {
			return out, ErrPersonalIdentity
		}
	}
	verifier, err := s.open(sealed, oauthAAD(org, id))
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET consumed_at=clock_timestamp(),code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return AsanaCallbackTarget{proof: proof, verifier: string(verifier)}, err
}
func (s Service) VerifyAsanaCallback(ctx context.Context, runtime AsanaRuntime, org string, target AsanaCallbackTarget, code string) (AsanaCallbackResult, error) {
	var out AsanaCallbackResult
	personal := target.proof.Purpose == PurposePersonal
	client := s.asanaClient(personal)
	if !s.asanaConfigured(personal) {
		return out, ErrNotConfigured
	}
	token, err := client.Exchange(ctx, code, target.verifier, s.asanaRedirect(personal))
	if err != nil {
		return out, err
	}
	if token.Scope != "" && !asanaScopeContains(token.Scope, personal) {
		return out, ErrPersonalCapability
	}
	identity, err := client.UserMe(ctx, token.AccessToken)
	if err != nil {
		return out, err
	}
	if len(identity.Workspaces) > 200 {
		return out, ErrInvalidRequest
	}
	identity.Name = asanaDisplayName(identity.Name, identity.GID)
	for i := range identity.Workspaces {
		identity.Workspaces[i].Name = asanaDisplayName(identity.Workspaces[i].Name, identity.Workspaces[i].GID)
	}
	encoded, _ := json.Marshal(identity)
	if len(encoded) > 60<<10 {
		return out, ErrInvalidRequest
	}
	if personal {
		if normalizedAsanaEmail(identity.Email) != target.proof.Email {
			return out, ErrPersonalIdentity
		}
		own, e := client.WorkspaceUser(ctx, token.AccessToken, target.proof.Workspace, identity.GID)
		if e != nil {
			return out, e
		}
		if normalizedAsanaEmail(own.Email) != target.proof.Email {
			return out, ErrPersonalIdentity
		}
		_, e = runtime.Do(ctx, org, target.proof.ParentID, target.proof.ParentVersion, 0, func(access string) error {
			parent, e := s.Asana.WorkspaceUser(ctx, access, target.proof.Workspace, identity.GID)
			if e != nil {
				return e
			}
			if normalizedAsanaEmail(parent.Email) != target.proof.Email {
				return ErrPersonalIdentity
			}
			return nil
		})
		if e != nil {
			return out, e
		}
		identity.Email = target.proof.Email
		identity.Workspaces = nil
		return AsanaCallbackResult{identity: identity}, nil // personal secrets never leave this function
	}
	if !asana.ValidToken(token.RefreshToken) {
		return out, ErrNeedsReauth
	}
	return AsanaCallbackResult{identity: identity, token: token}, nil
}
func normalizedAsanaEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
func (s Service) FinishAsanaCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, result AsanaCallbackResult, failure string) error {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return err
	}
	view, proof, _, _, consumed, err := lockAsanaSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return err
	}
	if view.Status != statusPending || !consumed {
		return ErrOAuthSessionState
	}
	personal := proof.Purpose == PurposePersonal
	if !personal && !CanManage(actor.Role) {
		failure = "permission_denied"
	}
	if !s.asanaConfigured(personal) || proof.AppID != s.asanaClient(personal).App.ClientID {
		failure = "configuration_changed"
	}
	if proof.ParentID != "" {
		workspace, version, e := asanaParent(ctx, tx, proof.ParentID, personal)
		if e != nil || version != proof.ParentVersion || workspace != proof.Workspace {
			failure = "connection_changed"
		}
	}
	if personal {
		email, e := personalEmail(ctx, tx, actor)
		if e != nil || email != proof.Email {
			failure = "account_mismatch"
		}
	}
	var pending []byte
	if failure == "" {
		if !asana.ValidID(result.identity.GID) {
			return ErrPersonalIdentity
		}
		proof.Identity = &result.identity
		if !personal {
			plain, _ := json.Marshal(result.token)
			pending, _, err = s.seal(plain, oauthAAD(org, id))
			if err != nil {
				return err
			}
		}
	}
	raw, _ := json.Marshal([]asanaProof{proof})
	status := statusSelecting
	if failure != "" {
		status = statusFailed
		raw = []byte("[]")
		pending = nil
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status=$2,error_code=$3,pending_accounts=$4::jsonb,pending_sealed=$5,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, status, failure, raw, pending)
	return err
}

// Prepare/verify/complete split keeps all HTTP calls outside row-lock transactions.
func (s Service) PrepareAsanaComplete(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in AsanaCompleteInput) (AsanaCompleteTarget, error) {
	var out AsanaCompleteTarget
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return out, err
	}
	view, proof, _, pending, _, err := lockAsanaSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return out, err
	}
	if proof.Purpose == PurposePersonal {
		return out, ErrInvalidRequest
	}
	if !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	if view.Status == statusCompleted {
		if in.WorkspaceID != proof.Workspace {
			return out, ErrVersionConflict
		}
		out.proof = proof
		return out, nil
	}
	if view.Status != statusSelecting || proof.Identity == nil || !asana.ValidID(in.WorkspaceID) {
		return out, ErrOAuthSessionState
	}
	if !s.asanaConfigured(false) || proof.AppID != s.Asana.App.ClientID {
		return out, ErrNotConfigured
	}
	for _, workspace := range proof.Identity.Workspaces {
		if workspace.GID == in.WorkspaceID {
			out.workspace = workspace
			break
		}
	}
	if out.workspace.GID == "" || proof.Purpose == PurposeReauthorize && proof.Workspace != in.WorkspaceID {
		return out, ErrInvalidRequest
	}
	plain, err := s.open(pending, oauthAAD(org, id))
	if err != nil {
		return out, err
	}
	if json.Unmarshal(plain, &out.token) != nil || !out.token.ExpiresAt.After(time.Now()) {
		return out, ErrNeedsReauth
	}
	out.proof = proof
	return out, nil
}
func (s Service) VerifyAsanaComplete(ctx context.Context, target AsanaCompleteTarget) error {
	if target.proof.BindingID != "" {
		return nil
	}
	_, err := s.Asana.WorkspaceUser(ctx, target.token.AccessToken, target.workspace.GID, target.proof.Identity.GID)
	if err != nil {
		return err
	}
	_, err = s.Asana.ProjectsPage(ctx, target.token.AccessToken, target.workspace.GID, "")
	return err
}
func (s Service) CompleteAsanaOrganization(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, target AsanaCompleteTarget) (Connection, bool, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return Connection{}, false, err
	}
	if !CanManage(actor.Role) {
		return Connection{}, false, ErrForbidden
	}
	view, proof, _, _, _, err := lockAsanaSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	if view.Status == statusCompleted && proof.BindingID != "" {
		selected := target.workspace.GID
		if selected == "" {
			selected = target.proof.Workspace
		}
		if selected != proof.Workspace {
			return Connection{}, false, ErrVersionConflict
		}
		item, e := loadConnection(ctx, tx, proof.BindingID)
		return item, false, e
	}
	if view.Status != statusSelecting || proof.Purpose == PurposePersonal || proof.Identity == nil || target.proof.Identity == nil || proof.Identity.GID != target.proof.Identity.GID || target.workspace.GID == "" {
		return Connection{}, false, ErrOAuthSessionState
	}
	if !s.asanaConfigured(false) || proof.AppID != s.Asana.App.ClientID {
		return Connection{}, false, ErrNotConfigured
	}
	grant, err := membership.NewID()
	if err != nil {
		return Connection{}, false, err
	}
	// Upsert first claims the unique grant key. It never replaces ciphertext before
	// taking the grant lock and checking the selected parent CAS.
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_asana_grants(organization_id,id,app_client_id,subject_id,subject_name,scope,status,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5,'DISCONNECTED',$6::uuid) ON CONFLICT(organization_id,app_client_id,subject_id) DO NOTHING`, grant, proof.AppID, proof.Identity.GID, proof.Identity.Name, target.token.Scope, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_asana_grants WHERE organization_id=openfox_private.current_organization_id() AND app_client_id=$1 AND subject_id=$2 FOR UPDATE`, proof.AppID, proof.Identity.GID).Scan(&grant)
	if err != nil {
		return Connection{}, false, err
	}
	var binding string
	if proof.ParentID != "" {
		var workspace, parentGrant string
		var version uint64
		err = tx.QueryRow(ctx, `SELECT external_id,version,asana_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='asana' AND scope='ORGANIZATION' AND status<>'DISCONNECTED' FOR UPDATE`, proof.ParentID).Scan(&workspace, &version, &parentGrant)
		if err != nil {
			return Connection{}, false, asanaMissing(err)
		}
		if version != proof.ParentVersion || workspace != target.workspace.GID {
			return Connection{}, false, ErrVersionConflict
		}
		// Changing the delegating account requires a new connection; do not strand or
		// silently combine a different grant's references.
		if parentGrant != grant {
			return Connection{}, false, ErrPersonalIdentity
		}
		binding = proof.ParentID
	} else {
		var existing string
		err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='asana' AND scope='ORGANIZATION' AND external_id=$1 AND status<>'DISCONNECTED'`, target.workspace.GID).Scan(&existing)
		if err == nil {
			return Connection{}, false, ErrAsanaAlreadyConnected
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, false, err
		}
		binding, err = membership.NewID()
		if err != nil {
			return Connection{}, false, err
		}
	}
	// Deterministic parent order also fences in-flight crawls for every workspace
	// sharing this user-delegated grant. Ordinary token renewal does not do this.
	rows, err := tx.Query(ctx, `SELECT id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND asana_grant_id=$1::uuid AND status<>'DISCONNECTED' ORDER BY id FOR UPDATE`, grant)
	if err != nil {
		return Connection{}, false, err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Connection{}, false, err
	}
	creds, _ := json.Marshal(asanaCredentials{AccessToken: target.token.AccessToken, RefreshToken: target.token.RefreshToken})
	sealed, key, err := s.seal(creds, asanaGrantAAD(org, grant))
	if err != nil {
		return Connection{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_asana_grants SET credentials_sealed=$2,credentials_key_id=$3,expires_at=$4,scope=$5,status='ACTIVE',generation=generation+1,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,retry_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, grant, sealed, key, target.token.ExpiresAt, target.token.Scope)
	if err != nil {
		return Connection{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND asana_grant_id=$1::uuid AND status<>'DISCONNECTED'`, grant)
	if err != nil {
		return Connection{}, false, err
	}
	if proof.ParentID == "" {
		settings, _ := json.Marshal(map[string]string{"auth_method": "asana_oauth", "delegating_user": proof.Identity.Name, "delegating_user_id": proof.Identity.GID})
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,settings,asana_grant_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'asana','ORGANIZATION',$2,$3,'workspace',$4::jsonb,$5::uuid,$6::uuid)`, binding, target.workspace.GID, target.workspace.Name, settings, grant, actor.UserID)
		if isUniqueViolation(err) {
			return Connection{}, false, ErrAsanaAlreadyConnected
		}
		if err != nil {
			return Connection{}, false, err
		}
	}
	proof.Workspace = target.workspace.GID
	return s.finishAsanaBinding(ctx, tx, id, proof, binding)
}
func (s Service) CompleteAsanaPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (Connection, bool, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return Connection{}, false, err
	}
	view, proof, _, _, _, err := lockAsanaSession(ctx, tx, id, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	if view.Status == statusCompleted && proof.BindingID != "" {
		item, e := loadConnection(ctx, tx, proof.BindingID)
		return item, false, e
	}
	if view.Status != statusSelecting || proof.Purpose != PurposePersonal || proof.Identity == nil {
		return Connection{}, false, ErrOAuthSessionState
	}
	if !s.asanaConfigured(true) || proof.AppID != s.AsanaIdentity.App.ClientID {
		return Connection{}, false, ErrNotConfigured
	}
	// Claim -> parent -> user -> child is used for initial binding and recovery.
	// The generated subject/FK also rejects any competing live different owner.
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_asana_identity_claims(organization_id,subject_id,owner_user_id) VALUES(openfox_private.current_organization_id(),$1,$2::uuid) ON CONFLICT(organization_id,subject_id) DO NOTHING`, proof.Identity.GID, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	var owner string
	err = tx.QueryRow(ctx, `SELECT owner_user_id::text FROM openfox.integration_asana_identity_claims WHERE organization_id=openfox_private.current_organization_id() AND subject_id=$1 FOR UPDATE`, proof.Identity.GID).Scan(&owner)
	if err != nil {
		return Connection{}, false, err
	}
	if owner != actor.UserID {
		var live bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND asana_identity_subject=$1)`, proof.Identity.GID).Scan(&live)
		if err != nil {
			return Connection{}, false, err
		}
		if live {
			return Connection{}, false, ErrPersonalInUse
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_asana_identity_claims SET owner_user_id=$2::uuid WHERE organization_id=openfox_private.current_organization_id() AND subject_id=$1`, proof.Identity.GID, actor.UserID)
		if err != nil {
			return Connection{}, false, err
		}
	}
	workspace, version, err := asanaParent(ctx, tx, proof.ParentID, true)
	if err != nil {
		return Connection{}, false, err
	}
	if version != proof.ParentVersion || workspace != proof.Workspace {
		return Connection{}, false, ErrVersionConflict
	}
	email, err := personalEmail(ctx, tx, actor)
	if err != nil {
		return Connection{}, false, err
	}
	if email != proof.Email || email != proof.Identity.Email {
		return Connection{}, false, ErrPersonalIdentity
	}
	var binding, external string
	var current uint64
	err = tx.QueryRow(ctx, `SELECT id::text,external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='asana' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, proof.ParentID, actor.UserID).Scan(&binding, &external, &current)
	create := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !create {
		return Connection{}, false, err
	}
	if current != proof.BindingVersion {
		return Connection{}, false, ErrVersionConflict
	}
	if !create && external != proof.Identity.GID {
		return Connection{}, false, ErrPersonalIdentity
	}
	settings, _ := json.Marshal(map[string]any{"auth_method": "asana_identity", "company_id": workspace, "parent_version": version, "verified_email": email, "verified_at": time.Now().UTC()})
	if create {
		binding, err = membership.NewID()
		if err != nil {
			return Connection{}, false, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'asana','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$3::uuid)`, binding, proof.ParentID, actor.UserID, proof.Identity.GID, proof.Identity.Name, settings)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',settings=$2::jsonb,display_name=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding, settings, proof.Identity.Name)
	}
	if isUniqueViolation(err) {
		return Connection{}, false, ErrPersonalInUse
	}
	if err != nil {
		return Connection{}, false, err
	}
	return s.finishAsanaBinding(ctx, tx, id, proof, binding)
}
func (s Service) finishAsanaBinding(ctx context.Context, tx pgx.Tx, id string, proof asanaProof, binding string) (Connection, bool, error) {
	proof.BindingID = binding
	proof.Identity = nil
	raw, _ := json.Marshal([]asanaProof{proof})
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='COMPLETED',completed_at=clock_timestamp(),pending_accounts=$2::jsonb,pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, raw)
	if err != nil {
		return Connection{}, false, err
	}
	item, err := loadConnection(ctx, tx, binding)
	return item, true, err
}

// The server records an observed scope only when the token response supplies it.
func asanaScopeContains(scope string, personal bool) bool {
	required := asana.OrganizationScope
	if personal {
		required = asana.IdentityScope
	}
	fields := strings.Fields(scope)
	for _, want := range strings.Fields(required) {
		found := false
		for _, got := range fields {
			if want == got {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func asanaDisplayName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return id
	}
	runes := []rune(name)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return name
}
