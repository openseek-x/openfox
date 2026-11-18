-- 阶段 5c「技能」：空间技能库、技能文件、Agent 装配技能、从 Runtime 导入。
--
-- 仅扩展（expand-only）：
--   - skills：空间内的技能。名称 ^[a-z0-9][a-z0-9-]{0,63}$，同一空间内唯一；描述 ≤ 1024；来源 manual / upload / url /
--     runtime / market（market 留给 5d），source_label 为来源说明（URL 主机名或电脑名），source_url 为 URL 导入的原地址；
--     file_count / total_bytes 由应用随文件写入维护（≤ 100 个、≤ 5 MB）；乐观锁 version。
--   - skill_files：技能的文件清单。内容放在 OPENFOX_STORAGE_ROOT（file_key），这里只存相对路径、大小与 sha256。
--     路径为相对路径：不以 / 开头，不含 \、控制字符、空段、「.」或「..」段；SKILL.md 必须存在由应用保证。
--   - agent_skills：Agent 装配的技能（同一空间；每个 Agent ≤ 20 个由应用保证），不随 Agent 配置版本固定，下一次 Run 生效。
--     为了用外键保证「同一空间」，给 agents 加 (organization_id, id, space_id) 唯一索引。
--   - skill_import_items：从 Runtime 导入时每个技能一行（pending / imported / skipped / failed / canceled），
--     供界面显示进度与汇总；随设备命令删除。
--   - device_commands：kind 增加 skill_scan（扫描本机技能）与 skill_import（导入选中的技能）；
--     结果大小上限按种类：skill_scan ≤ 64 KB，其余仍 ≤ 16 KB。
--   - 组织事件：技能与装配变化发语句级 skill.changed（带 space_id）。
-- 锁：agents 建唯一索引（表很小，短暂 SHARE 锁）；device_commands 换两个 CHECK 约束需扫描表（行数受 7 天清理限制）。
-- 回滚：应用回退后新表闲置即可；确需移除时见 db/migrations/README.md 的 000042 条目。

SET ROLE openfox_migrator;

CREATE UNIQUE INDEX agents_organization_id_id_space_id_key ON openfox.agents (organization_id, id, space_id);

CREATE TABLE openfox.skills (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    source_kind text NOT NULL,
    source_label text NOT NULL DEFAULT '',
    source_url text,
    file_count integer NOT NULL DEFAULT 0,
    total_bytes bigint NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, id, space_id),
    CONSTRAINT skills_name_unique UNIQUE (organization_id, space_id, name),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, updated_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT skills_name_valid CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    CONSTRAINT skills_description_valid CHECK (char_length(description) <= 1024),
    CONSTRAINT skills_source_kind_valid CHECK (source_kind IN ('manual', 'upload', 'url', 'runtime', 'market')),
    CONSTRAINT skills_source_label_valid CHECK (char_length(source_label) <= 200),
    CONSTRAINT skills_source_url_valid CHECK (
        source_url IS NULL OR (source_url ~ '^https://' AND char_length(source_url) <= 2048)
    ),
    CONSTRAINT skills_size_valid CHECK (file_count BETWEEN 0 AND 100 AND total_bytes BETWEEN 0 AND 5242880),
    CONSTRAINT skills_version_positive CHECK (version > 0)
);
CREATE INDEX skills_space_idx ON openfox.skills (organization_id, space_id, updated_at DESC);

CREATE TABLE openfox.skill_files (
    organization_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    path text NOT NULL,
    file_key text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, skill_id, path),
    FOREIGN KEY (organization_id, skill_id) REFERENCES openfox.skills (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT skill_files_path_valid CHECK (
        char_length(path) BETWEEN 1 AND 256
        AND path !~ '^/'
        AND path !~ '[\\[:cntrl:]]'
        AND path !~ '(^|/)\.{0,2}(/|$)'
    ),
    CONSTRAINT skill_files_file_valid CHECK (
        length(file_key) BETWEEN 1 AND 256 AND size_bytes BETWEEN 0 AND 5242880 AND sha256 ~ '^[0-9a-f]{64}$'
    )
);

