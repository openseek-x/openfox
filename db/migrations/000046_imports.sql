-- 阶段 6c「导入」：导入任务、导入条目、来源编号映射、导入新建的配置、外部账号。
--
-- 仅扩展（expand-only）：
--   - import_jobs：一次导入一行。状态 DRAFT（上传后配置中）→ QUEUED → RUNNING → SUCCEEDED / FAILED / CANCELLED，
--     运行中请求取消为 CANCEL_REQUESTED，完成后 7 天内可回滚为 ROLLED_BACK；草稿可丢弃为 DISCARDED。
--     config 保存列映射、状态映射、成员映射与选项（config_version 防并发覆盖）；lease_owner / lease_expires_at 是后台执行租约，
--     服务重启后租约过期即由 worker 从断点继续。credentials_sealed 只给 API 导入（6c-2 Jira Cloud）用，导入结束即清空。
--   - import_items：每个来源条目一行（ordinal = 文件中的行序），保存原始行（raw）、处理结果与写入的任务；
--     task_version 记录本条写完时的任务版本，回滚预览据此判断「导入后被改过」。
--   - import_mappings：空间 + 来源系统 + 来源实例 + 外部编号 → 任务，部分唯一索引保证同一空间重复导入不会重复创建任务；
--     回滚时写 released_at 释放映射，之后再导入会重新创建。
--   - import_created_resources：本次导入新建的状态 / 标签 / 工作流状态，回滚时清理其中已无任务使用的。
--   - external_accounts：来源系统的用户（保留原平台姓名，不占成员名额）。可挂一条邀请（invitation_id），
--     对方接受邀请入组后由 worker 关联到成员（linked_user_id）并把对应任务的处理人改为该成员。
--   - tasks.external_assignee_id：处理人是外部账号时的引用（可空）。
--   - 导入事务内设置事务级标记 openfox.import_job（set_config(..., true)），评论自动化与状态变更自动化的触发函数
--     带此标记时跳过，避免批量导入触发自动化或唤醒 Agent。
--   - list_import_organizations：后台 worker 找到有待执行导入、过期草稿或待关联外部账号的组织（控制面，只返回组织 ID）。
-- RLS 与其他租户表相同；openfox_app 可读写。
-- 锁：tasks 加可空列与外键（NOT VALID 后立即 VALIDATE，新列全为 NULL，校验瞬时完成）；其余只建新表、替换函数。
-- 回滚：应用回退后新表与新列闲置即可（旧版本不读）；两个触发函数的新逻辑只在标记存在时生效，可保留。
-- 确需移除时：恢复 000040 / 000044 的两个触发函数，DROP FUNCTION list_import_organizations，
--   ALTER TABLE tasks DROP COLUMN external_assignee_id，再 DROP TABLE import_created_resources, import_mappings,
--   import_items, import_jobs, external_accounts。

SET ROLE openfox_migrator;

