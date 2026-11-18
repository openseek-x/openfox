-- Agent-bound MCP settings. Public metadata is redacted; runtime credentials are sealed with row-bound AAD.
SET ROLE openfox_migrator;
CREATE TABLE openfox.agent_mcp_servers (
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    id uuid NOT NULL,
    instance_id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    scope text NOT NULL CHECK (scope IN ('PUBLIC','DEVICE_OWNER')),
    config jsonb NOT NULL,
    manifest jsonb NOT NULL DEFAULT '{"tools":[],"resources":[]}',
    status text NOT NULL CHECK (status IN ('connected','authorizing','device')),
    secret_ciphertext bytea,
    secret_nonce bytea,
    secret_key_id text,
    expires_at timestamptz,
    device_id uuid REFERENCES openfox.physical_devices(id) ON DELETE CASCADE,
    owner_user_id uuid,
    runtime text,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (organization_id,agent_id,id),
    FOREIGN KEY (organization_id,agent_id) REFERENCES openfox.agents(organization_id,id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id) ON DELETE CASCADE,
    CHECK ((scope='PUBLIC' AND device_id IS NULL AND owner_user_id IS NULL AND runtime IS NULL)
           OR (scope='DEVICE_OWNER' AND device_id IS NOT NULL AND owner_user_id IS NOT NULL AND runtime IS NOT NULL)),
    CHECK ((secret_ciphertext IS NULL AND secret_nonce IS NULL AND secret_key_id IS NULL)
           OR (secret_ciphertext IS NOT NULL AND octet_length(secret_nonce)=12 AND secret_key_id IS NOT NULL))
);
CREATE UNIQUE INDEX public_agent_mcp_name ON openfox.agent_mcp_servers(organization_id,agent_id,lower(name)) WHERE scope='PUBLIC';
CREATE UNIQUE INDEX owner_agent_mcp_name ON openfox.agent_mcp_servers(organization_id,agent_id,device_id,owner_user_id,runtime,lower(name)) WHERE scope='DEVICE_OWNER';
ALTER TABLE openfox.agent_mcp_servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_mcp_servers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_agent_mcp ON openfox.agent_mcp_servers USING (organization_id=openfox_private.current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.agent_mcp_servers TO openfox_app;
RESET ROLE;
