-- I09: stable GitCode repository identity and durable hook/Issue/comment work.
-- No third-party credentials or raw webhook bodies are stored in these tables.
-- Rollback is operational: stop/disconnect GitCode, preserve copies/mappings/work.
-- 131 deliberately remains absent; run this migration only in a new full-chain DB.
SET ROLE openfox_migrator;

CREATE UNIQUE INDEX integration_sync_links_connection_identity
    ON openfox.integration_sync_links (organization_id, connection_id, id);
CREATE TABLE openfox.integration_gitcode_repositories (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    sync_link_id uuid NOT NULL,
    remote_repository_id text NOT NULL CHECK (remote_repository_id ~ '^[1-9][0-9]{0,31}$'),
    repository_path text NOT NULL CHECK (char_length(repository_path) BETWEEN 3 AND 255),
    visibility text NOT NULL CHECK (visibility IN ('public','private')),
    active boolean NOT NULL DEFAULT true,
    hook_generation bigint NOT NULL DEFAULT 1 CHECK (hook_generation>0),
    hook_id text NOT NULL DEFAULT '' CHECK (hook_id='' OR hook_id ~ '^[1-9][0-9]{0,31}$'),
    hook_callback_hash bytea CHECK (hook_callback_hash IS NULL OR octet_length(hook_callback_hash)=32),
    hook_state text NOT NULL DEFAULT 'PENDING' CHECK (hook_state IN ('PENDING','ACTIVE','UNKNOWN','FAILED','DELETE_PENDING','DELETED')),
    last_error_code text NOT NULL DEFAULT '' CHECK (char_length(last_error_code)<=64),
    issue_write_status text NOT NULL DEFAULT 'UNVERIFIED' CHECK (issue_write_status IN ('UNVERIFIED','VERIFIED','DENIED')),
    comment_write_status text NOT NULL DEFAULT 'UNVERIFIED' CHECK (comment_write_status IN ('UNVERIFIED','VERIFIED','DENIED')),
    watermark timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,connection_id,id),
    UNIQUE (organization_id,sync_link_id),
    FOREIGN KEY (organization_id,connection_id) REFERENCES openfox.integration_connections (organization_id,id),
    FOREIGN KEY (organization_id,connection_id,sync_link_id) REFERENCES openfox.integration_sync_links (organization_id,connection_id,id)
);
CREATE UNIQUE INDEX integration_gitcode_repository_active_unique
    ON openfox.integration_gitcode_repositories (organization_id,remote_repository_id) WHERE active;
CREATE INDEX integration_gitcode_repository_connection ON openfox.integration_gitcode_repositories (organization_id,connection_id,id);

CREATE TABLE openfox.integration_gitcode_work (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('HOOK_ENSURE','HOOK_DELETE','ISSUE_CREATE','ISSUE_UPDATE','ISSUE_COMMENT','BACKLINK','BACKFILL','POLL','PULL')),
    object_key text NOT NULL CHECK (char_length(object_key) BETWEEN 1 AND 255),
    state text NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','SENDING','UNKNOWN','DONE','FAILED','CANCELLED')),
    generation bigint NOT NULL CHECK (generation>0),
    credential_generation bigint NOT NULL DEFAULT 0 CHECK (credential_generation>=0),
    sender_subject text NOT NULL DEFAULT '' CHECK (char_length(sender_subject)<=255),
    sender_login text NOT NULL DEFAULT '' CHECK (char_length(sender_login)<=255),
    marker text NOT NULL DEFAULT '' CHECK (char_length(marker)<=255),
    payload jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(payload)='object' AND pg_column_size(payload)<=1048576),
    remote_id text NOT NULL DEFAULT '' CHECK (char_length(remote_id)<=255),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
    next_attempt_at timestamptz DEFAULT clock_timestamp(),
    lease_token uuid,
    lease_expires_at timestamptz,
    last_error_code text NOT NULL DEFAULT '' CHECK (char_length(last_error_code)<=64),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,repository_id,kind,object_key),
    FOREIGN KEY (organization_id,connection_id,repository_id) REFERENCES openfox.integration_gitcode_repositories (organization_id,connection_id,id),
    CHECK ((lease_token IS NULL)=(lease_expires_at IS NULL)),
    CHECK (state NOT IN ('SENDING','UNKNOWN') OR (sender_subject<>'' AND credential_generation>0))
);
CREATE INDEX integration_gitcode_work_due ON openfox.integration_gitcode_work (next_attempt_at,organization_id,id)
    WHERE state IN ('PENDING','SENDING','UNKNOWN') AND next_attempt_at IS NOT NULL;
CREATE INDEX integration_gitcode_work_connection ON openfox.integration_gitcode_work (organization_id,connection_id,created_at DESC,id DESC);

ALTER TABLE openfox.integration_gitcode_repositories ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_gitcode_repositories FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_gitcode_work ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_gitcode_work FORCE ROW LEVEL SECURITY;
CREATE POLICY integration_gitcode_repositories_tenant ON openfox.integration_gitcode_repositories
    USING (organization_id=openfox_private.current_organization_id()) WITH CHECK (organization_id=openfox_private.current_organization_id());
CREATE POLICY integration_gitcode_work_tenant ON openfox.integration_gitcode_work
    USING (organization_id=openfox_private.current_organization_id()) WITH CHECK (organization_id=openfox_private.current_organization_id());
CREATE POLICY integration_gitcode_work_scan ON openfox.integration_gitcode_work FOR SELECT TO openfox_migrator USING (true);

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
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) FROM PUBLIC;
RESET ROLE;
GRANT SELECT, INSERT, UPDATE ON openfox.integration_gitcode_repositories,openfox.integration_gitcode_work TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_integration_sync_organizations(uuid,integer) TO openfox_context;
