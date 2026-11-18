package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codeup"
	"openfox/internal/membership"
)

const ProviderCodeup = "codeup"

var defaultCodeupClient = codeup.New()

func (s Service) codeupClient() *codeup.Client {
	if s.Codeup != nil {
		return s.Codeup
	}
	return defaultCodeupClient
}

// Optional fields preserve the original ProviderTokenInput sealed JSON and AAD.
type codeupCredentials struct {
	ProviderTokenInput
	WebhookToken  string `json:"webhook_token,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}
type CodeupRepository struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type CodeupAccess struct {
	ID           string
	Version      uint64
	CreatedBy    string
	Status       string
	Repositories []CodeupRepository
	Client       *codeup.Client
	credentials  codeupCredentials
	settings     map[string]json.RawMessage
}

func (a CodeupAccess) Selected(id, path string) bool {
	for _, r := range a.Repositories {
		if r.ID == id && r.Path == path {
			return true
		}
	}
	return false
}
func (a CodeupAccess) List(ctx context.Context, q string, page int) ([]RepositoryItem, bool, error) {
	rows, more, err := a.Client.Repositories(ctx, a.credentials.Secret, a.credentials.CompanyID, q, page)
	items := make([]RepositoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, RepositoryItem{FullName: r.Path, Name: r.Name, Private: r.Private(), HTMLURL: r.URL(), ExternalID: string(r.ID)})
	}
	return items, more, err
}
func (a CodeupAccess) ValidateRepositories(ctx context.Context, repos []CodeupRepository) error {
	if len(repos) > 20 {
		return ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	seen := map[string]bool{}
	for _, r := range repos {
		if !codeup.ValidID(r.ID) || !codeup.ValidPath(r.Path) || seen[r.ID] {
			return ErrInvalidRequest
		}
		seen[r.ID] = true
		actual, err := a.Client.Repository(ctx, a.credentials.Secret, a.credentials.CompanyID, r.ID)
		if err != nil {
			return err
		}
		if string(actual.ID) != r.ID || actual.Path != r.Path {
			return ErrInvalidRepository
		}
	}
	return nil
}

func (s Service) loadCodeup(ctx context.Context, tx pgx.Tx, org, id string, lock bool) (CodeupAccess, error) {
	if !membership.ValidateUUID(id) {
		return CodeupAccess{}, ErrNotFound
	}
	q := `SELECT id::text, version, created_by::text, status, settings, credentials_sealed FROM openfox.integration_connections
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='codeup' AND scope='ORGANIZATION' AND status<>'DISCONNECTED'`
	if lock {
		q += " FOR UPDATE"
	}
	var out CodeupAccess
	var sealed, settings []byte
	if err := tx.QueryRow(ctx, q, id).Scan(&out.ID, &out.Version, &out.CreatedBy, &out.Status, &settings, &sealed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrNotFound
		}
		return out, err
	}
	plain, err := s.open(sealed, connectionAAD(org, out.ID))
	if err != nil {
		return out, err
	}
	if json.Unmarshal(plain, &out.credentials) != nil || !out.credentials.validate(ProviderCodeup) {
		return out, ErrSecretsUnavailable
	}
	if json.Unmarshal(settings, &out.settings) != nil {
		return out, ErrInvalidRequest
	}
	if raw := out.settings["codeup_repositories"]; len(raw) > 0 && json.Unmarshal(raw, &out.Repositories) != nil {
		return out, ErrInvalidRequest
	}
	if out.Repositories == nil {
		out.Repositories = []CodeupRepository{}
	}
	out.Client = s.codeupClient()
	return out, nil
}
func (s Service) PrepareCodeup(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (CodeupAccess, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return CodeupAccess{}, err
	}
	return s.loadCodeup(ctx, tx, org, id, false)
}

type CodeupWorkSummary struct {
	RepositoryID   string     `json:"repository_id"`
	RepositoryPath string     `json:"repository_path"`
	Number         int        `json:"number"`
	Terminal       bool       `json:"terminal"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`
	NextCheckAt    *time.Time `json:"next_check_at"`
	ErrorCode      string     `json:"error_code"`
	BacklinkState  string     `json:"backlink_state"`
}
type CodeupConfiguration struct {
	Version      uint64              `json:"version"`
	Repositories []CodeupRepository  `json:"repositories"`
	Work         []CodeupWorkSummary `json:"work"`
	HasMore      bool                `json:"has_more"`
	NextCursor   string              `json:"next_cursor"`
}

func (s Service) GetCodeupConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (CodeupConfiguration, error) {
	a, err := s.PrepareCodeup(ctx, tx, org, actor, id)
	if err != nil {
		return CodeupConfiguration{}, err
	}
	page, err := listCodeupWork(ctx, tx, a.ID, "", 50)
	return CodeupConfiguration{Version: a.Version, Repositories: a.Repositories, Work: page.Work, HasMore: page.HasMore, NextCursor: page.NextCursor}, err
}

type CodeupWorkPage struct {
	Work       []CodeupWorkSummary `json:"work"`
	HasMore    bool                `json:"has_more"`
	NextCursor string              `json:"next_cursor"`
}
type codeupWorkCursor struct {
	Created time.Time `json:"created"`
	ID      string    `json:"id"`
}

func (s Service) ListCodeupWork(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, after string, limit int) (CodeupWorkPage, error) {
	a, err := s.PrepareCodeup(ctx, tx, org, actor, id)
	if err != nil {
		return CodeupWorkPage{}, err
	}
	return listCodeupWork(ctx, tx, a.ID, after, limit)
}
func listCodeupWork(ctx context.Context, tx pgx.Tx, id, after string, limit int) (CodeupWorkPage, error) {
	out := CodeupWorkPage{Work: []CodeupWorkSummary{}}
	if limit < 1 || limit > 50 || len(after) > 256 {
		return out, ErrInvalidRequest
	}
	var cursor codeupWorkCursor
	if after != "" {
		raw, err := base64.RawURLEncoding.DecodeString(after)
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Created.IsZero() || !membership.ValidateUUID(cursor.ID) {
			return out, ErrInvalidRequest
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text,created_at,repository_id,repository_path,number,terminal,last_checked_at,next_check_at,last_error_code,backlink_state
 FROM openfox.integration_codeup_mr_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid
 AND ($2='' OR (created_at,id)<($3,$4::uuid)) ORDER BY created_at DESC,id DESC LIMIT $5`, id, after, cursor.Created, nullableCodeupCursorID(cursor.ID), limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var row CodeupWorkSummary
		var c codeupWorkCursor
		if err := rows.Scan(&c.ID, &c.Created, &row.RepositoryID, &row.RepositoryPath, &row.Number, &row.Terminal, &row.LastCheckedAt, &row.NextCheckAt, &row.ErrorCode, &row.BacklinkState); err != nil {
			return out, err
		}
		if len(out.Work) == limit {
			out.HasMore = true
			break
		}
		out.Work = append(out.Work, row)
		raw, _ := json.Marshal(c)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if !out.HasMore {
		out.NextCursor = ""
	}
	return out, rows.Err()
}
func nullableCodeupCursorID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// lockCodeupConnections takes connection locks in UUID order, including inactive
// owners of selected repository work, before any work row can be transferred.
// Include active owners too: eligibility may change while waiting for their lock.
func lockCodeupConnections(ctx context.Context, tx pgx.Tx, id string, repos []CodeupRepository) ([]string, error) {
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text FROM openfox.integration_connections c
	 WHERE c.organization_id=openfox_private.current_organization_id() AND (c.id=$1::uuid OR (c.provider='codeup'
	 AND EXISTS(SELECT 1 FROM openfox.integration_codeup_mr_work w WHERE w.organization_id=c.organization_id AND w.connection_id=c.id AND w.repository_id=ANY($2::text[]) AND w.cloud_organization_id=(SELECT settings->>'company_id' FROM openfox.integration_connections WHERE organization_id=c.organization_id AND id=$1::uuid))))
	 ORDER BY c.id FOR UPDATE`, id, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var locked []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		locked = append(locked, id)
	}
	return locked, rows.Err()
}
func (s Service) SaveCodeupRepositories(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, prepared CodeupAccess, version uint64, repos []CodeupRepository) (CodeupConfiguration, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return CodeupConfiguration{}, err
	}
	if version == 0 || len(repos) > 20 {
		return CodeupConfiguration{}, ErrInvalidRequest
	}
	locked, err := lockCodeupConnections(ctx, tx, prepared.ID, repos)
	if err != nil {
		return CodeupConfiguration{}, err
	}
	a, err := s.loadCodeup(ctx, tx, org, prepared.ID, false)
	if err != nil {
		return CodeupConfiguration{}, err
	}
	if a.Version != version || prepared.Version != version {
		return CodeupConfiguration{}, ErrVersionConflict
	}
	if a.Status != StatusActive {
		return CodeupConfiguration{}, ErrNeedsReauth
	}
	if repos == nil {
		repos = []CodeupRepository{}
	}
	raw, _ := json.Marshal(repos)
	a.settings["codeup_repositories"] = raw
	settings, _ := json.Marshal(a.settings)
	if len(settings) > 16000 {
		return CodeupConfiguration{}, ErrInvalidRequest
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings=$2::jsonb,version=version+1,updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, settings); err != nil {
		return CodeupConfiguration{}, err
	}
	// Removed selections stop work without deleting historical links. Explicit
	// re-selection can revive paused work; uncertain comments stay uncertain.
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET next_check_at=NULL,last_error_code='repository_disabled',updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND NOT(repository_id=ANY($2::text[]))`, a.ID, ids); err != nil {
		return CodeupConfiguration{}, err
	}
	for _, r := range repos {
		_, err := tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work w SET connection_id=$1::uuid,repository_path=$3,requested_at=clock_timestamp(),next_check_at=clock_timestamp(),consecutive_failures=0,last_error_code='',updated_at=clock_timestamp(),
        lease_owner=CASE WHEN w.connection_id<>$1::uuid THEN '' ELSE w.lease_owner END,
        lease_token=CASE WHEN w.connection_id<>$1::uuid THEN NULL ELSE w.lease_token END,
        lease_expires_at=CASE WHEN w.connection_id<>$1::uuid THEN NULL ELSE w.lease_expires_at END,
        backlink_state=CASE WHEN w.connection_id<>$1::uuid AND w.backlink_state='SENDING' THEN 'UNKNOWN' ELSE w.backlink_state END
		 WHERE w.organization_id=openfox_private.current_organization_id() AND w.repository_id=$2 AND w.cloud_organization_id=$5 AND w.connection_id=ANY($4::uuid[])
		 AND (w.connection_id=$1::uuid OR EXISTS(SELECT 1 FROM openfox.integration_connections c WHERE c.organization_id=w.organization_id AND c.id=w.connection_id
          AND (c.status<>'ACTIVE' OR NOT(COALESCE(c.settings->'codeup_repositories','[]'::jsonb) @> jsonb_build_array(jsonb_build_object('id',w.repository_id))))))`, a.ID, r.ID, r.Path, locked, a.credentials.CompanyID)
		if err != nil {
			return CodeupConfiguration{}, err
		}
	}
	return s.GetCodeupConfiguration(ctx, tx, org, actor, a.ID)
}
func (s Service) ReconcileCodeup(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, repo string, number int, version uint64) error {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return err
	}
	a, err := s.loadCodeup(ctx, tx, org, id, true)
	if err != nil {
		return err
	}
	if version == 0 || a.Version != version {
		return ErrVersionConflict
	}
	if a.Status != StatusActive {
		return ErrNeedsReauth
	}
	var path string
	for _, r := range a.Repositories {
		if r.ID == repo {
			path = r.Path
		}
	}
	if path == "" || number <= 0 {
		return ErrNotFound
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET requested_at=clock_timestamp(),next_check_at=clock_timestamp(),consecutive_failures=0,last_error_code='',updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND repository_id=$2 AND repository_path=$3 AND number=$4`, a.ID, repo, path, number)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s Service) rotateCodeupWebhook(ctx context.Context, tx pgx.Tx, org, id string) (Webhook, error) {
	a, err := s.loadCodeup(ctx, tx, org, id, false)
	if u, e := url.Parse(s.PublicURL); e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Webhook{}, ErrInvalidRequest
	}
	if err != nil {
		return Webhook{}, err
	}
	if a.Status != StatusActive {
		return Webhook{}, ErrNeedsReauth
	}
	a.credentials.WebhookToken, err = randomBase64URL(32)
	if err != nil {
		return Webhook{}, err
	}
	a.credentials.WebhookSecret, err = randomHex(32)
	if err != nil {
		return Webhook{}, err
	}
	plain, _ := json.Marshal(a.credentials)
	blob, key, err := s.seal(plain, connectionAAD(org, a.ID))
	if err != nil {
		return Webhook{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,webhook_token_hash=$4,webhook_last_delivery_at=NULL,version=version+1,updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, blob, key, TokenHash(a.credentials.WebhookToken))
	if err != nil {
		return Webhook{}, err
	}
	return s.loadWebhook(ctx, tx, org, a.ID, false)
}

