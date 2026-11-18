-- Optional controlled samples for hifox-batch2.spec.ts, disposable databases only.
-- Detector execution and signed heartbeat persistence are verified by Go tests;
-- these deliberately mixed statuses exercise the real API/browser read chain.
\set ON_ERROR_STOP on
UPDATE openfox.physical_devices
SET capabilities = capabilities || '{"development_tools":[
  {"name":"node","version":"v22.23.1","path":"/工具 bin/node","status":"AVAILABLE","checked_at":"2026-11-13T01:02:03Z"},
  {"name":"npm","version":"","path":"","status":"MISSING","checked_at":"2026-11-13T01:02:03Z"},
  {"name":"python","version":"","path":"/bin/python3","status":"TIMEOUT","checked_at":"2026-11-13T01:02:05Z"},
  {"name":"uv","version":"","path":"/bin/uv","status":"ERROR","checked_at":"2026-11-13T01:02:05Z"}
]}'::jsonb
WHERE id = '00000000-0000-0000-0000-000000000011';
