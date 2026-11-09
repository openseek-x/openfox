package devices

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/deviceauth"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

const (
	defaultRegistrationTTL = 10 * time.Minute
	maxRegistrationTTL     = 10 * time.Minute
	maxCapabilityBytes     = 64 * 1024
)

type PostgresStore struct {
	Pool *postgres.Pool
}

func (s PostgresStore) IssueRegistrationToken(ctx context.Context, selection tenancy.OrganizationSelection, input RegistrationTokenInput, expiresAt time.Time) (RegistrationTokenResult, error) {
	if s.Pool == nil {
		return RegistrationTokenResult{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return RegistrationTokenResult{}, ErrInvalidRequest
	}
	name, installID, maxSlots, err := normalizeRegistrationInput(input)
	if err != nil {
		return RegistrationTokenResult{}, err
	}
	now := time.Now().UTC()
	expiresAt = expiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(defaultRegistrationTTL)
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(maxRegistrationTTL)) {
		return RegistrationTokenResult{}, ErrInvalidRequest
	}
	scope := input.Scope
	if scope == "" {
		scope = ScopeOrganization
	}
	if scope != ScopePersonal && scope != ScopeOrganization {
		return RegistrationTokenResult{}, ErrInvalidRequest
	}
	token, tokenHash, err := identity.NewSessionToken()
	if err != nil {
		return RegistrationTokenResult{}, err
	}
	result := RegistrationTokenResult{Scope: scope}
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO openfox.device_registration_tokens (
				organization_id, issued_by_user_id, token_hash, name, install_id,
				max_slots, expires_at, scope
			) VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, ''), $6, $7, $8)
			RETURNING id::text, expires_at
		`, selection.OrganizationID, selection.ActorID, tokenHash, name, installID, maxSlots, expiresAt, string(scope)).
			Scan(&result.ID, &result.ExpiresAt); err != nil {
			return err
		}
		return recordAudit(ctx, tx, selection, result.ID, "device.registration_issued", map[string]string{
			"status": "ACTIVE", "grant_scope": string(scope),
		})
	})
	if err != nil {
		return RegistrationTokenResult{}, err
	}
	result.Token = token
	return result, nil
}

func (s PostgresStore) RegisterWithToken(ctx context.Context, exchange RegistrationExchange) (RegistrationResult, error) {
	if s.Pool == nil {
		return RegistrationResult{}, postgres.ErrNilPool
	}
	if err := validateExchange(exchange); err != nil {
		return RegistrationResult{}, err
	}
	// token 可以是 v2 安装令牌（解出注册令牌明文）或旧的纯令牌；中心只按明文的 sha256 查找。
	secret, err := RegistrationSecret(exchange.Token)
	if err != nil {
		return RegistrationResult{}, ErrInvalidRequest
	}
	tokenHash := identity.HashSessionToken(secret)
	var result RegistrationResult
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT device_id::text, organization_id::text, owner_user_id::text,
			       key_generation, center_epoch
			  FROM openfox_private.register_device_with_token($1, $2, $3)
		`, tokenHash, exchange.KeyID, exchange.PublicKey).Scan(
			&result.DeviceID, &result.OrganizationID, &result.OwnerUserID,
			&result.KeyGeneration, &result.CenterEpoch,
		)
	})
	if err != nil {
		if isRegistrationConflict(err) {
			return RegistrationResult{}, ErrRegistrationConflict
		}
		return RegistrationResult{}, err
	}
	return result, nil
}

func (s PostgresStore) List(ctx context.Context, selection tenancy.OrganizationSelection) ([]Device, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return nil, ErrInvalidRequest
	}
	devices := make([]Device, 0)
	err := s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+deviceColumns+deviceFromSQL+`
			 WHERE g.organization_id = $1::uuid
		 ORDER BY d.created_at, d.id
		`, selection.OrganizationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			device, _, err := scanDevice(rows)
			if err != nil {
				return err
			}
			devices = append(devices, device)
		}
		return rows.Err()
	})
	return devices, err
}

