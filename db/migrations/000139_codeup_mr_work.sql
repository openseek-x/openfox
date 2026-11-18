-- I08: event-driven central Codeup new-MR work and reliable backlinks; no backfill.
SET ROLE openfox_migrator;

CREATE TABLE openfox.integration_codeup_mr_work (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    cloud_organization_id text NOT NULL CHECK (cloud_organization_id ~ '^[a-zA-Z0-9_-]{1,100}$'),
    repository_id text NOT NULL CHECK (repository_id ~ '^[1-9][0-9]{0,31}$'),
    repository_path text NOT NULL,
    number integer NOT NULL CHECK (number > 0),
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    next_check_at timestamptz DEFAULT clock_timestamp(),
    terminal boolean NOT NULL DEFAULT false,
    lease_owner text NOT NULL DEFAULT '',
    lease_token uuid,
    lease_expires_at timestamptz,
    last_checked_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_error_code text NOT NULL DEFAULT '',
    backlink_state text NOT NULL DEFAULT 'NONE',
    backlink_comment_id text NOT NULL DEFAULT '',
    backlink_patchset_id text NOT NULL DEFAULT '' CHECK (char_length(backlink_patchset_id)<=128),
    backlink_body text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, connection_id)
        REFERENCES openfox.integration_connections (organization_id, id),
    UNIQUE (organization_id, cloud_organization_id, repository_id, number),
    CHECK (char_length(btrim(repository_id)) BETWEEN 1 AND 255),
    CHECK (char_length(btrim(repository_path)) BETWEEN 1 AND 255),
    CHECK (char_length(lease_owner) <= 128 AND char_length(last_error_code) <= 64),
    CHECK ((lease_owner = '' AND lease_token IS NULL AND lease_expires_at IS NULL)
        OR (lease_owner <> '' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CHECK (backlink_state IN ('NONE','PENDING','SENDING','SENT','UNKNOWN','SKIPPED')),
    CHECK (char_length(backlink_comment_id) <= 128 AND octet_length(backlink_body) <= 65535),
    CHECK (backlink_state <> 'SENT' OR backlink_comment_id <> ''),
    CHECK (backlink_state NOT IN ('SENDING','UNKNOWN') OR (backlink_body<>'' AND backlink_patchset_id<>''))
);
CREATE INDEX integration_codeup_mr_work_due_idx
    ON openfox.integration_codeup_mr_work (next_check_at, organization_id, id)
    WHERE next_check_at IS NOT NULL;
CREATE INDEX integration_codeup_mr_work_connection_idx
    ON openfox.integration_codeup_mr_work (organization_id, connection_id, created_at DESC, id DESC);
ALTER TABLE openfox.integration_codeup_mr_work ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_codeup_mr_work FORCE ROW LEVEL SECURITY;
CREATE POLICY integration_codeup_mr_work_tenant_isolation ON openfox.integration_codeup_mr_work
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY integration_codeup_mr_work_scan ON openfox.integration_codeup_mr_work
    FOR SELECT TO openfox_migrator USING (true);

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
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) FROM PUBLIC;
RESET ROLE;
GRANT SELECT, INSERT, UPDATE ON openfox.integration_codeup_mr_work TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) TO openfox_context;
