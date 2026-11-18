package collaboration

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

func RequireUploadedIcon(ctx context.Context, tx pgx.Tx, iconID string) error {
	if !membership.ValidateUUID(iconID) {
		return invalidInput("icon is invalid")
	}
	var found bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM openfox.uploaded_icons
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
	)`, iconID).Scan(&found); err != nil {
		return err
	}
	if !found {
		return invalidInput("icon is invalid")
	}
	return nil
}

func requireSpaceIcon(ctx context.Context, tx pgx.Tx, icon *string) error {
	if icon == nil || !strings.HasPrefix(*icon, "upload:") {
		return nil
	}
	return RequireUploadedIcon(ctx, tx, strings.TrimPrefix(*icon, "upload:"))
}
