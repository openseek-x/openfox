-- 阶段 1：通用创建幂等记录，以及收件箱区分「等你审阅」的通知类型。
--
-- 1. api_idempotency_keys：创建类接口（任务、标签、分组、状态、关联类型、字段、选项、链接、关联）
--    的 Idempotency-Key 记录。作用域为 组织 × 操作人 × 操作 × 键，保留 24 小时；
--    请求摘要不同返回 409 IDEMPOTENCY_CONFLICT，相同则按 resource_id 返回资源当前状态。
--    行在创建资源的同一事务内写入，主键冲突让并发的重复请求排队等待前一个提交。
--    RLS 除组织隔离外再限定 actor_id = current_actor_id()，成员互相看不到对方的键。
-- 2. notifications.kind 增加 HANDOFF_REVIEW（会话交接为「等你审阅」）。
--    旧的 HANDOFF 继续表示「等你回复」。放宽 CHECK 只接受更多取值，已有数据全部满足新约束；
--    重建约束需要一次全表校验，持有 ACCESS EXCLUSIVE 锁的时间与 notifications 行数成正比。
-- 不改写已有数据，不放宽已有表的 GRANT 或 RLS 策略。
-- 回滚：应用回退后新表闲置；确需移除时见 db/migrations/README.md 的 000027 条目。

SET ROLE openfox_migrator;

CREATE TABLE openfox.api_idempotency_keys (
    organization_id uuid NOT NULL REFERENCES openfox.organizations (id),
    actor_id uuid NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    resource_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, actor_id, operation, idempotency_key),
    FOREIGN KEY (organization_id, actor_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT api_idempotency_keys_operation_valid CHECK (operation ~ '^[a-z][a-z0-9_.-]{1,63}$'),
    CONSTRAINT api_idempotency_keys_key_valid CHECK (idempotency_key ~ '^[A-Za-z0-9_-]{8,128}$'),
    CONSTRAINT api_idempotency_keys_hash_valid CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT api_idempotency_keys_expiry_valid CHECK (expires_at > created_at)
);

CREATE INDEX api_idempotency_keys_expiry_idx ON openfox.api_idempotency_keys (organization_id, expires_at);

ALTER TABLE openfox.api_idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.api_idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY api_idempotency_keys_actor_isolation ON openfox.api_idempotency_keys
    USING (
        organization_id = openfox_private.current_organization_id()
        AND actor_id = openfox_private.current_actor_id()
    )
    WITH CHECK (
        organization_id = openfox_private.current_organization_id()
        AND actor_id = openfox_private.current_actor_id()
    );

ALTER TABLE openfox.notifications
    DROP CONSTRAINT notifications_kind_valid,
    ADD CONSTRAINT notifications_kind_valid CHECK (
        kind IN ('MENTION', 'HANDOFF', 'HANDOFF_REVIEW', 'REPLY', 'TASK_COMMENT', 'RUN_FAILED', 'TASK_ASSIGNED')
    );

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.api_idempotency_keys TO openfox_app;
