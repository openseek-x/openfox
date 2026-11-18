-- I18: Telegram control, person proof, group epochs and durable IM authorization.
-- No vendor write, no legacy backfill. Existing cross-provider im_bots uniqueness
-- remains authoritative. Disconnected remote bots stay globally reserved.
-- Run only after the frozen 145, in a new isolated database for verification.
SET ROLE openfox_migrator;

CREATE TABLE openfox.telegram_bot_controls (
 organization_id uuid NOT NULL, bot_id uuid NOT NULL, connection_id uuid NOT NULL,
 remote_bot_id bigint NOT NULL CHECK(remote_bot_id BETWEEN 1 AND 4503599627370495),
 username text NOT NULL CHECK(char_length(username) BETWEEN 1 AND 64),
 kind text NOT NULL CHECK(kind IN ('TASK_ASSISTANT','AGENT')),
 target_space_id uuid, target_agent_id uuid,
 state text NOT NULL DEFAULT 'SETTING' CHECK(state IN ('SETTING','ACTIVE','ATTENTION','DELETING','DISCONNECTED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 authorization_generation bigint NOT NULL DEFAULT 1 CHECK(authorization_generation>0),
 webhook_generation bigint NOT NULL DEFAULT 1 CHECK(webhook_generation>0),
 token_version bigint NOT NULL DEFAULT 1 CHECK(token_version>0),
 route_hash bytea NOT NULL CHECK(octet_length(route_hash)=32),
 webhook_sealed bytea NOT NULL CHECK(octet_length(webhook_sealed) BETWEEN 1 AND 8192),
 managed_by uuid NOT NULL, managed_version bigint NOT NULL CHECK(managed_version>0),
 reconcile_requested boolean NOT NULL DEFAULT false,
 control_action text NOT NULL DEFAULT 'SET' CHECK(control_action IN ('','SET','DELETE','RECONCILE')),
 control_status text NOT NULL DEFAULT 'PENDING' CHECK(control_status IN ('PENDING','SENDING','DONE','UNKNOWN','FAILED')),
 control_claim text NOT NULL DEFAULT '' CHECK(char_length(control_claim)<=64),
 control_lease_until timestamptz, next_attempt_at timestamptz DEFAULT clock_timestamp(),
 control_attempts integer NOT NULL DEFAULT 0 CHECK(control_attempts BETWEEN 0 AND 8),
 last_error text NOT NULL DEFAULT '' CHECK(char_length(last_error)<=64),
 configured_at timestamptz, first_update_at timestamptz, last_update_at timestamptz,
 pending_update_count bigint NOT NULL DEFAULT 0 CHECK(pending_update_count>=0),
 unsupported_topic_count bigint NOT NULL DEFAULT 0 CHECK(unsupported_topic_count>=0),
 send_claim text NOT NULL DEFAULT '' CHECK(char_length(send_claim)<=64), send_lease_until timestamptz,
 send_not_before timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,bot_id), UNIQUE(organization_id,connection_id), UNIQUE(remote_bot_id), UNIQUE(route_hash),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,connection_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,managed_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,target_space_id) REFERENCES openfox.spaces(organization_id,id),
 FOREIGN KEY(organization_id,target_agent_id) REFERENCES openfox.agents(organization_id,id),
 CHECK((kind='TASK_ASSISTANT' AND target_space_id IS NOT NULL AND target_agent_id IS NULL) OR
       (kind='AGENT' AND target_agent_id IS NOT NULL AND target_space_id IS NULL))
);
CREATE INDEX telegram_controls_due ON openfox.telegram_bot_controls(organization_id,next_attempt_at) WHERE next_attempt_at IS NOT NULL;

