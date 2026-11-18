package collaboration

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Invitations live in a separate, admin-only relation. Legacy replacements
// continue to work on HUMAN/AGENT/CREW and cannot delete hidden reservations.
func syncInvitationAssignees(ctx context.Context, tx pgx.Tx, space, task string, actor Actor, current []TaskAssignee, desired []TaskAssigneeRef) ([]TaskAssignee, []TaskAssigneeRef, error) {
	currentRegular := make([]TaskAssignee, 0, len(current))
	desiredRegular := make([]TaskAssigneeRef, 0, len(desired))
	var oldIDs, newIDs []string
	for _, ref := range current {
		if ref.Kind == "INVITATION" {
			oldIDs = append(oldIDs, ref.ID)
		} else {
			currentRegular = append(currentRegular, ref)
		}
	}
	for _, ref := range desired {
		if ref.Kind == "INVITATION" {
			newIDs = append(newIDs, ref.ID)
		} else {
			desiredRegular = append(desiredRegular, ref)
		}
	}
	if (len(oldIDs) > 0 || len(newIDs) > 0) && !canManageOrganization(actor.Role) {
		return nil, nil, ErrOrganizationForbidden
	}
	var removed, added []string
	for _, id := range oldIDs {
		if !slices.Contains(newIDs, id) {
			removed = append(removed, id)
		}
	}
	for _, id := range newIDs {
		if !slices.Contains(oldIDs, id) {
			added = append(added, id)
		}
	}
	if len(added)+len(removed) > 0 {
		// Acceptance takes the invitation first, while a task edit already has
		// the task lock. NOWAIT turns the inverse lock order into a stale edit.
		var valid int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM (
			SELECT i.id FROM openfox.invitations i
			JOIN openfox.spaces s ON s.organization_id=i.organization_id AND s.id=$2::uuid
			LEFT JOIN openfox.users u ON u.normalized_email=i.normalized_email
			LEFT JOIN openfox.space_members sm ON sm.organization_id=i.organization_id AND sm.space_id=s.id AND sm.user_id=u.id
			WHERE i.organization_id=openfox_private.current_organization_id() AND i.id=ANY($1::uuid[])
			  AND (i.id=ANY($3::uuid[]) OR (i.status='PENDING' AND i.expires_at>clock_timestamp()
			    AND s.deleted_at IS NULL AND s.status='ACTIVE' AND
			    (s.id=ANY(i.space_ids) OR sm.status='ACTIVE' OR (s.visibility='SHARED' AND i.role<>'GUEST' AND sm.status IS NULL))))
			ORDER BY i.id FOR SHARE OF i NOWAIT
		) locked`, append(slices.Clone(added), removed...), space, removed).Scan(&valid)
		if err != nil {
			var dbErr *pgconn.PgError
			if errors.As(err, &dbErr) && dbErr.Code == "55P03" {
				return nil, nil, ErrVersionConflict
			}
			return nil, nil, err
		}
		if valid != len(added)+len(removed) {
			return nil, nil, errAssigneeNotAllowed
		}
		if len(removed) > 0 {
			_, err = tx.Exec(ctx, `DELETE FROM openfox.task_invitation_assignees WHERE organization_id=openfox_private.current_organization_id()
				AND task_id=$1::uuid AND invitation_id=ANY($2::uuid[])`, task, removed)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(added) > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO openfox.task_invitation_assignees(organization_id,task_id,invitation_id,created_by)
				SELECT openfox_private.current_organization_id(),$1::uuid,id,$2::uuid FROM unnest($3::uuid[]) id`, task, actor.UserID, added)
			if err != nil {
				return nil, nil, mapDatabaseError(err)
			}
		}
	}
	var fits bool
	if err := tx.QueryRow(ctx, `SELECT openfox_private.task_assignees_fit($1::uuid,$2)`, task, len(desiredRegular)).Scan(&fits); err != nil {
		return nil, nil, err
	}
	if !fits {
		return nil, nil, errTooManyAssignees
	}
	return currentRegular, desiredRegular, nil
}

// Only transient queries may contain an invitation reference. Views can be
// shared, so the persisted-filter parser continues to reject such references.
func ParseActorTaskFilter(v url.Values, actor Actor) (TaskFilter, error) {
	f, err := parseTaskFilter(v, true)
	if err != nil {
		return TaskFilter{}, err
	}
	if !canManageOrganization(actor.Role) && (hasInvitationAssignee(f.Assignees) || hasInvitationAssignee(f.AssigneesNot)) {
		return TaskFilter{}, ErrOrganizationForbidden
	}
	return f, nil
}

func hasInvitationAssignee(values []string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.HasPrefix(v, "invitation:") })
}
