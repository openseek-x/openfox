-- 阶段 12e「电脑管理与运维」：本机并发上限（D-05）、一台电脑加入多个组织（D-09）、设备密钥轮换命令（D-01）。
--
-- 仅扩展（expand-only），不提供 down 迁移；不回填、不改写已有数据：
--   1. physical_devices.local_max_runs：daemon 心跳上报的本机 --max-runs（可空；旧 daemon 不上报为 NULL，派发行为不变）。
--      BEFORE UPDATE OF reserved_slots 触发器在占用增加且超过本机上限时报
--      'physical device capacity exhausted (local limit)'（SQLSTATE 55000），派发侧按既有规则映射为「等待电脑空闲」。
--      生效上限 = LEAST(max_slots, local_max_runs)；只拦截“增加占用”，调小上限不影响已占用的槽位。
--      不改写 reserve_capacity；占用计数本来就按电脑跨组织共用。
--   2. 电脑所有者把电脑加入自己的另一个组织：
--      - openfox_app 获准在插入授权时写 scope / scope_user_id / scope_space_id / status / permission_version
--        （此前只能写 organization_id / device_id，由注册函数代写范围）；RLS 仍把写入限制在当前租户组织。
--      - list_device_owner_organizations：所有者本人的 ACTIVE 组织与这台电脑在各组织的授权状态。
--        调用方传入的 p_user_id 必须是已认证的当前用户；电脑不属于该用户时不返回任何行。
--      - list_device_active_organizations：设备侧列出自己 ACTIVE 授权的组织（daemon 按组织分别领取；密钥轮换确认后按组织写审计）。
--      - device_grants.removed_by_manager_at（可空）：组织 Owner/Admin 移除别人的电脑时记录；非空且授权未激活时
-- 所有者不能自行加回，需该组织 Owner/Admin 重新允许。
--   3. device_keys.predecessor_key_id（可空）：设备侧轮换登记新钥时记录它接替的密钥，确认时只允许吊销自己的前驱。
--   4. device_commands 的 kind 增加 key_rotate（电脑详情页触发本机 daemon 轮换密钥；列表是 000111 的超集）。
--
-- 锁：加可空列不重写表；新 CHECK 只校验新列（全为 NULL，校验很快）；建触发器与改 CHECK 需要短暂的
-- ACCESS EXCLUSIVE 锁（device_commands 的 CHECK 重建会扫描一次该表；该表受过期清理约束，行数很少）。
-- 回滚：回退应用后保留全部对象。旧二进制不调用新函数、不创建 key_rotate 命令、不再改写 local_max_runs——
-- 最后一次上报的本机上限会留在列里继续生效，若要停用：UPDATE openfox.physical_devices SET local_max_runs = NULL;
-- 若确需恢复旧的 kind 约束，须先确认没有 kind = 'key_rotate' 的行。
-- 本文件不依赖 000120 与 000122。

SET ROLE openfox_migrator;

ALTER TABLE openfox.physical_devices
    ADD COLUMN local_max_runs integer;

ALTER TABLE openfox.physical_devices
    ADD CONSTRAINT physical_devices_local_max_runs_range
        CHECK (local_max_runs IS NULL OR local_max_runs BETWEEN 1 AND 1024);

CREATE FUNCTION openfox_private.enforce_device_local_capacity()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF NEW.reserved_slots > OLD.reserved_slots
       AND NEW.local_max_runs IS NOT NULL
       AND NEW.reserved_slots > NEW.local_max_runs THEN
        RAISE EXCEPTION 'physical device capacity exhausted (local limit)' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
ALTER FUNCTION openfox_private.enforce_device_local_capacity() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enforce_device_local_capacity() FROM PUBLIC;

CREATE TRIGGER enforce_device_local_capacity
BEFORE UPDATE OF reserved_slots ON openfox.physical_devices
FOR EACH ROW EXECUTE FUNCTION openfox_private.enforce_device_local_capacity();

