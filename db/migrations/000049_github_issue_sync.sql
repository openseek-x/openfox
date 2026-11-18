-- 阶段 6b-2「GitHub Issue ↔ 空间同步」。
--
-- 仅扩展（expand-only）：
--   - integration_sync_links：同一连接下一个仓库只能关联一个空间（部分唯一）；一个空间最多一个双向同步（部分唯一，
--     双向空间里新建任务时要确定在哪个仓库建 Issue）；新增 last_synced_at。导入已有 Issue 的进度放在 settings.backfill。
--   - task_issue_links：任务 ↔ Issue（同步关联下一个 Issue 只对应一个任务，一个任务只对应一个 Issue）。
--     last_hash 是最近一次两边一致时的「标题 + 正文 + 开关状态 + 处理人」摘要，用来识别自己写回 GitHub 后收到的回声事件。
--   - task_comment_links：OpenFox 评论 ↔ GitHub 评论，双向去重。
--   - integration_sync_outbox：出站队列（事务性 outbox）。任务 / 评论写入的同一事务里由触发器入队，后台 worker 发送到 GitHub，
--     失败按退避重试；同一任务同类待发送项合并为一条（部分唯一），发送时读取任务的当前内容。
--   - 触发器 tasks_enqueue_issue_sync（INSERT / UPDATE，语句级）、task_comments_enqueue_issue_sync（INSERT）：
--     只处理有效的双向同步；事务级标记 openfox.sync_source（入站写入）或 openfox.import_job（导入）存在时跳过，避免回环与批量建 Issue。
--   - list_integration_sync_organizations：后台 worker 找到有到期出站项或正在导入已有 Issue 的组织（控制面，只返回组织 ID）。
-- RLS 与其他租户表相同；openfox_app 可读写。
-- 锁：integration_sync_links 建两个部分唯一索引（表在 6a 才上线、尚无同步关联）；tasks / task_comments 只新增触发器（CREATE TRIGGER
--   短暂持有 SHARE ROW EXCLUSIVE 锁）。
-- 回滚：应用回退前先 DROP 两个触发器（否则旧版本应用写任务时仍会入队，但不会有人发送，队列只增长不影响功能）；
--   确需移除时再 DROP 函数与 integration_sync_outbox, task_comment_links, task_issue_links，删除两个索引与 last_synced_at。

SET ROLE openfox_migrator;

ALTER TABLE openfox.integration_sync_links ADD COLUMN last_synced_at timestamptz;
CREATE UNIQUE INDEX integration_sync_links_repo_unique
    ON openfox.integration_sync_links (organization_id, connection_id, lower(external_project_id))
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX integration_sync_links_bidirectional_unique
    ON openfox.integration_sync_links (organization_id, space_id)
    WHERE deleted_at IS NULL AND direction = 'BIDIRECTIONAL';

CREATE TABLE openfox.task_issue_links (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    sync_link_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    repository text NOT NULL,
    issue_number integer NOT NULL,
    issue_id text NOT NULL DEFAULT '',
    url text NOT NULL,
    state text NOT NULL,
    last_hash text NOT NULL DEFAULT '',
    remote_updated_at timestamptz,
    backlink_comment_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    unlinked_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, sync_link_id) REFERENCES openfox.integration_sync_links (organization_id, id),
    FOREIGN KEY (organization_id, space_id, task_id) REFERENCES openfox.tasks (organization_id, space_id, id),
    CONSTRAINT task_issue_links_state_valid CHECK (state IN ('OPEN', 'CLOSED')),
    CONSTRAINT task_issue_links_text_valid CHECK (
        char_length(btrim(repository)) BETWEEN 1 AND 255 AND issue_number > 0 AND char_length(issue_id) <= 64
        AND char_length(url) BETWEEN 1 AND 2048 AND char_length(last_hash) <= 128 AND char_length(backlink_comment_id) <= 64)
);
CREATE UNIQUE INDEX task_issue_links_issue_unique ON openfox.task_issue_links (organization_id, sync_link_id, issue_number)
    WHERE unlinked_at IS NULL;
CREATE UNIQUE INDEX task_issue_links_task_unique ON openfox.task_issue_links (organization_id, task_id)
    WHERE unlinked_at IS NULL;

CREATE TABLE openfox.task_comment_links (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    issue_link_id uuid NOT NULL,
    comment_id uuid NOT NULL,
    remote_comment_id text NOT NULL,
    direction text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, issue_link_id) REFERENCES openfox.task_issue_links (organization_id, id),
    FOREIGN KEY (organization_id, comment_id) REFERENCES openfox.task_comments (organization_id, id),
    CONSTRAINT task_comment_links_direction_valid CHECK (direction IN ('INBOUND', 'OUTBOUND')),
    CONSTRAINT task_comment_links_remote_valid CHECK (char_length(btrim(remote_comment_id)) BETWEEN 1 AND 64)
);
CREATE UNIQUE INDEX task_comment_links_remote_unique ON openfox.task_comment_links (organization_id, issue_link_id, remote_comment_id);
CREATE UNIQUE INDEX task_comment_links_comment_unique ON openfox.task_comment_links (organization_id, comment_id);

