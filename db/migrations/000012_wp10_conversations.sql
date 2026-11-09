-- WP-10 conversation, message, comment, and notification storage.
-- This migration is additive and keeps the WP-09 execution tables as the
-- source of truth for queued inputs and execution triggers.

SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations
    DROP CONSTRAINT conversations_lifecycle_valid,
    DROP CONSTRAINT conversations_handoff_valid;

UPDATE openfox.conversations
   SET lifecycle = 'OPEN'
 WHERE lifecycle = 'ACTIVE';
UPDATE openfox.conversations
   SET handoff = CASE handoff
       WHEN 'REQUESTED' THEN 'WAITING_REPLY'
       WHEN 'ACCEPTED' THEN 'WAITING_REVIEW'
       ELSE 'NONE'
   END;

ALTER TABLE openfox.conversations
    ADD COLUMN visibility text NOT NULL DEFAULT 'PRIVATE',
    ADD COLUMN next_message_seq bigint NOT NULL DEFAULT 1,
    ALTER COLUMN lifecycle SET DEFAULT 'OPEN',
    ADD CONSTRAINT conversations_lifecycle_valid
        CHECK (lifecycle IN ('OPEN', 'COMPLETED', 'ARCHIVED')),
    ADD CONSTRAINT conversations_handoff_valid
        CHECK (handoff IN ('NONE', 'WAITING_REPLY', 'WAITING_REVIEW')),
    ADD CONSTRAINT conversations_visibility_valid
        CHECK (visibility IN ('PRIVATE', 'SHARED')),
    ADD CONSTRAINT conversations_next_message_seq_positive
        CHECK (next_message_seq > 0);

CREATE INDEX conversations_owner_idx
    ON openfox.conversations (organization_id, owner_user_id, updated_at DESC, id);

CREATE TABLE openfox.conversation_members (
    organization_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    user_id uuid NOT NULL,
    access_level text NOT NULL DEFAULT 'VIEWER',
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, conversation_id, user_id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT conversation_members_access_valid
        CHECK (access_level IN ('VIEWER', 'COMMENTER')),
    CONSTRAINT conversation_members_version_positive CHECK (version > 0)
);

CREATE INDEX conversation_members_user_idx
    ON openfox.conversation_members (organization_id, user_id, conversation_id);

