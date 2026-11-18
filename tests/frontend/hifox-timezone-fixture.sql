-- R12 browser-only controlled device reports; disposable fixture database only.
\set ON_ERROR_STOP on
UPDATE openfox.physical_devices
SET capabilities = capabilities || '{"timezone":"Asia/Shanghai"}'::jsonb
WHERE id = '00000000-0000-0000-0000-000000000011';
UPDATE openfox.physical_devices
SET capabilities = capabilities || '{"timezone":"America/New_York"}'::jsonb
WHERE id = '00000000-0000-0000-0000-000000000022';
UPDATE openfox.physical_devices
SET capabilities = capabilities - 'timezone'
WHERE id = '00000000-0000-0000-0000-000000000033';
