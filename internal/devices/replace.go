package devices

import (
	"context"
	"errors"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

const replacedMessage = "The Agent run was canceled because its computer was replaced by a new registration; send the message again."

// ReplaceDevice 把本组织里绑在旧电脑记录（replacedID）上的 Agent 迁到新记录（deviceID），
// 取消这些 Agent 的活跃 Run，并吊销旧记录在本组织的授权。调用者必须是两台电脑的所有者
// （组织 OWNER / ADMIN 也不行）；旧记录在线、两条授权范围不一致、Agent 超过上限都拒绝。
// 锁序：授权（按 device_id 排序）→ 容量函数（CancelAgentRuns）→ agents 行，与移除电脑一致。
func (s PostgresStore) ReplaceDevice(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID, replacedID string) (ReplacementResult, error) {
	if s.Pool == nil {
		return ReplacementResult{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return ReplacementResult{}, ErrInvalidRequest
	}
	newID, err := normalizeUUID(deviceID)
	if err != nil {
		return ReplacementResult{}, ErrInvalidRequest
	}
	oldID, err := normalizeUUID(replacedID)
	if err != nil || oldID == newID {
		return ReplacementResult{}, ErrInvalidRequest
	}
	var result ReplacementResult
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		result = ReplacementResult{}
		newDevice, _, err := loadVisibleDevice(ctx, tx, viewer, newID)
		if err != nil {
			return err
		}
		oldDevice, _, err := loadVisibleDevice(ctx, tx, viewer, oldID)
		if err != nil {
			return err
		}
		if newDevice.OwnerUserID != selection.ActorID || oldDevice.OwnerUserID != selection.ActorID {
			return ErrForbidden
		}
		// 按 device_id 排序加锁，避免两个反向请求互相死锁。
		ids := []string{newID, oldID}
		sort.Strings(ids)
		for _, id := range ids {
			if err := lockActiveGrant(ctx, tx, id); err != nil {
				return err
			}
		}
		// 加锁后重新读取，在线状态与授权范围以锁内为准。
		if newDevice, _, err = loadVisibleDevice(ctx, tx, viewer, newID); err != nil {
			return err
		}
		if oldDevice, _, err = loadVisibleDevice(ctx, tx, viewer, oldID); err != nil {
			return err
		}
		if newDevice.Status != "ACTIVE" {
			return ErrDeviceNotFound
		}
		if oldDevice.Online {
			return ErrDeviceOnline
		}
		if newDevice.GrantScope != oldDevice.GrantScope || newDevice.GrantUserID != oldDevice.GrantUserID ||
			newDevice.GrantSpaceID != oldDevice.GrantSpaceID {
			return ErrGrantScopeMismatch
		}
		agentIDs, err := listMovableAgents(ctx, tx, oldID, false)
		if err != nil {
			return err
		}
		if len(agentIDs) > maxUnbindAgents {
			return ErrTooManyAgents
		}
		if len(agentIDs) > 0 {
			canceled, err := dispatch.CancelAgentRuns(ctx, tx, selection.OrganizationID, agentIDs, replacedMessage)
			if err != nil {
				return err
			}
			result.CanceledRuns, result.CancelNotices = canceled.Canceled, canceled.Notices
			// 取消 Run 之后再锁 agents 行；集合变了（并发新建 / 归档）就让调用方重试。
			locked, err := listMovableAgents(ctx, tx, oldID, true)
			if err != nil {
				return err
			}
			if !sameStrings(agentIDs, locked) {
				return ErrVersionConflict
			}
			moved, err := moveAgents(ctx, tx, selection, agentIDs, oldID, newID)
			if err != nil {
				return err
			}
			result.MovedAgents = moved
		}
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.device_grants
			   SET status = 'REVOKED', permission_version = permission_version + 1
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND status = 'ACTIVE'`, oldID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDeviceNotFound
		}
		result.DeviceID, result.ReplacedDeviceID = newID, oldID
		return recordAudit(ctx, tx, selection, newID, "device.replaced", map[string]string{
			"replaced_device_id": oldID,
			"moved_agents":       itoa(result.MovedAgents),
			"canceled_runs":      itoa(result.CanceledRuns),
		})
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			// 配置版本触发器拒绝：新授权对这个 Agent 不适用（例如授权对象与 Agent 所有者不一致）。
			return ReplacementResult{}, ErrGrantScopeMismatch
		}
		return ReplacementResult{}, err
	}
	return result, nil
}

// listMovableAgents 列出本组织绑在这台电脑上、未归档且当前配置版本存在的托管 Agent（含内置小助手），按 id 排序。
// agents.current_config_version 没有外键：找不到当前配置版本的旧数据本来就无法运行，留在原处，不阻塞其他 Agent。
// 多取一条用来判断是否超过上限；lock 为真时加行锁。
func listMovableAgents(ctx context.Context, tx pgx.Tx, deviceID string, lock bool) ([]string, error) {
	query := `
		SELECT a.id::text
		  FROM openfox.agents a
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.device_id = $1::uuid AND a.space_id IS NOT NULL AND a.archived_at IS NULL
		   AND EXISTS (
		       SELECT 1 FROM openfox.agent_config_versions c
		        WHERE c.organization_id = a.organization_id AND c.agent_id = a.id
		          AND c.config_version = a.current_config_version)
		 ORDER BY a.id
		 LIMIT $2`
	if lock {
		query += ` FOR UPDATE OF a`
	}
	rows, err := tx.Query(ctx, query, deviceID, maxUnbindAgents+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// moveAgents 为每个 Agent 写入新配置版本（完整复制当前版本，只换 computer_id / grant_id），
// 再把 agents 指向新电脑与新版本。调用方已锁住这些 agents 行与两条授权。
func moveAgents(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, agentIDs []string, oldID, newID string) (int, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO openfox.agent_config_versions (
			organization_id, agent_id, config_version, instructions, computer_id, grant_id,
			runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds,
			directory_mode, directory_roots, created_by,
			chat_concurrency, chat_concurrency_value, workdir_isolation, approval_mode, collaboration_mode,
			custom_args, git_author_name_mode, git_author_name, git_author_email_mode, git_author_email, env_digest, git_co_author_enabled)
		SELECT c.organization_id, c.agent_id, c.config_version + 1, c.instructions, $3::uuid,
		       (SELECT g.id FROM openfox.device_grants g
		         WHERE g.organization_id = c.organization_id AND g.device_id = $3::uuid AND g.status = 'ACTIVE'),
		       c.runtime, c.model, c.max_concurrency, c.queue_timeout_seconds, c.run_timeout_seconds,
		       c.directory_mode, c.directory_roots, $4::uuid,
		       c.chat_concurrency, c.chat_concurrency_value, c.workdir_isolation, c.approval_mode, c.collaboration_mode,
		       c.custom_args, c.git_author_name_mode, c.git_author_name, c.git_author_email_mode, c.git_author_email, c.env_digest, c.git_co_author_enabled
		  FROM openfox.agents a
		  JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id AND c.agent_id = a.id AND c.config_version = a.current_config_version
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.id = ANY($1::uuid[]) AND a.device_id = $2::uuid AND a.archived_at IS NULL`,
		agentIDs, oldID, newID, selection.ActorID)
	if err != nil {
		return 0, err
	}
	if int(tag.RowsAffected()) != len(agentIDs) {
		return 0, ErrVersionConflict
	}
	update, err := tx.Exec(ctx, `
		UPDATE openfox.agents a
		   SET device_id = $3::uuid, current_config_version = a.current_config_version + 1,
		       version = a.version + 1, updated_at = clock_timestamp()
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.id = ANY($1::uuid[]) AND a.device_id = $2::uuid AND a.archived_at IS NULL`,
		agentIDs, oldID, newID)
	if err != nil {
		return 0, err
	}
	if int(update.RowsAffected()) != len(agentIDs) {
		return 0, ErrVersionConflict
	}
	return len(agentIDs), nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(n int) string { return strconv.Itoa(n) }
