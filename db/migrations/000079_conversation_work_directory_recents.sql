-- Only logical directory names; a recent entry never grants access to that path.
SET ROLE openfox_migrator;
CREATE TABLE openfox.conversation_work_directory_recents (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    device_id uuid NOT NULL REFERENCES openfox.physical_devices(id) ON DELETE CASCADE,
    directories text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (organization_id, user_id, agent_id, device_id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members(organization_id,user_id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, agent_id) REFERENCES openfox.agents(organization_id,id) ON DELETE CASCADE,
    CHECK (cardinality(directories) <= 10)
);
ALTER TABLE openfox.conversation_work_directory_recents ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversation_work_directory_recents FORCE ROW LEVEL SECURITY;
CREATE POLICY own_work_directory_recents ON openfox.conversation_work_directory_recents
    USING (organization_id=openfox_private.current_organization_id() AND user_id=openfox_private.current_actor_id());
GRANT SELECT, INSERT, UPDATE ON openfox.conversation_work_directory_recents TO openfox_app;
RESET ROLE;
