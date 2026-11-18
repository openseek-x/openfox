package resourcepolicy

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Usage 是组织运行配额的当前用量（阶段 12e，只读）。
//
// 活动运行的实际上限是「平台槽位（organization_capacity.max_slots）」与「管理员设置的
// active_max」中的较小值（见迁移 000002 的 reserve_capacity 与 000015 的触发器）；
// 调高 active_max 不会抬高平台槽位，所以这里把两者与生效值一并返回。
type Usage struct {
	OrganizationID string `json:"organization_id"`
	ConfigVersion  int64  `json:"config_version"`
	// Queued 是处于 QUEUED 的 Run 数，与派发物化输入时的队列判定口径一致。
	Queued   int `json:"queued"`
	QueueMax int `json:"queue_max"`
	// Active 是占用活动槽位的预留数（RESERVED、CLAIMED），与容量触发器的口径一致。
	Active    int `json:"active"`
	ActiveMax int `json:"active_max"`
	// CapacityMax 是平台槽位上限。
	CapacityMax int `json:"capacity_max"`
	// EffectiveActiveMax = min(ActiveMax, CapacityMax)。
	EffectiveActiveMax int `json:"effective_active_max"`
	// QueueFull 为 true 时新消息不会进入队列，会保持等待直到队列有空位。
	QueueFull bool `json:"queue_full"`
}

// GetUsage 返回当前组织的配额与用量。必须在组织租户事务内调用。
// 三个计数各是一条按组织过滤的聚合查询；结果是读取时刻的快照，仅用于展示与提示，不参与准入判定。
func (s Store) GetUsage(ctx context.Context, tx pgx.Tx, organizationID string) (Usage, error) {
	config, err := s.GetConfig(ctx, tx, organizationID)
	if err != nil {
		return Usage{}, err
	}
	usage := Usage{
		OrganizationID: organizationID, ConfigVersion: config.ConfigVersion,
		QueueMax: config.Policy.QueueMax, ActiveMax: config.Policy.ActiveMax,
	}
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM openfox.runs
		         WHERE organization_id = openfox_private.current_organization_id() AND state = 'QUEUED')::int,
		       (SELECT count(*) FROM openfox.capacity_reservations
		  WHERE organization_id = openfox_private.current_organization_id()
		           AND status IN ('RESERVED', 'CLAIMED'))::int,
		COALESCE((SELECT max_slots FROM openfox.organization_capacity
		         WHERE organization_id = openfox_private.current_organization_id()), 0)::int`).Scan(
		&usage.Queued, &usage.Active, &usage.CapacityMax); err != nil {
		return Usage{}, err
	}
	usage.EffectiveActiveMax = min(usage.ActiveMax, usage.CapacityMax)
	usage.QueueFull = usage.Queued >= usage.QueueMax
	return usage, nil
}