CREATE TABLE openfox.messages (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL,
    seq bigint NOT NULL,
    author_kind text NOT NULL DEFAULT 'USER',
    author_user_id uuid,
    author_agent_id uuid,
    body text NOT NULL,
    client_message_id text,
    reply_to_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    UNIQUE (organization_id, conversation_id, seq),
    UNIQUE (organization_id, conversation_id, id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    FOREIGN KEY (organization_id, author_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, author_agent_id)
        REFERENCES openfox.agents (organization_id, id),
    FOREIGN KEY (organization_id, conversation_id, reply_to_id)
        REFERENCES openfox.messages (organization_id, conversation_id, id),
    CONSTRAINT messages_author_kind_valid
        CHECK (author_kind IN ('USER', 'AGENT', 'SYSTEM')),
    CONSTRAINT messages_author_shape_valid CHECK (
        (author_kind = 'USER' AND author_user_id IS NOT NULL AND author_agent_id IS NULL)
        OR (author_kind = 'AGENT' AND author_user_id IS NULL AND author_agent_id IS NOT NULL)
        OR (author_kind = 'SYSTEM' AND author_user_id IS NULL AND author_agent_id IS NULL)
    ),
    CONSTRAINT messages_body_not_blank CHECK (length(btrim(body)) > 0),
    CONSTRAINT messages_body_length CHECK (length(body) <= 65536),
    CONSTRAINT messages_seq_positive CHECK (seq > 0),
    CONSTRAINT messages_client_id_length CHECK (
        client_message_id IS NULL OR length(btrim(client_message_id)) BETWEEN 1 AND 256
    )
);

CREATE UNIQUE INDEX messages_client_id_unique
    ON openfox.messages (organization_id, conversation_id, client_message_id)
    WHERE client_message_id IS NOT NULL;
CREATE INDEX messages_conversation_idx
    ON openfox.messages (organization_id, conversation_id, seq, id);

ALTER TABLE openfox.conversation_inputs
    ADD CONSTRAINT conversation_inputs_message_fk
        FOREIGN KEY (organization_id, message_id)
        REFERENCES openfox.messages (organization_id, id);

CREATE TABLE openfox.message_mentions (
    organization_id uuid NOT NULL,
    message_id uuid NOT NULL,
    position integer NOT NULL,
    mentioned_user_id uuid,
    mentioned_agent_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, message_id, position),
    FOREIGN KEY (organization_id, message_id)
        REFERENCES openfox.messages (organization_id, id),
    FOREIGN KEY (organization_id, mentioned_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, mentioned_agent_id)
        REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT message_mentions_position_nonnegative CHECK (position >= 0),
    CONSTRAINT message_mentions_target_shape CHECK (
        (mentioned_user_id IS NOT NULL AND mentioned_agent_id IS NULL)
        OR (mentioned_user_id IS NULL AND mentioned_agent_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX message_mentions_user_unique
    ON openfox.message_mentions (organization_id, message_id, mentioned_user_id)
    WHERE mentioned_user_id IS NOT NULL;
CREATE UNIQUE INDEX message_mentions_agent_unique
    ON openfox.message_mentions (organization_id, message_id, mentioned_agent_id)
    WHERE mentioned_agent_id IS NOT NULL;

CREATE TABLE openfox.task_comments (
    organization_id uuid NOT NULL,
    space_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    author_user_id uuid NOT NULL,
    body text NOT NULL,
    client_comment_id text,
    reply_to_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, space_id)
        REFERENCES openfox.spaces (organization_id, id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, author_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, reply_to_id)
        REFERENCES openfox.task_comments (organization_id, id),
    CONSTRAINT task_comments_body_not_blank CHECK (length(btrim(body)) > 0),
    CONSTRAINT task_comments_body_length CHECK (length(body) <= 65536),
    CONSTRAINT task_comments_client_id_length CHECK (
        client_comment_id IS NULL OR length(btrim(client_comment_id)) BETWEEN 1 AND 256
    )
);

CREATE UNIQUE INDEX task_comments_client_id_unique
    ON openfox.task_comments (organization_id, task_id, client_comment_id)
    WHERE client_comment_id IS NOT NULL;
CREATE INDEX task_comments_task_idx
    ON openfox.task_comments (organization_id, task_id, created_at, id);

CREATE TABLE openfox.comment_mentions (
    organization_id uuid NOT NULL,
    comment_id uuid NOT NULL,
    position integer NOT NULL,
    mentioned_user_id uuid,
    mentioned_agent_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, comment_id, position),
    FOREIGN KEY (organization_id, comment_id)
        REFERENCES openfox.task_comments (organization_id, id),
    FOREIGN KEY (organization_id, mentioned_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, mentioned_agent_id)
        REFERENCES openfox.agents (organization_id, id),
    CONSTRAINT comment_mentions_position_nonnegative CHECK (position >= 0),
    CONSTRAINT comment_mentions_target_shape CHECK (
        (mentioned_user_id IS NOT NULL AND mentioned_agent_id IS NULL)
        OR (mentioned_user_id IS NULL AND mentioned_agent_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX comment_mentions_user_unique
    ON openfox.comment_mentions (organization_id, comment_id, mentioned_user_id)
    WHERE mentioned_user_id IS NOT NULL;
CREATE UNIQUE INDEX comment_mentions_agent_unique
    ON openfox.comment_mentions (organization_id, comment_id, mentioned_agent_id)
    WHERE mentioned_agent_id IS NOT NULL;

CREATE TABLE openfox.notifications (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    recipient_user_id uuid NOT NULL,
    kind text NOT NULL,
    conversation_id uuid,
    message_id uuid,
    task_id uuid,
    comment_id uuid,
    dedupe_key text NOT NULL,
    safe_summary text NOT NULL,
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, recipient_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    FOREIGN KEY (organization_id, conversation_id)
        REFERENCES openfox.conversations (organization_id, id),
    FOREIGN KEY (organization_id, message_id)
        REFERENCES openfox.messages (organization_id, id),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES openfox.tasks (organization_id, id),
    FOREIGN KEY (organization_id, comment_id)
        REFERENCES openfox.task_comments (organization_id, id),
    CONSTRAINT notifications_kind_valid
        CHECK (kind IN ('MENTION', 'HANDOFF', 'REPLY', 'TASK_COMMENT')),
    CONSTRAINT notifications_dedupe_key_valid
        CHECK (length(btrim(dedupe_key)) BETWEEN 1 AND 256),
    CONSTRAINT notifications_summary_valid
        CHECK (length(btrim(safe_summary)) BETWEEN 1 AND 512),
    CONSTRAINT notifications_source_present CHECK (
        conversation_id IS NOT NULL OR task_id IS NOT NULL
    )
);

CREATE UNIQUE INDEX notifications_dedupe_unique
    ON openfox.notifications (organization_id, recipient_user_id, dedupe_key);
CREATE INDEX notifications_recipient_idx
    ON openfox.notifications (organization_id, recipient_user_id, read_at, created_at DESC, id);

-- Fan-out writes target recipients other than the current actor. A narrow
-- definer function keeps that write path tenant-scoped without widening the
-- recipient-only read policy on the table.
CREATE OR REPLACE FUNCTION openfox_private.enqueue_notification(
    p_recipient_user_id uuid,
    p_kind text,
    p_conversation_id uuid,
    p_message_id uuid,
    p_task_id uuid,
    p_comment_id uuid,
    p_dedupe_key text,
    p_safe_summary text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM openfox.organization_members
         WHERE organization_id = openfox_private.current_organization_id()
           AND user_id = p_recipient_user_id AND status = 'ACTIVE'
    ) THEN
        RAISE EXCEPTION 'notification recipient is not active in the organization'
            USING ERRCODE = '23514';
    END IF;
    INSERT INTO openfox.notifications (
        organization_id, recipient_user_id, kind, conversation_id, message_id,
        task_id, comment_id, dedupe_key, safe_summary
    ) VALUES (
        openfox_private.current_organization_id(), p_recipient_user_id, p_kind,
        p_conversation_id, p_message_id, p_task_id, p_comment_id,
        p_dedupe_key, p_safe_summary
    )
    ON CONFLICT (organization_id, recipient_user_id, dedupe_key) DO NOTHING;
END
$$;

ALTER FUNCTION openfox_private.enqueue_notification(uuid, text, uuid, uuid, uuid, uuid, text, text)
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_notification(uuid, text, uuid, uuid, uuid, uuid, text, text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION openfox_private.validate_message_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    conversation_space_id uuid;
BEGIN
    SELECT space_id INTO conversation_space_id
      FROM openfox.conversations
     WHERE organization_id = NEW.organization_id AND id = NEW.conversation_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'message conversation is not available' USING ERRCODE = '23514';
    END IF;
    IF NEW.reply_to_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM openfox.messages
         WHERE organization_id = NEW.organization_id
           AND conversation_id = NEW.conversation_id AND id = NEW.reply_to_id
    ) THEN
        RAISE EXCEPTION 'message reply is outside the conversation' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_message_scope() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_message_scope() FROM PUBLIC;

CREATE TRIGGER messages_scope_valid_trigger
BEFORE INSERT OR UPDATE ON openfox.messages
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_message_scope();

CREATE OR REPLACE FUNCTION openfox_private.validate_task_comment_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    task_space_id uuid;
BEGIN
    SELECT space_id INTO task_space_id
      FROM openfox.tasks
     WHERE organization_id = NEW.organization_id AND id = NEW.task_id;
    IF NOT FOUND OR task_space_id <> NEW.space_id THEN
        RAISE EXCEPTION 'task comment is outside the task space' USING ERRCODE = '23514';
    END IF;
    IF NEW.reply_to_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM openfox.task_comments
         WHERE organization_id = NEW.organization_id AND id = NEW.reply_to_id
           AND task_id = NEW.task_id
    ) THEN
        RAISE EXCEPTION 'task comment reply is outside the task' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.validate_task_comment_scope() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.validate_task_comment_scope() FROM PUBLIC;

CREATE TRIGGER task_comments_scope_valid_trigger
BEFORE INSERT OR UPDATE ON openfox.task_comments
FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_task_comment_scope();

RESET ROLE;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    openfox.conversations,
    openfox.conversation_members,
    openfox.messages,
    openfox.message_mentions,
    openfox.task_comments,
    openfox.comment_mentions
TO openfox_app;
GRANT SELECT, UPDATE, DELETE ON openfox.notifications TO openfox_app;

ALTER TABLE openfox.conversation_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversation_members FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.messages FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.message_mentions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.message_mentions FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.task_comments FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.comment_mentions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.comment_mentions FORCE ROW LEVEL SECURITY;
ALTER TABLE openfox.notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.notifications FORCE ROW LEVEL SECURITY;

CREATE POLICY conversation_members_tenant_isolation ON openfox.conversation_members
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY messages_tenant_isolation ON openfox.messages
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY message_mentions_tenant_isolation ON openfox.message_mentions
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY task_comments_tenant_isolation ON openfox.task_comments
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY comment_mentions_tenant_isolation ON openfox.comment_mentions
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
-- Notification creation is an application-side fan-out: the current actor
-- may create a notice for another active tenant member, while reads, updates,
-- and deletes remain restricted to the recipient. Separate command policies
-- avoid making INSERT ... ON CONFLICT depend on the recipient read predicate.
CREATE POLICY notifications_recipient_read ON openfox.notifications
    FOR SELECT
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND recipient_user_id = openfox_private.current_actor_id());
CREATE POLICY notifications_service_insert ON openfox.notifications
    FOR INSERT
    TO openfox_migrator
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY notifications_recipient_update ON openfox.notifications
    FOR UPDATE
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND recipient_user_id = openfox_private.current_actor_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id()
                AND recipient_user_id = openfox_private.current_actor_id());
CREATE POLICY notifications_recipient_delete ON openfox.notifications
    FOR DELETE
    TO openfox_app
    USING (organization_id = openfox_private.current_organization_id()
           AND recipient_user_id = openfox_private.current_actor_id());
CREATE POLICY notifications_migrator_read ON openfox.notifications
    FOR SELECT
    TO openfox_migrator
    USING (organization_id = openfox_private.current_organization_id());

REVOKE ALL ON FUNCTION openfox_private.validate_message_scope() FROM PUBLIC;
REVOKE ALL ON FUNCTION openfox_private.validate_task_comment_scope() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.enqueue_notification(uuid, text, uuid, uuid, uuid, uuid, text, text) TO openfox_app;
