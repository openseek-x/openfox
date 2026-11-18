package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

var (
	ErrShortcutAuth      = errors.New("integrations: Shortcut personal token rejected")
	ErrShortcutEmail     = errors.New("integrations: Shortcut email does not match")
	ErrShortcutWorkspace = errors.New("integrations: Shortcut workspace does not match")
	ErrShortcutInUse     = errors.New("integrations: Shortcut identity already bound; disconnect before changing identity")
	ErrShortcutRate      = errors.New("integrations: Shortcut rate limited")
)

type ShortcutPersonalInput struct {
	ConnectionID    string `json:"connection_id"`
	Version         uint64 `json:"version"`
	Token           string `json:"token"`
	CommentsEnabled bool   `json:"comments_enabled"`
}

type ShortcutIdentity struct {
	AuthMethod           string     `json:"auth_method"`
	CompanyID            string     `json:"company_id"`
	ParentVersion        uint64     `json:"parent_version"`
	Email                string     `json:"verified_email"`
	VerifiedAt           time.Time  `json:"verified_at"`
	CommentsEnabled      bool       `json:"comments_enabled"`
	CommentsAuthorizedAt *time.Time `json:"comments_authorized_at,omitempty"`
}

type ShortcutPersonalGrant struct {
	ConnectionID                               string
	Version                                    uint64
	CompanyID                                  string
	UserID                                     string
	CommentsAuthorizedAt                       time.Time
	parentID, actorID, org, email, token, name string
	historyID                                  string
	historyVersion                             uint64
	parentVersion                              uint64
	verified, disableOnly, commentsEnabled     bool
	transport                                  http.RoundTripper
}

type shortcutPersonalSecret struct{ Token, UserID, CompanyID string }

func (s Service) PrepareShortcutPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in ShortcutPersonalInput) (ShortcutPersonalGrant, error) {
	var g ShortcutPersonalGrant
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return g, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return g, ErrForbidden
	}
	if !membership.ValidateUUID(in.ConnectionID) || len(in.Token) > 4096 || strings.ContainsFunc(in.Token, unicode.IsControl) || (in.CommentsEnabled && in.Token == "") {
		return g, ErrInvalidRequest
	}
	if in.CommentsEnabled && s.Box == nil {
		return g, ErrSecretsUnavailable
	}
	g.parentID = strings.ToLower(in.ConnectionID)
	// Parent first, matching Disconnect and the verified import mapping path.
	err := tx.QueryRow(ctx, `SELECT external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='shortcut' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, g.parentID).Scan(&g.CompanyID, &g.parentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	if !membership.ValidateUUID(g.CompanyID) {
		return g, ErrNeedsReauth
	}
	err = tx.QueryRow(ctx, `SELECT u.normalized_email FROM openfox.organization_members m JOIN openfox.users u ON u.id=m.user_id WHERE m.organization_id=openfox_private.current_organization_id() AND m.user_id=$1::uuid AND m.status='ACTIVE' AND m.role<>'GUEST' AND m.authorization_version=$2 FOR SHARE OF m,u`, actor.UserID, actor.AuthorizationVersion).Scan(&g.email)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrForbidden
	}
	if err != nil {
		return g, err
	}
	g.org, g.actorID, g.token, g.transport, g.commentsEnabled = org, actor.UserID, in.Token, s.ProjectTransport, in.CommentsEnabled
	g.disableOnly = in.Token == ""
	// Fence an unlink/rebind that happens while the identity request is in flight.
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='shortcut' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid ORDER BY created_at DESC,id DESC LIMIT 1`, g.parentID, actor.UserID).Scan(&g.historyID, &g.historyVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return g, err
	}
	return g, nil
}