func (s PostgresStore) UpdateGrantScope(ctx context.Context, selection tenancy.OrganizationSelection, deviceID string, input GrantScopeInput) (Device, error) {
	if s.Pool == nil {
		return Device{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return Device{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil || !input.Scope.Valid() || input.ExpectedVersion <= 0 {
		return Device{}, ErrInvalidRequest
	}
	if input.Scope == ScopePersonal {
		if input.UserID != selection.ActorID || input.SpaceID != "" {
			return Device{}, ErrOwnerRequired
		}
	} else if input.Scope == ScopeSpace {
		if input.SpaceID == "" || input.UserID != "" {
			return Device{}, ErrInvalidRequest
		}
		if _, err := normalizeUUID(input.SpaceID); err != nil {
			return Device{}, ErrInvalidRequest
		}
	} else if input.UserID != "" || input.SpaceID != "" {
		return Device{}, ErrInvalidRequest
	}
	if input.Scope == ScopePersonal {
		input.UserID = selection.ActorID
	}
	if input.Scope == ScopeSpace {
		input.SpaceID = strings.ToLower(input.SpaceID)
	}
	var result Device
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var ownerID string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(d.owner_user_id::text, '')
			  FROM openfox.physical_devices d
			  JOIN openfox.device_grants g ON g.device_id = d.id
			 WHERE d.id = $1::uuid AND g.organization_id = $2::uuid
			   AND g.status = 'ACTIVE'
			 FOR UPDATE OF d, g
		`, deviceID, selection.OrganizationID).Scan(&ownerID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrGrantNotFound
			}
			return err
		}
		if ownerID == "" || ownerID != selection.ActorID {
			return ErrOwnerRequired
		}
		if input.Scope == ScopeSpace {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM openfox.spaces
					 WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'ACTIVE'
				)
			`, selection.OrganizationID, input.SpaceID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrGrantNotFound
			}
		}
		resultTag, err := tx.Exec(ctx, `
			UPDATE openfox.device_grants
			   SET scope = $3,
			       scope_user_id = NULLIF($4, '')::uuid,
			       scope_space_id = NULLIF($5, '')::uuid,
			       permission_version = permission_version + 1
			 WHERE organization_id = $1::uuid AND device_id = $2::uuid
			   AND status = 'ACTIVE' AND permission_version = $6
		`, selection.OrganizationID, deviceID, input.Scope, input.UserID, input.SpaceID, input.ExpectedVersion)
		if err != nil {
			return err
		}
		if resultTag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		if err := recordAudit(ctx, tx, selection, deviceID, "device.grant_scope_updated", map[string]string{
			"grant_scope": string(input.Scope),
		}); err != nil {
			return err
		}
		result, err = loadDevice(ctx, tx, selection.OrganizationID, deviceID)
		return err
	})
	return result, err
}

