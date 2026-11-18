-- I05: delegated workspace OAuth, identity-only personal bindings and bounded
-- resumable inbound project crawls. No business-data backfill or remote writes.
-- Apply only to the authorized isolated database. Retain schema and source
-- mappings when withdrawing the feature; do not shrink source CHECK with rows.
SET ROLE openfox_migrator;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_source_valid,
 ADD CONSTRAINT import_jobs_source_valid CHECK
 (source IN ('jira_csv','jira_cloud','jira_server','space_clone','teambition_csv','pingcode_csv','tapd_excel','tapd_api','pingcode_api','shortcut_api','zentao_api','teambition_api','yunxiao_api','asana_api'));

CREATE TABLE openfox.integration_asana_grants (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 app_client_id text NOT NULL CHECK (length(app_client_id) BETWEEN 1 AND 255),
 subject_id text NOT NULL CHECK (length(subject_id) BETWEEN 1 AND 255),
 subject_name text NOT NULL CHECK (length(subject_name) <= 200),
 credentials_sealed bytea,
 credentials_key_id text NOT NULL DEFAULT '',
 scope text NOT NULL,
 expires_at timestamptz,
 status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','NEEDS_ATTENTION','DISCONNECTED')),
 generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
 credentials_version bigint NOT NULL DEFAULT 1 CHECK (credentials_version > 0),
 refresh_lease uuid,
 refresh_lease_until timestamptz,
 request_lease uuid,
 request_lease_until timestamptz,
 next_request_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 retry_at timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK (length(last_error) <= 100),
 created_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,app_client_id,subject_id),
 FOREIGN KEY(organization_id,created_by) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK ((refresh_lease IS NULL) = (refresh_lease_until IS NULL)),
 CHECK ((request_lease IS NULL) = (request_lease_until IS NULL)),
 CHECK (credentials_sealed IS NULL OR octet_length(credentials_sealed) <= 16384),
 CHECK (length(credentials_key_id) <= 64),
 CHECK (status <> 'ACTIVE' OR (credentials_sealed IS NOT NULL AND credentials_key_id <> '' AND expires_at IS NOT NULL))
);

-- One subject belongs to one OpenFox member while any workspace binding is
-- live. A member may prove the same identity in multiple workspace children.
-- This registry contains no secret. After the last child is disconnected, a
-- newly verified claimant may transfer the registry row under its row lock.
CREATE TABLE openfox.integration_asana_identity_claims (
 organization_id uuid NOT NULL,
 subject_id text NOT NULL CHECK (length(subject_id) BETWEEN 1 AND 255),
 owner_user_id uuid NOT NULL,
 PRIMARY KEY(organization_id,subject_id),
 UNIQUE(organization_id,subject_id,owner_user_id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id)
);

ALTER TABLE openfox.integration_connections ADD COLUMN asana_grant_id uuid,
 ADD COLUMN asana_identity_subject text GENERATED ALWAYS AS
 (CASE WHEN provider='asana' AND scope='PERSONAL' AND status<>'DISCONNECTED' THEN external_id END) STORED,
 ADD CONSTRAINT integration_connections_asana_grant_fk FOREIGN KEY(organization_id,asana_grant_id)
 REFERENCES openfox.integration_asana_grants(organization_id,id),
 ADD CONSTRAINT integration_connections_asana_claim_fk FOREIGN KEY(organization_id,asana_identity_subject,owner_user_id)
 REFERENCES openfox.integration_asana_identity_claims(organization_id,subject_id,owner_user_id),
 ADD CONSTRAINT integration_connections_asana_shape CHECK (
  provider<>'asana' OR (
   credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint=''
   AND ((scope='ORGANIZATION' AND asana_grant_id IS NOT NULL AND account_kind='workspace' AND coalesce(settings->>'auth_method','')='asana_oauth')
    OR (scope='PERSONAL' AND asana_grant_id IS NULL AND parent_connection_id IS NOT NULL AND account_kind='identity'
     AND coalesce(settings->>'auth_method','')='asana_identity'
     AND length(coalesce(settings->>'company_id','')) BETWEEN 1 AND 255
     AND coalesce(settings->>'parent_version','') ~ '^[1-9][0-9]{0,18}$'
     AND length(coalesce(settings->>'verified_email','')) BETWEEN 1 AND 320))));
CREATE INDEX integration_connections_asana_grant ON openfox.integration_connections(organization_id,asana_grant_id,id)
 WHERE provider='asana' AND scope='ORGANIZATION' AND status<>'DISCONNECTED';

