\set ON_ERROR_STOP on

DO $$
DECLARE
    bypasses boolean;
    app_owner boolean;
    context_can_create_temp boolean;
    app_can_update_agent boolean;
BEGIN
    SELECT rolsuper OR rolbypassrls INTO bypasses
      FROM pg_roles WHERE rolname = 'openfox_app';
    SELECT (pg_get_userbyid(c.relowner) = 'openfox_app') INTO app_owner
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'openfox' AND c.relname = 'organizations';
    SELECT has_database_privilege('openfox_context', current_database(), 'TEMPORARY')
      INTO context_can_create_temp;
    SELECT has_table_privilege('openfox_app', 'openfox.agents', 'UPDATE')
      INTO app_can_update_agent;
    IF bypasses OR app_owner OR context_can_create_temp OR app_can_update_agent THEN
        RAISE EXCEPTION 'runtime roles have excessive privilege';
    END IF;
END
$$;

BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    visible_rows bigint;
BEGIN
    SELECT count(*) INTO visible_rows FROM openfox.organizations;
    IF visible_rows <> 0 THEN
        RAISE EXCEPTION 'missing context exposed % organization rows', visible_rows;
    END IF;
END
$$;
DO $$
BEGIN
    BEGIN
        PERFORM openfox_private.set_tenant_context(
            '00000000-0000-0000-0000-0000000000a1',
            '00000000-0000-0000-0000-000000000001', 1, 'user_request'
        );
        RAISE EXCEPTION 'openfox_app executed the trusted context setter';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
END
$$;
DO $$
BEGIN
    BEGIN
        PERFORM openfox_private.reserve_capacity(
            '00000000-0000-0000-0000-0000000000a1',
            '00000000-0000-0000-0000-0000000000c2',
            '00000000-0000-0000-0000-0000000000c3', 1,
            '00000000-0000-0000-0000-000000000022',
            '00000000-0000-0000-0000-0000000000a4',
            '00000000-0000-0000-0000-0000000000c4',
            clock_timestamp() + interval '1 minute'
        );
        RAISE EXCEPTION 'openfox_app executed the narrow capacity function';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'user_request'
);
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    organization_rows bigint;
    member_rows bigint;
    task_rows bigint;
BEGIN
    SELECT count(*) INTO organization_rows FROM openfox.organizations;
    SELECT count(*) INTO member_rows FROM openfox.organization_members;
    SELECT count(*) INTO task_rows FROM openfox.tasks;
    IF organization_rows <> 1 OR member_rows <> 1 OR task_rows <> 0 THEN
        RAISE EXCEPTION 'tenant A visibility mismatch: org %, members %, tasks %', organization_rows, member_rows, task_rows;
    END IF;
END
$$;
DO $$
DECLARE
    affected bigint;
BEGIN
    UPDATE openfox.spaces
       SET name = 'should not update tenant B'
     WHERE organization_id = '00000000-0000-0000-0000-0000000000b1';
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 0 THEN
        RAISE EXCEPTION 'cross-organization update affected % rows', affected;
    END IF;
END
$$;
DO $$
BEGIN
    BEGIN
        INSERT INTO openfox.space_members (organization_id, space_id, user_id)
        VALUES (
            '00000000-0000-0000-0000-0000000000a1',
            '00000000-0000-0000-0000-0000000000bb',
            '00000000-0000-0000-0000-000000000001'
        );
        RAISE EXCEPTION 'cross-organization composite association was accepted';
    EXCEPTION WHEN foreign_key_violation OR check_violation OR insufficient_privilege THEN
        NULL;
    END;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
BEGIN
    BEGIN
        INSERT INTO openfox.organizations (id, slug, name)
        VALUES ('00000000-0000-0000-0000-0000000000c1', 'org-c', 'Should fail');
        RAISE EXCEPTION 'missing context write was accepted';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'organization_worker'
);
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    visible_rows bigint;
BEGIN
    SELECT count(*) INTO visible_rows FROM openfox.organizations;
    IF visible_rows <> 1 THEN
        RAISE EXCEPTION 'context setup did not expose tenant A';
    END IF;
END
$$;
COMMIT;

BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
DECLARE
    visible_rows bigint;
BEGIN
    SELECT count(*) INTO visible_rows FROM openfox.organizations;
    IF visible_rows <> 0 THEN
        RAISE EXCEPTION 'context leaked across transaction boundary';
    END IF;
END
$$;
COMMIT;

-- Releasing twice is idempotent; the live offer and claim checks are exercised
-- by the independent race scripts invoked from run.sh.
BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'user_request'
);
SELECT openfox_private.reserve_capacity(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-0000000000d1',
    '00000000-0000-0000-0000-0000000000e1', 1,
    '00000000-0000-0000-0000-000000000022',
    '00000000-0000-0000-0000-0000000000a4',
    '00000000-0000-0000-0000-0000000000f1',
    clock_timestamp() + interval '1 minute'
);
SELECT openfox_private.release_reservation(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-0000000000d1'
);
SELECT openfox_private.release_reservation(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-0000000000d1'
) AS duplicate_release_must_be_false;
COMMIT;

DO $$
DECLARE
    organization_slots integer;
    device_slots integer;
    agent_slots integer;
BEGIN
    SELECT reserved_slots INTO organization_slots
      FROM openfox.organization_capacity
     WHERE organization_id = '00000000-0000-0000-0000-0000000000a1';
    SELECT reserved_slots INTO device_slots
      FROM openfox.physical_devices
     WHERE id = '00000000-0000-0000-0000-000000000022';
    SELECT reserved_slots INTO agent_slots
      FROM openfox.agents
     WHERE id = '00000000-0000-0000-0000-0000000000a4';
    IF organization_slots <> 0 OR device_slots <> 0 OR agent_slots <> 0 THEN
        RAISE EXCEPTION 'duplicate release changed capacity: org %, device %, agent %', organization_slots, device_slots, agent_slots;
    END IF;
END
$$;
