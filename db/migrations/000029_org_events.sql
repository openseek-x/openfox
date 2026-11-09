-- 阶段 2a「统一实时事件流」：组织事件表 organization_events，run_events 增加提交水位列。
--
-- 投递语义（解决 stream_seq CACHE 100 + 提交顺序与取号顺序不一致导致的漏发）：
--   - 每行记录写入事务的 txid（xid8，默认 pg_current_xact_id()）。
--   - 推送端只发送 txid < pg_snapshot_xmin(pg_current_snapshot()) 的行：这些事务在快照时刻已全部结束，
--     之后不会再出现 txid 更小的可见行；游标取 (txid, id)，严格单调，不漏发、不重发。
--   - 长事务会推迟推送（推迟到它结束），不会丢事件。
-- 仅扩展（expand-only），不改写已有数据：
--   - run_events.txid 先加可空列、再单独设置默认值，避免表重写；已有行 txid 为空，
--     按 COALESCE(txid, '0') 排在所有新行之前（它们早已提交），旧的纯数字游标继续兼容。
--   - 事件由触发器在业务写入的同一事务里写入：tasks、notifications（语句级合并）、runs 的 state、
--     conversations 的关键字段、messages 的新增。触发器为 definer，写入额外依赖 migrator 插入策略。
--   - organization_events 为新表，FORCE RLS：租户隔离 + 定向事件（recipient_user_id）只有收件人可读。
--     空间、会话等可见性在应用查询里按协作策略过滤。保留 7 天，由派发 worker 调用
--     openfox_private.purge_organization_events 分批清理（见文件末尾）。
-- 迁移运行器在事务内执行每个文件，无法使用 CONCURRENTLY；run_events 新索引的创建时间与行数成正比，
-- 期间阻塞 run_events 写入（本地与新部署可忽略，生产执行前需评估行数）。
-- 回滚：应用回退后新表、新列闲置即可；确需移除时见 db/migrations/README.md 的 000029 条目。

SET ROLE openfox_migrator;

-- 不缓存序列：同一事务内取号递增即可，跨事务的顺序由 txid 保证。
CREATE SEQUENCE openfox.organization_event_seq AS bigint NO CYCLE CACHE 1;

CREATE TABLE openfox.organization_events (
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    id bigint NOT NULL DEFAULT nextval('openfox.organization_event_seq'),
    txid xid8 NOT NULL DEFAULT pg_current_xact_id(),
    type text NOT NULL,
    entity text NOT NULL,
    entity_id uuid,
    version bigint,
    payload jsonb NOT NULL DEFAULT '{}',
    recipient_user_id uuid,
    space_id uuid,
    conversation_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    CONSTRAINT organization_events_type_valid CHECK (type ~ '^[a-z][a-z0-9_.-]{1,63}$'),
    CONSTRAINT organization_events_entity_valid CHECK (entity ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT organization_events_version_positive CHECK (version IS NULL OR version > 0),
    CONSTRAINT organization_events_payload_shape CHECK (
        jsonb_typeof(payload) = 'object' AND pg_column_size(payload) <= 16384
    )
);

ALTER SEQUENCE openfox.organization_event_seq OWNED BY openfox.organization_events.id;

CREATE INDEX organization_events_stream_idx
    ON openfox.organization_events (organization_id, txid, id);
CREATE INDEX organization_events_created_idx
    ON openfox.organization_events (organization_id, created_at);

ALTER TABLE openfox.organization_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.organization_events FORCE ROW LEVEL SECURITY;
CREATE POLICY organization_events_tenant_isolation ON openfox.organization_events
    USING (
        organization_id = openfox_private.current_organization_id()
        AND (recipient_user_id IS NULL OR recipient_user_id = openfox_private.current_actor_id())
    )
    WITH CHECK (organization_id = openfox_private.current_organization_id());

-- 事件由数据库触发器写入，与业务写入同一事务：业务提交则事件可见，业务回滚则事件一起回滚，
-- 不依赖每个写入路径记得调用。触发器以 definer（migrator）身份写入，需要单独的插入策略。
CREATE POLICY organization_events_migrator_insert ON openfox.organization_events
    FOR INSERT TO openfox_migrator
    WITH CHECK (true);

-- 任务：语句级触发器，按 (组织, 空间) 合并为一条 task.changed，payload 带至多 100 个任务 id。
CREATE OR REPLACE FUNCTION openfox_private.emit_task_events()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id, payload)
    SELECT c.organization_id, 'task.changed', 'task', c.space_id,
           jsonb_build_object('task_ids', to_jsonb((array_agg(c.id ORDER BY c.id))[1:100]),
                              'count', count(*))
      FROM changed c
     GROUP BY c.organization_id, c.space_id;
    RETURN NULL;
