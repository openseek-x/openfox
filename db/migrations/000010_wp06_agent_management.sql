-- WP-06 Agent management. The capacity-era agents row is extended in place so
-- existing device-bound rows remain valid while managed Agent definitions gain
-- space ownership and immutable configuration history.

SET ROLE openfox_migrator;

-- Device grants previously used (organization_id, device_id) as their public
-- identity. Add a stable grant id for Agent configuration references without
-- changing the existing device-auth and capacity foreign keys.
ALTER TABLE openfox.device_grants
    ADD COLUMN id uuid DEFAULT gen_random_uuid();

UPDATE openfox.device_grants
   SET id = gen_random_uuid()
 WHERE id IS NULL;

ALTER TABLE openfox.device_grants
    ALTER COLUMN id SET NOT NULL;

CREATE UNIQUE INDEX device_grants_organization_id_unique
    ON openfox.device_grants (organization_id, id);

ALTER TABLE openfox.agents
    ADD COLUMN space_id uuid,
    ADD COLUMN owner_user_id uuid,
    ADD COLUMN name text NOT NULL DEFAULT 'Legacy capacity agent',
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN kind text NOT NULL DEFAULT 'CODE',
    ADD COLUMN visibility text NOT NULL DEFAULT 'SHARED',
    ADD COLUMN archived_at timestamptz,
    ADD COLUMN current_config_version bigint,
    ADD COLUMN version bigint NOT NULL DEFAULT 1,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();

