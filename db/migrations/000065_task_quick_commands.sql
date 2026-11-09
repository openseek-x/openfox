-- 阶段 9f「快捷指令」：任务详情「召唤 Agent」菜单里的快捷指令（名称、图标、提示词、目标 Agent / 小队、可见性）。
--
-- 仅扩展（expand-only）：
--   - task_quick_commands：新表，按空间归属。visibility = PRIVATE（仅 owner_user_id 本人可见和维护）或
--     SHARED（空间内可读的人都可见；创建人与空间管理员维护，由应用层按现有空间策略判断）。
--     目标是 agent_id 与 crew_id 恰好一个。删除为软删除（deleted_at），排序用 position（0 起），
--     version 是编辑的乐观锁。每人每空间私有 50 条、每空间共享 50 条的上限由服务端持咨询锁后计数。
--     图标 icon_kind 与视图一致（icon 内置图标名 / emoji 表情 / upload 上传图标 ID），与 icon 同空同非空；
--     upload 引用的 uploaded_icons 由应用层校验存在（icon 为 text，不建外键）。
--   - 外键只到 (organization_id, agent_id) / (organization_id, crew_id)：Agent 与小队可以换空间
--     （资料编辑会改 space_id），带 space_id 的复合外键会挡住这类修改；「目标须在本空间」改由应用层在
--     新建 / 修改时校验，读列表时目标已不在本空间则标记为不可用，不报错。
--     RLS 只做租户隔离，读写权限在应用层。
-- 锁：只新建表和索引；外键添加时对 spaces / organization_members / agents / agent_crews 只有短暂的
--   SHARE ROW EXCLUSIVE 锁，不扫描、不重写已有数据。
-- 回滚：应用回退后新表闲置即可（旧版本不读也不写）。确需移除时：DROP TABLE openfox.task_quick_commands;

SET ROLE openfox_migrator;

CREATE TABLE openfox.task_quick_commands (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    visibility text NOT NULL,
    name text NOT NULL,
    icon_kind text,
    icon text,
    agent_id uuid,
    crew_id uuid,
    prompt text NOT NULL DEFAULT '',
    position integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    PRIMARY KEY (organization_id, id),
    CONSTRAINT task_quick_commands_space_fk
        FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    CONSTRAINT task_quick_commands_owner_fk
        FOREIGN KEY (organization_id, owner_user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_quick_commands_agent_fk
        FOREIGN KEY (organization_id, agent_id) REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT task_quick_commands_crew_fk
        FOREIGN KEY (organization_id, crew_id) REFERENCES openfox.agent_crews (organization_id, id),
    CONSTRAINT task_quick_commands_visibility_valid CHECK (visibility IN ('PRIVATE', 'SHARED')),
    CONSTRAINT task_quick_commands_name_valid CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT task_quick_commands_icon_valid CHECK (
        (icon_kind IS NULL AND icon IS NULL)
        OR (icon_kind IN ('icon', 'emoji', 'upload') AND char_length(icon) BETWEEN 1 AND 200)),
    CONSTRAINT task_quick_commands_target_valid CHECK ((agent_id IS NULL) <> (crew_id IS NULL)),
    CONSTRAINT task_quick_commands_prompt_valid CHECK (char_length(prompt) <= 4000),
    CONSTRAINT task_quick_commands_position_valid CHECK (position BETWEEN 0 AND 999),
    CONSTRAINT task_quick_commands_version_positive CHECK (version > 0)
);
CREATE INDEX task_quick_commands_private_idx
    ON openfox.task_quick_commands (organization_id, space_id, owner_user_id, position, created_at, id)
    WHERE visibility = 'PRIVATE' AND deleted_at IS NULL;
CREATE INDEX task_quick_commands_shared_idx
    ON openfox.task_quick_commands (organization_id, space_id, position, created_at, id)
    WHERE visibility = 'SHARED' AND deleted_at IS NULL;

ALTER TABLE openfox.task_quick_commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_quick_commands FORCE ROW LEVEL SECURITY;
CREATE POLICY task_quick_commands_tenant_isolation ON openfox.task_quick_commands
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.task_quick_commands TO openfox_app;
