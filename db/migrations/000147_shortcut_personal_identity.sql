-- P03: workspace-scoped personal identity with optional explicit comment access.
-- No business-data backfill and no personal Token for identity-only bindings.
-- 131 intentionally remains absent. Execute only after the current migration chain.
-- Rollback after disabling Shortcut personal comments and stopping related workers:
-- ALTER TABLE openfox.integration_connections DROP CONSTRAINT integration_connections_shortcut_identity_valid;
-- DROP INDEX openfox.integration_connections_shortcut_user_unique;
SET ROLE openfox_migrator;

CREATE UNIQUE INDEX integration_connections_shortcut_user_unique
 ON openfox.integration_connections(organization_id,(settings->>'company_id'),external_id)
 WHERE provider='shortcut' AND scope='PERSONAL' AND status<>'DISCONNECTED';

ALTER TABLE openfox.integration_connections ADD CONSTRAINT integration_connections_shortcut_identity_valid CHECK (
 provider<>'shortcut' OR scope<>'PERSONAL' OR (
 parent_connection_id IS NOT NULL AND account_kind='identity'
 AND external_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND COALESCE(settings->>'auth_method','')='shortcut_identity'
 AND COALESCE(settings->>'company_id','') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND COALESCE(settings->>'parent_version','') ~ '^[1-9][0-9]{0,18}$'
 AND char_length(COALESCE(settings->>'verified_email','')) BETWEEN 1 AND 320
 AND COALESCE(jsonb_typeof(settings->'comments_enabled'),'')='boolean'
 AND (
   (status='DISCONNECTED' AND credentials_sealed IS NULL AND credentials_key_id='')
   OR (settings->>'comments_enabled'='false' AND credentials_sealed IS NULL AND credentials_key_id='' AND NOT settings ? 'comments_authorized_at')
   OR (status<>'DISCONNECTED' AND settings->>'comments_enabled'='true' AND credentials_sealed IS NOT NULL AND credentials_key_id<>''
       AND COALESCE(jsonb_typeof(settings->'comments_authorized_at'),'')='string'
       AND char_length(COALESCE(settings->>'comments_authorized_at','')) BETWEEN 20 AND 40)
  )
 )) NOT VALID;
ALTER TABLE openfox.integration_connections VALIDATE CONSTRAINT integration_connections_shortcut_identity_valid;
RESET ROLE;
