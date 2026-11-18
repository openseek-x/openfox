package dispatch

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

// variantRunAuthorized 在运行入口按发起人实时权限和物理电脑归属复核父级；不读取明文。
func variantRunAuthorized(ctx context.Context, tx pgx.Tx, run dispatchRun) (bool, error) {
	var parentID *string
	if err := tx.QueryRow(ctx, `SELECT parent_agent_id::text FROM openfox.agents WHERE organization_id=$1::uuid AND id=$2::uuid`, run.OrganizationID, run.AgentID).Scan(&parentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if parentID == nil {
		return true, nil
	}
	readable := strings.ReplaceAll(strings.ReplaceAll(collaboration.SpaceReadableSQL, "$1", "om.role::text"), "openfox_private.current_actor_id()", "$2::uuid")
	var authorized bool
	err := tx.QueryRow(ctx, `SELECT c.status='ACTIVE' AND c.archived_at IS NULL AND p.status='ACTIVE' AND p.archived_at IS NULL AND s.status='ACTIVE'
		AND om.status='ACTIVE' AND (p.visibility='SHARED' OR p.owner_user_id=$2::uuid) AND `+readable+`
		AND (NOT (c.inherited_groups @> ARRAY['computer']::text[])
			OR COALESCE(pc.computer_id=cc.computer_id AND pc.grant_id=cc.grant_id,false))
		AND (COALESCE(p.owner_user_id=c.owner_user_id AND d.owner_user_id=p.owner_user_id,false)
			OR (NOT (c.inherited_groups @> ARRAY['env']::text[]) OR NOT EXISTS
				(SELECT 1 FROM openfox.agent_env_vars e WHERE e.organization_id=c.organization_id AND e.agent_id IN (p.id,c.id)))
			AND (NOT (c.inherited_groups @> ARRAY['repos']::text[]) OR NOT EXISTS
				(SELECT 1 FROM openfox.agent_repos r WHERE r.organization_id=c.organization_id AND r.agent_id IN (p.id,c.id) AND r.connection_id IS NOT NULL))
			AND NOT EXISTS (SELECT 1 FROM openfox.agent_mcp_servers m WHERE m.organization_id=c.organization_id
				AND m.agent_id=p.id AND m.secret_ciphertext IS NOT NULL))
		FROM openfox.agents c
		JOIN openfox.agents p ON p.organization_id=c.organization_id AND p.id=c.parent_agent_id
		JOIN openfox.spaces s ON s.organization_id=p.organization_id AND s.id=p.space_id
		JOIN openfox.organization_members om ON om.organization_id=c.organization_id AND om.user_id=$2::uuid
		JOIN openfox.physical_devices d ON d.id=$3::uuid
		LEFT JOIN openfox.agent_config_versions pc ON pc.organization_id=p.organization_id AND pc.agent_id=p.id AND pc.config_version=p.current_config_version
		LEFT JOIN openfox.agent_config_versions cc ON cc.organization_id=c.organization_id AND cc.agent_id=c.id AND cc.config_version=c.current_config_version
		WHERE c.organization_id=$1::uuid AND c.id=$4::uuid`, run.OrganizationID, run.ActorID, run.ComputerID, run.AgentID).Scan(&authorized)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return authorized, err
}