END
$$;

-- 收件箱：语句级触发器，按 (组织, 收件人) 合并为一条定向的 inbox.changed。
CREATE OR REPLACE FUNCTION openfox_private.emit_inbox_events()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, recipient_user_id, payload)
    SELECT c.organization_id, 'inbox.changed', 'notification', c.recipient_user_id,
           jsonb_build_object('count', count(*))
      FROM changed c
     GROUP BY c.organization_id, c.recipient_user_id;
    RETURN NULL;
END
$$;

-- Run 状态：行级触发器，只在创建与 state 变化时写 run.state。
CREATE OR REPLACE FUNCTION openfox_private.emit_run_state_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.state IS NOT DISTINCT FROM OLD.state
       AND NEW.wait_reason IS NOT DISTINCT FROM OLD.wait_reason THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.organization_events (
        organization_id, type, entity, entity_id, version, space_id, conversation_id, payload
    ) VALUES (
        NEW.organization_id, 'run.state', 'run', NEW.id, NEW.version, NEW.space_id, NEW.conversation_id,
        jsonb_build_object('state', NEW.state, 'wait_reason', NEW.wait_reason,
                           'failure_code', NEW.failure_code, 'task_id', NEW.task_id)
    );
    RETURN NULL;
END
$$;

-- 会话：生命周期、交接、标题、归档、绑定任务、可见性变化时写 conversation.updated；新消息写 conversation.message。
CREATE OR REPLACE FUNCTION openfox_private.emit_conversation_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF (NEW.lifecycle, NEW.handoff, NEW.title, NEW.archived_at, NEW.task_id, NEW.visibility)
       IS NOT DISTINCT FROM
       (OLD.lifecycle, OLD.handoff, OLD.title, OLD.archived_at, OLD.task_id, OLD.visibility) THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.organization_events (
        organization_id, type, entity, entity_id, version, space_id, conversation_id, payload
    ) VALUES (
        NEW.organization_id, 'conversation.updated', 'conversation', NEW.id, NEW.version,
        NEW.space_id, NEW.id,
        jsonb_build_object('lifecycle', NEW.lifecycle, 'handoff', NEW.handoff)
    );
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.emit_message_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    v_space_id uuid;
BEGIN
    SELECT space_id INTO v_space_id
      FROM openfox.conversations
     WHERE organization_id = NEW.organization_id AND id = NEW.conversation_id;
    INSERT INTO openfox.organization_events (
        organization_id, type, entity, entity_id, space_id, conversation_id, payload
    ) VALUES (
        NEW.organization_id, 'conversation.message', 'message', NEW.id, v_space_id, NEW.conversation_id,
        jsonb_build_object('seq', NEW.seq, 'author_kind', NEW.author_kind, 'kind', NEW.kind)
    );
    RETURN NULL;
END
$$;

ALTER FUNCTION openfox_private.emit_task_events() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_inbox_events() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_run_state_event() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_conversation_event() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_message_event() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_task_events() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_inbox_events() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_run_state_event() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_conversation_event() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_message_event() FROM PUBLIC;

