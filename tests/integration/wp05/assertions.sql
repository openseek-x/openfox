\set ON_ERROR_STOP on

-- The identity and device fixture contains two organizations. This
-- script exercises the new one-time registration and scoped grant boundary.
BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001', 1, 'user_request'
);
SET LOCAL ROLE openfox_app;

INSERT INTO openfox.device_registration_tokens (
    organization_id, issued_by_user_id, token_hash, name, install_id,
    max_slots, expires_at
) VALUES (
    '00000000-0000-0000-0000-0000000000a1',
    '00000000-0000-0000-0000-000000000001',
    decode(repeat('11', 32), 'hex'), 'WP05 device', 'install-wp05', 2,
    clock_timestamp() + interval '5 minutes'
)
RETURNING id::text AS registration_id
\gset

SELECT device_id::text
  FROM openfox_private.register_device_with_token(
      decode(repeat('11', 32), 'hex'), 'primary', decode(repeat('22', 32), 'hex')
  )
\gset registered_

SELECT 1 / ((count(*) = 1)::int) AS registration_row_check
  FROM openfox.physical_devices
 WHERE id = :'registered_device_id'::uuid;
SELECT 1 / (((SELECT owner_user_id::text FROM openfox.physical_devices WHERE id = :'registered_device_id'::uuid)
                 = '00000000-0000-0000-0000-000000000001')::int) AS registration_owner_check;
SELECT 1 / (((SELECT name FROM openfox.physical_devices WHERE id = :'registered_device_id'::uuid)
                 = 'WP05 device')::int) AS registration_name_check;

UPDATE openfox.physical_devices
   SET last_seen_at = clock_timestamp(),
       protocol_major = 1,
       protocol_minor = 0,
       daemon_version = 'test-daemon',
       operating_system = 'darwin',
       architecture = 'arm64',
       capabilities = '{"runtime":"unknown"}'::jsonb,
       center_epoch = 1,
       version = version + 1,
       updated_at = clock_timestamp()
 WHERE id = :'registered_device_id'::uuid
   AND status = 'ACTIVE'
   AND generation = 1;

UPDATE openfox.device_grants
   SET scope = 'SPACE',
       scope_space_id = '00000000-0000-0000-0000-0000000000aa',
       permission_version = permission_version + 1
 WHERE organization_id = '00000000-0000-0000-0000-0000000000a1'
   AND device_id = :'registered_device_id'::uuid
   AND status = 'ACTIVE';

SELECT 1 / ((EXISTS (
           SELECT 1 FROM openfox.device_grants
            WHERE organization_id = '00000000-0000-0000-0000-0000000000a1'
              AND device_id = :'registered_device_id'::uuid
              AND scope = 'SPACE'
              AND scope_space_id = '00000000-0000-0000-0000-0000000000aa'
       ) AND EXISTS (
           SELECT 1 FROM openfox.audit_events
            WHERE resource_kind = 'device'
              AND resource_id = :'registered_device_id'
       ))::int) AS scope_and_audit_check;
COMMIT;

-- A replayed registration token fails closed and does not create a second row.
BEGIN;
SET LOCAL ROLE openfox_app;
DO $$
BEGIN
    BEGIN
        PERFORM * FROM openfox_private.register_device_with_token(
            decode(repeat('11', 32), 'hex'), 'replay', decode(repeat('33', 32), 'hex')
        );
        RAISE EXCEPTION 'registration token replay was accepted';
    EXCEPTION WHEN insufficient_privilege THEN
        NULL;
    END;
END
$$;
COMMIT;

-- Tenant RLS hides the new grant from organization B, even when the device ID
-- is known. The control-plane health row is not exposed as tenant content.
BEGIN;
SET LOCAL ROLE openfox_context;
SELECT openfox_private.set_tenant_context(
    '00000000-0000-0000-0000-0000000000b1',
    '00000000-0000-0000-0000-000000000002', 1, 'user_request'
);
SET LOCAL ROLE openfox_app;
SELECT 1 / ((count(*) = 0)::int) AS cross_tenant_read_check
  FROM openfox.device_grants
 WHERE device_id = :'registered_device_id'::uuid;
UPDATE openfox.device_grants
   SET permission_version = permission_version + 1
 WHERE device_id = :'registered_device_id'::uuid;
SELECT 1 / ((count(*) = 0)::int) AS cross_tenant_write_check
  FROM openfox.device_grants
 WHERE device_id = :'registered_device_id'::uuid;
COMMIT;
