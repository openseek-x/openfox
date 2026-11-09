package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

// Agent 环境变量（迁移 000028 agent_env_vars）：值用 AES-256-GCM 加密落库，AAD 绑定组织 + Agent + 名称。
// 派发 Run 时读取当前值，不固定到配置版本；配置版本只记录摘要 env_digest（审计用）。

// MaxEnvVars 是每个 Agent 的环境变量上限。
const MaxEnvVars = 100

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type EnvVar struct {
	Name string `json:"name"`
	// Value 只对 Agent 所有者与组织 OWNER / ADMIN 输出；其他人、未配置主密钥或无法解密时为 null。
	Value     *string   `json:"value"`
	HasValue  bool      `json:"has_value"`
	Version   uint64    `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EnvVarList struct {
	EnvVars          []EnvVar `json:"env_vars"`
	CanViewValues    bool     `json:"can_view_values"`
	SecretsAvailable bool     `json:"secrets_available"`
}

type envRow struct {
	name      string
	sealed    secrets.Sealed
	version   int64
	updatedAt time.Time
}

func (s PostgresStore) ListEnvVars(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string) (EnvVarList, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return EnvVarList{}, err
	}
	result := EnvVarList{EnvVars: make([]EnvVar, 0), SecretsAvailable: s.Secrets != nil}
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		agent, err := requireReadableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor)
		if err != nil {
			return err
		}
		result.CanViewValues = canViewSecretValues(agent, actor)
		rows, err := loadEnvRows(ctx, tx, agentID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			item := EnvVar{Name: row.name, HasValue: true, Version: uint64(row.version), UpdatedAt: row.updatedAt}
			if result.CanViewValues && s.Secrets != nil {
				// 解密失败（主密钥已轮换等）时只返回 has_value，不报错。
				if plaintext, err := s.Secrets.Open(row.sealed, secrets.EnvAAD(selection.OrganizationID, agentID, row.name)); err == nil {
					value := string(plaintext)
					item.Value = &value
				}
			}
			result.EnvVars = append(result.EnvVars, item)
		}
		return nil
	})
	return result, err
}

// PutEnvVar 新建（version 为 nil）或按版本修改环境变量。
func (s PostgresStore) PutEnvVar(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID, name, value string, version *uint64) (EnvVar, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return EnvVar{}, err
	}
	if !envNamePattern.MatchString(name) || len(value) > secrets.MaxPlaintext || (version != nil && *version == 0) {
		return EnvVar{}, ErrInvalidRequest
	}
	if s.Secrets == nil {
		return EnvVar{}, ErrSecretsUnavailable
	}
	var result EnvVar
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		agent, err := lockEditableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor, false)
		if err != nil {
			return err
		}
		sealed, err := s.Secrets.Seal([]byte(value), secrets.EnvAAD(selection.OrganizationID, agentID, name))
		if err != nil {
			return err
		}
		var newVersion int64
		var updatedAt time.Time
		if version == nil {
			var count int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM openfox.agent_env_vars
				 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid`,
				agentID).Scan(&count); err != nil {
				return err
			}
			if count >= MaxEnvVars {
				return ErrLimitReached
			}
			err = tx.QueryRow(ctx, `
				INSERT INTO openfox.agent_env_vars (
					organization_id, agent_id, name, value_ciphertext, value_nonce, key_id, updated_by
				) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6::uuid)
				ON CONFLICT (organization_id, agent_id, name) DO NOTHING
				RETURNING version, updated_at`,
				agentID, name, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, actor.UserID).Scan(&newVersion, &updatedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrEnvVarExists
			}
		} else {
			err = tx.QueryRow(ctx, `
				UPDATE openfox.agent_env_vars
				   SET value_ciphertext = $3, value_nonce = $4, key_id = $5, updated_by = $6::uuid,
				       version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND agent_id = $1::uuid AND name = $2 AND version = $7
				RETURNING version, updated_at`,
				agentID, name, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, actor.UserID, int64(*version)).Scan(&newVersion, &updatedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				exists, existsErr := envVarExists(ctx, tx, agentID, name)
				if existsErr != nil {
					return existsErr
				}
				if exists {
					return ErrVersionConflict
				}
				return ErrResourceNotFound
			}
		}
		if err != nil {
			return mapDatabaseError(err)
		}
		result = EnvVar{Name: name, HasValue: true, Version: uint64(newVersion), UpdatedAt: updatedAt}
		if canViewSecretValues(agent, actor) {
			result.Value = &value
		}
		// 审计不记录名称与值。
		return recordAudit(ctx, tx, selection, actor, "agent.env_var_saved", agentID, spaceID, uint64(newVersion))
	})
	return result, err
}

