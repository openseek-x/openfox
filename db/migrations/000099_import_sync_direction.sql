-- Existing project links continue syncing in both directions.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_sync_links
  ADD COLUMN direction text NOT NULL DEFAULT 'BIDIRECTIONAL'
  CHECK (direction IN ('INBOUND', 'BIDIRECTIONAL'));
RESET ROLE;
