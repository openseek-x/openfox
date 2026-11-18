-- 首页「我的任务」：跨空间按「指派给我」「我创建的」列出任务。
--
-- 「我创建的」取自创建任务时同事务写入的 task.created 活动（actor_id 即创建人），
-- 不在 tasks 上新增列：回填需要绕过 FORCE RLS，风险高于收益。
-- 两个部分索引分别服务创建人与被指派人的查找。
--
-- 仅扩展（expand-only）。表为控制面数据、规模小；迁移运行器在事务内执行每个文件，
-- 无法使用 CONCURRENTLY，CREATE INDEX 期间会短暂阻塞这两张表的写入。
-- 回滚：应用回退后索引闲置即可；如需移除，DROP INDEX 这两个索引。

SET ROLE openfox_migrator;

CREATE INDEX IF NOT EXISTS task_activities_creator_idx
    ON openfox.task_activities (organization_id, actor_id, task_id)
    WHERE kind = 'task.created';

CREATE INDEX IF NOT EXISTS tasks_assignee_idx
    ON openfox.tasks (organization_id, assignee_id)
    WHERE assignee_id IS NOT NULL;

RESET ROLE;