-- 带转换表的触发器只能对应一种事件，INSERT 与 UPDATE 各建一个。
CREATE TRIGGER tasks_emit_events_insert
AFTER INSERT ON openfox.tasks
REFERENCING NEW TABLE AS changed
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_task_events();
CREATE TRIGGER tasks_emit_events_update
AFTER UPDATE ON openfox.tasks
REFERENCING NEW TABLE AS changed
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_task_events();

CREATE TRIGGER notifications_emit_events_insert
AFTER INSERT ON openfox.notifications
REFERENCING NEW TABLE AS changed
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_inbox_events();
CREATE TRIGGER notifications_emit_events_update
AFTER UPDATE ON openfox.notifications
REFERENCING NEW TABLE AS changed
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_inbox_events();

CREATE TRIGGER runs_emit_state_event
AFTER INSERT OR UPDATE OF state, wait_reason ON openfox.runs
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_run_state_event();

CREATE TRIGGER conversations_emit_event
AFTER UPDATE ON openfox.conversations
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_conversation_event();

CREATE TRIGGER messages_emit_event
AFTER INSERT ON openfox.messages
FOR EACH ROW EXECUTE FUNCTION openfox_private.emit_message_event();

ALTER TABLE openfox.run_events ADD COLUMN txid xid8;
ALTER TABLE openfox.run_events ALTER COLUMN txid SET DEFAULT pg_current_xact_id();
CREATE INDEX run_events_commit_stream_idx
    ON openfox.run_events (organization_id, (COALESCE(txid, '0'::xid8)), stream_seq);

RESET ROLE;

GRANT SELECT, INSERT, DELETE ON openfox.organization_events TO openfox_app;
GRANT USAGE ON SEQUENCE openfox.organization_event_seq TO openfox_app;

-- 保留期清理（7 天）：FORCE RLS 下组织 worker 以系统 actor 删不到定向事件（inbox.changed），
-- 控制面角色 openfox_context 对这张表也没有权限，因此清理走一个窄的 SECURITY DEFINER 函数：
--   - 只删除 created_at 早于 7 天的行，每次最多 p_limit（1–1000）行，返回删除行数；跨组织执行；
--   - 只授权给 openfox_context（派发 worker 在控制面事务里每 60 秒最多调用一次），openfox_app 不能调用；
--   - migrator 的 SELECT / DELETE 策略同样限定在 7 天以前的行，函数之外无法借 migrator 身份读写新事件。
-- 全局 (created_at) 索引避免清理时全表扫描（新表，建索引无锁表影响）。
SET ROLE openfox_migrator;

CREATE INDEX organization_events_retention_idx
    ON openfox.organization_events (created_at);

CREATE POLICY organization_events_migrator_retention_select ON openfox.organization_events
    FOR SELECT TO openfox_migrator
    USING (created_at < clock_timestamp() - interval '7 days');
CREATE POLICY organization_events_migrator_retention_delete ON openfox.organization_events
    FOR DELETE TO openfox_migrator
    USING (created_at < clock_timestamp() - interval '7 days');

CREATE OR REPLACE FUNCTION openfox_private.purge_organization_events(p_limit integer)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    v_deleted integer;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'invalid organization event purge limit' USING ERRCODE = '22023';
    END IF;
    DELETE FROM openfox.organization_events e
     USING (
         SELECT organization_id, id
           FROM openfox.organization_events
          WHERE created_at < clock_timestamp() - interval '7 days'
          ORDER BY created_at
          LIMIT p_limit
     ) doomed
     WHERE e.organization_id = doomed.organization_id AND e.id = doomed.id;
    GET DIAGNOSTICS v_deleted = ROW_COUNT;
    RETURN v_deleted;
END
$$;

ALTER FUNCTION openfox_private.purge_organization_events(integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.purge_organization_events(integer) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.purge_organization_events(integer) TO openfox_context;
