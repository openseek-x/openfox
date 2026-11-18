-- I10: fixed git.tencent.com partner API v3, organization PAT, stable repository
-- bindings and durable inbound/reconciliation work. No PAT/plain callback token
-- is stored in these tables. Run only after the existing migration chain.
-- Rollback: disable/disconnect Gongfeng first; retain these tables and imported
-- tasks/mappings. No destructive rollback SQL is required or authorized.
SET ROLE openfox_migrator;

CREATE TABLE openfox.integration_gongfeng_repositories (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    sync_link_id uuid NOT NULL,
    remote_project_id bigint NOT NULL CHECK (remote_project_id > 0),
    current_path text NOT NULL CHECK (char_length(current_path) BETWEEN 3 AND 255),
    private boolean,
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    status text NOT NULL DEFAULT 'PAUSED' CHECK (status IN ('ACTIVE','PAUSED','NEEDS_ATTENTION','REMOVED')),
    status_reason text NOT NULL DEFAULT '' CHECK (char_length(status_reason) <= 100),
    hook_id bigint CHECK (hook_id > 0),
    hook_state text NOT NULL DEFAULT 'PENDING' CHECK (hook_state IN ('PENDING','CREATE_PENDING','UPDATE_PENDING','ACTIVATE_PENDING','VERIFY_PENDING','SENDING','UNKNOWN','ACTIVE','DELETE_PENDING','DELETING','DELETE_UNKNOWN','DELETED','NEEDS_ATTENTION')),
    hook_url_hash bytea NOT NULL CHECK (octet_length(hook_url_hash)=32),
    hook_target_hash bytea CHECK (hook_target_hash IS NULL OR octet_length(hook_target_hash)=32),
    hook_generation bigint NOT NULL DEFAULT 0 CHECK (hook_generation >= 0),
    hook_credential_generation bigint NOT NULL DEFAULT 0 CHECK (hook_credential_generation >= 0),
    hook_scan jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(hook_scan)='object' AND pg_column_size(hook_scan)<=1024),
    hook_error text NOT NULL DEFAULT '' CHECK (char_length(hook_error) <= 100),
    hook_next_attempt_at timestamptz DEFAULT clock_timestamp(),
    lease_token uuid,
    lease_expires_at timestamptz,
    mr_enabled boolean NOT NULL DEFAULT true,
    commit_enabled boolean NOT NULL DEFAULT false,
    backlink_enabled boolean NOT NULL DEFAULT false,
    history_requested boolean NOT NULL DEFAULT false,
    history_cursor jsonb NOT NULL DEFAULT '{}',
    issue_watermark timestamptz,
    next_reconcile_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 8),
    excluded_confidential bigint NOT NULL DEFAULT 0 CHECK (excluded_confidential >= 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    removed_at timestamptz,
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,id,connection_id),
    UNIQUE (organization_id,sync_link_id),
    FOREIGN KEY (organization_id,connection_id) REFERENCES openfox.integration_connections (organization_id,id),
    FOREIGN KEY (organization_id,connection_id,sync_link_id) REFERENCES openfox.integration_sync_links (organization_id,connection_id,id),
    CHECK ((lease_token IS NULL) = (lease_expires_at IS NULL)),
    CHECK ((status='REMOVED') = (removed_at IS NOT NULL)),
    CHECK (jsonb_typeof(history_cursor)='object' AND pg_column_size(history_cursor)<=8192)
);
CREATE UNIQUE INDEX integration_gongfeng_repository_active_unique
    ON openfox.integration_gongfeng_repositories (organization_id,remote_project_id)
    WHERE removed_at IS NULL;
CREATE INDEX integration_gongfeng_repository_connection_idx
    ON openfox.integration_gongfeng_repositories (organization_id,connection_id,id);
CREATE INDEX integration_gongfeng_repository_hook_due_idx
    ON openfox.integration_gongfeng_repositories (hook_next_attempt_at,organization_id,id)
    WHERE hook_next_attempt_at IS NOT NULL;
CREATE INDEX integration_gongfeng_repository_reconcile_due_idx
    ON openfox.integration_gongfeng_repositories (next_reconcile_at,organization_id,id)
    WHERE status='ACTIVE' AND next_reconcile_at IS NOT NULL;

