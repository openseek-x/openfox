-- 阶段 3c「专项与视图」：组织级专项（可关联多个空间）、任务所属专项、空间视图（保存的筛选与显示设置）、
-- 视图的个人偏好（置顶、排序、展示方式）、上传的图标。
--
-- 仅扩展（expand-only）：
--   - projects：组织级专项。状态 backlog（待整理）/ planned（计划中）/ in_progress / completed / canceled，
--     优先级沿用任务的取值（NONE / LOW / NORMAL / HIGH / URGENT），负责人为组织成员，图标只允许表情。
--   - project_spaces：专项关联的空间（≥ 1 个由应用保证），每个专项最多一个主空间（部分唯一索引）。
--   - project_labels：专项上的标签（引用空间标签，应用校验标签所在空间已关联到专项）。
--   - tasks.project_id：任务所属专项（应用校验专项关联了任务所在空间；删除专项时应用先清空再删除）。
--   - views：空间里保存的视图。visibility personal（只有创建者可见，由 RLS 保证）/ space（空间成员可见）。
--     filters / display 为任务列表的查询参数与显示设置（jsonb，应用用任务筛选解析器校验）。
--     图标：icon_kind icon（内置图标名）/ emoji / upload（引用 uploaded_icons）。
--   - view_member_prefs：每个成员对视图的置顶、排序位置、展示方式（RLS 本人）。
--   - uploaded_icons：上传的图标文件元数据（文件本身放在 OPENFOX_STORAGE_ROOT 下）。
--   - 写入发 project.changed / view.changed 组织事件（只带 id，不带名称）。
-- 锁：tasks 加可空列与外键（新列全为 NULL，外键校验很快，但会短暂持有 SHARE ROW EXCLUSIVE 锁）。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000035 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.projects (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    icon text,
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'planned',
    priority text NOT NULL DEFAULT 'NONE',
    lead_user_id uuid,
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, lead_user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT projects_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT projects_icon_valid CHECK (icon IS NULL OR length(icon) BETWEEN 1 AND 16),
    CONSTRAINT projects_description_length CHECK (length(description) <= 10000),
    CONSTRAINT projects_status_valid CHECK (status IN ('backlog', 'planned', 'in_progress', 'completed', 'canceled')),
    CONSTRAINT projects_priority_valid CHECK (priority IN ('NONE', 'LOW', 'NORMAL', 'HIGH', 'URGENT')),
    CONSTRAINT projects_version_positive CHECK (version > 0)
);
CREATE INDEX projects_org_created_idx ON openfox.projects (organization_id, created_at DESC, id);

CREATE TABLE openfox.project_spaces (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    space_id uuid NOT NULL,
    is_primary boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, project_id, space_id),
    FOREIGN KEY (organization_id, project_id) REFERENCES openfox.projects (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id)
);
CREATE UNIQUE INDEX project_spaces_one_primary ON openfox.project_spaces (organization_id, project_id) WHERE is_primary;
CREATE INDEX project_spaces_space_idx ON openfox.project_spaces (organization_id, space_id, project_id);

CREATE TABLE openfox.project_labels (
    organization_id uuid NOT NULL,
    project_id uuid NOT NULL,
    label_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, project_id, label_id),
    FOREIGN KEY (organization_id, project_id) REFERENCES openfox.projects (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, label_id) REFERENCES openfox.labels (organization_id, id) ON DELETE CASCADE
);

ALTER TABLE openfox.tasks ADD COLUMN project_id uuid;
ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_project_fk FOREIGN KEY (organization_id, project_id)
        REFERENCES openfox.projects (organization_id, id);
CREATE INDEX tasks_project_idx ON openfox.tasks (organization_id, project_id) WHERE project_id IS NOT NULL;

