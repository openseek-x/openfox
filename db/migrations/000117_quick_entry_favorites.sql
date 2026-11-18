-- 阶段 12f「快捷条目收藏」：快捷回复与快捷指令的个人收藏（置顶显示）。
--
-- 仅扩展（expand-only），没有 down：
--   - quick_entry_favorites：新表。主键 (organization_id, user_id, entry_type, entry_id)，entry_type 为
--     REPLY（quick_replies）或 COMMAND（task_quick_commands）。收藏只属于本人，是否可见由应用层按条目当前的
--     可见性判断（组织级快捷回复、本人个人回复、空间内共享或本人私有的快捷指令）。
--   - 条目失效策略：采用「查询时过滤」而不是外键级联。条目是多态引用（两张表），无法用单个外键约束；
--     且两张条目表都是软删除（deleted_at），行不会被物理删除，级联没有触发场景。列表查询只对仍可见的条目
--     读取收藏标记，条目被软删除或对本人不再可见后收藏行成为不可见的孤行，不会泄露也不会占用上限；
--     条目恢复可见时收藏随之重新生效。
--   - 组织复合外键：(organization_id, user_id) → organization_members，保证收藏人属于同一组织。
--   - 强制 RLS，租户隔离策略与 000056 / 000065 一致；本人数据的读写范围由应用层按 user_id 限定。
-- 锁：只新建表和索引；外键添加时对 organization_members 只有短暂的 SHARE ROW EXCLUSIVE 锁，不扫描已有数据。
-- 回滚：应用回退后新表闲置即可（旧版本不读也不写）。确需移除时：DROP TABLE openfox.quick_entry_favorites;

SET ROLE openfox_migrator;

CREATE TABLE openfox.quick_entry_favorites (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    entry_type text NOT NULL,
    entry_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, user_id, entry_type, entry_id),
    CONSTRAINT quick_entry_favorites_member_fk
        FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT quick_entry_favorites_type_valid CHECK (entry_type IN ('REPLY', 'COMMAND'))
);

ALTER TABLE openfox.quick_entry_favorites ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.quick_entry_favorites FORCE ROW LEVEL SECURITY;
CREATE POLICY quick_entry_favorites_tenant_isolation ON openfox.quick_entry_favorites
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

GRANT SELECT, INSERT, DELETE ON openfox.quick_entry_favorites TO openfox_app;
