-- Disposable browser fixture only: supply two known source timestamps after 121.
-- This is not a backfill or a migration; the unknown row deliberately stays NULL.
\set ON_ERROR_STOP on
BEGIN;
SELECT openfox_private.set_tenant_context(
 '00000000-0000-0000-0000-0000000000a1',
 '00000000-0000-0000-0000-000000000001', 1, 'user_request');
UPDATE openfox.tasks SET completed_at=clock_timestamp()-interval '8 days'
 WHERE organization_id='00000000-0000-0000-0000-0000000000a1' AND id='00000000-0000-0000-0000-000000006032';
UPDATE openfox.tasks SET completed_at=clock_timestamp()-interval '31 days'
 WHERE organization_id='00000000-0000-0000-0000-0000000000a1' AND id='00000000-0000-0000-0000-000000006033';
COMMIT;
