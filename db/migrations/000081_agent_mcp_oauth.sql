-- One ten-minute, actor-scoped PKCE attempt per Agent connection.
SET ROLE openfox_migrator;
CREATE TABLE openfox.agent_mcp_oauth_attempts (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    server_id uuid NOT NULL,
    state_hash bytea NOT NULL CHECK (octet_length(state_hash) = 32),
    expires_at timestamptz NOT NULL,
    pending bytea,
    status text NOT NULL CHECK (status IN ('pending','exchanging','failed','connected')),
    PRIMARY KEY (organization_id, user_id, agent_id, server_id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members(organization_id,user_id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id, server_id) REFERENCES openfox.agent_mcp_servers(organization_id,agent_id,id) ON DELETE CASCADE
);
ALTER TABLE openfox.agent_mcp_oauth_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_mcp_oauth_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY own_mcp_oauth ON openfox.agent_mcp_oauth_attempts
    USING (organization_id=openfox_private.current_organization_id() AND user_id=openfox_private.current_actor_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.agent_mcp_oauth_attempts TO openfox_app;
RESET ROLE;
