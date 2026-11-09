\set ON_ERROR_STOP on
BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'organization_worker'
);
SELECT openfox_private.reserve_capacity(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-0000000000d2',
    '00000000-0000-0000-0000-0000000000e2', 1,
    '00000000-0000-0000-0000-000000000011',
    '00000000-0000-0000-0000-0000000000a2',
    '00000000-0000-0000-0000-0000000000f2',
    clock_timestamp() + interval '1 minute'
);
COMMIT;
BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'organization_worker'
);
SELECT openfox_private.release_reservation(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-0000000000d2'
);
COMMIT;
