package integrations

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"time"
)

// Run maintains expiring grants even when no import is currently queued, and
// removes abandoned OAuth session ciphertext. Every write uses tenant context.
func (r AsanaRuntime) Run(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	var after *string
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		var orgs []string
		err := r.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, e := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_asana_maintenance($1::uuid,100)`, after)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var org string
				if e = rows.Scan(&org); e != nil {
					return e
				}
				orgs = append(orgs, org)
			}
			return rows.Err()
		})
		if err == nil {
			for _, org := range orgs {
				if ctx.Err() != nil {
					return nil
				}
				if e := r.MaintainOrganization(ctx, org); e != nil {
					logger.Warn("asana_maintenance_failed", "organization_id", org, "error", e.Error())
				}
			}
			if len(orgs) < 100 {
				after = nil
			} else {
				last := orgs[len(orgs)-1]
				after = &last
			}
		} else if ctx.Err() == nil {
			logger.Warn("asana_maintenance_scan_failed", "error", err.Error())
		}
		timer.Reset(30 * time.Second)
	}
}
func (r AsanaRuntime) MaintainOrganization(ctx context.Context, org string) error {
	var connections []string
	err := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `WITH expired AS(SELECT id FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND provider IN ('asana','zoho_bugtracker') AND status IN ('PENDING','SELECTING') AND expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED) UPDATE openfox.integration_oauth_states s SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL FROM expired e WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=e.id`)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT DISTINCT ON(g.id) c.id::text FROM openfox.integration_asana_grants g JOIN openfox.integration_connections c ON c.organization_id=g.organization_id AND c.asana_grant_id=g.id AND c.status='ACTIVE' WHERE g.organization_id=openfox_private.current_organization_id() AND g.status='ACTIVE' AND g.expires_at<clock_timestamp()+interval '5 minutes' AND (g.retry_at IS NULL OR g.retry_at<=clock_timestamp()) ORDER BY g.id,c.id LIMIT 100`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			connections = append(connections, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	if r.Service.Asana == nil {
		return nil
	}
	failures := 0
	for _, id := range connections {
		if _, e := r.prepare(ctx, org, id, 0, 0, false, false); e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var retry *AsanaRetry
			if !errors.As(e, &retry) {
				failures++
			}
		}
	}
	if failures > 0 {
		return fmt.Errorf("asana_maintenance_grant_failed: %d grant(s)", failures)
	}
	return nil
}
