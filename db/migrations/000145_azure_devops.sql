-- I12: Azure DevOps Services delegated OAuth, identity-only claims, durable
-- owned Service Hooks/backlinks, and bounded revision-aware Boards crawls.
-- No business backfill and no remote effects. Run only in the approved isolated
-- database. Preserve imported tasks/links/source mappings when withdrawing.
SET ROLE openfox_migrator;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_source_valid,
 ADD CONSTRAINT import_jobs_source_valid CHECK
 (source IN ('jira_csv','jira_cloud','jira_server','space_clone','teambition_csv','pingcode_csv','tapd_excel','tapd_api','pingcode_api','shortcut_api','zentao_api','teambition_api','yunxiao_api','asana_api','azure_devops_api'));
ALTER TABLE openfox.integration_oauth_states DROP CONSTRAINT integration_oauth_states_sealed_size,
 ADD CONSTRAINT integration_oauth_states_sealed_size CHECK (
 (code_verifier_sealed IS NULL OR octet_length(code_verifier_sealed)<=4096) AND
 (pending_sealed IS NULL OR octet_length(pending_sealed)<=CASE WHEN provider='azure_devops' THEN 131072 ELSE 16384 END));

CREATE TABLE openfox.integration_azure_grants (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_client_id uuid NOT NULL,
 resource_tenant_id uuid NOT NULL,
 subject_id uuid NOT NULL,
 subject_name text NOT NULL CHECK (char_length(subject_name)<=200),
 scope text NOT NULL CHECK (octet_length(scope)<=32768),
 credentials_sealed bytea CHECK (octet_length(credentials_sealed)<=131072),
 credentials_key_id text NOT NULL DEFAULT '' CHECK (length(credentials_key_id)<=64),
 expires_at timestamptz,
 status text NOT NULL DEFAULT 'DISCONNECTED' CHECK (status IN ('ACTIVE','NEEDS_ATTENTION','CLEANUP','DISCONNECTED')),
 generation bigint NOT NULL DEFAULT 1 CHECK (generation>0),
 credentials_version bigint NOT NULL DEFAULT 1 CHECK (credentials_version>0),
 refresh_lease uuid,
 refresh_lease_until timestamptz,
 request_lease uuid,
 request_lease_until timestamptz,
 retry_at timestamptz,
 cleanup_until timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK (length(last_error)<=100),
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,app_client_id,resource_tenant_id,subject_id),
 FOREIGN KEY(organization_id,created_by) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK ((refresh_lease IS NULL)=(refresh_lease_until IS NULL)),
 CHECK ((request_lease IS NULL)=(request_lease_until IS NULL)),
 CHECK (status NOT IN ('ACTIVE','CLEANUP') OR (credentials_sealed IS NOT NULL AND credentials_key_id<>'' AND expires_at IS NOT NULL)),
 CHECK (status<>'CLEANUP' OR cleanup_until IS NOT NULL)
);

