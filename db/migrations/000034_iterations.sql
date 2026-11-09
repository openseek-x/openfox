-- 阶段 3b「迭代与调度器」：空间迭代设置、迭代表、任务所属迭代、迭代文档链接、调度候选查询。
--
-- 仅扩展（expand-only）：
--   - spaces：迭代设置——模式 manual（Jira）/ automatic（Linear）、时长 1–8 周、冷却 0–3 周、
--     起始星期、时区、自动创建的未来迭代数（1,2,3,4,6,8,10,12,15）、按状态自动加入（已开始 / 已完成）、
--     下一个迭代编号与自动模式下一个迭代的开始日期。是否启用沿用 000033 的 iterations_enabled。
--   - iterations：每个空间的迭代。status: planned → active → completed，或 canceled；同一空间最多一个 active
--     （部分唯一索引）。starts_at / ends_at 由应用按空间时区的「起始日 00:01」换算后写入；完成时把范围与完成数
--     （任务数、点数）定格在行上，供列表显示「完成率」。
--   - tasks.iteration_id：任务所属迭代（同空间，应用层校验）。
--   - iteration_links：迭代详情里的「文档或链接」（≤ 20 条 / 迭代）。
--   - openfox_private.list_iteration_due_organizations：派发 worker 每分钟调用，跨组织列出需要调度的组织
--     （自动模式空间里：活跃迭代已到结束时间、已计划迭代已到开始时间、或未来迭代数少于设定值）。
--     只授权给 openfox_context，与 list_dispatch_organizations 同一做法；具体处理在组织 worker 上下文里进行。
--   - iterations 写入触发 iteration.changed 组织事件。
-- 锁：spaces / tasks 加列为可空或常量默认值，不重写表。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000034 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.spaces
    ADD COLUMN iteration_mode text NOT NULL DEFAULT 'automatic',
    ADD COLUMN iteration_duration_weeks integer NOT NULL DEFAULT 2,
    ADD COLUMN iteration_cooldown_weeks integer NOT NULL DEFAULT 0,
    ADD COLUMN iteration_start_weekday integer NOT NULL DEFAULT 1,
    ADD COLUMN iteration_timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    ADD COLUMN iteration_upcoming_count integer NOT NULL DEFAULT 2,
    ADD COLUMN iteration_auto_add_started boolean NOT NULL DEFAULT false,
    ADD COLUMN iteration_auto_add_completed boolean NOT NULL DEFAULT false,
    ADD COLUMN iteration_next_start_date date,
    ADD COLUMN next_iteration_number integer NOT NULL DEFAULT 1;

ALTER TABLE openfox.spaces
    ADD CONSTRAINT spaces_iteration_mode_valid CHECK (iteration_mode IN ('manual', 'automatic')),
    ADD CONSTRAINT spaces_iteration_duration_valid CHECK (iteration_duration_weeks BETWEEN 1 AND 8),
    ADD CONSTRAINT spaces_iteration_cooldown_valid CHECK (iteration_cooldown_weeks BETWEEN 0 AND 3),
    ADD CONSTRAINT spaces_iteration_weekday_valid CHECK (iteration_start_weekday BETWEEN 0 AND 6),
    ADD CONSTRAINT spaces_iteration_timezone_valid CHECK (length(iteration_timezone) BETWEEN 1 AND 64),
    ADD CONSTRAINT spaces_iteration_upcoming_valid CHECK (iteration_upcoming_count IN (1, 2, 3, 4, 6, 8, 10, 12, 15)),
    ADD CONSTRAINT spaces_next_iteration_number_positive CHECK (next_iteration_number > 0);

