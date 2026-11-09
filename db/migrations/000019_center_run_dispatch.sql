-- 中心 Run 派发（WP-09 收尾）：expand-only。
-- 1. runs 增加启动/租约/排队期限/失败码事实列；
-- 2. 派发扫描所需的部分索引；
-- 3. 只返回组织 ID 的跨组织派发候选函数，worker 随后逐个进入组织上下文；
-- 4. Agent 槽位随当前配置 max_concurrency 同步，组织槽位按 active_max 初始化；
-- 5. 应用角色只获得按上下文来源限定的派发包装函数，不直接执行 reserve/claim/reap 容量函数。
-- 本迁移不删除、不重写任何已有业务数据；回填只把“未使用的 0 槽位”提升为配置值。

SET ROLE openfox_migrator;

ALTER TABLE openfox.runs
    ADD COLUMN started_at timestamptz,
    ADD COLUMN lease_expires_at timestamptz,
    ADD COLUMN queue_expires_at timestamptz,
    ADD COLUMN failure_code text NOT NULL DEFAULT '';

ALTER TABLE openfox.runs
    ADD CONSTRAINT runs_failure_code_valid CHECK (
        failure_code IN ('', 'CREDENTIAL_REJECTED', 'QUOTA_EXHAUSTED', 'RATE_LIMITED',
                         'PROVIDER_OVERLOADED', 'MODEL_NOT_FOUND', 'CONTEXT_TOO_LARGE',
                         'IMAGE_TOO_LARGE', 'NETWORK_UNREACHABLE', 'WORKTREE_PREP_FAILED',
                         'REPO_UNAVAILABLE', 'RUNTIME_UNAVAILABLE', 'QUEUE_TIMEOUT',
                         'RUN_TIMEOUT', 'AUTHORIZATION_REVOKED', 'UNKNOWN_PROCESS',
                         'PREPARE_FAILED', 'INTERNAL')
    );

-- 待派发输入按组织 + 创建序扫描；ASSIGNED/CONSUMED 历史行不进入该索引。
CREATE INDEX conversation_inputs_pending_idx
    ON openfox.conversation_inputs (organization_id, created_at, id)
    WHERE status = 'PENDING';
-- 排队超时与租约过期扫描。
CREATE INDEX runs_queue_expiry_idx
    ON openfox.runs (organization_id, queue_expires_at, id)
    WHERE state = 'QUEUED';
CREATE INDEX runs_lease_expiry_idx
    ON openfox.runs (organization_id, lease_expires_at, id)
    WHERE lease_expires_at IS NOT NULL
      AND reconciliation_required = false
      AND state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED');

-- security-definer 函数以 migrator 身份执行且受 FORCE RLS 约束；以下只读策略
-- 仅对非登录 migrator 生效，沿用 000011 的触发器可见性模式，不扩大 openfox_app。
CREATE POLICY conversation_inputs_dispatch_scan ON openfox.conversation_inputs
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY runs_dispatch_scan ON openfox.runs
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY dispatch_offers_dispatch_scan ON openfox.dispatch_offers
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY agent_config_versions_capacity_sync ON openfox.agent_config_versions
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY organization_runtime_configs_capacity_sync ON openfox.organization_runtime_configs
    FOR SELECT TO openfox_migrator USING (true);

-- 返回存在派发工作的组织 ID（按 ID 轮转，游标之后优先、再回绕）。
-- 只暴露组织 ID，不返回任何正文、Run 或成员信息；worker 必须再逐个进入
-- organization_worker 租户上下文处理。
CREATE OR REPLACE FUNCTION openfox_private.list_dispatch_organizations(
    p_after uuid,
    p_limit integer
) RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid dispatch organization limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    WITH work AS (
        SELECT i.organization_id FROM openfox.conversation_inputs i WHERE i.status = 'PENDING'
        UNION
        SELECT r.organization_id FROM openfox.runs r WHERE r.state = 'QUEUED'
        UNION
        SELECT o.organization_id FROM openfox.dispatch_offers o
         WHERE o.status = 'LIVE' AND o.expires_at <= clock_timestamp()
        UNION
        SELECT r.organization_id FROM openfox.runs r
         WHERE r.lease_expires_at IS NOT NULL
           AND r.lease_expires_at <= clock_timestamp()
           AND r.reconciliation_required = false
           AND r.state IN ('PREPARING', 'RUNNING', 'CANCEL_REQUESTED')
    )
    SELECT w.organization_id
      FROM work w
     ORDER BY (p_after IS NOT NULL AND w.organization_id <= p_after), w.organization_id
     LIMIT p_limit;
