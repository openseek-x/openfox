package deviceauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/audit"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

type PostgresStore struct {
	Pool *postgres.Pool
}

func (s PostgresStore) CreateChallenge(ctx context.Context, challenge Challenge) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO openfox.device_challenges (
				id, device_id, key_id, nonce, challenge_bytes, expires_at, created_at
			)
			SELECT $1::uuid, $2::uuid, $3, $4, $5, $6, $7
			 WHERE EXISTS (
				SELECT 1
				  FROM openfox.physical_devices d
				  JOIN openfox.device_keys k ON k.device_id = d.id
				 WHERE d.id = $2::uuid
				   AND d.status = 'ACTIVE'
				   AND k.key_id = $3
				   AND k.status = 'ACTIVE'
				   AND k.generation > 0
				   AND d.generation > 0
			 )
			RETURNING id
		`, challenge.ID, challenge.DeviceID, challenge.KeyID, challenge.Nonce,
			challenge.Challenge, challenge.ExpiresAt, challenge.CreatedAt).Scan(new(string))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeviceUnauthenticated
	}
	return err
}

func (s PostgresStore) ConsumeChallenge(
	ctx context.Context,
	request VerifyRequest,
	pending PendingSession,
	now time.Time,
	verify SignatureVerifier,
) (Session, error) {
	if s.Pool == nil {
		return Session{}, postgres.ErrNilPool
	}
	var session Session
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var (
			storedDeviceID, storedKeyID      string
			nonce, challengeBytes, publicKey []byte
			storedExpiresAt, consumedAt      *time.Time
			deviceStatus, keyStatus          string
			deviceGeneration, keyGeneration  int64
		)
		err := tx.QueryRow(ctx, `
			SELECT c.device_id::text, c.key_id, c.nonce, c.challenge_bytes,
			       c.expires_at, c.consumed_at,
			       d.status, d.generation,
			       k.status, k.generation, k.public_key
			  FROM openfox.device_challenges c
			  JOIN openfox.physical_devices d ON d.id = c.device_id
			  JOIN openfox.device_keys k
			    ON k.device_id = c.device_id AND k.key_id = c.key_id
			 WHERE c.id = $1::uuid
			 FOR UPDATE OF c
		`, request.ChallengeID).Scan(&storedDeviceID, &storedKeyID, &nonce, &challengeBytes,
			&storedExpiresAt, &consumedAt, &deviceStatus, &deviceGeneration,
			&keyStatus, &keyGeneration, &publicKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChallengeNotFound
		}
		if err != nil {
			return err
		}
		if storedDeviceID != request.DeviceID || storedKeyID != request.KeyID || consumedAt != nil || storedExpiresAt == nil || !now.Before(*storedExpiresAt) {
			return ErrChallengeConflict
		}
		if deviceStatus != "ACTIVE" || keyStatus != "ACTIVE" || deviceGeneration <= 0 || keyGeneration <= 0 || len(publicKey) != ed25519.PublicKeySize {
			return ErrDeviceUnauthenticated
		}
		if verify == nil || !verify(ed25519.PublicKey(publicKey), CanonicalPayload(
			request.ChallengeID, request.DeviceID, request.KeyID, nonce, challengeBytes,
		), request.Signature) {
			return ErrInvalidSignature
		}
		result, err := tx.Exec(ctx, `
			UPDATE openfox.device_challenges
			   SET consumed_at = $2
			 WHERE id = $1::uuid AND consumed_at IS NULL AND expires_at > $2
		`, request.ChallengeID, now)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrChallengeConflict
		}
		if len(pending.TokenHash) != 32 || pending.ExpiresAt.Before(pending.IssuedAt) || !pending.ExpiresAt.After(now) {
			return ErrInvalidRequest
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.device_sessions (
				device_id, key_id, device_generation, key_generation,
				token_hash, issued_at, expires_at
			) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		`, request.DeviceID, request.KeyID, deviceGeneration, keyGeneration,
			pending.TokenHash, pending.IssuedAt, pending.ExpiresAt); err != nil {
			return err
		}
		session = Session{
			DeviceID:         request.DeviceID,
			KeyID:            request.KeyID,
			DeviceGeneration: deviceGeneration,
			KeyGeneration:    keyGeneration,
			IssuedAt:         pending.IssuedAt,
			ExpiresAt:        pending.ExpiresAt,
		}
		return nil
	})
	return session, err
}

