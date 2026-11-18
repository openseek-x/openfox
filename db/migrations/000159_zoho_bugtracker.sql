-- I06: US BugTracker delegated OAuth and bounded one-shot read-only import.
-- No remote writeback, business-data backfill, or destructive rollback.
SET ROLE openfox_migrator;
ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_source_valid,
 ADD CONSTRAINT import_jobs_source_valid CHECK
 (source IN ('jira_csv','jira_cloud','jira_server','space_clone','teambition_csv','pingcode_csv','tapd_excel','tapd_api','pingcode_api','shortcut_api','zentao_api','teambition_api','yunxiao_api','asana_api','azure_devops_api','zoho_bugtracker_api'));

CREATE TABLE openfox.integration_zoho_runtime (
 organization_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 subject_id text NOT NULL CHECK(subject_id ~ '^[1-9][0-9]{0,63}$'),
 subject_name text NOT NULL CHECK(char_length(subject_name) BETWEEN 1 AND 200),
 app_id text NOT NULL CHECK(char_length(app_id) BETWEEN 1 AND 255),
 refresh_fingerprint bytea CHECK(octet_length(refresh_fingerprint)=32),
 credential_generation bigint NOT NULL DEFAULT 1 CHECK(credential_generation>0),
 credentials_version bigint NOT NULL DEFAULT 1 CHECK(credentials_version>0),
 refresh_lease uuid,
 refresh_lease_until timestamptz,
 request_lease uuid,
 request_lease_until timestamptz,
 refresh_failures smallint NOT NULL DEFAULT 0 CHECK(refresh_failures BETWEEN 0 AND 3),
 retry_at timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=100),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,connection_id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 UNIQUE(app_id,refresh_fingerprint),
 CHECK((refresh_lease IS NULL)=(refresh_lease_until IS NULL)),
 CHECK((request_lease IS NULL)=(request_lease_until IS NULL)),
 CHECK(refresh_lease IS NULL OR request_lease IS NULL)
);
CREATE UNIQUE INDEX integration_zoho_personal_identity ON openfox.integration_connections(organization_id,parent_connection_id,external_id)
 WHERE provider='zoho_bugtracker' AND scope='PERSONAL' AND status<>'DISCONNECTED';
ALTER TABLE openfox.integration_connections
 ADD CONSTRAINT integration_zoho_shape CHECK(provider<>'zoho_bugtracker' OR (
 external_id ~ '^us:[1-9][0-9]{0,63}$'
 AND ((scope='ORGANIZATION' AND account_kind='portal' AND coalesce(settings->>'auth_method','')='zoho_oauth'
 AND (status<>'ACTIVE' OR (credentials_sealed IS NOT NULL AND credentials_key_id<>'' AND token_expires_at IS NOT NULL)))
 OR (scope='PERSONAL' AND parent_connection_id IS NOT NULL AND account_kind='identity'
 AND coalesce(settings->>'auth_method','')='zoho_bugtracker_identity'
 AND coalesce(settings->>'company_id','') ~ '^us:[1-9][0-9]{0,63}$'
 AND coalesce(settings->>'parent_version','') ~ '^[1-9][0-9]{0,18}$'
 AND char_length(coalesce(settings->>'verified_email','')) BETWEEN 1 AND 320
 AND credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint='' AND token_expires_at IS NULL))));
ALTER TABLE openfox.integration_oauth_states ADD CONSTRAINT integration_zoho_personal_no_token CHECK
 (provider<>'zoho_bugtracker' OR purpose<>'PERSONAL' OR pending_sealed IS NULL);