ALTER TABLE openfox.agents
    ADD CONSTRAINT agents_space_fk
        FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    ADD CONSTRAINT agents_owner_fk
        FOREIGN KEY (organization_id, owner_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    ADD CONSTRAINT agents_name_not_blank
        CHECK (length(btrim(name)) > 0),
    ADD CONSTRAINT agents_description_length
        CHECK (length(description) <= 10000),
    ADD CONSTRAINT agents_kind_valid
        CHECK (kind IN ('CODE', 'OFFICE')),
    ADD CONSTRAINT agents_visibility_valid
        CHECK (visibility IN ('PRIVATE', 'SHARED')),
    ADD CONSTRAINT agents_version_positive
        CHECK (version > 0),
    ADD CONSTRAINT agents_managed_identity_complete
        CHECK (
            (space_id IS NULL AND owner_user_id IS NULL AND current_config_version IS NULL)
            OR (space_id IS NOT NULL AND owner_user_id IS NOT NULL AND current_config_version IS NOT NULL)
        );

CREATE UNIQUE INDEX agents_space_name_unique
    ON openfox.agents (organization_id, space_id, lower(name))
    WHERE space_id IS NOT NULL AND archived_at IS NULL;
CREATE INDEX agents_space_listing_idx
    ON openfox.agents (organization_id, space_id, archived_at, created_at, id);
CREATE INDEX agents_owner_listing_idx
    ON openfox.agents (organization_id, owner_user_id, archived_at, created_at, id);

CREATE TABLE openfox.agent_config_versions (
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    config_version bigint NOT NULL,
    instructions text NOT NULL DEFAULT '',
    computer_id uuid NOT NULL,
    grant_id uuid NOT NULL,
    runtime text NOT NULL,
    model text NOT NULL,
    max_concurrency integer NOT NULL DEFAULT 1,
    queue_timeout_seconds bigint NOT NULL DEFAULT 3600,
    run_timeout_seconds bigint NOT NULL DEFAULT 3600,
    directory_mode text NOT NULL DEFAULT 'NONE',
    directory_roots jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, agent_id, config_version),
    FOREIGN KEY (organization_id, agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, grant_id)
        REFERENCES openfox.device_grants (organization_id, id),
    FOREIGN KEY (computer_id)
        REFERENCES openfox.physical_devices (id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT agent_config_versions_version_positive
        CHECK (config_version > 0),
    CONSTRAINT agent_config_versions_instructions_length
        CHECK (length(instructions) <= 100000),
    CONSTRAINT agent_config_versions_runtime_valid
        CHECK (length(btrim(runtime)) BETWEEN 1 AND 64),
    CONSTRAINT agent_config_versions_model_valid
        CHECK (length(btrim(model)) BETWEEN 1 AND 128),
    CONSTRAINT agent_config_versions_concurrency_valid
        CHECK (max_concurrency BETWEEN 1 AND 16),
    CONSTRAINT agent_config_versions_queue_timeout_valid
        CHECK (queue_timeout_seconds BETWEEN 1 AND 604800),
    CONSTRAINT agent_config_versions_run_timeout_valid
        CHECK (run_timeout_seconds BETWEEN 60 AND 28800),
    CONSTRAINT agent_config_versions_directory_mode_valid
        CHECK (directory_mode IN ('NONE', 'WORKSPACE', 'EXPLICIT')),
    CONSTRAINT agent_config_versions_directory_roots_array
        CHECK (jsonb_typeof(directory_roots) = 'array')
);

CREATE INDEX agent_config_versions_listing_idx
    ON openfox.agent_config_versions (organization_id, agent_id, config_version DESC);

-- Keep direct application-role writes subject to the same device/grant and
-- scope invariant as the Go service. This is deliberately a typed policy
-- check, not a generic key/value authorization table.
CREATE OR REPLACE FUNCTION openfox_private.validate_agent_config_version()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    target_space uuid;
    target_owner uuid;
    grant_device uuid;
    grant_scope text;
    grant_space uuid;
    grant_user uuid;
BEGIN
    SELECT a.space_id, a.owner_user_id
      INTO target_space, target_owner
      FROM openfox.agents AS a
     WHERE a.organization_id = NEW.organization_id
       AND a.id = NEW.agent_id
       AND a.space_id IS NOT NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'managed Agent is not available' USING ERRCODE = '23514';
    END IF;

    SELECT g.device_id, g.scope, g.scope_space_id, g.scope_user_id
      INTO grant_device, grant_scope, grant_space, grant_user
      FROM openfox.device_grants AS g
      JOIN openfox.physical_devices AS d ON d.id = g.device_id
     WHERE g.organization_id = NEW.organization_id
       AND g.id = NEW.grant_id
       AND g.status = 'ACTIVE'
       AND d.status = 'ACTIVE';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Agent device grant is not active' USING ERRCODE = '23514';
    END IF;

    IF grant_device <> NEW.computer_id THEN
        RAISE EXCEPTION 'Agent computer and grant do not match' USING ERRCODE = '23514';
    END IF;

    IF grant_scope = 'SPACE' AND grant_space <> target_space THEN
        RAISE EXCEPTION 'Agent device grant is outside the space' USING ERRCODE = '23514';
    ELSIF grant_scope = 'PERSONAL' AND grant_user <> target_owner THEN
        RAISE EXCEPTION 'Agent device grant is outside the owner scope' USING ERRCODE = '23514';
    ELSIF grant_scope NOT IN ('ORGANIZATION', 'SPACE', 'PERSONAL') THEN
        RAISE EXCEPTION 'Agent device grant scope is invalid' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_agent_config_version()
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_agent_config_version() FROM PUBLIC;

CREATE TRIGGER agent_config_version_valid_trigger
BEFORE INSERT ON openfox.agent_config_versions
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_agent_config_version();

CREATE OR REPLACE FUNCTION openfox_private.reject_agent_config_version_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    RAISE EXCEPTION 'Agent configuration versions are immutable' USING ERRCODE = '55000';
END
$$;

ALTER FUNCTION openfox_private.reject_agent_config_version_mutation()
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.reject_agent_config_version_mutation() FROM PUBLIC;

CREATE TRIGGER agent_config_version_immutable_update
BEFORE UPDATE ON openfox.agent_config_versions
FOR EACH ROW EXECUTE FUNCTION openfox_private.reject_agent_config_version_mutation();
CREATE TRIGGER agent_config_version_immutable_delete
BEFORE DELETE ON openfox.agent_config_versions
FOR EACH ROW EXECUTE FUNCTION openfox_private.reject_agent_config_version_mutation();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE (
    device_id, space_id, owner_user_id, name, description, kind, visibility,
    archived_at, current_config_version, version, updated_at
) ON openfox.agents TO openfox_app;
GRANT SELECT, INSERT ON openfox.agent_config_versions TO openfox_app;
GRANT SELECT (id, organization_id, device_id, status, scope, scope_user_id, scope_space_id)
    ON openfox.device_grants TO openfox_app;

ALTER TABLE openfox.agent_config_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_config_versions FORCE ROW LEVEL SECURITY;

CREATE POLICY agent_config_versions_tenant_isolation
    ON openfox.agent_config_versions
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