CREATE TABLE openfox.import_jobs (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    source text NOT NULL,
    source_label text NOT NULL DEFAULT '',
    file_name text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'DRAFT',
    phase text NOT NULL DEFAULT '',
    config jsonb NOT NULL DEFAULT '{}',
    config_version bigint NOT NULL DEFAULT 1,
    preview jsonb NOT NULL DEFAULT '{}',
    credentials_sealed bytea,
    credentials_key_id text NOT NULL DEFAULT '',
    total_count integer NOT NULL DEFAULT 0,
    created_count integer NOT NULL DEFAULT 0,
    updated_count integer NOT NULL DEFAULT 0,
    skipped_count integer NOT NULL DEFAULT 0,
    failed_count integer NOT NULL DEFAULT 0,
    warnings jsonb NOT NULL DEFAULT '[]',
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    operation_id text NOT NULL,
    created_by uuid NOT NULL,
    started_by uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    cancel_requested_at timestamptz,
    retry_at timestamptz,
    rolled_back_at timestamptz,
    rolled_back_by uuid,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, started_by) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, rolled_back_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT import_jobs_source_valid CHECK (source IN ('jira_csv', 'jira_cloud')),
    CONSTRAINT import_jobs_status_valid CHECK (status IN (
        'DRAFT', 'QUEUED', 'RUNNING', 'CANCEL_REQUESTED', 'SUCCEEDED', 'FAILED', 'CANCELLED', 'ROLLED_BACK', 'DISCARDED')),
    CONSTRAINT import_jobs_phase_valid CHECK (phase IN ('', 'validating', 'fetching', 'importing', 'linking', 'finishing')),
    CONSTRAINT import_jobs_started_valid CHECK (status IN ('DRAFT', 'DISCARDED') OR (started_at IS NOT NULL AND started_by IS NOT NULL)),
    CONSTRAINT import_jobs_finished_valid CHECK (
        (status IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'ROLLED_BACK')) = (finished_at IS NOT NULL)),
    CONSTRAINT import_jobs_rolled_back_valid CHECK ((status = 'ROLLED_BACK') = (rolled_back_at IS NOT NULL AND rolled_back_by IS NOT NULL)),
    CONSTRAINT import_jobs_counts_valid CHECK (
        total_count >= 0 AND created_count >= 0 AND updated_count >= 0 AND skipped_count >= 0 AND failed_count >= 0),
    CONSTRAINT import_jobs_text_valid CHECK (
        char_length(source_label) <= 255 AND char_length(file_name) <= 255
        AND char_length(error_code) <= 64 AND char_length(error_message) <= 1000
        AND operation_id ~ '^op_[0-9a-z]{8,32}$' AND char_length(lease_owner) <= 128
        AND char_length(credentials_key_id) <= 64),
    CONSTRAINT import_jobs_credentials_size CHECK (credentials_sealed IS NULL OR octet_length(credentials_sealed) <= 16384),
    CONSTRAINT import_jobs_json_shape CHECK (
        jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 262144
        AND jsonb_typeof(preview) = 'object' AND pg_column_size(preview) <= 262144
        AND jsonb_typeof(warnings) = 'array' AND pg_column_size(warnings) <= 65536),
    CONSTRAINT import_jobs_versions_positive CHECK (version > 0 AND config_version > 0 AND attempts >= 0)
);
CREATE UNIQUE INDEX import_jobs_operation_unique ON openfox.import_jobs (organization_id, operation_id);
CREATE INDEX import_jobs_list_idx ON openfox.import_jobs (organization_id, created_at DESC, id DESC)
    WHERE status NOT IN ('DRAFT', 'DISCARDED');
CREATE INDEX import_jobs_active_idx ON openfox.import_jobs (organization_id, lease_expires_at)
    WHERE status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED');
-- 同一空间同一时间只允许一个排队 / 执行中的导入，避免两次导入并发写同一组映射。
CREATE UNIQUE INDEX import_jobs_space_active_unique ON openfox.import_jobs (organization_id, space_id)
    WHERE status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED');

CREATE TABLE openfox.import_items (
    organization_id uuid NOT NULL,
    job_id uuid NOT NULL,
    ordinal integer NOT NULL,
    external_id text NOT NULL,
    external_key text NOT NULL DEFAULT '',
    parent_external_id text NOT NULL DEFAULT '',
    raw jsonb NOT NULL,
    status text NOT NULL DEFAULT 'PENDING',
    action text NOT NULL DEFAULT '',
    task_id uuid,
    task_version bigint,
    error text NOT NULL DEFAULT '',
    processed_at timestamptz,
    PRIMARY KEY (organization_id, job_id, ordinal),
    FOREIGN KEY (organization_id, job_id) REFERENCES openfox.import_jobs (organization_id, id),
    FOREIGN KEY (organization_id, task_id) REFERENCES openfox.tasks (organization_id, id),
    CONSTRAINT import_items_ordinal_valid CHECK (ordinal >= 0),
    CONSTRAINT import_items_status_valid CHECK (status IN ('PENDING', 'CREATED', 'UPDATED', 'SKIPPED', 'FAILED')),
    CONSTRAINT import_items_action_valid CHECK (action IN ('', 'created', 'updated', 'skipped_existing', 'skipped_deleted',
        'skipped_filtered', 'skipped_invalid', 'failed')),
    CONSTRAINT import_items_processed_valid CHECK ((status = 'PENDING') = (processed_at IS NULL)),
    CONSTRAINT import_items_task_valid CHECK (status NOT IN ('CREATED', 'UPDATED') OR (task_id IS NOT NULL AND task_version IS NOT NULL)),
    CONSTRAINT import_items_text_valid CHECK (
        char_length(btrim(external_id)) BETWEEN 1 AND 255 AND char_length(external_key) <= 255
        AND char_length(parent_external_id) <= 255 AND char_length(error) <= 1000),
    CONSTRAINT import_items_raw_shape CHECK (jsonb_typeof(raw) = 'object' AND pg_column_size(raw) <= 1048576)
);
CREATE INDEX import_items_pending_idx ON openfox.import_items (organization_id, job_id, ordinal) WHERE status = 'PENDING';
CREATE INDEX import_items_task_idx ON openfox.import_items (organization_id, task_id) WHERE task_id IS NOT NULL;

