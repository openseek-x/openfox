-- 阶段 8g「替换电脑」联调发现：Agent 一旦跑过任务，就再也改不了电脑。
--
-- 原因：000002 的外键 capacity_reservations (organization_id, agent_id, device_id)
--   → agents (organization_id, id, device_id) 对所有预留行生效，包括早已 RELEASED 的历史行。
--   只要 Agent 有过一条预留，UPDATE agents SET device_id 就违反外键（23503），
--   Agent 设置里改电脑、以及 8g 的替换电脑都会因此失败（500）。
-- 改法（只放宽约束，不改写任何数据）：
--   1. 新外键 capacity_reservations_agent_fk：(organization_id, agent_id) → agents (organization_id, id)，
--      先 NOT VALID 再 VALIDATE。预留仍然必须属于存在的 Agent。
--   2. 新触发器 capacity_reservations_agent_device：INSERT，或 UPDATE 了 organization_id / agent_id / device_id 时，
--      要求 device_id 等于 Agent 当前的电脑，否则报 23503（与原外键同一错误码，调用方的错误处理不变）。
--      历史行只更新 status / released_at 时不触发，所以 Agent 换电脑后旧预留照常释放。
--      现有写入方 reserve_capacity 在插入预留前已 UPDATE 同一 agents 行（预留计数 +1），行锁已在本事务；
--      并发的换电脑（UPDATE agents SET device_id，属于键更新，取 FOR UPDATE 锁）与它互斥，触发器读到的是最新值。
--   3. 删除旧外键。agents 上的 UNIQUE (organization_id, id, device_id) 不再被引用，保留不动，留给以后的 contract 迁移。
-- RLS：不新增表、不改策略。触发器函数 SECURITY DEFINER、属主 openfox_migrator，
--   通过已有的 agents_execution_trigger_access（openfox_migrator 只读）读取 Agent，与其他执行触发器一致。
-- 锁：ADD CONSTRAINT ... NOT VALID 与 DROP CONSTRAINT 只短暂持有 capacity_reservations 与 agents 的
--   SHARE ROW EXCLUSIVE / ACCESS EXCLUSIVE 锁；VALIDATE CONSTRAINT 持 SHARE UPDATE EXCLUSIVE，不阻塞读写，
--   但要扫描 capacity_reservations 全表一次（每条都按主键查 agents）。CREATE TRIGGER 短暂持 SHARE ROW EXCLUSIVE。
--   单事务完成，其他会话看不到中间状态。
-- 新旧版本共存：旧二进制只会插入「电脑 = Agent 当前电脑」的预留，新约束对它与旧外键等价；
--   旧二进制改 Agent 电脑（Agent 设置）在新库上也能成功。回滚应用只需换回旧二进制，本迁移保留。
-- 预检查询（执行前；必须返回 0。旧外键已保证，这里只是确认）：
--   SELECT count(*) FROM openfox.capacity_reservations r
--     LEFT JOIN openfox.agents a ON a.organization_id = r.organization_id AND a.id = r.agent_id
--    WHERE a.id IS NULL;
-- 验证查询（执行后）：
--   SELECT conname, convalidated FROM pg_constraint
--    WHERE conrelid = 'openfox.capacity_reservations'::regclass AND contype = 'f'
--      AND confrelid = 'openfox.agents'::regclass;                       -- 期望只有 capacity_reservations_agent_fk，convalidated = true
--   SELECT count(*) FROM pg_trigger
--    WHERE tgrelid = 'openfox.capacity_reservations'::regclass
--      AND tgname = 'capacity_reservations_agent_device';               -- 期望 1
-- 回滚：不建议。重新加回旧外键会再次挡住换电脑；且 Agent 换过电脑后，旧外键对历史行 VALIDATE 会失败。
--   确需恢复时（只约束新行）：
--     ALTER TABLE openfox.capacity_reservations
--       ADD CONSTRAINT capacity_reservations_organization_id_agent_id_device_id_fkey
--       FOREIGN KEY (organization_id, agent_id, device_id) REFERENCES openfox.agents (organization_id, id, device_id) NOT VALID;
--     DROP TRIGGER capacity_reservations_agent_device ON openfox.capacity_reservations;
--     DROP FUNCTION openfox_private.enforce_reservation_agent_device();
--     ALTER TABLE openfox.capacity_reservations DROP CONSTRAINT capacity_reservations_agent_fk;

ALTER TABLE openfox.capacity_reservations
    ADD CONSTRAINT capacity_reservations_agent_fk
        FOREIGN KEY (organization_id, agent_id) REFERENCES openfox.agents (organization_id, id)
        NOT VALID;
ALTER TABLE openfox.capacity_reservations VALIDATE CONSTRAINT capacity_reservations_agent_fk;

CREATE FUNCTION openfox_private.enforce_reservation_agent_device()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    agent_device uuid;
BEGIN
    SELECT a.device_id
      INTO agent_device
      FROM openfox.agents AS a
     WHERE a.organization_id = NEW.organization_id
       AND a.id = NEW.agent_id;
    -- Agent 不存在时交给外键 capacity_reservations_agent_fk 报错
    IF FOUND AND agent_device IS DISTINCT FROM NEW.device_id THEN
        RAISE EXCEPTION 'capacity reservation device does not match the Agent computer'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.enforce_reservation_agent_device() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enforce_reservation_agent_device() FROM PUBLIC;

CREATE TRIGGER capacity_reservations_agent_device
    BEFORE INSERT OR UPDATE OF organization_id, agent_id, device_id ON openfox.capacity_reservations
    FOR EACH ROW
    EXECUTE FUNCTION openfox_private.enforce_reservation_agent_device();

ALTER TABLE openfox.capacity_reservations
    DROP CONSTRAINT capacity_reservations_organization_id_agent_id_device_id_fkey;
