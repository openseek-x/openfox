-- 阶段 12e：审计查询索引与配额用量读取。只做扩展，不改写、不删除任何数据；没有 down 迁移。
--
-- 1. 审计列表（GET /v1/organizations/{id}/audit-events）按 (created_at, id) 倒序做游标分页。
--    无筛选与按时间范围筛选走 000004 已有的 audit_events_organization_created_idx；
--    按操作人、动作、资源类型筛选且命中行很少时，仅靠该索引会沿时间倒序扫过大量不匹配的行
--    （隔离库 60 万行实测退化为全表顺序扫描），因此补三个「组织 + 筛选列 + 排序列」索引。
--    动作索引使用 text_pattern_ops：等值与前缀 LIKE 都可用，且不依赖数据库的排序规则。
--
--    锁影响：迁移运行器在事务内执行每个文件，无法使用 CONCURRENTLY。CREATE INDEX 持有
--    audit_events 的 SHARE 锁，建索引期间读取不受影响、写入（所有带审计的写接口）会等待。
--    三个索引的构建时间与行数成正比（隔离库 60 万行合计约 1 秒）；审计表受保留期约束，
--    数据量大的环境请在低峰期执行。
--
-- 2. openfox_app 获得 organization_capacity 的 SELECT，用于配额页显示平台槽位上限与生效值。
--    该表已启用并强制行级安全（000002 的 organization_capacity_tenant_isolation），只能读到当前组织的一行；
--    计数的写入仍只能经由容量函数，不新增任何写权限。
--
-- 回滚：回退应用即可，索引与授权保留无害。确需移除时：
--   DROP INDEX openfox.audit_events_org_actor_created_idx, openfox.audit_events_org_action_created_idx,
--       openfox.audit_events_org_resource_kind_created_idx;
--   REVOKE SELECT ON openfox.organization_capacity FROM openfox_app;

SET ROLE openfox_migrator;

CREATE INDEX audit_events_org_actor_created_idx
    ON openfox.audit_events (organization_id, actor_id, created_at, id);

CREATE INDEX audit_events_org_action_created_idx
    ON openfox.audit_events (organization_id, action text_pattern_ops, created_at, id);

CREATE INDEX audit_events_org_resource_kind_created_idx
    ON openfox.audit_events (organization_id, resource_kind, created_at, id);

GRANT SELECT ON openfox.organization_capacity TO openfox_app;

RESET ROLE;