func (s PostgresStore) DeleteEnvVar(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID, name string) error {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return err
	}
	if !envNamePattern.MatchString(name) {
		return ErrResourceNotFound
	}
	return s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockEditableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor, false); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM openfox.agent_env_vars
			 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid AND name = $2`,
			agentID, name)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrResourceNotFound
		}
		return recordAudit(ctx, tx, selection, actor, "agent.env_var_deleted", agentID, spaceID, 1)
	})
}

func envVarExists(ctx context.Context, tx pgx.Tx, agentID, name string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.agent_env_vars
		                WHERE organization_id = openfox_private.current_organization_id()
		                  AND agent_id = $1::uuid AND name = $2)`, agentID, name).Scan(&exists)
	return exists, err
}

func loadEnvRows(ctx context.Context, tx pgx.Tx, agentID string) ([]envRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT name, value_ciphertext, value_nonce, key_id, version, updated_at
		  FROM openfox.agent_env_vars
		 WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid
		 ORDER BY name
		 LIMIT $2`, agentID, MaxEnvVars+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]envRow, 0)
	for rows.Next() {
		var row envRow
		if err := rows.Scan(&row.name, &row.sealed.Ciphertext, &row.sealed.Nonce, &row.sealed.KeyID, &row.version, &row.updatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// computeEnvDigest 返回 sha256(按名称排序的 "name=sha256hex(value)\n")；没有环境变量、未配置主密钥或任何一个值
// 无法解密时返回空串（摘要只用于审计，不阻塞配置保存）。
func computeEnvDigest(ctx context.Context, tx pgx.Tx, box *secrets.Box, organizationID, agentID string) (string, error) {
	rows, err := loadEnvRows(ctx, tx, agentID)
	if err != nil || len(rows) == 0 || box == nil {
		return "", err
	}
	values := make(map[string][]byte, len(rows))
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		plaintext, err := box.Open(row.sealed, secrets.EnvAAD(organizationID, agentID, row.name))
		if err != nil {
			return "", nil
		}
		values[row.name] = plaintext
		names = append(names, row.name)
	}
	return EnvDigest(names, values), nil
}

// EnvDigest 是 env_digest 的纯函数部分：names 须已按字节序排序。
func EnvDigest(names []string, values map[string][]byte) string {
	if len(names) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, name := range names {
		valueDigest := sha256.Sum256(values[name])
		builder.WriteString(name)
		builder.WriteByte('=')
		builder.WriteString(hex.EncodeToString(valueDigest[:]))
		builder.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:])
}

// copyEnvVars 把源 Agent 的环境变量解密后用目标 Agent 的 AAD 重新加密写入。
func copyEnvVars(ctx context.Context, tx pgx.Tx, box *secrets.Box, organizationID, fromAgentID, toAgentID, actorID string) error {
	rows, err := loadEnvRows(ctx, tx, fromAgentID)
	if err != nil || len(rows) == 0 {
		return err
	}
	if box == nil {
		return ErrSecretsUnavailable
	}
	for _, row := range rows {
		plaintext, err := box.Open(row.sealed, secrets.EnvAAD(organizationID, fromAgentID, row.name))
		if err != nil {
			return ErrSecretsUnavailable
		}
		sealed, err := box.Seal(plaintext, secrets.EnvAAD(organizationID, toAgentID, row.name))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.agent_env_vars (
				organization_id, agent_id, name, value_ciphertext, value_nonce, key_id, updated_by
			) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6::uuid)`,
			toAgentID, row.name, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, actorID); err != nil {
			return mapDatabaseError(err)
		}
	}
	return nil
}

// agentRef 校验调用方身份并规范化路径里的空间与 Agent ID（Agent ID 非法按不存在处理）。
func (s PostgresStore) agentRef(selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string) (string, string, error) {
	if err := s.validate(selection, actor); err != nil {
		return "", "", err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return "", "", ErrNotFound
	}
	agentID, err = normalizeUUID(agentID)
	if err != nil {
		return "", "", ErrNotFound
	}
	return spaceID, agentID, nil
}
