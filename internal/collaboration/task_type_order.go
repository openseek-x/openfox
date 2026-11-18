package collaboration

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/membership"
)

// All type CRUD takes the space lock before type rows. NOWAIT on the ordered
// type locks also avoids waiting cycles with organization-wide workflow edits.
func lockTaskTypeSpace(ctx context.Context, tx pgx.Tx, spaceID string) error {
	if !membership.ValidateUUID(spaceID) {
		return ErrSpaceNotFound
	}
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id()
		AND id = $1::uuid AND deleted_at IS NULL FOR UPDATE`, spaceID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceNotFound
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM openfox.task_types
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		ORDER BY id FOR UPDATE NOWAIT`, spaceID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return ErrVersionConflict
		}
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return ErrVersionConflict
		}
		return err
	}
	return nil
}

// NextTaskTypePosition normalizes legacy NULLs in their displayed order before
// appending. It is shared by ordinary creation and fresh-space seed/template paths.
func NextTaskTypePosition(ctx context.Context, tx pgx.Tx, spaceID string) (int64, error) {
	if err := lockTaskTypeSpace(ctx, tx, spaceID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `WITH tail AS (
		SELECT COALESCE(max(position), -1) AS last FROM openfox.task_types
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
	), legacy AS (
		SELECT id, (SELECT last FROM tail) + row_number() OVER (ORDER BY created_at, id) AS position
		FROM openfox.task_types WHERE organization_id = openfox_private.current_organization_id()
		AND space_id = $1::uuid AND position IS NULL
	) UPDATE openfox.task_types t SET position = legacy.position FROM legacy
	WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = legacy.id`, spaceID); err != nil {
		return 0, err
	}
	var next int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(position), -1) + 1 FROM openfox.task_types
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID).Scan(&next)
	return next, err
}

type TaskTypeOrderItem struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

func (s Service) ReorderTaskTypes(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input []TaskTypeOrderItem) ([]TaskType, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if err := lockTaskTypeSpace(ctx, tx, spaceID); err != nil {
		return nil, err
	}
	ids := make([]string, len(input))
	expected := make(map[string]uint64, len(input))
	for i, item := range input {
		id := strings.ToLower(item.ID)
		if !membership.ValidateUUID(id) || item.Version == 0 || expected[id] != 0 {
			return nil, invalidInput("invalid task type order")
		}
		ids[i], expected[id] = id, item.Version
	}
	rows, err := tx.Query(ctx, `SELECT id::text, version, archived_at IS NOT NULL FROM openfox.task_types
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		ORDER BY position NULLS LAST, created_at, id`, spaceID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return nil, ErrVersionConflict
		}
		return nil, err
	}
	count := 0
	for rows.Next() {
		var id string
		var version uint64
		var archived bool
		if err := rows.Scan(&id, &version, &archived); err != nil {
			rows.Close()
			return nil, err
		}
		if expected[id] != version {
			rows.Close()
			return nil, ErrVersionConflict
		}
		if archived && (count >= len(ids) || ids[count] != id) {
			rows.Close()
			return nil, invalidInput("archived task types must keep their slots")
		}
		count++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count != len(ids) {
		return nil, invalidInput("order must contain every task type in the space")
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.task_types t SET position = x.n - 1, version = t.version + 1
		FROM unnest($2::uuid[]) WITH ORDINALITY AS x(id, n)
		WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
		AND t.id = x.id AND t.position IS DISTINCT FROM x.n - 1`, spaceID, ids); err != nil {
		return nil, err
	}
	return s.ListTaskTypes(ctx, tx, organizationID, spaceID, actor)
}