CREATE TABLE openfox.integration_gongfeng_work (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('RECEIPT','ISSUE_REFRESH','NOTE_REFRESH','NOTES_SCAN','MR_REFRESH','PUSH','BACKLINK','ISSUE_CREATE','ISSUE_UPDATE','ISSUE_COMMENT')),
    object_key text NOT NULL CHECK (char_length(object_key) BETWEEN 1 AND 180),
    parent_object_id bigint CHECK (parent_object_id > 0),
    scan_token uuid,
    scan_round smallint CHECK (scan_round IN (1,2)),
    payload jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','DONE','FAILED','CANCELLED')),
    requested_generation bigint NOT NULL DEFAULT 1 CHECK (requested_generation > 0),
    completed_generation bigint NOT NULL DEFAULT 0 CHECK (completed_generation >= 0 AND completed_generation <= requested_generation),
    next_attempt_at timestamptz DEFAULT clock_timestamp(),
    lease_token uuid,
    lease_expires_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 8),
    last_error_code text NOT NULL DEFAULT '' CHECK (char_length(last_error_code) <= 100),
    backlink_state text NOT NULL DEFAULT 'NONE' CHECK (backlink_state IN ('NONE','PENDING','SENDING','UNKNOWN','SENT','SKIPPED')),
    backlink_body text NOT NULL DEFAULT '' CHECK (octet_length(backlink_body) <= 300000),
    backlink_marker text NOT NULL DEFAULT '' CHECK (char_length(backlink_marker) <= 128),
    backlink_sender_id bigint CHECK (backlink_sender_id > 0),
    backlink_comment_id text NOT NULL DEFAULT '' CHECK (char_length(backlink_comment_id) <= 64),
    backlink_started_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,id),
    UNIQUE (organization_id,repository_id,kind,object_key),
    FOREIGN KEY (organization_id,connection_id) REFERENCES openfox.integration_connections (organization_id,id),
    FOREIGN KEY (organization_id,repository_id,connection_id)
        REFERENCES openfox.integration_gongfeng_repositories (organization_id,id,connection_id),
    CHECK (jsonb_typeof(payload)='object' AND pg_column_size(payload)<=16384),
    CHECK ((lease_token IS NULL) = (lease_expires_at IS NULL)),
    CHECK ((scan_token IS NULL) = (scan_round IS NULL)),
    CHECK (kind <> 'RECEIPT' OR (status='DONE' AND next_attempt_at IS NULL AND lease_token IS NULL)),
    CHECK (backlink_state NOT IN ('SENDING','UNKNOWN','SENT') OR
        (backlink_body<>'' AND backlink_marker<>'' AND backlink_sender_id IS NOT NULL AND backlink_started_at IS NOT NULL)),
    CHECK (backlink_state <> 'SENT' OR backlink_comment_id<>'')
);
CREATE INDEX integration_gongfeng_work_due_idx
    ON openfox.integration_gongfeng_work (next_attempt_at,organization_id,id)
    WHERE status IN ('PENDING','RUNNING') AND next_attempt_at IS NOT NULL;
CREATE INDEX integration_gongfeng_work_connection_idx
    ON openfox.integration_gongfeng_work (organization_id,connection_id,status,created_at,id);
CREATE INDEX integration_gongfeng_receipt_cleanup_idx
    ON openfox.integration_gongfeng_work (organization_id,created_at,id) WHERE kind='RECEIPT';

ALTER TABLE openfox.integration_gongfeng_repositories ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_gongfeng_repositories FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_gongfeng_work ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_gongfeng_work FORCE ROW LEVEL SECURITY;
CREATE POLICY gongfeng_repositories_tenant ON openfox.integration_gongfeng_repositories
    USING (organization_id=openfox_private.current_organization_id())
    WITH CHECK (organization_id=openfox_private.current_organization_id());
CREATE POLICY gongfeng_work_tenant ON openfox.integration_gongfeng_work
    USING (organization_id=openfox_private.current_organization_id())
    WITH CHECK (organization_id=openfox_private.current_organization_id());
CREATE POLICY gongfeng_repositories_scan ON openfox.integration_gongfeng_repositories
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY gongfeng_work_scan ON openfox.integration_gongfeng_work
    FOR SELECT TO openfox_migrator USING (true);

-- Reception digests are kept for seven days; business-object uniqueness and
-- UNKNOWN send state are retained independently. The application cannot delete
-- business work rows through this bounded receipt-only cleanup function.
CREATE FUNCTION openfox_private.prune_gongfeng_receipts(p_limit integer)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE removed integer;
BEGIN
    IF p_limit IS NULL OR p_limit<1 OR p_limit>200 THEN
        RAISE EXCEPTION 'invalid receipt cleanup limit' USING ERRCODE='22023';
    END IF;
    DELETE FROM openfox.integration_gongfeng_work w USING (
        SELECT id FROM openfox.integration_gongfeng_work
        WHERE organization_id=openfox_private.current_organization_id() AND kind='RECEIPT'
          AND created_at<clock_timestamp()-interval '7 days'
        ORDER BY created_at,id LIMIT p_limit FOR UPDATE SKIP LOCKED
    ) old WHERE w.organization_id=openfox_private.current_organization_id() AND w.id=old.id AND w.kind='RECEIPT';
    GET DIAGNOSTICS removed=ROW_COUNT;
    RETURN removed;
END $$;
ALTER FUNCTION openfox_private.prune_gongfeng_receipts(integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.prune_gongfeng_receipts(integer) FROM PUBLIC;

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
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) FROM PUBLIC;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_gongfeng_repositories TO openfox_app;
GRANT SELECT,INSERT,UPDATE ON openfox.integration_gongfeng_work TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.prune_gongfeng_receipts(integer) TO openfox_app;

GRANT EXECUTE ON FUNCTION openfox_private.list_integration_sync_organizations(uuid,integer) TO openfox_context;
