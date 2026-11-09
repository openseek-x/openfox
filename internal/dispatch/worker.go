package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/tenancy"
)

// WorkerActorID 是组织 worker 上下文使用的系统主体，与 000019 回填一致。
const WorkerActorID = "00000000-0000-0000-0000-000000000000"

// Runner 是 worker 需要的最小数据库边界，便于测试替换。
type Runner interface {
	RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error
	RunForOrganization(ctx context.Context, organizationID string, source tenancy.Source,
		selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error
}

// Worker 周期性扫描有派发工作的组织。每个组织的每个阶段使用独立短事务，
// 单组织失败只记录日志，不阻塞其他组织（组织之间公平轮转）。
type Worker struct {
	Pool   Runner
	Store  Store
	Hub    *Hub
	Logger *slog.Logger
	Now    func() time.Time

	cursor string
	// lastEventPurge 是上次清理组织事件的时间，清理最多每 eventPurgeInterval 一次。
	lastEventPurge time.Time
}

const (
	eventPurgeInterval = time.Minute
	// 每次清理最多 eventPurgeBatches 批、每批 eventPurgeBatchSize 行，单批是一个短事务。
	eventPurgeBatchSize = 1000
	eventPurgeBatches   = 10
	// deviceCommandPurgeBatchSize 是每个清理周期删除远程命令的上限（函数允许的最大值）。
	deviceCommandPurgeBatchSize = 1000
)

// maxPassBackoff 是整轮扫描连续失败（例如数据库不可用、迁移未应用）时的最长等待。
const maxPassBackoff = 30 * time.Second

// Run 按 PollInterval 循环，直到 ctx 取消。整轮失败时等待时间逐次翻倍（上限 maxPassBackoff），
// 成功一轮后恢复 PollInterval，避免故障期间每秒刷错误日志。
func (w *Worker) Run(ctx context.Context) error {
	interval := w.Store.config().PollInterval
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		_, err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			failures++
			wait := passBackoff(interval, failures)
			w.logger().Warn("dispatch_pass_failed", "error", err.Error(), "consecutive_failures", failures, "retry_in", wait.String())
			timer.Reset(wait)
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// passBackoff 返回第 failures 次连续失败后的等待时间：interval × 2^(failures-1)，不超过上限。
func passBackoff(interval time.Duration, failures int) time.Duration {
	wait := interval
	for i := 1; i < failures && wait < maxPassBackoff; i++ {
		wait *= 2
	}
	if wait > maxPassBackoff {
		wait = maxPassBackoff
	}
	return wait
}

// RunOnce 执行一轮扫描并在每个组织提交后推送 offer 唤醒。
func (w *Worker) RunOnce(ctx context.Context) (PassResult, error) {
	var total PassResult
	if w.Pool == nil {
		return total, errors.New("dispatch: nil worker pool")
	}
	config := w.Store.config()
	if time.Since(w.lastEventPurge) >= eventPurgeInterval {
		w.lastEventPurge = time.Now()
		// 清理失败只告警，不影响派发；下一个周期重试。
		if _, err := w.PurgeOrganizationEvents(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("organization_event_purge_failed", "error", err.Error())
		}
		if _, err := w.PurgeDeviceCommands(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("device_command_purge_failed", "error", err.Error())
		}
		if err := w.RunIterations(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("iteration_schedule_scan_failed", "error", err.Error())
		}
		if err := w.RunAutomations(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("automation_scan_failed", "error", err.Error())
		}
	}
	organizations, err := w.listOrganizations(ctx, config.BatchSize)
	if err != nil {
		return total, err
	}
	for _, organizationID := range organizations {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		result, err := w.RunOrganization(ctx, organizationID)
		total.add(result)
		if err != nil {
			w.logger().Warn("dispatch_organization_failed", "organization_id", organizationID, "error", err.Error())
		}
		w.cursor = organizationID
	}
	if len(organizations) < config.BatchSize {
		// 本轮已覆盖全部候选，下轮从头开始。
		w.cursor = ""
	}
	return total, nil
}

// PurgeOrganizationEvents 删除超过 7 天保留期的组织事件（跨组织，见 000029 末尾的 purge_organization_events），返回删除行数。
func (w *Worker) PurgeOrganizationEvents(ctx context.Context) (int, error) {
	total := 0
	for batch := 0; batch < eventPurgeBatches; batch++ {
		var deleted int
		if err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT openfox_private.purge_organization_events($1)`, eventPurgeBatchSize).Scan(&deleted)
		}); err != nil {
			return total, err
		}
		total += deleted
		if deleted < eventPurgeBatchSize {
			break
		}
	}
	return total, nil
}

// PurgeDeviceCommands 删除 7 天前的远程命令（跨组织，见 000032 末尾的 purge_device_commands），每次最多一批。
func (w *Worker) PurgeDeviceCommands(ctx context.Context) (int, error) {
	var deleted int
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT openfox_private.purge_device_commands($1)`, deviceCommandPurgeBatchSize).Scan(&deleted)
	})
	return deleted, err
}

