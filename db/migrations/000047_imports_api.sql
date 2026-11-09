-- 阶段 6c-2「导入：Jira API、新建空间与迭代、外部账号页签」。
--
-- 仅扩展（expand-only）：
--   - import_jobs.space_id 改为可空：选择「新建空间」的草稿在开始导入时才创建空间（丢弃草稿不留下空空间）；
--     约束保证只有 DRAFT / DISCARDED 可以没有空间。space_created 标记空间由本次导入创建（只有这种空间可保留票号）。
--   - list_import_organizations 增加「拉取中的 API 草稿」（status = DRAFT 且 phase = fetching，租约为空 / 过期，retry_at 已到）。
--   - import_jobs.source 增加 jira_server（Jira Data Center / Server）。
--   - import_created_resources 的 kind 增加 ITERATION（回滚时删除本次新建且已无任务使用的迭代）。
--   - external_accounts 增加 source_label（来源实例，例如 acme.atlassian.net / Jira CSV）与 link_method
--     （email = 导入时按邮箱匹配、invite = 接受邀请后关联、manual = 成员页手动关联；未关联为空串）。
-- 锁：import_jobs / import_created_resources / external_accounts 的约束替换与加列（带常量默认值，PostgreSQL 11+ 只改元数据）；
--   已关联的外部账号回填 link_method（6c-1 只在导入中产生，行数很少）；新约束先 NOT VALID 再 VALIDATE。
-- 回滚：应用回退后新列闲置即可；但旧版本读 import_jobs.space_id 时假定非空，回退应用前先丢弃没有空间的草稿：
--   UPDATE openfox.import_jobs SET status = 'DISCARDED' WHERE space_id IS NULL AND status = 'DRAFT';
--   （DISCARDED 行旧版本不会读取 space_id 以外的信息；如需恢复 NOT NULL，先删除或补齐这些行。）
-- 确需移除时：恢复 000046 的 list_import_organizations、source 与 kind 约束，删除新列与约束，并在补齐 space_id 后恢复 NOT NULL。

SET ROLE openfox_migrator;

ALTER TABLE openfox.import_jobs ALTER COLUMN space_id DROP NOT NULL;
ALTER TABLE openfox.import_jobs ADD COLUMN space_created boolean NOT NULL DEFAULT false;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_space_required CHECK (space_id IS NOT NULL OR status IN ('DRAFT', 'DISCARDED')) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_space_required;

-- jira_cloud = Jira Cloud（/rest/api/2/search/jql）；jira_server = Jira Data Center / Server（/rest/api/2/search）。
ALTER TABLE openfox.import_jobs DROP CONSTRAINT import_jobs_source_valid;
ALTER TABLE openfox.import_jobs
    ADD CONSTRAINT import_jobs_source_valid CHECK (source IN ('jira_csv', 'jira_cloud', 'jira_server')) NOT VALID;
ALTER TABLE openfox.import_jobs VALIDATE CONSTRAINT import_jobs_source_valid;

ALTER TABLE openfox.import_created_resources DROP CONSTRAINT import_created_resources_kind_valid;
ALTER TABLE openfox.import_created_resources
    ADD CONSTRAINT import_created_resources_kind_valid CHECK (kind IN ('STATUS', 'LABEL', 'WORKFLOW_STATUS', 'ITERATION')) NOT VALID;
ALTER TABLE openfox.import_created_resources VALIDATE CONSTRAINT import_created_resources_kind_valid;

ALTER TABLE openfox.external_accounts ADD COLUMN source_label text NOT NULL DEFAULT '';
ALTER TABLE openfox.external_accounts ADD COLUMN link_method text NOT NULL DEFAULT '';
-- 已关联的旧行按是否带邀请回填关联方式；约束允许已关联行的 link_method 为空串，兼容 6c-1 版本的应用写入。
UPDATE openfox.external_accounts
   SET link_method = CASE WHEN invitation_id IS NOT NULL THEN 'invite' ELSE 'email' END
 WHERE linked_user_id IS NOT NULL AND link_method = '';
ALTER TABLE openfox.external_accounts
    ADD CONSTRAINT external_accounts_link_method_valid CHECK (
        link_method IN ('', 'email', 'invite', 'manual')
        AND (link_method = '' OR linked_user_id IS NOT NULL)
        AND char_length(source_label) <= 255) NOT VALID;
ALTER TABLE openfox.external_accounts VALIDATE CONSTRAINT external_accounts_link_method_valid;
CREATE INDEX external_accounts_list_idx ON openfox.external_accounts (organization_id, created_at DESC, id DESC);

-- 与 000046 相同，另加拉取中的 API 草稿。
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
         WHERE (j.status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED') OR (j.status = 'DRAFT' AND j.phase = 'fetching'))
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
