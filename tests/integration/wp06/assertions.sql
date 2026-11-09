\set ON_ERROR_STOP on

-- Agent schema and tenant boundary preflight. The Go integration test covers
-- the write-side version/CAS and visibility cases.
SELECT 1 / ((count(*) = 1)::int) AS grant_id_constraint_check
  FROM information_schema.columns
 WHERE table_schema = 'openfox' AND table_name = 'device_grants' AND column_name = 'id';

SELECT 1 / ((count(*) = 1)::int) AS config_version_table_check
  FROM information_schema.tables
 WHERE table_schema = 'openfox' AND table_name = 'agent_config_versions';

BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-000000000002', 1, 'user_request'
);
SET LOCAL ROLE openfox_app;
SELECT 1 / ((count(*) = 0)::int) AS cross_tenant_config_read_check
  FROM openfox.agent_config_versions
 WHERE organization_id = '00000000-0000-0000-0000-0000000000a1';
COMMIT;
