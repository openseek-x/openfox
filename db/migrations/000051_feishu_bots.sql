-- 阶段 6d-2「飞书任务助手、绑定 Agents、飞书消息 ↔ 会话」。
--
-- 仅扩展（expand-only）：
--   - agents.builtin_kind 增加 TASK_ASSISTANT：每个空间最多一个内置任务助手 Agent（在该空间内共享，不出现在目录与空间 Agent 列表；
--     运行时的权限仍是「发消息的成员 ∩ Agent」）。
--   - conversations.origin 增加 IM：由飞书消息创建的会话。
--   - im_app_registrations：扫码创建飞书应用的会话（设备码流程，RFC 8628）。device_code 加密保存；后台 worker 按间隔轮询，
--     租约保证同一会话只由一个实例轮询（成功响应只返回一次凭证）。同一目标（空间的任务助手 / 某个 Agent）同时只有一个等待中的会话。
--   - im_bots：任务助手 / Agent 与飞书应用（组织级飞书连接，account_kind = task_assistant / agent）的对应。一个 Agent 只对应一个机器人，
--     一个空间只有一个任务助手机器人（部分唯一）。
--   - im_chat_bindings：飞书会话（单聊 / 群）↔ OpenFox 会话，同一机器人下一个飞书会话只对应一个 OpenFox 会话。
--   - im_inbound_messages：机器人收到的飞书消息队列（长连接 handler 只入队、3 秒内回 ACK；worker 解析发送人、读取群上下文、写入会话）。
--   - im_reply_outbox：发回飞书的消息队列。Agent 在已关联飞书会话的 OpenFox 会话里发言时由触发器入队；「请先绑定」等提示由应用入队。
--   - 个人飞书连接按 settings.union_id 查找（不同飞书应用下同一用户的 open_id 不同，union_id 相同）：部分索引。
--   - run_space_tool_calls：任务助手 Run 的空间任务工具调用记录（幂等重放、每次运行的建 / 改次数上限）。
--   - 控制面函数 list_im_worker_organizations：有到期的扫码会话、入站消息或回发项的组织。
-- RLS 与其他租户表相同；openfox_app 可读写，流水表另给 DELETE 用于保留期清理。
-- 锁：agents / conversations 换 CHECK 约束（校验现有行，不重写表）；agents 新建部分唯一索引（没有 TASK_ASSISTANT 行，索引为空）；
--   integration_connections 新建部分索引（只含飞书个人连接）；messages 只新增触发器（短暂 SHARE ROW EXCLUSIVE 锁）。
-- 回滚：应用回退前先 DROP TRIGGER messages_enqueue_im_reply；恢复旧 CHECK 前需先确认没有 TASK_ASSISTANT 的 Agent 与 origin = IM 的会话
--   （可归档后改回）；确需移除时再 DROP 函数与 im_reply_outbox、im_inbound_messages、im_chat_bindings、im_bots、im_app_registrations、run_space_tool_calls 和新索引。

SET ROLE openfox_migrator;

ALTER TABLE openfox.agents DROP CONSTRAINT agents_builtin_kind_valid;
ALTER TABLE openfox.agents ADD CONSTRAINT agents_builtin_kind_valid
    CHECK (builtin_kind IS NULL
           OR (builtin_kind = 'ASSISTANT' AND visibility = 'PRIVATE' AND owner_user_id IS NOT NULL)
           OR (builtin_kind = 'TASK_ASSISTANT' AND visibility = 'SHARED' AND space_id IS NOT NULL));
CREATE UNIQUE INDEX agents_builtin_task_assistant_unique
    ON openfox.agents (organization_id, space_id)
    WHERE builtin_kind = 'TASK_ASSISTANT' AND archived_at IS NULL;

ALTER TABLE openfox.conversations DROP CONSTRAINT conversations_origin_valid;
ALTER TABLE openfox.conversations ADD CONSTRAINT conversations_origin_valid
    CHECK (origin IN ('USER', 'TASK_ASSIGNMENT', 'IM'));

CREATE INDEX integration_connections_feishu_union_idx
    ON openfox.integration_connections (organization_id, (settings->>'union_id'))
    WHERE provider = 'feishu' AND scope = 'PERSONAL' AND status = 'ACTIVE';

