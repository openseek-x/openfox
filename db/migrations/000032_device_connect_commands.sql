-- 阶段 2c「连接电脑与 Runtime」：注册令牌的授权范围、中心 → 电脑的远程命令、Agent 聊天并发独立计数。
--
-- 1. 注册令牌（仅扩展）：
--   - device_registration_tokens.scope：签发时确定的授权范围（PERSONAL / ORGANIZATION），默认 ORGANIZATION，
--     与旧令牌的行为一致。非 guest 成员只能签发 PERSONAL，组织范围仅 OWNER / ADMIN（应用层校验）。
--     000009 对 openfox_app 是列级 INSERT 授权，这里补上 scope 列。
--   - register_device_with_token 按令牌范围写 device_grants：PERSONAL 授权给签发人本人。兑换时再校验一次
--     签发人当前角色：组织范围令牌的签发人已不是 OWNER / ADMIN、或个人范围令牌的签发人变成 GUEST，拒绝兑换。
--     函数签名不变（CREATE OR REPLACE），旧应用调用方式不受影响。
-- 2. 远程命令（新表）：device_commands 承载 Web 上「重新检测 Runtime / 测试连接 / 清理存储」三种命令，
--    经设备 WSS 唤醒下发，电脑回执结果。FORCE RLS 租户隔离；同一台电脑同一种命令同时只能有一条未完成
--    （部分唯一索引，重复点击复用）。保留 7 天，由 openfox_private.purge_device_commands 分批清理。
-- 3. 聊天并发独立计数（改写容量函数，L3）：见第 3 部分开头的说明。
-- 回滚：应用回退后新列、新表闲置即可；函数恢复与计数对账步骤见 db/migrations/README.md 的 000032 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.device_registration_tokens
    ADD COLUMN scope text NOT NULL DEFAULT 'ORGANIZATION';
ALTER TABLE openfox.device_registration_tokens
    ADD CONSTRAINT device_registration_tokens_scope_valid CHECK (scope IN ('PERSONAL', 'ORGANIZATION'));