func (s PostgresStore) RotateKey(ctx context.Context, selection tenancy.OrganizationSelection, deviceID string, input KeyRotationInput, now time.Time) (KeyRotationResult, error) {
	if s.Pool == nil {
		return KeyRotationResult{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return KeyRotationResult{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	oldKeyID, newKeyID, err := normalizeKeyIDs(input.OldKeyID, input.NewKeyID)
	if err != nil || input.ExpectedVersion <= 0 || oldKeyID == newKeyID || len(input.PublicKey) != 32 {
		return KeyRotationResult{}, ErrInvalidRequest
	}
	var result KeyRotationResult
	now = now.UTC()
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var ownerID string
		var deviceVersion int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(d.owner_user_id::text, ''), d.version
			  FROM openfox.physical_devices d
			  JOIN openfox.device_grants g ON g.device_id = d.id
			 WHERE d.id = $1::uuid AND g.organization_id = $2::uuid AND g.status = 'ACTIVE'
			 FOR UPDATE OF d, g
		`, deviceID, selection.OrganizationID).Scan(&ownerID, &deviceVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrGrantNotFound
			}
			return err
		}
		if ownerID != selection.ActorID {
			return ErrOwnerRequired
		}
		if deviceVersion != input.ExpectedVersion {
			return ErrVersionConflict
		}
		var oldGeneration int64
		if err := tx.QueryRow(ctx, `
			SELECT generation
			  FROM openfox.device_keys
			 WHERE device_id = $1::uuid AND key_id = $2 AND status = 'ACTIVE'
			 FOR UPDATE
		`, deviceID, oldKeyID).Scan(&oldGeneration); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDeviceNotFound
			}
			return err
		}
		newGeneration := oldGeneration + 1
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.device_keys (device_id, key_id, public_key, generation)
			VALUES ($1::uuid, $2, $3, $4)
		`, deviceID, newKeyID, input.PublicKey, newGeneration); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.device_keys
			   SET status = 'REVOKED', generation = generation + 1, revoked_at = $3
			 WHERE device_id = $1::uuid AND key_id = $2 AND status = 'ACTIVE'
			`, deviceID, oldKeyID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.device_sessions
			   SET revoked_at = COALESCE(revoked_at, $2)
			 WHERE device_id = $1::uuid AND key_id = $3 AND revoked_at IS NULL
		`, deviceID, now, oldKeyID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			UPDATE openfox.physical_devices
			   SET version = version + 1, updated_at = $2
			 WHERE id = $1::uuid AND version = $3
			 RETURNING version
		`, deviceID, now, input.ExpectedVersion).Scan(&result.DeviceVersion); err != nil {
			return ErrVersionConflict
		}
		result.DeviceID = deviceID
		result.KeyID = newKeyID
		result.KeyGeneration = newGeneration
		return recordAudit(ctx, tx, selection, deviceID, "device.key_rotated", map[string]string{
			"version": formatInt(result.DeviceVersion),
		})
	})
	return result, err
}

func (s PostgresStore) RecordHeartbeat(ctx context.Context, principal deviceauth.Principal, heartbeat Heartbeat, now time.Time) (HeartbeatResult, error) {
	if s.Pool == nil {
		return HeartbeatResult{}, postgres.ErrNilPool
	}
	if err := validateHeartbeat(heartbeat); err != nil {
		return HeartbeatResult{}, err
	}
	deviceID, err := normalizeUUID(principal.DeviceID)
	if err != nil || principal.DeviceGeneration <= 0 {
		return HeartbeatResult{}, ErrInvalidRequest
	}
	if _, err := normalizeUUID(principal.SessionID); err != nil || principal.KeyID == "" || principal.KeyGeneration <= 0 {
		return HeartbeatResult{}, ErrInvalidRequest
	}
	now = now.UTC()
	var result HeartbeatResult
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var centerEpoch int64
		if err := tx.QueryRow(ctx, `
			SELECT center_epoch FROM openfox.center_state WHERE id = 1
		`).Scan(&centerEpoch); err != nil {
			return err
		}
		if heartbeat.CenterEpoch != centerEpoch {
			return ErrEpochConflict
		}
		if err := tx.QueryRow(ctx, `
			UPDATE openfox.physical_devices
			   SET last_seen_at = $2, protocol_major = $3, protocol_minor = $4,
			       daemon_version = $5, operating_system = $6, architecture = $7,
			       capabilities = $8::jsonb, center_epoch = $9,
			       version = version + 1, updated_at = $2
			 WHERE id = $1::uuid AND status = 'ACTIVE' AND generation = $10
			   AND EXISTS (
					SELECT 1
					  FROM openfox.device_sessions ds
					  JOIN openfox.device_keys dk
					    ON dk.device_id = ds.device_id AND dk.key_id = ds.key_id
					 WHERE ds.id = $11::uuid
					   AND ds.device_id = $1::uuid
					   AND ds.key_id = $12
					   AND ds.key_generation = $13
					   AND ds.revoked_at IS NULL
					   AND ds.expires_at > $14
					   AND dk.status = 'ACTIVE'
					   AND dk.generation = $13
				   )
			 RETURNING version
		`, deviceID, now, heartbeat.ProtocolMajor, heartbeat.ProtocolMinor,
			heartbeat.DaemonVersion, heartbeat.OperatingSystem, heartbeat.Architecture,
			heartbeat.Capabilities, centerEpoch, principal.DeviceGeneration,
			principal.SessionID, principal.KeyID, principal.KeyGeneration, now).Scan(&result.Version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return deviceauth.ErrDeviceUnauthenticated
			}
			return err
		}
		result.DeviceID = deviceID
		result.CenterEpoch = centerEpoch
		result.ReceivedAt = now
		return nil
	})
	return result, err
}

func (s PostgresStore) CenterEpoch(ctx context.Context) (int64, error) {
	if s.Pool == nil {
		return 0, postgres.ErrNilPool
	}
	var epoch int64
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT center_epoch FROM openfox.center_state WHERE id = 1`).Scan(&epoch)
	})
	return epoch, err
}

