-- 阶段 7b「导出任务数据」：导出任务表 export_jobs。
--
-- 仅扩展（expand-only），新表，不改动任何已有表：
--   - export_jobs：一次导出一行。状态 QUEUED → RUNNING → SUCCEEDED / FAILED，文件保留到 expires_at（完成后 7 天）后由 worker
--     置为 EXPIRED 并删除文件。space_ids 是发起时选择的空间（最多 20 个）；导出内容在 worker 执行时按发起人「当时」的读权限过滤，
--     所以不在表里保存任何导出内容。file_key 是 artifacts 存储里的对象键（组织名下），下载接口按发起人 / 所有者 / 管理员鉴权。
--     lease_owner / lease_expires_at / attempts / retry_at 是后台执行租约与重试（与 import_jobs 同一做法）。
--   - list_export_organizations：后台 worker 找到有待执行导出或待清理过期文件的组织（控制面，只返回组织 ID）。
-- RLS 与其他租户表相同；openfox_app 可读写。
-- 锁：只建新表、新索引、新函数，没有对已有表加锁。
-- 回滚：应用回退后新表闲置即可（旧版本不读）。确需移除时：先删除存储里 export/ 目录下的文件，
--   再 DROP FUNCTION openfox_private.list_export_organizations(uuid, integer)，最后 DROP TABLE openfox.export_jobs。

SET ROLE openfox_migrator;

CREATE TABLE openfox.export_jobs (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    format text NOT NULL,
    space_ids uuid[] NOT NULL,
    status text NOT NULL DEFAULT 'QUEUED',
    space_count integer NOT NULL DEFAULT 0,
    task_count integer NOT NULL DEFAULT 0,
    file_key text NOT NULL DEFAULT '',
    file_name text NOT NULL DEFAULT '',
    content_type text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0,
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    operation_id text NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    expires_at timestamptz,
    retry_at timestamptz,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT export_jobs_format_valid CHECK (format IN ('csv', 'json')),
    CONSTRAINT export_jobs_status_valid CHECK (status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'EXPIRED')),
    CONSTRAINT export_jobs_spaces_valid CHECK (cardinality(space_ids) BETWEEN 1 AND 20),
    CONSTRAINT export_jobs_counts_valid CHECK (space_count >= 0 AND task_count >= 0 AND size_bytes >= 0),
    CONSTRAINT export_jobs_finished_valid CHECK ((status IN ('SUCCEEDED', 'FAILED', 'EXPIRED')) = (finished_at IS NOT NULL)),
    CONSTRAINT export_jobs_file_valid CHECK (status <> 'SUCCEEDED' OR (file_key <> '' AND file_name <> '' AND expires_at IS NOT NULL)),
    CONSTRAINT export_jobs_text_valid CHECK (
        char_length(file_key) <= 512 AND char_length(file_name) <= 255 AND char_length(content_type) <= 128
        AND char_length(error_code) <= 64 AND char_length(error_message) <= 1000
        AND operation_id ~ '^op_[0-9a-z]{8,32}$' AND char_length(lease_owner) <= 128),
    CONSTRAINT export_jobs_versions_positive CHECK (version > 0 AND attempts >= 0)
);
CREATE UNIQUE INDEX export_jobs_operation_unique ON openfox.export_jobs (organization_id, operation_id);
CREATE INDEX export_jobs_list_idx ON openfox.export_jobs (organization_id, created_at DESC, id DESC);
CREATE INDEX export_jobs_active_idx ON openfox.export_jobs (organization_id, lease_expires_at)
    WHERE status IN ('QUEUED', 'RUNNING');
CREATE INDEX export_jobs_expiry_idx ON openfox.export_jobs (expires_at) WHERE status = 'SUCCEEDED';

ALTER TABLE openfox.export_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.export_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY export_jobs_tenant_isolation ON openfox.export_jobs
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 控制面扫描（list_export_organizations 以 openfox_migrator 身份执行）只读。
CREATE POLICY export_jobs_scan ON openfox.export_jobs FOR SELECT TO openfox_migrator USING (true);

-- 后台 worker：有待执行导出（排队、租约过期、重试等待到期）或文件已过期待清理的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_export_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid export scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT j.organization_id FROM openfox.export_jobs j
         WHERE j.status IN ('QUEUED', 'RUNNING')
           AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= clock_timestamp())
           AND (j.retry_at IS NULL OR j.retry_at <= clock_timestamp())
        UNION
        SELECT j.organization_id FROM openfox.export_jobs j
         WHERE j.status = 'SUCCEEDED' AND j.expires_at <= clock_timestamp()
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_export_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_export_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.export_jobs TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_export_organizations(uuid, integer) TO openfox_context;
