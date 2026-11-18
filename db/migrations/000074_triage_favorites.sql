-- Personal triage destinations. No existing rows are changed or backfilled.
SET ROLE openfox_migrator;
CREATE TABLE openfox.triage_favorites (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    space_id uuid NOT NULL,
    PRIMARY KEY (organization_id, user_id, space_id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id) ON DELETE CASCADE
);
ALTER TABLE openfox.triage_favorites ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.triage_favorites FORCE ROW LEVEL SECURITY;
CREATE POLICY triage_favorites_owner ON openfox.triage_favorites TO openfox_app
    USING (organization_id = openfox_private.current_organization_id() AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id() AND user_id = openfox_private.current_actor_id());
RESET ROLE;
GRANT SELECT, INSERT, DELETE ON openfox.triage_favorites TO openfox_app;
