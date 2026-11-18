-- 阶段 5a「Agent 自动化」：自动化、触发器（定时 / 任务状态变更 / 评论 / Webhook）、事件队列、运行记录、Webhook 投递。
--
-- 仅扩展（expand-only）：
--   - automations：空间内的自动化。提示词、指派目标（Agent 或小队；小队按队长 Agent 执行）、通知人员、启停、排序位置。
--   - automation_triggers：每个自动化 1–10 个触发器。config 为有大小上限的 jsonb（结构由应用校验）；定时触发器的
--     next_run_at 由应用按频率、时间、时区计算；Webhook 触发器只存 URL 令牌的 SHA-256 哈希，签名密钥用
--     secrets.Box 密封保存，另有签名校验开关、IP 白名单（≤ 20 条）、最近一次请求的来源 IP。
--   - automation_events：任务状态变更与新评论的待处理事件，由 tasks / task_comments 上的触发器写入，
--     只在该空间存在启用的对应类型触发器时写入；后台 worker 按组织消费（processed_at）。
--   - automation_runs：每次触发一条。status：skipped（上一次还在运行 / 自动化已停用等）/ failed（还没创建 Run 就失败）/
--     started（已创建会话与消息，实际运行状态读 runs）。dedupe_key 在自动化内唯一，保证同一触发只处理一次。
--   - webhook_deliveries：每次 POST 一条投递记录（状态、签名状态、去重键、来源 IP、请求头与 body 的截断副本、尝试次数、
--     重放来源）。
--   - notifications.kind 增加 AUTOMATION_RESULT：Run 结束时由 runs 上的触发器通知自动化的通知人员。
--   - 组织事件：自动化与触发器变化发语句级 automation.changed（带 space_id）。
--   - resolve_webhook_trigger(token_hash)：Webhook 接收端在建立租户上下文前按令牌哈希找到组织与触发器（SECURITY DEFINER）。
--   - list_automation_due_organizations：后台 worker 找到有到期定时触发器或待处理事件的组织（控制面，只返回组织 ID）。
-- 锁：notifications 换 CHECK 约束需扫描表（与 000036 相同做法）；tasks / task_comments 只加触发器。
-- 回滚：应用回退后新表闲置即可；确需移除时见 db/migrations/README.md 的 000040 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.automations (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    title text NOT NULL,
    prompt text NOT NULL,
    assignee_kind text NOT NULL,
    assignee_id uuid NOT NULL,
    notify_user_ids uuid[] NOT NULL DEFAULT '{}',
    enabled boolean NOT NULL DEFAULT true,
    position integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT automations_title_valid CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),
    CONSTRAINT automations_prompt_valid CHECK (char_length(btrim(prompt)) BETWEEN 1 AND 20000),
    CONSTRAINT automations_assignee_kind_valid CHECK (assignee_kind IN ('AGENT', 'CREW')),
    CONSTRAINT automations_notify_size CHECK (cardinality(notify_user_ids) <= 20),
    CONSTRAINT automations_position_valid CHECK (position >= 0),
    CONSTRAINT automations_version_positive CHECK (version > 0)
);
CREATE INDEX automations_space_idx ON openfox.automations (organization_id, space_id, position, created_at);

CREATE TABLE openfox.automation_triggers (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    automation_id uuid NOT NULL,
    space_id uuid NOT NULL,
    kind text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    position smallint NOT NULL DEFAULT 0,
    config jsonb NOT NULL DEFAULT '{}',
    next_run_at timestamptz,
    webhook_token_hash bytea,
    webhook_secret_ciphertext bytea,
    webhook_secret_nonce bytea,
    webhook_secret_key_id text,
    signature_required boolean NOT NULL DEFAULT false,
    ip_allowlist text[] NOT NULL DEFAULT '{}',
    last_source_ip text,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, automation_id) REFERENCES openfox.automations (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT automation_triggers_kind_valid CHECK (kind IN ('schedule', 'task_status', 'comment', 'webhook')),
    CONSTRAINT automation_triggers_config_shape CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 4096),
    CONSTRAINT automation_triggers_schedule_valid CHECK ((kind = 'schedule') OR next_run_at IS NULL),
    CONSTRAINT automation_triggers_webhook_valid CHECK (
        (kind = 'webhook') = (webhook_token_hash IS NOT NULL)
        AND (webhook_token_hash IS NULL OR octet_length(webhook_token_hash) = 32)
    ),
    CONSTRAINT automation_triggers_secret_valid CHECK (
        (webhook_secret_ciphertext IS NULL AND webhook_secret_nonce IS NULL AND webhook_secret_key_id IS NULL)
        OR (kind = 'webhook' AND octet_length(webhook_secret_ciphertext) BETWEEN 16 AND 256
            AND octet_length(webhook_secret_nonce) = 12 AND webhook_secret_key_id ~ '^[A-Za-z0-9_.-]{1,64}$')
    ),
    CONSTRAINT automation_triggers_signature_valid CHECK (NOT signature_required OR webhook_secret_ciphertext IS NOT NULL),
    CONSTRAINT automation_triggers_allowlist_size CHECK (cardinality(ip_allowlist) <= 20),
    CONSTRAINT automation_triggers_source_ip_valid CHECK (last_source_ip IS NULL OR length(last_source_ip) <= 64),
    CONSTRAINT automation_triggers_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX automation_triggers_webhook_token_unique
    ON openfox.automation_triggers (webhook_token_hash) WHERE webhook_token_hash IS NOT NULL;
