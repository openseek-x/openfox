package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/membership"
	"openfox/internal/secrets"
)

const ProviderBitbucket = "bitbucket"

var ErrBitbucketAlreadyConnected = errors.New("integrations: Bitbucket workspace already connected")
var ErrBitbucketAppChanged = errors.New("integrations: Bitbucket consumer changed; reauthorize")

type BitbucketStartInput struct {
	Purpose      string `json:"purpose"`
	ConnectionID string `json:"connection_id"`
	Version      uint64 `json:"version"`
}
type BitbucketCompleteInput struct {
	WorkspaceID string `json:"workspace_id"`
}
type bitbucketProof struct {
	Purpose             string                `json:"purpose"`
	ParentID            string                `json:"parent_id,omitempty"`
	ParentVersion       uint64                `json:"parent_version,omitempty"`
	BindingVersion      uint64                `json:"binding_version,omitempty"`
	Workspace           string                `json:"workspace,omitempty"`
	AppID               string                `json:"app_id"`
	ConsumerFingerprint string                `json:"consumer_fingerprint"`
	Identity            *bitbucket.User       `json:"identity,omitempty"`
	Workspaces          []bitbucket.Workspace `json:"workspaces,omitempty"`
	PreviousSubject     *bitbucket.User       `json:"previous_subject,omitempty"`
	BindingID           string                `json:"binding_id,omitempty"`
}
type BitbucketSession struct {
	ID              string                `json:"id"`
	Purpose         string                `json:"purpose"`
	Status          string                `json:"status"`
	ErrorCode       string                `json:"error_code"`
	ExpiresAt       time.Time             `json:"expires_at"`
	Identity        *bitbucket.User       `json:"identity,omitempty"`
	Workspaces      []bitbucket.Workspace `json:"workspaces"`
	WorkspaceID     string                `json:"workspace_id,omitempty"`
	ConnectionID    string                `json:"connection_id,omitempty"`
	ParentVersion   uint64                `json:"parent_version"`
	BindingID       string                `json:"binding_id,omitempty"`
	PreviousSubject *bitbucket.User       `json:"previous_subject,omitempty"`
	SubjectChanged  bool                  `json:"subject_changed"`
}
type BitbucketCallbackTarget struct{ proof bitbucketProof }
type BitbucketCallbackResult struct {
	identity   bitbucket.User
	workspaces []bitbucket.Workspace
	token      bitbucket.Token
}
type bitbucketPending struct {
	Token     bitbucket.Token `json:"token"`
	ExpiresAt time.Time       `json:"expires_at"`
}
type BitbucketCompleteTarget struct {
	sessionID string
	proof     bitbucketProof
	token     bitbucket.Token
	workspace bitbucket.Workspace
	verified  bool
}

func bitbucketMissing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s Service) bitbucketClient(personal bool) *bitbucket.Client {
	if personal {
		return s.BitbucketIdentity
	}
	return s.Bitbucket
}
func (s Service) bitbucketConfigured(personal bool) bool {
	c := s.bitbucketClient(personal)
	return c != nil && c.App.ClientID != "" && c.App.ClientSecret != "" && s.Box != nil && s.PublicURL != "" && (!personal || s.Bitbucket != nil && c.App.ClientID != s.Bitbucket.App.ClientID)
}

// BitbucketIdentityConfigured controls the personal binding entry independently
// of the organization consumer. Existing bindings remain visible when disabled.
func (s Service) BitbucketIdentityConfigured() bool { return s.bitbucketConfigured(true) }
func bitbucketConsumerFingerprint(c *bitbucket.Client) string {
	if c == nil {
		return ""
	}
	b, _ := json.Marshal(c.App)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s Service) bitbucketProofConfigured(p bitbucketProof) bool {
	personal := p.Purpose == PurposePersonal
	return s.bitbucketConfigured(personal) && p.AppID == s.bitbucketClient(personal).App.ClientID && p.ConsumerFingerprint == bitbucketConsumerFingerprint(s.bitbucketClient(personal))
}