CREATE TABLE openfox.import_asana_crawls (
 organization_id uuid NOT NULL,
 job_id uuid NOT NULL,
 cycle_id uuid NOT NULL DEFAULT gen_random_uuid(),
 kind text NOT NULL CHECK (kind IN ('INITIAL','SYNC')),
 state text NOT NULL DEFAULT 'FETCHING' CHECK (state IN ('FETCHING','READY','APPLYING','DONE','FAILED')),
 connection_id uuid NOT NULL,
 connection_version bigint NOT NULL CHECK (connection_version > 0),
 grant_generation bigint NOT NULL CHECK (grant_generation > 0),
 workspace_id text NOT NULL CHECK (length(workspace_id) BETWEEN 1 AND 255),
 project_id text NOT NULL CHECK (length(project_id) BETWEEN 1 AND 128),
 root_offset text NOT NULL DEFAULT '' CHECK (octet_length(root_offset) <= 8192),
 root_offsets text[] NOT NULL DEFAULT '{}' CHECK (cardinality(root_offsets) <= 51),
 roots_done boolean NOT NULL DEFAULT false,
 request_count integer NOT NULL DEFAULT 0 CHECK (request_count BETWEEN 0 AND 10000),
 data_bytes integer NOT NULL DEFAULT 0 CHECK (data_bytes BETWEEN 0 AND 10485760),
 item_count integer NOT NULL DEFAULT 0 CHECK (item_count BETWEEN 0 AND 5000),
 failures integer NOT NULL DEFAULT 0 CHECK (failures BETWEEN 0 AND 3),
 offset_restarts integer NOT NULL DEFAULT 0 CHECK (offset_restarts BETWEEN 0 AND 1),
 lease uuid,
 lease_until timestamptz,
 retry_at timestamptz,
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '2 hours'),
 PRIMARY KEY(organization_id,job_id),
 UNIQUE(organization_id,job_id,cycle_id),
 FOREIGN KEY(organization_id,job_id) REFERENCES openfox.import_jobs(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 CHECK ((lease IS NULL) = (lease_until IS NULL))
);
CREATE TABLE openfox.import_asana_nodes (
 organization_id uuid NOT NULL,
 job_id uuid NOT NULL,
 cycle_id uuid NOT NULL,
 external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 255),
 parent_id text NOT NULL DEFAULT '' CHECK (length(parent_id) <= 255),
 source_data jsonb NOT NULL CHECK (jsonb_typeof(source_data)='object' AND octet_length(source_data::text) <= 262144),
 children_done boolean NOT NULL,
 discovery_depth smallint NOT NULL DEFAULT 0 CHECK(discovery_depth BETWEEN 0 AND 10),
 child_offset text NOT NULL DEFAULT '' CHECK (octet_length(child_offset) <= 8192),
 child_offsets text[] NOT NULL DEFAULT '{}' CHECK (cardinality(child_offsets) <= 51),
 applied boolean NOT NULL DEFAULT false,
 PRIMARY KEY(organization_id,job_id,cycle_id,external_id),
 FOREIGN KEY(organization_id,job_id,cycle_id) REFERENCES openfox.import_asana_crawls(organization_id,job_id,cycle_id) ON DELETE CASCADE
);
CREATE INDEX import_asana_node_frontier ON openfox.import_asana_nodes(organization_id,job_id,cycle_id,discovery_depth,external_id) WHERE NOT children_done;
ALTER TABLE openfox.import_sync_items ADD COLUMN source_completed_at timestamptz;

ALTER TABLE openfox.integration_asana_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_asana_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_asana_identity_claims ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_asana_identity_claims FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_asana_crawls ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_asana_crawls FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_asana_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_asana_nodes FORCE ROW LEVEL SECURITY;
CREATE POLICY asana_grant_org ON openfox.integration_asana_grants
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
CREATE POLICY asana_claim_org ON openfox.integration_asana_identity_claims
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
CREATE POLICY asana_crawl_org ON openfox.import_asana_crawls
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
CREATE POLICY asana_node_org ON openfox.import_asana_nodes
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_asana_grants,openfox.integration_asana_identity_claims TO openfox_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.import_asana_crawls,openfox.import_asana_nodes TO openfox_app;

SET ROLE openfox_migrator;
CREATE INDEX integration_connections_asana_identity ON openfox.integration_connections(organization_id,asana_identity_subject)
 WHERE asana_identity_subject IS NOT NULL;
CREATE INDEX integration_asana_grants_refresh ON openfox.integration_asana_grants(expires_at,organization_id,id) WHERE status='ACTIVE';
CREATE POLICY asana_grant_scan ON openfox.integration_asana_grants FOR SELECT TO openfox_migrator USING(true);
-- Narrow control-plane scanner returns tenant IDs, never credentials or users.
CREATE FUNCTION openfox_private.list_asana_maintenance(p_after uuid,p_limit integer)
RETURNS TABLE(organization_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT candidate.organization_id FROM (
  SELECT g.organization_id FROM openfox.integration_asana_grants g WHERE g.status='ACTIVE' AND g.expires_at<clock_timestamp()+interval '5 minutes'
  UNION
  SELECT s.organization_id FROM openfox.integration_oauth_states s WHERE s.provider='asana' AND s.status IN ('PENDING','SELECTING') AND s.expires_at<=clock_timestamp()
 ) candidate JOIN openfox.organization_directory d ON d.organization_id=candidate.organization_id AND d.deleted_at IS NULL
 WHERE p_after IS NULL OR candidate.organization_id>p_after ORDER BY candidate.organization_id LIMIT least(greatest(p_limit,1),100)
$$;
ALTER FUNCTION openfox_private.list_asana_maintenance(uuid,integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_asana_maintenance(uuid,integer) FROM PUBLIC;
RESET ROLE;
GRANT EXECUTE ON FUNCTION openfox_private.list_asana_maintenance(uuid,integer) TO openfox_context;
