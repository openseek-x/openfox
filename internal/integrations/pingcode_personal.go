package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type PingCodePersonalInput struct {
	ConnectionID string `json:"connection_id"`
	Version      uint64 `json:"version"`
	Token        string `json:"token"`
}

type PingCodePersonalGrant struct {
	ConnectionID string
	Version      uint64
	CompanyID    string
	UserID       string
	Name         string
	token        string
	transport    http.RoundTripper
}

type pingCodePersonalSecret struct {
	Token     string `json:"token"`
	CompanyID string `json:"company_id"`
	UserID    string `json:"user_id"`
}

func (s Service) PreparePingCodePersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in PingCodePersonalInput) (PingCodePersonalGrant, error) {
	var grant PingCodePersonalGrant
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return grant, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return grant, ErrForbidden
	}
	if !membership.ValidateUUID(in.ConnectionID) || in.Token == "" || len(in.Token) > 4096 || strings.ContainsAny(in.Token, "\r\n") {
		return grant, ErrInvalidRequest
	}
	if s.Box == nil {
		return grant, ErrSecretsUnavailable
	}
	var company string
	err := tx.QueryRow(ctx, `SELECT external_id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='pingcode' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, in.ConnectionID).Scan(&company)
	if errors.Is(err, pgx.ErrNoRows) {
		return grant, ErrNotFound
	}
	if err != nil {
		return grant, err
	}
	if !companyPattern.MatchString(company) {
		return grant, ErrNeedsReauth
	}
	grant.ConnectionID = in.ConnectionID
	grant.CompanyID = company
	grant.token = in.Token
	return grant, nil
}

func VerifyPingCodePersonal(ctx context.Context, grant PingCodePersonalGrant) (PingCodePersonalGrant, error) {
	var user providerIdentity
	if err := grant.request(ctx, http.MethodGet, "/myself", nil, &user); err != nil {
		return grant, err
	}
	if !companyPattern.MatchString(user.ID) {
		return grant, ErrNeedsReauth
	}
	var team providerIdentity
	if err := grant.request(ctx, http.MethodGet, "/directory/team", nil, &team); err != nil {
		return grant, err
	}
	if team.ID != grant.CompanyID {
		return grant, ErrNeedsReauth
	}
	grant.UserID = user.ID
	grant.Name = strings.TrimSpace(user.DisplayName)
	if grant.Name == "" || len(grant.Name) > 200 {
		grant.Name = strings.TrimSpace(user.Name)
	}
	if grant.Name == "" || len(grant.Name) > 200 {
		grant.Name = strings.TrimSpace(user.Username)
	}
	if grant.Name == "" || len(grant.Name) > 200 {
		grant.Name = user.ID
	}
	return grant, nil
}

func (s Service) SavePingCodePersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in PingCodePersonalInput, grant PingCodePersonalGrant) (Connection, error) {
	current, err := s.PreparePingCodePersonal(ctx, tx, org, actor, in)
	if err != nil {
		return Connection{}, err
	}
	if current.ConnectionID != grant.ConnectionID || current.CompanyID != grant.CompanyID || current.token != grant.token || !companyPattern.MatchString(grant.UserID) || grant.Name == "" || len(grant.Name) > 200 {
		return Connection{}, ErrVersionConflict
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":pingcode-personal:"+in.ConnectionID); err != nil {
		return Connection{}, err
	}
	var id string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='pingcode' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id=$2::uuid AND status<>'DISCONNECTED' FOR UPDATE`, in.ConnectionID, actor.UserID).Scan(&id, &version)
	create := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !create {
		return Connection{}, err
	}
	if version != in.Version {
		return Connection{}, ErrVersionConflict
	}
	var inUse bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='pingcode' AND scope='PERSONAL' AND parent_connection_id=$1::uuid AND owner_user_id<>$2::uuid AND external_id=$3 AND status<>'DISCONNECTED')`, in.ConnectionID, actor.UserID, grant.UserID).Scan(&inUse); err != nil {
		return Connection{}, err
	}
	if inUse {
		return Connection{}, ErrVersionConflict
	}
	if create {
		id, err = membership.NewID()
		if err != nil {
			return Connection{}, err
		}
	}
	plain, err := json.Marshal(pingCodePersonalSecret{Token: grant.token, CompanyID: grant.CompanyID, UserID: grant.UserID})
	if err != nil {
		return Connection{}, err
	}
	sealed, key, err := s.seal(plain, connectionAAD(org, id))
	if err != nil {
		return Connection{}, err
	}
	if create {
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,credentials_sealed,credentials_key_id,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'pingcode','PERSONAL',$2::uuid,$3::uuid,$4,$5,'sync_comments',$6,$7,jsonb_build_object('auth_method','pingcode_personal','company_id',$8::text),$3::uuid)`, id, in.ConnectionID, actor.UserID, grant.UserID, grant.Name, sealed, key, grant.CompanyID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET external_id=$2,display_name=$3,credentials_sealed=$4,credentials_key_id=$5,account_kind='sync_comments',settings=jsonb_build_object('auth_method','pingcode_personal','company_id',$6::text),status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, grant.UserID, grant.Name, sealed, key, grant.CompanyID)
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}

