-- 阶段 12b「执行结果补齐」（H-03、F-07）：运行结果的 Git 摘要与影响统计。
--
-- 仅扩展（expand-only）：
--   - run_results.git_summary：可空 jsonb，由 daemon 在运行结束后只读采集并随结果上报
--     （当前分支、新增提交列表、未提交改动数、相对基线的改动统计、未验证原因）。
--     NULL 表示旧 daemon 未上报或该目录没有仓库，界面显示“无数据”。
--     形状为 JSON 对象，序列化后不超过 32768 字节；内部字段的上限由应用层校验。
-- 已有的 branch_ref、checks、unverified 列沿用，不新增。
-- 锁：加可空列不重写表；CHECK 约束只约束新列（旧行为 NULL，约束对 NULL 为真），不需要全表校验。
-- 回滚：应用回退后该列闲置即可，不提供 down 迁移；旧二进制不读不写该列。

SET ROLE openfox_migrator;

ALTER TABLE openfox.run_results
    ADD COLUMN git_summary jsonb;

ALTER TABLE openfox.run_results
    ADD CONSTRAINT run_results_git_summary_shape CHECK (
        git_summary IS NULL
        OR (jsonb_typeof(git_summary) = 'object' AND pg_column_size(git_summary) <= 32768)
    );

RESET ROLE;