CREATE TABLE openfox.telegram_connection_preparations (
 organization_id uuid NOT NULL, id uuid NOT NULL DEFAULT gen_random_uuid(), actor_id uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0), operation_key uuid NOT NULL, request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 kind text NOT NULL CHECK(kind IN ('TASK_ASSISTANT','AGENT')), space_id uuid, agent_id uuid, target_version bigint NOT NULL CHECK(target_version>0),
 bot_id uuid, bot_version bigint NOT NULL DEFAULT 0 CHECK(bot_version>=0),
 recover_webhook boolean NOT NULL DEFAULT false,
 remote_bot_id bigint NOT NULL CHECK(remote_bot_id BETWEEN 1 AND 4503599627370495), username text NOT NULL,
 bot_name text NOT NULL, config jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(config)='object' AND pg_column_size(config)<=16384),
 extra_prompt text NOT NULL DEFAULT '' CHECK(char_length(extra_prompt)<=4000),
 token_sealed bytea CHECK(octet_length(token_sealed)<=4096),
 status text NOT NULL DEFAULT 'PENDING_CONFIRM' CHECK(status IN ('PENDING_CONFIRM','COMPLETED','FAILED','CANCELLED','EXPIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0), expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), completed_at timestamptz,
 PRIMARY KEY(organization_id,id), UNIQUE(organization_id,actor_id,operation_key),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,space_id) REFERENCES openfox.spaces(organization_id,id),
 FOREIGN KEY(organization_id,agent_id) REFERENCES openfox.agents(organization_id,id),
 CHECK(char_length(username) BETWEEN 1 AND 64 AND char_length(bot_name) BETWEEN 1 AND 200),
 CHECK((status='PENDING_CONFIRM')=(token_sealed IS NOT NULL))
);
CREATE INDEX telegram_preparations_expire ON openfox.telegram_connection_preparations(organization_id,expires_at) WHERE status='PENDING_CONFIRM';

