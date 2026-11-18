-- 阶段 11b · 评论提及新增 @小队 与 @全体成员，并让 @Agent 评论可以触发运行。
--
-- 仅扩展（expand-only），没有 down 迁移：
--   - comment_mentions 增加 mentioned_crew_id（外键指向 agent_crews）和 mention_all；
--     目标形态约束重建为「user / agent / crew / all 四选一」；
--     crew 与 all 增加部分唯一索引（同一评论同一小队只记一次，全体成员最多一条）。
--     旧行都是 user 或 agent 目标，满足新约束，不需要回填。
--   - task_agent_events 增加 kind = 'MENTIONED'（评论 @Agent / @小队队长触发的运行）、
--     comment_id、crew_id 两个可空列，以及 (organization_id, comment_id, agent_id) 的部分唯一索引，
--     同一评论对同一 Agent 最多入队一次（幂等键 comment-mention:<comment>:<agent> 由该唯一索引兜底）。
--     comment_id 不加外键：评论删除用墓碑，事件处理时按「评论不存在 / 已删除」跳过即可。
-- 锁与负载：
--   - ALTER TABLE ... ADD COLUMN（常量默认值）不重写表；
--   - 新 CHECK 约束 comment_mentions_target_shape 与 task_agent_events_kind_valid 需要全表校验，两张表行数有限（秒级）；
--   - 新索引是空列上的部分唯一索引，建立时只扫描一次。
-- 回滚：回退二进制即可，保留新增的列、约束与索引（旧版本只读写 user / agent 两种提及，不受影响）。
--   若确需恢复旧约束，须先确认没有 crew / all 行与 MENTIONED 事件：
--     SELECT count(*) FROM openfox.comment_mentions WHERE mentioned_crew_id IS NOT NULL OR mention_all;
--     SELECT count(*) FROM openfox.task_agent_events WHERE kind = 'MENTIONED';
--   再用新的前向迁移处理，不要改写已应用迁移。
-- 验证查询（应用后执行）：
--   SELECT conname FROM pg_constraint WHERE conname IN ('comment_mentions_target_shape','task_agent_events_kind_valid');
--   SELECT indexname FROM pg_indexes WHERE schemaname = 'openfox'
--    AND indexname IN ('comment_mentions_crew_unique','comment_mentions_all_unique','task_agent_events_mention_unique');
--   SELECT count(*) FROM openfox.comment_mentions WHERE mention_all OR mentioned_crew_id IS NOT NULL;  -- 刚迁移时为 0

SET ROLE openfox_migrator;

ALTER TABLE openfox.comment_mentions
    ADD COLUMN mentioned_crew_id uuid,
    ADD COLUMN mention_all boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT comment_mentions_crew_fk
        FOREIGN KEY (organization_id, mentioned_crew_id)
        REFERENCES openfox.agent_crews (organization_id, id),
    DROP CONSTRAINT comment_mentions_target_shape,
    ADD CONSTRAINT comment_mentions_target_shape CHECK (
        (mentioned_user_id IS NOT NULL)::int + (mentioned_agent_id IS NOT NULL)::int
        + (mentioned_crew_id IS NOT NULL)::int + mention_all::int = 1
    );

CREATE UNIQUE INDEX comment_mentions_crew_unique
    ON openfox.comment_mentions (organization_id, comment_id, mentioned_crew_id)
    WHERE mentioned_crew_id IS NOT NULL;
CREATE UNIQUE INDEX comment_mentions_all_unique
    ON openfox.comment_mentions (organization_id, comment_id)
    WHERE mention_all;

ALTER TABLE openfox.task_agent_events
    ADD COLUMN comment_id uuid,
    ADD COLUMN crew_id uuid,
    DROP CONSTRAINT task_agent_events_kind_valid,
    ADD CONSTRAINT task_agent_events_kind_valid CHECK (kind IN ('ASSIGNED', 'UNASSIGNED', 'MENTIONED')),
    ADD CONSTRAINT task_agent_events_mention_shape CHECK ((kind = 'MENTIONED') = (comment_id IS NOT NULL));

CREATE UNIQUE INDEX task_agent_events_mention_unique
    ON openfox.task_agent_events (organization_id, comment_id, agent_id)
    WHERE kind = 'MENTIONED';
