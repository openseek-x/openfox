-- 阶段 7g-2「GitLab（自部署 / 极狐）与 Gitee」。
--
-- 仅扩展（expand-only），不改动、不删除任何已有对象：
--   - integration_connections.token_expires_at（可空、无默认）：OAuth 访问令牌的过期时间。GitLab 访问令牌 2 小时过期、Gitee 1 天过期，
--     刷新令牌每次使用后换新（旧的立即失效），所以由后台刷新任务在到期前续期；GitHub OAuth App 令牌不过期，此列为空。
--     令牌本身仍然只在 credentials_sealed（信封加密）里，这一列只有时间。
--   - integration_connections_token_refresh_idx：部分索引，只覆盖需要续期的 ACTIVE 连接（GitLab、极狐、Gitee），刷新任务按到期时间扫描。
--   - task_issue_links.issue_ref（NOT NULL DEFAULT ''）：API 里引用该 Issue 的标识。GitHub / GitLab 为空（用 issue_number），
--     Gitee 的 Issue 编号是字母数字串（如 I1ABCD），放在这里；issue_number 存 Gitee 的数字 id，保证唯一索引不变。
--     长度约束先 NOT VALID 再 VALIDATE（新增的列全是空串，校验瞬间完成）。
--   - list_git_token_refresh：控制面函数（SECURITY DEFINER、OWNER openfox_migrator、REVOKE PUBLIC，只给 openfox_context 执行），
--     返回令牌将在 p_before 之前过期的 ACTIVE 连接（组织 ID + 连接 ID），按 (组织, 连接) 游标分页，上限 500。
--   - 不需要新的唯一索引：integration_connections_org_unique / personal_unique、task_git_links_*、integration_webhook_deliveries_unique
--     在 000045 / 000048 里本来就带 provider（或连接 ID）。GitLab 的个人连接用 parent_connection_id 区分实例，
--     personal_unique 已经把 COALESCE(parent_connection_id) 放进键里，所以不用重建。
-- RLS：不新增表；已有表的策略不变。
-- 锁：ADD COLUMN（可空 / 常量默认）只改目录，瞬间完成，但需要短暂的 ACCESS EXCLUSIVE 锁；
--   CREATE INDEX（非 CONCURRENTLY，迁移是单事务）持有 SHARE 锁，期间阻塞 integration_connections 的写入（读不受影响）。
--   部分索引谓词要求 token_expires_at 非空，此前没有这样的行，索引为空，但仍需顺序扫描全表一遍；
--   表行数大（百万级以上）时建议低峰期执行。VALIDATE CONSTRAINT 只持 SHARE UPDATE EXCLUSIVE 锁，不阻塞读写。
-- 回滚：应用回退前先在页面里断开 GitLab / 极狐 / Gitee 连接（断开后不再刷新）。之后：
--   DROP FUNCTION openfox_private.list_git_token_refresh(timestamptz, uuid, uuid, integer);
--   DROP INDEX openfox.integration_connections_token_refresh_idx;
--   ALTER TABLE openfox.task_issue_links DROP CONSTRAINT task_issue_links_ref_valid, DROP COLUMN issue_ref;
--   ALTER TABLE openfox.integration_connections DROP COLUMN token_expires_at;
--   旧版本应用不认识这些列，闲置的列不影响 GitHub / 飞书 / 钉钉。

SET ROLE openfox_migrator;

ALTER TABLE openfox.integration_connections ADD COLUMN token_expires_at timestamptz;
CREATE INDEX integration_connections_token_refresh_idx
    ON openfox.integration_connections (token_expires_at, organization_id, id)
    WHERE token_expires_at IS NOT NULL AND status = 'ACTIVE' AND provider IN ('gitlab', 'gitlab_cn', 'gitee');

ALTER TABLE openfox.task_issue_links ADD COLUMN issue_ref text NOT NULL DEFAULT '';
ALTER TABLE openfox.task_issue_links
    ADD CONSTRAINT task_issue_links_ref_valid CHECK (char_length(issue_ref) <= 64) NOT VALID;
ALTER TABLE openfox.task_issue_links VALIDATE CONSTRAINT task_issue_links_ref_valid;

CREATE OR REPLACE FUNCTION openfox_private.list_git_token_refresh(p_before timestamptz, p_after_organization uuid, p_after_connection uuid, p_limit integer)
RETURNS TABLE (organization_id uuid, connection_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_before IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid git token refresh scan' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT c.organization_id, c.id
      FROM openfox.integration_connections c
      JOIN openfox.organization_directory d ON d.organization_id = c.organization_id AND d.deleted_at IS NULL
     WHERE c.token_expires_at IS NOT NULL AND c.token_expires_at <= p_before AND c.status = 'ACTIVE'
       AND c.provider IN ('gitlab', 'gitlab_cn', 'gitee')
       AND (p_after_organization IS NULL
            OR (c.organization_id, c.id) > (p_after_organization, coalesce(p_after_connection, '00000000-0000-0000-0000-000000000000'::uuid)))
     ORDER BY c.organization_id, c.id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_git_token_refresh(timestamptz, uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_git_token_refresh(timestamptz, uuid, uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.list_git_token_refresh(timestamptz, uuid, uuid, integer) TO openfox_context;