-- A claim has no secret; namespace is the actual DevOps account and identity.
-- Different accounts may prove the same person independently. A live child FK
-- prevents transfer to another local owner until all that claim's children end.
CREATE TABLE openfox.integration_azure_identity_claims (
 organization_id uuid NOT NULL,
 account_id text NOT NULL CHECK (account_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 subject_id text NOT NULL CHECK (subject_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 owner_user_id uuid NOT NULL,
 PRIMARY KEY(organization_id,account_id,subject_id),
 UNIQUE(organization_id,account_id,subject_id,owner_user_id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id)
);
ALTER TABLE openfox.integration_connections ADD COLUMN azure_grant_id uuid,
 ADD COLUMN azure_identity_account text GENERATED ALWAYS AS
 (CASE WHEN provider='azure_devops' AND scope='PERSONAL' AND status<>'DISCONNECTED' THEN settings->>'account_id' END) STORED,
 ADD COLUMN azure_identity_subject text GENERATED ALWAYS AS
 (CASE WHEN provider='azure_devops' AND scope='PERSONAL' AND status<>'DISCONNECTED' THEN external_id END) STORED,
 ADD CONSTRAINT integration_connections_azure_grant_fk FOREIGN KEY(organization_id,azure_grant_id)
 REFERENCES openfox.integration_azure_grants(organization_id,id),
 ADD CONSTRAINT integration_connections_azure_claim_fk FOREIGN KEY(organization_id,azure_identity_account,azure_identity_subject,owner_user_id)
 REFERENCES openfox.integration_azure_identity_claims(organization_id,account_id,subject_id,owner_user_id),
 ADD CONSTRAINT integration_connections_azure_grant_shape CHECK (azure_grant_id IS NULL OR (provider='azure_devops' AND scope='ORGANIZATION')),
 ADD CONSTRAINT integration_connections_azure_shape CHECK (
 provider<>'azure_devops' OR (
 credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint=''
 AND external_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND ((scope='ORGANIZATION' AND azure_grant_id IS NOT NULL AND account_kind='organization'
  AND coalesce(settings->>'auth_method','')='azure_devops_oauth'
  AND length(coalesce(settings->>'organization_name','')) BETWEEN 1 AND 100)
 OR (scope='PERSONAL' AND azure_grant_id IS NULL AND parent_connection_id IS NOT NULL AND account_kind='identity'
  AND coalesce(settings->>'auth_method','')='azure_devops_identity'
  AND coalesce(settings->>'account_id','') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND coalesce(settings->>'parent_version','') ~ '^[1-9][0-9]{0,18}$'
  AND length(coalesce(settings->>'descriptor','')) BETWEEN 1 AND 2048))));
CREATE INDEX integration_connections_azure_grant ON openfox.integration_connections(organization_id,azure_grant_id,id)
 WHERE provider='azure_devops' AND scope='ORGANIZATION' AND status<>'DISCONNECTED';
CREATE INDEX integration_connections_azure_claim ON openfox.integration_connections(organization_id,azure_identity_account,azure_identity_subject)
 WHERE azure_identity_subject IS NOT NULL;

CREATE TABLE openfox.integration_azure_hooks (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 project_id uuid NOT NULL,
 repository_id uuid,
 event_type text NOT NULL CHECK (event_type IN ('git.push','git.pullrequest.created','git.pullrequest.updated','workitem.created','workitem.updated','workitem.deleted','workitem.restored')),
 resource_version text NOT NULL CHECK (length(resource_version) BETWEEN 1 AND 32),
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 remote_generation bigint NOT NULL DEFAULT 1 CHECK(remote_generation>0 AND remote_generation<=generation),
 connection_version bigint NOT NULL CHECK(connection_version>0),
 grant_generation bigint NOT NULL CHECK(grant_generation>0),
 desired boolean NOT NULL DEFAULT true,
 state text NOT NULL DEFAULT 'CREATE_PENDING' CHECK (state IN ('CREATE_PENDING','CREATING','CREATE_UNKNOWN','ACTIVE','DELETE_PENDING','DELETING','DELETE_UNKNOWN','DELETED','NEEDS_ATTENTION')),
 subscription_id uuid,
 secret_sealed bytea CHECK (octet_length(secret_sealed)<=4096),
 secret_key_id text NOT NULL DEFAULT '' CHECK (length(secret_key_id)<=64),
 lease uuid,
 lease_until timestamptz,
 retry_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 cleanup_until timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=100),
 last_delivery_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(id),
 UNIQUE NULLS NOT DISTINCT(organization_id,connection_id,project_id,repository_id,event_type),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 CHECK ((lease IS NULL)=(lease_until IS NULL)),
 CHECK (state IN ('DELETED','NEEDS_ATTENTION') OR (secret_sealed IS NOT NULL AND secret_key_id<>'')),
 CHECK (state<>'ACTIVE' OR subscription_id IS NOT NULL),
 CHECK ((event_type LIKE 'git.%')=(repository_id IS NOT NULL))
);
CREATE INDEX integration_azure_hooks_due ON openfox.integration_azure_hooks(retry_at,organization_id,id)
 WHERE state NOT IN ('ACTIVE','DELETED');

CREATE TABLE openfox.integration_azure_work (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 hook_id uuid NOT NULL,
 hook_generation bigint NOT NULL CHECK(hook_generation>0),
 connection_version bigint NOT NULL CHECK(connection_version>0),
 grant_generation bigint NOT NULL CHECK(grant_generation>0),
 account_id uuid NOT NULL,
 project_id uuid NOT NULL,
 repository_id uuid,
 kind text NOT NULL CHECK(kind IN ('PULL_REQUEST','PUSH','WORK_ITEM')),
 object_id integer NOT NULL CHECK(object_id>0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','DONE','FAILED','IGNORED')),
 lease uuid,
 lease_until timestamptz,
 retry_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=100),
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE NULLS NOT DISTINCT(organization_id,connection_id,project_id,repository_id,kind,object_id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,hook_id) REFERENCES openfox.integration_azure_hooks(organization_id,id),
 CHECK ((lease IS NULL)=(lease_until IS NULL)),
 CHECK ((kind<>'WORK_ITEM')=(repository_id IS NOT NULL))
);
CREATE INDEX integration_azure_work_due ON openfox.integration_azure_work(retry_at,organization_id,id) WHERE state IN ('PENDING','RUNNING');

-- repository remains the stable UUID namespace used by the existing indexes.
-- Only its human label changes when the remote organization/project/repo rename.
ALTER TABLE openfox.task_git_links ADD COLUMN repository_display text NOT NULL DEFAULT '' CHECK(char_length(repository_display)<=255);
CREATE TABLE openfox.integration_azure_backlinks (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 link_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 connection_version bigint NOT NULL CHECK(connection_version>0),
 grant_generation bigint NOT NULL CHECK(grant_generation>0),
 project_id uuid NOT NULL,
 repository_id uuid NOT NULL,
 pr_number integer NOT NULL CHECK(pr_number>0),
 marker text NOT NULL CHECK(length(marker) BETWEEN 1 AND 128),
 body text NOT NULL CHECK(octet_length(body) BETWEEN 1 AND 65536),
 author_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','UNKNOWN','DONE','SKIPPED','FAILED')),
 thread_id integer CHECK(thread_id>0),
 comment_id integer CHECK(comment_id>0),
 lease uuid,
 lease_until timestamptz,
 retry_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=100),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,link_id),
 FOREIGN KEY(organization_id,link_id) REFERENCES openfox.task_git_links(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 CHECK ((lease IS NULL)=(lease_until IS NULL)),
 CHECK (state<>'DONE' OR (thread_id IS NOT NULL AND comment_id IS NOT NULL))
);
CREATE INDEX integration_azure_backlinks_due ON openfox.integration_azure_backlinks(retry_at,organization_id,id) WHERE state IN ('PENDING','SENDING','UNKNOWN');

