-- I19. Explicit group consent, preserved epochs, and queue fences. No backfill.
SET ROLE openfox_migrator;
CREATE TABLE openfox.wecom_group_authorizations (
 organization_id uuid NOT NULL,
 bot_id uuid NOT NULL,
 chat_id text NOT NULL CHECK (char_length(btrim(chat_id)) BETWEEN 1 AND 128),
 conversation_id uuid NOT NULL,
 version bigint NOT NULL CHECK (version > 0),
 enabled boolean NOT NULL,
 updated_by uuid NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (organization_id, conversation_id),
 UNIQUE (organization_id, bot_id, chat_id, version),
 FOREIGN KEY (organization_id, bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY (organization_id, conversation_id) REFERENCES openfox.conversations(organization_id,id),
 FOREIGN KEY (organization_id, updated_by) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE UNIQUE INDEX wecom_group_authorizations_active ON openfox.wecom_group_authorizations(organization_id,bot_id,chat_id) WHERE enabled;
ALTER TABLE openfox.wecom_group_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.wecom_group_authorizations FORCE ROW LEVEL SECURITY;
CREATE POLICY wecom_group_authorizations_tenant ON openfox.wecom_group_authorizations
 USING (organization_id=openfox_private.current_organization_id()) WITH CHECK (organization_id=openfox_private.current_organization_id());
CREATE POLICY wecom_group_authorizations_scan ON openfox.wecom_group_authorizations FOR SELECT TO openfox_migrator USING (true);
ALTER TABLE openfox.im_inbound_messages ADD COLUMN group_authorization_version bigint CHECK (group_authorization_version>0);
ALTER TABLE openfox.im_reply_outbox ADD COLUMN group_authorization_version bigint CHECK (group_authorization_version>0);
CREATE INDEX im_inbound_group_discovery ON openfox.im_inbound_messages(organization_id,bot_id,chat_id,received_at DESC) WHERE chat_type='group';

CREATE OR REPLACE FUNCTION openfox_private.enqueue_im_reply()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 INSERT INTO openfox.im_reply_outbox(organization_id,bot_id,chat_id,message_id,reply_to_remote_id,group_authorization_version)
 SELECT n.organization_id,b.bot_id,b.chat_id,n.id,
  coalesce((SELECT i.remote_message_id FROM openfox.im_inbound_messages i
   JOIN openfox.messages m ON m.organization_id=i.organization_id AND m.id=i.message_id
   WHERE i.organization_id=n.organization_id AND i.bot_id=b.bot_id AND i.message_id IS NOT NULL AND m.conversation_id=n.conversation_id
   ORDER BY m.seq DESC LIMIT 1),''), ga.version
 FROM changed_new n
 JOIN openfox.im_chat_bindings b ON b.organization_id=n.organization_id AND b.conversation_id=n.conversation_id
 JOIN openfox.im_bots bot ON bot.organization_id=b.organization_id AND bot.id=b.bot_id AND bot.removed_at IS NULL
 JOIN openfox.integration_connections c ON c.organization_id=bot.organization_id AND c.id=bot.connection_id AND c.status='ACTIVE'
 LEFT JOIN openfox.wecom_group_authorizations ga ON ga.organization_id=b.organization_id AND ga.bot_id=b.bot_id AND ga.chat_id=b.chat_id AND ga.conversation_id=b.conversation_id AND ga.enabled
 WHERE n.author_kind IN ('AGENT','SYSTEM') AND (c.provider<>'wecom' OR b.chat_type<>'group' OR ga.version IS NOT NULL)
 ON CONFLICT DO NOTHING;
 RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_im_reply() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_reply() FROM PUBLIC;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.wecom_group_authorizations TO openfox_app;
