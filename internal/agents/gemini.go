package agents

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/runtimepolicy"
)

var ErrGeminiConfiguration = collaboration.NewRuleError(http.StatusUnprocessableEntity, "RUNTIME_CONFIGURATION_UNSUPPORTED", "Gemini supports only read-only snapshots with an explicit Gemini API key and model; MCP, skills, custom arguments, proxies and native extensions are not supported")

// Runtime changes and environment/MCP/skill writes share the existing Agent lock.
// Read names only; secrets are neither decrypted nor needed for this check.
func validateGeminiAttachments(ctx context.Context, tx pgx.Tx, agentID string) error {
	var attached bool
	var names []string
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM openfox.agent_mcp_servers WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1)
 OR EXISTS(SELECT 1 FROM openfox.agent_skill_loadout WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1),
 ARRAY(SELECT name FROM openfox.agent_env_vars WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1)`, agentID).Scan(&attached, &names)
	if err != nil {
		return err
	}
	if attached {
		return ErrGeminiConfiguration
	}
	for _, name := range names {
		if !runtimepolicy.GeminiEnvironmentAllowed(name) || runtimepolicy.ReservedEnvironment(name) {
			return ErrGeminiConfiguration
		}
	}
	return nil
}
