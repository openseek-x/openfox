-- I13: explicit IAM, per-repository bridge, bounded discovery and durable work.
-- Apply only to a fresh isolated database during this task. No business backfill.
SET ROLE openfox_migrator;

ALTER TABLE openfox.integration_webhook_deliveries
 ADD COLUMN payload_sha256 text,
 ADD CONSTRAINT integration_webhook_payload_digest CHECK
 (payload_sha256 IS NULL OR payload_sha256 ~ '^[0-9a-f]{64}$');

CREATE TABLE openfox.integration_codecommit_repositories (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 account_id text NOT NULL CHECK(account_id ~ '^[0-9]{12}$'),
 region text NOT NULL CHECK(char_length(region) BETWEEN 1 AND 32),
 repository_id text NOT NULL CHECK(char_length(repository_id) BETWEEN 1 AND 256),
 name text NOT NULL CHECK(name ~ '^[A-Za-z0-9_.-]{1,100}$'),
 arn text NOT NULL CHECK(char_length(arn) BETWEEN 1 AND 512),
 enabled boolean NOT NULL DEFAULT true,
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 configured_by uuid NOT NULL,
 bridge_id uuid NOT NULL DEFAULT gen_random_uuid(),
 bridge_token_hash bytea CHECK(bridge_token_hash IS NULL OR octet_length(bridge_token_hash)=32),
 last_event_at timestamptz,
 activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 initialized_at timestamptz,
 scan_kind text NOT NULL DEFAULT 'PR_OPEN' CHECK(scan_kind IN ('PR_OPEN','PR_CLOSED','BRANCHES')),
 scan_generation bigint NOT NULL DEFAULT 1 CHECK(scan_generation>0),
 next_token text NOT NULL DEFAULT '' CHECK(octet_length(next_token)<=8192),
 scan_pending jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(scan_pending)='array' AND pg_column_size(scan_pending)<=524288),
 scan_seen_tokens jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(scan_seen_tokens)='array' AND pg_column_size(scan_seen_tokens)<=1048576),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 lease_token uuid,
 lease_until timestamptz,
 failure_count integer NOT NULL DEFAULT 0 CHECK(failure_count>=0),
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=100),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,account_id,region,repository_id),
 UNIQUE(organization_id,connection_id,id),
 UNIQUE(bridge_id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,configured_by) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK(arn='arn:aws:codecommit:'||region||':'||account_id||':'||name),
 CHECK((lease_token IS NULL)=(lease_until IS NULL)),
 CHECK(enabled OR bridge_token_hash IS NULL)
);
CREATE INDEX integration_codecommit_repository_due ON openfox.integration_codecommit_repositories(next_attempt_at,organization_id,id) WHERE enabled AND next_attempt_at IS NOT NULL;
CREATE INDEX integration_codecommit_repository_connection ON openfox.integration_codecommit_repositories(organization_id,connection_id,id);

CREATE TABLE openfox.integration_codecommit_references (
 organization_id uuid NOT NULL,
 repository_row_id uuid NOT NULL,
 reference_name text NOT NULL CHECK(char_length(reference_name) BETWEEN 1 AND 256),
 observed_sha text NOT NULL DEFAULT '' CHECK(observed_sha='' OR observed_sha ~ '^[0-9a-f]{40}$'),
 applied_sha text NOT NULL DEFAULT '' CHECK(applied_sha='' OR applied_sha ~ '^[0-9a-f]{40}$'),
 generation bigint NOT NULL CHECK(generation>0),
 scan_generation bigint NOT NULL CHECK(scan_generation>0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,repository_row_id,reference_name),
 FOREIGN KEY(organization_id,repository_row_id) REFERENCES openfox.integration_codecommit_repositories(organization_id,id)
);

