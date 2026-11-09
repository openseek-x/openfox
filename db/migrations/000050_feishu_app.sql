-- 阶段 6d-1「飞书应用绑定、长连接、个人绑定、飞书通知」。
--
-- 仅扩展（expand-only）：
--   - user_notification_channels：渠道放宽为 desktop / browser / feishu（没有行 = 默认开启、全部事件，与原有渠道相同）。
--   - integration_connections：飞书应用（scope = ORGANIZATION，external_id = App ID）在所有组织之间唯一（部分唯一，
--     已断开的不算）。同一个飞书应用的长连接事件随机投递到其中一个客户端，绑到两个组织会串组织。
--     6d-2 的任务助手 / Agent 机器人也是组织级飞书连接（account_kind 区分），同样受此约束。
--     个人飞书连接（external_id = open_id）在同一组织、同一应用下唯一：一个飞书用户只对应一个成员。
--   - im_connection_states：每个需要长连接的飞书连接一行：持有长连接的中心实例（租约）、连接状态、最近连上 / 收到事件的时间、
--     最近一次错误。多实例部署时同一连接只由租约持有者建立长连接。
--   - im_inbound_events：长连接事件按 (连接, event_id) 去重（飞书至少投递一次）；应用定期清理 7 天前的记录。
--   - im_notification_outbox：飞书通知出站队列（事务性 outbox）。notifications 写入的同一事务里由触发器入队：
--     收件人在本组织有有效的个人飞书绑定、且其所属飞书应用有效时才入队；发送前再按通知渠道设置过滤。
--     入队时复制通知的类型、摘要和来源（notifications 只允许收件人本人读取）。
--   - 控制面函数 list_im_connections（后台找到需要长连接的飞书连接）、list_im_notification_organizations（有到期通知的组织）。
-- RLS 与其他租户表相同；openfox_app 可读写，两张流水表另给 DELETE 用于保留期清理。
-- 锁：user_notification_channels 换 CHECK 约束（行数 = 修改过通知设置的人数，校验很快）；integration_connections 建部分唯一索引
--   （飞书连接此前不存在，索引为空）；notifications 只新增触发器（CREATE TRIGGER 短暂持有 SHARE ROW EXCLUSIVE 锁）。
-- 回滚：应用回退前先 DROP TRIGGER notifications_enqueue_im_delivery（否则旧版本写通知时仍会入队，但不会有人发送）；
--   保存过 feishu 渠道设置的行需要先删除（DELETE FROM openfox.user_notification_channels WHERE channel = 'feishu'）才能恢复旧 CHECK；
--   确需移除时再 DROP 两个函数与 im_notification_outbox、im_inbound_events、im_connection_states，删除两个唯一索引。

SET ROLE openfox_migrator;

ALTER TABLE openfox.user_notification_channels DROP CONSTRAINT user_notification_channels_channel_valid;
ALTER TABLE openfox.user_notification_channels
    ADD CONSTRAINT user_notification_channels_channel_valid CHECK (channel IN ('desktop', 'browser', 'feishu'));

CREATE UNIQUE INDEX integration_connections_feishu_app_unique
    ON openfox.integration_connections (provider, lower(external_id))
    WHERE provider = 'feishu' AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED';
-- 同一个飞书用户（同一应用下的 open_id）在一个组织里只能绑定一个 OpenFox 成员，否则通知和群里 @ 的操作人会对应不清。
CREATE UNIQUE INDEX integration_connections_feishu_user_unique
    ON openfox.integration_connections (organization_id, parent_connection_id, external_id)
    WHERE provider = 'feishu' AND scope = 'PERSONAL' AND status <> 'DISCONNECTED';

CREATE TABLE openfox.im_connection_states (
    organization_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    state text NOT NULL DEFAULT 'PENDING',
    last_connected_at timestamptz,
    last_event_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    failure_count integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, connection_id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    -- PENDING = 等待某个实例接手；CONNECTING / CONNECTED / RECONNECTING 由持有者维护；FAILED = 鉴权失败等不可自动恢复的错误；
    -- STOPPED = 连接已断开或暂停，不再建立长连接。
    CONSTRAINT im_connection_states_state_valid CHECK (state IN ('PENDING', 'CONNECTING', 'CONNECTED', 'RECONNECTING', 'FAILED', 'STOPPED')),
    CONSTRAINT im_connection_states_text_valid CHECK (
        char_length(lease_owner) <= 128 AND char_length(last_error) <= 500 AND failure_count >= 0)
);

CREATE TABLE openfox.im_inbound_events (
    organization_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    event_id text NOT NULL,
    event_type text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, connection_id, event_id),
    FOREIGN KEY (organization_id, connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    CONSTRAINT im_inbound_events_text_valid CHECK (
        char_length(btrim(event_id)) BETWEEN 1 AND 128 AND char_length(event_type) BETWEEN 1 AND 128)
);
CREATE INDEX im_inbound_events_received_idx ON openfox.im_inbound_events (organization_id, received_at);