-- 被组织管理员移除的标记：组织 Owner/Admin（不是电脑所有者本人）把电脑移出或撤销授权时写入时间；
-- 之后电脑所有者不能自行把电脑加回该组织，直到该组织的 Owner/Admin「重新允许」（清空）。
-- 所有者自己退出不写。授权重新激活时清空。
ALTER TABLE openfox.device_grants
    ADD COLUMN removed_by_manager_at timestamptz;

-- 所有者本人的 ACTIVE 组织 + 这台电脑在各组织的授权（没有授权时为 NULL）。
-- 只在电脑属于 p_user_id 且电脑 ACTIVE 时返回行；调用方传入的 p_user_id 必须是已认证的当前用户。
CREATE FUNCTION openfox_private.list_device_owner_organizations(p_device_id uuid, p_user_id uuid)
RETURNS TABLE (organization_id uuid, slug text, name text, role text,
        grant_status text, grant_scope text, permission_version bigint, rejoin_blocked boolean)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT o.organization_id, o.slug, o.name, i.role, g.status, g.scope, g.permission_version,
           COALESCE(g.status <> 'ACTIVE' AND g.removed_by_manager_at IS NOT NULL, false)
      FROM openfox.physical_devices d
      JOIN openfox.user_organization_index i
        ON i.user_id = p_user_id AND i.membership_status = 'ACTIVE'
      JOIN openfox.organization_directory o
        ON o.organization_id = i.organization_id AND o.deleted_at IS NULL
      LEFT JOIN openfox.device_grants g
        ON g.organization_id = i.organization_id AND g.device_id = d.id
     WHERE d.id = p_device_id AND d.owner_user_id = p_user_id AND d.status = 'ACTIVE'
     ORDER BY o.created_at, o.organization_id
     LIMIT 100
$$;
ALTER FUNCTION openfox_private.list_device_owner_organizations(uuid, uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_device_owner_organizations(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.list_device_owner_organizations(uuid, uuid) TO openfox_app;

-- 设备侧：本机 ACTIVE 授权的组织（与 resolve_device_organization 判定一致），按授权创建时间排序。
CREATE FUNCTION openfox_private.list_device_active_organizations(p_device_id uuid)
RETURNS TABLE (organization_id uuid, permission_version bigint)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
    SELECT g.organization_id, g.permission_version
      FROM openfox.device_grants g
      JOIN openfox.physical_devices d ON d.id = g.device_id
     WHERE g.device_id = p_device_id
       AND g.status = 'ACTIVE'
       AND d.status = 'ACTIVE'
     ORDER BY g.created_at, g.organization_id
     LIMIT 100
$$;
ALTER FUNCTION openfox_private.list_device_active_organizations(uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_device_active_organizations(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.list_device_active_organizations(uuid) TO openfox_app;

-- 密钥轮换的前驱关系：设备侧登记新钥时记下「它接替的是哪把钥」。确认吊销时只允许新钥吊销自己的前驱，
-- 方向不能反过来（旧钥的会话不能吊销接替它的新钥）。该列写入后不再修改，吊销操作不会动它。
-- 注册与用户侧轮换建立的密钥没有前驱（NULL），不能用于确认吊销。
ALTER TABLE openfox.device_keys
    ADD COLUMN predecessor_key_id text;

ALTER TABLE openfox.device_commands DROP CONSTRAINT device_commands_kind_valid,
    ADD CONSTRAINT device_commands_kind_valid CHECK (kind IN (
        'runtime_probe', 'runtime_test', 'storage_cleanup', 'skill_scan', 'skill_import',
        'device_settings', 'workspace_path', 'runtime_session_export', 'key_rotate'
    ));

RESET ROLE;

GRANT UPDATE (local_max_runs) ON openfox.physical_devices TO openfox_app;
GRANT INSERT (scope, scope_user_id, scope_space_id, status, permission_version)
    ON openfox.device_grants TO openfox_app;
GRANT UPDATE (removed_by_manager_at) ON openfox.device_grants TO openfox_app;
