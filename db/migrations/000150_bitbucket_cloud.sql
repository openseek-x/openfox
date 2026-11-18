-- I11 authorization slice. Root serially extends this migration with repo/work
-- tables. Execute only on the dedicated isolated database; no data backfill.
SET ROLE openfox_migrator;

ALTER TABLE openfox.integration_connections DROP CONSTRAINT integration_connections_credentials_size,
 ADD CONSTRAINT integration_connections_credentials_size CHECK
 (credentials_sealed IS NULL OR octet_length(credentials_sealed)<=CASE WHEN provider='bitbucket' THEN 32768 ELSE 16384 END);
ALTER TABLE openfox.integration_oauth_states DROP CONSTRAINT integration_oauth_states_sealed_size,
 ADD CONSTRAINT integration_oauth_states_sealed_size CHECK (
 (code_verifier_sealed IS NULL OR octet_length(code_verifier_sealed)<=4096) AND
 (pending_sealed IS NULL OR octet_length(pending_sealed)<=CASE WHEN provider='azure_devops' THEN 131072 WHEN provider='bitbucket' THEN 32768 ELSE 16384 END));

CREATE TABLE openfox.integration_bitbucket_runtime (
 organization_id uuid NOT NULL,
 connection_id uuid NOT NULL,
 subject_id uuid NOT NULL,
 subject_name text NOT NULL CHECK(char_length(subject_name) BETWEEN 1 AND 200),
 consumer_id text NOT NULL CHECK(char_length(consumer_id) BETWEEN 1 AND 255),
 credential_generation bigint NOT NULL DEFAULT 1 CHECK(credential_generation>0),
 credentials_version bigint NOT NULL DEFAULT 1 CHECK(credentials_version>0),
 refresh_lease uuid,
 refresh_lease_until timestamptz,
 request_lease uuid,
 request_lease_until timestamptz,
 retry_at timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=100),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,connection_id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 CHECK((refresh_lease IS NULL)=(refresh_lease_until IS NULL)),
 CHECK((request_lease IS NULL)=(request_lease_until IS NULL)),
 CHECK(refresh_lease IS NULL OR request_lease IS NULL)
);
CREATE UNIQUE INDEX integration_bitbucket_one_workspace ON openfox.integration_connections(organization_id)
 WHERE provider='bitbucket' AND scope='ORGANIZATION' AND status<>'DISCONNECTED';
CREATE UNIQUE INDEX integration_bitbucket_personal_identity ON openfox.integration_connections(organization_id,parent_connection_id,external_id)
 WHERE provider='bitbucket' AND scope='PERSONAL' AND status<>'DISCONNECTED';
ALTER TABLE openfox.integration_connections
 ADD COLUMN bitbucket_identity_parent uuid GENERATED ALWAYS AS
 (CASE WHEN provider='bitbucket' AND scope='PERSONAL' THEN parent_connection_id END) STORED,
 ADD CONSTRAINT integration_bitbucket_identity_parent_fk FOREIGN KEY(organization_id,bitbucket_identity_parent)
 REFERENCES openfox.integration_bitbucket_runtime(organization_id,connection_id),
 ADD CONSTRAINT integration_bitbucket_shape CHECK(provider<>'bitbucket' OR (
 external_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND external_id<>'00000000-0000-0000-0000-000000000000'
 AND ((scope='ORGANIZATION' AND account_kind='workspace' AND coalesce(settings->>'auth_method','')='bitbucket_oauth'
  AND (status<>'ACTIVE' OR (credentials_sealed IS NOT NULL AND credentials_key_id<>'' AND token_expires_at IS NOT NULL)))
 OR (scope='PERSONAL' AND parent_connection_id IS NOT NULL AND account_kind='identity'
  AND coalesce(settings->>'auth_method','')='bitbucket_identity'
  AND coalesce(settings->>'parent_version','') ~ '^[1-9][0-9]{0,18}$'
  AND credentials_sealed IS NULL AND credentials_key_id='' AND credentials_hint='' AND token_expires_at IS NULL))));
ALTER TABLE openfox.integration_oauth_states ADD CONSTRAINT integration_bitbucket_personal_no_secret CHECK
 (provider<>'bitbucket' OR purpose<>'PERSONAL' OR (code_verifier_sealed IS NULL AND pending_sealed IS NULL));
