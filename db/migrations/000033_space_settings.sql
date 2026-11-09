-- 阶段 3a「空间设置与生命周期」：空间资料与功能开关、估时、标识符历史、个人侧栏排序、空间 Git 仓库、空间标签分组。
--
-- 仅扩展（expand-only）：
--   - spaces：图标、用途、功能开关、多处理人开关、估时设置、停用 / 软删除时间。已有空间按默认值解释：
--     用途 = other；视图、专项开启，迭代、分拣台关闭（3b / 3d 再接入）；估时未启用。
--     停用沿用已有 status = ARCHIVED（列表本来就只显示 ACTIVE），新增 retired_at / retired_by 记录时间与操作人；
--     删除为软删除：deleted_at / deleted_by 非空即对所有人不可见，数据保留（用户确认的决策）。
--   - 标识符：新建与修改改为 2–7 位字母数字（应用层校验，显示大写）。约束放宽为「旧格式或新格式」，存量不改写。
--     space_prefix_history 记录被替换的旧标识符，旧编号（ENG-12）按历史跳转到同一任务；历史标识符为原空间保留，
--     其他空间不能占用（应用层在同一事务里检查两张表；本表的唯一索引兜底同一标识符只属于一个空间）。
--   - tasks.estimate：估时值。点数刻度存点数，时间刻度存秒；NULL = 未估时。
--   - space_user_order：每个成员自己的侧栏空间顺序（RLS 本人）。
--   - space_repos：空间绑定的 Git 仓库。
--   - label_groups.space_id：空间范围的标签分组；原约束「分组只用于组织标签」放宽为「分组与标签同属一个范围」，
--     由应用层在写入事务里校验（CHECK 不能引用其他表）。
--   - spaces 写入触发 space.changed 组织事件（侧栏、设置页实时刷新）。
-- 锁：spaces / tasks / labels 加列均为可空或常量默认值，不重写表；放宽 CHECK 不扫描。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000033 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.spaces
    ADD COLUMN icon text,
    ADD COLUMN purpose text NOT NULL DEFAULT 'other',
    ADD COLUMN views_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN projects_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN iterations_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN triage_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN multi_assignee boolean NOT NULL DEFAULT true,
    ADD COLUMN estimation_scale text NOT NULL DEFAULT 'notUsed',
    ADD COLUMN estimation_extended boolean NOT NULL DEFAULT false,
    ADD COLUMN estimation_allow_zero boolean NOT NULL DEFAULT false,
    ADD COLUMN estimation_count_unestimated boolean NOT NULL DEFAULT false,
    ADD COLUMN retired_at timestamptz,
    ADD COLUMN retired_by uuid,
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN deleted_by uuid;

