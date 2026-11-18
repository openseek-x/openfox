-- P02: identity only, no personal credentials and no business-data backfill.
-- Existing per-owner/per-parent uniqueness remains. A remote enterprise subject
-- can be claimed by only one member, including across duplicate app connections.
SET ROLE openfox_migrator;
CREATE UNIQUE INDEX integration_connections_teambition_user_unique
 ON openfox.integration_connections(organization_id,(settings->>'company_id'),external_id)
 WHERE provider='teambition' AND scope='PERSONAL' AND status<>'DISCONNECTED';
ALTER TABLE openfox.integration_connections ADD CONSTRAINT integration_connections_teambition_identity_valid CHECK (
 provider<>'teambition' OR scope<>'PERSONAL' OR (
 parent_connection_id IS NOT NULL AND account_kind='identity'
 AND credentials_sealed IS NULL AND credentials_key_id=''
 AND COALESCE(settings->>'auth_method','')='teambition_identity'
 AND COALESCE(settings->>'company_id','') ~ '^[a-zA-Z0-9_-]{1,100}$'
 AND COALESCE(settings->>'parent_version','') ~ '^[1-9][0-9]{0,18}$'
 AND char_length(COALESCE(settings->>'verified_email','')) BETWEEN 1 AND 320
 )) NOT VALID;
ALTER TABLE openfox.integration_connections VALIDATE CONSTRAINT integration_connections_teambition_identity_valid;
RESET ROLE;
