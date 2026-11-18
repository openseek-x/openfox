package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

var (
	ErrTAPDPersonalAuth       = errors.New("integrations: TAPD user token rejected")
	ErrTAPDPersonalPermission = errors.New("integrations: TAPD project identity unavailable")
	ErrTAPDPersonalEmail      = errors.New("integrations: TAPD email does not match")
	ErrTAPDPersonalParent     = errors.New("integrations: TAPD identity requires a Basic parent")
	ErrTAPDPersonalInUse      = errors.New("integrations: TAPD identity already bound")
	ErrTAPDPersonalLimit      = errors.New("integrations: TAPD identity project limit")
	ErrTAPDPersonalRate       = errors.New("integrations: TAPD rate limited")
)

const TAPDPersonalMaxProjects = 20

type TAPDPersonalInput struct {
	ConnectionID string `json:"connection_id"`
	ProjectID    string `json:"project_id"`
	Version      uint64 `json:"version"`
	AccessToken  string `json:"access_token"`
}

// TAPDIdentity records a one-time identity proof, never an authorization to post.
type TAPDIdentity struct {
	AuthMethod    string    `json:"auth_method"`
	Nick          string    `json:"nick"`
	CompanyID     string    `json:"company_id"`
	Email         string    `json:"verified_email"`
	Projects      []string  `json:"projects"`
	VerifiedAt    time.Time `json:"verified_at"`
	ParentVersion uint64    `json:"parent_version"`
}

type TAPDPersonalGrant struct {
	parentID, projectID, companyID, email string
	parentVersion                         uint64
	identityID, nick, name                string
	token                                 string
	parentAuth                            ProviderTokenInput
	transport                             http.RoundTripper
	verified                              bool
}

func tapdNickValid(nick string) bool {
	return nick != "" && len(nick) <= 255 && strings.TrimSpace(nick) == nick &&
		!strings.ContainsAny(nick, ",;|<>") && !strings.ContainsFunc(nick, unicode.IsControl)
}