CREATE TABLE openfox.import_zoho_crawls (
 organization_id uuid NOT NULL,
 job_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 connection_version bigint NOT NULL CHECK(connection_version>0),
 credential_generation bigint NOT NULL CHECK(credential_generation>0),
 portal_id text NOT NULL CHECK(portal_id ~ '^[1-9][0-9]{0,63}$'),
 project_id text NOT NULL CHECK(project_id ~ '^[1-9][0-9]{0,63}$'),
 partition text NOT NULL DEFAULT 'open' CHECK(partition IN ('open','closed','done')),
 next_index integer NOT NULL DEFAULT 1 CHECK(next_index BETWEEN 1 AND 5101),
 item_count integer NOT NULL DEFAULT 0 CHECK(item_count BETWEEN 0 AND 5000),
 data_bytes integer NOT NULL DEFAULT 0 CHECK(data_bytes BETWEEN 0 AND 10485760),
 request_count integer NOT NULL DEFAULT 0 CHECK(request_count BETWEEN 0 AND 160),
 failures smallint NOT NULL DEFAULT 0 CHECK(failures BETWEEN 0 AND 3),
 lease uuid,
 lease_until timestamptz,
 retry_at timestamptz,
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '2 hours',
 PRIMARY KEY(organization_id,job_id),
 FOREIGN KEY(organization_id,job_id) REFERENCES openfox.import_jobs(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_zoho_runtime(organization_id,connection_id),
 CHECK((lease IS NULL)=(lease_until IS NULL))
);
ALTER TABLE openfox.integration_zoho_runtime ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_zoho_runtime FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_zoho_crawls ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_zoho_crawls FORCE ROW LEVEL SECURITY;
CREATE POLICY zoho_runtime_org ON openfox.integration_zoho_runtime
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
CREATE POLICY zoho_crawl_org ON openfox.import_zoho_crawls
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_zoho_runtime TO openfox_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.import_zoho_crawls TO openfox_app;

-- One fixed control-plane row budgets this service's Zoho requests across all
-- organizations/processes. It contains no tenant or credential information.
SET ROLE openfox_migrator;
CREATE TABLE openfox_private.zoho_request_budget (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 cooldown_until timestamptz NOT NULL DEFAULT '-infinity'
);
INSERT INTO openfox_private.zoho_request_budget(singleton) VALUES(true);
CREATE FUNCTION openfox_private.reserve_zoho_request() RETURNS double precision
 LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox_private AS $$
 DECLARE n timestamptz; ready timestamptz;
 BEGIN
 SELECT greatest(next_at,cooldown_until) INTO ready FROM openfox_private.zoho_request_budget WHERE singleton FOR UPDATE;
 n:=clock_timestamp();
 IF ready>n THEN RETURN extract(epoch FROM ready-n); END IF;
 UPDATE openfox_private.zoho_request_budget SET next_at=n+interval '2 seconds' WHERE singleton;
 RETURN 0;
 END $$;
CREATE FUNCTION openfox_private.defer_zoho_requests(p_until timestamptz) RETURNS void
 LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,openfox_private AS $$
 UPDATE openfox_private.zoho_request_budget SET cooldown_until=greatest(cooldown_until,p_until)
 WHERE singleton AND isfinite(p_until) AND p_until>clock_timestamp()
 $$;
REVOKE ALL ON openfox_private.zoho_request_budget FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.reserve_zoho_request(),openfox_private.defer_zoho_requests(timestamptz) FROM PUBLIC;
RESET ROLE;
GRANT EXECUTE ON FUNCTION openfox_private.reserve_zoho_request(),openfox_private.defer_zoho_requests(timestamptz) TO openfox_context;

-- Reuse the already-running OAuth maintenance pass; no additional worker.
SET ROLE openfox_migrator;
CREATE OR REPLACE FUNCTION openfox_private.list_asana_maintenance(p_after uuid,p_limit integer)
RETURNS TABLE(organization_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT candidate.organization_id FROM (
  SELECT g.organization_id FROM openfox.integration_asana_grants g WHERE g.status='ACTIVE' AND g.expires_at<clock_timestamp()+interval '5 minutes'
  UNION
  SELECT s.organization_id FROM openfox.integration_oauth_states s WHERE s.provider IN ('asana','zoho_bugtracker') AND s.status IN ('PENDING','SELECTING') AND s.expires_at<=clock_timestamp()
 ) candidate JOIN openfox.organization_directory d ON d.organization_id=candidate.organization_id AND d.deleted_at IS NULL
 WHERE p_after IS NULL OR candidate.organization_id>p_after ORDER BY candidate.organization_id LIMIT least(greatest(p_limit,1),100)
$$;
ALTER FUNCTION openfox_private.list_asana_maintenance(uuid,integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_asana_maintenance(uuid,integer) FROM PUBLIC;
RESET ROLE;
GRANT EXECUTE ON FUNCTION openfox_private.list_asana_maintenance(uuid,integer) TO openfox_context;