func VerifyShortcutPersonal(ctx context.Context, g ShortcutPersonalGrant) (ShortcutPersonalGrant, error) {
	if g.disableOnly {
		g.verified = true
		return g, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var current struct {
		ID        string `json:"id"`
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace2"`
	}
	if err := g.request(ctx, http.MethodGet, "/member", nil, &current); err != nil {
		return g, err
	}
	if !membership.ValidateUUID(current.ID) {
		return g, ErrShortcutAuth
	}
	if !strings.EqualFold(current.Workspace.ID, g.CompanyID) {
		return g, ErrShortcutWorkspace
	}
	var member struct {
		ID       string `json:"id"`
		Disabled *bool  `json:"disabled"`
		State    string `json:"state"`
		Profile  struct {
			Email       string `json:"email_address"`
			Deactivated *bool  `json:"deactivated"`
			Name        string `json:"name"`
		} `json:"profile"`
	}
	if err := g.request(ctx, http.MethodGet, "/members/"+strings.ToLower(current.ID), nil, &member); err != nil {
		return g, err
	}
	if !strings.EqualFold(member.ID, current.ID) || member.Disabled == nil || *member.Disabled || member.State != "full" || member.Profile.Deactivated == nil || *member.Profile.Deactivated {
		return g, ErrShortcutAuth
	}
	if strings.TrimSpace(member.Profile.Email) == "" || !strings.EqualFold(strings.TrimSpace(member.Profile.Email), g.email) {
		return g, ErrShortcutEmail
	}
	g.UserID = strings.ToLower(current.ID)
	g.name = strings.TrimSpace(member.Profile.Name)
	if g.name == "" || len(g.name) > 200 {
		g.name = g.UserID
	}
	g.verified = true
	return g, nil
}

func (s Service) SaveShortcutPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in ShortcutPersonalInput, g ShortcutPersonalGrant) (Connection, error) {
	current, err := s.PrepareShortcutPersonal(ctx, tx, org, actor, in)
	if err != nil {
		return Connection{}, err
	}
	if !g.verified || current.parentID != g.parentID || current.parentVersion != g.parentVersion || current.CompanyID != g.CompanyID || current.email != g.email || current.token != g.token || current.actorID != g.actorID || current.org != g.org || current.commentsEnabled != g.commentsEnabled || current.disableOnly != g.disableOnly {
		return Connection{}, ErrVersionConflict
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":shortcut-identity:"+g.CompanyID); err != nil {
		return Connection{}, err
	}
	// Prepare's history can become stale while this transaction waits for a
	// competing bind. Lock the latest row now, including a disconnected row, so
	// unlink cannot slip between this final fence and the active-binding lookup.
	current.historyID, current.historyVersion = "", 0
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='shortcut' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE`, g.parentID, actor.UserID).Scan(&current.historyID, &current.historyVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, err
	}
	var id, external, status string
	var version uint64
	var raw, sealed []byte
	err = tx.QueryRow(ctx, `SELECT id::text,external_id,version,settings,credentials_sealed,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='shortcut' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, g.parentID, actor.UserID).Scan(&id, &external, &version, &raw, &sealed, &status)
	create := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !create {
		return Connection{}, err
	}
	var old ShortcutIdentity
	if !create && (json.Unmarshal(raw, &old) != nil || old.AuthMethod != "shortcut_identity" || old.CompanyID != g.CompanyID) {
		return Connection{}, ErrNeedsReauth
	}
	if g.disableOnly {
		if create {
			return Connection{}, ErrInvalidRequest
		}
		if !old.CommentsEnabled && len(sealed) == 0 {
			return loadConnection(ctx, tx, id)
		}
		if g.historyID != current.historyID || g.historyVersion != current.historyVersion {
			return Connection{}, ErrVersionConflict
		}
		if in.Version != version {
			return Connection{}, ErrVersionConflict
		}
		// Closing comment access never refreshes the old identity proof.
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=NULL,credentials_key_id='',settings=(settings-'comments_authorized_at')||'{"comments_enabled":false}'::jsonb,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
		if err != nil {
			return Connection{}, err
		}
		return loadConnection(ctx, tx, id)
	}
	if !membership.ValidateUUID(g.UserID) || g.name == "" {
		return Connection{}, ErrInvalidRequest
	}
	if !create && external != g.UserID {
		return Connection{}, ErrShortcutInUse
	}
	settings := ShortcutIdentity{AuthMethod: "shortcut_identity", CompanyID: g.CompanyID, ParentVersion: g.parentVersion, Email: g.email, VerifiedAt: time.Now().UTC(), CommentsEnabled: in.CommentsEnabled}
	if !create && status == StatusActive && old.Email == g.email && old.ParentVersion == g.parentVersion && old.CommentsEnabled == in.CommentsEnabled {
		sameToken := !in.CommentsEnabled
		if in.CommentsEnabled {
			plain, e := s.open(sealed, connectionAAD(org, id))
			if e != nil {
				return Connection{}, e
			}
			var secret shortcutPersonalSecret
			sameToken = json.Unmarshal(plain, &secret) == nil && secret.Token == g.token && secret.UserID == g.UserID && secret.CompanyID == g.CompanyID
		}
		if sameToken {
			return loadConnection(ctx, tx, id)
		}
	}
	if in.Version != version {
		return Connection{}, ErrVersionConflict
	}
	if g.historyID != current.historyID || g.historyVersion != current.historyVersion {
		return Connection{}, ErrVersionConflict
	}
	if create {
		id, err = membership.NewID()
		if err != nil {
			return Connection{}, err
		}
	}
	var key string
	sealed = nil
	if in.CommentsEnabled {
		at := time.Now().UTC()
		if old.CommentsEnabled && old.CommentsAuthorizedAt != nil && old.ParentVersion == g.parentVersion && old.Email == g.email {
			at = *old.CommentsAuthorizedAt
		}
		settings.CommentsAuthorizedAt = &at
		plain, _ := json.Marshal(shortcutPersonalSecret{Token: g.token, UserID: g.UserID, CompanyID: g.CompanyID})
		sealed, key, err = s.seal(plain, connectionAAD(org, id))
		if err != nil {
			return Connection{}, err
		}
	}
	encoded, _ := json.Marshal(settings)
	if create {
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,credentials_sealed,credentials_key_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'shortcut','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6::jsonb,$7,$8,$3::uuid)`, id, g.parentID, actor.UserID, g.UserID, g.name, encoded, sealed, key)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET display_name=$2,settings=$3::jsonb,credentials_sealed=$4,credentials_key_id=$5,status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, g.name, encoded, sealed, key)
	}
	if isUniqueViolation(err) {
		return Connection{}, ErrShortcutInUse
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}

