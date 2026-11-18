package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/zohobugtracker"
	"openfox/internal/membership"
	"openfox/internal/secrets"
)

const ProviderZoho = "zoho_bugtracker"

var ErrZohoAppChanged = errors.New("integrations: Zoho application changed")
var ErrZohoGrantInUse = errors.New("integrations: Zoho authorization already used by another connection")
var ErrZohoAlreadyConnected = errors.New("integrations: Zoho portal already connected")
var ErrZohoLimit = errors.New("integrations: Zoho verification exceeds bounded directory limit")

type ZohoStartInput struct {
	Purpose      string `json:"purpose"`
	ConnectionID string `json:"connection_id"`
	Version      uint64 `json:"version"`
}
type ZohoCompleteInput struct {
	PortalID string `json:"portal_id"`
}
type zohoProof struct {
	Purpose        string                  `json:"purpose"`
	ParentID       string                  `json:"parent_id,omitempty"`
	ParentVersion  uint64                  `json:"parent_version,omitempty"`
	BindingVersion uint64                  `json:"binding_version,omitempty"`
	PortalID       string                  `json:"portal_id,omitempty"`
	Email          string                  `json:"email,omitempty"`
	AppID          string                  `json:"app_id"`
	AppFingerprint string                  `json:"app_fingerprint"`
	Identity       *zohobugtracker.User    `json:"identity,omitempty"`
	Portals        []zohobugtracker.Portal `json:"portals,omitempty"`
	BindingID      string                  `json:"binding_id,omitempty"`
}
type ZohoSession struct {
	ID            string                  `json:"id"`
	Purpose       string                  `json:"purpose"`
	Status        string                  `json:"status"`
	ErrorCode     string                  `json:"error_code"`
	ExpiresAt     time.Time               `json:"expires_at"`
	Identity      *zohobugtracker.User    `json:"identity,omitempty"`
	Portals       []zohobugtracker.Portal `json:"portals"`
	PortalID      string                  `json:"portal_id,omitempty"`
	ConnectionID  string                  `json:"connection_id,omitempty"`
	ParentVersion uint64                  `json:"parent_version"`
	BindingID     string                  `json:"binding_id,omitempty"`
}
type ZohoCallbackTarget struct {
	proof    zohoProof
	verifier string
}
type ZohoCallbackResult struct {
	identity zohobugtracker.User
	portals  []zohobugtracker.Portal
	token    zohobugtracker.Token
}
type ZohoCompleteTarget struct {
	session  string
	proof    zohoProof
	token    zohobugtracker.Token
	portal   zohobugtracker.Portal
	identity zohobugtracker.User
	verified bool
}