CREATE TABLE openfox.import_azure_crawls (
 organization_id uuid NOT NULL,
 job_id uuid NOT NULL,
 cycle_id uuid NOT NULL DEFAULT gen_random_uuid(),
 kind text NOT NULL CHECK(kind IN ('INITIAL','SYNC','HISTORY')),
 state text NOT NULL DEFAULT 'FETCHING' CHECK(state IN ('FETCHING','READY','APPLYING','DONE','FAILED')),
 connection_id uuid NOT NULL,
 connection_version bigint NOT NULL CHECK(connection_version>0),
 grant_generation bigint NOT NULL CHECK(grant_generation>0),
 account_id uuid NOT NULL,
 project_id uuid NOT NULL,
 project_name text NOT NULL CHECK(length(project_name) BETWEEN 1 AND 255),
 metadata jsonb NOT NULL CHECK(jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=262144),
 include_history boolean NOT NULL DEFAULT false,
 cutover timestamptz NOT NULL,
 watermark timestamptz NOT NULL,
 snapshot_at timestamptz,
 changed_from timestamptz,
 cursor_id integer NOT NULL DEFAULT 0 CHECK(cursor_id>=0),
 page_ids integer[] NOT NULL DEFAULT '{}' CHECK(cardinality(page_ids)<=200),
 item_count integer NOT NULL DEFAULT 0 CHECK(item_count BETWEEN 0 AND 5000),
 data_bytes integer NOT NULL DEFAULT 0 CHECK(data_bytes BETWEEN 0 AND 10485760),
 failures integer NOT NULL DEFAULT 0 CHECK(failures BETWEEN 0 AND 3),
 lease uuid,
 lease_until timestamptz,
 retry_at timestamptz,
 deadline timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '2 hours'),
 PRIMARY KEY(organization_id,job_id),
 UNIQUE(organization_id,job_id,cycle_id),
 FOREIGN KEY(organization_id,job_id) REFERENCES openfox.import_jobs(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 CHECK ((lease IS NULL)=(lease_until IS NULL))
);
CREATE TABLE openfox.import_azure_nodes (
 organization_id uuid NOT NULL,
 job_id uuid NOT NULL,
 cycle_id uuid NOT NULL,
 external_id integer NOT NULL CHECK(external_id>0),
 remote_revision integer NOT NULL CHECK(remote_revision>0),
 source_data jsonb NOT NULL CHECK(jsonb_typeof(source_data)='object' AND octet_length(source_data::text)<=262144),
 applied boolean NOT NULL DEFAULT false,
 PRIMARY KEY(organization_id,job_id,cycle_id,external_id),
 FOREIGN KEY(organization_id,job_id,cycle_id) REFERENCES openfox.import_azure_crawls(organization_id,job_id,cycle_id) ON DELETE CASCADE
);
ALTER TABLE openfox.import_sync_items ADD COLUMN source_revision integer NOT NULL DEFAULT 0 CHECK(source_revision>=0);

