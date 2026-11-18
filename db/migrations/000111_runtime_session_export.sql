-- Native, encrypted Runtime session exports. Session payload bytes remain outside
-- PostgreSQL; only a one-hour ciphertext object and its wrapped key are stored.
-- Codex rollout snapshots are kept on the exporting device for at most 24 hours.

SET ROLE openfox_migrator;

ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_kind_valid,
    ADD CONSTRAINT device_commands_kind_valid CHECK (kind IN (
        'runtime_probe', 'runtime_test', 'storage_cleanup', 'skill_scan', 'skill_import',
        'device_settings', 'workspace_path', 'runtime_session_export'
    ));

CREATE TABLE openfox.runtime_session_exports (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL,
    run_id uuid NOT NULL,
    device_id uuid NOT NULL,
    requested_by_user_id uuid NOT NULL,
    command_id uuid NOT NULL,
    runtime text NOT NULL,
    session_id text NOT NULL,
    status text NOT NULL DEFAULT 'PENDING',
    storage_key text,
    size_bytes bigint NOT NULL DEFAULT 0,
    sha256 text NOT NULL DEFAULT '',
    wrapped_key bytea,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    FOREIGN KEY (device_id) REFERENCES openfox.physical_devices (id),
    FOREIGN KEY (organization_id, requested_by_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, command_id)
        REFERENCES openfox.device_commands (organization_id, id),
    CONSTRAINT runtime_session_exports_runtime_valid CHECK (runtime IN ('codex','claude-code','opencode','pi')),
    CONSTRAINT runtime_session_exports_session_id_valid CHECK (
        char_length(session_id) BETWEEN 1 AND 256
        AND session_id = btrim(session_id)
        AND session_id !~ '[[:cntrl:]]'
        AND position('/' IN session_id) = 0
        AND position(E'\\' IN session_id) = 0
        AND position('..' IN session_id) = 0
    ),
    CONSTRAINT runtime_session_exports_status_valid CHECK (status IN ('PENDING','READY','FAILED')),
    CONSTRAINT runtime_session_exports_size_valid CHECK (size_bytes BETWEEN 0 AND 20971520),
    CONSTRAINT runtime_session_exports_sha_valid CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT runtime_session_exports_error_valid CHECK (error_code ~ '^[A-Z0-9_]{0,64}$'),
    CONSTRAINT runtime_session_exports_expiry_valid CHECK (
        expires_at > created_at AND expires_at <= created_at + interval '1 hour'
    ),
    CONSTRAINT runtime_session_exports_state_valid CHECK (
        (status = 'PENDING' AND storage_key IS NULL AND size_bytes = 0 AND sha256 = '' AND wrapped_key IS NULL AND error_code = '')
        OR (status = 'READY' AND storage_key IS NOT NULL
            AND storage_key = 'runtime-session-exports/' || organization_id::text || '/' || id::text
            AND size_bytes BETWEEN 1 AND 20971520 AND sha256 ~ '^[0-9a-f]{64}$'
            AND wrapped_key IS NOT NULL AND octet_length(wrapped_key) BETWEEN 256 AND 512 AND error_code = '')
        OR (status = 'FAILED' AND storage_key IS NULL AND size_bytes = 0 AND sha256 = ''
            AND wrapped_key IS NULL AND error_code <> '')
    )
);
CREATE UNIQUE INDEX runtime_session_exports_command_unique
    ON openfox.runtime_session_exports (organization_id, command_id);
CREATE INDEX runtime_session_exports_expiry_idx
    ON openfox.runtime_session_exports (expires_at, organization_id, id);

ALTER TABLE openfox.runtime_session_exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.runtime_session_exports FORCE ROW LEVEL SECURITY;
CREATE POLICY runtime_session_exports_tenant_isolation ON openfox.runtime_session_exports
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY runtime_session_exports_expiry_select ON openfox.runtime_session_exports
    FOR SELECT TO openfox_migrator USING (expires_at <= clock_timestamp());
CREATE POLICY runtime_session_exports_expiry_delete ON openfox.runtime_session_exports
    FOR DELETE TO openfox_migrator USING (expires_at <= clock_timestamp());

CREATE OR REPLACE FUNCTION openfox_private.list_expired_runtime_session_exports(p_after_org uuid, p_after_id uuid, p_limit integer)
RETURNS TABLE (organization_id uuid, id uuid, storage_key text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid Runtime session export cleanup limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT e.organization_id, e.id, COALESCE(e.storage_key, '')
      FROM openfox.runtime_session_exports e
     WHERE e.expires_at <= clock_timestamp()
       AND (p_after_org IS NULL OR (e.organization_id, e.id) > (p_after_org, p_after_id))
     ORDER BY e.organization_id, e.id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_expired_runtime_session_exports(uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_expired_runtime_session_exports(uuid, uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.runtime_session_exports TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_expired_runtime_session_exports(uuid, uuid, integer) TO openfox_context;