// Uses the existing authenticated envelope format. The provider-specific size
// leaves room for two bounded OAuth tokens plus the existing sealed hook fields.
func (s Service) sealBitbucket(plain []byte, aad string) ([]byte, string, error) {
	if s.Box == nil {
		return nil, "", ErrSecretsUnavailable
	}
	if len(plain) > 32768-keyIDLen-secrets.NonceSize-16 {
		return nil, "", ErrInvalidRequest
	}
	v, e := s.Box.SealOAuth(plain, aad)
	if e != nil {
		return nil, "", e
	}
	b := append([]byte(v.KeyID), v.Nonce...)
	return append(b, v.Ciphertext...), v.KeyID, nil
}
func bitbucketParent(ctx context.Context, tx pgx.Tx, id string, active bool) (string, uint64, error) {
	if !membership.ValidateUUID(id) {
		return "", 0, ErrInvalidRequest
	}
	var workspace, status string
	var version uint64
	e := tx.QueryRow(ctx, `SELECT external_id,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION' AND status<>'DISCONNECTED' FOR UPDATE`, id).Scan(&workspace, &version, &status)
	if e != nil {
		return "", 0, bitbucketMissing(e)
	}
	if active && status != StatusActive {
		return "", 0, ErrNeedsReauth
	}
	return workspace, version, nil
}

