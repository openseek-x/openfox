-- 阶段 6e「第三方账号绑定」。
--
-- 仅扩展（expand-only）：
--   - external_accounts.link_method 增加 verified：成员在「第三方账号绑定」页用自己的 Jira 凭证通过 /myself 校验后，
--     由系统把该 Jira 身份下的外部账号关联到本人（与管理员手工关联 manual、按邮箱 email、邀请 invite 区分）。
--   - integration_connections：个人 Jira 绑定（provider jira、scope PERSONAL、external_id = Jira 账号标识、
--     settings {site_url, deployment, display_name, email}，不保存凭证——用户决定只校验身份）。
--     同一组织、同一 Jira 站点下一个 Jira 账号只对应一个成员（部分唯一）；每个成员一个 Jira 个人绑定由已有的
--     integration_connections_personal_unique 保证。
-- 锁：external_accounts 替换 CHECK 约束（先 NOT VALID 再 VALIDATE，只持有 SHARE UPDATE EXCLUSIVE 锁校验现有行）；
--   integration_connections 新建部分唯一索引（此前没有 Jira 个人连接，索引为空）。
-- 回滚：应用回退前先把 link_method = 'verified' 的行改为 'manual'（UPDATE openfox.external_accounts SET link_method = 'manual'
--   WHERE link_method = 'verified'），再恢复旧约束；个人 Jira 连接闲置即可，确需移除时删除索引与这些行。

SET ROLE openfox_migrator;

ALTER TABLE openfox.external_accounts DROP CONSTRAINT external_accounts_link_method_valid;
ALTER TABLE openfox.external_accounts
    ADD CONSTRAINT external_accounts_link_method_valid CHECK (
        link_method IN ('', 'email', 'invite', 'manual', 'verified')
        AND (link_method = '' OR linked_user_id IS NOT NULL)
        AND char_length(source_label) <= 255) NOT VALID;
ALTER TABLE openfox.external_accounts VALIDATE CONSTRAINT external_accounts_link_method_valid;

CREATE UNIQUE INDEX integration_connections_jira_user_unique
    ON openfox.integration_connections (organization_id, lower(settings->>'site_url'), external_id)
    WHERE provider = 'jira' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';

RESET ROLE;
