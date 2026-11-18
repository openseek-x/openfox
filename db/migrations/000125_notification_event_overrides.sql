-- N01: extend existing channel preferences; no historical notification replay.
SET ROLE openfox_migrator;
-- Revisions are never reused after DELETE/reset and recreation.
CREATE SEQUENCE openfox.notification_channel_revision AS bigint;
ALTER TABLE openfox.user_notification_channels
 ADD COLUMN event_overrides jsonb NOT NULL DEFAULT '{}',
 ADD COLUMN muted_groups text[] NOT NULL DEFAULT '{}',
 ADD COLUMN revision bigint NOT NULL DEFAULT nextval('openfox.notification_channel_revision'),
 ADD CONSTRAINT notification_channel_overrides_shape CHECK (
   jsonb_typeof(event_overrides)='object' AND octet_length(event_overrides::text)<=8192
   AND NOT jsonb_path_exists(event_overrides, '$.* ? (@.type() != "boolean")')),
 ADD CONSTRAINT notification_channel_revision_valid CHECK (revision>0),
 ADD CONSTRAINT notification_channel_groups_valid CHECK (muted_groups <@ ARRAY[
   'assignments','status_changes','comments','mentions','human_chat','agent_activity','automation_runs']::text[]);

ALTER SEQUENCE openfox.notification_channel_revision OWNED BY openfox.user_notification_channels.revision;
GRANT USAGE ON SEQUENCE openfox.notification_channel_revision TO openfox_app;

-- Preserve kind-based legacy choices (not the display group). Explicit leaf values
-- override only that fallback, never channel consent, group mute or private-chat consent.
CREATE FUNCTION openfox_private.notification_channel_allows(
 p_user uuid, p_channel text, p_kind text, p_event text, p_level text)
RETURNS boolean LANGUAGE sql STABLE SET search_path=pg_catalog,openfox AS $$
 SELECT p_channel IN ('desktop','browser','feishu','email','wecom','lark','dingtalk')
  AND COALESCE(ch.enabled,p_channel NOT IN ('email','wecom'))
  AND (p_kind<>'DIRECT_MESSAGE' OR COALESCE(ch.direct_messages,p_channel IN ('desktop','browser')))
  AND NOT COALESCE(k.group_key=ANY(ch.muted_groups),false)
  AND COALESCE((ch.event_overrides->>p_event)::boolean,
    NOT (COALESCE(p_level='off',false) AND COALESCE(p_event IN (
      'chat_replied_participant','task_failed_participant','task_needs_human_input_participant','task_review_requested_participant'),false))
    AND NOT COALESCE((CASE
      WHEN p_kind IN ('TASK_ASSIGNED','TRIAGE_NEW') THEN 'assign'
      WHEN p_kind='TASK_UPDATED' THEN 'status'
      WHEN p_kind IN ('TASK_COMMENT','TASK_REACTION','REPLY') THEN 'comment'
      WHEN p_kind='MENTION' THEN 'mention'
      WHEN p_kind='AUTOMATION_RESULT' THEN 'automation'
      WHEN p_kind='DIRECT_MESSAGE' THEN 'human_chat'
      ELSE 'agent' END)=ANY(ch.disabled_events),false))
 FROM (SELECT 1) seed
 LEFT JOIN openfox.user_notification_channels ch ON ch.user_id=p_user AND ch.channel=p_channel
 LEFT JOIN openfox.inbox_policy_keys k ON k.key=p_event