func (s Service) LoadPingCodePersonal(ctx context.Context, tx pgx.Tx, org, parent, author string) (PingCodePersonalGrant, error) {
	var grant PingCodePersonalGrant
	var id, user, company string
	var sealed []byte
	err := tx.QueryRow(ctx, `SELECT c.id::text,c.external_id,p.external_id,c.credentials_sealed,c.version FROM openfox.integration_connections c JOIN openfox.organization_members m ON m.organization_id=c.organization_id AND m.user_id=c.owner_user_id JOIN openfox.integration_connections p ON p.organization_id=c.organization_id AND p.id=c.parent_connection_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.parent_connection_id=$1::uuid AND c.owner_user_id=$2::uuid AND c.provider='pingcode' AND c.scope='PERSONAL' AND c.account_kind='sync_comments' AND c.status='ACTIVE' AND c.settings->>'auth_method'='pingcode_personal' AND m.status='ACTIVE' AND m.role<>'GUEST' AND p.provider='pingcode' AND p.scope='ORGANIZATION' AND p.status='ACTIVE' FOR SHARE OF c,p,m`, parent, author).Scan(&id, &user, &company, &sealed, &grant.Version)
	if err != nil {
		return grant, err
	}
	plain, err := s.open(sealed, connectionAAD(org, id))
	if err != nil {
		return grant, err
	}
	var secret pingCodePersonalSecret
	if json.Unmarshal(plain, &secret) != nil || secret.Token == "" || secret.UserID != user || secret.CompanyID != company || !companyPattern.MatchString(secret.UserID) {
		return grant, ErrNeedsReauth
	}
	grant.ConnectionID = id
	grant.UserID = user
	grant.CompanyID = company
	grant.token = secret.Token
	return grant, nil
}

func (grant PingCodePersonalGrant) CreateComment(ctx context.Context, workItem, body string) (string, error) {
	if !companyPattern.MatchString(workItem) || strings.TrimSpace(body) == "" || len(body) > 64000 {
		return "", ErrInvalidRequest
	}
	var result struct {
		ID        string           `json:"id"`
		CreatedBy providerIdentity `json:"created_by"`
	}
	err := grant.request(ctx, http.MethodPost, "/comments", map[string]string{"content": body, "principal_type": "workitem", "principal_id": workItem}, &result)
	if err != nil {
		return "", err
	}
	if result.ID == "" || len(result.ID) > 255 || result.CreatedBy.ID != grant.UserID {
		return "", ErrUpstreamUnavailable
	}
	return result.ID, nil
}

func (grant PingCodePersonalGrant) request(ctx context.Context, method, path string, body any, result any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://open.pingcode.com/v2"+path, payload)
	if err != nil {
		return ErrInvalidRequest
	}
	req.Header.Set("Authorization", "Bearer "+grant.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Transport: grant.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrNeedsReauth
	}
	if resp.StatusCode != http.StatusOK && (method != http.MethodPost || resp.StatusCode != http.StatusCreated) {
		return ErrUpstreamUnavailable
	}
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit || json.Unmarshal(raw, result) != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}