CREATE TABLE openfox.iterations (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    number integer NOT NULL,
    name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'planned',
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    canceled_at timestamptz,
    scope_count integer,
    completed_count integer,
    scope_points numeric(16, 2),
    completed_points numeric(16, 2),
    created_by uuid,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, space_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT iterations_number_positive CHECK (number > 0),
    CONSTRAINT iterations_name_length CHECK (length(name) <= 80),
    CONSTRAINT iterations_description_length CHECK (length(description) <= 4000),
    CONSTRAINT iterations_status_valid CHECK (status IN ('planned', 'active', 'completed', 'canceled')),
    CONSTRAINT iterations_range_valid CHECK (ends_at > starts_at),
    CONSTRAINT iterations_status_times_valid CHECK (
        (status = 'planned' AND started_at IS NULL AND completed_at IS NULL AND canceled_at IS NULL)
        OR (status = 'active' AND started_at IS NOT NULL AND completed_at IS NULL AND canceled_at IS NULL)
        OR (status = 'completed' AND started_at IS NOT NULL AND completed_at IS NOT NULL AND canceled_at IS NULL)
        OR (status = 'canceled' AND completed_at IS NULL AND canceled_at IS NOT NULL)
    ),
    CONSTRAINT iterations_counts_valid CHECK (
        (scope_count IS NULL OR scope_count >= 0) AND (completed_count IS NULL OR completed_count >= 0)
        AND (scope_points IS NULL OR scope_points >= 0) AND (completed_points IS NULL OR completed_points >= 0)
    ),
    CONSTRAINT iterations_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX iterations_space_number_unique ON openfox.iterations (organization_id, space_id, number);
CREATE UNIQUE INDEX iterations_one_active ON openfox.iterations (organization_id, space_id) WHERE status = 'active';
CREATE INDEX iterations_space_status_idx ON openfox.iterations (organization_id, space_id, status, starts_at);
-- 调度器按时间扫描：只看未结束的迭代。
CREATE INDEX iterations_due_idx ON openfox.iterations (starts_at, ends_at) WHERE status IN ('planned', 'active');

ALTER TABLE openfox.tasks ADD COLUMN iteration_id uuid;
ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_iteration_fk FOREIGN KEY (organization_id, space_id, iteration_id)
        REFERENCES openfox.iterations (organization_id, space_id, id);
CREATE INDEX tasks_iteration_idx ON openfox.tasks (organization_id, iteration_id) WHERE iteration_id IS NOT NULL;

CREATE TABLE openfox.iteration_links (
    organization_id uuid NOT NULL,
    iteration_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    title text NOT NULL DEFAULT '',
    url text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, iteration_id) REFERENCES openfox.iterations (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT iteration_links_title_length CHECK (length(title) <= 200),
    CONSTRAINT iteration_links_url_valid CHECK (length(url) <= 2048 AND url ~ '^https?://[^\s]+$'),
    CONSTRAINT iteration_links_position_valid CHECK (position >= 0 AND position < 20)
);
CREATE INDEX iteration_links_iteration_idx ON openfox.iteration_links (organization_id, iteration_id, position);

ALTER TABLE openfox.iterations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.iterations FORCE ROW LEVEL SECURITY;
CREATE POLICY iterations_tenant_isolation ON openfox.iterations
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.iteration_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.iteration_links FORCE ROW LEVEL SECURITY;
CREATE POLICY iteration_links_tenant_isolation ON openfox.iteration_links
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 调度候选扫描在无组织上下文中运行（definer = migrator），spaces / iterations 都是 FORCE RLS，
-- 沿用 000019 的 *_dispatch_scan 模式给 migrator 只读策略；不扩大 openfox_app 的可见范围。
CREATE POLICY spaces_iteration_scan ON openfox.spaces
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY iterations_iteration_scan ON openfox.iterations
    FOR SELECT TO openfox_migrator USING (true);

-- 调度候选：跨组织列出需要处理的组织（自动模式、启用迭代、未停用未删除的空间）。
-- 条件（任一）：活跃迭代已到结束时间；已计划迭代已到开始时间且没有活跃迭代；未来（planned）迭代数少于设定值。
-- 按组织 id 游标分页，每次最多 p_limit 个；只返回组织 id，不返回业务数据。
CREATE OR REPLACE FUNCTION openfox_private.list_iteration_due_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid iteration scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT DISTINCT s.organization_id
      FROM openfox.spaces s
     WHERE s.iterations_enabled
       AND s.iteration_mode = 'automatic'
       AND s.status = 'ACTIVE'
       AND s.deleted_at IS NULL
       AND (p_after IS NULL OR s.organization_id > p_after)
       AND (
           EXISTS (SELECT 1 FROM openfox.iterations i
                    WHERE i.organization_id = s.organization_id AND i.space_id = s.id
                      AND i.status = 'active' AND i.ends_at <= clock_timestamp())
           OR (NOT EXISTS (SELECT 1 FROM openfox.iterations i
                            WHERE i.organization_id = s.organization_id AND i.space_id = s.id AND i.status = 'active')
               AND EXISTS (SELECT 1 FROM openfox.iterations i
                            WHERE i.organization_id = s.organization_id AND i.space_id = s.id
                              AND i.status = 'planned' AND i.starts_at <= clock_timestamp()))
           OR (SELECT count(*) FROM openfox.iterations i
                WHERE i.organization_id = s.organization_id AND i.space_id = s.id AND i.status = 'planned')
              < s.iteration_upcoming_count
       )
     ORDER BY s.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_iteration_due_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_iteration_due_organizations(uuid, integer) FROM PUBLIC;

-- 组织事件：迭代创建 / 状态与日期变化。
CREATE OR REPLACE FUNCTION openfox_private.emit_iteration_events()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
    SELECT c.organization_id, 'iteration.changed', 'iteration', c.space_id,
           jsonb_build_object('iteration_ids', to_jsonb((array_agg(c.id ORDER BY c.id))[1:100]), 'count', count(*))
      FROM changed c
     GROUP BY c.organization_id, c.space_id;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_iteration_events() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_iteration_events() FROM PUBLIC;

CREATE TRIGGER iterations_emit_events_insert
AFTER INSERT ON openfox.iterations
REFERENCING NEW TABLE AS changed
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_iteration_events();
CREATE TRIGGER iterations_emit_events_update
AFTER UPDATE ON openfox.iterations
REFERENCING NEW TABLE AS changed
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_iteration_events();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.iterations TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.iteration_links TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_iteration_due_organizations(uuid, integer) TO openfox_context;
