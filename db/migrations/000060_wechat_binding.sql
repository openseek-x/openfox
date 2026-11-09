-- 阶段 7g-W「微信扫码绑定 Agent」（腾讯官方 ClawBot / iLink 协议）。
--
-- 为什么新建表而不是复用 im_bots / integration_connections：
--   iLink 的模型是「每次扫码产生一个 bot（bot_token），按 Agent + 发起人绑定」，同一个 Agent 可以被多个成员各自扫码绑定；
--   而 im_bots 有 (组织, Agent) 唯一索引（一个 Agent 只对应一个机器人），且挂在组织级 App 连接下（需要 AppID / AppSecret，
--   长连接状态按连接记）。强行放进去要放宽既有唯一约束并改写飞书路径，风险大于收益，所以只新增 4 张表，不改动任何既有表的结构。
--
-- 仅扩展（expand-only）：
--   - wechat_qr_sessions：扫码会话（二维码、状态、过期时间 ≤ 5 分钟）。qrcode 只在服务端使用，API 不返回。
--   - wechat_bindings：Agent ↔ 微信绑定。bot_token 用服务端密钥加密保存（bot_token_sealed），同一 ilink_bot_id 只能有一条有效绑定，
--     同一发起人同一 Agent 只能有一条有效绑定（重复扫码在事务里先把旧的标为 REMOVED）。长轮询游标 sync_buf 与租约（lease_owner /
--     lease_expires_at）也在这张表上：租约保证同一绑定同一时刻只在一个实例上轮询。
--   - wechat_inbound：入站消息队列与去重（(绑定, remote_message_id) 唯一）。轮询线程只入队并推进游标，worker 另行处理（认人、写入会话、触发 Run）。
--   - wechat_reply_outbox：回发队列。Agent / SYSTEM 在绑定的会话里发言时由触发器入队；client_id 由 outbox 行 ID 派生，重试幂等。
--   - 控制面函数 list_wechat_bindings（需要轮询的绑定）与 list_wechat_worker_organizations（有待处理入站 / 回发的组织）。
--   - 不放宽任何 CHECK：conversations.origin 已在 000051 含 'IM'；无 user_notification_channels 变更。
-- RLS：新表与既有 IM 表一致（租户隔离策略 + 控制面 SECURITY DEFINER 函数所需的 migrator scan 策略）；函数 OWNER openfox_migrator、REVOKE PUBLIC。
-- 锁：全部是新建对象；只有 messages 上新增触发器（短暂 SHARE ROW EXCLUSIVE 锁，不重写表、不扫描现有行）。
-- 回滚：应用回退前先 DROP TRIGGER messages_enqueue_wechat_reply ON openfox.messages；确认没有 ACTIVE 绑定（在页面里解绑）后可
--   DROP FUNCTION openfox_private.enqueue_wechat_reply()、list_wechat_bindings(uuid, uuid, integer)、list_wechat_worker_organizations(uuid, integer)，
--   再依次 DROP TABLE wechat_reply_outbox、wechat_inbound、wechat_bindings、wechat_qr_sessions。旧版本不认识这些表，闲置不影响其他功能。

SET ROLE openfox_migrator;

