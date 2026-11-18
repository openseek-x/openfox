-- Agent-owned, pinned market packages. Historical space skills and bindings are untouched.
SET ROLE openfox_migrator;
CREATE TABLE openfox.agent_market_skill_installs (
    organization_id uuid NOT NULL,
    id uuid NOT NULL,
    agent_id uuid NOT NULL,
    space_id uuid NOT NULL,
    market_skill_id text NOT NULL CHECK (market_skill_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$'),
    market_version text NOT NULL CHECK (char_length(market_version) BETWEEN 1 AND 64),
    name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    description text NOT NULL CHECK (char_length(description) <= 1024),
    source_label text NOT NULL CHECK (char_length(source_label) <= 200),
    files jsonb NOT NULL CHECK (jsonb_typeof(files) = 'array' AND jsonb_array_length(files) BETWEEN 1 AND 100),
    total_bytes bigint NOT NULL CHECK (total_bytes BETWEEN 0 AND 5242880),
    position integer NOT NULL CHECK (position >= 0),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,agent_id,market_skill_id),
    UNIQUE (organization_id,agent_id,name),
    FOREIGN KEY (organization_id,agent_id,space_id) REFERENCES openfox.agents (organization_id,id,space_id) ON UPDATE CASCADE ON DELETE CASCADE,
    FOREIGN KEY (organization_id,created_by) REFERENCES openfox.organization_members (organization_id,user_id)
);
ALTER TABLE openfox.agent_market_skill_installs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_market_skill_installs FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_market_skills_tenant ON openfox.agent_market_skill_installs
    USING (organization_id=openfox_private.current_organization_id()) WITH CHECK (organization_id=openfox_private.current_organization_id());

-- Public skill IDs may refer to an existing space binding or an Agent's own install.
-- This view never exposes file keys and executes with the caller's tenant policies.
CREATE VIEW openfox.agent_skill_loadout WITH (security_invoker=true) AS
    SELECT a.organization_id, a.agent_id, a.space_id, a.position, a.created_at AS attached_at,
           s.id,s.name,s.description,s.source_kind,s.source_label,s.source_url,s.file_count,s.total_bytes,
           s.created_by,s.version,s.created_at,s.updated_at,s.market_skill_id,s.market_version,false AS agent_owned
      FROM openfox.agent_skills a JOIN openfox.skills s ON s.organization_id=a.organization_id AND s.id=a.skill_id
    UNION ALL
    SELECT i.organization_id,i.agent_id,i.space_id,i.position,i.created_at,
           i.id,i.name,i.description,'market',i.source_label,NULL,jsonb_array_length(i.files),i.total_bytes,
           i.created_by,1::bigint,i.created_at,i.created_at,i.market_skill_id,i.market_version,true
      FROM openfox.agent_market_skill_installs i;

CREATE TRIGGER agent_skill_binding_update AFTER UPDATE ON openfox.agent_skills
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_new();
CREATE TRIGGER agent_market_install_insert AFTER INSERT ON openfox.agent_market_skill_installs
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_new();
CREATE TRIGGER agent_market_install_update AFTER UPDATE ON openfox.agent_market_skill_installs
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_new();
CREATE TRIGGER agent_market_install_delete AFTER DELETE ON openfox.agent_market_skill_installs
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_old();
GRANT SELECT, INSERT, DELETE ON openfox.agent_market_skill_installs TO openfox_app;
GRANT UPDATE(position) ON openfox.agent_market_skill_installs TO openfox_app;
GRANT SELECT ON openfox.agent_skill_loadout TO openfox_app;
RESET ROLE;