DO $rls$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['integration_azure_grants','integration_azure_identity_claims','integration_azure_hooks','integration_azure_work','integration_azure_backlinks','import_azure_crawls','import_azure_nodes'] LOOP
  EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY azure_tenant ON openfox.%I USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id())',tab);
 END LOOP;
END
$rls$;
CREATE POLICY azure_grant_scan ON openfox.integration_azure_grants FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY azure_hook_scan ON openfox.integration_azure_hooks FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY azure_work_scan ON openfox.integration_azure_work FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY azure_backlink_scan ON openfox.integration_azure_backlinks FOR SELECT TO openfox_migrator USING(true);
CREATE INDEX integration_azure_grants_expiry ON openfox.integration_azure_grants(expires_at,organization_id,id) WHERE status IN ('ACTIVE','CLEANUP');

CREATE FUNCTION openfox_private.resolve_azure_hook(p_id uuid,p_generation bigint)
RETURNS TABLE(organization_id uuid,hook_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT h.organization_id,h.id FROM openfox.integration_azure_hooks h
 JOIN openfox.organization_directory d ON d.organization_id=h.organization_id AND d.deleted_at IS NULL
 WHERE h.id=p_id AND h.generation=p_generation AND h.desired
$$;
CREATE FUNCTION openfox_private.list_azure_maintenance(p_after uuid,p_limit integer)
RETURNS TABLE(organization_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT candidate.organization_id FROM (
  SELECT organization_id FROM openfox.integration_azure_grants WHERE status IN ('ACTIVE','CLEANUP') AND (expires_at<clock_timestamp()+interval '5 minutes' OR cleanup_until<=clock_timestamp())
  UNION SELECT organization_id FROM openfox.integration_azure_hooks WHERE state NOT IN ('ACTIVE','DELETED') AND retry_at<=clock_timestamp()
  UNION SELECT organization_id FROM openfox.integration_azure_work WHERE state IN ('PENDING','RUNNING') AND retry_at<=clock_timestamp()
  UNION SELECT organization_id FROM openfox.integration_azure_backlinks WHERE state IN ('PENDING','SENDING','UNKNOWN') AND retry_at<=clock_timestamp()
  UNION SELECT organization_id FROM openfox.integration_oauth_states WHERE provider='azure_devops' AND status IN ('PENDING','SELECTING') AND expires_at<=clock_timestamp()
 ) candidate JOIN openfox.organization_directory d ON d.organization_id=candidate.organization_id AND d.deleted_at IS NULL
 WHERE p_after IS NULL OR candidate.organization_id>p_after ORDER BY candidate.organization_id LIMIT least(greatest(p_limit,1),100)
$$;
REVOKE ALL ON FUNCTION openfox_private.resolve_azure_hook(uuid,bigint),openfox_private.list_azure_maintenance(uuid,integer) FROM PUBLIC;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_azure_grants,openfox.integration_azure_identity_claims,openfox.integration_azure_hooks,openfox.integration_azure_work,openfox.integration_azure_backlinks TO openfox_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.import_azure_crawls,openfox.import_azure_nodes TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_azure_hook(uuid,bigint),openfox_private.list_azure_maintenance(uuid,integer) TO openfox_context;
