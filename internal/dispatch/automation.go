package dispatch

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/automations"
	"openfox/internal/tenancy"
)

const automationScanBatch = 100

// RunAutomations 处理所有到期的定时触发器与待处理的自动化事件，与 RunIterations 同一节拍。
// 每个组织：先在组织 worker 事务里只读地列出待启动的运行；再逐项在创建者身份的事务里启动（会话与消息以创建者写入）；
// 最后在组织事务里标记已处理的事件。单项失败只记日志，事件保留到下一周期重试（dedupe_key 保证不重复）。
func (w *Worker) RunAutomations(ctx context.Context) error {
	service := automations.Service{Now: w.now}
	cursor := ""
	for {
		ids, err := w.listAutomationOrganizations(ctx, cursor)
		if err != nil {
			return err
		}
		for _, organizationID := range ids {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.runAutomationsIn(ctx, service, organizationID)
		}
		if len(ids) < automationScanBatch {
			return nil
		}
		cursor = ids[len(ids)-1]
	}
}

func (w *Worker) runAutomationsIn(ctx context.Context, service automations.Service, organizationID string) {
	var pending []automations.PendingRun
	var events []string
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		pending, events, err = service.PendingWork(ctx, tx)
		return err
	}); err != nil {
		w.logger().Warn("automation_scan_organization_failed", "organization_id", organizationID, "error", err.Error())
		return
	}
	failedEvents := map[string]bool{}
	for _, p := range pending {
		selection := CreatorSelection(organizationID, p)
		err := w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
				return selection, nil
			}), func(ctx context.Context, tx pgx.Tx) error {
				return service.ApplyPending(ctx, tx, organizationID, p)
			})
		if err != nil {
			failedEvents[p.EventID] = true
			w.logger().Warn("automation_run_failed", "organization_id", organizationID, "automation_id", p.AutomationID, "error", err.Error())
		}
	}
	var processed []string
	for _, id := range events {
		if !failedEvents[id] {
			processed = append(processed, id)
		}
	}
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		return automations.MarkEventsProcessed(ctx, tx, processed, w.now())
	}); err != nil {
		w.logger().Warn("automation_mark_events_failed", "organization_id", organizationID, "error", err.Error())
	}
}

// CreatorSelection 返回以创建者身份打开的组织事务选择，供派发与 Webhook 接收端使用。
// 创建者版本未知时用 1，运行会因非活跃成员记为 failed。
func CreatorSelection(organizationID string, p automations.PendingRun) tenancy.OrganizationSelection {
	version := p.CreatorVersion
	if version == 0 {
		version = 1
	}
	return tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: p.CreatorID, AuthorizationVersion: version}
}

func (w *Worker) listAutomationOrganizations(ctx context.Context, after string) ([]string, error) {
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var cursor any
		if after != "" {
			cursor = after
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_automation_due_organizations($1::uuid, $2)`, cursor, automationScanBatch)
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