END
$$;

ALTER FUNCTION openfox_private.list_dispatch_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_dispatch_organizations(uuid, integer) FROM PUBLIC;

-- Agent 槽位以当前配置版本的 max_concurrency 为准。BEFORE 触发器只改写 NEW，
-- 不需要给应用角色开放 agents.max_slots 的列更新权限。
CREATE OR REPLACE FUNCTION openfox_private.sync_agent_max_slots()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    configured integer;
BEGIN
    IF NEW.current_config_version IS NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.current_config_version IS NOT DISTINCT FROM OLD.current_config_version THEN
        RETURN NEW;
    END IF;
    SELECT c.max_concurrency INTO configured
      FROM openfox.agent_config_versions c
     WHERE c.organization_id = NEW.organization_id
       AND c.agent_id = NEW.id
       AND c.config_version = NEW.current_config_version;
    IF FOUND THEN
        NEW.max_slots := configured;
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.sync_agent_max_slots() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.sync_agent_max_slots() FROM PUBLIC;

CREATE TRIGGER agents_sync_max_slots_trigger
BEFORE INSERT OR UPDATE OF current_config_version ON openfox.agents
FOR EACH ROW EXECUTE FUNCTION openfox_private.sync_agent_max_slots();

-- 创建 Agent 时先插入 agents（current_config_version = 1）再插入配置行，
-- 上面的 BEFORE 触发器此时还读不到配置；配置插入后再按当前版本同步一次。
CREATE OR REPLACE FUNCTION openfox_private.sync_agent_max_slots_from_config()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    UPDATE openfox.agents
       SET max_slots = NEW.max_concurrency
     WHERE organization_id = NEW.organization_id
       AND id = NEW.agent_id
       AND current_config_version = NEW.config_version
       AND max_slots <> NEW.max_concurrency;
    RETURN NULL;
END
$$;

ALTER FUNCTION openfox_private.sync_agent_max_slots_from_config() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.sync_agent_max_slots_from_config() FROM PUBLIC;

CREATE TRIGGER agent_config_versions_sync_max_slots
AFTER INSERT ON openfox.agent_config_versions
FOR EACH ROW EXECUTE FUNCTION openfox_private.sync_agent_max_slots_from_config();

-- 组织首次获得运行配置时，把“从未使用的 0 槽位”初始化为 active_max。
-- 只在 INSERT 时生效；之后管理员修改 active_max 仍由 000015 的
-- LEAST(max_slots, active_max) 触发器约束，不隐式改写已有容量配额。
CREATE OR REPLACE FUNCTION openfox_private.init_organization_capacity()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    UPDATE openfox.organization_capacity
       SET max_slots = NEW.active_max
     WHERE organization_id = NEW.organization_id
       AND max_slots = 0
       AND reserved_slots = 0;
    RETURN NULL;
END
$$;

ALTER FUNCTION openfox_private.init_organization_capacity() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.init_organization_capacity() FROM PUBLIC;

CREATE TRIGGER organization_runtime_configs_init_capacity
AFTER INSERT ON openfox.organization_runtime_configs
FOR EACH ROW EXECUTE FUNCTION openfox_private.init_organization_capacity();

-- 回填：逐个组织安装上下文（FORCE RLS 下 migrator 也需要租户上下文）。
-- 只改写 max_slots = 0 且无占用的组织容量，以及配置与槽位不一致的 Agent；
-- 重复执行结果相同。
DO $$
DECLARE
    org record;
BEGIN
    FOR org IN
        SELECT organization_id FROM openfox.user_organization_index
        UNION
        SELECT organization_id FROM openfox.organization_directory
    LOOP
        PERFORM openfox_private.set_tenant_context(org.organization_id,
            '00000000-0000-0000-0000-000000000000', 1, 'organization_worker');
        UPDATE openfox.organization_capacity c
           SET max_slots = rc.active_max
          FROM openfox.organization_runtime_configs rc
         WHERE rc.organization_id = c.organization_id
           AND c.organization_id = org.organization_id
           AND c.max_slots = 0
           AND c.reserved_slots = 0;
        UPDATE openfox.agents a
           SET max_slots = cv.max_concurrency
          FROM openfox.agent_config_versions cv
         WHERE cv.organization_id = a.organization_id
           AND cv.agent_id = a.id
           AND cv.config_version = a.current_config_version
           AND a.organization_id = org.organization_id
           AND a.max_slots <> cv.max_concurrency;
    END LOOP;
    IF to_regclass('pg_temp.openfox_tenant_context') IS NOT NULL THEN
        TRUNCATE TABLE pg_temp.openfox_tenant_context;
    END IF;
