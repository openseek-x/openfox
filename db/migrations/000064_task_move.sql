-- 阶段 9c-1「任务移动到空间」（复测报告 D09）。
--
-- 原因：
--   1. 移动后任务在目标空间取新编号，旧链接、搜索、Git 提交里的旧编号要能解析到新编号。新表 task_display_aliases
--      按 (organization_id, space_id, sequence) 记录「源空间的哪个序号现在是哪个任务」。按空间而不是按前缀文字记，
--      源空间以后改前缀时，space_prefix_history 能把旧前缀解析回这个空间，再查别名即可。
--   2. task_git_links（000048）、task_issue_links（000049）以 (organization_id, space_id, task_id) 引用 tasks，
--      没有 ON UPDATE 动作，应用角色也没有这两张表的 DELETE 权限。UPDATE tasks SET space_id 时外键检查失败（23503），
--      停用空间的「移动未完成任务」因此对有 PR / 提交关联或 Issue 同步的任务直接报错。改为 ON UPDATE CASCADE，
--      Git 关联与 Issue 链接随任务改写 space_id。
-- 改法（expand-only，不改写、不回填任何已有数据）：
--   - 新建 task_display_aliases，开启并强制 RLS（租户隔离策略与 000048 / 000049 同一写法），
--     openfox_app 只有 SELECT / INSERT / UPDATE（写别名用 ON CONFLICT DO UPDATE）。
--     外键：tasks、spaces 为 ON DELETE CASCADE；moved_by → organization_members 为 NO ACTION。
--     应用只做软删除（tasks.deleted_at、spaces.deleted_at），别名行不受影响：已删除任务的别名在解析时按「任务不存在」处理。
--     只有运维手工硬删除任务或空间时才会级联删除对应别名，避免别名挡住硬删除；删除的别名不会再被用到。
--   - 两条外键：先以新名字 ADD CONSTRAINT ... ON UPDATE CASCADE NOT VALID，再 VALIDATE，最后删旧约束。
--     级联更新由外键触发器以表属主身份执行，不受 RLS 影响，应用角色不需要新权限。
-- 锁：CREATE TABLE 只锁新表。ADD CONSTRAINT ... NOT VALID 与 DROP CONSTRAINT 短暂持有 task_git_links / task_issue_links 的
--   ACCESS EXCLUSIVE 与 tasks 的 SHARE ROW EXCLUSIVE；VALIDATE CONSTRAINT 持 SHARE UPDATE EXCLUSIVE，不阻塞读写，
--   但要扫一遍两张子表（每行按主键查 tasks；这两张表行数小）。单事务完成，其他会话看不到中间状态。
-- 新旧版本共存：旧二进制不读别名表；新外键对插入与普通更新的约束与旧外键相同，只多了 space_id 的级联，
--   旧二进制在新库上的停用空间移动反而能成功。回滚应用只需换回旧二进制，本迁移保留。
-- 预检查询（执行前）：
--   SELECT conrelid::regclass, conname, pg_get_constraintdef(oid) FROM pg_constraint
--    WHERE conrelid IN ('openfox.task_git_links'::regclass, 'openfox.task_issue_links'::regclass)
--      AND confrelid = 'openfox.tasks'::regclass;                          -- 期望 2 行，名称为下文 DROP 的两个
--   SELECT to_regclass('openfox.task_display_aliases');                    -- 期望 NULL
-- 验证查询（执行后）：
--   SELECT conname, confupdtype, convalidated FROM pg_constraint
--    WHERE conrelid IN ('openfox.task_git_links'::regclass, 'openfox.task_issue_links'::regclass)
--      AND confrelid = 'openfox.tasks'::regclass;                          -- 期望只有 *_task_fk 两行，confupdtype = 'c'、convalidated = true
--   SELECT relrowsecurity, relforcerowsecurity FROM pg_class
--    WHERE oid = 'openfox.task_display_aliases'::regclass;                 -- 期望 t, t
--   SELECT privilege_type FROM information_schema.role_table_grants
--    WHERE table_schema = 'openfox' AND table_name = 'task_display_aliases' AND grantee = 'openfox_app'
--    ORDER BY 1;                                                           -- 期望 INSERT, SELECT, UPDATE
-- 回滚（先回退应用；会丢失全部旧编号别名，任务数据不受影响；已发生的移动不会撤销）：
--   BEGIN;
--   SET ROLE openfox_migrator;
--   DROP TABLE openfox.task_display_aliases;
--   ALTER TABLE openfox.task_git_links
--     ADD CONSTRAINT task_git_links_organization_id_space_id_task_id_fkey FOREIGN KEY (organization_id, space_id, task_id)
--     REFERENCES openfox.tasks (organization_id, space_id, id) NOT VALID;
--   ALTER TABLE openfox.task_git_links VALIDATE CONSTRAINT task_git_links_organization_id_space_id_task_id_fkey;
--   ALTER TABLE openfox.task_git_links DROP CONSTRAINT task_git_links_task_fk;
--   （task_issue_links 同样三步，名称换成 task_issue_links_*）
--   RESET ROLE;
--   COMMIT;
--   回退外键后，停用空间移动有 Git 关联 / Issue 链接的任务会再次失败。

SET ROLE openfox_migrator;

-- 1. 旧编号别名
CREATE TABLE openfox.task_display_aliases (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,   -- 旧编号所在的源空间
    sequence bigint NOT NULL, -- 旧序号
    task_id uuid NOT NULL,
    moved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    moved_by uuid NOT NULL,
    PRIMARY KEY (organization_id, space_id, sequence),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, task_id) REFERENCES openfox.tasks (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, moved_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_display_aliases_sequence_positive CHECK (sequence > 0)
);
CREATE INDEX task_display_aliases_task_idx ON openfox.task_display_aliases (organization_id, task_id);

ALTER TABLE openfox.task_display_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_display_aliases FORCE ROW LEVEL SECURITY;
CREATE POLICY task_display_aliases_tenant_isolation ON openfox.task_display_aliases
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 2. Git 关联与 Issue 链接随任务改写 space_id
ALTER TABLE openfox.task_git_links
    ADD CONSTRAINT task_git_links_task_fk FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id) ON UPDATE CASCADE NOT VALID;
ALTER TABLE openfox.task_git_links VALIDATE CONSTRAINT task_git_links_task_fk;
ALTER TABLE openfox.task_git_links DROP CONSTRAINT task_git_links_organization_id_space_id_task_id_fkey;

ALTER TABLE openfox.task_issue_links
    ADD CONSTRAINT task_issue_links_task_fk FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id) ON UPDATE CASCADE NOT VALID;
ALTER TABLE openfox.task_issue_links VALIDATE CONSTRAINT task_issue_links_task_fk;
ALTER TABLE openfox.task_issue_links DROP CONSTRAINT task_issue_links_organization_id_space_id_task_id_fkey;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.task_display_aliases TO openfox_app;
