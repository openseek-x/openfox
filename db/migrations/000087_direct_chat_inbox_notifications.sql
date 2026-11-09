-- Direct chat notifications form a separate group between mentions and Agent conversations.
-- Only catalogue positions move; no historical notification is touched.
SET ROLE openfox_migrator;
UPDATE openfox.inbox_policy_keys SET position=position+20 WHERE position>=35;
INSERT INTO openfox.inbox_policy_keys(key,group_key,default_level,position) VALUES
    ('human_chat',NULL,'normal',35),
    ('chat_message_received_recipient','human_chat','off',36);
ALTER TABLE openfox.user_notification_channels ADD COLUMN direct_messages boolean;
ALTER TABLE openfox.notifications
    ADD COLUMN direct_chat_id uuid,
    ADD COLUMN direct_message_id uuid,
    ADD FOREIGN KEY (organization_id,direct_chat_id,direct_message_id) REFERENCES openfox.direct_messages(organization_id,chat_id,id),
    DROP CONSTRAINT notifications_source_present,
    ADD CONSTRAINT notifications_source_present CHECK (conversation_id IS NOT NULL OR task_id IS NOT NULL OR automation_id IS NOT NULL OR (direct_chat_id IS NOT NULL AND direct_message_id IS NOT NULL)),
    DROP CONSTRAINT notifications_kind_valid,
    ADD CONSTRAINT notifications_kind_valid CHECK (kind IN ('MENTION','HANDOFF','HANDOFF_REVIEW','REPLY','TASK_COMMENT','RUN_FAILED','TASK_ASSIGNED','TRIAGE_NEW','AUTOMATION_RESULT','TASK_UPDATED','TASK_REACTION','RUN_BLOCKED','RUN_COMPLETED','DIRECT_MESSAGE'));
-- Post once when a new message is inserted. Do not copy its body or attachments to other channels.
CREATE FUNCTION openfox_private.direct_message_notice() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
BEGIN
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,actor_user_id,kind,direct_chat_id,direct_message_id,inbox_event,dedupe_key,safe_summary)
    SELECT NEW.organization_id,CASE WHEN c.user_low=NEW.author_id THEN c.user_high ELSE c.user_low END,
           NEW.author_id,'DIRECT_MESSAGE',c.id,NEW.id,'chat_message_received_recipient','direct:'||NEW.id,'New direct message.'
      FROM openfox.direct_chats c WHERE c.organization_id=NEW.organization_id AND c.id=NEW.chat_id
        AND NEW.author_id IN (c.user_low,c.user_high)
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION openfox_private.direct_message_notice() FROM PUBLIC;
CREATE TRIGGER direct_message_notice AFTER INSERT ON openfox.direct_messages FOR EACH ROW EXECUTE FUNCTION openfox_private.direct_message_notice();

ALTER TABLE openfox.im_notification_outbox ADD COLUMN direct_chat_id uuid, ADD COLUMN direct_message_id uuid;

-- An inbox choice never opts a member into sending private-message alerts to IM.
CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_notification()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    INSERT INTO openfox.im_notification_outbox (
        organization_id, notification_id, recipient_user_id, personal_connection_id, app_connection_id,
        kind, safe_summary, task_id, conversation_id, direct_chat_id, direct_message_id)
    SELECT n.organization_id, n.id, n.recipient_user_id, p.id, a.id, n.kind, n.safe_summary, n.task_id, n.conversation_id, n.direct_chat_id, n.direct_message_id
      FROM changed_new n
      JOIN openfox.integration_connections p
        ON p.organization_id = n.organization_id AND p.provider IN ('feishu', 'lark', 'dingtalk') AND p.scope = 'PERSONAL'
       AND p.owner_user_id = n.recipient_user_id AND p.status = 'ACTIVE'
      JOIN openfox.integration_connections a
        ON a.organization_id = p.organization_id AND a.id = p.parent_connection_id
       AND a.scope = 'ORGANIZATION' AND a.account_kind = 'app' AND a.status = 'ACTIVE'
     WHERE n.kind<>'DIRECT_MESSAGE' OR (p.provider='feishu' AND EXISTS (
         SELECT 1 FROM openfox.user_notification_channels ch
          WHERE ch.user_id=n.recipient_user_id AND ch.channel='feishu' AND ch.enabled AND ch.direct_messages IS TRUE))
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END
$$;
ALTER FUNCTION openfox_private.enqueue_im_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_notification() FROM PUBLIC;

RESET ROLE;