CREATE TABLE openfox.integration_codecommit_work (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 connection_id uuid NOT NULL,
 repository_row_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 connection_version bigint NOT NULL CHECK(connection_version>0),
 kind text NOT NULL CHECK(kind IN ('PR','PUSH')),
 work_key text NOT NULL CHECK(char_length(work_key) BETWEEN 1 AND 128),
 request_fingerprint text NOT NULL CHECK(request_fingerprint ~ '^[0-9a-f]{64}$'),
 requested_version bigint NOT NULL DEFAULT 1 CHECK(requested_version>0),
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','DONE','FAILED','IGNORED')),
 next_attempt_at timestamptz DEFAULT clock_timestamp(),
 lease_token uuid,
 lease_until timestamptz,
 failure_count integer NOT NULL DEFAULT 0 CHECK(failure_count>=0),
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=100),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 pull_request_id integer CHECK(pull_request_id>0),
 source_updated_at timestamptz,
 source_revision text NOT NULL DEFAULT '' CHECK(char_length(source_revision)<=1024),
 source_hash text NOT NULL DEFAULT '' CHECK(source_hash='' OR source_hash ~ '^[0-9a-f]{64}$'),
 baseline boolean NOT NULL DEFAULT false,
 reference_name text CHECK(char_length(reference_name) BETWEEN 1 AND 256),
 old_sha text CHECK(old_sha='' OR old_sha ~ '^[0-9a-f]{40}$'),
 new_sha text CHECK(new_sha='' OR new_sha ~ '^[0-9a-f]{40}$'),
 linked_count integer NOT NULL DEFAULT 0 CHECK(linked_count>=0),
 backlink_generation bigint NOT NULL DEFAULT 1 CHECK(backlink_generation>0),
 backlink_state text NOT NULL DEFAULT 'NONE' CHECK(backlink_state IN ('NONE','PENDING','SENDING','SENT','UNKNOWN','SKIPPED','FAILED')),
 backlink_intent jsonb CHECK(backlink_intent IS NULL OR (jsonb_typeof(backlink_intent)='object' AND pg_column_size(backlink_intent)<=65536)),
 backlink_link_ids uuid[] NOT NULL DEFAULT '{}' CHECK(cardinality(backlink_link_ids)<=100),
 backlink_comment_id text NOT NULL DEFAULT '' CHECK(char_length(backlink_comment_id)<=1024),
 backlink_sender text NOT NULL DEFAULT '' CHECK(char_length(backlink_sender)<=2048),
 reconcile_requested boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 UNIQUE(organization_id,repository_row_id,kind,work_key),
 FOREIGN KEY(organization_id,connection_id,repository_row_id) REFERENCES openfox.integration_codecommit_repositories(organization_id,connection_id,id),
 CHECK((lease_token IS NULL)=(lease_until IS NULL)),
 CHECK((kind='PR' AND pull_request_id IS NOT NULL AND reference_name IS NULL AND old_sha IS NULL AND new_sha IS NULL)
    OR (kind='PUSH' AND pull_request_id IS NULL AND reference_name IS NOT NULL AND old_sha IS NOT NULL AND new_sha IS NOT NULL AND backlink_state='NONE' AND backlink_intent IS NULL)),
 CHECK(backlink_state NOT IN ('PENDING','SENDING','SENT','UNKNOWN') OR (backlink_intent IS NOT NULL AND cardinality(backlink_link_ids)>0)),
 CHECK(backlink_state<>'SENT' OR backlink_comment_id<>'')
);
CREATE INDEX integration_codecommit_work_due ON openfox.integration_codecommit_work(next_attempt_at,organization_id,id) WHERE next_attempt_at IS NOT NULL;
CREATE INDEX integration_codecommit_work_repository ON openfox.integration_codecommit_work(organization_id,repository_row_id,requested_at,id);

DO $rls$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['integration_codecommit_repositories','integration_codecommit_references','integration_codecommit_work'] LOOP
  EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',tab);
  EXECUTE format('CREATE POLICY codecommit_tenant ON openfox.%I USING(organization_id=openfox_private.current_organization_id()) WITH CHECK(organization_id=openfox_private.current_organization_id())',tab);
 END LOOP;
END
$rls$;
CREATE POLICY codecommit_repository_scan ON openfox.integration_codecommit_repositories FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY codecommit_work_scan ON openfox.integration_codecommit_work FOR SELECT TO openfox_migrator USING(true);
CREATE FUNCTION openfox_private.resolve_codecommit_bridge(p_bridge uuid)
RETURNS TABLE(organization_id uuid,connection_id uuid,repository_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT r.organization_id,r.connection_id,r.id FROM openfox.integration_codecommit_repositories r
 JOIN openfox.integration_connections c ON c.organization_id=r.organization_id AND c.id=r.connection_id
 JOIN openfox.organization_directory d ON d.organization_id=r.organization_id AND d.deleted_at IS NULL
 WHERE r.bridge_id=p_bridge AND r.enabled AND r.bridge_token_hash IS NOT NULL
   AND c.provider='codecommit' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
$$;
ALTER FUNCTION openfox_private.resolve_codecommit_bridge(uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_codecommit_bridge(uuid) FROM PUBLIC;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_codecommit_repositories,openfox.integration_codecommit_references,openfox.integration_codecommit_work TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_codecommit_bridge(uuid) TO openfox_context;

-- Preserve every provider in the current 142 scanner. I07/143 adds no scanner source.
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
        UNION
        SELECT r.organization_id FROM openfox.integration_codecommit_repositories r
          JOIN openfox.integration_connections c ON c.organization_id=r.organization_id AND c.id=r.connection_id
         WHERE c.provider='codecommit' AND c.scope='ORGANIZATION' AND c.status='ACTIVE' AND r.enabled
           AND r.next_attempt_at<=clock_timestamp() AND (r.lease_until IS NULL OR r.lease_until<=clock_timestamp())
        UNION
        SELECT w.organization_id FROM openfox.integration_codecommit_work w
          JOIN openfox.integration_connections c ON c.organization_id=w.organization_id AND c.id=w.connection_id
          JOIN openfox.integration_codecommit_repositories r ON r.organization_id=w.organization_id AND r.id=w.repository_row_id
         WHERE c.provider='codecommit' AND c.scope='ORGANIZATION' AND c.status='ACTIVE' AND r.enabled
           AND w.next_attempt_at<=clock_timestamp() AND (w.lease_until IS NULL OR w.lease_until<=clock_timestamp())
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