CREATE TABLE openfox.integration_sync_outbox (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    sync_link_id uuid NOT NULL,
    task_id uuid NOT NULL,
    comment_id uuid,
    kind text NOT NULL,
    actor_user_id uuid,
    status text NOT NULL DEFAULT 'PENDING',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, sync_link_id) REFERENCES openfox.integration_sync_links (organization_id, id),
    FOREIGN KEY (organization_id, task_id) REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, comment_id) REFERENCES openfox.task_comments (organization_id, id),
    CONSTRAINT integration_sync_outbox_kind_valid CHECK (kind IN ('ISSUE_CREATE', 'ISSUE_UPDATE', 'COMMENT_CREATE')),
    CONSTRAINT integration_sync_outbox_comment_valid CHECK ((kind = 'COMMENT_CREATE') = (comment_id IS NOT NULL)),
    CONSTRAINT integration_sync_outbox_status_valid CHECK (status IN ('PENDING', 'SENDING', 'DONE', 'FAILED', 'SKIPPED')),
    CONSTRAINT integration_sync_outbox_finished_valid CHECK ((status IN ('DONE', 'FAILED', 'SKIPPED')) = (finished_at IS NOT NULL)),
    CONSTRAINT integration_sync_outbox_text_valid CHECK (attempts >= 0 AND char_length(lease_owner) <= 128 AND char_length(last_error) <= 1000)
);
-- 同一任务同类待发送项只保留一条（ISSUE_UPDATE 合并；ISSUE_CREATE 不会重复入队）；评论每条一项。
CREATE UNIQUE INDEX integration_sync_outbox_pending_task_unique ON openfox.integration_sync_outbox (organization_id, task_id, kind)
    WHERE status IN ('PENDING', 'SENDING') AND kind IN ('ISSUE_CREATE', 'ISSUE_UPDATE');
CREATE UNIQUE INDEX integration_sync_outbox_comment_unique ON openfox.integration_sync_outbox (organization_id, comment_id)
    WHERE comment_id IS NOT NULL;
CREATE INDEX integration_sync_outbox_due_idx ON openfox.integration_sync_outbox (organization_id, next_attempt_at, created_at)
    WHERE status IN ('PENDING', 'SENDING');

ALTER TABLE openfox.task_issue_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_issue_links FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_comment_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_comment_links FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_sync_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.integration_sync_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY task_issue_links_tenant_isolation ON openfox.task_issue_links
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_comment_links_tenant_isolation ON openfox.task_comment_links
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY integration_sync_outbox_tenant_isolation ON openfox.integration_sync_outbox
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY integration_sync_outbox_scan ON openfox.integration_sync_outbox FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY integration_sync_links_scan ON openfox.integration_sync_links FOR SELECT TO openfox_migrator USING (true);

-- 入站写入（openfox.sync_source）与导入（openfox.import_job）都不入队。
CREATE OR REPLACE FUNCTION openfox_private.issue_sync_suppressed()
RETURNS boolean
LANGUAGE sql
STABLE
SET search_path = pg_catalog
AS $$
    SELECT coalesce(current_setting('openfox.sync_source', true), '') <> ''
        OR coalesce(current_setting('openfox.import_job', true), '') <> ''
$$;
ALTER FUNCTION openfox_private.issue_sync_suppressed() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.issue_sync_suppressed() FROM PUBLIC;

-- 新建任务：所在空间有有效的双向同步时入队 ISSUE_CREATE。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_issue_sync_task_insert()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF openfox_private.issue_sync_suppressed() THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.integration_sync_outbox (organization_id, sync_link_id, task_id, kind, actor_user_id)
    SELECT n.organization_id, l.id, n.id, 'ISSUE_CREATE',
           NULLIF(openfox_private.current_actor_id(), '00000000-0000-0000-0000-000000000000'::uuid)
      FROM changed_new n
      JOIN openfox.integration_sync_links l
        ON l.organization_id = n.organization_id AND l.space_id = n.space_id
       AND l.deleted_at IS NULL AND l.direction = 'BIDIRECTIONAL' AND l.status = 'ACTIVE'
     WHERE n.deleted_at IS NULL
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;

