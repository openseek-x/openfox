package docs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Revision struct {
	Revision      uint64    `json:"revision"`
	NodeVersion   uint64    `json:"node_version"`
	Title         string    `json:"title"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
	Body          *string   `json:"body,omitempty"`
}

// Restore is a new saved version even if an older snapshot has identical content.
// UpdateNode already holds the node lock for the duration of this transaction.
func SnapshotRestoredRevision(ctx context.Context, tx pgx.Tx, nodeID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO openfox.doc_revisions
	 SELECT n.organization_id, n.id,
	   (SELECT COALESCE(MAX(r.revision), 0) + 1 FROM openfox.doc_revisions r WHERE r.organization_id=n.organization_id AND r.node_id=n.id),
	   n.version, n.title, n.body, n.updated_by, n.updated_at FROM openfox.doc_nodes n
	 WHERE n.organization_id=openfox_private.current_organization_id() AND n.id=$1::uuid
	 AND NOT EXISTS (SELECT 1 FROM openfox.doc_revisions r WHERE r.organization_id=n.organization_id AND r.node_id=n.id AND r.node_version=n.version)`, nodeID)
	return err
}

// ListRevisions returns metadata only; a page cannot multiply the 2 MB body limit.
func ListRevisions(ctx context.Context, tx pgx.Tx, nodeID string, before uint64) ([]Revision, uint64, error) {
	rows, err := tx.Query(ctx, `SELECT r.revision, r.node_version, r.title, r.created_by::text,
	 COALESCE(NULLIF(u.full_name, ''), u.email), r.created_at FROM openfox.doc_revisions r
	 JOIN openfox.users u ON u.id = r.created_by
	 WHERE r.organization_id = openfox_private.current_organization_id() AND r.node_id = $1::uuid
	 AND ($2::bigint = 0 OR r.revision < $2) ORDER BY r.revision DESC LIMIT 51`, nodeID, int64(before))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Revision, 0)
	for rows.Next() {
		var item Revision
		if err := rows.Scan(&item.Revision, &item.NodeVersion, &item.Title, &item.CreatedBy, &item.CreatedByName, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next uint64
	if len(items) > 50 {
		items = items[:50]
		next = items[49].Revision
	}
	return items, next, nil
}

func GetRevision(ctx context.Context, tx pgx.Tx, nodeID string, revision uint64) (Revision, error) {
	var item Revision
	err := tx.QueryRow(ctx, `SELECT r.revision, r.node_version, r.title, r.created_by::text,
	 COALESCE(NULLIF(u.full_name, ''), u.email), r.created_at, r.body FROM openfox.doc_revisions r
	 JOIN openfox.users u ON u.id = r.created_by
	 WHERE r.organization_id = openfox_private.current_organization_id() AND r.node_id = $1::uuid AND r.revision = $2`,
		nodeID, int64(revision)).Scan(&item.Revision, &item.NodeVersion, &item.Title, &item.CreatedBy, &item.CreatedByName, &item.CreatedAt, &item.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return item, err
}