CREATE TABLE openfox.im_app_registrations (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'feishu',
    purpose text NOT NULL,
    space_id uuid,
    agent_id uuid,
    requested_by uuid NOT NULL,
    -- 任务助手：Runtime 选择（computer_id / grant_id / runtime / model）与附加提示词；Agent：空对象。
    config jsonb NOT NULL DEFAULT '{}',
    device_code_sealed bytea,
    device_code_key_id text NOT NULL DEFAULT '',
    verification_url text NOT NULL DEFAULT '',
    poll_interval integer NOT NULL DEFAULT 5,
    expires_at timestamptz NOT NULL,
    next_poll_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    -- PENDING = 等待扫码确认；COMPLETED / DENIED（用户在飞书取消）/ EXPIRED（链接或会话过期）/ FAILED（飞书拒绝或保存失败）/
    -- CANCELLED（用户取消或被新会话取代）为终态。
    status text NOT NULL DEFAULT 'PENDING',
    error_code text NOT NULL DEFAULT '',
    connection_id uuid,
    bot_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, requested_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    CONSTRAINT im_app_registrations_purpose_valid CHECK (
        (purpose = 'TASK_ASSISTANT' AND space_id IS NOT NULL AND agent_id IS NULL)
        OR (purpose = 'AGENT' AND agent_id IS NOT NULL)),
    CONSTRAINT im_app_registrations_status_valid CHECK (status IN ('PENDING', 'COMPLETED', 'DENIED', 'EXPIRED', 'FAILED', 'CANCELLED')),
    CONSTRAINT im_app_registrations_finished_valid CHECK ((status = 'PENDING') = (finished_at IS NULL)),
    CONSTRAINT im_app_registrations_text_valid CHECK (
        provider ~ '^[a-z][a-z0-9_]{1,31}$' AND char_length(verification_url) <= 2048 AND char_length(error_code) <= 64
        AND char_length(lease_owner) <= 128 AND char_length(device_code_key_id) <= 64 AND poll_interval BETWEEN 1 AND 120
        AND (device_code_sealed IS NULL OR octet_length(device_code_sealed) <= 4096)
        AND jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 16384)
);
CREATE UNIQUE INDEX im_app_registrations_pending_space_unique ON openfox.im_app_registrations (organization_id, space_id)
    WHERE status = 'PENDING' AND purpose = 'TASK_ASSISTANT';
CREATE UNIQUE INDEX im_app_registrations_pending_agent_unique ON openfox.im_app_registrations (organization_id, agent_id)
    WHERE status = 'PENDING' AND purpose = 'AGENT';
CREATE INDEX im_app_registrations_due_idx ON openfox.im_app_registrations (organization_id, next_poll_at)
    WHERE status = 'PENDING';

CREATE TABLE openfox.im_bots (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    connection_id uuid NOT NULL,
    kind text NOT NULL,
    agent_id uuid NOT NULL,
    space_id uuid,
    extra_prompt text NOT NULL DEFAULT '',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    removed_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT im_bots_kind_valid CHECK ((kind = 'TASK_ASSISTANT' AND space_id IS NOT NULL) OR kind = 'AGENT'),
    CONSTRAINT im_bots_text_valid CHECK (char_length(extra_prompt) <= 4000 AND version > 0)
);
CREATE UNIQUE INDEX im_bots_connection_unique ON openfox.im_bots (organization_id, connection_id);
CREATE UNIQUE INDEX im_bots_agent_unique ON openfox.im_bots (organization_id, agent_id) WHERE removed_at IS NULL;
CREATE UNIQUE INDEX im_bots_space_assistant_unique ON openfox.im_bots (organization_id, space_id)
    WHERE removed_at IS NULL AND kind = 'TASK_ASSISTANT';

CREATE TABLE openfox.im_chat_bindings (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    bot_id uuid NOT NULL,
    chat_id text NOT NULL,
    chat_type text NOT NULL,
    conversation_id uuid NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, bot_id) REFERENCES openfox.im_bots (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id) REFERENCES openfox.conversations (organization_id, id),
    CONSTRAINT im_chat_bindings_type_valid CHECK (chat_type IN ('p2p', 'group')),
    CONSTRAINT im_chat_bindings_text_valid CHECK (char_length(btrim(chat_id)) BETWEEN 1 AND 128)
);
CREATE UNIQUE INDEX im_chat_bindings_chat_unique ON openfox.im_chat_bindings (organization_id, bot_id, chat_id);
CREATE UNIQUE INDEX im_chat_bindings_conversation_unique ON openfox.im_chat_bindings (organization_id, conversation_id);