CREATE TABLE openfox.import_mappings (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    space_id uuid NOT NULL,
    source_system text NOT NULL,
    source_instance text NOT NULL DEFAULT '',
    external_id text NOT NULL,
    external_key text NOT NULL DEFAULT '',
    task_id uuid NOT NULL,
    created_job_id uuid NOT NULL,
    last_job_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id) REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, task_id) REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, created_job_id) REFERENCES openfox.import_jobs (organization_id, id),
    FOREIGN KEY (organization_id, last_job_id) REFERENCES openfox.import_jobs (organization_id, id),
    CONSTRAINT import_mappings_source_valid CHECK (source_system ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT import_mappings_text_valid CHECK (
        char_length(source_instance) <= 255 AND char_length(btrim(external_id)) BETWEEN 1 AND 255
        AND char_length(external_key) <= 255)
);
CREATE UNIQUE INDEX import_mappings_live_unique
    ON openfox.import_mappings (organization_id, space_id, source_system, lower(source_instance), external_id)
    WHERE released_at IS NULL;
CREATE INDEX import_mappings_task_idx ON openfox.import_mappings (organization_id, task_id);
CREATE INDEX import_mappings_job_idx ON openfox.import_mappings (organization_id, created_job_id) WHERE released_at IS NULL;

