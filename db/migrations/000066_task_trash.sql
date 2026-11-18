-- 阶段 10e「任务回收站」：空间「最近删除」列表（验收台账 R04）。
--
-- 原因：GET …/spaces/{spaceID}/deleted-tasks 按 (deleted_at DESC, id DESC) 游标分页列出空间里已删除的任务
--   （非空间管理员再按 deleted_by 过滤）。现有 tasks 索引都以 created_at / status_id / priority 等开头，
--   没有能按 deleted_at 排序的，空间任务多时只能扫描全空间再排序。
-- 改法（仅扩展，expand-only）：新建部分索引 tasks_trash_idx，只收录已删除的任务（deleted_at IS NOT NULL），
--   不改表结构、不改写或回填任何数据。id 也按降序，与列表排序 (deleted_at DESC, id DESC) 完全一致，
--   顺序扫描索引即得结果顺序（id 升序时计划会多一步 Incremental Sort）。删除仍是现有的软删除（deleted_at / deleted_by），恢复把这两列置空。
-- 锁与规模：迁移运行器在事务内执行每个文件，不能用 CONCURRENTLY。CREATE INDEX 持 tasks 的 SHARE 锁，
--   期间阻塞 tasks 的写入（读不受影响），耗时取决于扫描全表的时间，与已删除行数关系不大。
--   tasks 是租户业务表，当前部署规模为万行以内，建索引在秒级以内完成；部分索引只含已删除行，体积很小。
--   规模明显更大的环境请在低峰期执行，并先用下方预检查询评估。
-- 新旧版本共存：旧二进制不使用该索引，新索引只增加已删除行写入时的极小开销。
-- 预检查询（执行前）：
--   SELECT count(*) AS tasks, count(*) FILTER (WHERE deleted_at IS NOT NULL) AS deleted FROM openfox.tasks;
--   SELECT to_regclass('openfox.tasks_trash_idx');                          -- 期望 NULL
-- 验证查询（执行后）：
--   SELECT indexdef FROM pg_indexes WHERE schemaname = 'openfox' AND indexname = 'tasks_trash_idx';
--     -- 期望 ... USING btree (organization_id, space_id, deleted_at DESC, id DESC) WHERE (deleted_at IS NOT NULL)
--   EXPLAIN SELECT id FROM openfox.tasks
--    WHERE organization_id = '<org>'::uuid AND space_id = '<space>'::uuid AND deleted_at IS NOT NULL
--    ORDER BY deleted_at DESC, id DESC LIMIT 51;                            -- 期望使用 tasks_trash_idx
-- 回滚（先回退应用；只删索引，不影响任何数据）：
--   SET ROLE openfox_migrator;
--   DROP INDEX openfox.tasks_trash_idx;
--   RESET ROLE;

SET ROLE openfox_migrator;

CREATE INDEX tasks_trash_idx
    ON openfox.tasks (organization_id, space_id, deleted_at DESC, id DESC)
    WHERE deleted_at IS NOT NULL;

RESET ROLE;