CREATE TABLE openfox.im_inbound_messages (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    bot_id uuid NOT NULL,
    remote_message_id text NOT NULL,
    chat_id text NOT NULL,
    chat_type text NOT NULL,
    sender_open_id text NOT NULL DEFAULT '',
    sender_union_id text NOT NULL DEFAULT '',
    message_type text NOT NULL DEFAULT '',
    -- 去掉 @ 机器人之后的纯文本；非文本消息为空，由 worker 回复提示。
    text text NOT NULL DEFAULT '',
    parent_remote_id text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'PENDING',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    result text NOT NULL DEFAULT '',
    message_id uuid,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, bot_id) REFERENCES openfox.im_bots (organization_id, id),
    CONSTRAINT im_inbound_messages_type_valid CHECK (chat_type IN ('p2p', 'group')),
    -- DONE = 已写入会话；IGNORED = 不需要处理（未 @ 机器人、机器人已移除等）；REJECTED = 已回复提示（未绑定账号、无权限、非文本）；
    -- FAILED = 重试耗尽。
    CONSTRAINT im_inbound_messages_status_valid CHECK (status IN ('PENDING', 'PROCESSING', 'DONE', 'IGNORED', 'REJECTED', 'FAILED')),
    CONSTRAINT im_inbound_messages_finished_valid CHECK ((status IN ('PENDING', 'PROCESSING')) = (finished_at IS NULL)),
    CONSTRAINT im_inbound_messages_text_valid CHECK (
        char_length(btrim(remote_message_id)) BETWEEN 1 AND 128 AND char_length(btrim(chat_id)) BETWEEN 1 AND 128
        AND char_length(sender_open_id) <= 128 AND char_length(sender_union_id) <= 128 AND char_length(message_type) <= 32
        AND char_length(text) <= 8000 AND char_length(parent_remote_id) <= 128 AND attempts >= 0
        AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 1000 AND char_length(result) <= 64)
);
CREATE UNIQUE INDEX im_inbound_messages_remote_unique ON openfox.im_inbound_messages (organization_id, bot_id, remote_message_id);
CREATE INDEX im_inbound_messages_due_idx ON openfox.im_inbound_messages (organization_id, next_attempt_at, received_at)
    WHERE status IN ('PENDING', 'PROCESSING');
CREATE INDEX im_inbound_messages_message_idx ON openfox.im_inbound_messages (organization_id, message_id) WHERE message_id IS NOT NULL;
CREATE INDEX im_inbound_messages_finished_idx ON openfox.im_inbound_messages (organization_id, finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE openfox.im_reply_outbox (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    bot_id uuid NOT NULL,
    chat_id text NOT NULL,
    -- Agent 回复对应的 OpenFox 消息；应用直接入队的提示没有 message_id，正文放 body。
    message_id uuid,
    body text NOT NULL DEFAULT '',
    reply_to_remote_id text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'PENDING',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    remote_message_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, bot_id) REFERENCES openfox.im_bots (organization_id, id),
    FOREIGN KEY (organization_id, message_id) REFERENCES openfox.messages (organization_id, id),
    CONSTRAINT im_reply_outbox_body_valid CHECK (message_id IS NOT NULL OR char_length(btrim(body)) > 0),
    CONSTRAINT im_reply_outbox_status_valid CHECK (status IN ('PENDING', 'SENDING', 'DONE', 'FAILED', 'SKIPPED')),
    CONSTRAINT im_reply_outbox_finished_valid CHECK ((status IN ('DONE', 'FAILED', 'SKIPPED')) = (finished_at IS NOT NULL)),
    CONSTRAINT im_reply_outbox_text_valid CHECK (
        char_length(btrim(chat_id)) BETWEEN 1 AND 128 AND char_length(body) <= 8000 AND char_length(reply_to_remote_id) <= 128
        AND attempts >= 0 AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 1000
        AND char_length(remote_message_id) <= 128)
);
CREATE UNIQUE INDEX im_reply_outbox_message_unique ON openfox.im_reply_outbox (organization_id, message_id) WHERE message_id IS NOT NULL;
CREATE INDEX im_reply_outbox_due_idx ON openfox.im_reply_outbox (organization_id, next_attempt_at, created_at)
    WHERE status IN ('PENDING', 'SENDING');
CREATE INDEX im_reply_outbox_finished_idx ON openfox.im_reply_outbox (organization_id, finished_at) WHERE finished_at IS NOT NULL;

