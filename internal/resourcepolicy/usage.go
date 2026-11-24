package resourcepolicy

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Usage supplies the queue-full notice. QueueMax is only exposed to organization admins.
type Usage struct {
	OrganizationID string `json:"organization_id"`
	Queued         int    `json:"queued"`
	QueueMax       int    `json:"queue_max,omitempty"`
	QueueFull      bool   `json:"queue_full"`
}

func (s Store) GetUsage(ctx context.Context, tx pgx.Tx, organizationID string) (Usage, error) {
	config, err := s.GetConfig(ctx, tx, organizationID)
	if err != nil {
		return Usage{}, err
	}
	usage := Usage{OrganizationID: organizationID, QueueMax: config.Policy.QueueMax}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.runs
		WHERE organization_id = openfox_private.current_organization_id() AND state = 'QUEUED'`).Scan(&usage.Queued); err != nil {
		return Usage{}, err
	}
	usage.QueueFull = usage.Queued >= usage.QueueMax
	return usage, nil
}
