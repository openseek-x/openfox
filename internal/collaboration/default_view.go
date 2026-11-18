package collaboration

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// The caller holds the space lock. This order also serializes setting a default
// with a view becoming personal or being deleted, without changing view ACLs.
func validateDefaultView(ctx context.Context, tx pgx.Tx, spaceID, viewID string, enabled bool) (string, error) {
	viewID = strings.ToLower(strings.TrimSpace(viewID))
	if !membership.ValidateUUID(viewID) {
		return "", ErrViewNotFound
	}
	if !enabled {
		return "", ErrViewsDisabled
	}
	var found string
	err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.views
		WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid
		AND id=$2::uuid AND visibility='space' FOR UPDATE`, spaceID, viewID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrViewNotFound
	}
	return found, err
}

func (s Service) lockViewSpace(ctx context.Context, tx pgx.Tx, viewID string) error {
	spaceID, err := s.ViewSpaceID(ctx, tx, viewID)
	if err != nil {
		return err
	}
	return lockSpaceRow(ctx, tx, spaceID)
}

// A view author retains their existing delete/personalize permission. Clearing
// this one dependent pointer is not a grant to manage other space settings.
func clearDefaultView(ctx context.Context, tx pgx.Tx, spaceID, viewID string) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.spaces
		SET default_view_id=NULL,version=version+1,updated_at=clock_timestamp()
		WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND default_view_id=$2::uuid`, spaceID, viewID)
	return err
}
