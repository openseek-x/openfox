package agents

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/runtimepolicy"
)

var ErrCodeBuddyConfiguration = collaboration.NewRuleError(http.StatusUnprocessableEntity, "RUNTIME_CONFIGURATION_UNSUPPORTED", "CodeBuddy supports only read-only runs with an explicit model endpoint; MCP, skills, custom arguments and native extensions are not supported")

// The lock-only Agent projection intentionally has no Config. Read the immutable
// version selected by that locked row, rather than relying on an unloaded pointer.
func requireCodeBuddyExtensionsAllowed(ctx context.Context, tx pgx.Tx, agentID string) error {
	var provider string
	err := tx.QueryRow(ctx, `SELECT c.runtime FROM openfox.agents a JOIN openfox.agent_config_versions c
 ON c.organization_id=a.organization_id AND c.agent_id=a.id AND c.config_version=a.current_config_version
 WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1`, agentID).Scan(&provider)
	if err != nil {
		return err
	}
	if provider == runtimepolicy.CodeBuddy {
		return ErrCodeBuddyConfiguration
	}
	if provider == runtimepolicy.Hermes {
		return ErrHermesConfiguration
	}
	if provider == runtimepolicy.Gemini {
		return ErrGeminiConfiguration
	}
	return nil
}

// Called under the existing Agent write lock before switching runtime. MCP and
// skill mutations use that same lock, so neither can race a successful switch.
func validateCodeBuddyAttachments(ctx context.Context, tx pgx.Tx, agentID string) error {
	var attached bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM openfox.agent_mcp_servers WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1)
 OR EXISTS(SELECT 1 FROM openfox.agent_skill_loadout WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1)
 OR EXISTS(SELECT 1 FROM openfox.agent_env_vars WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1
   AND name LIKE 'CODEBUDDY\_%' ESCAPE '\' AND name NOT IN ('CODEBUDDY_API_KEY','CODEBUDDY_BASE_URL','CODEBUDDY_MODEL'))`, agentID).Scan(&attached)
	if err != nil {
		return err
	}
	if attached {
		return ErrCodeBuddyConfiguration
	}
	return nil
}

// Called while the Agent lock is held; an unrelated runtime's environment keys
// do not change the existing provider's contract.
func requireRuntimeEnvironmentAllowed(ctx context.Context, tx pgx.Tx, agentID, name string) error {
	var provider string
	err := tx.QueryRow(ctx, `SELECT c.runtime FROM openfox.agents a JOIN openfox.agent_config_versions c ON c.organization_id=a.organization_id AND c.agent_id=a.id AND c.config_version=a.current_config_version WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1`, agentID).Scan(&provider)
	if err != nil {
		return err
	}
	if provider == runtimepolicy.CodeBuddy && (!runtimepolicy.CodeBuddyEnvironmentAllowed(name) || runtimepolicy.ReservedEnvironment(name)) {
		return ErrCodeBuddyConfiguration
	}
	if provider == runtimepolicy.Hermes && (!runtimepolicy.HermesEnvironmentAllowed(name) || runtimepolicy.ReservedEnvironment(name)) {
		return ErrHermesConfiguration
	}
	if provider == runtimepolicy.Gemini && (!runtimepolicy.GeminiEnvironmentAllowed(name) || runtimepolicy.ReservedEnvironment(name)) {
		return ErrGeminiConfiguration
	}
	return nil
}
