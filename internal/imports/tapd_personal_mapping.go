package imports

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Nicknames are exact provider IDs here. Never split a multi-owner value or
// use the case-folded external_accounts key as proof of verified identity.
func loadTAPDVerifiedBindings(ctx context.Context, tx pgx.Tx, origin ProjectOrigin, ids []string, lock bool) (map[string]VerifiedBinding, error) {
	out := map[string]VerifiedBinding{}
	parentLock := ""
	if lock {
		parentLock = " FOR SHARE"
	}
	var company string
	var parentVersion uint64
	err := tx.QueryRow(ctx, `SELECT settings->>'company_id',version FROM openfox.integration_connections
	 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='tapd' AND scope='ORGANIZATION'
	 AND status='ACTIVE' AND settings->>'auth_method'='basic'`+parentLock, origin.Connection).Scan(&company, &parentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	childLock := ""
	if lock {
		childLock = " FOR SHARE OF c,m,u"
	}
	rows, err := tx.Query(ctx, `SELECT c.settings->>'nick',c.id::text,c.owner_user_id::text,c.version
	 FROM openfox.integration_connections c
	 JOIN openfox.organization_members m ON m.organization_id=c.organization_id AND m.user_id=c.owner_user_id
	 JOIN openfox.users u ON u.id=m.user_id
	 WHERE c.organization_id=openfox_private.current_organization_id() AND c.provider='tapd' AND c.scope='PERSONAL'
	 AND c.parent_connection_id=$1::uuid AND c.status='ACTIVE' AND c.account_kind='identity'
	 AND c.settings->>'auth_method'='tapd_identity' AND c.settings->>'company_id'=$2 AND c.settings->>'parent_version'=$3
	 AND c.settings->>'verified_email'=u.normalized_email AND c.settings->'projects' ? $4
	 AND m.status='ACTIVE' AND m.role<>'GUEST' AND c.settings->>'nick'=ANY($5::text[])
	 ORDER BY c.id`+childLock, origin.Connection, company, strconv.FormatUint(parentVersion, 10), origin.Project, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var binding VerifiedBinding
		if err := rows.Scan(&source, &binding.ID, &binding.UserID, &binding.Version); err != nil {
			return nil, err
		}
		out[source] = binding
	}
	return out, rows.Err()
}