-- 任务助手 Run 的空间任务工具调用（space_task_create / space_task_search / space_task_update）：按 (Run, call_id) 幂等重放，
-- 并据此统计每次运行的建 / 改次数上限（用户决定：每次运行最多建 5 个、改 10 次）。
CREATE TABLE openfox.run_space_tool_calls (
    organization_id uuid NOT NULL,
    run_id uuid NOT NULL,
    call_id text NOT NULL,
    tool text NOT NULL,
    ok boolean NOT NULL,
    result text NOT NULL,
    task_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, run_id, call_id),
    FOREIGN KEY (organization_id, run_id) REFERENCES openfox.runs (organization_id, id),
    CONSTRAINT run_space_tool_calls_tool_valid CHECK (tool IN ('space_task_create', 'space_task_search', 'space_task_update')),
    CONSTRAINT run_space_tool_calls_text_valid CHECK (char_length(btrim(call_id)) BETWEEN 1 AND 128 AND octet_length(result) <= 32768)
);

ALTER TABLE openfox.im_app_registrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_app_registrations FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_bots FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_chat_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_chat_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_inbound_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_inbound_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_reply_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_reply_outbox FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_space_tool_calls ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.run_space_tool_calls FORCE ROW LEVEL SECURITY;
CREATE POLICY im_app_registrations_tenant_isolation ON openfox.im_app_registrations
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_bots_tenant_isolation ON openfox.im_bots
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_chat_bindings_tenant_isolation ON openfox.im_chat_bindings
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_inbound_messages_tenant_isolation ON openfox.im_inbound_messages
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_reply_outbox_tenant_isolation ON openfox.im_reply_outbox
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY run_space_tool_calls_tenant_isolation ON openfox.run_space_tool_calls
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_app_registrations_scan ON openfox.im_app_registrations FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY im_inbound_messages_scan ON openfox.im_inbound_messages FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY im_reply_outbox_scan ON openfox.im_reply_outbox FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY im_bots_scan ON openfox.im_bots FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY im_chat_bindings_scan ON openfox.im_chat_bindings FOR SELECT TO openfox_migrator USING (true);

-- Agent 在关联了飞书会话的 OpenFox 会话里发言（文本或草稿 / 执行结果卡），或系统消息（例如 Run 无法开始 / 失败）时，入队发回飞书。
-- 机器人已移除或其飞书连接不是 ACTIVE 时不入队。reply_to 取该会话最近一条已写入的飞书入站消息（群里用「回复」串起上下文）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_reply()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.im_reply_outbox (organization_id, bot_id, chat_id, message_id, reply_to_remote_id)
    SELECT n.organization_id, b.bot_id, b.chat_id, n.id,
           coalesce((SELECT i.remote_message_id FROM openfox.im_inbound_messages i
                      JOIN openfox.messages m ON m.organization_id = i.organization_id AND m.id = i.message_id
                     WHERE i.organization_id = n.organization_id AND i.bot_id = b.bot_id AND i.message_id IS NOT NULL
                       AND m.conversation_id = n.conversation_id
                     ORDER BY m.seq DESC LIMIT 1), '')
      FROM changed_new n
      JOIN openfox.im_chat_bindings b ON b.organization_id = n.organization_id AND b.conversation_id = n.conversation_id
      JOIN openfox.im_bots bot ON bot.organization_id = b.organization_id AND bot.id = b.bot_id AND bot.removed_at IS NULL
      JOIN openfox.integration_connections c
        ON c.organization_id = bot.organization_id AND c.id = bot.connection_id AND c.status = 'ACTIVE'
     WHERE n.author_kind IN ('AGENT', 'SYSTEM')
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_im_reply() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_reply() FROM PUBLIC;

CREATE TRIGGER messages_enqueue_im_reply AFTER INSERT ON openfox.messages
    REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_im_reply();

-- 后台 worker：有到期的扫码会话（含租约过期）、入站消息或回发项的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_im_worker_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im worker scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT r.organization_id FROM openfox.im_app_registrations r
         WHERE r.status = 'PENDING' AND r.next_poll_at <= clock_timestamp()
           AND (r.lease_expires_at IS NULL OR r.lease_expires_at <= clock_timestamp())
        UNION
        SELECT i.organization_id FROM openfox.im_inbound_messages i
         WHERE (i.status = 'PENDING' AND i.next_attempt_at <= clock_timestamp())
            OR (i.status = 'PROCESSING' AND (i.lease_expires_at IS NULL OR i.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT q.organization_id FROM openfox.im_reply_outbox q
         WHERE (q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.im_app_registrations, openfox.im_bots, openfox.im_chat_bindings TO openfox_app;
GRANT SELECT, INSERT ON openfox.run_space_tool_calls TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.im_inbound_messages, openfox.im_reply_outbox TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) TO openfox_context;
