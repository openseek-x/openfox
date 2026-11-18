package agents

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"openfox/internal/collaboration"
)

var ErrHermesConfiguration = collaboration.NewRuleError(http.StatusUnprocessableEntity, "RUNTIME_CONFIGURATION_UNSUPPORTED", "Hermes supports read-only runs with a public HTTPS model endpoint and declared context capacity; MCP, skills, custom arguments and native extensions are not supported")

// Use the existing Agent row lock, also held by MCP/skill/env mutations, when
// changing runtime. The immutable configuration cannot race extension attachment.
func validateHermesAttachments(ctx context.Context, tx pgx.Tx, agentID string) error {
	var attached bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM openfox.agent_mcp_servers WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1)
 OR EXISTS(SELECT 1 FROM openfox.agent_skill_loadout WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1)
 OR EXISTS(SELECT 1 FROM openfox.agent_env_vars WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1
 AND name LIKE 'HERMES\_%' ESCAPE '\' AND name NOT IN ('HERMES_API_KEY','HERMES_BASE_URL','HERMES_MODEL','HERMES_CONTEXT_LENGTH'))`, agentID).Scan(&attached)
	if err != nil {
		return err
	}
	if attached {
		return ErrHermesConfiguration
	}
	return nil
}
