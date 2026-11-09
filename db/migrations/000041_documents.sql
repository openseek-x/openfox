-- 阶段 5b「文档库」：文档集、文档树（我的文档 / 文档集）、附件。
--
-- 仅扩展（expand-only）：
--   - doc_sets：空间内的文档集（团队文档）。名称 ≤ 64，图标与视图图标同一结构，position 排序，乐观锁 version。
--   - doc_nodes：文档树节点。容器二选一：set_id（文档集，空间成员按空间权限读写）或 owner_user_id（我的文档，只有本人可见）。
--     kind：folder（文件夹）/ doc（Markdown 正文）/ sheet（CSV 正文）/ file（上传的文件，存 OPENFOX_STORAGE_ROOT，
--     ≤ 20 MB，与 artifacts 存储边界的单文件上限一致；file_ext 保存原扩展名，供下载时还原文件名）。
--     parent_id 指向同一容器内的父节点（应用保证；移动时连同子树一起改容器），删除父节点级联删除子树。
--   - doc_attachments：编辑器里插入的图片与附件（≤ 20 MB，同一存储上限），属于某个文档节点，随节点删除。
--   - RLS：除租户隔离外，owner_user_id 不为空的节点（我的文档）只对本人可见；附件随所属节点可见。
--   - 组织事件 doc.changed：文档集节点按空间广播（space_id）；我的文档只发给本人（recipient_user_id）。
-- 锁：只建新表。
-- 回滚：应用回退后新表闲置即可；确需移除时见 db/migrations/README.md 的 000041 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.doc_sets (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    name text NOT NULL,
    icon_kind text,
    icon text,
    icon_upload_id uuid,
    position integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, id, space_id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, icon_upload_id) REFERENCES openfox.uploaded_icons (organization_id, id),
    CONSTRAINT doc_sets_name_valid CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
    CONSTRAINT doc_sets_icon_valid CHECK (
        (icon_kind IS NULL AND icon IS NULL AND icon_upload_id IS NULL)
        OR (icon_kind IN ('icon', 'emoji') AND length(icon) BETWEEN 1 AND 64 AND icon_upload_id IS NULL)
        OR (icon_kind = 'upload' AND icon IS NULL AND icon_upload_id IS NOT NULL)
    ),
    CONSTRAINT doc_sets_position_valid CHECK (position >= 0),
    CONSTRAINT doc_sets_version_positive CHECK (version > 0)
);
CREATE INDEX doc_sets_space_idx ON openfox.doc_sets (organization_id, space_id, position, created_at);

CREATE TABLE openfox.doc_nodes (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    set_id uuid,
    owner_user_id uuid,
    parent_id uuid,
    kind text NOT NULL,
    title text NOT NULL,
    body text,
    file_key text,
    file_ext text,
    content_type text,
    size_bytes bigint,
    sha256 text,
    position integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, set_id, space_id) REFERENCES openfox.doc_sets (organization_id, id, space_id)
        ON DELETE CASCADE,
    FOREIGN KEY (organization_id, owner_user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, parent_id) REFERENCES openfox.doc_nodes (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, updated_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT doc_nodes_container_valid CHECK ((set_id IS NULL) <> (owner_user_id IS NULL)),
    CONSTRAINT doc_nodes_parent_not_self CHECK (parent_id IS NULL OR parent_id <> id),
    CONSTRAINT doc_nodes_kind_valid CHECK (kind IN ('folder', 'doc', 'sheet', 'file')),
    CONSTRAINT doc_nodes_title_valid CHECK (char_length(title) BETWEEN 0 AND 200),
    CONSTRAINT doc_nodes_body_valid CHECK (
        (kind IN ('doc', 'sheet') AND body IS NOT NULL AND octet_length(body) <= 2097152)
        OR (kind IN ('folder', 'file') AND body IS NULL)
    ),
    CONSTRAINT doc_nodes_file_valid CHECK (
        (kind = 'file' AND file_key IS NOT NULL AND length(file_key) BETWEEN 1 AND 256
            AND length(content_type) BETWEEN 1 AND 128 AND size_bytes BETWEEN 0 AND 20971520
            AND sha256 ~ '^[0-9a-f]{64}$' AND (file_ext IS NULL OR file_ext ~ '^[a-z0-9]{1,16}$'))
        OR (kind <> 'file' AND file_key IS NULL AND file_ext IS NULL AND content_type IS NULL
            AND size_bytes IS NULL AND sha256 IS NULL)
    ),
    CONSTRAINT doc_nodes_position_valid CHECK (position >= 0),
    CONSTRAINT doc_nodes_version_positive CHECK (version > 0)
);
CREATE INDEX doc_nodes_set_tree_idx ON openfox.doc_nodes (organization_id, set_id, parent_id, position) WHERE set_id IS NOT NULL;
CREATE INDEX doc_nodes_personal_tree_idx ON openfox.doc_nodes (organization_id, space_id, owner_user_id, parent_id, position)
    WHERE owner_user_id IS NOT NULL;
