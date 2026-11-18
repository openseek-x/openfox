-- 阶段 11f Agent 分身（M-09）：expand-only，没有 down 迁移。
-- 1. agents.parent_agent_id：分身指向同组织内的父 Agent（复合外键，没有 ON DELETE 级联；Agent 只归档不物理删除）。
--    创建后不可修改（openfox_app 没有该列的 UPDATE 权限），所以「父 Agent 不能是分身」只需在写入时校验一次。
-- 2. agents.inherited_groups：分身当前仍继承父级的配置组。取值由 CHECK 限定为 8 个组；非分身必须为空。
--    openfox_app 可以更新该列（分身覆盖某组配置、恢复继承、父级传播时维护）。
-- 3. 部分索引 (organization_id, parent_agent_id) WHERE parent_agent_id IS NOT NULL：父级按 id 顺序加锁分身、列出分身。
-- 4. 触发器 agents_variant_parent_valid：父 Agent 必须同组织、自身不是分身、不是内置 Agent；分身自己不能是内置 Agent。
--    同时锁住父行并计数，保证每个父 Agent 最多 20 个分身（含已归档）。SECURITY DEFINER；INSERT 时触发。
-- 5. 重建 agent_skill_loadout：分身的技能清单来自 COALESCE(parent_agent_id, id)，space_id 仍是查看者所在空间，
--    这样设置页和派发按分身 id 读取时拿到父级技能快照（含跨空间）。列名与顺序不变。
-- 不改写已有行：新列对旧行为 NULL / 空数组；不需要回填。
-- 锁与影响：ADD COLUMN（NULL / 常量默认值）不重写表；ADD CONSTRAINT CHECK 扫描一次 agents；
--          外键验证扫描一次 agents；CREATE INDEX 在迁移事务内（无 CONCURRENTLY）会短暂阻塞 agents 写入。agents 是控制面小表。
-- 回滚：应用回退后新列闲置即可；确需移除时（不建议）先确认没有分身再 DROP 触发器、索引、约束和列。
--
-- 验证查询（迁移后）：
--   SELECT column_name, data_type, is_nullable FROM information_schema.columns
--    WHERE table_schema='openfox' AND table_name='agents' AND column_name IN ('parent_agent_id','inherited_groups');
--     -- 期望 2 行：uuid / YES，ARRAY / NO
--   SELECT conname FROM pg_constraint WHERE conrelid='openfox.agents'::regclass AND (conname LIKE 'agents_variant%' OR conname='agents_parent_fk');
--     -- 期望 agents_parent_fk、agents_variant_groups_valid、agents_variant_shape_valid
--   SELECT count(*) FROM openfox.agents WHERE parent_agent_id IS NOT NULL OR inherited_groups <> '{}';
--     -- 迁移当下期望 0
--   SELECT has_column_privilege('openfox_app','openfox.agents','parent_agent_id','UPDATE'),
--          has_column_privilege('openfox_app','openfox.agents','inherited_groups','UPDATE');
--     -- 期望 false, true

SET ROLE openfox_migrator;

ALTER TABLE openfox.agents
    ADD COLUMN parent_agent_id uuid,
    ADD COLUMN inherited_groups text[] NOT NULL DEFAULT '{}';

ALTER TABLE openfox.agents
    ADD CONSTRAINT agents_parent_fk
        FOREIGN KEY (organization_id, parent_agent_id) REFERENCES openfox.agents (organization_id, id),
    ADD CONSTRAINT agents_variant_groups_valid
        CHECK (inherited_groups <@ ARRAY['instructions', 'computer', 'runtime', 'directory', 'limits', 'repos', 'env', 'run_settings']::text[]),
    ADD CONSTRAINT agents_variant_shape_valid
        CHECK ((parent_agent_id IS NULL AND inherited_groups = '{}')
               OR (parent_agent_id IS NOT NULL AND parent_agent_id <> id AND builtin_kind IS NULL));

CREATE INDEX agents_parent_idx
    ON openfox.agents (organization_id, parent_agent_id)
    WHERE parent_agent_id IS NOT NULL;

CREATE OR REPLACE FUNCTION openfox_private.validate_agent_variant_parent()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    parent_parent uuid;
    parent_builtin text;
    parent_archived timestamptz;
