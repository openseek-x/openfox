-- 首页收件箱的三个分区：expand-only。
-- 1. notifications 增加 cleared_at / snoozed_until：收件箱 = 未清理且不在稍后期内，
--    稍后 = snoozed_until 晚于当前时间，已清理 = cleared_at 非空。旧行两列均为 NULL，
--    即全部落在「收件箱」分区，与迁移前的列表行为一致。
-- 2. notifications 增加 actor_user_id：由 enqueue_notification 在 user_request 上下文中
--    自动记录发起人（不信任调用方传参），用于行内「某某 提到了你」。旧行为 NULL。
-- 3. kind 增加 RUN_FAILED（Agent 运行失败）与 TASK_ASSIGNED（任务分配给我）。
--    放宽 CHECK 只会接受更多取值，已有数据全部满足新约束；重建约束需要一次全表校验，
--    持有 ACCESS EXCLUSIVE 锁的时间与 notifications 行数成正比。
-- 不改写已有数据，不放宽任何 GRANT 或 RLS 策略：openfox_app 仍只能读写收件人为自己的行。

SET ROLE openfox_migrator;

ALTER TABLE openfox.notifications
    ADD COLUMN actor_user_id uuid,
    ADD COLUMN snoozed_until timestamptz,
    ADD COLUMN cleared_at timestamptz,
    ADD CONSTRAINT notifications_actor_member_fk
        FOREIGN KEY (organization_id, actor_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    DROP CONSTRAINT notifications_kind_valid,
    ADD CONSTRAINT notifications_kind_valid CHECK (
        kind IN ('MENTION', 'HANDOFF', 'REPLY', 'TASK_COMMENT', 'RUN_FAILED', 'TASK_ASSIGNED')
    );

-- 收件箱分组查询按 (组织, 收件人) 取出当前分区的行再按任务/对话聚合；
-- 已有 notifications_recipient_idx 的前缀可覆盖该访问路径，这里只为清理时间补一个索引，
-- 让「已清理」分区与归档统计不必扫描收件人的全部通知。
CREATE INDEX notifications_recipient_cleared_idx
    ON openfox.notifications (organization_id, recipient_user_id, cleared_at DESC)
    WHERE cleared_at IS NOT NULL;

-- 签名不变，CREATE OR REPLACE 保留所有者与已有 EXECUTE 授权。
-- 新增：user_request 上下文下记录 actor_user_id；发起人与收件人相同时不写通知。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_notification(
    p_recipient_user_id uuid,
    p_kind text,
    p_conversation_id uuid,
    p_message_id uuid,
    p_task_id uuid,
    p_comment_id uuid,
    p_dedupe_key text,
    p_safe_summary text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    v_actor uuid;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM openfox.organization_members
         WHERE organization_id = openfox_private.current_organization_id()
           AND user_id = p_recipient_user_id AND status = 'ACTIVE'
    ) THEN
        RAISE EXCEPTION 'notification recipient is not active in the organization'
            USING ERRCODE = '23514';
    END IF;
    IF openfox_private.current_context_source() = 'user_request' THEN
        v_actor := openfox_private.current_actor_id();
        IF v_actor = p_recipient_user_id THEN
            RETURN;
        END IF;
    END IF;
    INSERT INTO openfox.notifications (
        organization_id, recipient_user_id, kind, conversation_id, message_id,
        task_id, comment_id, dedupe_key, safe_summary, actor_user_id
    ) VALUES (
        openfox_private.current_organization_id(), p_recipient_user_id, p_kind,
        p_conversation_id, p_message_id, p_task_id, p_comment_id,
        p_dedupe_key, p_safe_summary, v_actor
    )
    ON CONFLICT (organization_id, recipient_user_id, dedupe_key) DO NOTHING;
END
$$;

RESET ROLE;