CREATE TABLE openfox.wechat_qr_sessions (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL,
    requested_by uuid NOT NULL,
    qrcode text NOT NULL,
    -- 展示给用户的二维码内容（qrcode_img_content，通常是一个 URL）；前端只把它当作二维码文本。
    qr_content text NOT NULL,
    -- 状态轮询使用的基址（scaned_but_redirect 之后改为重定向主机，已通过白名单校验）。
    poll_base_url text NOT NULL,
    -- PENDING = 等待扫码；SCANNED = 已扫码待确认；NEED_VERIFY = 需要输入验证码；CONFIRMED / EXPIRED / BLOCKED（验证码错误次数过多）/
    -- FAILED / CANCELLED 为终态。
    status text NOT NULL DEFAULT 'PENDING',
    error_code text NOT NULL DEFAULT '',
    binding_id uuid,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, requested_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT wechat_qr_sessions_status_valid CHECK (status IN ('PENDING', 'SCANNED', 'NEED_VERIFY', 'CONFIRMED', 'EXPIRED', 'BLOCKED', 'FAILED', 'CANCELLED')),
    CONSTRAINT wechat_qr_sessions_finished_valid CHECK ((status IN ('PENDING', 'SCANNED', 'NEED_VERIFY')) = (finished_at IS NULL)),
    CONSTRAINT wechat_qr_sessions_text_valid CHECK (
        char_length(qrcode) BETWEEN 1 AND 2048 AND char_length(qr_content) BETWEEN 1 AND 2048
        AND char_length(poll_base_url) BETWEEN 1 AND 512 AND char_length(error_code) <= 64)
);
CREATE INDEX wechat_qr_sessions_user_idx ON openfox.wechat_qr_sessions (organization_id, requested_by, created_at DESC);
CREATE INDEX wechat_qr_sessions_finished_idx ON openfox.wechat_qr_sessions (organization_id, finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE openfox.wechat_bindings (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL,
    created_by uuid NOT NULL,
    ilink_bot_id text NOT NULL,
    -- 扫码的微信用户；入站消息只信任这个 ID。
    ilink_user_id text NOT NULL,
    bot_token_sealed bytea NOT NULL,
    bot_token_key_id text NOT NULL DEFAULT '',
    base_url text NOT NULL,
    -- ACTIVE；EXPIRED = 微信登录已失效（iLink 返回 -14），需要重新扫码；REMOVED = 已解绑或被新绑定取代。
    status text NOT NULL DEFAULT 'ACTIVE',
    status_reason text NOT NULL DEFAULT '',
    sync_buf text NOT NULL DEFAULT '',
    poll_timeout_ms integer NOT NULL DEFAULT 35000,
    -- 最近一条入站消息的 context_token，回复时带上。
    last_context_token text NOT NULL DEFAULT '',
    last_inbound_at timestamptz,
    -- 绑定对应的 OpenFox 会话（origin = IM），第一条入站消息时创建。
    conversation_id uuid,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    connected boolean NOT NULL DEFAULT false,
    last_error text NOT NULL DEFAULT '',
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    removed_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id) REFERENCES openfox.conversations (organization_id, id),
    CONSTRAINT wechat_bindings_status_valid CHECK (status IN ('ACTIVE', 'EXPIRED', 'REMOVED')),
    CONSTRAINT wechat_bindings_removed_valid CHECK ((status = 'REMOVED') = (removed_at IS NOT NULL)),
    CONSTRAINT wechat_bindings_text_valid CHECK (
        char_length(btrim(ilink_bot_id)) BETWEEN 1 AND 256 AND char_length(btrim(ilink_user_id)) BETWEEN 1 AND 256
        AND octet_length(bot_token_sealed) <= 4096 AND char_length(bot_token_key_id) <= 64
        AND char_length(base_url) BETWEEN 1 AND 512 AND char_length(status_reason) <= 200 AND char_length(sync_buf) <= 16384
        AND poll_timeout_ms BETWEEN 1000 AND 120000 AND char_length(last_context_token) <= 2048
        AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 500 AND version > 0)
);
-- 同一个 ilink_bot_id 只能有一条有效（未移除）绑定；已失效（EXPIRED）的也占位，直到解绑或被同一发起人同一 Agent 的新绑定取代。
CREATE UNIQUE INDEX wechat_bindings_bot_unique ON openfox.wechat_bindings (ilink_bot_id) WHERE status <> 'REMOVED';
CREATE UNIQUE INDEX wechat_bindings_owner_agent_unique ON openfox.wechat_bindings (organization_id, created_by, agent_id) WHERE status <> 'REMOVED';
CREATE UNIQUE INDEX wechat_bindings_conversation_unique ON openfox.wechat_bindings (organization_id, conversation_id) WHERE conversation_id IS NOT NULL;
CREATE INDEX wechat_bindings_agent_idx ON openfox.wechat_bindings (organization_id, agent_id) WHERE status <> 'REMOVED';
CREATE INDEX wechat_bindings_scan_idx ON openfox.wechat_bindings (organization_id, id) WHERE status = 'ACTIVE';

