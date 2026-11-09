package dispatch

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/mcpconnect"
	"openfox/internal/secrets"
)

var ErrMCPUnsupported = errors.New("dispatch: update the device to run the configured MCP servers")

// This runs in the same transaction as authorizeRunRead. Fetch a local binding
// only when every input is a real user message by the physical computer owner.
// A system trigger (including an input with no message) can never opt into it.
func loadRunMCP(ctx context.Context, tx pgx.Tx, run dispatchRun, result *RunContext, box *secrets.Box, supported bool) error {
	rows, e := tx.Query(ctx, `WITH device AS (
 SELECT d.id AS physical_id,d.owner_user_id AS physical_owner,d.capabilities,
   (r.actor_id=d.owner_user_id AND EXISTS (SELECT 1 FROM openfox.conversation_inputs i WHERE i.organization_id=r.organization_id AND i.run_id=r.id)
    AND NOT EXISTS (SELECT 1 FROM openfox.conversation_inputs i LEFT JOIN openfox.messages m ON m.organization_id=i.organization_id AND m.conversation_id=i.conversation_id AND m.id=i.message_id
      WHERE i.organization_id=r.organization_id AND i.run_id=r.id AND (m.authenticated_user_input IS NOT TRUE OR m.author_kind IS DISTINCT FROM 'USER' OR m.author_user_id IS DISTINCT FROM r.actor_id))) owner_inputs
 FROM openfox.runs r JOIN openfox.physical_devices d ON d.id=r.computer_id AND d.status='ACTIVE'
 JOIN openfox.agents a ON a.organization_id=r.organization_id AND a.id=r.agent_id AND a.archived_at IS NULL
 JOIN openfox.agent_config_versions c ON c.organization_id=a.organization_id AND c.agent_id=a.id AND c.config_version=a.current_config_version AND c.computer_id=r.computer_id AND c.grant_id=r.grant_id
 WHERE r.organization_id=$1 AND r.id=$2)
 SELECT `+mcpconnect.Columns+`,COALESCE(d.capabilities->'agent_mcp'='true'::jsonb,false)
 FROM openfox.agent_mcp_servers m CROSS JOIN device d
 WHERE m.organization_id=$1 AND m.agent_id=$3 AND (m.scope='PUBLIC' OR (m.device_id=d.physical_id AND m.owner_user_id=d.physical_owner AND d.owner_inputs AND lower(replace(m.runtime,'-','_'))=$4))
 AND m.status IN ('connected','device') AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp())
 ORDER BY lower(m.name),m.id LIMIT 64 FOR SHARE OF m`, run.OrganizationID, run.ID, run.AgentID, strings.ReplaceAll(strings.ToLower(result.Runtime), "-", "_"))
	if e != nil {
		return e
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var caps bool
		v, e := mcpconnect.Scan(rows, &caps)
		if e != nil {
			return e
		}
		if !supported || !caps {
			return ErrMCPUnsupported
		}
		if v.Status != "connected" && v.Status != "device" {
			return ErrAuthorizationRevoked
		}
		if v.Config.Scope == mcpconnect.DeviceOwner && (v.DeviceID != run.ComputerID || v.OwnerUserID != run.ActorID || !strings.EqualFold(v.Runtime, result.Runtime)) {
			return ErrAuthorizationRevoked
		}
		c, e := v.Open(box, run.OrganizationID, run.AgentID)
		if e != nil {
			return ErrSecretsUnavailable
		}
		key := strings.ToLower(c.Name)
		if seen[key] {
			return ErrInvalidRequest
		}
		seen[key] = true
		item := mcpconnect.RunServer{CatalogID: c.CatalogID, Name: c.Name, Transport: strings.ToUpper(c.Transport), Scope: c.Scope, URL: c.URL, Headers: c.RequestHeaders(), Command: c.Command, Args: c.Args, Env: c.Env, ServerID: v.ID}
		if c.Scope == mcpconnect.DeviceOwner {
			result.MCPActorUserID = run.ActorID
			result.MCPDeviceOwnerID = v.OwnerUserID
			item.OwnerUserID = v.OwnerUserID
			item.DeviceID = v.DeviceID
		}
		result.MCPServers = append(result.MCPServers, item)
	}
	return rows.Err()
}