CREATE OR REPLACE FUNCTION openfox_private.register_device_with_token(
    p_token_hash bytea,
    p_key_id text,
    p_public_key bytea
) RETURNS TABLE (
    device_id uuid,
    organization_id uuid,
    owner_user_id uuid,
    key_generation bigint,
    center_epoch bigint
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private, pg_temp
AS $$
DECLARE
    registration openfox.device_registration_tokens%ROWTYPE;
    member_authorization_version bigint;
    member_role text;
    new_device_id uuid;
    new_epoch bigint;
BEGIN
    IF p_token_hash IS NULL OR octet_length(p_token_hash) <> 32
       OR p_key_id IS NULL OR length(btrim(p_key_id)) = 0 OR length(p_key_id) > 128
       OR p_public_key IS NULL OR octet_length(p_public_key) <> 32 THEN
        RAISE EXCEPTION 'invalid device registration' USING ERRCODE = '22023';
    END IF;

    SELECT *
      INTO registration
      FROM openfox.device_registration_tokens
     WHERE token_hash = p_token_hash
     FOR UPDATE;
    IF NOT FOUND OR registration.consumed_at IS NOT NULL
       OR registration.revoked_at IS NOT NULL
       OR registration.expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'device registration token is not active' USING ERRCODE = '42501';
    END IF;

    SELECT oi.authorization_version, oi.role
      INTO member_authorization_version, member_role
      FROM openfox.user_organization_index AS oi
     WHERE oi.organization_id = registration.organization_id
       AND oi.user_id = registration.issued_by_user_id
       AND oi.membership_status = 'ACTIVE';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'device registration issuer is not active' USING ERRCODE = '42501';
    END IF;
    -- 签发后角色可能变化：按兑换时的角色重新判断签发资格。
    IF (registration.scope = 'ORGANIZATION' AND member_role NOT IN ('OWNER', 'ADMIN'))
       OR (registration.scope = 'PERSONAL' AND member_role = 'GUEST') THEN
        RAISE EXCEPTION 'device registration issuer may not grant this scope' USING ERRCODE = '42501';
    END IF;

    PERFORM openfox_private.set_tenant_context(
        registration.organization_id,
        registration.issued_by_user_id,
        member_authorization_version,
        'user_request'
    );

    new_device_id := gen_random_uuid();
    INSERT INTO openfox.physical_devices (
        id, owner_user_id, device_key, name, install_id, max_slots
    ) VALUES (
        new_device_id,
        registration.issued_by_user_id,
        'device-' || new_device_id::text,
        registration.name,
        registration.install_id,
        registration.max_slots
    );

    INSERT INTO openfox.device_keys (device_id, key_id, public_key)
    VALUES (new_device_id, btrim(p_key_id), p_public_key);

    INSERT INTO openfox.device_grants (organization_id, device_id, scope, scope_user_id)
    VALUES (
        registration.organization_id,
        new_device_id,
        registration.scope,
        CASE WHEN registration.scope = 'PERSONAL' THEN registration.issued_by_user_id END
    );

    SELECT cs.center_epoch INTO new_epoch
      FROM openfox.center_state AS cs
     WHERE cs.id = 1;

    UPDATE openfox.device_registration_tokens
       SET consumed_at = clock_timestamp(), device_id = new_device_id
     WHERE id = registration.id;

    INSERT INTO openfox.audit_events (
        id, organization_id, actor_id, action, decision, reason,
        resource_kind, resource_id, request_id, authorization_version,
        safe_metadata, created_at
    ) VALUES (
        gen_random_uuid(), registration.organization_id, registration.issued_by_user_id,
        'device.registered', 'allow', 'allowed', 'device', new_device_id::text,
        registration.id::text, member_authorization_version,
        jsonb_build_object('status', 'ACTIVE', 'grant_scope', registration.scope),
        clock_timestamp()
    );

    RETURN QUERY SELECT new_device_id, registration.organization_id,
                        registration.issued_by_user_id, 1::bigint, new_epoch;
END
$$;

CREATE TABLE openfox.device_commands (
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    device_id uuid NOT NULL REFERENCES openfox.physical_devices (id),
    kind text NOT NULL,
    params jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'PENDING',
    requested_by_user_id uuid NOT NULL,
    result jsonb,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz,
    completed_at timestamptz,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, requested_by_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT device_commands_kind_valid CHECK (kind IN ('runtime_probe', 'runtime_test', 'storage_cleanup')),
    CONSTRAINT device_commands_status_valid CHECK (status IN ('PENDING', 'DELIVERED', 'SUCCEEDED', 'FAILED', 'EXPIRED')),
    CONSTRAINT device_commands_params_shape CHECK (jsonb_typeof(params) = 'object' AND pg_column_size(params) <= 2048),
    CONSTRAINT device_commands_result_shape CHECK (
        result IS NULL OR (jsonb_typeof(result) = 'object' AND pg_column_size(result) <= 16384)
    ),
    CONSTRAINT device_commands_error_code_valid CHECK (error_code ~ '^[A-Z0-9_]{0,64}$'),
    CONSTRAINT device_commands_expiry_after_create CHECK (expires_at > created_at),
    CONSTRAINT device_commands_completion_valid CHECK (
        (status IN ('PENDING', 'DELIVERED') AND completed_at IS NULL)
        OR (status IN ('SUCCEEDED', 'FAILED', 'EXPIRED') AND completed_at IS NOT NULL)
    )
);

-- 同一台电脑、同一种命令只能有一条未完成：重复点击时应用复用这一条。
CREATE UNIQUE INDEX device_commands_open_unique
    ON openfox.device_commands (organization_id, device_id, kind)
    WHERE status IN ('PENDING', 'DELIVERED');
-- 电脑上线后拉取待执行命令；页面查询某台电脑最近的命令。
CREATE INDEX device_commands_device_idx
    ON openfox.device_commands (organization_id, device_id, created_at DESC);
CREATE INDEX device_commands_created_idx
    ON openfox.device_commands (created_at);

ALTER TABLE openfox.device_commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.device_commands FORCE ROW LEVEL SECURITY;
CREATE POLICY device_commands_tenant_isolation ON openfox.device_commands
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 清理函数以属主身份执行，只能删除 7 天前的行。
CREATE POLICY device_commands_purge ON openfox.device_commands
    FOR DELETE TO openfox_migrator
    USING (created_at < clock_timestamp() - interval '7 days');
CREATE POLICY device_commands_purge_select ON openfox.device_commands
    FOR SELECT TO openfox_migrator
    USING (created_at < clock_timestamp() - interval '7 days');

CREATE OR REPLACE FUNCTION openfox_private.purge_device_commands(p_limit integer)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    v_deleted integer;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'invalid device command purge limit' USING ERRCODE = '22023';
    END IF;
    DELETE FROM openfox.device_commands c
     USING (
         SELECT organization_id, id
           FROM openfox.device_commands
          WHERE created_at < clock_timestamp() - interval '7 days'
          ORDER BY created_at
          LIMIT p_limit
     ) doomed
     WHERE c.organization_id = doomed.organization_id AND c.id = doomed.id;
    GET DIAGNOSTICS v_deleted = ROW_COUNT;
    RETURN v_deleted;
END
$$;

ALTER FUNCTION openfox_private.purge_device_commands(integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.purge_device_commands(integer) FROM PUBLIC;

RESET ROLE;

GRANT INSERT (scope) ON openfox.device_registration_tokens TO openfox_app;
GRANT SELECT, INSERT, UPDATE ON openfox.device_commands TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.purge_device_commands(integer) TO openfox_context;

-- ===== 3. Agent 聊天并发独立计数 =====
-- 之前 reserve_capacity 对所有 Run 都占 agents.max_slots（= 配置的「任务并发」max_concurrency，默认 1），
-- 会话 Run 实际受任务并发限制，配置里的「聊天并发」不生效。改为两个池：
--   - TASK 池：agents.max_slots / reserved_slots（不变），任务 Run 使用；
--   - CHAT 池：agents.chat_max_slots / chat_reserved_slots（新增），task_id 为空的会话 Run 使用。
--     chat_max_slots 由当前配置同步：DEFAULT → 10，MANUAL → chat_concurrency_value，SHARE_TASK → NULL
--     （NULL 表示会话 Run 与任务共用 TASK 池，即旧行为）。
--   - capacity_reservations.pool 记录每条预留占的是哪个池，释放时按它扣对应计数，保证增减一一对应。
--     已有预留全部是 TASK（迁移前它们增加的就是 reserved_slots），默认值 TASK 即正确。
-- 组织容量与电脑容量不分池，仍对所有 Run 生效。
-- 锁序与原函数一致：组织容量 → 物理电脑 → Agent → 预留；Run 行只做非锁定读（task_id 在 Run 创建后不变）。
-- 函数签名全部不变；旧应用代码无需改动即可继续工作（聊天池满时报错文本为 'agent chat capacity exhausted'，
-- 旧代码按「agent」映射为 AGENT_CONCURRENCY，新代码映射为 AGENT_CHAT_CONCURRENCY）。
-- 数据写入：agents 按当前配置回填 chat_max_slots（agents 行数与 Agent 数相同，单条 UPDATE）。
-- runs_wait_reason_valid 改为超集约束，重建约束需要扫描 runs 全表（持 ACCESS EXCLUSIVE 到事务结束），
-- 生产执行前需评估 runs 行数。

SET ROLE openfox_migrator;

ALTER TABLE openfox.agents
    ADD COLUMN chat_max_slots integer DEFAULT 10,
    ADD COLUMN chat_reserved_slots integer NOT NULL DEFAULT 0;
ALTER TABLE openfox.agents
    ADD CONSTRAINT agents_chat_max_slots_nonnegative CHECK (chat_max_slots IS NULL OR chat_max_slots >= 0),
    ADD CONSTRAINT agents_chat_reserved_slots_nonnegative CHECK (chat_reserved_slots >= 0);

ALTER TABLE openfox.capacity_reservations
    ADD COLUMN pool text NOT NULL DEFAULT 'TASK';
ALTER TABLE openfox.capacity_reservations
    ADD CONSTRAINT capacity_reservations_pool_valid CHECK (pool IN ('TASK', 'CHAT'));

ALTER TABLE openfox.runs DROP CONSTRAINT runs_wait_reason_valid;
ALTER TABLE openfox.runs ADD CONSTRAINT runs_wait_reason_valid CHECK (
    wait_reason IN ('', 'ORGANIZATION_CAPACITY', 'AGENT_CONCURRENCY', 'AGENT_CHAT_CONCURRENCY', 'COMPUTER_CONCURRENCY',
                    'CONVERSATION_BUSY', 'WORKDIR_BUSY', 'AWAITING_COMPUTER',
                    'COMPUTER_OFFLINE', 'TASK_GATE', 'OFFER_EXPIRED')
);

-- 配置 → 聊天池上限
CREATE OR REPLACE FUNCTION openfox_private.chat_slots_for(p_mode text, p_value integer)
RETURNS integer
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog
AS $$
    SELECT CASE p_mode
               WHEN 'SHARE_TASK' THEN NULL
               WHEN 'MANUAL' THEN p_value
               ELSE 10
           END
$$;

-- 回填：按每个 Agent 当前配置版本。
UPDATE openfox.agents a
   SET chat_max_slots = openfox_private.chat_slots_for(c.chat_concurrency, c.chat_concurrency_value)
  FROM openfox.agent_config_versions c
 WHERE c.organization_id = a.organization_id
   AND c.agent_id = a.id
   AND c.config_version = a.current_config_version
   AND a.chat_max_slots IS DISTINCT FROM openfox_private.chat_slots_for(c.chat_concurrency, c.chat_concurrency_value);

CREATE OR REPLACE FUNCTION openfox_private.sync_agent_max_slots()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    configured integer;
    chat_mode text;
    chat_value integer;
BEGIN
    IF NEW.current_config_version IS NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.current_config_version IS NOT DISTINCT FROM OLD.current_config_version THEN
        RETURN NEW;
    END IF;
    SELECT c.max_concurrency, c.chat_concurrency, c.chat_concurrency_value
      INTO configured, chat_mode, chat_value
      FROM openfox.agent_config_versions c
     WHERE c.organization_id = NEW.organization_id
       AND c.agent_id = NEW.id
       AND c.config_version = NEW.current_config_version;
    IF FOUND THEN
        NEW.max_slots := configured;
        NEW.chat_max_slots := openfox_private.chat_slots_for(chat_mode, chat_value);
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.sync_agent_max_slots_from_config()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    UPDATE openfox.agents
       SET max_slots = NEW.max_concurrency,
           chat_max_slots = openfox_private.chat_slots_for(NEW.chat_concurrency, NEW.chat_concurrency_value)
     WHERE organization_id = NEW.organization_id
       AND id = NEW.agent_id
       AND current_config_version = NEW.config_version
       AND (max_slots <> NEW.max_concurrency
            OR chat_max_slots IS DISTINCT FROM
               openfox_private.chat_slots_for(NEW.chat_concurrency, NEW.chat_concurrency_value));
    RETURN NULL;
END
$$;

-- 按预留的池扣减 Agent 计数；p_strict=true 时计数已为 0 抛 23514（与原函数一致），
-- false 时返回 false（孤儿回收按需扣减）。调用方已持有 Agent 行锁。
CREATE OR REPLACE FUNCTION openfox_private.release_agent_slot(
    p_organization_id uuid,
    p_agent_id uuid,
    p_pool text,
    p_strict boolean
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_pool = 'CHAT' THEN
        UPDATE openfox.agents
           SET chat_reserved_slots = chat_reserved_slots - 1
         WHERE organization_id = p_organization_id AND id = p_agent_id AND chat_reserved_slots > 0;
    ELSE
        UPDATE openfox.agents
           SET reserved_slots = reserved_slots - 1
         WHERE organization_id = p_organization_id AND id = p_agent_id AND reserved_slots > 0;
    END IF;
    IF NOT FOUND THEN
        IF p_strict THEN
            RAISE EXCEPTION 'Agent capacity counter invariant violated' USING ERRCODE = '23514';
        END IF;
        RETURN false;
    END IF;
    RETURN true;
END
$$;

CREATE OR REPLACE FUNCTION openfox_private.reserve_capacity(
    p_organization_id uuid,
    p_reservation_id uuid,
    p_run_id uuid,
    p_generation bigint,
    p_device_id uuid,
    p_agent_id uuid,
    p_offer_id uuid,
    p_expires_at timestamptz
) RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    run_task_id uuid;
    run_found boolean;
    agent_chat_max integer;
    reservation_pool text;
BEGIN
    IF p_organization_id IS NULL
       OR p_reservation_id IS NULL
       OR p_run_id IS NULL
       OR p_generation IS NULL
       OR p_generation <= 0
       OR p_device_id IS NULL
       OR p_agent_id IS NULL
       OR p_offer_id IS NULL
       OR p_expires_at IS NULL
       OR p_expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'invalid capacity reservation' USING ERRCODE = '22023';
    END IF;
    IF p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match reservation' USING ERRCODE = '42501';
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization capacity not configured' USING ERRCODE = '23503';
    END IF;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = p_device_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'physical device not found' USING ERRCODE = '23503';
    END IF;
    SELECT a.chat_max_slots INTO agent_chat_max
      FROM openfox.agents a
     WHERE a.organization_id = p_organization_id AND a.id = p_agent_id AND a.device_id = p_device_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'agent not found' USING ERRCODE = '23503';
    END IF;

    -- 选池：会话 Run（task_id 为空）且 Agent 配置了独立聊天池时用 CHAT，否则 TASK。
    -- 找不到 Run 时按 TASK 处理（与改造前的行为一致）。
    SELECT r.task_id INTO run_task_id
      FROM openfox.runs r
     WHERE r.organization_id = p_organization_id AND r.id = p_run_id;
    run_found := FOUND;
    reservation_pool := CASE WHEN run_found AND run_task_id IS NULL AND agent_chat_max IS NOT NULL
                             THEN 'CHAT' ELSE 'TASK' END;

    IF NOT EXISTS (
        SELECT 1 FROM openfox.device_grants
        WHERE organization_id = p_organization_id
          AND device_id = p_device_id
          AND status = 'ACTIVE'
    ) THEN
        RAISE EXCEPTION 'device grant is not active' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM openfox.physical_devices
        WHERE id = p_device_id AND status = 'ACTIVE' AND reserved_slots < max_slots
    ) THEN
        RAISE EXCEPTION 'physical device capacity exhausted' USING ERRCODE = '55000';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id AND reserved_slots < max_slots
    ) THEN
        RAISE EXCEPTION 'organization capacity exhausted' USING ERRCODE = '55000';
    END IF;
    IF reservation_pool = 'CHAT' THEN
        IF NOT EXISTS (
            SELECT 1 FROM openfox.agents
            WHERE organization_id = p_organization_id AND id = p_agent_id
              AND status = 'ACTIVE' AND chat_reserved_slots < chat_max_slots
        ) THEN
            RAISE EXCEPTION 'agent chat capacity exhausted' USING ERRCODE = '55000';
        END IF;
    ELSIF NOT EXISTS (
        SELECT 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = p_agent_id
          AND status = 'ACTIVE' AND reserved_slots < max_slots
    ) THEN
        RAISE EXCEPTION 'agent capacity exhausted' USING ERRCODE = '55000';
    END IF;

    UPDATE openfox.organization_capacity
       SET reserved_slots = reserved_slots + 1
     WHERE organization_id = p_organization_id;
    UPDATE openfox.physical_devices
       SET reserved_slots = reserved_slots + 1
     WHERE id = p_device_id;
    IF reservation_pool = 'CHAT' THEN
        UPDATE openfox.agents
           SET chat_reserved_slots = chat_reserved_slots + 1
         WHERE organization_id = p_organization_id AND id = p_agent_id;
    ELSE
        UPDATE openfox.agents
           SET reserved_slots = reserved_slots + 1
         WHERE organization_id = p_organization_id AND id = p_agent_id;
    END IF;

    INSERT INTO openfox.capacity_reservations (
        organization_id, id, run_id, generation, device_id, agent_id, pool
    ) VALUES (
        p_organization_id, p_reservation_id, p_run_id, p_generation, p_device_id, p_agent_id, reservation_pool
    );
    INSERT INTO openfox.dispatch_offers (
        organization_id, id, reservation_id, run_id, generation, target_device_id, expires_at
    ) VALUES (
        p_organization_id, p_offer_id, p_reservation_id, p_run_id, p_generation, p_device_id, p_expires_at
    );
    RETURN p_offer_id;
EXCEPTION
    WHEN unique_violation THEN
        RAISE EXCEPTION 'capacity reservation or offer conflicts with an existing effective operation'
            USING ERRCODE = '23505';
END
$$;

-- 与 000011 版本相同，只把 Agent 计数改为按预留的池扣减。
CREATE OR REPLACE FUNCTION openfox_private.release_reservation(
    p_organization_id uuid,
    p_reservation_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    reservation_device_id uuid;
    reservation_agent_id uuid;
    reservation_status text;
    reservation_pool text;
BEGIN
    IF p_organization_id IS NULL OR p_reservation_id IS NULL
       OR p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match reservation' USING ERRCODE = '42501';
    END IF;
    SELECT device_id, agent_id, status
      INTO reservation_device_id, reservation_agent_id, reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = p_reservation_id;
    IF NOT FOUND OR reservation_status NOT IN ('RESERVED', 'CLAIMED') THEN
        RETURN false;
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = reservation_device_id FOR UPDATE;
    PERFORM 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = reservation_agent_id
        FOR UPDATE;
    SELECT status, pool INTO reservation_status, reservation_pool
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = p_reservation_id
     FOR UPDATE;
    IF reservation_status IS DISTINCT FROM 'RESERVED'
       AND reservation_status IS DISTINCT FROM 'CLAIMED' THEN
        RETURN false;
    END IF;

    UPDATE openfox.dispatch_offers
       SET status = 'CANCELED'
     WHERE organization_id = p_organization_id
       AND reservation_id = p_reservation_id
       AND status = 'LIVE';
    UPDATE openfox.capacity_reservations
       SET status = 'RELEASED', released_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = p_reservation_id
       AND status IN ('RESERVED', 'CLAIMED');
    IF NOT FOUND THEN
        RAISE EXCEPTION 'reservation state changed while releasing' USING ERRCODE = '40001';
    END IF;
    UPDATE openfox.organization_capacity
       SET reserved_slots = reserved_slots - 1
     WHERE organization_id = p_organization_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    UPDATE openfox.physical_devices
       SET reserved_slots = reserved_slots - 1
     WHERE id = reservation_device_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'physical device capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    PERFORM openfox_private.release_agent_slot(p_organization_id, reservation_agent_id, reservation_pool, true);
    RETURN true;
END
$$;

-- 与 000002 版本相同，只把 Agent 计数改为按预留的池扣减。
CREATE OR REPLACE FUNCTION openfox_private.reap_offer(
    p_organization_id uuid,
    p_offer_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    reservation_id uuid;
    reservation_device_id uuid;
    reservation_agent_id uuid;
    offer_status text;
    offer_expiry timestamptz;
    reservation_status text;
    reservation_pool text;
BEGIN
    IF p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match offer' USING ERRCODE = '42501';
    END IF;
    SELECT o.reservation_id, o.status, o.expires_at
      INTO reservation_id, offer_status, offer_expiry
      FROM openfox.dispatch_offers o
     WHERE o.organization_id = p_organization_id AND o.id = p_offer_id;
    IF NOT FOUND OR offer_status <> 'LIVE' OR offer_expiry > clock_timestamp() THEN
        RETURN false;
    END IF;
    SELECT device_id, agent_id, status
      INTO reservation_device_id, reservation_agent_id, reservation_status
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = reservation_id;
    IF NOT FOUND OR reservation_status <> 'RESERVED' THEN
        RETURN false;
    END IF;

    PERFORM 1 FROM openfox.organization_capacity
        WHERE organization_id = p_organization_id FOR UPDATE;
    PERFORM 1 FROM openfox.physical_devices
        WHERE id = reservation_device_id FOR UPDATE;
    PERFORM 1 FROM openfox.agents
        WHERE organization_id = p_organization_id AND id = reservation_agent_id
        FOR UPDATE;
    SELECT status, pool INTO reservation_status, reservation_pool
      FROM openfox.capacity_reservations
     WHERE organization_id = p_organization_id AND id = reservation_id
     FOR UPDATE;
    IF reservation_status IS DISTINCT FROM 'RESERVED' THEN
        RETURN false;
    END IF;
    SELECT status INTO offer_status
      FROM openfox.dispatch_offers
     WHERE organization_id = p_organization_id AND id = p_offer_id
     FOR UPDATE;
    IF offer_status <> 'LIVE'
       OR NOT EXISTS (
           SELECT 1 FROM openfox.dispatch_offers
            WHERE organization_id = p_organization_id AND id = p_offer_id
              AND expires_at <= clock_timestamp()
       ) THEN
        RETURN false;
    END IF;

    UPDATE openfox.dispatch_offers
       SET status = 'EXPIRED'
     WHERE organization_id = p_organization_id AND id = p_offer_id;
    UPDATE openfox.capacity_reservations
       SET status = 'RELEASED', released_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = reservation_id AND status = 'RESERVED';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'reservation state changed while reaping' USING ERRCODE = '40001';
    END IF;
    UPDATE openfox.organization_capacity
       SET reserved_slots = reserved_slots - 1
     WHERE organization_id = p_organization_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'organization capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    UPDATE openfox.physical_devices
       SET reserved_slots = reserved_slots - 1
     WHERE id = reservation_device_id AND reserved_slots > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'physical device capacity counter invariant violated' USING ERRCODE = '23514';
    END IF;
    PERFORM openfox_private.release_agent_slot(p_organization_id, reservation_agent_id, reservation_pool, true);
    RETURN true;
END
$$;

-- 与 000020 版本相同，只把 Agent 计数改为按预留的池扣减（仍是按需扣减）。
CREATE OR REPLACE FUNCTION openfox_private.dispatch_reclaim_orphan_reservations(
    p_organization_id uuid,
    p_min_age_seconds integer,
    p_limit integer
) RETURNS TABLE (reservation_id uuid, run_id uuid, reason text, counters_consistent boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    candidate record;
    current_status text;
    current_pool text;
    run_state text;
    run_reservation uuid;
    run_found boolean;
    orphan_reason text;
    consistent boolean;
BEGIN
    IF openfox_private.current_context_source() IS DISTINCT FROM 'organization_worker' THEN
        RAISE EXCEPTION 'orphan reservation reclaim requires the organization worker context' USING ERRCODE = '42501';
    END IF;
    IF p_organization_id IS NULL OR p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match reservation' USING ERRCODE = '42501';
    END IF;
    IF p_min_age_seconds IS NULL OR p_min_age_seconds < 30 OR p_min_age_seconds > 86400
       OR p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid orphan reclaim bounds' USING ERRCODE = '22023';
    END IF;

    FOR candidate IN
        SELECT c.id, c.run_id, c.device_id, c.agent_id
          FROM openfox.capacity_reservations c
          LEFT JOIN openfox.runs r ON r.organization_id = c.organization_id AND r.id = c.run_id
         WHERE c.organization_id = p_organization_id
           AND c.status IN ('RESERVED', 'CLAIMED')
           AND c.created_at <= clock_timestamp() - make_interval(secs => p_min_age_seconds)
           AND (r.id IS NULL
                OR r.state IN ('SUCCEEDED', 'FAILED', 'CANCELED')
                OR r.reservation_id IS DISTINCT FROM c.id)
         ORDER BY c.created_at, c.id
         LIMIT p_limit
    LOOP
        PERFORM 1 FROM openfox.organization_capacity
            WHERE organization_id = p_organization_id FOR UPDATE;
        PERFORM 1 FROM openfox.physical_devices
            WHERE id = candidate.device_id FOR UPDATE;
        PERFORM 1 FROM openfox.agents
            WHERE organization_id = p_organization_id AND id = candidate.agent_id FOR UPDATE;
        SELECT c.status, c.pool INTO current_status, current_pool
          FROM openfox.capacity_reservations c
         WHERE c.organization_id = p_organization_id AND c.id = candidate.id
         FOR UPDATE;
        IF current_status IS DISTINCT FROM 'RESERVED' AND current_status IS DISTINCT FROM 'CLAIMED' THEN
            CONTINUE;
        END IF;

        SELECT r.state, r.reservation_id INTO run_state, run_reservation
          FROM openfox.runs r
         WHERE r.organization_id = p_organization_id AND r.id = candidate.run_id;
        run_found := FOUND;
        IF NOT run_found THEN
            orphan_reason := 'RUN_MISSING';
        ELSIF run_state IN ('SUCCEEDED', 'FAILED', 'CANCELED') THEN
            orphan_reason := 'RUN_TERMINAL';
        ELSIF run_reservation IS DISTINCT FROM candidate.id THEN
            orphan_reason := 'RUN_DETACHED';
        ELSE
            CONTINUE;
        END IF;

        UPDATE openfox.dispatch_offers o
           SET status = 'CANCELED'
         WHERE o.organization_id = p_organization_id
           AND o.reservation_id = candidate.id
           AND o.status = 'LIVE';
        UPDATE openfox.capacity_reservations
           SET status = 'RELEASED', released_at = clock_timestamp()
         WHERE organization_id = p_organization_id AND id = candidate.id
           AND status IN ('RESERVED', 'CLAIMED');
        IF NOT FOUND THEN
            RAISE EXCEPTION 'reservation state changed while reclaiming' USING ERRCODE = '40001';
        END IF;
        consistent := true;
        UPDATE openfox.organization_capacity
           SET reserved_slots = reserved_slots - 1
         WHERE organization_id = p_organization_id AND reserved_slots > 0;
        IF NOT FOUND THEN
            consistent := false;
        END IF;
        UPDATE openfox.physical_devices
           SET reserved_slots = reserved_slots - 1
         WHERE id = candidate.device_id AND reserved_slots > 0;
        IF NOT FOUND THEN
            consistent := false;
        END IF;
        IF NOT openfox_private.release_agent_slot(p_organization_id, candidate.agent_id, current_pool, false) THEN
            consistent := false;
        END IF;

        reservation_id := candidate.id;
        run_id := candidate.run_id;
        reason := orphan_reason;
        counters_consistent := consistent;
        RETURN NEXT;
    END LOOP;
END
$$;

ALTER FUNCTION openfox_private.chat_slots_for(text, integer) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.release_agent_slot(uuid, uuid, text, boolean) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.chat_slots_for(text, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.release_agent_slot(uuid, uuid, text, boolean) FROM PUBLIC;

RESET ROLE;
