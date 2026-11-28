-- OPE-19「测试智能体截图录像与报告交付」：智能体在原任务发布一条带附件的正式报告评论。
--
-- 仅扩展（expand-only），不改写已有数据：
--   - attachments：uploaded_by 改为可空，新增 uploaded_by_agent_id（智能体上传者）、comment_id（所属任务评论）、
--     source_artifact_id（复制自本次 Run 的哪个产物，用于重试去重）。CHECK 恰有一个上传者；
--     comment_id 只用于智能体复制产物得到的任务附件。已有行都有成员上传者、comment_id 为空，约束直接校验通过。
--     同一评论内同一产物只复制一次（唯一索引），重试只补缺失的附件。
--   - task_comments：新增 run_report（本次 Run 的正式报告评论），每次 Run 最多一条（部分唯一索引），
--     重复发布复用同一条评论。
-- 锁：两张表加可空列 / 默认值列与 CHECK、外键（ACCESS EXCLUSIVE，扫描校验，行数有限）；去掉 NOT NULL 只改目录；
--   两个新索引建在小表上，未使用 CONCURRENTLY（迁移在事务内执行）。
-- 回滚：应用回退后新列闲置即可。旧版本按非空读取 attachments.uploaded_by，回退前先删除智能体附件及其存储对象
--   （见 db/migrations/README.md 的 000176 条目），再视需要恢复 NOT NULL。

SET ROLE openfox_migrator;

ALTER TABLE openfox.attachments
    ALTER COLUMN uploaded_by DROP NOT NULL,
    ADD COLUMN uploaded_by_agent_id uuid,
    ADD COLUMN comment_id uuid,
    ADD COLUMN source_artifact_id uuid,
    ADD CONSTRAINT attachments_uploaded_by_agent_fk
        FOREIGN KEY (organization_id, uploaded_by_agent_id) REFERENCES openfox.agents (organization_id, id),
    ADD CONSTRAINT attachments_comment_fk
        FOREIGN KEY (organization_id, comment_id) REFERENCES openfox.task_comments (organization_id, id),
    ADD CONSTRAINT attachments_source_artifact_fk
        FOREIGN KEY (organization_id, source_artifact_id) REFERENCES openfox.artifacts (organization_id, id),
    ADD CONSTRAINT attachments_uploader_shape CHECK (
        (uploaded_by IS NOT NULL AND uploaded_by_agent_id IS NULL)
        OR (uploaded_by IS NULL AND uploaded_by_agent_id IS NOT NULL)),
    ADD CONSTRAINT attachments_comment_shape CHECK (
        (comment_id IS NULL AND source_artifact_id IS NULL AND uploaded_by_agent_id IS NULL)
        OR (comment_id IS NOT NULL AND source_artifact_id IS NOT NULL AND uploaded_by_agent_id IS NOT NULL
            AND owner_kind = 'TASK'));

CREATE UNIQUE INDEX attachments_comment_source_unique
    ON openfox.attachments (organization_id, comment_id, source_artifact_id)
    WHERE comment_id IS NOT NULL;

ALTER TABLE openfox.task_comments
    ADD COLUMN run_report boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT task_comments_run_report_shape CHECK (NOT run_report OR (author_agent_id IS NOT NULL AND run_id IS NOT NULL));

CREATE UNIQUE INDEX task_comments_run_report_unique
    ON openfox.task_comments (organization_id, run_id)
    WHERE run_report;

RESET ROLE;
