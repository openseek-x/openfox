-- 阶段 3d「分拣台」：空间分拣设置、分拣责任成员、分拣规则、任务的分拣状态（进入时间、来源、稍后处理、重复目标）。
--
-- 仅扩展（expand-only）：
--   - spaces：triage_require_priority（接受前必须设置优先级）、triage_action（none / notify / assign：新任务进入分拣台时
--     通知或指派给责任成员）。是否启用沿用 000033 的 triage_enabled。
--   - triage_members：分拣责任成员（通知 / 指派对象，≤ 20 人由应用保证）。
--   - triage_rules：分拣规则。conditions / actions 为有大小上限的 jsonb（应用校验结构）；position 决定执行顺序；
--     任务进入分拣台时，在同一事务里按 position 执行所有命中的启用规则，同一字段以后执行的为准。
--   - tasks：triage_entered_at（进入分拣台时间，排序用）、triage_source（member / guest / agent / integration，
--     规则条件「来源」）、triage_snoozed_until（稍后处理到期前不在队列默认视图里显示，读时计算，无需调度器）、
--     duplicate_of_task_id（标记重复的目标任务，同组织）。
--   - notifications.kind 增加 TRIAGE_NEW（新任务进入分拣台时通知责任成员）。
--   - 分拣设置、规则写入发 space.changed / triage.changed 组织事件。
-- 锁：tasks / spaces 加可空列或常量默认值，不重写表；tasks 的自引用外键在新列全为 NULL 时校验很快，
--   但会短暂持有 SHARE ROW EXCLUSIVE 锁；notifications 换 CHECK 约束需扫描表（与 000027 相同做法）。
-- 回滚：应用回退后新列、新表闲置即可；确需移除时见 db/migrations/README.md 的 000036 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.spaces
    ADD COLUMN triage_require_priority boolean NOT NULL DEFAULT false,
    ADD COLUMN triage_action text NOT NULL DEFAULT 'none';
ALTER TABLE openfox.spaces
    ADD CONSTRAINT spaces_triage_action_valid CHECK (triage_action IN ('none', 'notify', 'assign'));

CREATE TABLE openfox.triage_members (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, space_id, user_id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, user_id) REFERENCES openfox.organization_members (organization_id, user_id)
);

CREATE TABLE openfox.triage_rules (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL,
    position integer NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    conditions jsonb NOT NULL DEFAULT '[]'::jsonb,
    actions jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_used_at timestamptz,
    created_by uuid NOT NULL,
    updated_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, updated_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT triage_rules_name_valid CHECK (length(btrim(name)) BETWEEN 1 AND 80),
    CONSTRAINT triage_rules_position_valid CHECK (position >= 0 AND position < 1000),
    CONSTRAINT triage_rules_conditions_shape CHECK (jsonb_typeof(conditions) = 'array' AND octet_length(conditions::text) <= 8192),
    CONSTRAINT triage_rules_actions_shape CHECK (
        jsonb_typeof(actions) = 'array' AND jsonb_array_length(actions) >= 1 AND octet_length(actions::text) <= 8192
    ),
    CONSTRAINT triage_rules_version_positive CHECK (version > 0)
);
-- 重排时先整体挪到临时区间再写回，所以 position 唯一不做成约束；执行顺序用 (position, created_at, id)。
CREATE INDEX triage_rules_space_idx ON openfox.triage_rules (organization_id, space_id, position, created_at, id);

ALTER TABLE openfox.tasks
    ADD COLUMN triage_entered_at timestamptz,
    ADD COLUMN triage_source text,
    ADD COLUMN triage_snoozed_until timestamptz,
    ADD COLUMN duplicate_of_task_id uuid;
ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_triage_source_valid
        CHECK (triage_source IS NULL OR triage_source IN ('member', 'guest', 'agent', 'integration')),
    ADD CONSTRAINT tasks_duplicate_not_self CHECK (duplicate_of_task_id IS NULL OR duplicate_of_task_id <> id),
    ADD CONSTRAINT tasks_duplicate_of_fk FOREIGN KEY (organization_id, duplicate_of_task_id)
        REFERENCES openfox.tasks (organization_id, id);
