-- 阶段 2a「电脑管理」：电脑名称与并发上限可由所有者修改。
--
-- physical_devices 是跨组织的控制面表（没有 RLS），不能给 openfox_app 直接 UPDATE 名称与并发列：
-- 那样任何组织的应用请求都能改任意电脑。改为 SECURITY DEFINER 函数，在函数内校验
--   1) 当前操作人是电脑所有者；2) 当前组织对这台电脑有 ACTIVE 授权；3) 设置版本一致。
-- settings_version 是设置类修改的乐观锁；心跳每 20 秒会递增 version，不能复用它。
-- 并发上限低于当前已预留数时允许保存：只影响后续预留（reserve 只在 reserved_slots < max_slots 时成功），
-- 已在运行的任务不受影响。
-- 仅扩展（expand-only）：新增一列（带默认值）与一个函数，不改写已有数据、不放宽已有 GRANT 或 RLS。
-- 回滚：应用回退后列与函数闲置即可；确需移除时见 db/migrations/README.md 的 000030 条目。

SET ROLE openfox_migrator;

ALTER TABLE openfox.physical_devices
    ADD COLUMN settings_version bigint NOT NULL DEFAULT 1;
ALTER TABLE openfox.physical_devices
    ADD CONSTRAINT physical_devices_settings_version_positive CHECK (settings_version > 0);

-- 返回新的 settings_version；版本不一致返回 NULL（应用映射为 409 VERSION_CONFLICT）。
-- 不是所有者、没有当前组织的有效授权或电脑已吊销：抛 42501（应用映射为 404，避免探测）。
CREATE OR REPLACE FUNCTION openfox_private.update_device_settings(
    p_device_id uuid,
    p_expected_version bigint,
    p_name text,
    p_max_slots integer
) RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    v_org uuid := openfox_private.current_organization_id();
    v_actor uuid := openfox_private.current_actor_id();
    v_owner uuid;
    v_version bigint;
BEGIN
    IF p_device_id IS NULL OR p_expected_version IS NULL OR p_expected_version < 1
       OR (p_name IS NOT NULL AND (length(btrim(p_name)) = 0 OR length(p_name) > 100))
       OR (p_max_slots IS NOT NULL AND (p_max_slots < 1 OR p_max_slots > 256)) THEN
        RAISE EXCEPTION 'invalid device settings' USING ERRCODE = '22023';
    END IF;
    IF v_org IS NULL OR v_actor IS NULL THEN
        RAISE EXCEPTION 'tenant context required' USING ERRCODE = '42501';
    END IF;
    SELECT d.owner_user_id INTO v_owner
      FROM openfox.physical_devices d
     WHERE d.id = p_device_id AND d.status <> 'REVOKED'
       AND EXISTS (
           SELECT 1 FROM openfox.device_grants g
            WHERE g.organization_id = v_org AND g.device_id = d.id AND g.status = 'ACTIVE'
       )
     FOR UPDATE OF d;
    IF NOT FOUND OR v_owner IS DISTINCT FROM v_actor THEN
        RAISE EXCEPTION 'device not manageable' USING ERRCODE = '42501';
    END IF;
    UPDATE openfox.physical_devices
       SET name = COALESCE(btrim(p_name), name),
           max_slots = COALESCE(p_max_slots, max_slots),
           settings_version = settings_version + 1,
           updated_at = clock_timestamp()
     WHERE id = p_device_id AND settings_version = p_expected_version
     RETURNING settings_version INTO v_version;
    RETURN v_version;
END
$$;

ALTER FUNCTION openfox_private.update_device_settings(uuid, bigint, text, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.update_device_settings(uuid, bigint, text, integer) FROM PUBLIC;

RESET ROLE;

GRANT EXECUTE ON FUNCTION openfox_private.update_device_settings(uuid, bigint, text, integer) TO openfox_app;