-- 修改任务：标题 / 描述 / 状态 / 第一位人员处理人变化，且任务已关联 Issue、同步为有效的双向同步时入队（合并为一条）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_issue_sync_task_update()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF openfox_private.issue_sync_suppressed() THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.integration_sync_outbox (organization_id, sync_link_id, task_id, kind, actor_user_id)
    SELECT n.organization_id, il.sync_link_id, n.id, 'ISSUE_UPDATE',
           NULLIF(openfox_private.current_actor_id(), '00000000-0000-0000-0000-000000000000'::uuid)
      FROM changed_new n
      JOIN changed_old o ON o.organization_id = n.organization_id AND o.id = n.id
      JOIN openfox.task_issue_links il ON il.organization_id = n.organization_id AND il.task_id = n.id AND il.unlinked_at IS NULL
      JOIN openfox.integration_sync_links l
        ON l.organization_id = il.organization_id AND l.id = il.sync_link_id
       AND l.deleted_at IS NULL AND l.direction = 'BIDIRECTIONAL' AND l.status = 'ACTIVE'
     WHERE n.deleted_at IS NULL
       AND (n.title IS DISTINCT FROM o.title OR n.description IS DISTINCT FROM o.description
            OR n.status_id IS DISTINCT FROM o.status_id OR n.assignee_id IS DISTINCT FROM o.assignee_id)
    ON CONFLICT (organization_id, task_id, kind) WHERE status IN ('PENDING', 'SENDING') AND kind IN ('ISSUE_CREATE', 'ISSUE_UPDATE')
    DO UPDATE SET updated_at = clock_timestamp(), actor_user_id = EXCLUDED.actor_user_id;
    RETURN NULL;
END
$$;

-- 新评论：任务已关联 Issue、同步为有效的双向同步时入队 COMMENT_CREATE（任务的 Issue 还在创建中时同样入队，worker 会等待）。
CREATE OR REPLACE FUNCTION openfox_private.enqueue_issue_sync_comment()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF openfox_private.issue_sync_suppressed() THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.integration_sync_outbox (organization_id, sync_link_id, task_id, comment_id, kind, actor_user_id)
    SELECT n.organization_id, l.id, n.task_id, n.id, 'COMMENT_CREATE', n.author_user_id
      FROM changed_new n
      JOIN openfox.tasks t ON t.organization_id = n.organization_id AND t.id = n.task_id AND t.deleted_at IS NULL
      JOIN openfox.integration_sync_links l
        ON l.organization_id = n.organization_id AND l.space_id = t.space_id
       AND l.deleted_at IS NULL AND l.direction = 'BIDIRECTIONAL' AND l.status = 'ACTIVE'
     WHERE EXISTS (SELECT 1 FROM openfox.task_issue_links il
                    WHERE il.organization_id = n.organization_id AND il.task_id = n.task_id
                      AND il.sync_link_id = l.id AND il.unlinked_at IS NULL)
        OR EXISTS (SELECT 1 FROM openfox.integration_sync_outbox q
                    WHERE q.organization_id = n.organization_id AND q.task_id = n.task_id
                      AND q.kind = 'ISSUE_CREATE' AND q.status IN ('PENDING', 'SENDING'))
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_issue_sync_task_insert() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.enqueue_issue_sync_task_update() OWNER TO openfox_migrator;
ALTER FUNCTION openfox_private.enqueue_issue_sync_comment() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_issue_sync_task_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.enqueue_issue_sync_task_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.enqueue_issue_sync_comment() FROM PUBLIC;

CREATE TRIGGER tasks_enqueue_issue_sync_insert AFTER INSERT ON openfox.tasks
    REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_issue_sync_task_insert();
CREATE TRIGGER tasks_enqueue_issue_sync_update AFTER UPDATE ON openfox.tasks
    REFERENCING OLD TABLE AS changed_old NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_issue_sync_task_update();
CREATE TRIGGER task_comments_enqueue_issue_sync AFTER INSERT ON openfox.task_comments
    REFERENCING NEW TABLE AS changed_new FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_issue_sync_comment();

-- 后台 worker：有到期出站项（含租约过期的 SENDING）或正在导入已有 Issue 的组织。
CREATE OR REPLACE FUNCTION openfox_private.list_integration_sync_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid integration sync scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT q.organization_id FROM openfox.integration_sync_outbox q
         WHERE (q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT l.organization_id FROM openfox.integration_sync_links l
         WHERE l.deleted_at IS NULL AND l.status = 'ACTIVE' AND l.settings #>> '{backfill,status}' = 'running'
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;

GRANT SELECT, INSERT, UPDATE ON openfox.task_issue_links, openfox.task_comment_links, openfox.integration_sync_outbox TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.issue_sync_suppressed() TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_integration_sync_organizations(uuid, integer) TO openfox_context;