CREATE INDEX automation_triggers_automation_idx ON openfox.automation_triggers (organization_id, automation_id, position);
CREATE INDEX automation_triggers_due_idx ON openfox.automation_triggers (next_run_at) WHERE enabled AND kind = 'schedule';
CREATE INDEX automation_triggers_space_kind_idx ON openfox.automation_triggers (organization_id, space_id, kind) WHERE enabled;

CREATE TABLE openfox.automation_events (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    status_id uuid,
    comment_id uuid,
    actor_user_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    processed_at timestamptz,
    PRIMARY KEY (organization_id, id),
    CONSTRAINT automation_events_kind_valid CHECK (
        (kind = 'task_status' AND status_id IS NOT NULL AND comment_id IS NULL)
        OR (kind = 'comment' AND comment_id IS NOT NULL)
    )
);
CREATE INDEX automation_events_pending_idx ON openfox.automation_events (organization_id, created_at) WHERE processed_at IS NULL;

CREATE TABLE openfox.automation_runs (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    automation_id uuid NOT NULL,
    trigger_id uuid,
    trigger_kind text NOT NULL,
    status text NOT NULL,
    reason text NOT NULL DEFAULT '',
    trigger_title text NOT NULL DEFAULT '',
    trigger_payload jsonb NOT NULL DEFAULT '{}',
    dedupe_key text NOT NULL,
    conversation_id uuid,
    message_id uuid,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, automation_id) REFERENCES openfox.automations (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, conversation_id) REFERENCES openfox.conversations (organization_id, id),
    FOREIGN KEY (organization_id, message_id) REFERENCES openfox.messages (organization_id, id),
    CONSTRAINT automation_runs_trigger_kind_valid CHECK (trigger_kind IN ('manual', 'schedule', 'task_status', 'comment', 'webhook')),
    CONSTRAINT automation_runs_status_valid CHECK (status IN ('skipped', 'failed', 'started')),
    CONSTRAINT automation_runs_started_valid CHECK ((status = 'started') = (conversation_id IS NOT NULL AND message_id IS NOT NULL)),
    CONSTRAINT automation_runs_reason_length CHECK (char_length(reason) <= 500),
    CONSTRAINT automation_runs_title_length CHECK (char_length(trigger_title) <= 200),
    CONSTRAINT automation_runs_payload_shape CHECK (jsonb_typeof(trigger_payload) = 'object' AND pg_column_size(trigger_payload) <= 16384),
    CONSTRAINT automation_runs_dedupe_valid CHECK (char_length(dedupe_key) BETWEEN 1 AND 256)
);
CREATE UNIQUE INDEX automation_runs_dedupe_unique ON openfox.automation_runs (organization_id, automation_id, dedupe_key);
CREATE INDEX automation_runs_automation_idx ON openfox.automation_runs (organization_id, automation_id, created_at DESC);
CREATE INDEX automation_runs_message_idx ON openfox.automation_runs (organization_id, message_id) WHERE message_id IS NOT NULL;