func (w *Worker) listOrganizations(ctx context.Context, limit int) ([]string, error) {
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_dispatch_organizations($1::uuid, $2)`, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// RunOrganization 依次执行孤儿预留回收、offer 回收、租约过期、排队超时、物化输入与发 offer。
// 顺序保证先释放容量再预留，并让刚物化的 Run 在同一轮获得 offer。
func (w *Worker) RunOrganization(ctx context.Context, organizationID string) (PassResult, error) {
	var result PassResult
	var orphans []OrphanReservation
	limit := w.Store.config().BatchSize
	phases := []struct {
		name string
		fn   func(context.Context, pgx.Tx) error
	}{
		{"reclaim_orphans", func(ctx context.Context, tx pgx.Tx) (err error) {
			orphans, err = w.Store.ReclaimOrphanReservations(ctx, tx, organizationID, limit)
			result.OrphansReclaimed = len(orphans)
			return err
		}},
		{"reap_offers", func(ctx context.Context, tx pgx.Tx) (err error) {
			result.OffersReaped, err = w.Store.ReapExpiredOffers(ctx, tx, organizationID, limit)
			return err
		}},
		{"expire_leases", func(ctx context.Context, tx pgx.Tx) (err error) {
			result.LeasesExpired, err = w.Store.ExpireLeases(ctx, tx, organizationID, limit)
			return err
		}},
		{"expire_queued", func(ctx context.Context, tx pgx.Tx) (err error) {
			result.QueueExpired, err = w.Store.ExpireQueued(ctx, tx, organizationID, limit)
			return err
		}},
		{"materialize_inputs", func(ctx context.Context, tx pgx.Tx) (err error) {
			result.RunsCreated, result.InputsRejected, err = w.Store.MaterializeInputs(ctx, tx, organizationID, limit)
			return err
		}},
		{"offer_queued", func(ctx context.Context, tx pgx.Tx) error {
			offers, err := w.Store.OfferQueued(ctx, tx, organizationID, limit)
			if err != nil {
				return err
			}
			result.Offers = offers
			result.OffersCreated = len(offers)
			return nil
		}},
	}
	// 指派事件先于物化输入处理：指派后发的消息在同一轮就能变成 Run。
	w.runTaskAgentEvents(ctx, organizationID)
	var firstErr error
	for _, phase := range phases {
		if err := w.inOrganization(ctx, organizationID, phase.fn); err != nil {
			// 回滚的阶段计数作废，避免汇总中出现未提交的结果。
			resetPhase(&result, phase.name)
			if phase.name == "reclaim_orphans" {
				orphans = nil
			}
			if firstErr == nil {
				firstErr = err
			}
			w.logger().Warn("dispatch_phase_failed", "organization_id", organizationID, "phase", phase.name, "error", err.Error())
		}
	}
	// 回收只在异常残留时发生，逐条告警，便于追查来源。
	for _, orphan := range orphans {
		w.logger().Warn("dispatch_orphan_reservation_released", "organization_id", organizationID,
			"reservation_id", orphan.ReservationID, "run_id", orphan.RunID, "reason", orphan.Reason,
			"counters_consistent", orphan.CountersConsistent)
	}
	for _, offer := range result.Offers {
		w.Hub.PublishOffer(offer)
	}
	return result, firstErr
}

func (w *Worker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: WorkerActorID, AuthorizationVersion: 1}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

func resetPhase(result *PassResult, phase string) {
	switch phase {
	case "reclaim_orphans":
		result.OrphansReclaimed = 0
	case "reap_offers":
		result.OffersReaped = 0
	case "expire_leases":
		result.LeasesExpired = 0
	case "expire_queued":
		result.QueueExpired = 0
	case "materialize_inputs":
		result.RunsCreated, result.InputsRejected = 0, 0
	case "offer_queued":
		result.Offers, result.OffersCreated = nil, 0
	}
}

func (r *PassResult) add(other PassResult) {
	r.RunsCreated += other.RunsCreated
	r.InputsRejected += other.InputsRejected
	r.OffersCreated += other.OffersCreated
	r.OffersReaped += other.OffersReaped
	r.QueueExpired += other.QueueExpired
	r.LeasesExpired += other.LeasesExpired
	r.OrphansReclaimed += other.OrphansReclaimed
	r.Offers = append(r.Offers, other.Offers...)
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