ALTER TABLE openfox.integration_bitbucket_runtime ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_bitbucket_runtime FORCE ROW LEVEL SECURITY;
CREATE POLICY bitbucket_runtime_org ON openfox.integration_bitbucket_runtime
 USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id());
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_bitbucket_runtime TO openfox_app;

-- Repository selection and manually configured hook generation. No historical
-- Bitbucket rows exist; defaults never opt a repository into task visibility.
SET ROLE openfox_migrator;
ALTER TABLE openfox.integration_bitbucket_runtime
 ADD COLUMN hook_generation bigint NOT NULL DEFAULT 1 CHECK(hook_generation>0);
CREATE TABLE openfox.integration_bitbucket_repositories (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 workspace_id uuid NOT NULL CHECK(workspace_id<>'00000000-0000-0000-0000-000000000000'::uuid),
 repository_id uuid NOT NULL CHECK(repository_id<>'00000000-0000-0000-0000-000000000000'::uuid),
 full_name text NOT NULL CHECK(char_length(full_name) BETWEEN 3 AND 255),
 is_private boolean,
 pull_requests boolean NOT NULL DEFAULT true,
 commits boolean NOT NULL DEFAULT false,
 backlink_private boolean NOT NULL DEFAULT true,
 backlink_public boolean NOT NULL DEFAULT false,
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','NEEDS_ATTENTION','REMOVED')),
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=100),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 hook_generation bigint NOT NULL DEFAULT 0 CHECK(hook_generation>=0),
 configured_by uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,connection_id,repository_id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_bitbucket_runtime(organization_id,connection_id),
 FOREIGN KEY(organization_id,configured_by) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE TABLE openfox.integration_bitbucket_repository_spaces (
 organization_id uuid NOT NULL,
 repository_id uuid NOT NULL,
 space_id uuid NOT NULL,
 PRIMARY KEY(organization_id,repository_id,space_id),
 FOREIGN KEY(organization_id,repository_id) REFERENCES openfox.integration_bitbucket_repositories(organization_id,id),
 FOREIGN KEY(organization_id,space_id) REFERENCES openfox.spaces(organization_id,id)
);
DO $rls$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['integration_bitbucket_repositories','integration_bitbucket_repository_spaces'] LOOP
  EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY bitbucket_tenant ON openfox.%I USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id())',tab);
 END LOOP;
END
$rls$;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_bitbucket_repositories TO openfox_app;
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.integration_bitbucket_repository_spaces TO openfox_app;

-- Append to root-owned 142 after repository core; do not apply independently
-- to a database that already records another checksum for migration 142.
SET ROLE openfox_migrator;
ALTER TABLE openfox.integration_webhook_deliveries DROP CONSTRAINT integration_webhook_deliveries_status_valid,
 ADD CONSTRAINT integration_webhook_deliveries_status_valid CHECK(status IN ('QUEUED','PROCESSED','IGNORED','REJECTED','FAILED'));
