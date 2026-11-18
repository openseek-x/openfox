package imports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Preserve a source fact and the mapping chosen for this import. The activity
// actor remains the actual importer; this never invents task.created attribution.
func recordZohoReporter(ctx context.Context, tx pgx.Tx, b *batchCtx, it pendingItem, task string) error {
	if b.job.Source != SourceZoho || it.Record == nil || it.Record.Reporter.ID == "" {
		return nil
	}
	source := it.Record.Reporter
	name, _ := TruncateRunes(source.Name, 200)
	diff := map[string]any{"source_reporter_id": strings.TrimPrefix(source.ID, "us:"), "source_reporter_region": "US", "source_reporter_name": name}
	resolved := b.resolved.Users[source.ID]
	if resolved.UserID != "" {
		var status, localName string
		e := tx.QueryRow(ctx, `SELECT m.status,coalesce(nullif(u.full_name,''),u.email) FROM openfox.organization_members m JOIN openfox.users u ON u.id=m.user_id WHERE m.organization_id=openfox_private.current_organization_id() AND m.user_id=$1::uuid FOR SHARE OF m,u`, resolved.UserID).Scan(&status, &localName)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var member string
		if e == nil {
			e = tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, b.job.SpaceID, resolved.UserID).Scan(&member)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		if status == "ACTIVE" && b.collab.ImportAssignable(ctx, tx, b.job.SpaceID, b.actor, resolved.UserID) {
			diff["mapped_user_id"] = resolved.UserID
			diff["mapped_user_name"], _ = TruncateRunes(localName, 200)
		} else {
			b.warns.add("zoho_reporter_unavailable", "映射成员已失效或不能访问目标空间，仅保留来源报告人")
		}
	} else if resolved.ExternalAccountID != "" {
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.external_accounts WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='zoho_bugtracker' AND external_user_id=$2 FOR SHARE`, resolved.ExternalAccountID, source.ID).Scan(&id)
		if e != nil {
			return e
		}
		diff["mapped_external_account_id"] = id
	}
	raw, e := json.Marshal(diff)
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.task_activities SET safe_diff=safe_diff||$4::jsonb WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1::uuid AND kind='task.imported' AND actor_id=$2::uuid AND safe_diff->>'source'='zoho_bugtracker' AND safe_diff->>'key'=$3`, task, b.actor.UserID, it.Record.Key, raw)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}