$$;
REVOKE ALL ON FUNCTION openfox_private.notification_channel_allows(uuid,text,text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.notification_channel_allows(uuid,text,text,text,text) TO openfox_app;

-- Notifications are recipient-only. Workers receive only a policy decision for an
-- existing outbox item in their current tenant, never notification content.
-- The migrator's existing notification read policy and comment tombstones remain in force.
CREATE FUNCTION openfox_private.notification_outbox_policy(p_outbox uuid,p_channel text)
RETURNS TABLE(enabled boolean,allowed boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
 SELECT COALESCE(ch.enabled,p_channel NOT IN ('email','wecom')),
        openfox_private.notification_channel_allows(n.recipient_user_id,p_channel,n.kind,n.inbox_event,n.inbox_level)
 FROM (
   SELECT q.organization_id,q.notification_id,q.recipient_user_id
     FROM openfox.email_notification_outbox q
    WHERE p_channel='email' AND q.organization_id=openfox_private.current_organization_id() AND q.id=p_outbox
   UNION ALL
   SELECT q.organization_id,q.notification_id,q.recipient_user_id
     FROM openfox.im_notification_outbox q
     JOIN openfox.integration_connections a ON a.organization_id=q.organization_id AND a.id=q.app_connection_id
    WHERE p_channel IN ('feishu','lark','dingtalk','wecom') AND a.provider=p_channel
      AND q.organization_id=openfox_private.current_organization_id() AND q.id=p_outbox
 ) queued
 JOIN openfox.notifications n ON n.organization_id=queued.organization_id AND n.id=queued.notification_id
  AND n.recipient_user_id=queued.recipient_user_id
 LEFT JOIN openfox.user_notification_channels ch ON ch.user_id=n.recipient_user_id AND ch.channel=p_channel
$$;
REVOKE ALL ON FUNCTION openfox_private.notification_outbox_policy(uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.notification_outbox_policy(uuid,text) TO openfox_app;

CREATE OR REPLACE FUNCTION openfox_private.snapshot_inbox_delivery() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, openfox AS $$
DECLARE leaf text; author_type text; audience text;
BEGIN
    leaf := NEW.inbox_event;
    IF leaf IS NULL THEN
        CASE NEW.kind
        WHEN 'MENTION' THEN
            IF NEW.comment_id IS NOT NULL THEN
                SELECT CASE WHEN author_agent_id IS NULL THEN 'member' ELSE 'agent' END INTO author_type
                  FROM openfox.task_comments WHERE organization_id=NEW.organization_id AND id=NEW.comment_id;
                leaf := 'comment_mentioned_' || author_type || '_recipient';
            ELSE
                SELECT CASE WHEN author_kind='AGENT' THEN 'agent' ELSE 'member' END INTO author_type
                  FROM openfox.messages WHERE organization_id=NEW.organization_id AND id=NEW.message_id;
                leaf := 'chat_mentioned_' || coalesce(author_type,'member') || '_recipient';
            END IF;
        WHEN 'TASK_COMMENT' THEN
            SELECT CASE WHEN c.author_agent_id IS NULL THEN 'member' ELSE 'agent' END,
                   CASE WHEN parent.author_user_id=NEW.recipient_user_id THEN 'author'
                        WHEN t.assignee_id=NEW.recipient_user_id OR EXISTS (SELECT 1 FROM openfox.task_assignees a
                         WHERE a.organization_id=c.organization_id AND a.task_id=c.task_id AND a.user_id=NEW.recipient_user_id) THEN 'assignee'
                        ELSE 'follower' END,
                   CASE WHEN c.reply_to_id IS NULL THEN 'comment_created_' ELSE 'comment_replied_' END
              INTO author_type,audience,leaf FROM openfox.task_comments c
              JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id
              LEFT JOIN openfox.task_comments parent ON parent.organization_id=c.organization_id AND parent.id=c.reply_to_id
             WHERE c.organization_id=NEW.organization_id AND c.id=NEW.comment_id;
            leaf := leaf || author_type || '_' || audience;
        WHEN 'TASK_ASSIGNED' THEN leaf := 'task_assigned_assignee';
        WHEN 'REPLY' THEN leaf := 'chat_replied_participant';
        WHEN 'RUN_FAILED' THEN leaf := 'task_failed_participant';
        WHEN 'HANDOFF' THEN leaf := 'task_needs_human_input_participant';
        WHEN 'HANDOFF_REVIEW' THEN leaf := 'task_review_requested_participant';
        WHEN 'RUN_BLOCKED' THEN leaf := 'task_agent_blocked_participant';
        WHEN 'RUN_COMPLETED' THEN leaf := 'task_agent_completed_participant';
        ELSE NULL;
        END CASE;
    END IF;
    IF leaf IS NULL THEN RETURN NEW; END IF;
    SELECT CASE WHEN g.level='off' THEN 'off' ELSE COALESCE(p.level,k.default_level) END
      INTO STRICT NEW.inbox_level FROM openfox.inbox_policy_keys k
      LEFT JOIN openfox.user_inbox_preferences p ON p.user_id=NEW.recipient_user_id AND p.key=k.key
      LEFT JOIN openfox.user_inbox_preferences g ON g.user_id=NEW.recipient_user_id AND g.key=k.group_key
     WHERE k.key=leaf AND k.group_key IS NOT NULL;
    NEW.inbox_event := leaf;
    IF NEW.inbox_level='off' AND leaf IN ('chat_replied_participant','task_failed_participant','task_needs_human_input_participant','task_review_requested_participant') AND NOT EXISTS (
        SELECT 1 FROM openfox.user_notification_channels ch
         WHERE ch.user_id=NEW.recipient_user_id AND ch.event_overrides->leaf='true'::jsonb
           AND openfox_private.notification_channel_allows(NEW.recipient_user_id,ch.channel,NEW.kind,leaf,NEW.inbox_level)
    ) THEN
        RETURN NULL; -- preserve legacy suppression unless a channel explicitly opts in
    END IF;
    RETURN NEW;
END $$;

RESET ROLE;
