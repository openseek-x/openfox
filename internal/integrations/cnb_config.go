package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/cnb"
	"openfox/internal/membership"
)

const ProviderCNB = "cnb"

var defaultCNBClient = cnb.New()

func (s Service) cnbClient() *cnb.Client {
	if s.CNB != nil {
		return s.CNB
	}
	return defaultCNBClient
}

// Optional fields preserve the original ProviderTokenInput sealed JSON and AAD.
type cnbCredentials struct {
	ProviderTokenInput
	WebhookToken  string `json:"webhook_token,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}
type CNBRepository struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}
type CNBAccess struct {
	ID           string
	Version      uint64
	CreatedBy    string
	Status       string
	Repositories []CNBRepository
	Client       *cnb.Client
	credentials  cnbCredentials
	settings     map[string]json.RawMessage
}

func (a CNBAccess) Selected(id, path string) bool {
	for _, r := range a.Repositories {
		if r.ID == id && r.Path == path {
			return true
		}
	}
	return false
}
func (a CNBAccess) List(ctx context.Context, q string, page int) ([]RepositoryItem, bool, error) {
	rows, more, err := a.Client.Repositories(ctx, a.credentials.Secret, q, page)
	items := make([]RepositoryItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, RepositoryItem{FullName: r.Path, Name: r.Name, Private: r.Private(), HTMLURL: r.URL(), ExternalID: r.ID})
	}
	return items, more, err
}
func (a CNBAccess) ValidateRepositories(ctx context.Context, repos []CNBRepository) error {
	if len(repos) > 20 {
		return ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	seen := map[string]bool{}
	for _, r := range repos {
		if r.ID == "" || len(r.ID) > 255 || !cnb.ValidPath(r.Path) || seen[r.ID] {
			return ErrInvalidRequest
		}
		seen[r.ID] = true
		actual, err := a.Client.Repository(ctx, a.credentials.Secret, r.Path)
		if err != nil {
			return err
		}
		if actual.ID != r.ID {
			return ErrInvalidRepository
		}
	}
	return nil
}

func (s Service) loadCNB(ctx context.Context, tx pgx.Tx, org, id string, lock bool) (CNBAccess, error) {
	if !membership.ValidateUUID(id) {
		return CNBAccess{}, ErrNotFound
	}
	q := `SELECT id::text, version, created_by::text, status, settings, credentials_sealed FROM openfox.integration_connections
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='cnb' AND scope='ORGANIZATION' AND status<>'DISCONNECTED'`
	if lock {
		q += " FOR UPDATE"
	}
	var out CNBAccess
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
	if json.Unmarshal(plain, &out.credentials) != nil || !out.credentials.validate(ProviderCNB) {
		return out, ErrSecretsUnavailable
	}
	if json.Unmarshal(settings, &out.settings) != nil {
		return out, ErrInvalidRequest
	}
	if raw := out.settings["cnb_repositories"]; len(raw) > 0 && json.Unmarshal(raw, &out.Repositories) != nil {
		return out, ErrInvalidRequest
	}
	if out.Repositories == nil {
		out.Repositories = []CNBRepository{}
	}
	out.Client = s.cnbClient()
	return out, nil
}
func (s Service) PrepareCNB(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (CNBAccess, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return CNBAccess{}, err
	}
	return s.loadCNB(ctx, tx, org, id, false)
}

type CNBWorkSummary struct {
	RepositoryID   string     `json:"repository_id"`
	RepositoryPath string     `json:"repository_path"`
	Number         int        `json:"number"`
	Terminal       bool       `json:"terminal"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`
	NextCheckAt    *time.Time `json:"next_check_at"`
	ErrorCode      string     `json:"error_code"`
	BacklinkState  string     `json:"backlink_state"`
}
type CNBConfiguration struct {
	Version      uint64           `json:"version"`
	Repositories []CNBRepository  `json:"repositories"`
	Work         []CNBWorkSummary `json:"work"`
	HasMore      bool             `json:"has_more"`
}

func (s Service) GetCNBConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (CNBConfiguration, error) {
	a, err := s.PrepareCNB(ctx, tx, org, actor, id)
	if err != nil {
		return CNBConfiguration{}, err
	}
	out := CNBConfiguration{Version: a.Version, Repositories: a.Repositories, Work: []CNBWorkSummary{}}
	rows, err := tx.Query(ctx, `SELECT repository_id,repository_path,number,terminal,last_checked_at,next_check_at,last_error_code,backlink_state
	 FROM openfox.integration_cnb_pr_work WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid ORDER BY updated_at DESC,id LIMIT 51`, a.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var row CNBWorkSummary
		if err := rows.Scan(&row.RepositoryID, &row.RepositoryPath, &row.Number, &row.Terminal, &row.LastCheckedAt, &row.NextCheckAt, &row.ErrorCode, &row.BacklinkState); err != nil {
			return out, err
		}
		out.Work = append(out.Work, row)
	}
	if len(out.Work) > 50 {
		out.HasMore = true
		out.Work = out.Work[:50]
	}
	return out, rows.Err()
}

// lockCNBConnections takes connection locks in UUID order, including inactive
// owners of selected repository work, before any work row can be transferred.
// Include active owners too: eligibility may change while waiting for their lock.
func lockCNBConnections(ctx context.Context, tx pgx.Tx, id string, repos []CNBRepository) ([]string, error) {
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text FROM openfox.integration_connections c
	 WHERE c.organization_id=openfox_private.current_organization_id() AND (c.id=$1::uuid OR (c.provider='cnb'
	 AND EXISTS(SELECT 1 FROM openfox.integration_cnb_pr_work w WHERE w.organization_id=c.organization_id AND w.connection_id=c.id AND w.repository_id=ANY($2::text[]))))
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
func (s Service) SaveCNBRepositories(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, prepared CNBAccess, version uint64, repos []CNBRepository) (CNBConfiguration, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return CNBConfiguration{}, err
	}
	if version == 0 || len(repos) > 20 {
		return CNBConfiguration{}, ErrInvalidRequest
	}
	locked, err := lockCNBConnections(ctx, tx, prepared.ID, repos)
	if err != nil {
		return CNBConfiguration{}, err
	}
	a, err := s.loadCNB(ctx, tx, org, prepared.ID, false)
	if err != nil {
		return CNBConfiguration{}, err
	}
	if a.Version != version || prepared.Version != version {
		return CNBConfiguration{}, ErrVersionConflict
	}
	if a.Status != StatusActive {
		return CNBConfiguration{}, ErrNeedsReauth
	}
	if repos == nil {
		repos = []CNBRepository{}
	}
	raw, _ := json.Marshal(repos)
	a.settings["cnb_repositories"] = raw
	settings, _ := json.Marshal(a.settings)
	if len(settings) > 16000 {
		return CNBConfiguration{}, ErrInvalidRequest
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings=$2::jsonb,version=version+1,updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, settings); err != nil {
		return CNBConfiguration{}, err
	}
	// Removed selections stop work without deleting historical links. Explicit
	// re-selection can revive paused work; uncertain comments stay uncertain.
	ids := make([]string, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_cnb_pr_work SET next_check_at=NULL,last_error_code='repository_disabled',updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND NOT(repository_id=ANY($2::text[]))`, a.ID, ids); err != nil {
		return CNBConfiguration{}, err
	}
	for _, r := range repos {
		_, err := tx.Exec(ctx, `UPDATE openfox.integration_cnb_pr_work w SET connection_id=$1::uuid,repository_path=$3,requested_at=clock_timestamp(),next_check_at=clock_timestamp(),consecutive_failures=0,last_error_code='',updated_at=clock_timestamp(),
        lease_owner=CASE WHEN w.connection_id<>$1::uuid THEN '' ELSE w.lease_owner END,
        lease_token=CASE WHEN w.connection_id<>$1::uuid THEN NULL ELSE w.lease_token END,
        lease_expires_at=CASE WHEN w.connection_id<>$1::uuid THEN NULL ELSE w.lease_expires_at END,
        backlink_state=CASE WHEN w.connection_id<>$1::uuid AND w.backlink_state='SENDING' THEN 'UNKNOWN' ELSE w.backlink_state END
		 WHERE w.organization_id=openfox_private.current_organization_id() AND w.repository_id=$2 AND w.connection_id=ANY($4::uuid[])
		 AND (w.connection_id=$1::uuid OR EXISTS(SELECT 1 FROM openfox.integration_connections c WHERE c.organization_id=w.organization_id AND c.id=w.connection_id
          AND (c.status<>'ACTIVE' OR NOT(COALESCE(c.settings->'cnb_repositories','[]'::jsonb) @> jsonb_build_array(jsonb_build_object('id',w.repository_id))))))`, a.ID, r.ID, r.Path, locked)
		if err != nil {
			return CNBConfiguration{}, err
		}
	}
	return s.GetCNBConfiguration(ctx, tx, org, actor, a.ID)
}
func (s Service) ReconcileCNB(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, repo string, number int) error {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return err
	}
	a, err := s.loadCNB(ctx, tx, org, id, true)
	if err != nil {
		return err
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
	tag, err := tx.Exec(ctx, `UPDATE openfox.integration_cnb_pr_work SET requested_at=clock_timestamp(),next_check_at=clock_timestamp(),consecutive_failures=0,last_error_code='',updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND repository_id=$2 AND repository_path=$3 AND number=$4`, a.ID, repo, path, number)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s Service) rotateCNBWebhook(ctx context.Context, tx pgx.Tx, org, id string) (Webhook, error) {
	a, err := s.loadCNB(ctx, tx, org, id, false)
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

// No token, user profile, PR body or unrestricted environment expansion is sent.
func cnbBridgeTemplate(payloadURL string) string {
	return `# Add to a protected target branch. Store OPENFOX_WEBHOOK_SECRET in a CNB secret.
# Official plugin v1.0.2; no cron is installed. Private PR backlinks can post comments.
$:
  pull_request.target:
    - stages:
        - name: OpenFox PR event
          image: cnbcool/webhook@sha256:fc7b7375cb7581729f6d7dc955364bb773ca54bb1c429902e2924eb068800bd4
          settings: &openfox
            urls: ` + strings.TrimSpace(payloadURL) + `
            signature_secret: ${OPENFOX_WEBHOOK_SECRET}
            signature_header: X-CNB-Signature
            debug: false
            skip_verify: false
            template: |
              {"event":"{{ CNB_EVENT }}","repository":"{{ CNB_REPO_SLUG }}","repository_id":"{{ CNB_REPO_ID }}","build_id":"{{ CNB_BUILD_ID }}","number":"{{ CNB_PULL_REQUEST_IID }}","before":"{{ CNB_BEFORE_SHA }}","head":"{{ CNB_COMMIT }}"}
  pull_request.merged:
    - stages:
        - name: OpenFox merged event
          image: cnbcool/webhook@sha256:fc7b7375cb7581729f6d7dc955364bb773ca54bb1c429902e2924eb068800bd4
          settings: *openfox
  push:
    - stages:
        - name: OpenFox commit event
          image: cnbcool/webhook@sha256:fc7b7375cb7581729f6d7dc955364bb773ca54bb1c429902e2924eb068800bd4
          settings: *openfox
`
}

// Network failures from an older credential must not revoke a renewed connection.
func (s Service) MarkCNBTokenRevoked(ctx context.Context, tx pgx.Tx, id string, version uint64) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='token_revoked',version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='cnb' AND status='ACTIVE' AND version=$2`, id, version)
	return err
}
