-- 阶段 7a「克隆空间到另一个组织」：复用 import_jobs 的后台任务框架。
--
-- 仅扩展（expand-only）：
--   - import_jobs.source 增加 space_clone；任务挂在「目标组织」下（worker 只访问目标组织），
--     源组织 / 源空间 / 快照包引用记在新列 source_organization_id、source_space_id、bundle_key（可空，无外键：跨组织不建外键）。
--   - import_jobs.status 增加 CLEANING（清理中）、CLEANED（已清理）、CLEANUP_FAILED（清理失败）：
--     克隆失败或取消后自动清理已创建的数据（软删除本次创建的任务与空间、归档未被使用的组织级配置）。
--     CLEANED / CLEANUP_FAILED 是终态，要求 finished_at 非空；CLEANING 不是终态。
--   - import_jobs.phase 增加 creating_space / comments / cleaning（克隆各阶段）。
--   - import_jobs_space_required：space_clone 的任务在 worker 建好空间之前 space_id 可以为空。
--   - import_created_resources.kind 增加 CUSTOM_FIELD / PROJECT / RELATION_TYPE（克隆新建的组织级配置，清理时归档未被使用的）。
--   - list_import_organizations：把 CLEANING 计入待处理（其余逻辑与 000047 相同）。
-- 所有新约束先 NOT VALID 再 VALIDATE。
-- 锁：import_jobs / import_created_resources 的约束替换与加可空列（只改元数据）；导入相关表数据很少。
-- 兼容：旧版本应用不识别新状态；回退应用前，先让克隆任务进入终态（见回滚）。
-- 回滚：
--   1. 等所有 space_clone 任务结束，或执行
--      UPDATE openfox.import_jobs SET status = 'FAILED', finished_at = COALESCE(finished_at, clock_timestamp()), phase = ''
--       WHERE source = 'space_clone' AND status IN ('CLEANING', 'CLEANED', 'CLEANUP_FAILED');
--   2. 恢复 000047 的 source / phase / status / space_required 约束、000046 的 kind 约束与 000047 的 list_import_organizations；
--   3. DROP COLUMN source_organization_id, source_space_id, bundle_key。

SET ROLE openfox_migrator;

ALTER TABLE openfox.import_jobs ADD COLUMN source_organization_id uuid;
ALTER TABLE openfox.import_jobs ADD COLUMN source_space_id uuid;
ALTER TABLE openfox.import_jobs ADD COLUMN bundle_key text NOT NULL DEFAULT '';
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_clone_valid CHECK (
        char_length(bundle_key) <= 512
        AND ((source = 'space_clone') = (source_organization_id IS NOT NULL AND source_space_id IS NOT NULL))) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_clone_valid;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_source_valid;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_source_valid CHECK (source IN ('jira_csv', 'jira_cloud', 'jira_server', 'space_clone')) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_source_valid;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_status_valid;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_status_valid CHECK (status IN (
        'DRAFT', 'QUEUED', 'RUNNING', 'CANCEL_REQUESTED', 'SUCCEEDED', 'FAILED', 'CANCELLED', 'ROLLED_BACK', 'DISCARDED',
        'CLEANING', 'CLEANED', 'CLEANUP_FAILED')) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_status_valid;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_phase_valid;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_phase_valid CHECK (phase IN (
        '', 'validating', 'fetching', 'importing', 'linking', 'finishing', 'creating_space', 'comments', 'cleaning')) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_phase_valid;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_finished_valid;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_finished_valid CHECK (
        (status IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'ROLLED_BACK', 'CLEANED', 'CLEANUP_FAILED')) = (finished_at IS NOT NULL)) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_finished_valid;

ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_space_required;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_space_required CHECK (
        space_id IS NOT NULL OR status IN ('DRAFT', 'DISCARDED') OR source = 'space_clone') NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_space_required;

ALTER TABLE openfox.import_created_resources DROP CONSTRAINT import_created_resources_kind_valid;
ALTER TABLE openfox.import_created_resources
    ADD CONSTRAINT import_created_resources_kind_valid CHECK (kind IN (
        'STATUS', 'LABEL', 'WORKFLOW_STATUS', 'ITERATION', 'CUSTOM_FIELD', 'PROJECT', 'RELATION_TYPE')) NOT VALID;
ALTER TABLE openfox.import_created_resources VALIDATE CONSTRAINT import_created_resources_kind_valid;

-- 与 000047 相同，另加 CLEANING。
CREATE OR REPLACE FUNCTION openfox_private.list_import_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid import scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT j.organization_id FROM openfox.import_jobs j
         WHERE (j.status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED', 'CLEANING') OR (j.status = 'DRAFT' AND j.phase = 'fetching'))
           AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= clock_timestamp())
           AND (j.retry_at IS NULL OR j.retry_at <= clock_timestamp())
        UNION
        SELECT j.organization_id FROM openfox.import_jobs j
         WHERE j.status = 'DRAFT' AND j.created_at <= clock_timestamp() - interval '24 hours'
        UNION
        SELECT a.organization_id FROM openfox.external_accounts a
          JOIN openfox.invitations i ON i.id = a.invitation_id
         WHERE a.linked_user_id IS NULL AND i.status = 'ACCEPTED'
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;

RESET ROLE;
