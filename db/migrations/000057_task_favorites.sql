-- 阶段 7e「任务视图补齐」：任务收藏表 task_favorites。
--
-- 仅扩展（expand-only），新表，不改动任何已有表：
--   - task_favorites：个人偏好，一行 = 某成员收藏了某任务。主键 (organization_id, user_id, task_id)，天然幂等。
--     RLS 双重限定：租户隔离 + 只有本人能读写自己的行（与 agent_favorites 一致）。
--     读权限不在表里表达：收藏时应用层按任务读权限校验；列表按「收藏」范围查询时，仍要同时满足空间可读与任务未删除。
--   - tasks.start_date 在 000025 已存在（含 start_date <= due_date 约束），甘特图不需要新列。
-- 锁：只建新表与新索引，没有对已有表加锁（外键引用 organization_members / tasks 的主键，不改它们）。
-- 回滚：应用回退后新表闲置即可（旧版本不读）。确需移除时：DROP TABLE openfox.task_favorites（只丢失收藏偏好）。

SET ROLE openfox_migrator;

CREATE TABLE openfox.task_favorites (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    task_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, user_id, task_id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id)
);
-- 反向查：某任务被谁收藏（删除任务 / 清理时用），以及按收藏时间倒序列出个人收藏。
CREATE INDEX task_favorites_task_idx ON openfox.task_favorites (organization_id, task_id);

ALTER TABLE openfox.task_favorites ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_favorites FORCE ROW LEVEL SECURITY;
CREATE POLICY task_favorites_owner ON openfox.task_favorites
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND user_id = openfox_private.current_actor_id());

RESET ROLE;

GRANT SELECT, INSERT, DELETE ON openfox.task_favorites TO openfox_app;
