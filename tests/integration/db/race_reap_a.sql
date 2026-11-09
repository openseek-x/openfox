\set ON_ERROR_STOP on
BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'organization_worker'
);
SELECT openfox_private.reap_offer(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-0000000000f5'
);
COMMIT;
