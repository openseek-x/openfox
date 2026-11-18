-- 阶段 7c「附件（任务与对话）」：附件表 attachments。
--
-- 仅扩展（expand-only），新表，不改动任何已有表：
--   - attachments：一行一个附件，owner_kind 区分任务附件（TASK，task_id 必填）与对话附件（CONVERSATION，conversation_id 必填）。
--     对话附件先上传（message_id 为空，只有上传人自己能看到 / 删除），发消息时用 attachment_ids 绑定到消息（message_id 写入后不可再改）。
--     file_key 是 artifacts 存储里的对象键（attachments/<组织>/<附件 ID>，每个附件独占一个对象，删除行即可删除对象）。
--     单文件 ≤ 20 MB（与文档库一致）；每个任务最多 100 个由服务端在锁住任务行后计数。
--   权限不在表里表达：读 / 下载按任务或会话的读权限，删除按上传人或空间管理员，均由应用层判断；RLS 只做租户隔离。
-- 锁：只建新表与新索引，没有对已有表加锁（外键引用 tasks / conversations / messages 的主键，不改它们）。
-- 回滚：应用回退后新表闲置即可（旧版本不读）。确需移除时：先删除存储里 attachments/ 目录下的文件，再 DROP TABLE openfox.attachments。

SET ROLE openfox_migrator;

CREATE TABLE openfox.attachments (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    owner_kind text NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid,
    conversation_id uuid,
    message_id uuid,
    name text NOT NULL,
    file_key text NOT NULL,
    content_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 text NOT NULL,
    uploaded_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, task_id) REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id) REFERENCES openfox.conversations (organization_id, id),
    FOREIGN KEY (organization_id, message_id) REFERENCES openfox.messages (organization_id, id),
    FOREIGN KEY (organization_id, uploaded_by) REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT attachments_owner_valid CHECK (
        (owner_kind = 'TASK' AND task_id IS NOT NULL AND conversation_id IS NULL AND message_id IS NULL)
        OR (owner_kind = 'CONVERSATION' AND conversation_id IS NOT NULL AND task_id IS NULL)),
    CONSTRAINT attachments_file_valid CHECK (
        char_length(btrim(name)) BETWEEN 1 AND 255
        AND char_length(file_key) BETWEEN 1 AND 256 AND char_length(content_type) BETWEEN 1 AND 128
        AND size_bytes BETWEEN 0 AND 20971520 AND sha256 ~ '^[0-9a-f]{64}$')
);
CREATE INDEX attachments_task_idx ON openfox.attachments (organization_id, task_id, created_at, id)
    WHERE task_id IS NOT NULL;
CREATE INDEX attachments_conversation_idx ON openfox.attachments (organization_id, conversation_id, uploaded_by)
    WHERE conversation_id IS NOT NULL AND message_id IS NULL;
CREATE INDEX attachments_message_idx ON openfox.attachments (organization_id, message_id)
    WHERE message_id IS NOT NULL;

ALTER TABLE openfox.attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.attachments FORCE ROW LEVEL SECURITY;
CREATE POLICY attachments_tenant_isolation ON openfox.attachments
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;

-- message_id 只在发消息时从空绑定到消息，其余列不可改。
GRANT SELECT, INSERT, DELETE ON openfox.attachments TO openfox_app;
GRANT UPDATE (message_id) ON openfox.attachments TO openfox_app;
