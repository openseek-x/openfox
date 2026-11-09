-- 阶段 1「全局配置」：组织级标签与单选分组、状态默认与折叠、自定义关联类型与任务关联、
-- 自定义字段（选项、绑定到任务类型、任务取值）。
--
-- 仅扩展（expand-only），不改写已有数据：
--   - labels.space_id 改为可空，空值表示组织级标签；空间级唯一索引不变，另建组织级部分唯一索引。
--     旧代码只按 space_id = $1 查询，组织级标签对旧接口不可见。
--   - 新增 CHECK 只约束新列或已有应用层上限（标签名 ≤50 字符，旧写入路径已强制），
--     ADD CONSTRAINT 会校验已有行。
--   - task_statuses 不回填默认状态：没有显式默认时，应用取 TODO 类别中 position 最小的状态。
--   - 内置关联类型（BLOCKS / CLONES / DUPLICATES / RELATES）是代码常量，不建行，无需为每个组织播种。
--   - task_labels 增加作用域触发器：任务所在空间必须等于行的空间，标签必须是组织级或同空间标签。
--     旧路径只写同空间标签，已有行不受影响（触发器只作用于之后的写入）。
-- 回滚：应用回退后新列和新表闲置即可；确需移除时见 db/migrations/README.md 的 000026 条目。

SET ROLE openfox_migrator;

-- 标签分组（界面称「单选分组」：同一任务在一个分组内最多一个标签，由应用在任务版本锁内校验）。
CREATE TABLE openfox.label_groups (
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    color text NOT NULL,
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT label_groups_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 50),
    CONSTRAINT label_groups_description_length CHECK (length(description) <= 1000),
    CONSTRAINT label_groups_color_valid CHECK (color ~ '^#[0-9a-fA-F]{6}$'),
    CONSTRAINT label_groups_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX label_groups_name_unique ON openfox.label_groups (organization_id, lower(name));

ALTER TABLE openfox.labels
    ALTER COLUMN space_id DROP NOT NULL,
    ADD COLUMN color text,
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN group_id uuid,
    ADD COLUMN archived_at timestamptz,
    ADD COLUMN version bigint NOT NULL DEFAULT 1,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();

ALTER TABLE openfox.labels
    ADD CONSTRAINT labels_group_fk FOREIGN KEY (organization_id, group_id)
        REFERENCES openfox.label_groups (organization_id, id),
    ADD CONSTRAINT labels_group_organization_only CHECK (group_id IS NULL OR space_id IS NULL),
    ADD CONSTRAINT labels_name_length CHECK (length(name) <= 50),
    ADD CONSTRAINT labels_color_valid CHECK (color IS NULL OR color ~ '^#[0-9a-fA-F]{6}$'),
    ADD CONSTRAINT labels_description_length CHECK (length(description) <= 1000),
    ADD CONSTRAINT labels_version_positive CHECK (version > 0);

CREATE UNIQUE INDEX labels_organization_name_unique
    ON openfox.labels (organization_id, lower(name))
    WHERE space_id IS NULL;
CREATE INDEX labels_group_idx
    ON openfox.labels (organization_id, group_id)
    WHERE group_id IS NOT NULL;

-- 使用次数、「上次应用」与合并按标签访问 task_labels。
CREATE INDEX task_labels_label_idx ON openfox.task_labels (organization_id, label_id, created_at);

-- 作用域触发器以 definer 身份读取 tasks 与 labels；FORCE RLS 下需要给 migrator 只读策略。
-- tasks 已有 tasks_execution_trigger_access（000011）。
CREATE POLICY labels_migrator_trigger_access ON openfox.labels
    FOR SELECT TO openfox_migrator
    USING (true);

CREATE OR REPLACE FUNCTION openfox_private.validate_task_label_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    task_space_id uuid;
    label_space_id uuid;
