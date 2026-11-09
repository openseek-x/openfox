-- Older centers expose generic device capabilities to all members. Never persist
-- a personal runtime inventory there, including writes from overlapping old centers.
SET ROLE openfox_migrator;
CREATE TABLE openfox.device_mcp_inventory (
    organization_id uuid NOT NULL,
    device_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    runtimes jsonb NOT NULL CHECK (jsonb_typeof(runtimes)='object'),
    checked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,device_id),
    FOREIGN KEY (organization_id,device_id) REFERENCES openfox.device_grants(organization_id,device_id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id) ON DELETE CASCADE
);
ALTER TABLE openfox.device_mcp_inventory ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.device_mcp_inventory FORCE ROW LEVEL SECURITY;
CREATE POLICY read_own_device_inventory ON openfox.device_mcp_inventory FOR SELECT
    USING (organization_id=openfox_private.current_organization_id() AND openfox_private.current_actor_id() IN (device_id,owner_user_id));
CREATE POLICY report_device_inventory ON openfox.device_mcp_inventory FOR INSERT
    WITH CHECK (organization_id=openfox_private.current_organization_id() AND device_id=openfox_private.current_actor_id());
CREATE POLICY update_device_inventory ON openfox.device_mcp_inventory FOR UPDATE
    USING (organization_id=openfox_private.current_organization_id() AND device_id=openfox_private.current_actor_id())
    WITH CHECK (organization_id=openfox_private.current_organization_id() AND device_id=openfox_private.current_actor_id());
GRANT SELECT, INSERT, UPDATE ON openfox.device_mcp_inventory TO openfox_app;
CREATE FUNCTION openfox_private.strip_device_mcp_inventory() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.capabilities := NEW.capabilities - 'runtime_mcp_inventory';
    RETURN NEW;
END
$$;
CREATE TRIGGER strip_device_mcp_inventory BEFORE INSERT OR UPDATE OF capabilities ON openfox.physical_devices
    FOR EACH ROW WHEN (NEW.capabilities ? 'runtime_mcp_inventory')
    EXECUTE FUNCTION openfox_private.strip_device_mcp_inventory();
UPDATE openfox.physical_devices SET capabilities=capabilities-'runtime_mcp_inventory' WHERE capabilities ? 'runtime_mcp_inventory';
RESET ROLE;
