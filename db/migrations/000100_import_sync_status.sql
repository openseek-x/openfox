-- Additive reconciliation state. Null item baselines mean observe once before writing.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_sync_items
 ADD COLUMN source_status text,
 ADD COLUMN source_status_id text,
 ADD COLUMN local_status_id text;
ALTER TABLE openfox.import_sync_links
 ADD COLUMN status_mappings jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(status_mappings)='object'),
 ADD COLUMN last_checked timestamptz;
RESET ROLE;
