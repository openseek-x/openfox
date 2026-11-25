SET ROLE openfox_migrator;

ALTER TABLE openfox.device_grants ADD COLUMN allow_workflows boolean NOT NULL DEFAULT false;
GRANT SELECT (allow_workflows) ON openfox.device_grants TO openfox_app;

-- Rejoining a removed organization requires a new consent.
CREATE FUNCTION openfox_private.reset_revoked_workflow_permission() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    NEW.allow_workflows := false;
    RETURN NEW;
END
$$;
REVOKE ALL ON FUNCTION openfox_private.reset_revoked_workflow_permission() FROM PUBLIC;
CREATE TRIGGER device_grants_reset_workflow_permission BEFORE UPDATE OF status ON openfox.device_grants
    FOR EACH ROW WHEN (NEW.status <> 'ACTIVE' AND NEW.allow_workflows)
    EXECUTE FUNCTION openfox_private.reset_revoked_workflow_permission();

-- Serialize all settings changes on the same owner-checked settings version.
CREATE FUNCTION openfox_private.update_device_settings(
    p_device uuid, p_version bigint, p_name text, p_max_slots integer, p_allow_workflows boolean
) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private AS $$
DECLARE
    new_version bigint;
BEGIN
    new_version := openfox_private.update_device_settings(p_device, p_version, p_name, p_max_slots);
    IF new_version IS NOT NULL AND p_allow_workflows IS NOT NULL THEN
        UPDATE openfox.device_grants SET allow_workflows = p_allow_workflows
         WHERE organization_id = openfox_private.current_organization_id()
           AND device_id = p_device AND status = 'ACTIVE';
        IF NOT FOUND THEN
            RAISE EXCEPTION 'active device grant required' USING ERRCODE = '42501';
        END IF;
    END IF;
    RETURN new_version;
END
$$;
REVOKE ALL ON FUNCTION openfox_private.update_device_settings(uuid,bigint,text,integer,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.update_device_settings(uuid,bigint,text,integer,boolean) TO openfox_app;

-- The origin stays with the conversation even if its automation definition is deleted.
ALTER TABLE openfox.conversations DROP CONSTRAINT conversations_origin_valid;
ALTER TABLE openfox.conversations ADD CONSTRAINT conversations_origin_valid
    CHECK (origin IN ('USER','TASK_ASSIGNMENT','IM','AUTOMATION'));
RESET ROLE;

-- Apply this cross-tenant provenance update as the migration runner, outside forced tenant RLS.
UPDATE openfox.conversations c SET origin = 'AUTOMATION'
  FROM openfox.automation_runs a
 WHERE c.organization_id = a.organization_id AND c.id = a.conversation_id AND c.origin = 'USER';
