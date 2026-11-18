package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"openfox/internal/platform/postgres"
	"openfox/internal/runtimepolicy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

const MaxRuntimeEnvironment = 100

var ErrSecretsUnavailable = errors.New("devices: secrets unavailable")

type RuntimeEnvironmentMetadata struct {
	Name      string    `json:"name"`
	HasValue  bool      `json:"has_value"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}
type RuntimeDefaults struct {
	Model            *string                      `json:"model"`
	Version          int64                        `json:"version"`
	EnvVars          []RuntimeEnvironmentMetadata `json:"env_vars"`
	SecretsAvailable bool                         `json:"secrets_available"`
}
type RuntimeDefaultsPatch struct {
	ExpectedVersion *int64             `json:"expected_version"`
	Model           json.RawMessage    `json:"model,omitempty"`
	SetEnv          map[string]*string `json:"set_env,omitempty"`
	RemoveEnv       []string           `json:"remove_env,omitempty"`
}
type RuntimeDefaultsStore interface {
	GetRuntimeDefaults(context.Context, tenancy.OrganizationSelection, string, string) (RuntimeDefaults, error)
	PatchRuntimeDefaults(context.Context, tenancy.OrganizationSelection, string, string, RuntimeDefaultsPatch) (RuntimeDefaults, error)
}

// Lock the physical device before its grant and defaults row. The same locks
// serialize revocation/removal with a settings write; no physical data is changed.
func requireRuntimeDefaultsOwner(ctx context.Context, tx pgx.Tx, deviceID string) (json.RawMessage, error) {
	var capabilities json.RawMessage
	err := tx.QueryRow(ctx, `SELECT d.capabilities FROM openfox.physical_devices d
 WHERE d.id=$1::uuid AND d.owner_user_id=openfox_private.current_actor_id() AND d.status <> 'REVOKED'
 FOR UPDATE OF d`, deviceID).Scan(&capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	var grantID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.device_grants
 WHERE organization_id=openfox_private.current_organization_id() AND device_id=$1::uuid AND status='ACTIVE'
 FOR SHARE`, deviceID).Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDeviceNotFound
	}
	return capabilities, err
}

func (s PostgresStore) GetRuntimeDefaults(ctx context.Context, selection tenancy.OrganizationSelection, deviceID, runtime string) (RuntimeDefaults, error) {
	if s.Pool == nil {
		return RuntimeDefaults{}, postgres.ErrNilPool
	}
	if validateSelection(selection) != nil || !runtimepolicy.ValidProvider(runtime) {
		return RuntimeDefaults{}, ErrInvalidRequest
	}
	id, err := normalizeUUID(deviceID)
	if err != nil {
		return RuntimeDefaults{}, ErrDeviceNotFound
	}
	var result RuntimeDefaults
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := requireRuntimeDefaultsOwner(ctx, tx, id); err != nil {
			return err
		}
		result, err = s.loadRuntimeDefaults(ctx, tx, id, runtime)
		return err
	})
	return result, err
}

