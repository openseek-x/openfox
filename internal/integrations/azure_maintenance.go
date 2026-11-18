package integrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r AzureRuntime) Run(ctx context.Context, logger *slog.Logger) error {
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
			rows, e := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_azure_maintenance($1::uuid,100)`, after)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					return e
				}
				orgs = append(orgs, id)
			}
			return rows.Err()
		})
		if err == nil {
			for _, org := range orgs {
				if ctx.Err() != nil {
					return nil
				}
				if e := r.MaintainOrganization(ctx, org); e != nil {
					logger.Warn("azure_maintenance_failed", "organization_id", org, "code", "maintenance_failed")
				}
			}
			if len(orgs) < 100 {
				after = nil
			} else {
				last := orgs[len(orgs)-1]
				after = &last
			}
		} else if ctx.Err() == nil {
			logger.Warn("azure_maintenance_scan_failed", "code", "database_unavailable")
		}
		timer.Reset(30 * time.Second)
	}
}

func (r AzureRuntime) MaintainOrganization(ctx context.Context, org string) error {
	var parents, expired []string
	err := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `WITH expired AS(SELECT id FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' AND status IN ('PENDING','SELECTING') AND expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 500 FOR UPDATE SKIP LOCKED) UPDATE openfox.integration_oauth_states s SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL FROM expired e WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=e.id`)
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT DISTINCT ON(g.id) c.id::text FROM openfox.integration_azure_grants g JOIN openfox.integration_connections c ON c.organization_id=g.organization_id AND c.azure_grant_id=g.id AND c.status='ACTIVE' WHERE g.organization_id=openfox_private.current_organization_id() AND g.status='ACTIVE' AND g.expires_at<clock_timestamp()+interval '2 minutes' AND (g.retry_at IS NULL OR g.retry_at<=clock_timestamp()) ORDER BY g.id,c.id LIMIT 100`)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			parents = append(parents, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.Query(ctx, `SELECT id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND status='CLEANUP' AND (cleanup_until<=clock_timestamp() OR expires_at<=clock_timestamp()) ORDER BY id LIMIT 100`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			expired = append(expired, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	failures := 0
	for _, grant := range expired {
		if e := r.expireAzureCleanup(ctx, org, grant); e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures++
		}
	}
	if r.Service.Azure != nil {
		for _, parent := range parents {
			if _, e := r.prepare(ctx, org, parent, 0, 0, false, false); e != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				var retry *AzureRetry
				if !errors.As(e, &retry) {
					failures++
				}
			}
		}
	}
	if e := r.pruneAzureBoardsHooks(ctx, org); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failures++
	}
	if e := r.ProcessAzureHooks(ctx, org, 20); e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failures++
	}
	for _, process := range []func(context.Context, string, int) error{r.ProcessAzureRepos, r.ProcessAzureBacklinks} {
		if e := process(ctx, org, 20); e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("azure_maintenance_failed: %d item(s)", failures)
	}
	return nil
}

func (r AzureRuntime) expireAzureCleanup(ctx context.Context, org, grant string) error {
	return r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='CLEANUP' AND (cleanup_until<=clock_timestamp() OR expires_at<=clock_timestamp()) FOR UPDATE`, grant).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND azure_grant_id=$1::uuid ORDER BY id FOR UPDATE`, grant)
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks h SET desired=false,state='NEEDS_ATTENTION',last_error='remote_cleanup_required',secret_sealed=NULL,secret_key_id='',lease=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE h.organization_id=openfox_private.current_organization_id() AND h.state<>'DELETED' AND h.connection_id IN(SELECT id FROM openfox.integration_connections WHERE organization_id=h.organization_id AND azure_grant_id=$1::uuid)`, grant)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET status='DISCONNECTED',credentials_sealed=NULL,credentials_key_id='',expires_at=NULL,generation=generation+1,request_lease=NULL,request_lease_until=NULL,refresh_lease=NULL,refresh_lease_until=NULL,last_error='remote_cleanup_required',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, grant)
		return err
	})
}