CREATE TABLE openfox.wechat_inbound (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    binding_id uuid NOT NULL,
    remote_message_id text NOT NULL,
    text text NOT NULL DEFAULT '',
    context_token text NOT NULL DEFAULT '',
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
    FOREIGN KEY (organization_id, binding_id) REFERENCES openfox.wechat_bindings (organization_id, id),
    CONSTRAINT wechat_inbound_status_valid CHECK (status IN ('PENDING', 'PROCESSING', 'DONE', 'IGNORED', 'REJECTED', 'FAILED')),
    CONSTRAINT wechat_inbound_finished_valid CHECK ((status IN ('PENDING', 'PROCESSING')) = (finished_at IS NULL)),
    CONSTRAINT wechat_inbound_text_valid CHECK (
        char_length(btrim(remote_message_id)) BETWEEN 1 AND 256 AND char_length(text) <= 8000 AND char_length(context_token) <= 2048
        AND attempts >= 0 AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 1000 AND char_length(result) <= 64)
);
CREATE UNIQUE INDEX wechat_inbound_remote_unique ON openfox.wechat_inbound (organization_id, binding_id, remote_message_id);
CREATE INDEX wechat_inbound_due_idx ON openfox.wechat_inbound (organization_id, next_attempt_at, received_at) WHERE status IN ('PENDING', 'PROCESSING');
CREATE INDEX wechat_inbound_finished_idx ON openfox.wechat_inbound (organization_id, finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE openfox.wechat_reply_outbox (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    binding_id uuid NOT NULL,
    -- Agent 回复对应的 OpenFox 消息；应用直接入队的提示没有 message_id，正文放 body。
    message_id uuid,
    body text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'PENDING',
    attempts integer NOT NULL DEFAULT 0,
    -- 已发出的分段数：长文本切成多段发送，重试时从未发出的分段继续，client_id = outbox ID + 分段序号。
    sent_parts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, binding_id) REFERENCES openfox.wechat_bindings (organization_id, id),
    FOREIGN KEY (organization_id, message_id) REFERENCES openfox.messages (organization_id, id),
    CONSTRAINT wechat_reply_outbox_body_valid CHECK (message_id IS NOT NULL OR char_length(btrim(body)) > 0),
    CONSTRAINT wechat_reply_outbox_status_valid CHECK (status IN ('PENDING', 'SENDING', 'DONE', 'FAILED', 'SKIPPED')),
    CONSTRAINT wechat_reply_outbox_finished_valid CHECK ((status IN ('DONE', 'FAILED', 'SKIPPED')) = (finished_at IS NOT NULL)),
    CONSTRAINT wechat_reply_outbox_text_valid CHECK (
        char_length(body) <= 8000 AND attempts >= 0 AND sent_parts >= 0 AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 1000)
);
CREATE UNIQUE INDEX wechat_reply_outbox_message_unique ON openfox.wechat_reply_outbox (organization_id, message_id) WHERE message_id IS NOT NULL;
CREATE INDEX wechat_reply_outbox_due_idx ON openfox.wechat_reply_outbox (organization_id, next_attempt_at, created_at) WHERE status IN ('PENDING', 'SENDING');
CREATE INDEX wechat_reply_outbox_finished_idx ON openfox.wechat_reply_outbox (organization_id, finished_at) WHERE finished_at IS NOT NULL;

ALTER TABLE openfox.wechat_qr_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_qr_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_inbound ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_inbound FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_reply_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wechat_reply_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY wechat_qr_sessions_tenant_isolation ON openfox.wechat_qr_sessions
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY wechat_bindings_tenant_isolation ON openfox.wechat_bindings
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY wechat_inbound_tenant_isolation ON openfox.wechat_inbound
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY wechat_reply_outbox_tenant_isolation ON openfox.wechat_reply_outbox
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY wechat_bindings_scan ON openfox.wechat_bindings FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY wechat_inbound_scan ON openfox.wechat_inbound FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY wechat_reply_outbox_scan ON openfox.wechat_reply_outbox FOR SELECT TO openfox_migrator USING (true);

-- Agent 或系统在绑定的会话里发言时，入队发回微信。绑定不是 ACTIVE 时不入队。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_wechat_reply()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.wechat_reply_outbox (organization_id, binding_id, message_id)
    SELECT n.organization_id, b.id, n.id
      FROM changed_new n
      JOIN openfox.wechat_bindings b ON b.organization_id = n.organization_id AND b.conversation_id = n.conversation_id AND b.status = 'ACTIVE'
     WHERE n.author_kind IN ('AGENT', 'SYSTEM')
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_wechat_reply() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_wechat_reply() FROM PUBLIC;

CREATE TRIGGER messages_enqueue_wechat_reply AFTER INSERT ON openfox.messages
    REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_wechat_reply();

-- 轮询管理器：列出需要长轮询的有效绑定（分页）。
CREATE OR REPLACE FUNCTION openfox_private.list_wechat_bindings(p_after_organization uuid, p_after_binding uuid, p_limit integer)
RETURNS TABLE (organization_id uuid, binding_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid wechat binding scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT b.organization_id, b.id
      FROM openfox.wechat_bindings b
      JOIN openfox.organization_directory d ON d.organization_id = b.organization_id AND d.deleted_at IS NULL
     WHERE b.status = 'ACTIVE'
       AND (p_after_organization IS NULL
            OR (b.organization_id, b.id) > (p_after_organization, coalesce(p_after_binding, '00000000-0000-0000-0000-000000000000'::uuid)))
     ORDER BY b.organization_id, b.id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_wechat_bindings(uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_wechat_bindings(uuid, uuid, integer) FROM PUBLIC;

-- worker：有到期的入站消息或回发项（含租约过期）的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_wechat_worker_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid wechat worker scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT i.organization_id FROM openfox.wechat_inbound i
         WHERE (i.status = 'PENDING' AND i.next_attempt_at <= clock_timestamp())
            OR (i.status = 'PROCESSING' AND (i.lease_expires_at IS NULL OR i.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT q.organization_id FROM openfox.wechat_reply_outbox q
         WHERE (q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_wechat_worker_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_wechat_worker_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.wechat_qr_sessions, openfox.wechat_bindings TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.wechat_inbound, openfox.wechat_reply_outbox TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_wechat_bindings(uuid, uuid, integer) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.list_wechat_worker_organizations(uuid, integer) TO openfox_context;