-- 分拣队列：按空间 + 进入时间读取（只索引在分拣里的任务）。
CREATE INDEX tasks_triage_queue_idx ON openfox.tasks (organization_id, space_id, triage_entered_at, id)
    WHERE triage_entered_at IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE openfox.notifications
    DROP CONSTRAINT notifications_kind_valid,
    ADD CONSTRAINT notifications_kind_valid CHECK (
        kind IN ('MENTION', 'HANDOFF', 'HANDOFF_REVIEW', 'REPLY', 'TASK_COMMENT', 'RUN_FAILED', 'TASK_ASSIGNED',
                 'TRIAGE_NEW')
    );

ALTER TABLE openfox.triage_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.triage_members FORCE ROW LEVEL SECURITY;
CREATE POLICY triage_members_tenant_isolation ON openfox.triage_members
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

ALTER TABLE openfox.triage_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.triage_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY triage_rules_tenant_isolation ON openfox.triage_rules
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 分拣设置变化沿用 space.changed（000033 的行级触发器按列清单触发，这里补上新列）。
DROP TRIGGER spaces_emit_event_update ON openfox.spaces;
CREATE TRIGGER spaces_emit_event_update
AFTER UPDATE OF name, visibility, status, prefix, icon, purpose, views_enabled, projects_enabled,
    iterations_enabled, triage_enabled, multi_assignee, estimation_scale, estimation_extended,
    estimation_allow_zero, estimation_count_unestimated, retired_at, deleted_at,
    triage_require_priority, triage_action
ON openfox.spaces
FOR EACH ROW WHEN (OLD IS DISTINCT FROM NEW)
EXECUTE FUNCTION openfox_private.emit_space_event();

-- 规则与责任成员变化：triage.changed（只带空间 id）。语句级：一次重排 / 批量改成员只发一条。
-- INSERT 只有新过渡表、DELETE 只有旧过渡表，所以按操作分三个函数。
CREATE OR REPLACE FUNCTION openfox_private.emit_triage_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
    SELECT DISTINCT c.organization_id, 'triage.changed', 'space', c.space_id, '{}'::jsonb
      FROM (SELECT organization_id, space_id FROM changed_new
            UNION SELECT organization_id, space_id FROM changed_old) c;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_triage_event() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_triage_event() FROM PUBLIC;

CREATE OR REPLACE FUNCTION openfox_private.emit_triage_event_ins()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
    SELECT DISTINCT c.organization_id, 'triage.changed', 'space', c.space_id, '{}'::jsonb FROM changed_new c;
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION openfox_private.emit_triage_event_del()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
    SELECT DISTINCT c.organization_id, 'triage.changed', 'space', c.space_id, '{}'::jsonb FROM changed_old c;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_triage_event_ins() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_triage_event_del() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_triage_event_ins() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_triage_event_del() FROM PUBLIC;

CREATE TRIGGER triage_rules_emit_insert AFTER INSERT ON openfox.triage_rules
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_triage_event_ins();
CREATE TRIGGER triage_rules_emit_update AFTER UPDATE ON openfox.triage_rules
REFERENCING OLD TABLE AS changed_old NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_triage_event();
CREATE TRIGGER triage_rules_emit_delete AFTER DELETE ON openfox.triage_rules
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_triage_event_del();
CREATE TRIGGER triage_members_emit_insert AFTER INSERT ON openfox.triage_members
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_triage_event_ins();
CREATE TRIGGER triage_members_emit_delete AFTER DELETE ON openfox.triage_members
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_triage_event_del();

RESET ROLE;

GRANT SELECT, INSERT, DELETE ON openfox.triage_members TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.triage_rules TO openfox_app;