CREATE INDEX doc_nodes_parent_idx ON openfox.doc_nodes (organization_id, parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE openfox.doc_attachments (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL,
    name text NOT NULL,
    file_key text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, node_id) REFERENCES openfox.doc_nodes (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT doc_attachments_name_valid CHECK (char_length(btrim(name)) BETWEEN 1 AND 255),
    CONSTRAINT doc_attachments_file_valid CHECK (
        length(file_key) BETWEEN 1 AND 256 AND length(content_type) BETWEEN 1 AND 128
        AND size_bytes BETWEEN 0 AND 20971520 AND sha256 ~ '^[0-9a-f]{64}$'
    )
);
CREATE INDEX doc_attachments_node_idx ON openfox.doc_attachments (organization_id, node_id);

ALTER TABLE openfox.doc_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.doc_sets FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.doc_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.doc_nodes FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.doc_attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.doc_attachments FORCE ROW LEVEL SECURITY;

CREATE POLICY doc_sets_tenant_isolation ON openfox.doc_sets
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 我的文档只对本人可见（读写都限制）；文档集节点按租户隔离，空间权限由应用判断。
CREATE POLICY doc_nodes_tenant_isolation ON openfox.doc_nodes
    USING (organization_id = openfox_private.current_organization_id()
           AND (owner_user_id IS NULL OR owner_user_id = openfox_private.current_actor_id()))
    WITH CHECK (organization_id = openfox_private.current_organization_id()
           AND (owner_user_id IS NULL OR owner_user_id = openfox_private.current_actor_id()));
CREATE POLICY doc_attachments_tenant_isolation ON openfox.doc_attachments
    USING (organization_id = openfox_private.current_organization_id()
           AND EXISTS (SELECT 1 FROM openfox.doc_nodes n
                        WHERE n.organization_id = doc_attachments.organization_id AND n.id = doc_attachments.node_id))
    WITH CHECK (organization_id = openfox_private.current_organization_id()
           AND EXISTS (SELECT 1 FROM openfox.doc_nodes n
                        WHERE n.organization_id = doc_attachments.organization_id AND n.id = doc_attachments.node_id));

-- 组织事件：文档集节点与文档集按空间广播；我的文档只发给本人。
CREATE OR REPLACE FUNCTION openfox_private.emit_doc_event_new()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF TG_TABLE_NAME = 'doc_sets' THEN
        INSERT INTO openfox.organization_events (organization_id, type, entity, space_id)
        SELECT DISTINCT c.organization_id, 'doc.changed', 'doc', c.space_id FROM changed_new c;
    ELSE
        INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, recipient_user_id)
        SELECT DISTINCT c.organization_id, 'doc.changed', 'doc', c.space_id, c.owner_user_id FROM changed_new c;
    END IF;
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION openfox_private.emit_doc_event_old()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF TG_TABLE_NAME = 'doc_sets' THEN
        INSERT INTO openfox.organization_events (organization_id, type, entity, space_id)
        SELECT DISTINCT c.organization_id, 'doc.changed', 'doc', c.space_id FROM changed_old c;
    ELSE
        INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, recipient_user_id)
        SELECT DISTINCT c.organization_id, 'doc.changed', 'doc', c.space_id, c.owner_user_id FROM changed_old c;
    END IF;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_doc_event_new() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_doc_event_old() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_doc_event_new() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_doc_event_old() FROM PUBLIC;

CREATE TRIGGER doc_sets_emit_insert AFTER INSERT ON openfox.doc_sets
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_doc_event_new();
CREATE TRIGGER doc_sets_emit_update AFTER UPDATE ON openfox.doc_sets
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_doc_event_new();
CREATE TRIGGER doc_sets_emit_delete AFTER DELETE ON openfox.doc_sets
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_doc_event_old();
CREATE TRIGGER doc_nodes_emit_insert AFTER INSERT ON openfox.doc_nodes
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_doc_event_new();
CREATE TRIGGER doc_nodes_emit_update AFTER UPDATE ON openfox.doc_nodes
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_doc_event_new();
CREATE TRIGGER doc_nodes_emit_delete AFTER DELETE ON openfox.doc_nodes
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_doc_event_old();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.doc_sets TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.doc_nodes TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.doc_attachments TO openfox_app;
