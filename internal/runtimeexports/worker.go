package runtimeexports

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/tenancy"
)

const (
	cleanupInterval = 5 * time.Minute
	cleanupBatch    = 100
	cleanupActorID  = "00000000-0000-0000-0000-000000000000"
)

type Runner interface {
	RunControlPlane(context.Context, func(context.Context, pgx.Tx) error) error
	RunForOrganization(context.Context, string, tenancy.Source, tenancy.OrganizationSelector, func(context.Context, pgx.Tx) error) error
}

// Worker deletes expired ciphertext objects, then removes their tenant rows.
// Failed object deletions remain indexed for a later retry.
type Worker struct {
	Pool     Runner
	Storage  artifacts.Storage
	Logger   *slog.Logger
	Interval time.Duration

	afterOrganization string
	afterID           string
}

type expiredExport struct {
	organizationID string
	id             string
	storageKey     string
}

func (w *Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = cleanupInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("runtime_session_export_cleanup_failed", "error", err.Error())
		}
		timer.Reset(interval)
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("runtimeexports: nil cleanup pool")
	}
	if w.Storage == nil {
		return errors.New("runtimeexports: nil cleanup storage")
	}
	var items []expiredExport
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id::text, id::text, storage_key
			FROM openfox_private.list_expired_runtime_session_exports($1::uuid, $2::uuid, $3)`,
			nilIfEmpty(w.afterOrganization), nilIfEmpty(w.afterID), cleanupBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item expiredExport
			if err := rows.Scan(&item.organizationID, &item.id, &item.storageKey); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.storageKey != "" {
			if err := w.Storage.Delete(ctx, item.storageKey); err != nil {
				w.logger().Warn("runtime_session_export_object_delete_failed", "organization_id", item.organizationID, "export_id", item.id, "error", err.Error())
				w.advance(item)
				continue
			}
		}
		selection := tenancy.OrganizationSelection{OrganizationID: item.organizationID, ActorID: cleanupActorID, AuthorizationVersion: 1}
		err := w.Pool.RunForOrganization(ctx, item.organizationID, tenancy.SourceOrganizationWorker,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
			func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `DELETE FROM openfox.runtime_session_exports
					WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
					  AND expires_at<=clock_timestamp()`, item.id)
				return err
			})
		if err != nil {
			w.logger().Warn("runtime_session_export_row_delete_failed", "organization_id", item.organizationID, "export_id", item.id, "error", err.Error())
		}
		w.advance(item)
	}
	if len(items) < cleanupBatch {
		w.afterOrganization, w.afterID = "", ""
	}
	return nil
}

func (w *Worker) advance(item expiredExport) {
	w.afterOrganization, w.afterID = item.organizationID, item.id
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