func (s PostgresStore) runForSelection(ctx context.Context, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx) error) error {
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return s.Pool.RunForOrganization(ctx, selection.OrganizationID, tenancy.SourceUserRequest, selector, fn)
}

func loadDevice(ctx context.Context, tx pgx.Tx, organizationID, deviceID string) (Device, error) {
	device, _, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+deviceFromSQL+`
		 WHERE g.organization_id = $1::uuid AND g.device_id = $2::uuid
	`, organizationID, deviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	return device, err
}

func normalizeRegistrationInput(input RegistrationTokenInput) (string, string, int, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 255 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", 0, ErrInvalidRequest
	}
	installID := strings.TrimSpace(input.InstallID)
	if len(installID) > 255 || strings.IndexFunc(installID, unicode.IsControl) >= 0 {
		return "", "", 0, ErrInvalidRequest
	}
	maxSlots := input.MaxSlots
	if maxSlots == 0 {
		maxSlots = 1
	}
	if maxSlots < 1 || maxSlots > 1024 {
		return "", "", 0, ErrInvalidRequest
	}
	return name, installID, maxSlots, nil
}

func validateExchange(exchange RegistrationExchange) error {
	if strings.TrimSpace(exchange.Token) == "" || len(exchange.Token) > MaxInstallTokenLength ||
		exchange.Token != strings.TrimSpace(exchange.Token) || len(exchange.PublicKey) != 32 {
		return ErrInvalidRequest
	}
	if strings.TrimSpace(exchange.KeyID) == "" || len(exchange.KeyID) > 128 || exchange.KeyID != strings.TrimSpace(exchange.KeyID) {
		return ErrInvalidRequest
	}
	return nil
}

func validateHeartbeat(heartbeat Heartbeat) error {
	capabilities := bytes.TrimSpace(heartbeat.Capabilities)
	if heartbeat.CenterEpoch <= 0 || heartbeat.ProtocolMajor < 1 || heartbeat.ProtocolMinor < 0 ||
		len(heartbeat.DaemonVersion) > 128 || len(heartbeat.OperatingSystem) > 128 ||
		len(heartbeat.Architecture) > 128 || len(capabilities) == 0 ||
		len(capabilities) > maxCapabilityBytes || !json.Valid(capabilities) ||
		capabilities[0] != '{' {
		return ErrInvalidRequest
	}
	return nil
}

func normalizeKeyIDs(oldKeyID, newKeyID string) (string, string, error) {
	oldKeyID = strings.TrimSpace(oldKeyID)
	newKeyID = strings.TrimSpace(newKeyID)
	if oldKeyID == "" || newKeyID == "" || len(oldKeyID) > 128 || len(newKeyID) > 128 ||
		strings.IndexFunc(oldKeyID, unicode.IsControl) >= 0 || strings.IndexFunc(newKeyID, unicode.IsControl) >= 0 {
		return "", "", ErrInvalidRequest
	}
	return oldKeyID, newKeyID, nil
}

func recordAudit(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, resourceID, action string, metadata map[string]string) error {
	requestID := randomUUID()
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID:                   requestID,
		OrganizationID:       selection.OrganizationID,
		ActorID:              selection.ActorID,
		Action:               action,
		Decision:             "allow",
		Reason:               "allowed",
		Resource:             audit.ResourceRef{Kind: "device", OrganizationID: selection.OrganizationID, ID: resourceID},
		RequestID:            requestID,
		AuthorizationVersion: selection.AuthorizationVersion,
		SafeMetadata:         metadata,
		CreatedAt:            time.Now().UTC(),
	})
}

func randomUUID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return hex.EncodeToString(value)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return hex.EncodeToString(value[0:4]) + "-" + hex.EncodeToString(value[4:6]) + "-" +
		hex.EncodeToString(value[6:8]) + "-" + hex.EncodeToString(value[8:10]) + "-" + hex.EncodeToString(value[10:16])
}

func formatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func normalizeUUID(value string) (string, error) {
	if len(value) != 36 {
		return "", ErrInvalidRequest
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return "", ErrInvalidRequest
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return "", ErrInvalidRequest
		}
	}
	return strings.ToLower(value), nil
}

func isRegistrationConflict(err error) bool {
	if errors.Is(err, ErrRegistrationConflict) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "registration token is not active") ||
		strings.Contains(message, "device registration issuer is not active") ||
		strings.Contains(message, "device registration issuer may not grant this scope")
}
