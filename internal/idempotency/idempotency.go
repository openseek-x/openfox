// Package idempotency 实现创建类接口的 Idempotency-Key 语义（迁移 000027）。
//
// 约定：
//   - 作用域：组织 × 当前操作人 × 操作名 × 键；保留 24 小时，过期后同一个键视为新请求。
//   - 键由客户端生成，格式 ^[A-Za-z0-9_-]{8,128}$；为空表示调用方不要求幂等。
//   - 请求摘要为规范化请求体的 SHA-256；同键不同摘要返回 ErrConflict（HTTP 409 IDEMPOTENCY_CONFLICT）。
//   - 同键同摘要：Begin 返回 Replay=true 与首次创建的资源 ID，调用方返回该资源的当前状态。
//   - 记录与业务数据在同一事务写入：并发的重复请求在主键上排队，前一个提交后按重放处理，
//     前一个回滚则由后一个重新创建。
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// HeaderName 是请求头名称；ReplayedHeader 在重放响应中标记为 true。
const (
	HeaderName     = "Idempotency-Key"
	ReplayedHeader = "Idempotent-Replayed"
)

var (
	ErrInvalidKey = errors.New("idempotency: invalid key")
	ErrConflict   = errors.New("idempotency: key reused with a different request")

	keyPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,63}$`)
)

// Claim 是 Begin 的结果。Active 为 false 表示请求没有带键，调用方照常创建、无需 Complete。
type Claim struct {
	Active     bool
	Replay     bool
	ResourceID string
	operation  string
	key        string
}

// ValidKey 校验客户端提供的键；空字符串合法（表示不要求幂等）。
func ValidKey(key string) bool {
	return key == "" || keyPattern.MatchString(key)
}

// RequestHash 返回请求体的规范化摘要。request 通常是已解析、已规范化的输入结构体，
// 同一结构体经 encoding/json 序列化的结果是确定的。
func RequestHash(operation string, request any) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte(operation+"\x00"), payload...))
	return hex.EncodeToString(digest[:]), nil
}

// Begin 在租户事务内登记幂等键。必须在任何业务写入之前调用。
func Begin(ctx context.Context, tx pgx.Tx, operation, key string, request any) (Claim, error) {
	if key == "" {
		return Claim{}, nil
	}
	if !keyPattern.MatchString(key) || !operationPattern.MatchString(operation) {
		return Claim{}, ErrInvalidKey
	}
	hash, err := RequestHash(operation, request)
	if err != nil {
		return Claim{}, err
	}
	// 同一个键过期后允许复用：先删掉过期记录（只影响当前操作人自己的行）。
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.api_idempotency_keys
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND actor_id = openfox_private.current_actor_id()
		   AND operation = $1 AND idempotency_key = $2
		   AND expires_at <= clock_timestamp()`, operation, key); err != nil {
		return Claim{}, err
	}
	inserted, err := tx.Exec(ctx, `
		INSERT INTO openfox.api_idempotency_keys (
			organization_id, actor_id, operation, idempotency_key, request_hash, expires_at
		) VALUES (
			openfox_private.current_organization_id(), openfox_private.current_actor_id(),
			$1, $2, $3, clock_timestamp() + interval '24 hours'
		)
		ON CONFLICT (organization_id, actor_id, operation, idempotency_key) DO NOTHING`,
		operation, key, hash)
	if err != nil {
		return Claim{}, err
	}
	if inserted.RowsAffected() == 1 {
		return Claim{Active: true, operation: operation, key: key}, nil
	}
	var storedHash string
	var resourceID *string
	err = tx.QueryRow(ctx, `
		SELECT request_hash, resource_id::text
		  FROM openfox.api_idempotency_keys
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND actor_id = openfox_private.current_actor_id()
		   AND operation = $1 AND idempotency_key = $2`, operation, key).Scan(&storedHash, &resourceID)
	if err != nil {
		return Claim{}, err
	}
	// 记录提交时一定已经写入 resource_id；为空只可能是首个请求创建失败后被删，按冲突处理更安全。
	if storedHash != hash || resourceID == nil {
		return Claim{}, ErrConflict
	}
	return Claim{Active: true, Replay: true, ResourceID: *resourceID, operation: operation, key: key}, nil
}

// Complete 记录首次创建的资源 ID。没有带键（Active=false）或是重放时不做任何事。
func Complete(ctx context.Context, tx pgx.Tx, claim Claim, resourceID string) error {
	if !claim.Active || claim.Replay {
		return nil
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.api_idempotency_keys
		   SET resource_id = $3::uuid
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND actor_id = openfox_private.current_actor_id()
		   AND operation = $1 AND idempotency_key = $2`, claim.operation, claim.key, resourceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("idempotency: claim row missing")
	}
	return nil
}
