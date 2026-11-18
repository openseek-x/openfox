-- WP-14 typed runtime configuration, Run snapshots, and safe artifact cleanup.
-- This migration is expand-only. It does not delete existing Run, event, result,
-- artifact, or audit data.

SET ROLE openfox_migrator;

CREATE TABLE openfox.organization_runtime_configs (
    organization_id uuid PRIMARY KEY
        REFERENCES openfox.organizations (id),
    config_version bigint NOT NULL DEFAULT 1,
    applied_version bigint NOT NULL DEFAULT 0,
    restart_required boolean NOT NULL DEFAULT false,
    queue_max integer NOT NULL DEFAULT 1000,
    active_max integer NOT NULL DEFAULT 10,
    queue_timeout_hours integer NOT NULL DEFAULT 24,
    spool_max_bytes bigint NOT NULL DEFAULT 2147483648,
    disk_min_free_bytes bigint NOT NULL DEFAULT 2147483648,
    run_max_bytes bigint NOT NULL DEFAULT 20971520,
    detailed_retention_days integer NOT NULL DEFAULT 14,
    artifact_file_max_bytes bigint NOT NULL DEFAULT 20971520,
    artifact_run_max_bytes bigint NOT NULL DEFAULT 104857600,
    artifact_retention_days integer NOT NULL DEFAULT 90,
    audit_retention_days integer NOT NULL DEFAULT 180,
    idempotency_retention_days integer NOT NULL DEFAULT 30,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    applied_at timestamptz,
    CONSTRAINT organization_runtime_configs_version_valid CHECK (
        config_version > 0 AND applied_version >= 0 AND applied_version <= config_version
    ),
    CONSTRAINT organization_runtime_configs_queue_max_valid CHECK (queue_max BETWEEN 1 AND 10000),
    CONSTRAINT organization_runtime_configs_active_max_valid CHECK (active_max BETWEEN 1 AND 100),
    CONSTRAINT organization_runtime_configs_queue_timeout_valid CHECK (queue_timeout_hours BETWEEN 1 AND 168),
    CONSTRAINT organization_runtime_configs_spool_max_valid CHECK (spool_max_bytes >= 268435456),
    CONSTRAINT organization_runtime_configs_disk_min_free_valid CHECK (disk_min_free_bytes >= 536870912),
    CONSTRAINT organization_runtime_configs_run_max_valid CHECK (run_max_bytes BETWEEN 1048576 AND 104857600),
    CONSTRAINT organization_runtime_configs_detailed_retention_valid CHECK (detailed_retention_days BETWEEN 1 AND 90),
    CONSTRAINT organization_runtime_configs_artifact_file_max_valid CHECK (artifact_file_max_bytes BETWEEN 1048576 AND 104857600),
    CONSTRAINT organization_runtime_configs_artifact_run_max_valid CHECK (artifact_run_max_bytes >= artifact_file_max_bytes),
    CONSTRAINT organization_runtime_configs_artifact_retention_valid CHECK (artifact_retention_days BETWEEN 1 AND 730),
    CONSTRAINT organization_runtime_configs_audit_retention_valid CHECK (audit_retention_days BETWEEN 30 AND 730),
    CONSTRAINT organization_runtime_configs_idempotency_retention_valid CHECK (idempotency_retention_days >= 30)
);

INSERT INTO openfox.organization_runtime_configs (organization_id)
SELECT id FROM openfox.organizations
ON CONFLICT (organization_id) DO NOTHING;

