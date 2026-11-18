-- P01: one verified TAPD identity/nick per OpenFox organization.
-- No credentials, historical tasks, or external-account links are backfilled.
-- Existing duplicates abort this migration rather than being deleted.
-- Rollback after stopping new bindings and verified import/sync jobs:
-- DROP INDEX openfox.integration_connections_tapd_nick_unique;
-- DROP INDEX openfox.integration_connections_tapd_user_unique;
SET ROLE openfox_migrator;

CREATE UNIQUE INDEX integration_connections_tapd_user_unique
  ON openfox.integration_connections (organization_id, external_id)
  WHERE provider = 'tapd' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';

CREATE UNIQUE INDEX integration_connections_tapd_nick_unique
  ON openfox.integration_connections (organization_id, (settings->>'nick'))
  WHERE provider = 'tapd' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';

RESET ROLE;