// Initial authorization has no parent. All existing-parent mutations take the
// parent before membership/child rows; member suspension uses the member lock.
func (s Service) StartBitbucket(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in BitbucketStartInput) (StartResult, error) {
	personal := in.Purpose == PurposePersonal
	if !personal && in.Purpose != PurposeOrganization && in.Purpose != PurposeReauthorize {
		return StartResult{}, ErrInvalidRequest
	}
	if !s.bitbucketConfigured(personal) {
		return StartResult{}, ErrNotConfigured
	}
	p := bitbucketProof{Purpose: in.Purpose, ParentID: in.ConnectionID, AppID: s.bitbucketClient(personal).App.ClientID, ConsumerFingerprint: bitbucketConsumerFingerprint(s.bitbucketClient(personal))}
	if personal || in.Purpose == PurposeReauthorize {
		var e error
		p.Workspace, p.ParentVersion, e = bitbucketParent(ctx, tx, in.ConnectionID, personal)
		if e != nil {
			return StartResult{}, e
		}
		var previous bitbucket.User
		var consumer string
		e = tx.QueryRow(ctx, `SELECT subject_id::text,subject_name,consumer_id FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, in.ConnectionID).Scan(&previous.UUID, &previous.DisplayName, &consumer)
		if e != nil {
			return StartResult{}, e
		}
		if consumer != s.Bitbucket.App.ClientID {
			return StartResult{}, ErrBitbucketAppChanged
		}
		p.PreviousSubject = &previous
		if !personal && in.Version != p.ParentVersion {
			return StartResult{}, ErrVersionConflict
		}
	} else if in.ConnectionID != "" || in.Version != 0 {
		return StartResult{}, ErrInvalidRequest
	}
	if e := checkPersonalActor(ctx, tx, org, actor); e != nil {
		return StartResult{}, e
	}
	if !personal && !CanManage(actor.Role) {
		return StartResult{}, ErrForbidden
	}
	if personal {
		e := tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='bitbucket' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED'`, p.ParentID, actor.UserID).Scan(&p.BindingVersion)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return StartResult{}, e
		}
		if in.Version != p.BindingVersion {
			return StartResult{}, ErrVersionConflict
		}
	}
	id, e := membership.NewID()
	if e != nil {
		return StartResult{}, e
	}
	state, e := randomBase64URL(32)
	if e != nil {
		return StartResult{}, e
	}
	authorize, e := s.bitbucketClient(personal).AuthorizeURL(state)
	if e != nil {
		return StartResult{}, e
	}
	raw, _ := json.Marshal([]bitbucketProof{p})
	var parent *string
	if p.ParentID != "" {
		parent = &p.ParentID
	}
	var expiry time.Time
	e = tx.QueryRow(ctx, `INSERT INTO openfox.integration_oauth_states(organization_id,id,provider,purpose,user_id,state_hash,connection_id,pending_accounts) VALUES(openfox_private.current_organization_id(),$1::uuid,'bitbucket',$2,$3::uuid,$4,$5::uuid,$6::jsonb) RETURNING expires_at`, id, strings.ToUpper(in.Purpose), actor.UserID, TokenHash(state), parent, raw).Scan(&expiry)
	return StartResult{SessionID: id, AuthorizeURL: authorize, ExpiresAt: expiry}, e
}
func lockBitbucketSession(ctx context.Context, tx pgx.Tx, id, user string) (BitbucketSession, bitbucketProof, []byte, bool, error) {
	var v BitbucketSession
	var p bitbucketProof
	var raw, pending []byte
	var expired, consumed bool
	var purpose string
	if !membership.ValidateUUID(id) {
		return v, p, nil, false, ErrNotFound
	}
	e := tx.QueryRow(ctx, `SELECT id::text,status,error_code,expires_at,expires_at<=clock_timestamp(),consumed_at IS NOT NULL,pending_accounts,pending_sealed,lower(purpose) FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND user_id=$2::uuid AND provider='bitbucket' FOR UPDATE`, id, user).Scan(&v.ID, &v.Status, &v.ErrorCode, &v.ExpiresAt, &expired, &consumed, &raw, &pending, &purpose)
	if e != nil {
		return v, p, nil, false, bitbucketMissing(e)
	}
	var proofs []bitbucketProof
	if json.Unmarshal(raw, &proofs) != nil {
		return v, p, nil, false, ErrOAuthSessionState
	}
	if len(proofs) == 1 {
		p = proofs[0]
	}
	p.Purpose = purpose
	if expired && (v.Status == statusPending || v.Status == statusSelecting) {
		v.Status = statusExpired
		p = bitbucketProof{Purpose: purpose}
		pending = nil
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	}
	v.Purpose, v.Identity, v.Workspaces, v.WorkspaceID, v.ConnectionID, v.ParentVersion, v.BindingID, v.PreviousSubject = p.Purpose, p.Identity, p.Workspaces, p.Workspace, p.ParentID, p.ParentVersion, p.BindingID, p.PreviousSubject
	if v.Workspaces == nil {
		v.Workspaces = []bitbucket.Workspace{}
	}
	v.SubjectChanged = p.Purpose == PurposeReauthorize && p.Identity != nil && p.PreviousSubject != nil && p.Identity.UUID != p.PreviousSubject.UUID
	return v, p, pending, consumed, e
}
func (s Service) GetBitbucketSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (BitbucketSession, error) {
	v, p, _, _, e := lockBitbucketSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return v, e
	}
	if e = checkPersonalActor(ctx, tx, org, actor); e != nil {
		return BitbucketSession{}, e
	}
	if p.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return BitbucketSession{}, ErrForbidden
	}
	return v, nil
}
func (s Service) bitbucketRecheck(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p bitbucketProof) error {
	if p.ParentID != "" {
		w, v, e := bitbucketParent(ctx, tx, p.ParentID, p.Purpose == PurposePersonal)
		if e != nil {
			return e
		}
		if w != p.Workspace || v != p.ParentVersion {
			return ErrVersionConflict
		}
		var locked string
		if e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, p.ParentID).Scan(&locked); e != nil {
			return e
		}
	}
	if e := checkPersonalActor(ctx, tx, org, actor); e != nil {
		return e
	}
	if p.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return ErrForbidden
	}
	if !s.bitbucketProofConfigured(p) {
		return ErrBitbucketAppChanged
	}
	return nil
}
func (s Service) ConsumeBitbucketCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (BitbucketCallbackTarget, error) {
	v, p, _, consumed, e := lockBitbucketSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return BitbucketCallbackTarget{}, e
	}
	if v.Status != statusPending || consumed {
		return BitbucketCallbackTarget{}, ErrOAuthSessionState
	}
	if e = s.bitbucketRecheck(ctx, tx, org, actor, p); e != nil {
		return BitbucketCallbackTarget{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET consumed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return BitbucketCallbackTarget{proof: p}, e
}
func (s Service) VerifyBitbucketCallback(ctx context.Context, target BitbucketCallbackTarget, code string) (BitbucketCallbackResult, error) {
	var out BitbucketCallbackResult
	p := target.proof
	if !s.bitbucketProofConfigured(p) {
		return out, ErrBitbucketAppChanged
	}
	personal := p.Purpose == PurposePersonal
	c := s.bitbucketClient(personal)
	token, e := c.Exchange(ctx, code)
	if e != nil {
		return out, e
	}
	if e = bitbucket.ValidateScopes(token.Scopes, personal); e != nil {
		return out, e
	}
	identity, e := c.Identity(ctx, token.AccessToken)
	if e != nil {
		return out, e
	}
	identity.DisplayName = asanaDisplayName(identity.DisplayName, identity.UUID)
	if personal {
		return BitbucketCallbackResult{identity: identity}, nil
	} // no personal token survives this call
	ctx, cancel := bitbucket.WithTraversalBudget(ctx)
	defer cancel()
	next := ""
	seen := map[string]bool{}
	unique := map[string]bool{}
	total := 0
	workspaces := []bitbucket.Workspace{}
	for {
		rows, e := c.Workspaces(ctx, token.AccessToken, next)
		if e != nil {
			return out, e
		}
		total += len(rows.Values)
		if total > 100 {
			return out, bitbucket.ErrIncomplete
		}
		for _, row := range rows.Values {
			if unique[row.Workspace.UUID] {
				return out, bitbucket.ErrIncomplete
			}
			unique[row.Workspace.UUID] = true
			if *row.Administrator {
				w := row.Workspace
				w.Name = asanaDisplayName(w.Name, w.UUID)
				workspaces = append(workspaces, w)
			}
		}
		if rows.Next == "" {
			break
		}
		if seen[rows.Next] || total == 100 {
			return out, bitbucket.ErrIncomplete
		}
		seen[rows.Next] = true
		next = rows.Next
	}
	return BitbucketCallbackResult{identity: identity, workspaces: workspaces, token: token}, nil
}
func bitbucketFailure(code string) string {
	switch code {
	case "access_denied", "exchange_failed", "permission_denied", "configuration_changed", "connection_changed", "scope_denied", "account_mismatch":
		return code
	case "":
		return ""
	default:
		return "exchange_failed"
	}
}
func (s Service) FinishBitbucketCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, result BitbucketCallbackResult, failure string) error {
	v, p, _, consumed, e := lockBitbucketSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return e
	}
	if v.Status != statusPending || !consumed {
		return ErrOAuthSessionState
	}
	if e = s.bitbucketRecheck(ctx, tx, org, actor, p); e != nil {
		return e
	}
	failure = bitbucketFailure(failure)
	var pending []byte
	if failure == "" {
		if _, e = bitbucket.UUID(result.identity.UUID); e != nil {
			return ErrPersonalIdentity
		}
		p.Identity = &result.identity
		p.Workspaces = result.workspaces
		if p.Purpose != PurposePersonal {
			raw, _ := json.Marshal(bitbucketPending{Token: result.token, ExpiresAt: result.token.ExpiresAt})
			pending, _, e = s.sealBitbucket(raw, oauthAAD(org, id))
			if e != nil {
				return e
			}
		}
	}
	raw, _ := json.Marshal([]bitbucketProof{p})
	if len(raw) > 60<<10 {
		return ErrInvalidRequest
	}
	status := statusSelecting
	if failure != "" {
		status = statusFailed
		raw = []byte("[]")
		pending = nil
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status=$2,error_code=$3,pending_accounts=$4::jsonb,pending_sealed=$5,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, status, failure, raw, pending)
	return e
}
func (s Service) PrepareBitbucketComplete(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in BitbucketCompleteInput) (BitbucketCompleteTarget, error) {
	var out BitbucketCompleteTarget
	v, p, pending, _, e := lockBitbucketSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return out, e
	}
	if p.Purpose == PurposePersonal {
		return out, ErrInvalidRequest
	}
	if e := checkPersonalActor(ctx, tx, org, actor); e != nil {
		return out, e
	}
	if !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	workspace, e := bitbucket.UUID(in.WorkspaceID)
	if e != nil {
		return out, ErrInvalidRequest
	}
	if v.Status == statusCompleted {
		if workspace != p.Workspace {
			return out, ErrVersionConflict
		}
		return BitbucketCompleteTarget{sessionID: id, proof: p, verified: true}, nil
	}
	if v.Status != statusSelecting || p.Identity == nil {
		return out, ErrOAuthSessionState
	}
	if !s.bitbucketProofConfigured(p) {
		return out, ErrBitbucketAppChanged
	}
	for _, w := range p.Workspaces {
		if w.UUID == workspace {
			out.workspace = w
			break
		}
	}
	if out.workspace.UUID == "" || p.Purpose == PurposeReauthorize && workspace != p.Workspace {
		return out, ErrInvalidRequest
	}
	plain, e := s.open(pending, oauthAAD(org, id))
	if e != nil {
		return out, e
	}
	var stored bitbucketPending
	if json.Unmarshal(plain, &stored) != nil || !stored.ExpiresAt.After(time.Now()) {
		return out, ErrNeedsReauth
	}
	stored.Token.ExpiresAt = stored.ExpiresAt
	out.sessionID, out.proof, out.token = id, p, stored.Token
	return out, nil
}
func (s Service) VerifyBitbucketComplete(ctx context.Context, target BitbucketCompleteTarget) (BitbucketCompleteTarget, error) {
	if target.proof.BindingID != "" {
		return target, nil
	}
	if !s.bitbucketProofConfigured(target.proof) {
		return target, ErrBitbucketAppChanged
	}
	if !target.token.ExpiresAt.After(time.Now()) || target.proof.Identity == nil {
		return target, ErrNeedsReauth
	}
	identity, e := s.Bitbucket.Identity(ctx, target.token.AccessToken)
	if e != nil {
		return target, e
	}
	if identity.UUID != target.proof.Identity.UUID {
		return target, ErrPersonalIdentity
	}
	allowed, e := s.Bitbucket.WorkspaceAdmin(ctx, target.token.AccessToken, target.workspace.UUID)
	if e != nil {
		return target, e
	}
	if !allowed {
		return target, ErrForbidden
	}
	workspace, e := s.Bitbucket.Workspace(ctx, target.token.AccessToken, target.workspace.UUID)
	if e != nil {
		return target, e
	}
	workspace.Name = asanaDisplayName(workspace.Name, workspace.UUID)
	target.workspace = workspace
	target.verified = true
	return target, nil
}
func (s Service) CompleteBitbucketOrganization(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, target BitbucketCompleteTarget) (Connection, bool, error) {
	v, p, _, _, e := lockBitbucketSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return Connection{}, false, e
	}
	if !target.verified || target.sessionID != id {
		return Connection{}, false, ErrOAuthSessionState
	}
	if v.Status == statusCompleted && p.BindingID != "" {
		if target.proof.BindingID != p.BindingID && target.workspace.UUID != p.Workspace {
			return Connection{}, false, ErrVersionConflict
		}
		if e := checkPersonalActor(ctx, tx, org, actor); e != nil {
			return Connection{}, false, e
		}
		if !CanManage(actor.Role) {
			return Connection{}, false, ErrForbidden
		}
		c, e := loadConnection(ctx, tx, p.BindingID)
		return c, false, e
	}
	if v.Status != statusSelecting || p.Purpose == PurposePersonal || p.Identity == nil || target.proof.Identity == nil || p.Identity.UUID != target.proof.Identity.UUID || p.ConsumerFingerprint != target.proof.ConsumerFingerprint || !target.token.ExpiresAt.After(time.Now()) {
		return Connection{}, false, ErrOAuthSessionState
	}
	found := false
	for _, w := range p.Workspaces {
		if w.UUID == target.workspace.UUID {
			found = true
		}
	}
	if !found {
		return Connection{}, false, ErrInvalidRequest
	}
	if e = s.bitbucketRecheck(ctx, tx, org, actor, p); e != nil {
		return Connection{}, false, e
	}
	binding := p.ParentID
	creds := connectionCredentials{}
	if binding != "" {
		if target.workspace.UUID != p.Workspace {
			return Connection{}, false, ErrVersionConflict
		}
		var locked string
		var sealed []byte
		e = tx.QueryRow(ctx, `SELECT consumer_id FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, binding).Scan(&locked)
		if e != nil {
			return Connection{}, false, e
		}
		if locked != s.Bitbucket.App.ClientID {
			return Connection{}, false, ErrBitbucketAppChanged
		}
		e = tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding).Scan(&sealed)
		if e != nil {
			return Connection{}, false, e
		}
		if len(sealed) > 0 {
			creds, e = s.openCredentials(org, binding, sealed)
			if e != nil {
				return Connection{}, false, e
			}
		}
	} else {
		binding, e = membership.NewID()
		if e != nil {
			return Connection{}, false, e
		}
	}
	creds.AccessToken, creds.RefreshToken, creds.Scope = target.token.AccessToken, target.token.RefreshToken, target.token.Scopes
	raw, _ := json.Marshal(creds)
	sealed, key, e := s.sealBitbucket(raw, connectionAAD(org, binding))
	if e != nil {
		return Connection{}, false, e
	}
	settings, _ := json.Marshal(map[string]string{"auth_method": "bitbucket_oauth", "workspace_slug": target.workspace.Slug, "delegating_user": p.Identity.DisplayName, "delegating_user_id": p.Identity.UUID})
	if p.ParentID == "" {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,settings,credentials_sealed,credentials_key_id,token_expires_at,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'bitbucket','ORGANIZATION',$2,$3,'workspace',$4::jsonb,$5,$6,$7,$8::uuid)`, binding, target.workspace.UUID, target.workspace.Name, settings, sealed, key, target.token.ExpiresAt, actor.UserID)
		if isUniqueViolation(e) {
			return Connection{}, false, ErrBitbucketAlreadyConnected
		}
		if e != nil {
			return Connection{}, false, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_runtime(organization_id,connection_id,subject_id,subject_name,consumer_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4)`, binding, p.Identity.UUID, p.Identity.DisplayName, p.AppID)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,token_expires_at=$4,settings=settings||$5::jsonb,display_name=$6,status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding, sealed, key, target.token.ExpiresAt, settings, target.workspace.Name)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET subject_id=$2::uuid,subject_name=$3,credential_generation=credential_generation+1,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,retry_at=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, binding, p.Identity.UUID, p.Identity.DisplayName)
		}
	}
	if e != nil {
		return Connection{}, false, e
	}
	p.Workspace = target.workspace.UUID
	return s.finishBitbucketBinding(ctx, tx, id, p, binding)
}
func (s Service) CompleteBitbucketPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (Connection, bool, error) {
	v, p, _, _, e := lockBitbucketSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return Connection{}, false, e
	}
	if v.Status == statusCompleted && p.Purpose == PurposePersonal && p.BindingID != "" {
		if e = checkPersonalActor(ctx, tx, org, actor); e != nil {
			return Connection{}, false, e
		}
		c, e := loadConnection(ctx, tx, p.BindingID)
		return c, false, e
	}
	if v.Status != statusSelecting || p.Purpose != PurposePersonal || p.Identity == nil {
		return Connection{}, false, ErrOAuthSessionState
	}
	if e = s.bitbucketRecheck(ctx, tx, org, actor, p); e != nil {
		return Connection{}, false, e
	}
	var binding, external string
	var version uint64
	e = tx.QueryRow(ctx, `SELECT id::text,external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='bitbucket' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, p.ParentID, actor.UserID).Scan(&binding, &external, &version)
	create := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !create {
		return Connection{}, false, e
	}
	if version != p.BindingVersion {
		return Connection{}, false, ErrVersionConflict
	}
	if !create && external != p.Identity.UUID {
		return Connection{}, false, ErrPersonalIdentity
	}
	settings, _ := json.Marshal(map[string]any{"auth_method": "bitbucket_identity", "parent_version": p.ParentVersion, "workspace_id": p.Workspace, "verified_at": time.Now().UTC()})
	if create {
		binding, e = membership.NewID()
		if e != nil {
			return Connection{}, false, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'bitbucket','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$3::uuid)`, binding, p.ParentID, actor.UserID, p.Identity.UUID, p.Identity.DisplayName, settings)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',settings=$2::jsonb,display_name=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding, settings, p.Identity.DisplayName)
	}
	if isUniqueViolation(e) {
		return Connection{}, false, ErrPersonalInUse
	}
	if e != nil {
		return Connection{}, false, e
	}
	return s.finishBitbucketBinding(ctx, tx, id, p, binding)
}
func (s Service) finishBitbucketBinding(ctx context.Context, tx pgx.Tx, id string, p bitbucketProof, binding string) (Connection, bool, error) {
	p.BindingID = binding
	raw, _ := json.Marshal([]bitbucketProof{p})
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='COMPLETED',completed_at=clock_timestamp(),pending_accounts=$2::jsonb,pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, raw)
	if e != nil {
		return Connection{}, false, e
	}
	c, e := loadConnection(ctx, tx, binding)
	return c, true, e
}
