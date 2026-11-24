package devices

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/deviceauth"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// 设备侧两步式密钥轮换（阶段 12e，D-01）：
//
//  1. RegisterNextKey：daemon 用「旧密钥」的会话登记新公钥，并用新私钥与旧私钥各签一次
//     （证明持有新钥、且调用方确实持有旧私钥而不只是会话令牌）。旧密钥保持有效。
//  2. daemon 用新私钥换取新会话、把新密钥写入本地配置成功之后，才调用 ConfirmKeyRotation（以「新密钥」的会话）
//     让中心吊销旧密钥及其会话。确认只能由新钥吊销它登记时记录的前驱，方向反过来一律拒绝。
//
// 任一步失败时旧密钥仍有效（中心从不在新配置落盘前吊销旧密钥），因此不会出现两边都不认的情况。
// 两步都幂等：重复登记同一把新钥返回原结果；旧密钥已吊销时确认直接成功。

var (
	// ErrKeyConflict 表示新密钥编号已被另一把公钥占用或已吊销。
	ErrKeyConflict = errors.New("devices: device key conflict")
	// ErrKeyRotationInvalid 表示确认请求与当前会话不匹配（例如用旧密钥自己确认自己）。
	ErrKeyRotationInvalid = errors.New("devices: key rotation request does not match the session")
)

// KeyRotationStore 是设备侧密钥轮换的存储（PostgresStore 实现；单测替身可以不实现，API 返回 501）。
type KeyRotationStore interface {
	RegisterNextKey(ctx context.Context, principal deviceauth.Principal, input NextKeyInput, now time.Time) (NextKeyResult, error)
	ConfirmKeyRotation(ctx context.Context, principal deviceauth.Principal, previousKeyID string, now time.Time) (KeyRotationConfirmResult, error)
}

type NextKeyInput struct {
	KeyID     string
	PublicKey []byte
	Signature []byte
	// CurrentSignature 是当前会话所用密钥的私钥对同一内容的签名。
	CurrentSignature []byte
}

type NextKeyResult struct {
	DeviceID      string `json:"device_id"`
	KeyID         string `json:"key_id"`
	KeyGeneration int64  `json:"key_generation"`
	// Created 为 false 表示同一把新钥此前已登记（幂等重放）。
	Created bool `json:"created"`
}

type KeyRotationConfirmResult struct {
	DeviceID      string `json:"device_id"`
	KeyID         string `json:"key_id"`
	PreviousKeyID string `json:"previous_key_id"`
	// Revoked 为 false 表示旧密钥此前已吊销（幂等重放）。
	Revoked bool `json:"revoked"`
}

// sessionStillValidSQL 复核当前会话仍有效（与心跳的判定一致），防止已吊销会话在认证之后、事务之前抢跑。
const sessionStillValidSQL = `
	SELECT EXISTS (
		SELECT 1
		  FROM openfox.device_sessions ds
		  JOIN openfox.device_keys dk ON dk.device_id = ds.device_id AND dk.key_id = ds.key_id
		 WHERE ds.id = $1::uuid AND ds.device_id = $2::uuid AND ds.key_id = $3
		   AND ds.key_generation = $4 AND ds.revoked_at IS NULL AND ds.expires_at > $5
		   AND dk.status = 'ACTIVE' AND dk.generation = $4
	)`