CREATE TABLE openfox.telegram_identities (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,owner_user_id uuid NOT NULL,
 remote_user_id bigint NOT NULL CHECK(remote_user_id BETWEEN 1 AND 4503599627370495),
 private_chat_id bigint NOT NULL CHECK(private_chat_id BETWEEN 1 AND 4503599627370495),
 display_name text NOT NULL CHECK(char_length(display_name)<=200),
 active boolean NOT NULL DEFAULT true,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 confirmed_at timestamptz NOT NULL DEFAULT clock_timestamp(),disabled_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK(active=(disabled_at IS NULL))
);
CREATE UNIQUE INDEX telegram_identity_remote_active ON openfox.telegram_identities(organization_id,bot_id,remote_user_id) WHERE active;
CREATE UNIQUE INDEX telegram_identity_owner_active ON openfox.telegram_identities(organization_id,bot_id,owner_user_id) WHERE active;
CREATE TABLE openfox.telegram_binding_sessions (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,owner_user_id uuid NOT NULL,
 actor_version bigint NOT NULL CHECK(actor_version>0),authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 code_hash bytea NOT NULL UNIQUE CHECK(octet_length(code_hash)=32),
 status text NOT NULL DEFAULT 'WAITING' CHECK(status IN ('WAITING','CANDIDATE','COMPLETED','CANCELLED','EXPIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),expires_at timestamptz NOT NULL,consumed_at timestamptz,
 candidate_user_id bigint CHECK(candidate_user_id BETWEEN 1 AND 4503599627370495),
 candidate_chat_id bigint CHECK(candidate_chat_id BETWEEN 1 AND 4503599627370495),candidate_name text NOT NULL DEFAULT '' CHECK(char_length(candidate_name)<=200),
 identity_id uuid,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.telegram_identities(organization_id,id),
 CHECK(status NOT IN ('CANDIDATE','COMPLETED') OR (candidate_user_id IS NOT NULL AND candidate_chat_id IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE UNIQUE INDEX telegram_binding_pending ON openfox.telegram_binding_sessions(organization_id,bot_id,owner_user_id) WHERE status IN ('WAITING','CANDIDATE');
CREATE INDEX telegram_binding_expire ON openfox.telegram_binding_sessions(organization_id,expires_at) WHERE status IN ('WAITING','CANDIDATE');

CREATE TABLE openfox.telegram_group_discoveries (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,
 chat_id bigint NOT NULL CHECK(chat_id BETWEEN -4503599627370495 AND -1),title text NOT NULL CHECK(char_length(title)<=255),
 identity_id uuid NOT NULL,identity_version bigint NOT NULL CHECK(identity_version>0),owner_user_id uuid NOT NULL,
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,bot_id,chat_id,owner_user_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.telegram_identities(organization_id,id),
 FOREIGN KEY(organization_id,owner_user_id) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE TABLE openfox.telegram_group_epochs (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,
 chat_id bigint NOT NULL CHECK(chat_id BETWEEN -4503599627370495 AND -1),title text NOT NULL CHECK(char_length(title)<=255),
 enabled boolean NOT NULL DEFAULT true,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),public_reply_consent boolean NOT NULL CHECK(public_reply_consent),
 background_enabled boolean NOT NULL DEFAULT false,authorized_by uuid NOT NULL,identity_id uuid NOT NULL,identity_version bigint NOT NULL CHECK(identity_version>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),disabled_at timestamptz,
 PRIMARY KEY(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,authorized_by) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.telegram_identities(organization_id,id),
 CHECK(enabled=(disabled_at IS NULL))
);
CREATE UNIQUE INDEX telegram_group_enabled ON openfox.telegram_group_epochs(organization_id,bot_id,chat_id) WHERE enabled;
CREATE TABLE openfox.telegram_routes (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,chat_id bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN ('p2p','group')),conversation_id uuid NOT NULL,identity_id uuid,identity_version bigint,epoch_id uuid,
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),closed_at timestamptz,
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,conversation_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.telegram_identities(organization_id,id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.telegram_group_epochs(organization_id,id),
 CHECK((kind='p2p' AND chat_id BETWEEN 1 AND 4503599627370495 AND identity_id IS NOT NULL AND identity_version IS NOT NULL AND identity_version>0 AND epoch_id IS NULL) OR
       (kind='group' AND chat_id BETWEEN -4503599627370495 AND -1 AND identity_id IS NULL AND identity_version IS NULL AND epoch_id IS NOT NULL))
);
CREATE UNIQUE INDEX telegram_route_current ON openfox.telegram_routes(organization_id,bot_id,chat_id) WHERE closed_at IS NULL;

CREATE TABLE openfox.telegram_update_receipts (
 organization_id uuid NOT NULL,connection_id uuid NOT NULL,event_id text NOT NULL,
 body_hash bytea NOT NULL CHECK(octet_length(body_hash)=32),kind text NOT NULL CHECK(char_length(kind)<=32),
 inbound_id uuid,route_id uuid,chat_id bigint,remote_message_id bigint,update_id bigint,
 actor_id uuid,actor_version bigint,identity_id uuid,identity_version bigint,epoch_id uuid,epoch_version bigint,authorization_generation bigint,
 result text NOT NULL DEFAULT '' CHECK(char_length(result)<=64),
 PRIMARY KEY(organization_id,connection_id,event_id),UNIQUE(organization_id,inbound_id),
 FOREIGN KEY(organization_id,connection_id,event_id) REFERENCES openfox.im_inbound_events(organization_id,connection_id,event_id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,inbound_id) REFERENCES openfox.im_inbound_messages(organization_id,id) ON DELETE SET NULL(inbound_id),
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.telegram_routes(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.telegram_identities(organization_id,id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.telegram_group_epochs(organization_id,id),
 CHECK(inbound_id IS NULL OR (route_id IS NOT NULL AND chat_id IS NOT NULL AND remote_message_id IS NOT NULL AND update_id IS NOT NULL
 AND actor_version IS NOT NULL AND identity_version IS NOT NULL AND authorization_generation IS NOT NULL AND chat_id BETWEEN -4503599627370495 AND 4503599627370495 AND chat_id<>0
 AND remote_message_id BETWEEN 1 AND 4503599627370495 AND update_id BETWEEN 0 AND 4503599627370495
 AND actor_id IS NOT NULL AND actor_version>0 AND identity_id IS NOT NULL AND identity_version>0 AND authorization_generation>0)),
 CHECK((epoch_id IS NULL)=(epoch_version IS NULL))
);
-- Only persistent per-message authorization lives here. Application callers may
-- INSERT/SELECT, never rewrite identity, actor, route or a message association.
-- REMOTE: message_id is preallocated as the durable inbound queue UUID. The FK
-- is deferred only within the transaction that also calls PostMessage.
CREATE TABLE openfox.telegram_inputs (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,inbound_id uuid,message_id uuid NOT NULL,
 source_kind text NOT NULL CHECK(source_kind IN ('REMOTE','OPENFOX')),
 route_id uuid NOT NULL,chat_id bigint NOT NULL,remote_message_id bigint,update_id bigint,
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),actor_id uuid NOT NULL,actor_version bigint NOT NULL CHECK(actor_version>0),
 identity_id uuid NOT NULL,identity_version bigint NOT NULL CHECK(identity_version>0),epoch_id uuid,epoch_version bigint,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,inbound_id),UNIQUE(organization_id,message_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,inbound_id) REFERENCES openfox.im_inbound_messages(organization_id,id) ON DELETE SET NULL(inbound_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(organization_id,route_id) REFERENCES openfox.telegram_routes(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.telegram_identities(organization_id,id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.telegram_group_epochs(organization_id,id),
 CHECK(chat_id BETWEEN -4503599627370495 AND 4503599627370495 AND chat_id<>0),
 CHECK((epoch_id IS NULL)=(epoch_version IS NULL)),CHECK(epoch_version IS NULL OR epoch_version>0),
 CHECK((source_kind='REMOTE' AND remote_message_id IS NOT NULL AND update_id IS NOT NULL AND remote_message_id BETWEEN 1 AND 4503599627370495 AND update_id BETWEEN 0 AND 4503599627370495)
 OR (source_kind='OPENFOX' AND remote_message_id IS NULL AND update_id IS NULL AND inbound_id IS NULL))
);
CREATE FUNCTION openfox_private.validate_telegram_input_scope() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE r_kind text;r_bot uuid;r_chat bigint;r_gen bigint;r_identity uuid;r_identity_version bigint;r_epoch uuid;r_closed timestamptz;
 i_bot uuid;i_owner uuid;i_version bigint;i_gen bigint;i_active boolean;epoch_valid boolean;
BEGIN
 SELECT kind,bot_id,chat_id,authorization_generation,identity_id,identity_version,epoch_id,closed_at
 INTO r_kind,r_bot,r_chat,r_gen,r_identity,r_identity_version,r_epoch,r_closed
 FROM openfox.telegram_routes WHERE organization_id=NEW.organization_id AND id=NEW.route_id;
 SELECT bot_id,owner_user_id,version,authorization_generation,active INTO i_bot,i_owner,i_version,i_gen,i_active
 FROM openfox.telegram_identities WHERE organization_id=NEW.organization_id AND id=NEW.identity_id;
 IF r_kind IS NULL OR i_bot IS NULL OR r_bot<>NEW.bot_id OR r_chat<>NEW.chat_id OR r_closed IS NOT NULL
 OR r_gen<>NEW.authorization_generation OR i_bot<>NEW.bot_id OR i_owner<>NEW.actor_id
 OR i_version<>NEW.identity_version OR i_gen<>NEW.authorization_generation OR NOT i_active
 OR (r_kind='p2p' AND (r_identity<>NEW.identity_id OR r_identity_version<>NEW.identity_version OR NEW.epoch_id IS NOT NULL))
 OR (r_kind='group' AND (NEW.epoch_id IS NULL OR r_epoch<>NEW.epoch_id)) THEN
 RAISE EXCEPTION 'invalid telegram input scope' USING ERRCODE='23514';
 END IF;
 IF r_kind='group' THEN
 SELECT enabled AND public_reply_consent AND version=NEW.epoch_version AND bot_id=NEW.bot_id AND chat_id=NEW.chat_id
 AND authorization_generation=NEW.authorization_generation INTO epoch_valid
 FROM openfox.telegram_group_epochs WHERE organization_id=NEW.organization_id AND id=NEW.epoch_id;
 IF epoch_valid IS DISTINCT FROM true THEN RAISE EXCEPTION 'invalid telegram input epoch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER telegram_input_scope BEFORE INSERT ON openfox.telegram_inputs FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_telegram_input_scope();
REVOKE ALL ON FUNCTION openfox_private.validate_telegram_input_scope() FROM PUBLIC;
CREATE TABLE openfox.telegram_replies (
 organization_id uuid NOT NULL,outbox_id uuid NOT NULL,input_id uuid,bot_id uuid NOT NULL,notice_route_id uuid,
 chat_id bigint NOT NULL,reply_to_message_id bigint NOT NULL CHECK(reply_to_message_id>0),
 authorization_generation bigint NOT NULL CHECK(authorization_generation>0),
 body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 4096),body_hash bytea NOT NULL CHECK(octet_length(body_hash)=32),
 claim_token text NOT NULL DEFAULT '' CHECK(char_length(claim_token)<=64),token_version bigint,
 notice boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,outbox_id),
 FOREIGN KEY(organization_id,outbox_id) REFERENCES openfox.im_reply_outbox(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,input_id) REFERENCES openfox.telegram_inputs(organization_id,id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,notice_route_id) REFERENCES openfox.telegram_routes(organization_id,id),
 CHECK((notice AND input_id IS NULL AND (chat_id>0 OR notice_route_id IS NOT NULL)) OR(NOT notice AND input_id IS NOT NULL AND notice_route_id IS NULL))
);
CREATE TABLE openfox.telegram_background_messages (
 organization_id uuid NOT NULL,epoch_id uuid NOT NULL,chat_message_id bigint NOT NULL CHECK(chat_message_id>0),
 sender_id bigint NOT NULL CHECK(sender_id BETWEEN 1 AND 4503599627370495),message_date timestamptz NOT NULL,
 body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 8000),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,epoch_id,chat_message_id),
 FOREIGN KEY(organization_id,epoch_id) REFERENCES openfox.telegram_group_epochs(organization_id,id) ON DELETE CASCADE
);
CREATE INDEX telegram_background_expire ON openfox.telegram_background_messages(organization_id,message_date);
CREATE TABLE openfox.telegram_chat_limits (
 organization_id uuid NOT NULL,bot_id uuid NOT NULL,chat_id bigint NOT NULL,
 not_before timestamptz NOT NULL DEFAULT clock_timestamp(),notice_after timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,bot_id,chat_id),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id)
);

-- Serialize only queue admission. Never take a Bot row lock from a message
-- trigger (the conversation is already locked by PostMessage/Finalize).
CREATE FUNCTION openfox_private.limit_telegram_reply_queue() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE pending integer;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM openfox.im_bots b JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id
 WHERE b.organization_id=NEW.organization_id AND b.id=NEW.bot_id AND c.provider='telegram') THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(NEW.organization_id::text||':telegram:reply-budget:'||NEW.bot_id::text,0));
 IF NEW.message_id IS NOT NULL AND EXISTS(SELECT 1 FROM openfox.im_reply_outbox WHERE organization_id=NEW.organization_id AND message_id=NEW.message_id) THEN RETURN NEW; END IF;
 SELECT count(*) INTO pending FROM(SELECT 1 FROM openfox.im_reply_outbox WHERE organization_id=NEW.organization_id AND bot_id=NEW.bot_id AND status IN ('PENDING','SENDING') LIMIT 1000) q;
 IF pending>=1000 THEN
  -- Preserve the local terminal message and expose a durable delivery failure.
  -- Queue saturation must not roll back task/Run completion.
  NEW.status:='FAILED';NEW.last_error:='queue_full';NEW.finished_at:=clock_timestamp();
  NEW.lease_owner:='';NEW.lease_expires_at:=NULL;
 END IF;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION openfox_private.limit_telegram_reply_queue() FROM PUBLIC;