CREATE TABLE openfox.uploaded_icons (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    storage_key text NOT NULL,
    content_type text NOT NULL,
    size_bytes integer NOT NULL,
    sha256 text NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT uploaded_icons_key_unique UNIQUE (storage_key),
    CONSTRAINT uploaded_icons_type_valid CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp', 'image/gif')),
    CONSTRAINT uploaded_icons_size_valid CHECK (size_bytes BETWEEN 1 AND 1048576),
    CONSTRAINT uploaded_icons_sha256_valid CHECK (sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE openfox.views (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    icon_kind text,
    icon text,
    icon_upload_id uuid,
    visibility text NOT NULL DEFAULT 'personal',
    filters jsonb NOT NULL DEFAULT '{}'::jsonb,
    display jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, icon_upload_id) REFERENCES openfox.uploaded_icons (organization_id, id),
    CONSTRAINT views_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT views_description_length CHECK (length(description) <= 500),
    CONSTRAINT views_visibility_valid CHECK (visibility IN ('personal', 'space')),
    CONSTRAINT views_icon_valid CHECK (
        (icon_kind IS NULL AND icon IS NULL AND icon_upload_id IS NULL)
        OR (icon_kind IN ('icon', 'emoji') AND length(icon) BETWEEN 1 AND 64 AND icon_upload_id IS NULL)
        OR (icon_kind = 'upload' AND icon IS NULL AND icon_upload_id IS NOT NULL)
    ),
    CONSTRAINT views_filters_shape CHECK (jsonb_typeof(filters) = 'object' AND octet_length(filters::text) <= 16384),
    CONSTRAINT views_display_shape CHECK (jsonb_typeof(display) = 'object' AND octet_length(display::text) <= 16384),
    CONSTRAINT views_version_positive CHECK (version > 0)
);
CREATE INDEX views_space_idx ON openfox.views (organization_id, space_id, visibility, created_at, id);

CREATE TABLE openfox.view_member_prefs (
    organization_id uuid NOT NULL,
    view_id uuid NOT NULL,
    user_id uuid NOT NULL,
    pinned boolean NOT NULL DEFAULT false,
    position integer,
    view_mode text,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, view_id, user_id),
    FOREIGN KEY (organization_id, view_id) REFERENCES openfox.views (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT view_member_prefs_position_valid CHECK (position IS NULL OR (position >= 0 AND position < 10000)),
    CONSTRAINT view_member_prefs_mode_valid CHECK (view_mode IS NULL OR view_mode IN ('list', 'board', 'table'))
);
CREATE INDEX view_member_prefs_user_idx ON openfox.view_member_prefs (organization_id, user_id, view_id);

ALTER TABLE openfox.projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.projects FORCE ROW LEVEL SECURITY;
CREATE POLICY projects_tenant_isolation ON openfox.projects
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.project_spaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.project_spaces FORCE ROW LEVEL SECURITY;
CREATE POLICY project_spaces_tenant_isolation ON openfox.project_spaces
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.project_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.project_labels FORCE ROW LEVEL SECURITY;
CREATE POLICY project_labels_tenant_isolation ON openfox.project_labels
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.uploaded_icons ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.uploaded_icons FORCE ROW LEVEL SECURITY;
CREATE POLICY uploaded_icons_tenant_isolation ON openfox.uploaded_icons
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 私人视图只对创建者可见：在 RLS 层保证，避免某个读路径漏掉可见性过滤。
-- 空间可读性（私有空间）仍由应用的空间权限检查负责，与任务一致。
ALTER TABLE openfox.views ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.views FORCE ROW LEVEL SECURITY;
CREATE POLICY views_visibility ON openfox.views
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND (visibility = 'space' OR created_by = openfox_private.current_actor_id()))
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND (visibility = 'space' OR created_by = openfox_private.current_actor_id()));

ALTER TABLE openfox.view_member_prefs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.view_member_prefs FORCE ROW LEVEL SECURITY;
CREATE POLICY view_member_prefs_owner ON openfox.view_member_prefs
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND user_id = openfox_private.current_actor_id());

-- 组织事件：只带 id（私人视图的名称不进事件流）。
CREATE OR REPLACE FUNCTION openfox_private.emit_project_view_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    v_row record;
BEGIN
    v_row := CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    IF TG_TABLE_NAME = 'projects' THEN
        INSERT INTO openfox.organization_events (organization_id, type, entity, entity_id, version, payload)
        VALUES (v_row.organization_id, 'project.changed', 'project', v_row.id,
                CASE WHEN TG_OP = 'DELETE' THEN NULL ELSE v_row.version END,
                jsonb_build_object('deleted', TG_OP = 'DELETE'));
    ELSE
        INSERT INTO openfox.organization_events (organization_id, type, entity, entity_id, space_id, version, payload)
        VALUES (v_row.organization_id, 'view.changed', 'view', v_row.id, v_row.space_id,
                CASE WHEN TG_OP = 'DELETE' THEN NULL ELSE v_row.version END,
                jsonb_build_object('deleted', TG_OP = 'DELETE'));
    END IF;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_project_view_event() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_project_view_event() FROM PUBLIC;

CREATE TRIGGER projects_emit_event
AFTER INSERT OR UPDATE OR DELETE ON openfox.projects
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_project_view_event();
CREATE TRIGGER views_emit_event
AFTER INSERT OR UPDATE OR DELETE ON openfox.views
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_project_view_event();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.projects TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.project_spaces TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.project_labels TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.uploaded_icons TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.views TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.view_member_prefs TO openfox_app;
