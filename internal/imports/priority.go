package imports

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

var (
	ErrPriorityConfirmation     = errors.New("imports: priority upgrade confirmation required")
	ErrPriorityVersionConflict  = errors.New("imports: organization settings changed")
	ErrPriorityLevelUnavailable = errors.New("imports: lowest priority requires five levels")
)

const priorityUnavailableMessage = "目标组织当前为四级优先级，无法保留来源最低档。请由管理员恢复五级后重试；系统不会降档或自动升级。"

type PriorityImpact struct {
	CurrentLevels       int    `json:"current_levels"`
	OrganizationVersion uint64 `json:"organization_version"`
	LowestCount         int    `json:"lowest_count"`
	UpgradeRequired     bool   `json:"upgrade_required"`
	// Applied is saved with the job; CurrentLevels/OrganizationVersion describe the pre-upgrade state.
	UpgradeApplied bool `json:"upgrade_applied"`
}

type PriorityConfirmation struct {
	ConfirmPriorityUpgrade bool   `json:"confirm_priority_upgrade"`
	OrganizationVersion    uint64 `json:"organization_version"`
}

func priorityImpact(levels int, version uint64, count int) PriorityImpact {
	return PriorityImpact{CurrentLevels: levels, OrganizationVersion: version, LowestCount: count, UpgradeRequired: levels == 4 && count > 0}
}

// Match processItem's skip rules, including historical sync mappings and deleted/moved tasks.
// One bounded query for all affected IDs; ignored columns and filtered/invalid rows have no impact.
func analyzePriority(ctx context.Context, tx pgx.Tx, spaceID string, cfg Config, infos []rowInfo, l lookups, provider string) (PriorityImpact, error) {
	ids := make([]string, 0)
	for _, info := range infos {
		if info.Invalid || info.Filtered || info.Epic {
			continue
		}
		if p, _ := MapPriority(info.Data.Priority, l.priorityLevels); p == "LOWEST" {
			ids = append(ids, info.Data.SourceID)
		}
	}
	count := len(ids)
	if count > 0 && spaceID != "" {
		var skipped int
		err := tx.QueryRow(ctx, `
   SELECT count(*) FROM (
    SELECT DISTINCT ON (m.external_id) m.source_instance, m.space_id, t.space_id AS task_space,
           t.deleted_at IS NOT NULL AS deleted
    FROM openfox.import_mappings m JOIN openfox.tasks t ON t.organization_id=m.organization_id AND t.id=m.task_id
    WHERE m.organization_id=openfox_private.current_organization_id() AND m.space_id=$1::uuid
      AND m.source_system=$2 AND (lower(m.source_instance)=$4 OR m.source_instance='')
      AND m.released_at IS NULL AND m.external_id=ANY($3::text[])
    ORDER BY m.external_id,(lower(m.source_instance)=$4) DESC
   ) mapped WHERE NOT $5 OR deleted OR task_space<>space_id OR ($6 AND source_instance='')`,
			spaceID, provider, ids, l.instance, cfg.Options.Overwrite, cfg.Options.ContinuousSync).Scan(&skipped)
		if err != nil {
			return PriorityImpact{}, err
		}
		count -= skipped
	}
	return priorityImpact(l.priorityLevels, l.organizationVersion, count), nil
}

// The caller already holds the job/idempotency lock. Keep this organization lock through
// enqueue, so manual settings changes cannot race the upgrade. Rollback covers both writes.
func confirmPriorityUpgrade(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, impact *PriorityImpact, confirmation PriorityConfirmation) error {
	if impact.LowestCount == 0 {
		return nil
	}
	var levels int
	var version uint64
	if err := tx.QueryRow(ctx, `SELECT priority_levels, version FROM openfox.organizations
 WHERE id=openfox_private.current_organization_id() FOR UPDATE`).Scan(&levels, &version); err != nil {
		return err
	}
	*impact = priorityImpact(levels, version, impact.LowestCount)
	if confirmation.ConfirmPriorityUpgrade && confirmation.OrganizationVersion != version {
		return ErrPriorityVersionConflict
	}
	if !impact.UpgradeRequired {
		return nil
	}
	if !confirmation.ConfirmPriorityUpgrade || confirmation.OrganizationVersion == 0 {
		return ErrPriorityConfirmation
	}
	five := 5
	if _, _, err := (collaboration.Service{}).UpdateOrganizationSettings(ctx, tx, org, actor, collaboration.OrganizationPatch{Version: version, PriorityLevels: &five}); err != nil {
		return err
	}
	impact.UpgradeApplied = true
	return nil
}

// Acquire before task/project locks, following the existing organization 5→4 lock order.
func lockPriorityLevels(ctx context.Context, tx pgx.Tx) (int, error) {
	var levels int
	err := tx.QueryRow(ctx, `SELECT priority_levels FROM openfox.organizations WHERE id=openfox_private.current_organization_id() FOR SHARE`).Scan(&levels)
	return levels, err
}