func (s PostgresStore) AuthenticateSession(ctx context.Context, tokenHash []byte, now time.Time) (Session, error) {
	if s.Pool == nil {
		return Session{}, postgres.ErrNilPool
	}
	var session Session
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT s.id::text, s.device_id::text, s.key_id,
			       s.device_generation, s.key_generation, s.issued_at, s.expires_at
			  FROM openfox.device_sessions s
			  JOIN openfox.physical_devices d
			    ON d.id = s.device_id
			  JOIN openfox.device_keys k
			    ON k.device_id = s.device_id AND k.key_id = s.key_id
			 WHERE s.token_hash = $1
			   AND s.revoked_at IS NULL
			   AND s.expires_at > $2
			   AND d.status = 'ACTIVE'
			   AND d.generation = s.device_generation
			   AND k.status = 'ACTIVE'
			   AND k.generation = s.key_generation
			   AND openfox_private.device_has_active_grant(s.device_id)
		`, tokenHash, now).Scan(&session.ID, &session.DeviceID, &session.KeyID,
			&session.DeviceGeneration, &session.KeyGeneration, &session.IssuedAt, &session.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	return session, err
}

func (s PostgresStore) SelectOrganization(ctx context.Context, deviceID, organizationID string) (tenancy.OrganizationSelection, error) {
	if s.Pool == nil {
		return tenancy.OrganizationSelection{}, postgres.ErrNilPool
	}
	var selection tenancy.OrganizationSelection
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT organization_id::text, permission_version
			  FROM openfox_private.resolve_device_organization($1::uuid, $2::uuid)
		`, deviceID, organizationID).Scan(&selection.OrganizationID, &selection.AuthorizationVersion)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tenancy.OrganizationSelection{}, ErrOrganizationNotFound
	}
	if err != nil {
		return tenancy.OrganizationSelection{}, err
	}
	selection.ActorID = deviceID
	return selection, nil
}

func (s PostgresStore) RegisterDevice(ctx context.Context, registration Registration) (string, error) {
	if s.Pool == nil {
		return "", postgres.ErrNilPool
	}
	var deviceID string
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return registration.Organization, nil
	})
	err := s.Pool.RunForOrganization(ctx, registration.Organization.OrganizationID, tenancy.SourceUserRequest, selector,
		func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `
				INSERT INTO openfox.physical_devices (
					owner_user_id, device_key, name, install_id, max_slots
				)
				VALUES ($1::uuid, $2, $3, NULLIF($4, ''), $5)
				RETURNING id::text
			`, registration.Organization.ActorID, registration.DeviceKey, registration.Name,
				registration.InstallID, registration.MaxSlots).Scan(&deviceID); err != nil {
				if isUniqueViolation(err) {
					return ErrDeviceAlreadyRegistered
				}
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.device_keys (device_id, key_id, public_key)
				VALUES ($1::uuid, $2, $3)
			`, deviceID, registration.KeyID, []byte(registration.PublicKey)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO openfox.device_grants (organization_id, device_id)
				VALUES ($1::uuid, $2::uuid)
			`, registration.Organization.OrganizationID, deviceID)
			if err != nil {
				return err
			}
			return recordAudit(ctx, tx, registration.Organization, deviceID,
				"device.registered", map[string]string{"status": "ACTIVE", "grant_scope": "organization"})
		})
	return deviceID, err
}

func (s PostgresStore) RevokeGrant(ctx context.Context, selection tenancy.OrganizationSelection, deviceID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return s.Pool.RunForOrganization(ctx, selection.OrganizationID, tenancy.SourceUserRequest, selector,
		func(ctx context.Context, tx pgx.Tx) error {
			var status string
			err := tx.QueryRow(ctx, `
				SELECT status
				  FROM openfox.device_grants
				 WHERE organization_id = $1::uuid AND device_id = $2::uuid
				 FOR UPDATE
			`, selection.OrganizationID, deviceID).Scan(&status)
			if errors.Is(err, pgx.ErrNoRows) || status != "ACTIVE" {
				return ErrOrganizationNotFound
			}
			result, err := tx.Exec(ctx, `
				UPDATE openfox.device_grants
				   SET status = 'REVOKED', permission_version = permission_version + 1
				 WHERE organization_id = $1::uuid AND device_id = $2::uuid
				   AND status = 'ACTIVE'
			`, selection.OrganizationID, deviceID)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return ErrOrganizationNotFound
			}
			return recordAudit(ctx, tx, selection, deviceID,
				"device.grant_revoked", map[string]string{"status": "REVOKED"})
		})
}

func (s PostgresStore) RevokeDevice(ctx context.Context, deviceID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var revoked bool
		if err := tx.QueryRow(ctx, `
			SELECT openfox_private.revoke_device($1::uuid, $2)
		`, deviceID, now).Scan(&revoked); err != nil {
			return err
		}
		if !revoked {
			return ErrDeviceUnauthenticated
		}
		return nil
	})
}

func (s PostgresStore) RevokeKey(ctx context.Context, deviceID, keyID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var revoked bool
		if err := tx.QueryRow(ctx, `
			SELECT openfox_private.revoke_device_key($1::uuid, $2, $3)
		`, deviceID, keyID, now).Scan(&revoked); err != nil {
			return err
		}
		if !revoked {
			return ErrDeviceUnauthenticated
		}
		return nil
	})
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

func recordAudit(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, resourceID, action string, metadata map[string]string) error {
	eventID, err := newUUID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID:                   eventID,
		OrganizationID:       selection.OrganizationID,
		ActorID:              selection.ActorID,
		Action:               action,
		Decision:             "allow",
		Reason:               "allowed",
		Resource:             audit.ResourceRef{Kind: "device", OrganizationID: selection.OrganizationID, ID: resourceID},
		RequestID:            eventID,
		AuthorizationVersion: selection.AuthorizationVersion,
		SafeMetadata:         metadata,
		CreatedAt:            time.Now().UTC(),
	})
}