CREATE TRIGGER telegram_reply_queue_budget BEFORE INSERT ON openfox.im_reply_outbox FOR EACH ROW EXECUTE FUNCTION openfox_private.limit_telegram_reply_queue();

DO $policies$
DECLARE table_name text;
BEGIN
 FOREACH table_name IN ARRAY ARRAY['telegram_bot_controls','telegram_connection_preparations','telegram_identities',
 'telegram_binding_sessions','telegram_group_discoveries','telegram_group_epochs','telegram_routes','telegram_update_receipts',
 'telegram_inputs','telegram_replies','telegram_background_messages','telegram_chat_limits'] LOOP
 EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',table_name);
 EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',table_name);
 EXECUTE format('CREATE POLICY %I ON openfox.%I USING (organization_id=openfox_private.current_organization_id()) WITH CHECK (organization_id=openfox_private.current_organization_id())',table_name||'_tenant',table_name);
 END LOOP;
END $policies$;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE ON openfox.telegram_bot_controls,openfox.telegram_connection_preparations,
 openfox.telegram_identities,openfox.telegram_binding_sessions,openfox.telegram_group_discoveries,
 openfox.telegram_group_epochs,openfox.telegram_routes,openfox.telegram_update_receipts,
 openfox.telegram_replies,openfox.telegram_background_messages,openfox.telegram_chat_limits TO openfox_app;