CREATE TABLE openfox.import_created_resources (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL,
    kind text NOT NULL,
    resource_id uuid NOT NULL,
    space_id uuid,
    task_type_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    cleaned_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, job_id) REFERENCES openfox.import_jobs (organization_id, id),
    CONSTRAINT import_created_resources_kind_valid CHECK (kind IN ('STATUS', 'LABEL', 'WORKFLOW_STATUS')),
    CONSTRAINT import_created_resources_workflow_valid CHECK (
        kind <> 'WORKFLOW_STATUS' OR (space_id IS NOT NULL AND task_type_id IS NOT NULL))
);
-- WORKFLOW_STATUS 的 resource_id 是状态 ID，同一状态加入多个任务类型的工作流时按 task_type_id 区分。
CREATE UNIQUE INDEX import_created_resources_unique ON openfox.import_created_resources
    (organization_id, job_id, kind, resource_id, COALESCE(task_type_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE TABLE openfox.external_accounts (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    external_user_id text NOT NULL,
    display_name text NOT NULL,
    email text NOT NULL DEFAULT '',
    invitation_id uuid,
    linked_user_id uuid,
    linked_at timestamptz,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    version bigint NOT NULL DEFAULT 1,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (invitation_id) REFERENCES openfox.invitations (id),
    FOREIGN KEY (organization_id, linked_user_id) REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT external_accounts_provider_valid CHECK (provider ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT external_accounts_linked_valid CHECK ((linked_user_id IS NULL) = (linked_at IS NULL)),
    CONSTRAINT external_accounts_text_valid CHECK (
        char_length(btrim(external_user_id)) BETWEEN 1 AND 255
        AND char_length(btrim(display_name)) BETWEEN 1 AND 200
        AND char_length(email) <= 320),
    CONSTRAINT external_accounts_version_positive CHECK (version > 0)
);
CREATE UNIQUE INDEX external_accounts_unique ON openfox.external_accounts (organization_id, provider, lower(external_user_id));
CREATE INDEX external_accounts_pending_link_idx ON openfox.external_accounts (organization_id, invitation_id)
    WHERE invitation_id IS NOT NULL AND linked_user_id IS NULL;

ALTER TABLE openfox.tasks ADD COLUMN external_assignee_id uuid;
ALTER TABLE openfox.tasks
    ADD CONSTRAINT tasks_external_assignee_fk FOREIGN KEY (organization_id, external_assignee_id)
        REFERENCES openfox.external_accounts (organization_id, id) NOT VALID;
ALTER TABLE openfox.tasks VALIDATE CONSTRAINT tasks_external_assignee_fk;
CREATE INDEX tasks_external_assignee_idx ON openfox.tasks (organization_id, external_assignee_id)
    WHERE external_assignee_id IS NOT NULL;

ALTER TABLE openfox.import_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_items FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_mappings FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_created_resources ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.import_created_resources FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.external_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.external_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY import_jobs_tenant_isolation ON openfox.import_jobs
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY import_items_tenant_isolation ON openfox.import_items
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY import_mappings_tenant_isolation ON openfox.import_mappings
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY import_created_resources_tenant_isolation ON openfox.import_created_resources
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY external_accounts_tenant_isolation ON openfox.external_accounts
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- 控制面扫描（list_import_organizations 以 openfox_migrator 身份执行）只读。
CREATE POLICY import_jobs_scan ON openfox.import_jobs FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY external_accounts_scan ON openfox.external_accounts FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY invitations_import_scan ON openfox.invitations FOR SELECT TO openfox_migrator USING (true);

-- 导入事务（带 openfox.import_job 标记）写的评论不触发评论自动化（其余与 000044 相同）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_automation_comment()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF coalesce(current_setting('openfox.import_job', true), '') <> '' THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.automation_events (organization_id, kind, space_id, task_id, comment_id, actor_user_id)
    SELECT n.organization_id, 'comment', n.space_id, n.task_id, n.id, n.author_user_id
      FROM changed_new n
     WHERE n.author_agent_id IS NULL
       AND EXISTS (SELECT 1 FROM openfox.automation_triggers t
                    WHERE t.organization_id = n.organization_id AND t.space_id = n.space_id
                      AND t.kind = 'comment' AND t.enabled);
    RETURN NULL;
END
$$;

-- 导入事务（覆盖导入改状态）不触发状态变更自动化（其余与 000040 相同）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_automation_task_status()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF coalesce(current_setting('openfox.import_job', true), '') <> '' THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.automation_events (organization_id, kind, space_id, task_id, status_id, actor_user_id)
    SELECT n.organization_id, 'task_status', n.space_id, n.id, n.status_id,
           NULLIF(openfox_private.current_actor_id(), '00000000-0000-0000-0000-000000000000'::uuid)
      FROM changed_new n
      JOIN changed_old o ON o.organization_id = n.organization_id AND o.id = n.id
     WHERE n.status_id IS DISTINCT FROM o.status_id
       AND n.deleted_at IS NULL
       AND EXISTS (SELECT 1 FROM openfox.automation_triggers t
                    WHERE t.organization_id = n.organization_id AND t.space_id = n.space_id
                      AND t.kind = 'task_status' AND t.enabled);
    RETURN NULL;
END
$$;

-- 后台 worker：有待执行导入（排队、租约过期、请求取消、限流等待到期）、超过 24 小时的草稿或待关联外部账号（邀请已接受）的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_import_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid import scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT j.organization_id FROM openfox.import_jobs j
         WHERE j.status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED')
           AND (j.lease_expires_at IS NULL OR j.lease_expires_at <= clock_timestamp())
           AND (j.retry_at IS NULL OR j.retry_at <= clock_timestamp())
        UNION
        SELECT j.organization_id FROM openfox.import_jobs j
         WHERE j.status = 'DRAFT' AND j.created_at <= clock_timestamp() - interval '24 hours'
        UNION
        SELECT a.organization_id FROM openfox.external_accounts a
          JOIN openfox.invitations i ON i.id = a.invitation_id
         WHERE a.linked_user_id IS NULL AND i.status = 'ACCEPTED'
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_import_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_import_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.import_jobs, openfox.import_items, openfox.import_mappings,
    openfox.import_created_resources, openfox.external_accounts TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_import_organizations(uuid, integer) TO openfox_context;