CREATE TABLE openfox.integration_bitbucket_work (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 selection_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 repository_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('PULL_REQUEST','COMMENT','PUSH')),
 resource_key text NOT NULL CHECK(length(resource_key) BETWEEN 1 AND 128),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND pg_column_size(payload)<=65536),
 connection_version bigint NOT NULL CHECK(connection_version>0),
 credential_generation bigint NOT NULL CHECK(credential_generation>0),
 selection_version bigint NOT NULL CHECK(selection_version>0),
 hook_generation bigint NOT NULL CHECK(hook_generation>0),
 requested_version bigint NOT NULL DEFAULT 1 CHECK(requested_version>0),
 applied_source_at timestamptz,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','DONE','FAILED','IGNORED')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 lease uuid,
 lease_until timestamptz,
 last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=100),
 linked_count integer NOT NULL DEFAULT 0 CHECK(linked_count>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,selection_id,resource_key),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_bitbucket_runtime(organization_id,connection_id),
 FOREIGN KEY(organization_id,selection_id) REFERENCES openfox.integration_bitbucket_repositories(organization_id,id),
 CHECK((lease IS NULL)=(lease_until IS NULL))
);
CREATE INDEX integration_bitbucket_work_due ON openfox.integration_bitbucket_work(next_attempt_at,organization_id,id) WHERE next_attempt_at IS NOT NULL;
CREATE TABLE openfox.integration_bitbucket_deliveries (
 organization_id uuid NOT NULL,
 delivery_id uuid NOT NULL,
 hook_id uuid NOT NULL,
 request_id uuid NOT NULL,
 payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
 work_id uuid,
 requested_version bigint CHECK(requested_version>0),
 PRIMARY KEY(organization_id,delivery_id),
 FOREIGN KEY(organization_id,delivery_id) REFERENCES openfox.integration_webhook_deliveries(organization_id,id),
 FOREIGN KEY(organization_id,work_id) REFERENCES openfox.integration_bitbucket_work(organization_id,id),
 CHECK((work_id IS NULL)=(requested_version IS NULL))
);
CREATE INDEX integration_bitbucket_delivery_work ON openfox.integration_bitbucket_deliveries(organization_id,work_id,requested_version) WHERE work_id IS NOT NULL;
CREATE TABLE openfox.integration_bitbucket_backlinks (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 selection_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 repository_id uuid NOT NULL,
 pull_request_id bigint NOT NULL CHECK(pull_request_id>0),
 connection_version bigint NOT NULL CHECK(connection_version>0),
 selection_version bigint NOT NULL CHECK(selection_version>0),
 hook_generation bigint NOT NULL CHECK(hook_generation>0),
 patchset_hash bytea NOT NULL CHECK(octet_length(patchset_hash)=32),
 link_ids uuid[] NOT NULL CHECK(cardinality(link_ids) BETWEEN 1 AND 100),
 body text NOT NULL CHECK(octet_length(body) BETWEEN 1 AND 32768),
 marker text NOT NULL CHECK(length(marker) BETWEEN 1 AND 128),
 sender_id uuid,
 credential_generation bigint,
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','SENDING','SENT','UNKNOWN','SKIPPED','FAILED')),
 comment_id bigint CHECK(comment_id>0),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 lease uuid,
 lease_until timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 last_error text NOT NULL DEFAULT '' CHECK(length(last_error)<=100),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,workspace_id,repository_id,pull_request_id,patchset_hash),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_bitbucket_runtime(organization_id,connection_id),
 FOREIGN KEY(organization_id,selection_id) REFERENCES openfox.integration_bitbucket_repositories(organization_id,id),
 CHECK((lease IS NULL)=(lease_until IS NULL)),
 CHECK(state NOT IN ('SENDING','SENT','UNKNOWN') OR (sender_id IS NOT NULL AND credential_generation>0)),
 CHECK(state<>'SENT' OR comment_id IS NOT NULL)
);
CREATE INDEX integration_bitbucket_backlinks_due ON openfox.integration_bitbucket_backlinks(next_attempt_at,organization_id,id) WHERE next_attempt_at IS NOT NULL;
CREATE TABLE openfox.integration_bitbucket_comment_sources (
 organization_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 repository_id uuid NOT NULL,
 pull_request_id bigint NOT NULL CHECK(pull_request_id>0),
 comment_id bigint NOT NULL CHECK(comment_id>0),
 task_id uuid NOT NULL,
 activity_id uuid NOT NULL,
 author_id uuid,
 source_url text NOT NULL CHECK(length(source_url) BETWEEN 1 AND 2048),
 occurred_at timestamptz NOT NULL,
 source_updated_at timestamptz NOT NULL,
 deleted boolean NOT NULL DEFAULT false,
 PRIMARY KEY(organization_id,workspace_id,repository_id,pull_request_id,comment_id,task_id),
 FOREIGN KEY(organization_id,task_id) REFERENCES openfox.tasks(organization_id,id),
 FOREIGN KEY(organization_id,activity_id) REFERENCES openfox.task_activities(organization_id,id)
);
DO $rls$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['integration_bitbucket_work','integration_bitbucket_deliveries','integration_bitbucket_backlinks','integration_bitbucket_comment_sources'] LOOP
  EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY bitbucket_tenant ON openfox.%I USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id())',tab);
 END LOOP;
