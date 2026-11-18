-- I04: center-edition read-only work-item imports and verified personal identities.
-- No backfill; only source CHECK and provider-specific identity invariants.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_source_valid,
 ADD CONSTRAINT import_jobs_source_valid CHECK
 (source IN ('jira_csv','jira_cloud','jira_server','space_clone','teambition_csv','pingcode_csv','tapd_excel','tapd_api','pingcode_api','shortcut_api','zentao_api','teambition_api','yunxiao_api'));
CREATE UNIQUE INDEX integration_connections_yunxiao_identity_unique
 ON openfox.integration_connections(organization_id,external_id)
 WHERE provider='yunxiao' AND scope='PERSONAL' AND status<>'DISCONNECTED';
ALTER TABLE openfox.integration_connections ADD CONSTRAINT integration_connections_yunxiao_identity_valid CHECK (
 provider<>'yunxiao' OR scope<>'PERSONAL' OR (
 parent_connection_id IS NOT NULL AND owner_user_id IS NOT NULL AND account_kind='identity'
 AND credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint=''
 AND coalesce(settings->>'auth_method','')='yunxiao_identity'
 AND coalesce(settings->>'company_id','')<>'' AND coalesce(settings->>'external_user_id','')<>''
 AND coalesce(settings->>'verified_email','')<>'' AND coalesce(settings->>'parent_version','')<>''
 ));
RESET ROLE;