func (s PostgresStore) RegisterNextKey(ctx context.Context, principal deviceauth.Principal, input NextKeyInput, now time.Time) (NextKeyResult, error) {
	if s.Pool == nil {
		return NextKeyResult{}, postgres.ErrNilPool
	}
	deviceID, err := validatePrincipal(principal)
	if err != nil {
		return NextKeyResult{}, err
	}
	_, keyID, err := normalizeKeyIDs(principal.KeyID, input.KeyID)
	if err != nil || keyID == principal.KeyID || len(input.PublicKey) != ed25519.PublicKeySize ||
		len(input.Signature) != ed25519.SignatureSize || len(input.CurrentSignature) != ed25519.SignatureSize {
		return NextKeyResult{}, ErrInvalidRequest
	}
	payload := deviceauth.KeyRotationPayload(deviceID, principal.KeyID, keyID, input.PublicKey)
	if !ed25519.Verify(ed25519.PublicKey(input.PublicKey), payload, input.Signature) {
		return NextKeyResult{}, deviceauth.ErrInvalidSignature
	}
	now = now.UTC()
	result := NextKeyResult{DeviceID: deviceID, KeyID: keyID}
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockRotatingDevice(ctx, tx, principal, deviceID, now); err != nil {
			return err
		}
		// 调用方必须持有当前会话所用密钥的私钥：泄露的会话令牌本身不足以换上攻击者的密钥。
		var currentPublic []byte
		if err := tx.QueryRow(ctx, `
			SELECT public_key FROM openfox.device_keys
			 WHERE device_id = $1::uuid AND key_id = $2 AND status = 'ACTIVE'`, deviceID, principal.KeyID).Scan(&currentPublic); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return deviceauth.ErrDeviceUnauthenticated
			}
			return err
		}
		if len(currentPublic) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(currentPublic), payload, input.CurrentSignature) {
			return deviceauth.ErrInvalidSignature
		}
		var existing []byte
		var status string
		var generation int64
		err := tx.QueryRow(ctx, `
			SELECT public_key, status, generation FROM openfox.device_keys
			 WHERE device_id = $1::uuid AND key_id = $2
			 FOR UPDATE`, deviceID, keyID).Scan(&existing, &status, &generation)
		if err == nil {
			if status == "ACTIVE" && bytes.Equal(existing, input.PublicKey) {
				result.KeyGeneration = generation
				return nil
			}
			return ErrKeyConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// 之前中途失败、从未确认的待用新钥：吊销，只保留当前会话的密钥 + 本次新钥两把 ACTIVE。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.device_sessions SET revoked_at = COALESCE(revoked_at, $3)
			 WHERE device_id = $1::uuid AND key_id <> $2 AND revoked_at IS NULL`, deviceID, principal.KeyID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.device_keys
			   SET status = 'REVOKED', generation = generation + 1, revoked_at = $3
			 WHERE device_id = $1::uuid AND key_id <> $2 AND status = 'ACTIVE'`, deviceID, principal.KeyID, now); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO openfox.device_keys (device_id, key_id, public_key, generation, predecessor_key_id)
			SELECT $1::uuid, $2, $3, COALESCE(max(generation), 0) + 1, $4
			  FROM openfox.device_keys WHERE device_id = $1::uuid
			RETURNING generation`, deviceID, keyID, input.PublicKey, principal.KeyID).Scan(&result.KeyGeneration); err != nil {
			if isDevicePublicKeyConflict(err) {
				return ErrKeyConflict
			}
			return err
		}
		result.Created = true
		return nil
	})
	if err != nil {
		return NextKeyResult{}, err
	}
	return result, nil
}

func (s PostgresStore) ConfirmKeyRotation(ctx context.Context, principal deviceauth.Principal, previousKeyID string, now time.Time) (KeyRotationConfirmResult, error) {
	if s.Pool == nil {
		return KeyRotationConfirmResult{}, postgres.ErrNilPool
	}
	deviceID, err := validatePrincipal(principal)
	if err != nil {
		return KeyRotationConfirmResult{}, err
	}
	previousKeyID, _, err = normalizeKeyIDs(previousKeyID, principal.KeyID)
	if err != nil {
		return KeyRotationConfirmResult{}, ErrInvalidRequest
	}
	if previousKeyID == principal.KeyID {
		return KeyRotationConfirmResult{}, ErrKeyRotationInvalid
	}
	now = now.UTC()
	result := KeyRotationConfirmResult{DeviceID: deviceID, KeyID: principal.KeyID, PreviousKeyID: previousKeyID}
	var organizations []tenancy.OrganizationSelection
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockRotatingDevice(ctx, tx, principal, deviceID, now); err != nil {
			return err
		}
		// 方向校验：只允许新钥吊销它在登记时记录的前驱。predecessor_key_id 写入后不再修改，吊销不会动它
		// （generation 在吊销时会加 1，created_at 只能比较先后，都不如前驱关系可靠）。
		// 旧钥的会话——包括只有令牌、没有私钥的调用方——因此不能吊销接替它的新钥，也不能吊销任何别的钥。
		var predecessor string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(predecessor_key_id, '') FROM openfox.device_keys
			 WHERE device_id = $1::uuid AND key_id = $2 AND status = 'ACTIVE'`, deviceID, principal.KeyID).Scan(&predecessor); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return deviceauth.ErrDeviceUnauthenticated
			}
			return err
		}
		if predecessor == "" || predecessor != previousKeyID {
			return ErrKeyRotationInvalid
		}
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT status FROM openfox.device_keys
			 WHERE device_id = $1::uuid AND key_id = $2
			 FOR UPDATE`, deviceID, previousKeyID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrKeyRotationInvalid
			}
			return err
		}
		if status == "ACTIVE" {
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.device_keys
				   SET status = 'REVOKED', generation = generation + 1, revoked_at = $3
				 WHERE device_id = $1::uuid AND key_id = $2 AND status = 'ACTIVE'`, deviceID, previousKeyID, now); err != nil {
				return err
			}
			result.Revoked = true
		}
		// 旧密钥的会话一律吊销（重放时也补一次，处理极端情况下残留的会话）。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.device_sessions SET revoked_at = COALESCE(revoked_at, $3)
			 WHERE device_id = $1::uuid AND key_id = $2 AND revoked_at IS NULL`, deviceID, previousKeyID, now); err != nil {
			return err
		}
		if !result.Revoked {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.physical_devices SET version = version + 1, updated_at = $2
			 WHERE id = $1::uuid`, deviceID, now); err != nil {
			return err
		}
		// audit_events.actor_id 引用用户：设备侧轮换记在电脑所有者名下（所有者发起或 daemon 代为执行）。
		var ownerID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(owner_user_id::text, '') FROM openfox.physical_devices WHERE id = $1::uuid`,
			deviceID).Scan(&ownerID); err != nil {
			return err
		}
		if ownerID == "" {
			return nil
		}
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, permission_version
			  FROM openfox_private.list_device_active_organizations($1::uuid)`, deviceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			selection := tenancy.OrganizationSelection{ActorID: ownerID}
			if err := rows.Scan(&selection.OrganizationID, &selection.AuthorizationVersion); err != nil {
				return err
			}
			organizations = append(organizations, selection)
		}
		return rows.Err()
	})
	if err != nil {
		return KeyRotationConfirmResult{}, err
	}
	// 审计按组织隔离：轮换已提交后在每个已授权组织各记一条。审计写入失败不回滚轮换（密钥状态以 device_keys 为准）。
	for _, selection := range organizations {
		_ = s.recordDeviceAudit(ctx, selection, deviceID, "device.key_rotated")
	}
	return result, nil
}