GRANT DELETE ON openfox.telegram_connection_preparations,openfox.telegram_binding_sessions,openfox.telegram_group_discoveries,
 openfox.telegram_replies,openfox.telegram_background_messages,openfox.telegram_chat_limits TO openfox_app;
GRANT SELECT,INSERT ON openfox.telegram_inputs TO openfox_app;

SET ROLE openfox_migrator;
CREATE POLICY telegram_bot_controls_control_read ON openfox.telegram_bot_controls FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY telegram_connection_preparations_control_read ON openfox.telegram_connection_preparations FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY telegram_binding_sessions_control_read ON openfox.telegram_binding_sessions FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY telegram_group_discoveries_control_read ON openfox.telegram_group_discoveries FOR SELECT TO openfox_migrator USING(true);
CREATE POLICY telegram_background_messages_control_read ON openfox.telegram_background_messages FOR SELECT TO openfox_migrator USING(true);

CREATE FUNCTION openfox_private.resolve_telegram_webhook(p_route_hash bytea)
RETURNS TABLE(organization_id uuid,bot_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT t.organization_id,t.bot_id FROM openfox.telegram_bot_controls t
 JOIN openfox.integration_connections c ON c.organization_id=t.organization_id AND c.id=t.connection_id
 JOIN openfox.organization_directory d ON d.organization_id=t.organization_id AND d.deleted_at IS NULL
 WHERE octet_length(p_route_hash)=32 AND t.route_hash=p_route_hash
 AND c.provider='telegram' AND c.status<>'DISCONNECTED' AND t.state IN ('SETTING','ACTIVE','ATTENTION')
$$;
ALTER FUNCTION openfox_private.resolve_telegram_webhook(bytea) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_telegram_webhook(bytea) FROM PUBLIC;

-- Preserve the complete 145 scanner (candidate SHA 420e368f...) plus Telegram.
CREATE OR REPLACE FUNCTION openfox_private.list_im_worker_organizations(p_after uuid, p_limit integer)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 THEN
        RAISE EXCEPTION 'invalid im worker scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT o.organization_id FROM (
        SELECT r.organization_id FROM openfox.im_app_registrations r
         WHERE r.status = 'PENDING' AND r.next_poll_at <= clock_timestamp()
           AND (r.lease_expires_at IS NULL OR r.lease_expires_at <= clock_timestamp())
        UNION
        SELECT i.organization_id FROM openfox.im_inbound_messages i
         WHERE (i.status = 'PENDING' AND i.next_attempt_at <= clock_timestamp())
            OR (i.status = 'PROCESSING' AND (i.lease_expires_at IS NULL OR i.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT q.organization_id FROM openfox.im_reply_outbox q
         WHERE (q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
            OR (q.status = 'SENDING' AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
        UNION
        SELECT a.organization_id FROM openfox.slack_apps a
         WHERE (a.next_attempt_at<=clock_timestamp() AND (a.lease_expires_at IS NULL OR a.lease_expires_at<=clock_timestamp()))
            OR (a.state IN ('PROVING','CREATING') AND a.lease_expires_at<=clock_timestamp())
            OR (a.state='ACTIVE' AND a.token_expires_at<=clock_timestamp()+interval '5 minutes' AND (a.next_attempt_at IS NULL OR a.next_attempt_at<=clock_timestamp()))
            OR (a.state='WAITING_URL_VERIFICATION' AND a.kind='CENTRAL')
            OR (a.refresh_lease_owner<>'' AND a.refresh_lease_expires_at<=clock_timestamp())
        UNION
        SELECT e.organization_id FROM openfox.slack_events e
         WHERE (e.state='PENDING' AND e.next_attempt_at<=clock_timestamp()) OR (e.state='PROCESSING' AND e.lease_expires_at<=clock_timestamp())
        UNION
        SELECT d.organization_id FROM openfox.slack_deliveries d
         WHERE (d.state='PENDING' AND d.next_attempt_at<=clock_timestamp()) OR (d.state='SENDING' AND d.lease_expires_at<=clock_timestamp())
        UNION
        SELECT s.organization_id FROM openfox.slack_oauth_sessions s
         WHERE (s.status='EXCHANGING' AND s.lease_expires_at<=clock_timestamp()) OR (s.status='PENDING' AND s.expires_at<=clock_timestamp())
        UNION
        SELECT n.organization_id FROM openfox.im_notification_outbox n
         JOIN openfox.integration_connections c ON c.organization_id=n.organization_id AND c.id=n.app_connection_id
         WHERE c.provider='slack' AND ((n.status='PENDING' AND n.next_attempt_at<=clock_timestamp()) OR (n.status='SENDING' AND n.lease_expires_at<=clock_timestamp()))
        UNION
        SELECT t.organization_id FROM openfox.telegram_bot_controls t
         WHERE t.state<>'DISCONNECTED' AND ((t.next_attempt_at IS NOT NULL AND t.next_attempt_at<=clock_timestamp())
         OR (t.control_status='SENDING' AND t.control_lease_until<=clock_timestamp()))
        UNION
        SELECT p.organization_id FROM openfox.telegram_connection_preparations p
         WHERE (p.status='PENDING_CONFIRM' AND p.expires_at<=clock_timestamp()) OR p.created_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT s.organization_id FROM openfox.telegram_binding_sessions s
         WHERE (s.status IN ('WAITING','CANDIDATE') AND s.expires_at<=clock_timestamp()) OR s.created_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT g.organization_id FROM openfox.telegram_group_discoveries g WHERE g.expires_at<=clock_timestamp()
        UNION
        SELECT b.organization_id FROM openfox.telegram_background_messages b WHERE b.message_date<clock_timestamp()-interval '10 minutes'
        UNION
        SELECT m.organization_id FROM openfox.im_inbound_messages m JOIN openfox.telegram_bot_controls t ON t.organization_id=m.organization_id AND t.bot_id=m.bot_id
         WHERE m.finished_at<clock_timestamp()-interval '7 days'
        UNION
        SELECT q.organization_id FROM openfox.im_reply_outbox q JOIN openfox.telegram_bot_controls t ON t.organization_id=q.organization_id AND t.bot_id=q.bot_id
         WHERE q.finished_at<clock_timestamp()-interval '30 days'
        UNION
        SELECT e.organization_id FROM openfox.im_inbound_events e JOIN openfox.telegram_bot_controls t ON t.organization_id=e.organization_id AND t.connection_id=e.connection_id
         WHERE e.received_at<clock_timestamp()-interval '7 days'
    ) o
    JOIN openfox.organization_directory d ON d.organization_id = o.organization_id AND d.deleted_at IS NULL
     WHERE p_after IS NULL OR o.organization_id > p_after
     ORDER BY o.organization_id
     LIMIT p_limit;
END
$$;
ALTER FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_im_worker_organizations(uuid, integer) FROM PUBLIC;

RESET ROLE;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_telegram_webhook(bytea) TO openfox_context;
