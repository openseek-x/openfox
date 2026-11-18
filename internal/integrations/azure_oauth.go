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
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type AzureStartInput struct {
	Purpose      string `json:"purpose"`
	TenantID     string `json:"tenant_id"`
	ConnectionID string `json:"connection_id"`
	Version      uint64 `json:"version"`
}
type AzureCompleteInput struct {
	AccountID string `json:"account_id"`
}
type AzureAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type AzureIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Descriptor  string `json:"descriptor,omitempty"`
}
type AzureSession struct {
	ID               string         `json:"id"`
	Purpose          string         `json:"purpose"`
	Status           string         `json:"status"`
	ErrorCode        string         `json:"error_code"`
	ExpiresAt        time.Time      `json:"expires_at"`
	TenantID         string         `json:"tenant_id,omitempty"`
	AccountID        string         `json:"account_id,omitempty"`
	Identity         *AzureIdentity `json:"identity,omitempty"`
	Accounts         []AzureAccount `json:"accounts"`
	BindingID        string         `json:"binding_id,omitempty"`
	AffectedAccounts []AzureAccount `json:"affected_accounts"`
}
type azureProof struct {
	Purpose          string         `json:"purpose"`
	AppID            string         `json:"app_id"`
	TenantID         string         `json:"tenant_id"`
	ParentID         string         `json:"parent_id,omitempty"`
	ParentVersion    uint64         `json:"parent_version,omitempty"`
	BindingVersion   uint64         `json:"binding_version,omitempty"`
	GrantID          string         `json:"grant_id,omitempty"`
	GrantGeneration  uint64         `json:"grant_generation,omitempty"`
	AccountID        string         `json:"account_id,omitempty"`
	OrganizationName string         `json:"organization_name,omitempty"`
	SubjectID        string         `json:"subject_id,omitempty"`
	Identity         *AzureIdentity `json:"identity,omitempty"`
	Accounts         []AzureAccount `json:"accounts,omitempty"`
	BindingID        string         `json:"binding_id,omitempty"`
}
type azureSessionRow struct {
	view              AzureSession
	proof             azureProof
	verifier, pending []byte
	expired, consumed bool
}
type AzureCallbackTarget struct {
	proof    azureProof
	verifier string
}
type AzureCallbackResult struct {
	proof    azureProof
	identity AzureIdentity
	accounts []AzureAccount
	token    azuredevops.Token
}
type AzureCompleteTarget struct {
	proof    azureProof
	account  AzureAccount
	accounts []AzureAccount
	token    azuredevops.Token
	identity azuredevops.Identity
	verified bool
}

func (s Service) azureOAuthClient(personal bool) *azuredevops.Client {
	if personal {
		return s.AzureIdentity
	}
	return s.Azure
}
func (s Service) azureRedirect(personal bool) string {
	path := "/api/v1/integrations/azure_devops/callback"
	if personal {
		path = "/api/v1/integrations/azure_devops/identity/callback"
	}
	return strings.TrimRight(s.PublicURL, "/") + path
}