// lockRotatingDevice 锁住电脑行（串行化同一电脑的并发轮换），并复核当前会话仍有效。
func lockRotatingDevice(ctx context.Context, tx pgx.Tx, principal deviceauth.Principal, deviceID string, now time.Time) error {
	var generation int64
	if err := tx.QueryRow(ctx, `
		SELECT generation FROM openfox.physical_devices
		 WHERE id = $1::uuid AND status = 'ACTIVE'
		 FOR UPDATE`, deviceID).Scan(&generation); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deviceauth.ErrDeviceUnauthenticated
		}
		return err
	}
	if generation != principal.DeviceGeneration {
		return deviceauth.ErrDeviceUnauthenticated
	}
	var valid bool
	if err := tx.QueryRow(ctx, sessionStillValidSQL, principal.SessionID, deviceID, principal.KeyID,
		principal.KeyGeneration, now).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return deviceauth.ErrDeviceUnauthenticated
	}
	return nil
}

func validatePrincipal(principal deviceauth.Principal) (string, error) {
	deviceID, err := normalizeUUID(principal.DeviceID)
	if err != nil || principal.DeviceGeneration <= 0 || principal.KeyGeneration <= 0 || principal.KeyID == "" {
		return "", ErrInvalidRequest
	}
	if _, err := normalizeUUID(principal.SessionID); err != nil {
		return "", ErrInvalidRequest
	}
	return deviceID, nil
}

// recordDeviceAudit 在指定组织记一条设备审计（设备请求来源，操作人为电脑所有者）。
func (s PostgresStore) recordDeviceAudit(ctx context.Context, selection tenancy.OrganizationSelection, deviceID, action string) error {
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return s.Pool.RunForOrganization(ctx, selection.OrganizationID, tenancy.SourceDeviceRequest, selector,
		func(ctx context.Context, tx pgx.Tx) error {
			return recordAudit(ctx, tx, selection, deviceID, action, map[string]string{"status": "ACTIVE"})
		})
}
