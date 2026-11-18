-- I04 operational rollback only. Stop the affected API and import workers first.
-- Set openfox.rollback_organization_id on this maintenance connection to the
-- explicitly selected organization UUID. No default/all-organization target.
-- Run before replacing the application. Retain migration 135 and copied tasks.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT id FROM openfox.integration_connections
 WHERE organization_id=current_setting('openfox.rollback_organization_id')::uuid
 AND provider='yunxiao' ORDER BY scope,id FOR UPDATE;
UPDATE openfox.integration_connections SET status='DISCONNECTED',
 status_reason='I04 application rollback',credentials_sealed=NULL,
 credentials_key_id='',credentials_hint='',disconnected_at=clock_timestamp(),
 updated_at=clock_timestamp(),version=version+1
 WHERE organization_id=current_setting('openfox.rollback_organization_id')::uuid
 AND provider='yunxiao' AND (status<>'DISCONNECTED' OR credentials_sealed IS NOT NULL);
UPDATE openfox.import_sync_links l SET state='DISCONNECTED',lease=NULL,
 version=l.version+1,error_code=''
 FROM openfox.import_jobs j
 WHERE l.organization_id=current_setting('openfox.rollback_organization_id')::uuid
 AND j.organization_id=l.organization_id AND j.id=l.job_id AND j.source='yunxiao_api'
 AND l.state<>'DISCONNECTED';
UPDATE openfox.import_jobs SET status='DISCARDED',updated_at=clock_timestamp(),version=version+1
 WHERE organization_id=current_setting('openfox.rollback_organization_id')::uuid
 AND source='yunxiao_api' AND status='DRAFT';
UPDATE openfox.import_jobs SET status='CANCELLED',phase='',finished_at=clock_timestamp(),
 cancel_requested_at=coalesce(cancel_requested_at,clock_timestamp()),
 lease_owner='',lease_expires_at=NULL,retry_at=NULL,updated_at=clock_timestamp(),version=version+1
 WHERE organization_id=current_setting('openfox.rollback_organization_id')::uuid
 AND source='yunxiao_api' AND status IN('QUEUED','RUNNING','CANCEL_REQUESTED');
COMMIT;
-- Verification: all three counts must be zero. Tasks/source mappings are retained.
SELECT
 (SELECT count(*) FROM openfox.integration_connections WHERE organization_id=current_setting('openfox.rollback_organization_id')::uuid AND provider='yunxiao' AND (status<>'DISCONNECTED' OR credentials_sealed IS NOT NULL)) AS active_credentials,
 (SELECT count(*) FROM openfox.import_sync_links l JOIN openfox.import_jobs j ON j.organization_id=l.organization_id AND j.id=l.job_id WHERE j.organization_id=current_setting('openfox.rollback_organization_id')::uuid AND j.source='yunxiao_api' AND l.state<>'DISCONNECTED') AS active_sync,
 (SELECT count(*) FROM openfox.import_jobs WHERE organization_id=current_setting('openfox.rollback_organization_id')::uuid AND source='yunxiao_api' AND status IN('DRAFT','QUEUED','RUNNING','CANCEL_REQUESTED')) AS active_jobs;