// Entry checks are deliberately non-locking. Mutating operations call the
// locking check only after grant -> parent -> claim, never before those locks.
func azureActor(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, personal, lock bool) error {
	var err error
	if lock {
		err = checkPersonalActor(ctx, tx, org, actor)
	} else {
		err = (collaboration.Service{}).CheckActor(ctx, tx, org, actor)
	}
	if err != nil {
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest || !personal && !CanManage(actor.Role) {
		return ErrForbidden
	}
	return nil
}
func (s Service) azureOAuthParent(ctx context.Context, tx pgx.Tx, id string, active bool) (AzureAccess, error) {
	var a AzureAccess
	if !membership.ValidateUUID(id) {
		return a, ErrInvalidRequest
	}
	err := tx.QueryRow(ctx, `SELECT azure_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='azure_devops' AND scope='ORGANIZATION' AND status<>'DISCONNECTED'`, id).Scan(&a.GrantID)
	if err != nil {
		return a, azureMissing(err)
	}
	var app, grantStatus, parentStatus string
	err = tx.QueryRow(ctx, `SELECT app_client_id::text,resource_tenant_id::text,subject_id::text,generation,status FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, a.GrantID).Scan(&app, &a.TenantID, &a.SubjectID, &a.GrantGeneration, &grantStatus)
	if err != nil {
		return a, azureMissing(err)
	}
	if s.Azure == nil || !strings.EqualFold(app, s.Azure.App.ClientID) {
		return a, ErrAzureAppChanged
	}
	err = tx.QueryRow(ctx, `SELECT external_id,settings->>'organization_name',version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND azure_grant_id=$2::uuid AND provider='azure_devops' AND scope='ORGANIZATION' AND status<>'DISCONNECTED' FOR SHARE`, id, a.GrantID).Scan(&a.AccountID, &a.OrganizationName, &a.ConnectionVersion, &parentStatus)
	if err != nil {
		return a, azureMissing(err)
	}
	if (active && (parentStatus != StatusActive || grantStatus != StatusActive)) || grantStatus == StatusDisconnected || grantStatus == "CLEANUP" {
		return a, ErrNeedsReauth
	}
	if !azuredevops.ValidUUID(a.AccountID) || !azuredevops.ValidOrganization(a.OrganizationName) || !azuredevops.ValidUUID(a.TenantID) {
		return a, ErrInvalidRequest
	}
	a.ConnectionID = id
	return a, nil
}
func azureParentMatches(p azureProof, a AzureAccess) bool {
	return p.ParentID == a.ConnectionID && p.ParentVersion == a.ConnectionVersion && p.GrantID == a.GrantID && p.GrantGeneration == a.GrantGeneration && p.TenantID == a.TenantID && p.AccountID == a.AccountID && p.OrganizationName == a.OrganizationName
}
func (s Service) StartAzure(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in AzureStartInput) (StartResult, error) {
	personal := in.Purpose == PurposePersonal
	if !personal && in.Purpose != PurposeOrganization && in.Purpose != PurposeReauthorize {
		return StartResult{}, ErrInvalidRequest
	}
	if err := azureActor(ctx, tx, org, actor, personal, false); err != nil {
		return StartResult{}, err
	}
	if !s.azureConfigured(personal) {
		return StartResult{}, ErrNotConfigured
	}
	proof := azureProof{Purpose: in.Purpose, AppID: strings.ToLower(s.azureOAuthClient(personal).App.ClientID), ParentID: in.ConnectionID}
	if personal || in.Purpose == PurposeReauthorize {
		a, err := s.azureOAuthParent(ctx, tx, in.ConnectionID, personal)
		if err != nil {
			return StartResult{}, err
		}
		if in.TenantID != "" && !strings.EqualFold(in.TenantID, a.TenantID) {
			return StartResult{}, ErrInvalidRequest
		}
		proof.TenantID, proof.AccountID, proof.OrganizationName = a.TenantID, a.AccountID, a.OrganizationName
		proof.ParentVersion, proof.GrantGeneration, proof.GrantID = a.ConnectionVersion, a.GrantGeneration, a.GrantID
		if personal {
			err = tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED'`, in.ConnectionID, actor.UserID).Scan(&proof.BindingVersion)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return StartResult{}, err
			}
			if in.Version != proof.BindingVersion {
				return StartResult{}, ErrVersionConflict
			}
		} else if in.Version != a.ConnectionVersion {
			return StartResult{}, ErrVersionConflict
		}
	} else {
		if !azuredevops.ValidUUID(in.TenantID) || in.ConnectionID != "" || in.Version != 0 {
			return StartResult{}, ErrInvalidRequest
		}
		proof.TenantID = strings.ToLower(in.TenantID)
	}
	if err := azureActor(ctx, tx, org, actor, personal, true); err != nil {
		return StartResult{}, err
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
	authorize, err := s.azureOAuthClient(personal).AuthorizeURL(proof.TenantID, state, base64.RawURLEncoding.EncodeToString(sum[:]), s.azureRedirect(personal), personal)
	if err != nil {
		return StartResult{}, err
	}
	sealed, _, err := s.sealAzure([]byte(verifier), oauthAAD(org, id))
	if err != nil {
		return StartResult{}, err
	}
	raw, err := azureProofJSON(proof)
	if err != nil {
		return StartResult{}, err
	}
	var parent *string
	if proof.ParentID != "" {
		parent = &proof.ParentID
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO openfox.integration_oauth_states(organization_id,id,provider,purpose,user_id,state_hash,code_verifier_sealed,connection_id,pending_accounts) VALUES(openfox_private.current_organization_id(),$1::uuid,'azure_devops',$2,$3::uuid,$4,$5,$6::uuid,$7::jsonb) RETURNING expires_at`, id, strings.ToUpper(in.Purpose), actor.UserID, TokenHash(state), sealed, parent, raw).Scan(&expires)
	return StartResult{SessionID: id, AuthorizeURL: authorize, ExpiresAt: expires}, err
}
func azureProofJSON(p azureProof) ([]byte, error) {
	b, e := json.Marshal([]azureProof{p})
	if e != nil || len(b) > 60<<10 {
		return nil, ErrInvalidRequest
	}
	return b, nil
}
func readAzureSession(ctx context.Context, tx pgx.Tx, id, user string, lock bool) (azureSessionRow, error) {
	var row azureSessionRow
	if !membership.ValidateUUID(id) || !membership.ValidateUUID(user) {
		return row, ErrNotFound
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var raw []byte
	var purpose string
	err := tx.QueryRow(ctx, `SELECT id::text,status,error_code,expires_at,expires_at<=clock_timestamp(),consumed_at IS NOT NULL,pending_accounts,code_verifier_sealed,pending_sealed,lower(purpose) FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND user_id=$2::uuid AND provider='azure_devops'`+suffix, id, user).Scan(&row.view.ID, &row.view.Status, &row.view.ErrorCode, &row.view.ExpiresAt, &row.expired, &row.consumed, &raw, &row.verifier, &row.pending, &purpose)
	if err != nil {
		return row, azureMissing(err)
	}
	var p []azureProof
	if json.Unmarshal(raw, &p) != nil || len(p) > 1 {
		return row, ErrOAuthSessionState
	}
	if len(p) == 1 {
		row.proof = p[0]
	}
	row.proof.Purpose = purpose
	row.view.Purpose = purpose
	row.view.TenantID = row.proof.TenantID
	row.view.AccountID = row.proof.AccountID
	row.view.Identity = row.proof.Identity
	row.view.BindingID = row.proof.BindingID
	row.view.Accounts = append([]AzureAccount{}, row.proof.Accounts...)
	row.view.AffectedAccounts = []AzureAccount{}
	return row, nil
}
func expireAzureSession(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='EXPIRED',error_code='session_expired',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status IN('PENDING','SELECTING')`, id)
	return err
}
func (s Service) GetAzureSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AzureSession, error) {
	if err := azureActor(ctx, tx, org, actor, true, false); err != nil {
		return AzureSession{}, err
	}
	row, err := readAzureSession(ctx, tx, id, actor.UserID, true)
	if err != nil {
		return AzureSession{}, err
	}
	if row.proof.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return AzureSession{}, ErrForbidden
	}
	if row.expired && (row.view.Status == statusPending || row.view.Status == statusSelecting) {
		if err = expireAzureSession(ctx, tx, id); err != nil {
			return AzureSession{}, err
		}
		return AzureSession{ID: id, Purpose: row.proof.Purpose, Status: statusExpired, ErrorCode: "session_expired", ExpiresAt: row.view.ExpiresAt, Accounts: []AzureAccount{}, AffectedAccounts: []AzureAccount{}}, nil
	}
	if row.proof.Purpose != PurposePersonal && row.proof.SubjectID != "" {
		rows, e := tx.Query(ctx, `SELECT c.external_id,c.display_name FROM openfox.integration_connections c JOIN openfox.integration_azure_grants g ON g.organization_id=c.organization_id AND g.id=c.azure_grant_id WHERE c.organization_id=openfox_private.current_organization_id() AND g.app_client_id=$1::uuid AND g.resource_tenant_id=$2::uuid AND g.subject_id=$3::uuid AND c.status<>'DISCONNECTED' ORDER BY c.id LIMIT 501`, row.proof.AppID, row.proof.TenantID, row.proof.SubjectID)
		if e != nil {
			return AzureSession{}, e
		}
		for rows.Next() {
			var a AzureAccount
			if e = rows.Scan(&a.ID, &a.Name); e != nil {
				rows.Close()
				return AzureSession{}, e
			}
			row.view.AffectedAccounts = append(row.view.AffectedAccounts, a)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return AzureSession{}, e
		}
		if len(row.view.AffectedAccounts) > 500 {
			return AzureSession{}, ErrInvalidRequest
		}
	}
	return row.view, nil
}
func (s Service) ConsumeAzureCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, personal bool) (AzureCallbackTarget, error) {
	var out AzureCallbackTarget
	if err := azureActor(ctx, tx, org, actor, personal, false); err != nil {
		return out, err
	}
	row, err := readAzureSession(ctx, tx, id, actor.UserID, false)
	if err != nil {
		return out, err
	}
	p := row.proof
	if row.expired || row.view.Status != statusPending || row.consumed || personal != (p.Purpose == PurposePersonal) {
		return out, ErrOAuthSessionState
	}
	if !s.azureConfigured(personal) || p.AppID != strings.ToLower(s.azureOAuthClient(personal).App.ClientID) {
		return out, ErrAzureAppChanged
	}
	if p.ParentID != "" {
		a, e := s.azureOAuthParent(ctx, tx, p.ParentID, personal)
		if e != nil {
			return out, e
		}
		if !azureParentMatches(p, a) {
			return out, ErrVersionConflict
		}
	}
	if err = azureActor(ctx, tx, org, actor, personal, true); err != nil {
		return out, err
	}
	locked, err := readAzureSession(ctx, tx, id, actor.UserID, true)
	if err != nil {
		return out, err
	}
	if locked.expired || locked.view.Status != statusPending || locked.consumed {
		return out, ErrOAuthSessionState
	}
	plain, err := s.open(locked.verifier, oauthAAD(org, id))
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET consumed_at=clock_timestamp(),code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return AzureCallbackTarget{proof: p, verifier: string(plain)}, err
}
func (s Service) VerifyAzureCallback(ctx context.Context, runtime AzureRuntime, org string, target AzureCallbackTarget, code string) (AzureCallbackResult, error) {
	var out AzureCallbackResult
	p := target.proof
	personal := p.Purpose == PurposePersonal
	if !s.azureConfigured(personal) || p.AppID != strings.ToLower(s.azureOAuthClient(personal).App.ClientID) || !azuredevops.ValidUUID(p.TenantID) {
		return out, ErrAzureAppChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client, _ := azureSequence(s.azureOAuthClient(personal))
	token, err := client.Exchange(ctx, p.TenantID, code, target.verifier, s.azureRedirect(personal), personal)
	if err != nil {
		return out, err
	}
	if !azureScopeAllowed(token.Scope, personal) {
		return out, ErrPersonalCapability
	}
	profile, err := client.Profile(ctx, token.AccessToken)
	if err != nil {
		return out, err
	}
	accounts, err := client.Accounts(ctx, token.AccessToken, profile.ID)
	if err != nil {
		return out, err
	}
	if len(accounts) > 200 {
		return out, ErrInvalidRequest
	}
	identity := AzureIdentity{ID: profile.ID, DisplayName: azuredevops.DisplayName(profile.DisplayName, profile.ID)}
	if len(profile.Email) <= 320 {
		identity.Email = profile.Email
	}
	out = AzureCallbackResult{proof: p, identity: identity, accounts: []AzureAccount{}}
	for _, a := range accounts {
		out.accounts = append(out.accounts, AzureAccount{ID: a.ID, Name: a.Name})
	}
	if personal {
		found := false
		for _, a := range accounts {
			if a.ID == p.AccountID && a.Name == p.OrganizationName {
				found = true
			}
		}
		if !found {
			return AzureCallbackResult{}, ErrPersonalIdentity
		}
		var confirmed azuredevops.Identity
		_, err = runtime.Do(ctx, org, p.ParentID, p.ParentVersion, p.GrantGeneration, func(ctx context.Context, c *azuredevops.Client, access string) error {
			var e error
			confirmed, e = c.Identity(ctx, access, p.OrganizationName, profile.ID)
			return e
		})
		if err != nil {
			return AzureCallbackResult{}, err
		}
		if confirmed.ID != profile.ID || !confirmed.IsActive || confirmed.IsContainer {
			return AzureCallbackResult{}, ErrPersonalIdentity
		}
		out.identity.Descriptor = confirmed.Descriptor
		out.accounts = nil
		return out, nil // No personal token survives this function.
	}
	out.token = token
	return out, nil
}

// AzureFailure only emits stable safe codes, never OAuth/provider response text.
func AzureFailure(err error) string {
	var upstream *azuredevops.Error
	var retry *AzureRetry
	switch {
	case errors.Is(err, ErrAzureAppChanged), errors.Is(err, ErrNotConfigured):
		return "configuration_changed"
	case errors.Is(err, ErrPersonalCapability):
		return "scope_not_allowed"
	case errors.Is(err, ErrForbidden), errors.Is(err, membership.ErrAuthorizationStale):
		return "permission_denied"
	case errors.As(err, &retry):
		return "rate_limited"
	case errors.As(err, &upstream):
		if upstream.Code == "rate_limited" || upstream.HTTPStatus == 429 {
			return "rate_limited"
		}
		if upstream.HTTPStatus == 403 {
			return "permission_denied"
		}
		if upstream.HTTPStatus == 401 || upstream.Code == "invalid_grant" {
			return "authentication_failed"
		}
		return "exchange_failed"
	default:
		return PersonalFailure(err)
	}
}
func (s Service) FinishAzureCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, result AzureCallbackResult, failure string) error {
	row, err := readAzureSession(ctx, tx, id, actor.UserID, false)
	if err != nil {
		return err
	}
	p := row.proof
	personal := p.Purpose == PurposePersonal
	if row.view.Status != statusPending || !row.consumed {
		return ErrOAuthSessionState
	}
	if p.ParentID != "" {
		a, e := s.azureOAuthParent(ctx, tx, p.ParentID, personal)
		if e != nil || !azureParentMatches(p, a) {
			failure = "connection_changed"
		}
	}
	if e := azureActor(ctx, tx, org, actor, personal, true); e != nil {
		failure = "permission_denied"
	}
	if !s.azureConfigured(personal) || p.AppID != strings.ToLower(s.azureOAuthClient(personal).App.ClientID) {
		failure = "configuration_changed"
	}
	row, err = readAzureSession(ctx, tx, id, actor.UserID, true)
	if err != nil {
		return err
	}
	if row.view.Status != statusPending || !row.consumed {
		return ErrOAuthSessionState
	}
	if row.expired {
		return expireAzureSession(ctx, tx, id)
	}
	var pending []byte
	if failure == "" {
		if !azuredevops.ValidUUID(result.identity.ID) || !azureSameSession(result.proof, p) || personal && !azuredevops.ValidText(result.identity.Descriptor, 2048) {
			failure = "account_mismatch"
		} else {
			p.Identity = &result.identity
			p.SubjectID = result.identity.ID
			p.Accounts = result.accounts
			if !personal {
				if !azureScopeAllowed(result.token.Scope, false) || !azuredevops.ValidToken(result.token.RefreshToken) || !result.token.ExpiresAt.After(time.Now()) {
					failure = "scope_not_allowed"
				} else {
					plain, _ := json.Marshal(result.token)
					pending, _, err = s.sealAzure(plain, oauthAAD(org, id))
					if err != nil {
						return err
					}
					if len(pending) > 131072 {
						return ErrInvalidRequest
					}
				}
			}
		}
	}
	status := statusSelecting
	if failure != "" {
		status = statusFailed
		p.Identity = nil
		p.Accounts = nil
		p.SubjectID = ""
		pending = nil
		switch failure {
		case "connection_changed", "permission_denied", "configuration_changed", "account_mismatch", "scope_not_allowed", "rate_limited", "authentication_failed", "access_denied", "invalid_state", "exchange_failed":
		default:
			failure = "exchange_failed"
		}
	}
	raw, err := azureProofJSON(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status=$2,error_code=$3,pending_accounts=$4::jsonb,pending_sealed=$5,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, status, failure, raw, pending)
	return err
}