func zohoMissing(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return e
}
func zohoNamespace(id string) string { return "us:" + id }
func zohoID(namespace string) string {
	if strings.HasPrefix(namespace, "us:") && zohobugtracker.ValidID(namespace[3:]) {
		return namespace[3:]
	}
	return ""
}
func (s Service) zohoClient(personal bool) *zohobugtracker.Client {
	if personal {
		return s.ZohoIdentity
	}
	return s.Zoho
}
func (s Service) zohoConfigured(personal bool) bool {
	c := s.zohoClient(personal)
	return c != nil && c.App.ClientID != "" && c.App.ClientSecret != "" && s.Box != nil && s.PublicURL != "" && (!personal || s.Zoho != nil && c.App.ClientID != s.Zoho.App.ClientID)
}
func (s Service) ZohoIdentityConfigured() bool { return s.zohoConfigured(true) }
func zohoFingerprint(c *zohobugtracker.Client) string {
	if c == nil {
		return ""
	}
	raw, _ := json.Marshal(c.App)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s Service) zohoProofConfigured(p zohoProof) bool {
	return s.zohoConfigured(p.Purpose == PurposePersonal) && p.AppID == s.zohoClient(p.Purpose == PurposePersonal).App.ClientID && p.AppFingerprint == zohoFingerprint(s.zohoClient(p.Purpose == PurposePersonal))
}
func (s Service) ZohoRedirectURI() string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/integrations/zoho_bugtracker/callback"
}
func zohoParent(ctx context.Context, tx pgx.Tx, id string, active bool) (string, uint64, error) {
	if !membership.ValidateUUID(id) {
		return "", 0, ErrInvalidRequest
	}
	var ns, status string
	var version uint64
	e := tx.QueryRow(ctx, `SELECT external_id,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='zoho_bugtracker' AND scope='ORGANIZATION' AND status<>'DISCONNECTED' FOR UPDATE`, id).Scan(&ns, &version, &status)
	if e != nil {
		return "", 0, zohoMissing(e)
	}
	if active && status != StatusActive {
		return "", 0, ErrNeedsReauth
	}
	portal := zohoID(ns)
	if portal == "" {
		return "", 0, ErrInvalidRequest
	}
	return portal, version, nil
}
func (s Service) StartZoho(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in ZohoStartInput) (StartResult, error) {
	personal := in.Purpose == PurposePersonal
	if !personal && in.Purpose != PurposeOrganization && in.Purpose != PurposeReauthorize {
		return StartResult{}, ErrInvalidRequest
	}
	if !s.zohoConfigured(personal) {
		return StartResult{}, ErrNotConfigured
	}
	p := zohoProof{Purpose: in.Purpose, ParentID: in.ConnectionID, AppID: s.zohoClient(personal).App.ClientID, AppFingerprint: zohoFingerprint(s.zohoClient(personal))}
	if personal || in.Purpose == PurposeReauthorize {
		var e error
		p.PortalID, p.ParentVersion, e = zohoParent(ctx, tx, in.ConnectionID, personal)
		if e != nil {
			return StartResult{}, e
		}
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
		var e error
		p.Email, e = personalEmail(ctx, tx, actor)
		if e != nil {
			return StartResult{}, e
		}
		e = tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='zoho_bugtracker' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED'`, in.ConnectionID, actor.UserID).Scan(&p.BindingVersion)
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
	verifier, e := randomBase64URL(32)
	if e != nil {
		return StartResult{}, e
	}
	sum := sha256.Sum256([]byte(verifier))
	authorize, e := s.zohoClient(personal).AuthorizeURL(state, base64.RawURLEncoding.EncodeToString(sum[:]), s.ZohoRedirectURI(), personal)
	if e != nil {
		return StartResult{}, e
	}
	sealed, _, e := s.seal([]byte(verifier), oauthAAD(org, id))
	if e != nil {
		return StartResult{}, e
	}
	raw, _ := json.Marshal([]zohoProof{p})
	var parent *string
	if p.ParentID != "" {
		parent = &p.ParentID
	}
	var expiry time.Time
	e = tx.QueryRow(ctx, `INSERT INTO openfox.integration_oauth_states(organization_id,id,provider,purpose,user_id,state_hash,code_verifier_sealed,connection_id,pending_accounts) VALUES(openfox_private.current_organization_id(),$1::uuid,'zoho_bugtracker',$2,$3::uuid,$4,$5,$6::uuid,$7::jsonb) RETURNING expires_at`, id, strings.ToUpper(in.Purpose), actor.UserID, TokenHash(state), sealed, parent, raw).Scan(&expiry)
	return StartResult{SessionID: id, AuthorizeURL: authorize, ExpiresAt: expiry}, e
}
func lockZohoSession(ctx context.Context, tx pgx.Tx, id, user string) (ZohoSession, zohoProof, []byte, []byte, bool, error) {
	var v ZohoSession
	var p zohoProof
	var raw, pending, verifier []byte
	var expired, consumed bool
	var purpose string
	if !membership.ValidateUUID(id) {
		return v, p, nil, nil, false, ErrNotFound
	}
	e := tx.QueryRow(ctx, `SELECT id::text,status,error_code,expires_at,expires_at<=clock_timestamp(),consumed_at IS NOT NULL,pending_accounts,pending_sealed,code_verifier_sealed,lower(purpose) FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND user_id=$2::uuid AND provider='zoho_bugtracker' FOR UPDATE`, id, user).Scan(&v.ID, &v.Status, &v.ErrorCode, &v.ExpiresAt, &expired, &consumed, &raw, &pending, &verifier, &purpose)
	if e != nil {
		return v, p, nil, nil, false, zohoMissing(e)
	}
	var proofs []zohoProof
	if json.Unmarshal(raw, &proofs) != nil {
		return v, p, nil, nil, false, ErrOAuthSessionState
	}
	if len(proofs) == 1 {
		p = proofs[0]
	}
	p.Purpose = purpose
	if expired && (v.Status == statusPending || v.Status == statusSelecting) {
		v.Status = statusExpired
		p = zohoProof{Purpose: purpose}
		pending = nil
		verifier = nil
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	}
	v.Purpose, v.Identity, v.Portals, v.PortalID, v.ConnectionID, v.ParentVersion, v.BindingID = p.Purpose, p.Identity, p.Portals, p.PortalID, p.ParentID, p.ParentVersion, p.BindingID
	if v.Portals == nil {
		v.Portals = []zohobugtracker.Portal{}
	}
	return v, p, pending, verifier, consumed, e
}
func (s Service) zohoRecheck(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p zohoProof) error {
	if p.ParentID != "" {
		portal, version, e := zohoParent(ctx, tx, p.ParentID, p.Purpose == PurposePersonal)
		if e != nil {
			return e
		}
		if portal != p.PortalID || version != p.ParentVersion {
			return ErrVersionConflict
		}
	}
	if e := checkPersonalActor(ctx, tx, org, actor); e != nil {
		return e
	}
	if p.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return ErrForbidden
	}
	if !s.zohoProofConfigured(p) {
		return ErrZohoAppChanged
	}
	if p.Purpose == PurposePersonal {
		email, e := personalEmail(ctx, tx, actor)
		if e != nil {
			return e
		}
		if email != p.Email {
			return ErrPersonalIdentity
		}
	}
	return nil
}
func (s Service) GetZohoSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (ZohoSession, error) {
	v, p, _, _, _, e := lockZohoSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return v, e
	}
	if e = checkPersonalActor(ctx, tx, org, actor); e != nil {
		return ZohoSession{}, e
	}
	if p.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return ZohoSession{}, ErrForbidden
	}
	return v, nil
}
func (s Service) ConsumeZohoCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (ZohoCallbackTarget, error) {
	v, p, _, sealed, consumed, e := lockZohoSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return ZohoCallbackTarget{}, e
	}
	if v.Status != statusPending || consumed {
		return ZohoCallbackTarget{}, ErrOAuthSessionState
	}
	if e = s.zohoRecheck(ctx, tx, org, actor, p); e != nil {
		return ZohoCallbackTarget{}, e
	}
	verifier, e := s.open(sealed, oauthAAD(org, id))
	if e != nil {
		return ZohoCallbackTarget{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET consumed_at=clock_timestamp(),code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return ZohoCallbackTarget{proof: p, verifier: string(verifier)}, e
}

// Directory readers finish bounded pagination and reject identity/page changes.
func zohoPortals(ctx context.Context, r ZohoRuntime, c *zohobugtracker.Client, token string) (string, []zohobugtracker.Portal, error) {
	var subject string
	portals := []zohobugtracker.Portal{}
	seen := map[zohobugtracker.ID]bool{}
	index := 1
	for pages := 0; pages < 11; pages++ {
		var page zohobugtracker.PortalPage
		e := r.Call(ctx, func() error { var e error; page, e = c.PortalsPage(ctx, token, index); return e })
		if e != nil {
			return "", nil, e
		}
		if subject != "" && subject != string(page.LoginID) {
			return "", nil, ErrPersonalIdentity
		}
		subject = string(page.LoginID)
		for _, p := range page.Portals {
			if seen[p.ID] {
				return "", nil, ErrZohoLimit
			}
			seen[p.ID] = true
			p.Name = asanaDisplayName(p.Name, string(p.ID))
			portals = append(portals, p)
		}
		if len(portals) > 1000 {
			return "", nil, ErrZohoLimit
		}
		if page.NextIndex == 0 {
			return subject, portals, nil
		}
		index = page.NextIndex
	}
	return "", nil, ErrZohoLimit
}
func zohoUser(ctx context.Context, r ZohoRuntime, c *zohobugtracker.Client, token, portal, subject, org string, parent *ZohoAccess) (zohobugtracker.User, error) {
	var found zohobugtracker.User
	seen := map[zohobugtracker.ID]bool{}
	index := 1
	for pages := 0; pages < 11; pages++ {
		var page zohobugtracker.UserPage
		get := func(access string) error { var e error; page, e = c.UsersPage(ctx, access, portal, index); return e }
		var e error
		if parent != nil {
			_, e = r.Do(ctx, org, parent.ConnectionID, parent.ConnectionVersion, parent.CredentialGeneration, get)
		} else {
			e = r.Call(ctx, func() error { return get(token) })
		}
		if e != nil {
			return found, e
		}
		for _, u := range page.Users {
			if seen[u.ID] {
				return found, ErrZohoLimit
			}
			seen[u.ID] = true
			if string(u.ID) == subject {
				found = u
			}
		}
		if len(seen) > 1000 {
			return found, ErrZohoLimit
		}
		if page.NextIndex == 0 {
			if found.ID == "" || !found.Active {
				return found, ErrPersonalIdentity
			}
			found.Name = asanaDisplayName(found.Name, string(found.ID))
			found.Email = strings.ToLower(strings.TrimSpace(found.Email))
			return found, nil
		}
		index = page.NextIndex
	}
	return found, ErrZohoLimit
}

func (s Service) VerifyZohoCallback(ctx context.Context, r ZohoRuntime, org string, target ZohoCallbackTarget, code string) (ZohoCallbackResult, error) {
	var out ZohoCallbackResult
	p := target.proof
	if !s.zohoProofConfigured(p) {
		return out, ErrZohoAppChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := s.zohoClient(p.Purpose == PurposePersonal)
	var token zohobugtracker.Token
	e := r.Call(ctx, func() error {
		var e error
		token, e = c.Exchange(ctx, code, target.verifier, s.ZohoRedirectURI())
		return e
	})
	if e != nil {
		return out, e
	}
	if !zohobugtracker.ScopeMatches(token.Scope, p.Purpose == PurposePersonal) {
		return out, ErrPersonalCapability
	}
	subject, portals, e := zohoPortals(ctx, r, c, token.AccessToken)
	if e != nil {
		return out, e
	}
	if p.Purpose == PurposePersonal {
		visible := false
		for _, portal := range portals {
			visible = visible || string(portal.ID) == p.PortalID
		}
		if !visible {
			return out, ErrPersonalIdentity
		}
		access := ZohoAccess{ConnectionID: p.ParentID, ConnectionVersion: p.ParentVersion}
		u, e := zohoUser(ctx, r, s.Zoho, "", p.PortalID, subject, org, &access)
		if e != nil {
			return out, e
		}
		if u.Email != p.Email || !zohoMemberRole(u.Role) {
			return out, ErrPersonalIdentity
		}
		return ZohoCallbackResult{identity: u}, nil
	}
	if !zohobugtracker.ValidToken(token.RefreshToken) {
		return out, ErrNeedsReauth
	}
	out.identity = zohobugtracker.User{ID: zohobugtracker.ID(subject), Name: subject}
	out.token = token
	out.portals = []zohobugtracker.Portal{}
	for _, portal := range portals {
		if portal.Role == "admin" {
			out.portals = append(out.portals, portal)
		}
	}
	if len(out.portals) == 0 {
		return out, ErrForbidden
	}
	return out, nil
}
func zohoMemberRole(role string) bool {
	return role == "admin" || role == "manager" || role == "employee"
}
func (s Service) FinishZohoCallback(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, result ZohoCallbackResult, failure string) error {
	v, p, _, _, consumed, e := lockZohoSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return e
	}
	if v.Status != statusPending || !consumed {
		return ErrOAuthSessionState
	}
	if e = s.zohoRecheck(ctx, tx, org, actor, p); e != nil {
		return e
	}
	failure = bitbucketFailure(failure)
	var pending []byte
	if failure == "" {
		if !zohobugtracker.ValidID(string(result.identity.ID)) {
			return ErrPersonalIdentity
		}
		p.Identity = &result.identity
		p.Portals = result.portals
		if p.Purpose != PurposePersonal {
			raw, _ := json.Marshal(result.token)
			pending, _, e = s.sealZoho(raw, oauthAAD(org, id))
			if e != nil {
				return e
			}
		}
	}
	raw, _ := json.Marshal([]zohoProof{p})
	if len(raw) > 60<<10 {
		return ErrZohoLimit
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
func (s Service) PrepareZohoComplete(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in ZohoCompleteInput) (ZohoCompleteTarget, error) {
	var out ZohoCompleteTarget
	v, p, pending, _, _, e := lockZohoSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return out, e
	}
	if e = checkPersonalActor(ctx, tx, org, actor); e != nil {
		return out, e
	}
	if p.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	portal := in.PortalID
	if p.Purpose == PurposePersonal {
		if portal != "" && portal != p.PortalID {
			return out, ErrInvalidRequest
		}
		portal = p.PortalID
	}
	if v.Status == statusCompleted {
		if portal != p.PortalID {
			return out, ErrVersionConflict
		}
		return ZohoCompleteTarget{session: id, proof: p, verified: true}, nil
	}
	if v.Status != statusSelecting || p.Identity == nil || !zohobugtracker.ValidID(portal) {
		return out, ErrOAuthSessionState
	}
	if e = s.zohoRecheck(ctx, tx, org, actor, p); e != nil {
		return out, e
	}
	out.session, out.proof = id, p
	if p.Purpose == PurposePersonal {
		out.portal = zohobugtracker.Portal{ID: zohobugtracker.ID(portal)}
		return out, nil
	}
	for _, candidate := range p.Portals {
		if string(candidate.ID) == portal {
			out.portal = candidate
			break
		}
	}
	if out.portal.ID == "" || p.Purpose == PurposeReauthorize && portal != p.PortalID {
		return out, ErrInvalidRequest
	}
	raw, e := s.open(pending, oauthAAD(org, id))
	if e != nil {
		return out, e
	}
	if json.Unmarshal(raw, &out.token) != nil || !out.token.ExpiresAt.After(time.Now()) {
		return out, ErrNeedsReauth
	}
	return out, nil
}
func (s Service) VerifyZohoComplete(ctx context.Context, r ZohoRuntime, org string, target ZohoCompleteTarget) (ZohoCompleteTarget, error) {
	if target.proof.BindingID != "" {
		return target, nil
	}
	p := target.proof
	if !s.zohoProofConfigured(p) {
		return target, ErrZohoAppChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	portal := string(target.portal.ID)
	var parent *ZohoAccess
	if p.Purpose == PurposePersonal {
		parent = &ZohoAccess{ConnectionID: p.ParentID, ConnectionVersion: p.ParentVersion}
	} else {
		subject, portals, e := zohoPortals(ctx, r, s.Zoho, target.token.AccessToken)
		if e != nil {
			return target, e
		}
		if subject != string(p.Identity.ID) {
			return target, ErrPersonalIdentity
		}
		admin := false
		for _, v := range portals {
			if string(v.ID) == portal && v.Role == "admin" {
				admin = true
				target.portal = v
			}
		}
		if !admin {
			return target, ErrForbidden
		}
	}
	u, e := zohoUser(ctx, r, s.Zoho, target.token.AccessToken, portal, string(p.Identity.ID), org, parent)
	if e != nil {
		return target, e
	}
	if p.Purpose == PurposePersonal {
		if u.Email != p.Email || !zohoMemberRole(u.Role) {
			return target, ErrPersonalIdentity
		}
	} else if u.Role != "admin" {
		return target, ErrForbidden
	}
	target.identity = u
	target.verified = true
	return target, nil
}
func (s Service) CompleteZoho(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, target ZohoCompleteTarget) (Connection, bool, error) {
	if !target.verified || target.session != id {
		return Connection{}, false, ErrInvalidRequest
	}
	v, p, _, _, _, e := lockZohoSession(ctx, tx, id, actor.UserID)
	if e != nil {
		return Connection{}, false, e
	}
	if e = checkPersonalActor(ctx, tx, org, actor); e != nil {
		return Connection{}, false, e
	}
	if p.Purpose != PurposePersonal && !CanManage(actor.Role) {
		return Connection{}, false, ErrForbidden
	}
	if v.Status == statusCompleted && p.BindingID != "" {
		selected := string(target.portal.ID)
		if selected == "" {
			selected = target.proof.PortalID
		}
		if selected != p.PortalID {
			return Connection{}, false, ErrVersionConflict
		}
		c, e := loadConnection(ctx, tx, p.BindingID)
		return c, false, e
	}
	if v.Status != statusSelecting || p.Identity == nil || target.proof.Identity == nil || p.Identity.ID != target.proof.Identity.ID || target.identity.ID != p.Identity.ID || p.AppFingerprint != target.proof.AppFingerprint {
		return Connection{}, false, ErrOAuthSessionState
	}
	if e = s.zohoRecheck(ctx, tx, org, actor, p); e != nil {
		return Connection{}, false, e
	}
	if p.Purpose == PurposePersonal {
		return s.completeZohoPersonal(ctx, tx, org, actor, id, p, target)
	}
	return s.completeZohoOrganization(ctx, tx, org, actor, id, p, target)
}
func (s Service) completeZohoOrganization(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, p zohoProof, target ZohoCompleteTarget) (Connection, bool, error) {
	portal := string(target.portal.ID)
	if !zohobugtracker.ValidID(portal) || !target.token.ExpiresAt.After(time.Now()) || target.identity.Role != "admin" || !target.identity.Active {
		return Connection{}, false, ErrInvalidRequest
	}
	allowed := false
	for _, candidate := range p.Portals {
		allowed = allowed || candidate.ID == target.portal.ID
	}
	if !allowed {
		return Connection{}, false, ErrInvalidRequest
	}
	binding := p.ParentID
	var e error
	if binding != "" {
		if p.PortalID != portal {
			return Connection{}, false, ErrVersionConflict
		}
		var subject string
		e = tx.QueryRow(ctx, `SELECT subject_id FROM openfox.integration_zoho_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, binding).Scan(&subject)
		if e != nil {
			return Connection{}, false, e
		}
		if subject != string(target.identity.ID) {
			return Connection{}, false, ErrPersonalIdentity
		}
	} else {
		binding, e = membership.NewID()
		if e != nil {
			return Connection{}, false, e
		}
	}
	creds := connectionCredentials{AccessToken: target.token.AccessToken, RefreshToken: target.token.RefreshToken, Scope: target.token.Scope}
	if !zohobugtracker.ValidToken(creds.RefreshToken) {
		return Connection{}, false, ErrNeedsReauth
	}
	sealed, key, e := s.sealZohoCredentials(org, binding, creds)
	if e != nil {
		return Connection{}, false, e
	}
	settings, _ := json.Marshal(map[string]string{"auth_method": "zoho_oauth", "delegating_user": target.identity.Name, "delegating_user_id": string(target.identity.ID)})
	if p.ParentID == "" {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,settings,credentials_sealed,credentials_key_id,token_expires_at,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'zoho_bugtracker','ORGANIZATION',$2,$3,'portal',$4::jsonb,$5,$6,$7,$8::uuid)`, binding, zohoNamespace(portal), asanaDisplayName(target.portal.Name, portal), settings, sealed, key, target.token.ExpiresAt, actor.UserID)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',settings=$2::jsonb,display_name=$3,credentials_sealed=$4,credentials_key_id=$5,token_expires_at=$6,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding, settings, asanaDisplayName(target.portal.Name, portal), sealed, key, target.token.ExpiresAt)
	}
	if isUniqueViolation(e) {
		return Connection{}, false, ErrZohoAlreadyConnected
	}
	if e != nil {
		return Connection{}, false, e
	}
	sum := sha256.Sum256([]byte(creds.RefreshToken))
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_zoho_runtime(organization_id,connection_id,subject_id,subject_name,app_id,refresh_fingerprint) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5) ON CONFLICT(organization_id,connection_id) DO UPDATE SET subject_name=excluded.subject_name,app_id=excluded.app_id,refresh_fingerprint=excluded.refresh_fingerprint,credential_generation=integration_zoho_runtime.credential_generation+1,credentials_version=integration_zoho_runtime.credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,refresh_failures=0,retry_at=NULL,last_error='',updated_at=clock_timestamp()`, binding, string(target.identity.ID), target.identity.Name, p.AppID, sum[:])
	if isUniqueViolation(e) {
		return Connection{}, false, ErrZohoGrantInUse
	}
	if e != nil {
		return Connection{}, false, e
	}
	p.PortalID = portal
	return s.finishZohoBinding(ctx, tx, id, p, binding)
}
func (s Service) completeZohoPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, p zohoProof, target ZohoCompleteTarget) (Connection, bool, error) {
	if string(target.portal.ID) != p.PortalID || target.identity.Email != p.Email || !target.identity.Active || !zohoMemberRole(target.identity.Role) {
		return Connection{}, false, ErrPersonalIdentity
	}
	var binding, external string
	var version uint64
	e := tx.QueryRow(ctx, `SELECT id::text,external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='zoho_bugtracker' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, p.ParentID, actor.UserID).Scan(&binding, &external, &version)
	create := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !create {
		return Connection{}, false, e
	}
	if version != p.BindingVersion {
		return Connection{}, false, ErrVersionConflict
	}
	subject := zohoNamespace(string(target.identity.ID))
	if !create && external != subject {
		return Connection{}, false, ErrPersonalIdentity
	}
	settings, _ := json.Marshal(map[string]any{"auth_method": "zoho_bugtracker_identity", "company_id": zohoNamespace(p.PortalID), "parent_version": p.ParentVersion, "verified_email": p.Email, "verified_at": time.Now().UTC()})
	if create {
		binding, e = membership.NewID()
		if e != nil {
			return Connection{}, false, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'zoho_bugtracker','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$3::uuid)`, binding, p.ParentID, actor.UserID, subject, target.identity.Name, settings)
	} else {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',settings=$2::jsonb,display_name=$3,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, binding, settings, target.identity.Name)
	}
	if isUniqueViolation(e) {
		return Connection{}, false, ErrPersonalInUse
	}
	if e != nil {
		return Connection{}, false, e
	}
	return s.finishZohoBinding(ctx, tx, id, p, binding)
}
func (s Service) finishZohoBinding(ctx context.Context, tx pgx.Tx, id string, p zohoProof, binding string) (Connection, bool, error) {
	p.BindingID = binding
	p.Identity = nil
	p.Portals = nil
	raw, _ := json.Marshal([]zohoProof{p})
	_, e := tx.Exec(ctx, `UPDATE openfox.integration_oauth_states SET status='COMPLETED',completed_at=clock_timestamp(),pending_accounts=$2::jsonb,pending_sealed=NULL,code_verifier_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, raw)
	if e != nil {
		return Connection{}, false, e
	}
	c, e := loadConnection(ctx, tx, binding)
	return c, true, e
}

// Two protocol-bounded tokens and optional scope fit the existing 16KiB
// provider envelope; ordinary secret limits remain unchanged.
func (s Service) sealZoho(raw []byte, aad string) ([]byte, string, error) {
	if s.Box == nil {
		return nil, "", ErrSecretsUnavailable
	}
	if len(raw) > 16384-keyIDLen-secrets.NonceSize-16 {
		return nil, "", ErrInvalidRequest
	}
	sealed, e := s.Box.SealOAuth(raw, aad)
	if e != nil {
		return nil, "", e
	}
	blob := append([]byte(sealed.KeyID), sealed.Nonce...)
	return append(blob, sealed.Ciphertext...), sealed.KeyID, nil
}
func (s Service) sealZohoCredentials(org, id string, creds connectionCredentials) ([]byte, string, error) {
	raw, e := json.Marshal(creds)
	if e != nil {
		return nil, "", e
	}
	return s.sealZoho(raw, connectionAAD(org, id))
}