END
$$;

-- 结束一个仍处于 QUEUED 的 Run（用户取消或排队超时）。
-- 与 claim_offer 使用同一锁序：组织容量 → 物理电脑 → Agent → Run，
-- 因此不会出现“claim 已提交、取消却释放了 CLAIMED 预留”的竞争。
-- Run 行被其他事务持有时使用 SKIP LOCKED 返回 BUSY，由调用方稍后重试，
-- 避免与先锁 Run 再释放容量的结果路径形成死锁。
-- 返回：DONE（已结束）、NOT_QUEUED（状态已变化）、BUSY（Run 正被处理）。
CREATE OR REPLACE FUNCTION openfox_private.finish_queued_run(
    p_organization_id uuid,
    p_run_id uuid,
    p_state text,
    p_failure_code text,
    p_reason text
) RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    run_state text;
    run_reservation uuid;
    reservation_device uuid;
    reservation_agent uuid;
    reservation_status text;
    locked_run uuid;
    locked_reservation uuid;
BEGIN
    IF p_organization_id IS NULL OR p_run_id IS NULL
       OR p_organization_id <> openfox_private.current_organization_id() THEN
        RAISE EXCEPTION 'tenant context does not match Run' USING ERRCODE = '42501';
    END IF;
    IF p_state NOT IN ('CANCELED', 'FAILED') OR p_failure_code IS NULL
       OR p_reason IS NULL OR length(p_reason) > 1024 THEN
        RAISE EXCEPTION 'invalid queued Run completion' USING ERRCODE = '22023';
    END IF;

    SELECT r.state, r.reservation_id INTO run_state, run_reservation
      FROM openfox.runs r
     WHERE r.organization_id = p_organization_id AND r.id = p_run_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Run not found' USING ERRCODE = '23503';
    END IF;
    IF run_state <> 'QUEUED' THEN
        RETURN 'NOT_QUEUED';
    END IF;

    locked_reservation := run_reservation;
    IF run_reservation IS NOT NULL THEN
        SELECT device_id, agent_id INTO reservation_device, reservation_agent
          FROM openfox.capacity_reservations
         WHERE organization_id = p_organization_id AND id = run_reservation;
        PERFORM 1 FROM openfox.organization_capacity
            WHERE organization_id = p_organization_id FOR UPDATE;
        PERFORM 1 FROM openfox.physical_devices
            WHERE id = reservation_device FOR UPDATE;
        PERFORM 1 FROM openfox.agents
            WHERE organization_id = p_organization_id AND id = reservation_agent FOR UPDATE;
    END IF;

    SELECT r.id INTO locked_run
      FROM openfox.runs r
     WHERE r.organization_id = p_organization_id AND r.id = p_run_id
     FOR UPDATE SKIP LOCKED;
    IF NOT FOUND THEN
        RETURN 'BUSY';
    END IF;
    SELECT r.state, r.reservation_id INTO run_state, run_reservation
      FROM openfox.runs r
     WHERE r.organization_id = p_organization_id AND r.id = p_run_id;
    IF run_state <> 'QUEUED' THEN
        RETURN 'NOT_QUEUED';
    END IF;
    -- 加锁期间出现了新的（或不同的）预留：此时未持有容量锁，继续释放会形成
    -- Run → 容量的反向锁序，返回 BUSY 让调用方重试。
    IF run_reservation IS DISTINCT FROM locked_reservation THEN
        RETURN 'BUSY';
    END IF;

    IF run_reservation IS NOT NULL THEN
        SELECT status INTO reservation_status
          FROM openfox.capacity_reservations
         WHERE organization_id = p_organization_id AND id = run_reservation
         FOR UPDATE;
        IF reservation_status = 'CLAIMED' THEN
            -- QUEUED 与 CLAIMED 同时出现说明不变量被破坏，拒绝而不是释放。
            RAISE EXCEPTION 'queued Run has a claimed reservation' USING ERRCODE = '23514';
        END IF;
        IF reservation_status = 'RESERVED' THEN
            PERFORM openfox_private.release_reservation(p_organization_id, run_reservation);
        END IF;
    END IF;

    UPDATE openfox.runs
       SET state = p_state,
           failure_code = p_failure_code,
           reason = p_reason,
           wait_reason = '',
           cancel_requested_at = CASE WHEN p_state = 'CANCELED' THEN clock_timestamp() ELSE NULL END,
           finished_at = clock_timestamp(),
           version = version + 1,
           updated_at = clock_timestamp()
     WHERE organization_id = p_organization_id AND id = p_run_id AND state = 'QUEUED';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Run state changed while finishing' USING ERRCODE = '40001';
    END IF;
    RETURN 'DONE';