func (s Service) PrepareTAPDPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in TAPDPersonalInput) (TAPDPersonalGrant, error) {
	var g TAPDPersonalGrant
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return g, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return g, ErrForbidden
	}
	if !membership.ValidateUUID(in.ConnectionID) || !tapdCompanyPattern.MatchString(in.ProjectID) || in.AccessToken == "" || len(in.AccessToken) > 4096 || strings.ContainsFunc(in.AccessToken, unicode.IsControl) {
		return g, ErrInvalidRequest
	}
	in.ConnectionID = strings.ToLower(in.ConnectionID)
	if err := tx.QueryRow(ctx, `SELECT u.email FROM openfox.organization_members m JOIN openfox.users u ON u.id=m.user_id
	 WHERE m.organization_id=openfox_private.current_organization_id() AND m.user_id=$1::uuid AND m.status='ACTIVE'
	 AND m.role<>'GUEST' AND m.authorization_version=$2 FOR SHARE OF m,u`, actor.UserID, actor.AuthorizationVersion).Scan(&g.email); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return g, ErrForbidden
		}
		return g, err
	}
	var sealed []byte
	var method string
	err := tx.QueryRow(ctx, `SELECT version,credentials_sealed,COALESCE(settings->>'auth_method',''),COALESCE(settings->>'company_id','')
	 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
	 AND provider='tapd' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, in.ConnectionID).Scan(&g.parentVersion, &sealed, &method, &g.companyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	if method != "basic" {
		return g, ErrTAPDPersonalParent
	}
	plain, err := s.open(sealed, connectionAAD(strings.ToLower(org), in.ConnectionID))
	if err != nil {
		return g, err
	}
	if json.Unmarshal(plain, &g.parentAuth) != nil || !g.parentAuth.validate("tapd") || g.parentAuth.Method != "basic" || g.parentAuth.CompanyID != g.companyID {
		return g, ErrNeedsReauth
	}
	g.parentID, g.projectID, g.token, g.transport = in.ConnectionID, in.ProjectID, in.AccessToken, s.TAPDPersonalTransport
	return g, nil
}

// tapdScalar accepts the documented string IDs and numeric company_id without
// converting through float64 or silently accepting non-integral IDs.
type tapdScalar string

func (v *tapdScalar) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
		if !tapdCompanyPattern.MatchString(s) {
			return ErrUpstreamUnavailable
		}
	}
	*v = tapdScalar(s)
	return nil
}

func VerifyTAPDPersonal(ctx context.Context, g TAPDPersonalGrant) (TAPDPersonalGrant, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var user struct {
		ID      tapdScalar `json:"id"`
		Nick    string     `json:"nick"`
		Name    string     `json:"name"`
		Enabled tapdScalar `json:"enabled"`
		Status  tapdScalar `json:"status_id"`
	}
	if err := g.get(ctx, "/users/info", nil, false, &user); err != nil {
		return g, err
	}
	if !tapdCompanyPattern.MatchString(string(user.ID)) || !tapdNickValid(user.Nick) || user.Enabled != "1" || user.Status != "1" {
		return g, ErrTAPDPersonalAuth
	}
	query := url.Values{"workspace_id": {g.projectID}}
	for _, basic := range []bool{false, true} {
		var project struct {
			Workspace struct {
				ID        tapdScalar `json:"id"`
				CompanyID tapdScalar `json:"company_id"`
				Status    string     `json:"status"`
			} `json:"Workspace"`
		}
		if err := g.get(ctx, "/workspaces/get_workspace_info", query, basic, &project); err != nil {
			return g, err
		}
		if string(project.Workspace.ID) != g.projectID || string(project.Workspace.CompanyID) != g.companyID || project.Workspace.Status != "normal" {
			return g, ErrTAPDPersonalPermission
		}
	}
	var members []struct {
		User struct {
			Nick  string `json:"user"`
			Email string `json:"email"`
		} `json:"UserWorkspace"`
	}
	if err := g.get(ctx, "/workspaces/users", url.Values{"workspace_id": {g.projectID}, "user": {user.Nick}, "fields": {"user,email"}}, true, &members); err != nil {
		return g, err
	}
	if len(members) != 1 || members[0].User.Nick != user.Nick {
		return g, ErrTAPDPersonalPermission
	}
	email := strings.TrimSpace(members[0].User.Email)
	if email == "" || !strings.EqualFold(email, strings.TrimSpace(g.email)) {
		return g, ErrTAPDPersonalEmail
	}
	g.identityID, g.nick, g.name, g.verified = string(user.ID), user.Nick, strings.TrimSpace(user.Name), true
	if g.name == "" || len(g.name) > 200 {
		g.name = user.Nick
	}
	if len(g.name) > 200 {
		g.name = g.identityID
	}
	return g, nil
}

func (g TAPDPersonalGrant) get(ctx context.Context, path string, q url.Values, basic bool, dst any) error {
	endpoint := "https://api.tapd.cn" + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrInvalidRequest
	}
	if basic {
		req.SetBasicAuth(g.parentAuth.Account, g.parentAuth.Secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	req.Header.Set("Accept", "application/json")
	client := http.Client{Transport: g.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 401:
		return ErrTAPDPersonalAuth
	case 403:
		return ErrTAPDPersonalPermission
	case 429:
		return ErrTAPDPersonalRate
	}
	if resp.StatusCode != 200 {
		return ErrUpstreamUnavailable
	}
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return ErrUpstreamUnavailable
	}
	var envelope struct {
		Status int             `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ErrUpstreamUnavailable
	}
	if envelope.Status != 1 {
		if path == "/users/info" {
			return ErrTAPDPersonalAuth
		}
		return ErrTAPDPersonalPermission
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, dst) != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}