func (s PostgresStore) loadRuntimeDefaults(ctx context.Context, tx pgx.Tx, deviceID, runtime string) (RuntimeDefaults, error) {
	result := RuntimeDefaults{EnvVars: []RuntimeEnvironmentMetadata{}, SecretsAvailable: s.Secrets != nil}
	err := tx.QueryRow(ctx, `SELECT model,version FROM openfox.device_runtime_defaults
 WHERE organization_id=openfox_private.current_organization_id() AND device_id=$1::uuid AND runtime=$2`, deviceID, runtime).Scan(&result.Model, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT name,version,updated_at FROM openfox.device_runtime_env_vars
 WHERE organization_id=openfox_private.current_organization_id() AND device_id=$1::uuid AND runtime=$2 ORDER BY name LIMIT 101`, deviceID, runtime)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		item := RuntimeEnvironmentMetadata{HasValue: true}
		if err := rows.Scan(&item.Name, &item.Version, &item.UpdatedAt); err != nil {
			return result, err
		}
		result.EnvVars = append(result.EnvVars, item)
	}
	return result, rows.Err()
}

func (s PostgresStore) PatchRuntimeDefaults(ctx context.Context, selection tenancy.OrganizationSelection, deviceID, runtime string, input RuntimeDefaultsPatch) (RuntimeDefaults, error) {
	if s.Pool == nil {
		return RuntimeDefaults{}, postgres.ErrNilPool
	}
	if validateSelection(selection) != nil || !runtimepolicy.ValidProvider(runtime) || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 || (len(input.Model) == 0 && len(input.SetEnv) == 0 && len(input.RemoveEnv) == 0) || len(input.SetEnv) > MaxRuntimeEnvironment || len(input.RemoveEnv) > MaxRuntimeEnvironment {
		return RuntimeDefaults{}, ErrInvalidRequest
	}
	id, err := normalizeUUID(deviceID)
	if err != nil {
		return RuntimeDefaults{}, ErrDeviceNotFound
	}
	var model *string
	if len(input.Model) > 0 {
		if json.Unmarshal(input.Model, &model) != nil || (model != nil && !runtimepolicy.ValidModel(*model)) {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if (runtime == runtimepolicy.CodeBuddy || runtime == runtimepolicy.Hermes) && model != nil && *model == "auto" {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if runtime == runtimepolicy.Gemini && model != nil && *model != runtimepolicy.DefaultModel && !runtimepolicy.ValidGeminiModel(*model) {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if model != nil && *model == runtimepolicy.DefaultModel {
			model = nil
		}
	}
	for name, value := range input.SetEnv {
		if runtime == runtimepolicy.Gemini && !runtimepolicy.GeminiEnvironmentAllowed(name) {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if runtime == runtimepolicy.Hermes && !runtimepolicy.HermesEnvironmentAllowed(name) {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if runtime == runtimepolicy.CodeBuddy && !runtimepolicy.CodeBuddyEnvironmentAllowed(name) {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if !runtimepolicy.EnvironmentNamePattern.MatchString(name) || runtimepolicy.ReservedEnvironment(name) || value == nil || len(*value) > secrets.MaxPlaintext || strings.IndexFunc(*value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) >= 0 {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
	}
	seen := map[string]bool{}
	for _, name := range input.RemoveEnv {
		if !runtimepolicy.EnvironmentNamePattern.MatchString(name) || seen[name] {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		if _, ok := input.SetEnv[name]; ok {
			return RuntimeDefaults{}, ErrInvalidRequest
		}
		seen[name] = true
	}
	var result RuntimeDefaults
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		capabilities, err := requireRuntimeDefaultsOwner(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(input.SetEnv) > 0 && s.Secrets == nil {
			return ErrSecretsUnavailable
		}
		if model != nil {
			if cap, ok := runtimepolicy.Find(runtimepolicy.ParseCapabilities(capabilities), runtime); ok && !cap.HasModel(*model) {
				return ErrInvalidRequest
			}
		}
		current, err := s.loadRuntimeDefaults(ctx, tx, id, runtime)
		if err != nil {
			return err
		}
		if current.Version != *input.ExpectedVersion {
			return ErrVersionConflict
		}
		names := map[string]bool{}
		for _, item := range current.EnvVars {
			names[item.Name] = true
		}
		for _, name := range input.RemoveEnv {
			delete(names, name)
		}
		for name := range input.SetEnv {
			names[name] = true
		}
		if len(names) > MaxRuntimeEnvironment {
			return ErrInvalidRequest
		}
		if len(input.Model) == 0 {
			model = current.Model
		}
		var version int64
		err = tx.QueryRow(ctx, `INSERT INTO openfox.device_runtime_defaults(organization_id,device_id,runtime,model,version,updated_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,1,openfox_private.current_actor_id())
 ON CONFLICT(organization_id,device_id,runtime) DO UPDATE SET model=EXCLUDED.model,version=device_runtime_defaults.version+1,updated_by=EXCLUDED.updated_by,updated_at=clock_timestamp()
 WHERE device_runtime_defaults.version=$4 RETURNING version`, id, runtime, model, *input.ExpectedVersion).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		if err != nil {
			return err
		}
		for name, value := range input.SetEnv {
			sealed, err := s.Secrets.Seal([]byte(*value), secrets.DeviceRuntimeEnvAAD(selection.OrganizationID, id, runtime, name))
			if err != nil {
				return ErrSecretsUnavailable
			}
			if _, err = tx.Exec(ctx, `INSERT INTO openfox.device_runtime_env_vars(organization_id,device_id,runtime,name,value_ciphertext,value_nonce,key_id,version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(organization_id,device_id,runtime,name) DO UPDATE SET value_ciphertext=EXCLUDED.value_ciphertext,value_nonce=EXCLUDED.value_nonce,key_id=EXCLUDED.key_id,version=EXCLUDED.version,updated_at=clock_timestamp()`, id, runtime, name, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, version); err != nil {
				return err
			}
		}
		if len(input.RemoveEnv) > 0 {
			if _, err = tx.Exec(ctx, `DELETE FROM openfox.device_runtime_env_vars WHERE organization_id=openfox_private.current_organization_id() AND device_id=$1::uuid AND runtime=$2 AND name=ANY($3::text[])`, id, runtime, input.RemoveEnv); err != nil {
				return err
			}
		}
		if err = recordAudit(ctx, tx, selection, id, "device.runtime_defaults_updated", map[string]string{"source": runtime, "version": formatInt(version)}); err != nil {
			return err
		}
		result, err = s.loadRuntimeDefaults(ctx, tx, id, runtime)
		return err
	})
	return result, err
}

// Reject duplicate JSON properties before map decoding can silently discard a
// value. This endpoint also rejects unknown fields and null environment values.
func (p *RuntimeDefaultsPatch) UnmarshalJSON(data []byte) error {
	fields, err := uniqueRuntimeObject(data)
	if err != nil {
		return err
	}
	if raw, ok := fields["set_env"]; ok {
		if _, err := uniqueRuntimeObject(raw); err != nil {
			return err
		}
	}
	type plain RuntimeDefaultsPatch
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return ErrInvalidRequest
	}
	*p = RuntimeDefaultsPatch(value)
	return nil
}
func uniqueRuntimeObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalidRequest
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalidRequest
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrInvalidRequest
		}
		if _, ok := fields[key]; ok {
			return nil, ErrInvalidRequest
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrInvalidRequest
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, ErrInvalidRequest
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalidRequest
	}
	return fields, nil
}