ALTER TABLE openfox.spaces
    ADD CONSTRAINT spaces_icon_valid CHECK (icon IS NULL OR length(icon) BETWEEN 1 AND 64),
    ADD CONSTRAINT spaces_purpose_valid CHECK (purpose IN (
        'software_development', 'product_management', 'marketing', 'design', 'project_management',
        'operations', 'it_support', 'human_resources', 'customer_service', 'legal', 'finance', 'sales',
        'data_science', 'other')),
    ADD CONSTRAINT spaces_estimation_scale_valid CHECK (estimation_scale IN (
        'notUsed', 'storyPoints', 'time', 'exponential', 'fibonacci', 'linear', 'tShirt')),
    ADD CONSTRAINT spaces_retired_consistent CHECK (
        (retired_at IS NULL AND retired_by IS NULL) OR (retired_at IS NOT NULL AND retired_by IS NOT NULL)),
    ADD CONSTRAINT spaces_deleted_consistent CHECK (
        (deleted_at IS NULL AND deleted_by IS NULL) OR (deleted_at IS NOT NULL AND deleted_by IS NOT NULL)),
    ADD CONSTRAINT spaces_retired_by_fk FOREIGN KEY (organization_id, retired_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    ADD CONSTRAINT spaces_deleted_by_fk FOREIGN KEY (organization_id, deleted_by)
        REFERENCES openfox.organization_members (organization_id, user_id);

-- 标识符：旧格式（小写、2–32 位）或新格式（2–7 位大写字母数字）。
ALTER TABLE openfox.spaces DROP CONSTRAINT spaces_prefix_valid;
ALTER TABLE openfox.spaces ADD CONSTRAINT spaces_prefix_valid CHECK (
    prefix ~ '^[a-z][a-z0-9_-]{1,31}$' OR prefix ~ '^[A-Z0-9]{2,7}$');

CREATE INDEX spaces_live_idx ON openfox.spaces (organization_id, status) WHERE deleted_at IS NULL;

CREATE TABLE openfox.space_prefix_history (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    prefix text NOT NULL,
    replaced_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replaced_by uuid NOT NULL,
    PRIMARY KEY (organization_id, space_id, prefix),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, replaced_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT space_prefix_history_prefix_valid CHECK (
        prefix ~ '^[a-z][a-z0-9_-]{1,31}$' OR prefix ~ '^[A-Z0-9]{2,7}$')
);
CREATE UNIQUE INDEX space_prefix_history_prefix_unique
    ON openfox.space_prefix_history (organization_id, lower(prefix));

ALTER TABLE openfox.tasks ADD COLUMN estimate numeric(14, 2);
ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_estimate_range CHECK (estimate IS NULL OR (estimate >= 0 AND estimate <= 1000000000));

CREATE TABLE openfox.space_user_order (
    organization_id uuid NOT NULL,
    user_id uuid NOT NULL,
    space_id uuid NOT NULL,
    position integer NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, user_id, space_id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    CONSTRAINT space_user_order_position_valid CHECK (position >= 0 AND position < 10000)
);

CREATE TABLE openfox.space_repos (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    url text NOT NULL,
    description text NOT NULL DEFAULT '',
    base_branch text NOT NULL DEFAULT '',
    position integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT space_repos_url_valid CHECK (
        length(url) <= 2048 AND (url ~ '^https://[^\s]+$' OR url ~ '^(ssh://)?[A-Za-z0-9._-]+@[A-Za-z0-9.-]+[:/][^\s]+$')
    ),
    CONSTRAINT space_repos_description_length CHECK (length(description) <= 500),
    CONSTRAINT space_repos_branch_valid CHECK (length(base_branch) <= 255 AND base_branch !~ '\s'),
    CONSTRAINT space_repos_position_valid CHECK (position >= 0 AND position < 64),
    CONSTRAINT space_repos_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX space_repos_url_unique ON openfox.space_repos (organization_id, space_id, lower(url));

ALTER TABLE openfox.label_groups ADD COLUMN space_id uuid;
ALTER TABLE openfox.label_groups
    ADD CONSTRAINT label_groups_space_fk FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id);
ALTER TABLE openfox.labels DROP CONSTRAINT labels_group_organization_only;

-- RLS
ALTER TABLE openfox.space_prefix_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.space_prefix_history FORCE ROW LEVEL SECURITY;
CREATE POLICY space_prefix_history_tenant_isolation ON openfox.space_prefix_history
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.space_user_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.space_user_order FORCE ROW LEVEL SECURITY;
CREATE POLICY space_user_order_owner ON openfox.space_user_order
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND user_id = openfox_private.current_actor_id());

ALTER TABLE openfox.space_repos ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.space_repos FORCE ROW LEVEL SECURITY;
CREATE POLICY space_repos_tenant_isolation ON openfox.space_repos
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 组织事件：空间资料 / 开关 / 生命周期变化写一条 space.changed。
-- 行级触发器 + 列清单：建任务会递增 next_task_number，不能因此给每个新任务都发一条空间事件。
CREATE OR REPLACE FUNCTION openfox_private.emit_space_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, entity_id, space_id, version, payload)
    VALUES (NEW.organization_id, 'space.changed', 'space', NEW.id, NEW.id, NEW.version,
            jsonb_build_object('status', NEW.status, 'deleted', NEW.deleted_at IS NOT NULL));
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_space_event() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_space_event() FROM PUBLIC;

CREATE TRIGGER spaces_emit_event_insert
AFTER INSERT ON openfox.spaces
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_space_event();
CREATE TRIGGER spaces_emit_event_update
AFTER UPDATE OF name, visibility, status, prefix, icon, purpose, views_enabled, projects_enabled,
    iterations_enabled, triage_enabled, multi_assignee, estimation_scale, estimation_extended,
    estimation_allow_zero, estimation_count_unestimated, retired_at, deleted_at
ON openfox.spaces
FOR EACH ROW WHEN (OLD IS DISTINCT FROM NEW)
EXECUTE FUNCTION openfox_private.emit_space_event();

RESET ROLE;

GRANT SELECT, INSERT ON openfox.space_prefix_history TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.space_user_order TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.space_repos TO openfox_app;
