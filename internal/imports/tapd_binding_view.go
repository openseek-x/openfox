package imports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type TAPDBindingDetails struct {
	Nick       string    `json:"nick"`
	CompanyID  string    `json:"company_id"`
	Projects   []string  `json:"projects"`
	VerifiedAt time.Time `json:"verified_at"`
}

func loadTAPDAccountBindingInstances(ctx context.Context, tx pgx.Tx, userID string) ([]AccountBindingInstance, error) {
	rows, err := tx.Query(ctx, `SELECT p.id::text,p.display_name,COALESCE(p.settings->>'company_id',''),
	 p.status='ACTIVE' AND p.settings->>'auth_method'='basic',
	 COALESCE(c.id::text,''),COALESCE(c.display_name,''),COALESCE(c.version,0),COALESCE(c.created_at,p.created_at),COALESCE(c.settings,'{}'::jsonb)
	 FROM openfox.integration_connections p LEFT JOIN openfox.integration_connections c
	 ON c.organization_id=p.organization_id AND c.parent_connection_id=p.id AND c.owner_user_id=$1::uuid
	 AND c.provider='tapd' AND c.scope='PERSONAL' AND c.status<>'DISCONNECTED' AND c.settings->>'auth_method'='tapd_identity'
	 WHERE p.organization_id=openfox_private.current_organization_id() AND p.provider='tapd' AND p.scope='ORGANIZATION'
	 AND p.status<>'DISCONNECTED' ORDER BY p.created_at,p.id LIMIT $2`, userID, maxBindingInstances)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountBindingInstance{}
	for rows.Next() {
		var in AccountBindingInstance
		var binding AccountBinding
		var supported bool
		var raw []byte
		if err := rows.Scan(&in.ConnectionID, &in.DisplayName, &in.Host, &supported, &binding.ConnectionID, &binding.DisplayName, &binding.Version, &binding.CreatedAt, &raw); err != nil {
			return nil, err
		}
		in.IdentitySupported = &supported
		if binding.ConnectionID != "" {
			var detail TAPDBindingDetails
			if err := json.Unmarshal(raw, &detail); err != nil {
				return nil, err
			}
			binding.TAPDIdentity = &detail
			binding.Detail = detail.Nick
			in.Binding = &binding
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