// Network failures from an older credential must not revoke a renewed connection.
func (s Service) MarkCodeupTokenRevoked(ctx context.Context, tx pgx.Tx, id string, version uint64) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='token_revoked',version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='codeup' AND status='ACTIVE' AND version=$2`, id, version)
	return err
}

// SaveCodeupCredentials requires explicit same-identity CAS after upstream validation.
func (s Service) SaveCodeupCredentials(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64, input ProviderTokenInput, identity string) (Connection, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return Connection{}, err
	}
	if !input.validate(ProviderCodeup) || version == 0 {
		return Connection{}, ErrInvalidRequest
	}
	a, err := s.loadCodeup(ctx, tx, org, id, true)
	if err != nil {
		return Connection{}, err
	}
	if a.Version != version {
		return Connection{}, ErrVersionConflict
	}
	var existing string
	if err = tx.QueryRow(ctx, `SELECT external_id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID).Scan(&existing); err != nil {
		return Connection{}, err
	}
	if existing != identity || input.CompanyID != a.credentials.CompanyID {
		return Connection{}, ErrInvalidRequest
	}
	a.credentials.ProviderTokenInput = input
	raw, _ := json.Marshal(a.credentials)
	blob, key, err := s.seal(raw, connectionAAD(org, a.ID))
	if err != nil {
		return Connection{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, blob, key); err != nil {
		return Connection{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_codeup_mr_work SET requested_at=clock_timestamp(),next_check_at=clock_timestamp(),consecutive_failures=0,last_error_code='',lease_owner='',lease_token=NULL,lease_expires_at=NULL,backlink_state=CASE WHEN backlink_state='SENDING' THEN 'UNKNOWN' ELSE backlink_state END WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, a.ID); err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, a.ID)
}