CREATE TABLE openfox.webhook_deliveries (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    automation_id uuid NOT NULL,
    trigger_id uuid NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    status text NOT NULL,
    signature_status text NOT NULL,
    provider text NOT NULL DEFAULT '',
    event text NOT NULL DEFAULT '',
    dedupe_key text NOT NULL,
    dedupe_source text NOT NULL,
    content_type text NOT NULL DEFAULT '',
    source_ip text NOT NULL DEFAULT '',
    headers jsonb NOT NULL DEFAULT '{}',
    body bytea NOT NULL DEFAULT '',
    body_truncated boolean NOT NULL DEFAULT false,
    response_status integer NOT NULL,
    response_body text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 1,
    last_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replay_of uuid,
    automation_run_id uuid,
    error text NOT NULL DEFAULT '',
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, trigger_id) REFERENCES openfox.automation_triggers (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, replay_of) REFERENCES openfox.webhook_deliveries (organization_id, id) ON DELETE SET NULL (replay_of),
    FOREIGN KEY (organization_id, automation_run_id) REFERENCES openfox.automation_runs (organization_id, id) ON DELETE SET NULL (automation_run_id),
    CONSTRAINT webhook_deliveries_status_valid CHECK (status IN ('queued', 'dispatched', 'rejected', 'ignored', 'failed')),
    CONSTRAINT webhook_deliveries_signature_valid CHECK (
        signature_status IN ('valid', 'invalid', 'missing', 'not_required', 'expired', 'ip_blocked')
    ),
    CONSTRAINT webhook_deliveries_dedupe_valid CHECK (char_length(dedupe_key) BETWEEN 1 AND 256 AND dedupe_source IN ('header', 'body_hash')),
    CONSTRAINT webhook_deliveries_text_length CHECK (
        char_length(provider) <= 64 AND char_length(event) <= 128 AND char_length(content_type) <= 128
        AND char_length(source_ip) <= 64 AND char_length(response_body) <= 4096 AND char_length(error) <= 1000
    ),
    CONSTRAINT webhook_deliveries_headers_shape CHECK (jsonb_typeof(headers) = 'object' AND pg_column_size(headers) <= 16384),
    CONSTRAINT webhook_deliveries_body_size CHECK (octet_length(body) <= 262144),
    CONSTRAINT webhook_deliveries_attempts_positive CHECK (attempts > 0)
);
CREATE INDEX webhook_deliveries_trigger_idx ON openfox.webhook_deliveries (organization_id, trigger_id, received_at DESC);
CREATE INDEX webhook_deliveries_dedupe_idx ON openfox.webhook_deliveries (organization_id, trigger_id, dedupe_key, received_at DESC);

ALTER TABLE openfox.automations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.automations FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.automation_triggers ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.automation_triggers FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.automation_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.automation_events FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.automation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.automation_runs FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.webhook_deliveries FORCE ROW LEVEL SECURITY;

CREATE POLICY automations_tenant_isolation ON openfox.automations
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY automation_triggers_tenant_isolation ON openfox.automation_triggers
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY automation_events_tenant_isolation ON openfox.automation_events
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY automation_runs_tenant_isolation ON openfox.automation_runs
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY webhook_deliveries_tenant_isolation ON openfox.webhook_deliveries
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 沿用 *_dispatch_scan 模式：只给 migrator 只读策略，供控制面函数扫描到期组织与按令牌哈希查找触发器。
CREATE POLICY automation_triggers_scan ON openfox.automation_triggers FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY automation_events_scan ON openfox.automation_events FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY automations_scan ON openfox.automations FOR SELECT TO openfox_migrator USING (true);

-- 任务状态变更与新评论：只在该空间有启用的对应类型触发器时写入事件。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_automation_task_status()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.automation_events (organization_id, kind, space_id, task_id, status_id, actor_user_id)
    SELECT n.organization_id, 'task_status', n.space_id, n.id, n.status_id,
           NULLIF(openfox_private.current_actor_id(), '00000000-0000-0000-0000-000000000000'::uuid)
      FROM changed_new n
      JOIN changed_old o ON o.organization_id = n.organization_id AND o.id = n.id
     WHERE n.status_id IS DISTINCT FROM o.status_id
       AND n.deleted_at IS NULL
       AND EXISTS (SELECT 1 FROM openfox.automation_triggers t
                    WHERE t.organization_id = n.organization_id AND t.space_id = n.space_id
                      AND t.kind = 'task_status' AND t.enabled);
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION openfox_private.enqueue_automation_comment()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.automation_events (organization_id, kind, space_id, task_id, comment_id, actor_user_id)
    SELECT n.organization_id, 'comment', n.space_id, n.task_id, n.id, n.author_user_id
      FROM changed_new n
     WHERE EXISTS (SELECT 1 FROM openfox.automation_triggers t
                    WHERE t.organization_id = n.organization_id AND t.space_id = n.space_id
                      AND t.kind = 'comment' AND t.enabled);
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_automation_task_status() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.enqueue_automation_comment() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_automation_task_status() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.enqueue_automation_comment() FROM PUBLIC;

CREATE TRIGGER tasks_enqueue_automation_status AFTER UPDATE ON openfox.tasks
REFERENCING OLD TABLE AS changed_old NEW TABLE AS changed_new
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_automation_task_status();
CREATE TRIGGER task_comments_enqueue_automation AFTER INSERT ON openfox.task_comments
REFERENCING NEW TABLE AS changed_new
FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_automation_comment();

-- Run 结束时通知自动化的通知人员（只通知仍是 ACTIVE 成员的人）。
ALTER TABLE openfox.notifications DROP CONSTRAINT notifications_kind_valid;
ALTER TABLE openfox.notifications ADD CONSTRAINT notifications_kind_valid CHECK (kind IN (
    'MENTION', 'HANDOFF', 'HANDOFF_REVIEW', 'REPLY', 'TASK_COMMENT', 'RUN_FAILED', 'TASK_ASSIGNED', 'TRIAGE_NEW',
    'AUTOMATION_RESULT'
));