BEGIN
    SELECT space_id INTO task_space_id
      FROM openfox.tasks
     WHERE organization_id = NEW.organization_id AND id = NEW.task_id;
    IF NOT FOUND OR task_space_id <> NEW.space_id THEN
        RAISE EXCEPTION 'task label is outside the task space' USING ERRCODE = '23514';
    END IF;
    SELECT space_id INTO label_space_id
      FROM openfox.labels
     WHERE organization_id = NEW.organization_id AND id = NEW.label_id;
    IF NOT FOUND OR (label_space_id IS NOT NULL AND label_space_id <> NEW.space_id) THEN
        RAISE EXCEPTION 'label is outside the task space' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_task_label_scope() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_task_label_scope() FROM PUBLIC;

CREATE TRIGGER task_labels_scope_valid_trigger
BEFORE INSERT OR UPDATE ON openfox.task_labels
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_task_label_scope();

-- 状态：组织默认状态（新任务默认）与列表默认折叠。
ALTER TABLE openfox.task_statuses
    ADD COLUMN is_default boolean NOT NULL DEFAULT false,
    ADD COLUMN collapsed_by_default boolean NOT NULL DEFAULT false,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE openfox.task_statuses
    ADD CONSTRAINT task_statuses_default_category CHECK (
        NOT is_default OR (category IN ('BACKLOG', 'TODO') AND archived_at IS NULL)
    );
CREATE UNIQUE INDEX task_statuses_default_unique
    ON openfox.task_statuses (organization_id)
    WHERE is_default;

-- 自定义关联类型（内置类型为代码常量）。
CREATE TABLE openfox.task_relation_types (
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    outward_label text NOT NULL,
    inward_label text NOT NULL,
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_relation_types_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 50),
    CONSTRAINT task_relation_types_outward_valid CHECK (length(btrim(outward_label)) BETWEEN 1 AND 50),
    CONSTRAINT task_relation_types_inward_valid CHECK (length(btrim(inward_label)) BETWEEN 1 AND 50),
    CONSTRAINT task_relation_types_version_positive CHECK (version > 0)
);

CREATE UNIQUE INDEX task_relation_types_name_unique
    ON openfox.task_relation_types (organization_id, lower(name));

-- 任务关联：源与目标可在同组织的不同空间；类型为内置常量或自定义类型二选一。
CREATE TABLE openfox.task_relations (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    source_space_id uuid NOT NULL,
    source_task_id uuid NOT NULL,
    target_space_id uuid NOT NULL,
    target_task_id uuid NOT NULL,
    builtin_type text,
    relation_type_id uuid,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, source_space_id, source_task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, target_space_id, target_task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, relation_type_id)
        REFERENCES openfox.task_relation_types (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_relations_type_valid CHECK (
        (builtin_type IS NULL) <> (relation_type_id IS NULL)
        AND (builtin_type IS NULL OR builtin_type IN ('BLOCKS', 'CLONES', 'DUPLICATES', 'RELATES'))
    ),
    CONSTRAINT task_relations_not_self CHECK (source_task_id <> target_task_id)
);

CREATE UNIQUE INDEX task_relations_unique
    ON openfox.task_relations (
        organization_id, source_task_id, target_task_id,
        COALESCE(builtin_type, relation_type_id::text)
    );
-- 「相关」是对称类型：A→B 与 B→A 视为同一条。
CREATE UNIQUE INDEX task_relations_symmetric_unique
    ON openfox.task_relations (
        organization_id, LEAST(source_task_id, target_task_id), GREATEST(source_task_id, target_task_id)
    )
    WHERE builtin_type = 'RELATES';
CREATE INDEX task_relations_target_idx ON openfox.task_relations (organization_id, target_task_id);
CREATE INDEX task_relations_type_idx
    ON openfox.task_relations (organization_id, relation_type_id)
    WHERE relation_type_id IS NOT NULL;

-- 自定义字段：组织级字段库。删除即在组织中隐藏（archived_at），已有取值保留。
CREATE TABLE openfox.custom_fields (
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    kind text NOT NULL,
    description text NOT NULL DEFAULT '',
    default_value jsonb,
    member_scope text,
    created_by uuid,
    archived_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT custom_fields_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 64),
    CONSTRAINT custom_fields_kind_valid CHECK (
        kind IN ('TEXT', 'NUMBER', 'SELECT', 'MULTI_SELECT', 'BOOLEAN', 'DATE', 'MEMBER', 'URL', 'RICH_TEXT')
    ),
    CONSTRAINT custom_fields_description_length CHECK (length(description) <= 1000),
    CONSTRAINT custom_fields_member_scope_valid CHECK (
        (kind = 'MEMBER' AND member_scope IN ('ALL', 'HUMANS', 'AGENTS'))
        OR (kind <> 'MEMBER' AND member_scope IS NULL)
    ),
    CONSTRAINT custom_fields_default_size CHECK (default_value IS NULL OR octet_length(default_value::text) <= 70000),
    CONSTRAINT custom_fields_version_positive CHECK (version > 0)
);