CREATE TABLE openfox.im_notification_outbox (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    notification_id uuid NOT NULL,
    recipient_user_id uuid NOT NULL,
    personal_connection_id uuid NOT NULL,
    app_connection_id uuid NOT NULL,
    -- 入队时复制通知的类型、摘要与来源：notifications 的读取策略只允许收件人本人读，worker 不以收件人身份运行。
    kind text NOT NULL,
    safe_summary text NOT NULL,
    task_id uuid,
    conversation_id uuid,
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
    FOREIGN KEY (organization_id, notification_id) REFERENCES openfox.notifications (organization_id, id),
    FOREIGN KEY (organization_id, personal_connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    FOREIGN KEY (organization_id, app_connection_id) REFERENCES openfox.integration_connections (organization_id, id),
    CONSTRAINT im_notification_outbox_status_valid CHECK (status IN ('PENDING', 'SENDING', 'DONE', 'FAILED', 'SKIPPED')),
    CONSTRAINT im_notification_outbox_finished_valid CHECK ((status IN ('DONE', 'FAILED', 'SKIPPED')) = (finished_at IS NOT NULL)),
    CONSTRAINT im_notification_outbox_text_valid CHECK (
        attempts >= 0 AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 1000
        AND char_length(remote_message_id) <= 128 AND char_length(kind) BETWEEN 1 AND 64
        AND char_length(safe_summary) BETWEEN 1 AND 512)
);
CREATE UNIQUE INDEX im_notification_outbox_unique ON openfox.im_notification_outbox (organization_id, notification_id, personal_connection_id);
CREATE INDEX im_notification_outbox_due_idx ON openfox.im_notification_outbox (organization_id, next_attempt_at, created_at)
    WHERE status IN ('PENDING', 'SENDING');
CREATE INDEX im_notification_outbox_finished_idx ON openfox.im_notification_outbox (organization_id, finished_at)
    WHERE finished_at IS NOT NULL;

ALTER TABLE openfox.im_connection_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_connection_states FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_inbound_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_inbound_events FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_notification_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.im_notification_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY im_connection_states_tenant_isolation ON openfox.im_connection_states
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_inbound_events_tenant_isolation ON openfox.im_inbound_events
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_notification_outbox_tenant_isolation ON openfox.im_notification_outbox
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY im_notification_outbox_scan ON openfox.im_notification_outbox FOR SELECT TO openfox_migrator USING (true);

-- 新通知：收件人在本组织有有效的个人飞书绑定、所属飞书应用有效时入队。渠道开关与关闭的事件在发送时判断（设置随时可改）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_notification()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.im_notification_outbox (
        organization_id, notification_id, recipient_user_id, personal_connection_id, app_connection_id,
        kind, safe_summary, task_id, conversation_id)
    SELECT n.organization_id, n.id, n.recipient_user_id, p.id, a.id, n.kind, n.safe_summary, n.task_id, n.conversation_id
      FROM changed_new n
      JOIN openfox.integration_connections p
        ON p.organization_id = n.organization_id AND p.provider = 'feishu' AND p.scope = 'PERSONAL'
       AND p.owner_user_id = n.recipient_user_id AND p.status = 'ACTIVE'
      JOIN openfox.integration_connections a
        ON a.organization_id = p.organization_id AND a.id = p.parent_connection_id
       AND a.scope = 'ORGANIZATION' AND a.account_kind = 'app' AND a.status = 'ACTIVE'
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_im_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_notification() FROM PUBLIC;

CREATE TRIGGER notifications_enqueue_im_delivery AFTER INSERT ON openfox.notifications
    REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_im_notification();

-- 后台长连接管理：需要长连接的飞书连接（组织级、有效、组织未删除），按 (组织, 连接) 分页。
CREATE OR REPLACE FUNCTION openfox_private.list_im_connections(p_after_organization uuid, p_after_connection uuid, p_limit integer)
RETURNS TABLE (organization_id uuid, connection_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im connection scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT c.organization_id, c.id
      FROM openfox.integration_connections c
      JOIN openfox.organization_directory d ON d.organization_id = c.organization_id AND d.deleted_at IS NULL
     WHERE c.provider = 'feishu' AND c.scope = 'ORGANIZATION' AND c.status = 'ACTIVE'
       AND (p_after_organization IS NULL
            OR (c.organization_id, c.id) > (p_after_organization, coalesce(p_after_connection, '00000000-0000-0000-0000-000000000000'::uuid)))
     ORDER BY c.organization_id, c.id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) FROM PUBLIC;

-- 后台通知发送：有到期出站项（含租约过期的 SENDING）的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_im_notification_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im notification scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT DISTINCT q.organization_id
      FROM openfox.im_notification_outbox q
      JOIN openfox.organization_directory d ON d.organization_id = q.organization_id AND d.deleted_at IS NULL
     WHERE ((q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp())))
       AND (p_after IS NULL OR q.organization_id > p_after)
     ORDER BY q.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_notification_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_notification_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.im_connection_states TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.im_inbound_events, openfox.im_notification_outbox TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_im_connections(uuid, uuid, integer) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.list_im_notification_organizations(uuid, integer) TO openfox_context;