CREATE OR REPLACE FUNCTION openfox_private.notify_automation_result()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.notifications (organization_id, recipient_user_id, kind, conversation_id, dedupe_key, safe_summary)
    SELECT NEW.organization_id, m.user_id, 'AUTOMATION_RESULT', NEW.conversation_id,
           'automation-run:' || NEW.id::text,
           left(a.title || CASE NEW.state WHEN 'SUCCEEDED' THEN ' · 已完成' WHEN 'FAILED' THEN ' · 失败' ELSE ' · 已取消' END, 512)
      FROM openfox.conversation_inputs ci
      JOIN openfox.automation_runs ar ON ar.organization_id = ci.organization_id AND ar.message_id = ci.message_id
      JOIN openfox.automations a ON a.organization_id = ar.organization_id AND a.id = ar.automation_id
      JOIN openfox.organization_members m ON m.organization_id = a.organization_id
       AND m.user_id = ANY (a.notify_user_ids) AND m.status = 'ACTIVE'
     WHERE ci.organization_id = NEW.organization_id AND ci.run_id = NEW.id
    ON CONFLICT (organization_id, recipient_user_id, dedupe_key) DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.notify_automation_result() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.notify_automation_result() FROM PUBLIC;

CREATE TRIGGER runs_notify_automation_result
AFTER UPDATE OF state ON openfox.runs
FOR EACH ROW
WHEN (NEW.state IN ('SUCCEEDED', 'FAILED', 'CANCELED') AND OLD.state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED'))
EXECUTE FUNCTION openfox_private.notify_automation_result();

-- 组织事件：自动化与触发器变化，按空间合并。
CREATE OR REPLACE FUNCTION openfox_private.emit_automation_event_new()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id)
    SELECT DISTINCT c.organization_id, 'automation.changed', 'automation', c.space_id FROM changed_new c;
    RETURN NULL;
END
$$;
CREATE OR REPLACE FUNCTION openfox_private.emit_automation_event_old()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.organization_events (organization_id, type, entity, space_id)
    SELECT DISTINCT c.organization_id, 'automation.changed', 'automation', c.space_id FROM changed_old c;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.emit_automation_event_new() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.emit_automation_event_old() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.emit_automation_event_new() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.emit_automation_event_old() FROM PUBLIC;

CREATE TRIGGER automations_emit_insert AFTER INSERT ON openfox.automations
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_automation_event_new();
CREATE TRIGGER automations_emit_update AFTER UPDATE ON openfox.automations
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_automation_event_new();
CREATE TRIGGER automations_emit_delete AFTER DELETE ON openfox.automations
REFERENCING OLD TABLE AS changed_old FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_automation_event_old();
CREATE TRIGGER automation_triggers_emit_insert AFTER INSERT ON openfox.automation_triggers
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_automation_event_new();
CREATE TRIGGER automation_triggers_emit_update AFTER UPDATE ON openfox.automation_triggers
REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_automation_event_new();

-- Webhook 接收端：按令牌哈希找到组织与触发器（触发器或自动化停用时也返回，由应用记为 ignored）。
CREATE OR REPLACE FUNCTION openfox_private.resolve_webhook_trigger(p_token_hash bytea)
RETURNS TABLE (organization_id uuid, automation_id uuid, trigger_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT t.organization_id, t.automation_id, t.id
      FROM openfox.automation_triggers t
      JOIN openfox.organization_directory d ON d.organization_id = t.organization_id AND d.deleted_at IS NULL
     WHERE p_token_hash IS NOT NULL AND octet_length(p_token_hash) = 32
       AND t.webhook_token_hash = p_token_hash
$$;
ALTER FUNCTION openfox_private.resolve_webhook_trigger(bytea) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_webhook_trigger(bytea) FROM PUBLIC;

-- 后台 worker：有到期定时触发器或待处理事件的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_automation_due_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid automation scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT t.organization_id FROM openfox.automation_triggers t
         WHERE t.enabled AND t.kind = 'schedule' AND t.next_run_at <= clock_timestamp()
        UNION
        SELECT e.organization_id FROM openfox.automation_events e WHERE e.processed_at IS NULL
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_automation_due_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_automation_due_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.automations TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.automation_triggers TO openfox_app;
GRANT SELECT, UPDATE ON openfox.automation_events TO openfox_app;
GRANT SELECT, INSERT ON openfox.automation_runs TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.webhook_deliveries TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_webhook_trigger(bytea) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.list_automation_due_organizations(uuid, integer) TO openfox_context;