CREATE TABLE openfox.run_resource_snapshots (
    organization_id uuid NOT NULL,
    run_id uuid NOT NULL,
    config_version bigint NOT NULL,
    queue_max integer NOT NULL,
    active_max integer NOT NULL,
    queue_timeout_hours integer NOT NULL,
    spool_max_bytes bigint NOT NULL,
    disk_min_free_bytes bigint NOT NULL,
    run_max_bytes bigint NOT NULL,
    detailed_retention_days integer NOT NULL,
    artifact_file_max_bytes bigint NOT NULL,
    artifact_run_max_bytes bigint NOT NULL,
    artifact_retention_days integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, run_id),
    FOREIGN KEY (organization_id, run_id)
        REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_resource_snapshots_version_valid CHECK (config_version > 0),
    CONSTRAINT run_resource_snapshots_queue_max_valid CHECK (queue_max BETWEEN 1 AND 10000),
    CONSTRAINT run_resource_snapshots_active_max_valid CHECK (active_max BETWEEN 1 AND 100),
    CONSTRAINT run_resource_snapshots_queue_timeout_valid CHECK (queue_timeout_hours BETWEEN 1 AND 168),
    CONSTRAINT run_resource_snapshots_spool_max_valid CHECK (spool_max_bytes >= 268435456),
    CONSTRAINT run_resource_snapshots_disk_min_free_valid CHECK (disk_min_free_bytes >= 536870912),
    CONSTRAINT run_resource_snapshots_run_max_valid CHECK (run_max_bytes BETWEEN 1048576 AND 104857600),
    CONSTRAINT run_resource_snapshots_detailed_retention_valid CHECK (detailed_retention_days BETWEEN 1 AND 90),
    CONSTRAINT run_resource_snapshots_artifact_file_max_valid CHECK (artifact_file_max_bytes BETWEEN 1048576 AND 104857600),
    CONSTRAINT run_resource_snapshots_artifact_run_max_valid CHECK (artifact_run_max_bytes >= artifact_file_max_bytes),
    CONSTRAINT run_resource_snapshots_artifact_retention_valid CHECK (artifact_retention_days BETWEEN 1 AND 730)
);

ALTER TABLE openfox.artifacts
    ADD COLUMN pinned boolean NOT NULL DEFAULT false,
    ADD COLUMN version bigint NOT NULL DEFAULT 1;

ALTER TABLE openfox.artifacts
    ADD CONSTRAINT artifacts_version_positive CHECK (version > 0);

ALTER TABLE openfox.artifacts
    DROP CONSTRAINT artifacts_state_valid,
    ADD CONSTRAINT artifacts_state_valid CHECK (state IN ('AVAILABLE', 'DELETING', 'DELETED', 'MISSING'));

CREATE TABLE openfox.artifact_cleanup_previews (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    requested_by uuid NOT NULL,
    config_version bigint NOT NULL,
    status text NOT NULL DEFAULT 'PREVIEW',
    expires_at timestamptz NOT NULL,
    candidate_count integer NOT NULL DEFAULT 0,
    candidate_bytes bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    confirmed_at timestamptz,
    confirmed_by uuid,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, requested_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, confirmed_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT artifact_cleanup_previews_config_version_valid CHECK (config_version > 0),
    CONSTRAINT artifact_cleanup_previews_status_valid CHECK (status IN ('PREVIEW', 'CONFIRMED', 'COMPLETE', 'EXPIRED')),
    CONSTRAINT artifact_cleanup_previews_count_valid CHECK (candidate_count >= 0),
    CONSTRAINT artifact_cleanup_previews_bytes_valid CHECK (candidate_bytes >= 0),
    CONSTRAINT artifact_cleanup_previews_version_valid CHECK (version > 0),
    CONSTRAINT artifact_cleanup_previews_confirm_shape CHECK (
        (status = 'PREVIEW' AND confirmed_at IS NULL AND confirmed_by IS NULL)
        OR (status <> 'PREVIEW' AND confirmed_at IS NOT NULL AND confirmed_by IS NOT NULL)
    )
);