func (s Service) SaveTAPDPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in TAPDPersonalInput, g TAPDPersonalGrant) (Connection, error) {
	current, err := s.PrepareTAPDPersonal(ctx, tx, org, actor, in)
	if err != nil {
		return Connection{}, err
	}
	if !g.verified || current.parentID != g.parentID || current.parentVersion != g.parentVersion || current.companyID != g.companyID || current.email != g.email || current.projectID != g.projectID || current.token != g.token {
		return Connection{}, ErrVersionConflict
	}
	// Serializes this member's competing first binds, including different parents.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":tapd-identity:"+actor.UserID); err != nil {
		return Connection{}, err
	}
	var id, external string
	var version uint64
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT id::text,external_id,version,settings FROM openfox.integration_connections
	 WHERE organization_id=openfox_private.current_organization_id() AND provider='tapd' AND scope='PERSONAL'
	 AND owner_user_id=$1::uuid AND parent_connection_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, actor.UserID, in.ConnectionID).Scan(&id, &external, &version, &raw)
	create := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !create {
		return Connection{}, err
	}
	settings := TAPDIdentity{AuthMethod: "tapd_identity", Nick: g.nick, CompanyID: g.companyID, Email: strings.ToLower(strings.TrimSpace(g.email)), Projects: []string{}, ParentVersion: g.parentVersion}
	if !create {
		var old TAPDIdentity
		if json.Unmarshal(raw, &old) != nil || old.AuthMethod != "tapd_identity" || external != g.identityID || old.CompanyID != g.companyID {
			return Connection{}, ErrTAPDPersonalInUse
		}
		// Proofs from a different parent revision, nickname, or local email
		// cannot be revived by checking just one of the previous projects.
		if old.Nick == g.nick && old.Email == settings.Email && old.ParentVersion == settings.ParentVersion {
			settings.Projects = old.Projects
		}
		if old.Nick == g.nick && old.Email == settings.Email && old.ParentVersion == settings.ParentVersion && slices.Contains(old.Projects, in.ProjectID) {
			return loadConnection(ctx, tx, id)
		}
	}
	if in.Version != version {
		return Connection{}, ErrVersionConflict
	}
	if !slices.Contains(settings.Projects, in.ProjectID) {
		if len(settings.Projects) >= TAPDPersonalMaxProjects {
			return Connection{}, ErrTAPDPersonalLimit
		}
		settings.Projects = append(settings.Projects, in.ProjectID)
	}
	slices.Sort(settings.Projects)
	settings.VerifiedAt = time.Now().UTC()
	encoded, err := json.Marshal(settings)
	if err != nil {
		return Connection{}, err
	}
	if create {
		id, err = membership.NewID()
		if err != nil {
			return Connection{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by)
		 VALUES(openfox_private.current_organization_id(),$1::uuid,'tapd','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$3::uuid)`, id, in.ConnectionID, actor.UserID, g.identityID, g.name, encoded)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET display_name=$2,settings=$3::jsonb,version=version+1,updated_at=clock_timestamp()
		 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, g.name, encoded)
	}
	if isUniqueViolation(err) {
		return Connection{}, ErrTAPDPersonalInUse
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}

func (s Service) RemoveTAPDPersonalProject(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, project string, version uint64) (Connection, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return Connection{}, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return Connection{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || !tapdCompanyPattern.MatchString(project) || version == 0 {
		return Connection{}, ErrInvalidRequest
	}
	var parent string
	err := tx.QueryRow(ctx, `SELECT parent_connection_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
	 AND id=$1::uuid AND provider='tapd' AND scope='PERSONAL' AND owner_user_id=$2::uuid AND status<>'DISCONNECTED'`, id, actor.UserID).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, parent).Scan(&parent); err != nil {
		return Connection{}, err
	}
	var raw []byte
	var current uint64
	err = tx.QueryRow(ctx, `SELECT settings,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id()
	 AND id=$1::uuid AND provider='tapd' AND scope='PERSONAL' AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, id, actor.UserID).Scan(&raw, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	var settings TAPDIdentity
	if json.Unmarshal(raw, &settings) != nil || settings.AuthMethod != "tapd_identity" {
		return Connection{}, ErrInvalidRequest
	}
	if !slices.Contains(settings.Projects, project) {
		return loadConnection(ctx, tx, id)
	}
	if current != version {
		return Connection{}, ErrVersionConflict
	}
	settings.Projects = slices.DeleteFunc(settings.Projects, func(v string) bool { return v == project })
	if len(settings.Projects) == 0 {
		result, err := s.Disconnect(ctx, tx, org, actor, id, version, "")
		return result.Connection, err
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings=$2::jsonb,version=version+1,updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, encoded)
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}