END
$rls$;
CREATE POLICY bitbucket_work_scan ON openfox.integration_bitbucket_work FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY bitbucket_backlinks_scan ON openfox.integration_bitbucket_backlinks FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY bitbucket_repositories_scan ON openfox.integration_bitbucket_repositories FOR SELECT TO openfox_migrator USING(true);
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_bitbucket_work,openfox.integration_bitbucket_deliveries,openfox.integration_bitbucket_backlinks,openfox.integration_bitbucket_comment_sources TO openfox_app;
-- root: extend the existing fixed-search_path SECURITY DEFINER organization
-- scanner with due work/backlinks, preserving every other provider predicate.

-- Preserve 140/141 provider branches and add bounded Bitbucket due work.
SET ROLE openfox_migrator;
CREATE OR REPLACE FUNCTION openfox_private.list_integration_sync_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid integration sync scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT q.organization_id FROM openfox.integration_sync_outbox q
         WHERE (q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT l.organization_id FROM openfox.integration_sync_links l
         WHERE l.deleted_at IS NULL AND l.status = 'ACTIVE'
           AND l.settings #>> '{backfill,status}' = 'running'
        UNION
        SELECT w.organization_id FROM openfox.integration_cnb_pr_work w
          JOIN openfox.integration_connections c
            ON c.organization_id = w.organization_id AND c.id = w.connection_id
         WHERE c.provider = 'cnb' AND c.scope = 'ORGANIZATION' AND c.status = 'ACTIVE'
           AND w.next_check_at <= clock_timestamp()
           AND (w.lease_expires_at IS NULL OR w.lease_expires_at <= clock_timestamp())
        UNION
        SELECT w.organization_id FROM openfox.integration_codeup_mr_work w
          JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
         WHERE c.provider='codeup' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
           AND w.next_check_at<=clock_timestamp()
           AND (w.lease_expires_at IS NULL OR w.lease_expires_at<=clock_timestamp())
        UNION
        SELECT w.organization_id FROM openfox.integration_gitcode_work w
          JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
         WHERE c.provider='gitcode' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
           AND w.state IN ('PENDING','SENDING','UNKNOWN') AND w.next_attempt_at<=clock_timestamp()
           AND (w.lease_expires_at IS NULL OR w.lease_expires_at<=clock_timestamp())
        UNION
        SELECT r.organization_id FROM openfox.integration_gongfeng_repositories r
          JOIN openfox.integration_connections c ON c.organization_id=r.organization_id AND c.id=r.connection_id
         WHERE c.provider='gongfeng' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
           AND (r.hook_next_attempt_at<=clock_timestamp() OR (r.status='ACTIVE' AND r.next_reconcile_at<=clock_timestamp()))
           AND (r.lease_expires_at IS NULL OR r.lease_expires_at<=clock_timestamp())
        UNION
        SELECT w.organization_id FROM openfox.integration_gongfeng_work w
          JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
          JOIN openfox.integration_gongfeng_repositories r ON r.organization_id=w.organization_id AND r.id=w.repository_id
         WHERE c.provider='gongfeng' AND c.scope='ORGANIZATION' AND c.status='ACTIVE' AND r.status='ACTIVE'
           AND w.status IN ('PENDING','RUNNING') AND w.next_attempt_at<=clock_timestamp()
           AND (w.lease_expires_at IS NULL OR w.lease_expires_at<=clock_timestamp())
        UNION
        SELECT w.organization_id FROM openfox.integration_bitbucket_work w
          JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
          JOIN openfox.integration_bitbucket_repositories r ON r.organization_id=w.organization_id AND r.id=w.selection_id
         WHERE c.provider='bitbucket' AND c.scope='ORGANIZATION' AND c.status='ACTIVE' AND r.status='ACTIVE'
           AND w.state IN ('PENDING','RUNNING') AND w.next_attempt_at<=clock_timestamp()
           AND (w.lease_until IS NULL OR w.lease_until<=clock_timestamp())
        UNION
        SELECT b.organization_id FROM openfox.integration_bitbucket_backlinks b
          JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.external_id=b.workspace_id::text
         WHERE c.provider='bitbucket' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
           AND b.state IN ('PENDING','SENDING','UNKNOWN') AND b.next_attempt_at<=clock_timestamp()
           AND (b.lease_until IS NULL OR b.lease_until<=clock_timestamp())
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) FROM PUBLIC;
RESET ROLE;
GRANT EXECUTE ON FUNCTION openfox_private.list_integration_sync_organizations(uuid,integer) TO openfox_context;