BEGIN
    IF NEW.parent_agent_id IS NULL THEN
        RETURN NEW;
    END IF;
    -- 锁父行，与应用层「先锁父再锁分身」一致，并发创建不会超过 20。
    SELECT a.parent_agent_id, a.builtin_kind, a.archived_at
      INTO parent_parent, parent_builtin, parent_archived
      FROM openfox.agents AS a
     WHERE a.organization_id = NEW.organization_id
       AND a.id = NEW.parent_agent_id
       FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'variant parent is not available' USING ERRCODE = '23514';
    END IF;
    IF parent_parent IS NOT NULL OR parent_builtin IS NOT NULL OR parent_archived IS NOT NULL THEN
        RAISE EXCEPTION 'a variant cannot have a variant or built-in Agent as its parent' USING ERRCODE = '23514';
    END IF;
    IF (SELECT count(*) FROM openfox.agents
         WHERE organization_id = NEW.organization_id AND parent_agent_id = NEW.parent_agent_id) >= 20 THEN
        RAISE EXCEPTION 'an Agent can have at most 20 variants' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_agent_variant_parent()
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_agent_variant_parent() FROM PUBLIC;

CREATE TRIGGER agents_variant_parent_valid
BEFORE INSERT ON openfox.agents
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_agent_variant_parent();


-- 技能清单跟随父级。CREATE OR REPLACE 不能改列名或顺序；security_invoker 保持调用者的租户策略。
CREATE OR REPLACE VIEW openfox.agent_skill_loadout WITH (security_invoker = true) AS
SELECT v.organization_id,
       v.id AS agent_id,
       v.space_id,
       src.position,
       src.attached_at,
       src.id,
       src.name,
       src.description,
       src.source_kind,
       src.source_label,
       src.source_url,
       src.file_count,
       src.total_bytes,
       src.created_by,
       src.version,
       src.created_at,
       src.updated_at,
       src.market_skill_id,
       src.market_version,
       src.agent_owned
  FROM openfox.agents v
  JOIN (
        SELECT a.organization_id, a.agent_id AS source_agent_id, a.position, a.created_at AS attached_at,
               s.id, s.name, s.description, s.source_kind, s.source_label, s.source_url, s.file_count, s.total_bytes,
               s.created_by, s.version, s.created_at, s.updated_at, s.market_skill_id, s.market_version, false AS agent_owned
          FROM openfox.agent_skills a
          JOIN openfox.skills s ON s.organization_id = a.organization_id AND s.id = a.skill_id
        UNION ALL
        SELECT i.organization_id, i.agent_id, i.position, i.created_at,
               i.id, i.name, i.description, 'market', i.source_label, NULL::text,
               jsonb_array_length(i.files), i.total_bytes,
               i.created_by, 1::bigint, i.created_at, i.created_at, i.market_skill_id, i.market_version, true
          FROM openfox.agent_market_skill_installs i
       ) src
    ON src.organization_id = v.organization_id
   AND src.source_agent_id = COALESCE(v.parent_agent_id, v.id);

-- 归档先锁容量及相关实体电脑，再锁父/子 Agent 并取消 Run；避免容量→设备→Agent 反向死锁。
CREATE OR REPLACE FUNCTION openfox_private.lock_variant_archive_capacity(p_org uuid, p_agent uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    device_row record;
BEGIN
    IF p_org IS NULL OR p_agent IS NULL OR p_org <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'invalid variant archive scope' USING ERRCODE = '42501';
    END IF;
    -- 缺少容量配置时仍允许归档无预留的 Agent；存在容量行则先锁定，维持取消锁序。
    PERFORM 1 FROM openfox.organization_capacity WHERE organization_id=p_org FOR UPDATE;
    FOR device_row IN
        SELECT d.id FROM openfox.physical_devices d
         WHERE d.id IN (
             SELECT r.computer_id FROM openfox.runs r
              WHERE r.organization_id=p_org AND r.state IN ('QUEUED','PREPARING','RUNNING')
                AND (r.agent_id=p_agent OR r.agent_id IN
                    (SELECT a.id FROM openfox.agents a WHERE a.organization_id=p_org AND a.parent_agent_id=p_agent))
         ) ORDER BY d.id FOR UPDATE
    LOOP
        NULL;
    END LOOP;
END
$$;
ALTER FUNCTION openfox_private.lock_variant_archive_capacity(uuid, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.lock_variant_archive_capacity(uuid, uuid) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.lock_variant_archive_capacity(uuid, uuid) TO openfox_app;

-- 表级 INSERT 已覆盖新列；parent_agent_id 创建后不可改，只放开 inherited_groups 的 UPDATE。
GRANT UPDATE (inherited_groups) ON openfox.agents TO openfox_app;