func azureSelected(p azureProof, id string) (AzureAccount, bool) {
	for _, a := range p.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return AzureAccount{}, false
}
func azureCompleteAccount(target AzureCompleteTarget) string {
	if target.account.ID != "" {
		return target.account.ID
	}
	return target.proof.AccountID
}
func (s Service) PrepareAzureComplete(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in AzureCompleteInput) (AzureCompleteTarget, error) {
	var target AzureCompleteTarget
	if err := azureActor(ctx, tx, org, actor, false, false); err != nil {
		return target, err
	}
	row, err := readAzureSession(ctx, tx, id, actor.UserID, false)
	if err != nil {
		return target, err
	}
	p := row.proof
	if p.Purpose == PurposePersonal {
		return target, ErrInvalidRequest
	}
	if !azuredevops.ValidUUID(in.AccountID) {
		return target, ErrInvalidRequest
	}
	selected := strings.ToLower(in.AccountID)
	if row.view.Status == statusCompleted {
		if selected != p.AccountID {
			return target, ErrVersionConflict
		}
		target.proof = p
		return target, nil
	}
	if row.expired || row.view.Status != statusSelecting || p.Identity == nil {
		return target, ErrOAuthSessionState
	}
	if !s.azureConfigured(false) || p.AppID != strings.ToLower(s.Azure.App.ClientID) {
		return target, ErrAzureAppChanged
	}
	account, ok := azureSelected(p, selected)
	if !ok || p.Purpose == PurposeReauthorize && p.AccountID != selected {
		return target, ErrInvalidRequest
	}
	if p.ParentID != "" {
		a, e := s.azureOAuthParent(ctx, tx, p.ParentID, false)
		if e != nil {
			return target, e
		}
		if !azureParentMatches(p, a) {
			return target, ErrVersionConflict
		}
	}
	plain, err := s.open(row.pending, oauthAAD(org, id))
	if err != nil {
		return target, err
	}
	if json.Unmarshal(plain, &target.token) != nil || !target.token.ExpiresAt.After(time.Now()) || !azuredevops.ValidToken(target.token.AccessToken) || !azuredevops.ValidToken(target.token.RefreshToken) {
		return AzureCompleteTarget{}, ErrNeedsReauth
	}
	if !azureScopeAllowed(target.token.Scope, false) {
		return AzureCompleteTarget{}, ErrPersonalCapability
	}
	target.proof = p
	target.account = account
	return target, nil
}
func (s Service) VerifyAzureComplete(ctx context.Context, target AzureCompleteTarget) (AzureCompleteTarget, error) {
	if target.proof.BindingID != "" {
		return target, nil
	}
	if !s.azureConfigured(false) || target.proof.AppID != strings.ToLower(s.Azure.App.ClientID) {
		return AzureCompleteTarget{}, ErrAzureAppChanged
	}
	if !azuredevops.ValidUUID(target.proof.TenantID) || !azuredevops.ValidUUID(target.proof.SubjectID) {
		return AzureCompleteTarget{}, ErrInvalidRequest
	}
	if !target.token.ExpiresAt.After(time.Now()) || !azuredevops.ValidToken(target.token.AccessToken) {
		return AzureCompleteTarget{}, ErrNeedsReauth
	}
	if !azureScopeAllowed(target.token.Scope, false) {
		return AzureCompleteTarget{}, ErrPersonalCapability
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client, _ := azureSequence(s.Azure)
	accounts, err := client.Accounts(ctx, target.token.AccessToken, target.proof.SubjectID)
	if err != nil {
		return AzureCompleteTarget{}, err
	}
	if len(accounts) > 200 {
		return AzureCompleteTarget{}, ErrInvalidRequest
	}
	found := false
	target.accounts = []AzureAccount{}
	for _, a := range accounts {
		target.accounts = append(target.accounts, AzureAccount{ID: a.ID, Name: a.Name})
		if a.ID == target.account.ID && a.Name == target.account.Name {
			found = true
		}
	}
	if !found {
		return AzureCompleteTarget{}, ErrPersonalIdentity
	}
	target.identity, err = client.Identity(ctx, target.token.AccessToken, target.account.Name, target.proof.SubjectID)
	if err != nil {
		return AzureCompleteTarget{}, err
	}
	if target.identity.ID != target.proof.SubjectID || !target.identity.IsActive || target.identity.IsContainer {
		return AzureCompleteTarget{}, ErrPersonalIdentity
	}
	if _, err = client.Projects(ctx, target.token.AccessToken, target.account.Name, ""); err != nil {
		return AzureCompleteTarget{}, err
	}
	target.verified = true
	return target, nil
}
func azureSameSession(p, target azureProof) bool {
	return p.AppID == target.AppID && p.TenantID == target.TenantID && p.SubjectID == target.SubjectID && p.ParentID == target.ParentID && p.ParentVersion == target.ParentVersion && p.BindingVersion == target.BindingVersion && p.GrantID == target.GrantID && p.GrantGeneration == target.GrantGeneration && p.Purpose == target.Purpose && p.AccountID == target.AccountID && p.OrganizationName == target.OrganizationName
}
func azureLockActorUser(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, personal bool) error {
	if err := azureActor(ctx, tx, org, actor, personal, true); err != nil {
		return err
	}
	var user string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.users WHERE id=$1::uuid FOR SHARE`, actor.UserID).Scan(&user); err != nil {
		return azureMissing(err)
	}
	return nil
}
func (s Service) CompleteAzureOrganization(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, target AzureCompleteTarget) (Connection, bool, error) {
	if err := azureActor(ctx, tx, org, actor, false, false); err != nil {
		return Connection{}, false, err
	}
	row, err := readAzureSession(ctx, tx, id, actor.UserID, false)
	if err != nil {
		return Connection{}, false, err
	}
	p := row.proof
	if row.view.Status == statusCompleted {
		if p.AccountID != azureCompleteAccount(target) || p.Purpose == PurposePersonal {
			return Connection{}, false, ErrVersionConflict
		}
		if err = azureLockActorUser(ctx, tx, org, actor, false); err != nil {
			return Connection{}, false, err
		}
		item, e := loadConnection(ctx, tx, p.BindingID)
		return item, false, e
	}
	if row.expired || row.view.Status != statusSelecting || p.Purpose == PurposePersonal || !azureSameSession(p, target.proof) || !target.verified || target.identity.ID != p.SubjectID || p.Identity == nil {
		return Connection{}, false, ErrOAuthSessionState
	}
	if _, ok := azureSelected(p, target.account.ID); !ok || p.Purpose == PurposeReauthorize && p.AccountID != target.account.ID {
		return Connection{}, false, ErrInvalidRequest
	}
	if !s.azureConfigured(false) || p.AppID != strings.ToLower(s.Azure.App.ClientID) {
		return Connection{}, false, ErrAzureAppChanged
	}
	if !target.token.ExpiresAt.After(time.Now()) || !azureScopeAllowed(target.token.Scope, false) || !azuredevops.ValidToken(target.token.RefreshToken) {
		return Connection{}, false, ErrNeedsReauth
	}
	grant, err := membership.NewID()
	if err != nil {
		return Connection{}, false, err
	}
	// The unique grant key serializes all sessions for this app/tenant/subject.
	// FK checks take compatible key-share locks; explicit business locks follow
	// grant -> ordered parents -> membership/user -> session/new child.
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_azure_grants(organization_id,id,app_client_id,resource_tenant_id,subject_id,subject_name,scope,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7::uuid) ON CONFLICT(organization_id,app_client_id,resource_tenant_id,subject_id) DO NOTHING`, grant, p.AppID, p.TenantID, p.SubjectID, p.Identity.DisplayName, target.token.Scope, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	var generation uint64
	err = tx.QueryRow(ctx, `SELECT id::text,generation FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND app_client_id=$1::uuid AND resource_tenant_id=$2::uuid AND subject_id=$3::uuid FOR UPDATE`, p.AppID, p.TenantID, p.SubjectID).Scan(&grant, &generation)
	if err != nil {
		return Connection{}, false, err
	}
	// A simultaneous completion may have finished while we waited for this
	// grant. Check its result before rejecting the old reauthorization version.
	latest, err := readAzureSession(ctx, tx, id, actor.UserID, false)
	if err != nil {
		return Connection{}, false, err
	}
	if latest.view.Status == statusCompleted {
		if latest.proof.AccountID != target.account.ID {
			return Connection{}, false, ErrVersionConflict
		}
		if err = azureLockActorUser(ctx, tx, org, actor, false); err != nil {
			return Connection{}, false, err
		}
		item, e := loadConnection(ctx, tx, latest.proof.BindingID)
		return item, false, e
	}
	rows, err := tx.Query(ctx, `SELECT id::text,external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND azure_grant_id=$1::uuid AND status<>'DISCONNECTED' ORDER BY id LIMIT 501 FOR UPDATE`, grant)
	if err != nil {
		return Connection{}, false, err
	}
	parentFound := false
	count := 0
	for rows.Next() {
		var parent, account string
		var version uint64
		if err = rows.Scan(&parent, &account, &version); err != nil {
			rows.Close()
			return Connection{}, false, err
		}
		count++
		if parent == p.ParentID {
			if p.GrantID != grant || p.GrantGeneration != generation || p.ParentVersion != version || p.AccountID != account || target.account.ID != account {
				rows.Close()
				return Connection{}, false, ErrVersionConflict
			}
			parentFound = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Connection{}, false, err
	}
	if count > 500 {
		return Connection{}, false, ErrInvalidRequest
	}
	if p.ParentID != "" && !parentFound {
		return Connection{}, false, ErrVersionConflict
	}
	// Installed hooks keep their original remote owner generation and secret
	// for reconciliation; reauthorization cannot leave a stale green status.
	// UNKNOWN operation states/reasons remain intact (never imply not sent).
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks h SET state='NEEDS_ATTENTION',last_error='connection_changed',updated_at=clock_timestamp() WHERE h.organization_id=openfox_private.current_organization_id() AND h.desired AND h.state='ACTIVE' AND h.connection_id IN(SELECT id FROM openfox.integration_connections WHERE organization_id=h.organization_id AND azure_grant_id=$1::uuid AND status<>'DISCONNECTED')`, grant)
	if err != nil {
		return Connection{}, false, err
	}
	if err = azureLockActorUser(ctx, tx, org, actor, false); err != nil {
		return Connection{}, false, err
	}
	locked, err := readAzureSession(ctx, tx, id, actor.UserID, true)
	if err != nil {
		return Connection{}, false, err
	}
	if locked.view.Status == statusCompleted {
		if locked.proof.AccountID != target.account.ID {
			return Connection{}, false, ErrVersionConflict
		}
		item, e := loadConnection(ctx, tx, locked.proof.BindingID)
		return item, false, e
	}
	if locked.expired || locked.view.Status != statusSelecting || !azureSameSession(locked.proof, p) {
		return Connection{}, false, ErrOAuthSessionState
	}
	binding := p.ParentID
	if binding == "" {
		var existing string
		err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' AND scope='ORGANIZATION' AND external_id=$1 AND status<>'DISCONNECTED'`, target.account.ID).Scan(&existing)
		if err == nil {
			return Connection{}, false, ErrAzureAlreadyConnected
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, false, err
		}
		binding, err = membership.NewID()
		if err != nil {
			return Connection{}, false, err
		}
	}
	plain, _ := json.Marshal(azureCredentials{AccessToken: target.token.AccessToken, RefreshToken: target.token.RefreshToken})
	sealed, key, err := s.sealAzure(plain, azureGrantAAD(org, grant))
	if err != nil {
		return Connection{}, false, err
	}
	if len(sealed) > 131072 {
		return Connection{}, false, ErrInvalidRequest
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET credentials_sealed=$2,credentials_key_id=$3,expires_at=$4,scope=$5,subject_name=$6,status='ACTIVE',generation=generation+1,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,retry_at=NULL,cleanup_until=NULL,last_error='',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, grant, sealed, key, target.token.ExpiresAt, target.token.Scope, p.Identity.DisplayName)
	if err != nil {
		return Connection{}, false, err
	}
	// Every referenced account is fenced, including accounts no longer returned
	// by this user's fresh membership list. Only proved accounts reactivate.
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='account_access_unverified',settings=settings||jsonb_build_object('delegating_user',$2::text,'delegating_user_id',$3::text),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND azure_grant_id=$1::uuid AND status<>'DISCONNECTED'`, grant, p.Identity.DisplayName, p.SubjectID)
	if err != nil {
		return Connection{}, false, err
	}
	accountsRaw, _ := json.Marshal(target.accounts)
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections c SET status='ACTIVE',status_reason='',display_name=a.name,settings=jsonb_set(c.settings,'{organization_name}',to_jsonb(a.name)) FROM jsonb_to_recordset($2::jsonb) AS a(id text,name text) WHERE c.organization_id=openfox_private.current_organization_id() AND c.azure_grant_id=$1::uuid AND c.status<>'DISCONNECTED' AND c.external_id=a.id`, grant, accountsRaw)
	if err != nil {
		return Connection{}, false, err
	}
	if p.ParentID == "" {
		settings, _ := json.Marshal(map[string]string{"auth_method": "azure_devops_oauth", "organization_name": target.account.Name, "tenant_id": p.TenantID, "delegating_user": p.Identity.DisplayName, "delegating_user_id": p.SubjectID})
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,settings,azure_grant_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'azure_devops','ORGANIZATION',$2,$3,'organization',$4::jsonb,$5::uuid,$6::uuid)`, binding, target.account.ID, target.account.Name, settings, grant, actor.UserID)
		if isUniqueViolation(err) {
			return Connection{}, false, ErrAzureAlreadyConnected
		}
		if err != nil {
			return Connection{}, false, err
		}
	}
	p.AccountID = target.account.ID
	p.OrganizationName = target.account.Name
	p.GrantID = grant
	p.GrantGeneration = generation + 1
	return s.finishAzureBinding(ctx, tx, id, p, binding)
}
func (s Service) CompleteAzurePersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (Connection, bool, error) {
	if err := azureActor(ctx, tx, org, actor, true, false); err != nil {
		return Connection{}, false, err
	}
	row, err := readAzureSession(ctx, tx, id, actor.UserID, false)
	if err != nil {
		return Connection{}, false, err
	}
	p := row.proof
	if p.Purpose != PurposePersonal {
		return Connection{}, false, ErrInvalidRequest
	}
	if row.view.Status == statusCompleted {
		if err = azureLockActorUser(ctx, tx, org, actor, true); err != nil {
			return Connection{}, false, err
		}
		item, e := loadConnection(ctx, tx, p.BindingID)
		return item, false, e
	}
	if row.expired || row.view.Status != statusSelecting || p.Identity == nil || !azuredevops.ValidUUID(p.SubjectID) || p.SubjectID != p.Identity.ID || !azuredevops.ValidText(p.Identity.Descriptor, 2048) {
		return Connection{}, false, ErrOAuthSessionState
	}
	if !s.azureConfigured(true) || p.AppID != strings.ToLower(s.AzureIdentity.App.ClientID) {
		return Connection{}, false, ErrAzureAppChanged
	}
	access, err := s.azureOAuthParent(ctx, tx, p.ParentID, true)
	if err != nil {
		return Connection{}, false, err
	}
	if !azureParentMatches(p, access) {
		return Connection{}, false, ErrVersionConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_azure_identity_claims(organization_id,account_id,subject_id,owner_user_id) VALUES(openfox_private.current_organization_id(),$1,$2,$3::uuid) ON CONFLICT(organization_id,account_id,subject_id) DO NOTHING`, p.AccountID, p.SubjectID, actor.UserID)
	if err != nil {
		return Connection{}, false, err
	}
	var owner string
	err = tx.QueryRow(ctx, `SELECT owner_user_id::text FROM openfox.integration_azure_identity_claims WHERE organization_id=openfox_private.current_organization_id() AND account_id=$1 AND subject_id=$2 FOR UPDATE`, p.AccountID, p.SubjectID).Scan(&owner)
	if err != nil {
		return Connection{}, false, err
	}
	if owner != actor.UserID {
		var live bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND azure_identity_account=$1 AND azure_identity_subject=$2)`, p.AccountID, p.SubjectID).Scan(&live)
		if err != nil {
			return Connection{}, false, err
		}
		if live {
			return Connection{}, false, ErrPersonalInUse
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_identity_claims SET owner_user_id=$3::uuid WHERE organization_id=openfox_private.current_organization_id() AND account_id=$1 AND subject_id=$2`, p.AccountID, p.SubjectID, actor.UserID)
		if err != nil {
			return Connection{}, false, err
		}
	}
	if err = azureLockActorUser(ctx, tx, org, actor, true); err != nil {
		return Connection{}, false, err
	}
	locked, err := readAzureSession(ctx, tx, id, actor.UserID, true)
	if err != nil {
		return Connection{}, false, err
	}
	if locked.view.Status == statusCompleted {
		item, e := loadConnection(ctx, tx, locked.proof.BindingID)
		return item, false, e
	}
	if locked.expired || locked.view.Status != statusSelecting || !azureSameSession(p, locked.proof) {
		return Connection{}, false, ErrOAuthSessionState
	}
	var binding, external string
	var current uint64
	err = tx.QueryRow(ctx, `SELECT id::text,external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, p.ParentID, actor.UserID).Scan(&binding, &external, &current)
	create := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !create {
		return Connection{}, false, err
	}
	if current != p.BindingVersion {
		return Connection{}, false, ErrVersionConflict
	}
	if !create && external != p.SubjectID {
		return Connection{}, false, ErrPersonalIdentity
	}
	settings, _ := json.Marshal(map[string]any{"auth_method": "azure_devops_identity", "account_id": p.AccountID, "parent_version": p.ParentVersion, "descriptor": p.Identity.Descriptor})
	if create {
		binding, err = membership.NewID()
		if err != nil {
			return Connection{}, false, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'azure_devops','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$3::uuid)`, binding, p.ParentID, actor.UserID, p.SubjectID, p.Identity.DisplayName, settings)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',display_name=$2,settings=$3::jsonb,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding, p.Identity.DisplayName, settings)
	}
	if isUniqueViolation(err) {
		return Connection{}, false, ErrPersonalInUse
	}
	if err != nil {
		return Connection{}, false, err
	}
	return s.finishAzureBinding(ctx, tx, id, p, binding)
}
func (s Service) finishAzureBinding(ctx context.Context, tx pgx.Tx, id string, p azureProof, binding string) (Connection, bool, error) {
	p.BindingID = binding
	p.Identity = nil
	p.Accounts = nil
	raw, err := azureProofJSON(p)
	if err != nil {
		return Connection{}, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='COMPLETED',error_code='',completed_at=clock_timestamp(),pending_accounts=$2::jsonb,pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, raw)
	if err != nil {
		return Connection{}, false, err
	}
	item, err := loadConnection(ctx, tx, binding)
	return item, true, err
}