CREATE INDEX custom_fields_listing_idx
    ON openfox.custom_fields (organization_id, created_at, id)
    WHERE archived_at IS NULL;

CREATE TABLE openfox.custom_field_options (
    organization_id uuid NOT NULL,
    field_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    label text NOT NULL,
    color text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    archived_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, field_id)
        REFERENCES openfox.custom_fields (organization_id, id),
    CONSTRAINT custom_field_options_label_length CHECK (length(label) <= 100),
    CONSTRAINT custom_field_options_color_valid CHECK (color ~ '^#[0-9a-fA-F]{6}$'),
    CONSTRAINT custom_field_options_position_nonnegative CHECK (position >= 0),
    CONSTRAINT custom_field_options_version_positive CHECK (version > 0)
);

CREATE INDEX custom_field_options_field_idx
    ON openfox.custom_field_options (organization_id, field_id, position, id);

-- 字段绑定到空间的任务类型（多对多）。
CREATE TABLE openfox.task_type_fields (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_type_id uuid NOT NULL,
    field_id uuid NOT NULL,
    position integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, task_type_id, field_id),
    FOREIGN KEY (organization_id, space_id, task_type_id)
        REFERENCES openfox.task_types (organization_id, space_id, id),
    FOREIGN KEY (organization_id, field_id)
        REFERENCES openfox.custom_fields (organization_id, id),
    FOREIGN KEY (organization_id, created_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_type_fields_position_nonnegative CHECK (position >= 0)
);

CREATE INDEX task_type_fields_field_idx ON openfox.task_type_fields (organization_id, field_id);

-- 任务上的字段取值；按字段类型的取值校验在应用层完成。
CREATE TABLE openfox.task_field_values (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    field_id uuid NOT NULL,
    value jsonb NOT NULL,
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, task_id, field_id),
    FOREIGN KEY (organization_id, space_id, task_id)
        REFERENCES openfox.tasks (organization_id, space_id, id),
    FOREIGN KEY (organization_id, field_id)
        REFERENCES openfox.custom_fields (organization_id, id),
    FOREIGN KEY (organization_id, updated_by)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT task_field_values_size CHECK (octet_length(value::text) <= 70000)
);

CREATE INDEX task_field_values_field_idx ON openfox.task_field_values (organization_id, field_id);

ALTER TABLE openfox.label_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.label_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY label_groups_tenant_isolation ON openfox.label_groups
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_relation_types ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_relation_types FORCE ROW LEVEL SECURITY;
CREATE POLICY task_relation_types_tenant_isolation ON openfox.task_relation_types
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_relations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_relations FORCE ROW LEVEL SECURITY;
CREATE POLICY task_relations_tenant_isolation ON openfox.task_relations
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.custom_fields ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.custom_fields FORCE ROW LEVEL SECURITY;
CREATE POLICY custom_fields_tenant_isolation ON openfox.custom_fields
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.custom_field_options ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.custom_field_options FORCE ROW LEVEL SECURITY;
CREATE POLICY custom_field_options_tenant_isolation ON openfox.custom_field_options
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_type_fields ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_type_fields FORCE ROW LEVEL SECURITY;
CREATE POLICY task_type_fields_tenant_isolation ON openfox.task_type_fields
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.task_field_values ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_field_values FORCE ROW LEVEL SECURITY;
CREATE POLICY task_field_values_tenant_isolation ON openfox.task_field_values
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    openfox.label_groups,
    openfox.task_relation_types,
    openfox.custom_fields,
    openfox.custom_field_options,
    openfox.task_field_values
TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.task_relations, openfox.task_type_fields TO openfox_app;
