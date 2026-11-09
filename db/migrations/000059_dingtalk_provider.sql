-- 阶段 7g-1「钉钉」：服务商标识为 dingtalk，复用 6d 的全部表（im_connection_states / im_inbound_events / im_notification_outbox / im_bots 等）。
--
-- 仅扩展（expand-only）：
--   - integration_connections：为 dingtalk 单独建三个部分索引，与 000050 / 000051 里按 provider = 'feishu' 建的索引一一对应：
--       * integration_connections_dingtalk_app_unique：同一个 钉钉应用（App ID）在所有组织之间唯一（已断开的不算）；
--       * integration_connections_dingtalk_user_unique：同一个 钉钉用户在一个组织、一个应用下只对应一个成员；
--       * integration_connections_dingtalk_union_idx：机器人按 settings.union_id 认人（钉钉里它是员工 userid）。
--     飞书与 钉钉的索引各自带 provider 谓词，互不影响：同一个 App ID 字符串在飞书和 钉钉下不会被误判为重复。
--     不改动、不删除任何已有索引，所以没有「先建新、再删旧」的步骤，也没有旧索引需要重建的回滚语句。
--   - enqueue_im_notification（通知入队触发器函数）：个人连接的 provider 由 = 'feishu' 放宽为 IN ('feishu', 'lark', 'dingtalk')，
--     只放宽，既有行为不变。触发器本身（notifications_enqueue_im_delivery）不需要重建。
--   - list_im_connections（控制面：后台找到需要长连接的连接）：同样放宽为 IN ('feishu', 'lark', 'dingtalk')。
--   - 通知渠道：dingtalk 沿用 user_notification_channels 的 'feishu' 行（CHECK 约束不变）。
--   - im_app_registrations.provider 在 000051 已存在（带格式约束），不需要改动。
-- RLS：不新增表；已有表的策略不变。函数仍是 SECURITY DEFINER、OWNER openfox_migrator、REVOKE PUBLIC，GRANT EXECUTE 沿用 000050。
-- 锁：CREATE INDEX（非 CONCURRENTLY，迁移是单事务）会在 integration_connections 上持有 SHARE 锁，期间阻塞该表的写入
--   （读不受影响）。三个索引都带 provider = 'dingtalk' 的部分谓词，此前不存在 钉钉连接，索引为空，建索引几乎瞬间完成，
--   但全表仍需被顺序扫描一遍以确认没有匹配行：表行数大（百万级以上）时建议在低峰期执行。
--   CREATE OR REPLACE FUNCTION 只在函数对象上短暂加锁，不锁表。
-- 回滚：应用回退前先确认没有 dingtalk 连接（SELECT count(*) FROM openfox.integration_connections WHERE provider = 'dingtalk' AND status <> 'DISCONNECTED'），
--   有则先在页面里断开。之后：DROP INDEX openfox.integration_connections_dingtalk_app_unique, openfox.integration_connections_dingtalk_user_unique,
--   openfox.integration_connections_dingtalk_union_idx；再用 000058 里的定义 CREATE OR REPLACE 恢复 enqueue_im_notification 与 list_im_connections
--   （把 IN ('feishu', 'lark', 'dingtalk') 改回 IN ('feishu', 'lark')，即 000058 的定义）。旧版本不认识 dingtalk，闲置的 dingtalk 行不影响飞书。

SET ROLE openfox_migrator;

CREATE UNIQUE INDEX integration_connections_dingtalk_app_unique
    ON openfox.integration_connections (provider, lower(external_id))
    WHERE provider = 'dingtalk' AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED';
CREATE UNIQUE INDEX integration_connections_dingtalk_user_unique
    ON openfox.integration_connections (organization_id, parent_connection_id, external_id)
    WHERE provider = 'dingtalk' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';
CREATE INDEX integration_connections_dingtalk_union_idx
    ON openfox.integration_connections (organization_id, (settings->>'union_id'))
    WHERE provider = 'dingtalk' AND scope = 'PERSONAL' AND status = 'ACTIVE';

CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_notification()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.im_notification_outbox (
        organization_id, notification_id, recipient_user_id, personal_connection_id, app_connection_id,
        kind, safe_summary, task_id, conversation_id)
    SELECT n.organization_id, n.id, n.recipient_user_id, p.id, a.id, n.kind, n.safe_summary, n.task_id, n.conversation_id
      FROM changed_new n
      JOIN openfox.integration_connections p
        ON p.organization_id = n.organization_id AND p.provider IN ('feishu', 'lark', 'dingtalk') AND p.scope = 'PERSONAL'
       AND p.owner_user_id = n.recipient_user_id AND p.status = 'ACTIVE'
      JOIN openfox.integration_connections a
        ON a.organization_id = p.organization_id AND a.id = p.parent_connection_id
       AND a.scope = 'ORGANIZATION' AND a.account_kind = 'app' AND a.status = 'ACTIVE'
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_im_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_notification() FROM PUBLIC;

CREATE OR REPLACE FUNCTION openfox_private.list_im_connections(p_after_organization uuid, p_after_connection uuid, p_limit integer)
RETURNS TABLE (organization_id uuid, connection_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im connection scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT c.organization_id, c.id
      FROM openfox.integration_connections c
      JOIN openfox.organization_directory d ON d.organization_id = c.organization_id AND d.deleted_at IS NULL
     WHERE c.provider IN ('feishu', 'lark', 'dingtalk') AND c.scope = 'ORGANIZATION' AND c.status = 'ACTIVE'
       AND (p_after_organization IS NULL
            OR (c.organization_id, c.id) > (p_after_organization, coalesce(p_after_connection, '00000000-0000-0000-0000-000000000000'::uuid)))
     ORDER BY c.organization_id, c.id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) FROM PUBLIC;

RESET ROLE;