END
$$;

ALTER FUNCTION openfox_private.finish_queued_run(uuid, uuid, text, text, text) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.finish_queued_run(uuid, uuid, text, text, text) FROM PUBLIC;

-- 读取当前事务租户上下文的来源；只供下面的包装函数在定义者权限下使用，不授予运行时角色。
CREATE OR REPLACE FUNCTION openfox_private.current_context_source()
RETURNS text
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox_private, pg_temp
AS $$
DECLARE
    context_schema oid;
    context_source text;
BEGIN
    context_schema := pg_my_temp_schema();
    IF context_schema = 0 OR NOT EXISTS (
        SELECT 1 FROM pg_class
         WHERE relnamespace = context_schema AND relname = 'openfox_tenant_context'
    ) THEN
        RETURN NULL;
    END IF;
    EXECUTE 'SELECT source FROM pg_temp.openfox_tenant_context LIMIT 1' INTO context_source;
    RETURN context_source;
END
$$;

-- 派发 worker 预留容量：仅 organization_worker 上下文可调用，其余校验由 reserve_capacity 完成。
CREATE OR REPLACE FUNCTION openfox_private.dispatch_reserve_capacity(
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
BEGIN
    IF openfox_private.current_context_source() IS DISTINCT FROM 'organization_worker' THEN
        RAISE EXCEPTION 'capacity reservation requires the organization worker context' USING ERRCODE = '42501';
    END IF;
    RETURN openfox_private.reserve_capacity(p_organization_id, p_reservation_id, p_run_id, p_generation,
        p_device_id, p_agent_id, p_offer_id, p_expires_at);
END
$$;

-- 派发 worker 回收过期 offer：仅 organization_worker 上下文可调用。
CREATE OR REPLACE FUNCTION openfox_private.dispatch_reap_offer(
    p_organization_id uuid,
    p_offer_id uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF openfox_private.current_context_source() IS DISTINCT FROM 'organization_worker' THEN
        RAISE EXCEPTION 'offer reaping requires the organization worker context' USING ERRCODE = '42501';
    END IF;
    RETURN openfox_private.reap_offer(p_organization_id, p_offer_id);
END
$$;

-- 设备领取 offer：仅 device_request 上下文，且上下文 actor 必须就是领取设备本身。
CREATE OR REPLACE FUNCTION openfox_private.device_claim_offer(
    p_organization_id uuid,
    p_offer_id uuid,
    p_device_id uuid,
    p_generation bigint
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF openfox_private.current_context_source() IS DISTINCT FROM 'device_request'
       OR p_device_id IS DISTINCT FROM openfox_private.current_actor_id() THEN
        RAISE EXCEPTION 'offer claim requires the claiming device context' USING ERRCODE = '42501';
    END IF;
    RETURN openfox_private.claim_offer(p_organization_id, p_offer_id, p_device_id, p_generation);
END
$$;

ALTER FUNCTION openfox_private.current_context_source() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.dispatch_reserve_capacity(uuid, uuid, uuid, bigint, uuid, uuid, uuid, timestamptz) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.dispatch_reap_offer(uuid, uuid) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.device_claim_offer(uuid, uuid, uuid, bigint) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.current_context_source() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.dispatch_reserve_capacity(uuid, uuid, uuid, bigint, uuid, uuid, uuid, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.dispatch_reap_offer(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.device_claim_offer(uuid, uuid, uuid, bigint) FROM PUBLIC;

RESET ROLE;

-- 应用角色仍不能直接执行 reserve_capacity / claim_offer / reap_offer（000002 的不变量保持不变）；
-- 只授予按上下文来源限定的包装函数。finish_queued_run 是本迁移新增的窄函数，
-- 供 worker 与用户取消在应用角色下调用，内部校验 current_organization_id()。
GRANT EXECUTE ON FUNCTION openfox_private.dispatch_reserve_capacity(uuid, uuid, uuid, bigint, uuid, uuid, uuid, timestamptz)
    TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.dispatch_reap_offer(uuid, uuid) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.device_claim_offer(uuid, uuid, uuid, bigint) TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.finish_queued_run(uuid, uuid, text, text, text) TO openfox_app;
-- 候选组织列表在建立租户上下文之前调用，仅授予 context 角色。
GRANT EXECUTE ON FUNCTION openfox_private.list_dispatch_organizations(uuid, integer) TO openfox_context;
