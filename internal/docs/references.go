package docs

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// ReadableReference contains only metadata needed for a passive conversation card.
// Never load document bodies, file keys, attachments or download URLs here.
type ReadableReference struct {
	ID, SpaceID, Kind, Title string
}

// LoadReadableReferences resolves at most one message page. Like SearchAcrossSpaces,
// it enforces both personal ownership and current space access, including GUEST.
func LoadReadableReferences(ctx context.Context, tx pgx.Tx, role string, ids []string) (map[string]ReadableReference, error) {
	if len(ids) > 100 {
		return nil, invalidf("too many document references")
	}
	result := make(map[string]ReadableReference, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,n.space_id::text,n.kind,n.title
		FROM openfox.doc_nodes n JOIN openfox.spaces s ON s.organization_id=n.organization_id AND s.id=n.space_id
		WHERE n.organization_id=openfox_private.current_organization_id() AND n.id=ANY($2::uuid[])
		AND n.kind IN ('doc','sheet','file')
		AND (n.set_id IS NOT NULL OR n.owner_user_id=openfox_private.current_actor_id())
		AND `+collaboration.SpaceReadableSQL, role, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ReadableReference
		if err := rows.Scan(&item.ID, &item.SpaceID, &item.Kind, &item.Title); err != nil {
			return nil, err
		}
		result[item.ID] = item
	}
	return result, rows.Err()
}
