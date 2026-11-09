-- 派发 worker 自动回收孤儿容量预留：expand-only。
-- 孤儿预留：状态仍为 RESERVED/CLAIMED，但对应 Run 不存在、已到终态，或 Run 已不再指向它。
-- 正常路径里“建预留 + Run 指向预留”“Run 终态 + 释放预留”“offer 过期回收 + 解除指向”
-- 都在同一事务内完成，因此满足条件的行只可能来自异常残留（例如早期测试数据或人工改库）。
-- 残留预留会占用组织 active_max（enforce_runtime_active_limit 按行计数），让后续 Run 一直等待
-- ORGANIZATION_CAPACITY。本迁移只新增一条 migrator 只读策略与一个函数，并替换候选组织函数；
-- 不修改任何表结构，不改写已有数据（回收在运行时由 worker 执行）。

SET ROLE openfox_migrator;

-- 候选组织扫描在无组织上下文中运行，需要看到全部有效预留；只对非登录 migrator 生效，
-- 沿用 000019 的 *_dispatch_scan 模式，不扩大 openfox_app 的可见范围。
CREATE POLICY capacity_reservations_dispatch_scan ON openfox.capacity_reservations
    FOR SELECT TO openfox_migrator USING (true);

-- 返回存在派发工作的组织 ID；在 000019 的基础上增加“存在超龄孤儿预留”。
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
        UNION
        SELECT c.organization_id FROM openfox.capacity_reservations c
          LEFT JOIN openfox.runs r ON r.organization_id = c.organization_id AND r.id = c.run_id
         WHERE c.status IN ('RESERVED', 'CLAIMED')
           AND c.created_at <= clock_timestamp() - interval '1 minute'
           AND (r.id IS NULL
                OR r.state IN ('SUCCEEDED', 'FAILED', 'CANCELED')
                OR r.reservation_id IS DISTINCT FROM c.id)
    )
    SELECT w.organization_id
      FROM work w
     ORDER BY (p_after IS NOT NULL AND w.organization_id <= p_after), w.organization_id
     LIMIT p_limit;
END
$$;

-- 回收当前组织中创建超过 p_min_age_seconds 的孤儿预留，最多 p_limit 条。
-- 仅 organization_worker 上下文可调用。锁序与 release_reservation 一致：
-- 组织容量 → 物理电脑 → Agent → 预留；Run 只做非锁定读，不与“Run → 容量”路径形成环。
-- 加锁后重新判定：并发事务若正在把 Run 置为终态并释放同一预留，本函数读到的仍是旧的
-- 非终态（读已提交），或预留已是 RELEASED，两种情况都会跳过。
-- 计数器只在 > 0 时扣减（用户确认：计数与预留行不一致时仍释放预留，按需扣减），
-- counters_consistent=false 表示至少一个计数器原本已是 0，由调用方记录告警。
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
        SELECT c.status INTO current_status
          FROM openfox.capacity_reservations c
         WHERE c.organization_id = p_organization_id AND c.id = candidate.id
         FOR UPDATE;
        IF current_status IS DISTINCT FROM 'RESERVED' AND current_status IS DISTINCT FROM 'CLAIMED' THEN
            CONTINUE;
        END IF;

        -- 持锁后重新判定孤儿条件。
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

        -- 输出列名与表列同名（reservation_id/run_id），表列一律带别名引用。
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
        UPDATE openfox.agents
           SET reserved_slots = reserved_slots - 1
         WHERE organization_id = p_organization_id AND id = candidate.agent_id AND reserved_slots > 0;
        IF NOT FOUND THEN
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

ALTER FUNCTION openfox_private.list_dispatch_organizations(uuid, integer) OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.dispatch_reclaim_orphan_reservations(uuid, integer, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_dispatch_organizations(uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.dispatch_reclaim_orphan_reservations(uuid, integer, integer) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.list_dispatch_organizations(uuid, integer) TO openfox_context;
GRANT EXECUTE ON FUNCTION openfox_private.dispatch_reclaim_orphan_reservations(uuid, integer, integer) TO openfox_app;
