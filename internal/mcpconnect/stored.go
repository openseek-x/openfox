package mcpconnect

import (
	"encoding/json"
	"time"

	"openfox/internal/secrets"
)

// Stored contains only a redacted config in its JSON representation. Source/runtime metadata
// and sealed material are used internally after the caller's authorization check.
type Stored struct {
	ID         string `json:"id"`
	InstanceID string `json:"-"`
	Name       string `json:"name"`
	CatalogID  string `json:"catalog_id,omitempty"`
	Transport  string `json:"transport"`
	Version    int64  `json:"version"`
	Config     Config `json:"config"`
	Manifest
	Status      string     `json:"status"`
	DeviceID    string     `json:"-"`
	OwnerUserID string     `json:"-"`
	Runtime     string     `json:"-"`
	ExpiresAt   *time.Time `json:"-"`
	sealed      secrets.Sealed
}

const Columns = `id::text,instance_id::text,name,version,config,manifest,CASE WHEN expires_at<=clock_timestamp() THEN 'authorizing' ELSE status END,
 COALESCE(device_id::text,''),COALESCE(owner_user_id::text,''),COALESCE(runtime,''),expires_at,secret_ciphertext,secret_nonce,COALESCE(secret_key_id,'')`

func Scan(row interface{ Scan(...any) error }, extra ...any) (Stored, error) {
	var r Stored
	fields := []any{&r.ID, &r.InstanceID, &r.Name, &r.Version, &r.Config, &r.Manifest, &r.Status, &r.DeviceID, &r.OwnerUserID, &r.Runtime, &r.ExpiresAt, &r.sealed.Ciphertext, &r.sealed.Nonce, &r.sealed.KeyID}
	err := row.Scan(append(fields, extra...)...)
	r.CatalogID, r.Transport = r.Config.CatalogID, r.Config.Transport
	return r, err
}
func aad(org, agent, id string) string {
	return "agent-mcp-v1\x00" + org + "\x00" + agent + "\x00" + id
}
func (r Stored) Open(box *secrets.Box, org, agent string) (Config, error) {
	if len(r.sealed.Ciphertext) == 0 {
		return r.Config, nil
	}
	b, err := box.Open(r.sealed, aad(org, agent, r.ID))
	if err != nil {
		return Config{}, err
	}
	var c Config
	err = json.Unmarshal(b, &c)
	return c, err
}
func Seal(box *secrets.Box, org, agent, id string, c Config) (*secrets.Sealed, error) {
	if maskedURL(c.URL) == c.URL && c.Credential == "" && len(c.Args)+len(c.Env)+len(c.Headers) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	s, err := box.Seal(b, aad(org, agent, id))
	if err != nil {
		return nil, err
	}
	return &s, nil
}
