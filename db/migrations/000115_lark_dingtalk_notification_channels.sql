-- 个人通知渠道新增 lark、dingtalk：此前它们复用 feishu 行，现在各有各的行。
-- 只做 expand，无 down：应用回滚时保留新增的 CHECK 取值与行（旧二进制仍读 feishu 行，行为不变）。
-- 先于新版应用执行：新版应用会保存 lark/dingtalk 渠道行，旧 CHECK 会拒绝。
--
-- 触发函数 openfox_private.enqueue_im_notification（000113）不替换：它只在 wecom 与 DIRECT_MESSAGE
-- 两处读渠道行，而 DIRECT_MESSAGE 规则（仅 feishu、wecom）按决策保持不变；lark/dingtalk 的渠道
-- 开关与事件过滤在投递 worker 里按 provider 读取（internal/integrations/feishu_notify.go），
-- 与触发函数无关，因此无需改动触发函数。
--
-- 回填：为已有 ACTIVE 的 lark/dingtalk 个人连接的用户，复制其当前 feishu 行（若有），保持行为不变。
-- 有界（行数 ≤ 有 feishu 行的用户数 × 2）且幂等（ON CONFLICT DO NOTHING，重复执行不产生新行）。
-- 没有 feishu 行的用户不回填：无行即默认开启，与此前一致。
--
-- 验证查询（执行后）：
--   SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'user_notification_channels_channel_valid';
--   -- 每个已有 lark/dingtalk 个人连接且有 feishu 行的用户都应已有同名渠道行，结果应为 0：
--   SELECT count(*) FROM openfox.user_notification_channels f
--     JOIN openfox.integration_connections c ON c.owner_user_id = f.user_id AND c.scope = 'PERSONAL'
--      AND c.status <> 'DISCONNECTED' AND c.provider IN ('lark', 'dingtalk')
--    WHERE f.channel = 'feishu'
--      AND NOT EXISTS (SELECT 1 FROM openfox.user_notification_channels x WHERE x.user_id = f.user_id AND x.channel = c.provider);
--   -- 重复执行回填语句后行数不变。
SET ROLE openfox_migrator;

ALTER TABLE openfox.user_notification_channels
    DROP CONSTRAINT user_notification_channels_channel_valid;
ALTER TABLE openfox.user_notification_channels
    ADD CONSTRAINT user_notification_channels_channel_valid
    CHECK (channel IN ('desktop', 'browser', 'feishu', 'lark', 'dingtalk', 'email', 'wecom'));

-- integration_connections 是强制 RLS 的租户表：回填期间给迁移角色一条临时只读策略，同一事务内删除（与 000106 做法一致）。
CREATE POLICY integration_connections_notify_backfill_read ON openfox.integration_connections
    FOR SELECT TO openfox_migrator USING (true);

INSERT INTO openfox.user_notification_channels (user_id, channel, enabled, disabled_events, direct_messages, updated_at)
SELECT DISTINCT f.user_id, c.provider, f.enabled, f.disabled_events, f.direct_messages, f.updated_at
  FROM openfox.user_notification_channels f
  JOIN openfox.integration_connections c
    ON c.owner_user_id = f.user_id AND c.scope = 'PERSONAL' AND c.status <> 'DISCONNECTED'
   AND c.provider IN ('lark', 'dingtalk')
 WHERE f.channel = 'feishu'
ON CONFLICT (user_id, channel) DO NOTHING;

DROP POLICY integration_connections_notify_backfill_read ON openfox.integration_connections;

RESET ROLE;