CREATE TABLE openfox.agent_skills (
    organization_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    skill_id uuid NOT NULL,
    space_id uuid NOT NULL,
    position integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, agent_id, skill_id),
    FOREIGN KEY (organization_id, agent_id, space_id)
        REFERENCES openfox.agents (organization_id, id, space_id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, skill_id, space_id)
        REFERENCES openfox.skills (organization_id, id, space_id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT agent_skills_position_valid CHECK (position >= 0)
);
CREATE INDEX agent_skills_skill_idx ON openfox.agent_skills (organization_id, skill_id);

CREATE TABLE openfox.skill_import_items (
    organization_id uuid NOT NULL,
    command_id uuid NOT NULL,
    space_id uuid NOT NULL,
    position integer NOT NULL,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    reason text NOT NULL DEFAULT '',
    skill_id uuid,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, command_id, name),
    FOREIGN KEY (organization_id, command_id) REFERENCES openfox.device_commands (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, skill_id) REFERENCES openfox.skills (organization_id, id) ON DELETE SET NULL (skill_id),
    CONSTRAINT skill_import_items_name_valid CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    CONSTRAINT skill_import_items_status_valid CHECK (status IN ('pending', 'imported', 'skipped', 'failed', 'canceled')),
    CONSTRAINT skill_import_items_reason_valid CHECK (reason ~ '^[A-Z0-9_]{0,64}$'),
    CONSTRAINT skill_import_items_position_valid CHECK (position BETWEEN 0 AND 19)
);

ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_kind_valid;
ALTER TABLE openfox.device_commands ADD CONSTRAINT device_commands_kind_valid CHECK (kind IN (
    'runtime_probe', 'runtime_test', 'storage_cleanup', 'skill_scan', 'skill_import'
));
ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_result_shape;
ALTER TABLE openfox.device_commands ADD CONSTRAINT device_commands_result_shape CHECK (
    result IS NULL OR (
        jsonb_typeof(result) = 'object'
        AND pg_column_size(result) <= CASE WHEN kind = 'skill_scan' THEN 65536 ELSE 16384 END
    )
);

ALTER TABLE openfox.skills ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.skills FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.skill_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.skill_files FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_skills ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.agent_skills FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.skill_import_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.skill_import_items FORCE ROW LEVEL SECURITY;

-- 空间读写权限由应用判断（与文档集、自动化相同）；这里只做租户隔离。
CREATE POLICY skills_tenant_isolation ON openfox.skills
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY skill_files_tenant_isolation ON openfox.skill_files
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY agent_skills_tenant_isolation ON openfox.agent_skills
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY skill_import_items_tenant_isolation ON openfox.skill_import_items
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 组织事件：技能与装配变化按空间广播。
CREATE OR REPLACE FUNCTION openfox_private.emit_skill_event_new()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id)
    SELECT DISTINCT c.organization_id, 'skill.changed', 'skill', c.space_id FROM changed_new c;
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION openfox_private.emit_skill_event_old()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id)
    SELECT DISTINCT c.organization_id, 'skill.changed', 'skill', c.space_id FROM changed_old c;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_skill_event_new() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_skill_event_old() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_skill_event_new() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_skill_event_old() FROM PUBLIC;

CREATE TRIGGER skills_emit_insert AFTER INSERT ON openfox.skills
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_new();
CREATE TRIGGER skills_emit_update AFTER UPDATE ON openfox.skills
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_new();
CREATE TRIGGER skills_emit_delete AFTER DELETE ON openfox.skills
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_old();
CREATE TRIGGER agent_skills_emit_insert AFTER INSERT ON openfox.agent_skills
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_new();
CREATE TRIGGER agent_skills_emit_delete AFTER DELETE ON openfox.agent_skills
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_skill_event_old();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.skills TO openfox_app;
GRANT SELECT, INSERT, DELETE ON openfox.skill_files TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.agent_skills TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.skill_import_items TO openfox_app;