func (s Service) LoadShortcutPersonal(ctx context.Context, tx pgx.Tx, org, parent, author string) (ShortcutPersonalGrant, error) {
	var g ShortcutPersonalGrant
	var parentVersion uint64
	err := tx.QueryRow(ctx, `SELECT external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='shortcut' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, parent).Scan(&g.CompanyID, &parentVersion)
	if err != nil {
		return g, err
	}
	var sealed, raw []byte
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.external_id,c.version,c.credentials_sealed,c.settings FROM openfox.integration_connections c JOIN openfox.organization_members m ON m.organization_id=c.organization_id AND m.user_id=c.owner_user_id JOIN openfox.users u ON u.id=m.user_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.provider='shortcut' AND c.scope='PERSONAL' AND c.account_kind='identity' AND c.parent_connection_id=$1::uuid AND c.owner_user_id=$2::uuid AND c.status='ACTIVE' AND c.settings->>'auth_method'='shortcut_identity' AND c.settings->>'company_id'=$3 AND c.settings->>'parent_version'=$4 AND c.settings->>'verified_email'=u.normalized_email AND c.settings->>'comments_enabled'='true' AND c.credentials_sealed IS NOT NULL AND m.status='ACTIVE' AND m.role<>'GUEST' FOR SHARE OF c,m,u`, parent, author, g.CompanyID, strconv.FormatUint(parentVersion, 10)).Scan(&g.ConnectionID, &g.UserID, &g.Version, &sealed, &raw)
	if err != nil {
		return g, err
	}
	var settings ShortcutIdentity
	if json.Unmarshal(raw, &settings) != nil || settings.CommentsAuthorizedAt == nil {
		return g, ErrNeedsReauth
	}
	plain, err := s.open(sealed, connectionAAD(org, g.ConnectionID))
	if err != nil {
		return g, err
	}
	var secret shortcutPersonalSecret
	if json.Unmarshal(plain, &secret) != nil || secret.Token == "" || secret.UserID != g.UserID || secret.CompanyID != g.CompanyID {
		return g, ErrNeedsReauth
	}
	g.token, g.transport, g.CommentsAuthorizedAt = secret.Token, s.ProjectTransport, *settings.CommentsAuthorizedAt
	return g, nil
}

func (g ShortcutPersonalGrant) CreateComment(ctx context.Context, external, body string) (string, error) {
	parts := strings.Split(external, "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], g.CompanyID) || strings.TrimSpace(body) == "" || len(body) > 64000 {
		return "", ErrInvalidRequest
	}
	story, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || story <= 0 || strconv.FormatInt(story, 10) != parts[1] {
		return "", ErrInvalidRequest
	}
	var result struct {
		ID       int64  `json:"id"`
		StoryID  int64  `json:"story_id"`
		AuthorID string `json:"author_id"`
	}
	if err = g.request(ctx, http.MethodPost, "/stories/"+parts[1]+"/comments", map[string]string{"text": body}, &result); err != nil {
		return "", err
	}
	if result.ID <= 0 || result.StoryID != story || !strings.EqualFold(result.AuthorID, g.UserID) {
		return "", ErrUpstreamUnavailable
	}
	return strconv.FormatInt(result.ID, 10), nil
}

func (g ShortcutPersonalGrant) request(ctx context.Context, method, path string, body, result any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.app.shortcut.com/api/v3"+path, payload)
	if err != nil {
		return ErrInvalidRequest
	}
	req.Header.Set("Shortcut-Token", g.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Transport: g.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 401, 403:
		return ErrShortcutAuth
	case 429:
		return ErrShortcutRate
	}
	want := http.StatusOK
	if method == http.MethodPost {
		want = http.StatusCreated
	}
	if resp.StatusCode != want {
		return ErrUpstreamUnavailable
	}
	const limit = 1 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(b) > limit || json.Unmarshal(b, result) != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}
