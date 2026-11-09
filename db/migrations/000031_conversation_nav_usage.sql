-- 阶段 2b「对话与执行」：会话的个人状态（置顶、已读位置）与 Run 的 token 用量。
--
-- 仅扩展（expand-only），不改写已有数据：
--   - conversation_user_state 为新表：每个成员对每个会话一行，置顶与已读都是个人偏好，
--     不修改 conversations.version（发消息已在递增它，复用会导致完成 / 交接的乐观锁频繁冲突）。
--     RLS 限定只能读写本人的行（与 agent_directory_pins 同一做法）。
--   - run_results.usage 为可空 jsonb：设备随结果上报 {input, cached_input, cache_write_input, output,
--     reasoning_output}（均为非负整数）；旧 daemon 不上报时为空，统计按 0 计。
--   - conversations 新增导航索引：按所有者 + 是否归档 + 最近更新排序，配合 (updated_at, id) 复合游标。
-- 迁移运行器在事务内执行每个文件，无法使用 CONCURRENTLY；conversations 新索引的创建时间与行数成正比，
-- 期间阻塞 conversations 写入（本地与新部署可忽略，生产执行前需评估行数）。
-- 回滚：应用回退后新表、新列与索引闲置即可；确需移除时见 db/migrations/README.md 的 000031 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.conversation_user_state (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    pinned_at timestamptz,
    last_read_seq bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, user_id, conversation_id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    CONSTRAINT conversation_user_state_read_seq_nonnegative CHECK (last_read_seq >= 0)
);

CREATE INDEX conversation_user_state_pinned_idx
    ON openfox.conversation_user_state (organization_id, user_id, pinned_at DESC)
    WHERE pinned_at IS NOT NULL;

ALTER TABLE openfox.conversation_user_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversation_user_state FORCE ROW LEVEL SECURITY;
CREATE POLICY conversation_user_state_owner ON openfox.conversation_user_state
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND user_id = openfox_private.current_actor_id());

ALTER TABLE openfox.run_results ADD COLUMN usage jsonb;
ALTER TABLE openfox.run_results
    ADD CONSTRAINT run_results_usage_shape CHECK (
        usage IS NULL OR (jsonb_typeof(usage) = 'object' AND pg_column_size(usage) <= 1024)
    );

CREATE INDEX conversations_owner_nav_idx
    ON openfox.conversations (organization_id, owner_user_id, (archived_at IS NULL), updated_at DESC, id DESC);

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.conversation_user_state TO openfox_app;
