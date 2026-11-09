package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

type ShortcutHook struct {
	URL          string     `json:"url"`
	Enabled      bool       `json:"enabled"`
	Version      uint64     `json:"version"`
	LastDelivery *time.Time `json:"last_delivery,omitempty"`
}
type shortcutCredentials struct {
	ProviderTokenInput
	SigningSecret string `json:"shortcut_hook_secret,omitempty"`
}

func (s Service) shortcutHookCredentials(ctx context.Context, tx pgx.Tx, org, id string, lock bool) (ShortcutHook, shortcutCredentials, error) {
	var h ShortcutHook
	var c shortcutCredentials
	var blob []byte
	if !membership.ValidateUUID(id) {
		return h, c, ErrNotFound
	}
	query := `SELECT credentials_sealed,version,webhook_last_delivery_at FROM openfox.integration_connections
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND scope='ORGANIZATION' AND provider='shortcut' AND status='ACTIVE'`
	if lock {
		query += " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, query, id).Scan(&blob, &h.Version, &h.LastDelivery)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, c, ErrNotFound
	}
	if err != nil {
		return h, c, err
	}
	plain, err := s.open(blob, connectionAAD(org, id))
	if err != nil {
		return h, c, err
	}
	if json.Unmarshal(plain, &c) != nil || !c.validate("shortcut") {
		return h, c, ErrNeedsReauth
	}
	h.Enabled = c.SigningSecret != ""
	if u, err := url.Parse(s.PublicURL); err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
		h.URL = strings.TrimRight(s.PublicURL, "/") + "/api/v1/hooks/shortcut/" + org + "/" + id
	}
	return h, c, nil
}

// A nil secret means read. An explicitly empty secret revokes push delivery;
// polling keeps its own version and schedule.
func (s Service) ShortcutHookSettings(ctx context.Context, tx pgx.Tx, org, id string, actor collaboration.Actor, version uint64, secret *string) (ShortcutHook, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return ShortcutHook{}, err
	}
	if !CanManage(actor.Role) {
		return ShortcutHook{}, ErrForbidden
	}
	h, c, err := s.shortcutHookCredentials(ctx, tx, org, id, secret != nil)
	if err != nil || secret == nil {
		return h, err
	}
	if version != h.Version {
		return h, ErrVersionConflict
	}
	if *secret != "" && (h.URL == "" || len(*secret) < 16 || len(*secret) > 256 || strings.ContainsAny(*secret, "\r\n")) {
		return h, ErrInvalidRequest
	}
	c.SigningSecret = *secret
	raw, err := json.Marshal(c)
	if err != nil {
		return h, err
	}
	blob, key, err := s.seal(raw, connectionAAD(org, id))
	if err != nil {
		return h, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,version=version+1,updated_at=clock_timestamp()
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$4`, id, blob, key, version)
	if err != nil {
		return h, err
	}
	if tag.RowsAffected() != 1 {
		return h, ErrVersionConflict
	}
	h.Version++
	h.Enabled = *secret != ""
	return h, nil
}

// Events only request a fresh provider read. Provider payload values never
// bypass the normal project/space checks, field reconciliation or pause state.
func (s Service) ReceiveShortcutHook(ctx context.Context, tx pgx.Tx, org, id, sig string, body []byte) error {
	h, c, err := s.shortcutHookCredentials(ctx, tx, org, id, true)
	if err != nil {
		return err
	}
	signature, err := hex.DecodeString(sig)
	mac := hmac.New(sha256.New, []byte(c.SigningSecret))
	mac.Write(body)
	if !h.Enabled || err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return ErrForbidden
	}
	var event struct {
		ID      string
		Actions []struct {
			EntityType string `json:"entity_type"`
		}
	}
	if json.Unmarshal(body, &event) != nil || !membership.ValidateUUID(event.ID) || len(event.Actions) > 5000 {
		return ErrInvalidRequest
	}
	status := "IGNORED"
	for _, a := range event.Actions {
		if a.EntityType == "story" || a.EntityType == "epic" {
			status = "PROCESSED"
			break
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,signature_status,status)
         VALUES(openfox_private.current_organization_id(),$1::uuid,'shortcut',$2,'project_change','VERIFIED',$3) ON CONFLICT(organization_id,connection_id,delivery_id) DO NOTHING`, id, event.ID, status)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if status == "PROCESSED" {
		if _, err = tx.Exec(ctx, `UPDATE openfox.import_sync_links SET next_check=LEAST(next_check,clock_timestamp()) WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state='ACTIVE' AND lease IS NULL`, id); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET webhook_last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return err
}