CREATE TABLE openfox.artifact_cleanup_preview_items (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    preview_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    storage_key text NOT NULL,
    size_bytes bigint NOT NULL,
    expected_version bigint NOT NULL,
    expected_sha256 text NOT NULL,
    expected_retention_until timestamptz,
    status text NOT NULL DEFAULT 'PENDING',
    failure_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    acted_at timestamptz,
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, preview_id, artifact_id),
    FOREIGN KEY (organization_id, preview_id)
        REFERENCES openfox.artifact_cleanup_previews (organization_id, id),
    FOREIGN KEY (organization_id, artifact_id)
        REFERENCES openfox.artifacts (organization_id, id),
    CONSTRAINT artifact_cleanup_preview_items_size_valid CHECK (size_bytes >= 0),
    CONSTRAINT artifact_cleanup_preview_items_version_valid CHECK (expected_version > 0),
    CONSTRAINT artifact_cleanup_preview_items_hash_valid CHECK (expected_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT artifact_cleanup_preview_items_status_valid CHECK (status IN ('PENDING', 'READY', 'SKIPPED', 'DELETED', 'FAILED')),
    CONSTRAINT artifact_cleanup_preview_items_failure_valid CHECK (
        failure_code IN ('', 'STALE', 'STORAGE_DELETE_FAILED')
    )
);

CREATE INDEX artifact_cleanup_previews_expiry_idx
    ON openfox.artifact_cleanup_previews (organization_id, status, expires_at);
CREATE INDEX artifact_cleanup_preview_items_preview_idx
    ON openfox.artifact_cleanup_preview_items (organization_id, preview_id, status);
CREATE INDEX artifacts_cleanup_candidate_idx
    ON openfox.artifacts (organization_id, state, pinned, retention_until, created_at)
    WHERE state = 'AVAILABLE' AND pinned = false;

-- Capacity reservations already serialize on the organization capacity row.
-- This trigger applies the newer typed active limit at that same boundary;
-- rollback of a rejected insert restores the counter updates in the caller.
CREATE OR REPLACE FUNCTION openfox_private.enforce_runtime_active_limit()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    active_limit integer;
    effective_reservations integer;
BEGIN
    IF NEW.status NOT IN ('RESERVED', 'CLAIMED') THEN
        RETURN NEW;
    END IF;
    SELECT LEAST(c.max_slots, COALESCE(rc.active_max, c.max_slots))
      INTO active_limit
      FROM openfox.organization_capacity c
      LEFT JOIN openfox.organization_runtime_configs rc
        ON rc.organization_id = c.organization_id
     WHERE c.organization_id = NEW.organization_id
     FOR UPDATE OF c;
    IF active_limit IS NULL THEN
        RAISE EXCEPTION 'organization capacity is not configured' USING ERRCODE = '23503';
    END IF;
    SELECT count(*)::integer
      INTO effective_reservations
      FROM openfox.capacity_reservations r
     WHERE r.organization_id = NEW.organization_id
       AND r.status IN ('RESERVED', 'CLAIMED');
    IF effective_reservations >= active_limit THEN
        RAISE EXCEPTION 'organization runtime active limit exhausted' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.enforce_runtime_active_limit() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enforce_runtime_active_limit() FROM PUBLIC;

CREATE TRIGGER capacity_reservations_runtime_active_limit
    BEFORE INSERT ON openfox.capacity_reservations
    FOR EACH ROW
    EXECUTE FUNCTION openfox_private.enforce_runtime_active_limit();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON
    openfox.organization_runtime_configs,
    openfox.run_resource_snapshots,
    openfox.artifact_cleanup_previews,
    openfox.artifact_cleanup_preview_items
TO openfox_app;
GRANT UPDATE ON openfox.artifacts TO openfox_app;

ALTER TABLE openfox.organization_runtime_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.organization_runtime_configs FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_resource_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_resource_snapshots FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.artifact_cleanup_previews ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.artifact_cleanup_previews FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.artifact_cleanup_preview_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.artifact_cleanup_preview_items FORCE ROW LEVEL SECURITY;

CREATE POLICY organization_runtime_configs_tenant_isolation ON openfox.organization_runtime_configs
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY run_resource_snapshots_tenant_isolation ON openfox.run_resource_snapshots
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY artifact_cleanup_previews_tenant_isolation ON openfox.artifact_cleanup_previews
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY artifact_cleanup_preview_items_tenant_isolation ON openfox.artifact_cleanup_preview_items
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
