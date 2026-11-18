-- I16: commercial classic Teams, typed immutable origin and current authority.
-- Expand-only. Execute only in a fresh isolated test database for this delivery.
-- No credentials in these tables; existing sealed integration connection is used.
-- Rollback first disables Teams ingress/scopes/claims, waits owned sends, then
-- rolls back application code while retaining source/history and unknown results.
-- No destructive rollback, automatic resend, or new common outbox status.
SET ROLE openfox_migrator;

ALTER TABLE openfox.im_chat_bindings ADD COLUMN teams_context jsonb
 CHECK(teams_context IS NULL OR (jsonb_typeof(teams_context)='object' AND pg_column_size(teams_context)<=16384));
ALTER TABLE openfox.im_inbound_messages ADD COLUMN teams_context jsonb
 CHECK(teams_context IS NULL OR (jsonb_typeof(teams_context)='object' AND pg_column_size(teams_context)<=16384));
ALTER TABLE openfox.im_reply_outbox ADD COLUMN teams_context jsonb
 CHECK(teams_context IS NULL OR (jsonb_typeof(teams_context)='object' AND pg_column_size(teams_context)<=16384));
ALTER TABLE openfox.im_inbound_events ADD COLUMN teams_payload_hash bytea
 CHECK(teams_payload_hash IS NULL OR octet_length(teams_payload_hash)=32);

CREATE UNIQUE INDEX integration_connections_teams_app_unique
 ON openfox.integration_connections(lower(external_id))
 WHERE provider='teams' AND scope='ORGANIZATION' AND status<>'DISCONNECTED';
CREATE UNIQUE INDEX integration_connections_teams_identity_unique
 ON openfox.integration_connections(organization_id,parent_connection_id,lower(external_id))
 WHERE provider='teams' AND scope='PERSONAL' AND status<>'DISCONNECTED';

CREATE UNIQUE INDEX integration_connections_teams_from_unique
 ON openfox.integration_connections(organization_id,parent_connection_id,(settings->>'from_id'))
 WHERE provider='teams' AND scope='PERSONAL' AND status<>'DISCONNECTED';

CREATE TABLE openfox.teams_binding_challenges (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,user_id uuid NOT NULL,
 code_hash bytea NOT NULL CHECK(octet_length(code_hash)=32),
 actor_version bigint NOT NULL CHECK(actor_version>0),bot_version bigint NOT NULL CHECK(bot_version>0),
 expires_at timestamptz NOT NULL,consumed_at timestamptz,invalidated_at timestamptz,identity_id uuid,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,id),UNIQUE(code_hash),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.integration_connections(organization_id,id),
 CHECK(expires_at>created_at),CHECK(consumed_at IS NULL OR invalidated_at IS NULL),
 CHECK((consumed_at IS NULL)=(identity_id IS NULL))
);
CREATE UNIQUE INDEX teams_binding_challenges_pending ON openfox.teams_binding_challenges(organization_id,bot_id,user_id)
 WHERE consumed_at IS NULL AND invalidated_at IS NULL;
CREATE INDEX teams_binding_challenges_expiry ON openfox.teams_binding_challenges(organization_id,expires_at);

CREATE TABLE openfox.teams_scope_authorizations (
 organization_id uuid NOT NULL,id uuid NOT NULL DEFAULT gen_random_uuid(),bot_id uuid NOT NULL,
 scope_key text NOT NULL CHECK(scope_key ~ '^teams:[0-9a-f]{64}$'),
 kind text NOT NULL CHECK(kind IN ('group','channel')),
 route jsonb NOT NULL CHECK(jsonb_typeof(route)='object' AND pg_column_size(route)<=16384),
 generation bigint NOT NULL CHECK(generation>0),version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 enabled boolean NOT NULL DEFAULT false,public_reply_consent boolean NOT NULL DEFAULT false,configured_by uuid,
 discovered_at timestamptz NOT NULL DEFAULT clock_timestamp(),last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 disabled_at timestamptz,retired_at timestamptz,
 PRIMARY KEY(organization_id,id),UNIQUE(organization_id,bot_id,scope_key,generation),
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,configured_by) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK(NOT enabled OR (public_reply_consent AND configured_by IS NOT NULL AND disabled_at IS NULL AND retired_at IS NULL))
);
CREATE UNIQUE INDEX teams_scope_authorizations_current ON openfox.teams_scope_authorizations(organization_id,bot_id,scope_key)
 WHERE retired_at IS NULL;
CREATE INDEX teams_scope_authorizations_list ON openfox.teams_scope_authorizations(organization_id,bot_id,last_seen_at DESC,id)
 WHERE retired_at IS NULL;

-- Source survives queue retention. The nullable inbound FK is only a live proof
-- pointer; message/route/actor/identity/epoch facts are never rewritten by app.
CREATE TABLE openfox.teams_inputs (
 organization_id uuid NOT NULL,message_id uuid NOT NULL,bot_id uuid NOT NULL,binding_id uuid NOT NULL,
 source_kind text NOT NULL CHECK(source_kind IN ('REMOTE','OPENFOX')),
 actor_id uuid NOT NULL,actor_version bigint NOT NULL CHECK(actor_version>0),
 identity_id uuid NOT NULL,identity_version bigint NOT NULL CHECK(identity_version>0),
 scope_id uuid,scope_generation bigint,
 inbound_id uuid,event_id text,payload_hash bytea,route jsonb,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(organization_id,message_id),UNIQUE(organization_id,inbound_id),
 FOREIGN KEY(organization_id,message_id) REFERENCES openfox.messages(organization_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(organization_id,bot_id) REFERENCES openfox.im_bots(organization_id,id),
 FOREIGN KEY(organization_id,binding_id) REFERENCES openfox.im_chat_bindings(organization_id,id),
 FOREIGN KEY(organization_id,actor_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY(organization_id,identity_id) REFERENCES openfox.integration_connections(organization_id,id),
 FOREIGN KEY(organization_id,scope_id) REFERENCES openfox.teams_scope_authorizations(organization_id,id),
 FOREIGN KEY(organization_id,inbound_id) REFERENCES openfox.im_inbound_messages(organization_id,id) ON DELETE SET NULL(inbound_id),
 CHECK((scope_id IS NULL)=(scope_generation IS NULL)),CHECK(scope_generation IS NULL OR scope_generation>0),
 CHECK((source_kind='REMOTE' AND event_id IS NOT NULL AND char_length(event_id) BETWEEN 1 AND 128
 AND payload_hash IS NOT NULL AND octet_length(payload_hash)=32 AND route IS NOT NULL
 AND jsonb_typeof(route)='object' AND pg_column_size(route)<=16384)
 OR (source_kind='OPENFOX' AND inbound_id IS NULL AND event_id IS NULL AND payload_hash IS NULL AND route IS NULL))
);
CREATE INDEX teams_inputs_binding ON openfox.teams_inputs(organization_id,binding_id,message_id);

CREATE FUNCTION openfox_private.validate_teams_input_scope() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
DECLARE b_bot uuid;b_context jsonb;b_type text;p_connection uuid;p_status text;p_removed timestamptz;
 i_parent uuid;i_owner uuid;i_version bigint;i_status text;i_provider text;i_scope text;member_ok boolean;scope_ok boolean;
BEGIN
 SELECT bot_id,teams_context,chat_type INTO b_bot,b_context,b_type FROM openfox.im_chat_bindings
 WHERE organization_id=NEW.organization_id AND id=NEW.binding_id;
 SELECT b.connection_id,c.status,b.removed_at INTO p_connection,p_status,p_removed
 FROM openfox.im_bots b JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 WHERE b.organization_id=NEW.organization_id AND b.id=NEW.bot_id AND c.provider='teams';
 SELECT parent_connection_id,owner_user_id,version,status,provider,scope INTO i_parent,i_owner,i_version,i_status,i_provider,i_scope
 FROM openfox.integration_connections WHERE organization_id=NEW.organization_id AND id=NEW.identity_id;
 SELECT status='ACTIVE' AND role<>'GUEST' AND authorization_version=NEW.actor_version INTO member_ok
 FROM openfox.organization_members WHERE organization_id=NEW.organization_id AND user_id=NEW.actor_id;
 IF b_bot IS DISTINCT FROM NEW.bot_id OR b_context IS NULL OR p_connection IS NULL OR p_status<>'ACTIVE' OR p_removed IS NOT NULL
 OR i_parent IS DISTINCT FROM p_connection OR i_owner IS DISTINCT FROM NEW.actor_id OR i_version IS DISTINCT FROM NEW.identity_version
 OR i_status IS DISTINCT FROM 'ACTIVE' OR i_provider IS DISTINCT FROM 'teams' OR i_scope IS DISTINCT FROM 'PERSONAL'
 OR member_ok IS DISTINCT FROM true THEN RAISE EXCEPTION 'invalid teams input authority' USING ERRCODE='23514'; END IF;
 IF b_type='p2p' THEN
  IF NEW.scope_id IS NOT NULL OR b_context->>'identity_id' IS DISTINCT FROM NEW.identity_id::text
  OR b_context->>'identity_version' IS DISTINCT FROM NEW.identity_version::text THEN
   RAISE EXCEPTION 'invalid teams personal input' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT enabled AND public_reply_consent AND disabled_at IS NULL AND retired_at IS NULL
   AND bot_id=NEW.bot_id AND generation=NEW.scope_generation INTO scope_ok
  FROM openfox.teams_scope_authorizations WHERE organization_id=NEW.organization_id AND id=NEW.scope_id;
  IF scope_ok IS DISTINCT FROM true OR b_context->>'scope_id' IS DISTINCT FROM NEW.scope_id::text
  OR b_context->>'scope_generation' IS DISTINCT FROM NEW.scope_generation::text THEN
   RAISE EXCEPTION 'invalid teams scope input' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER teams_input_scope BEFORE INSERT ON openfox.teams_inputs FOR EACH ROW EXECUTE FUNCTION openfox_private.validate_teams_input_scope();
REVOKE ALL ON FUNCTION openfox_private.validate_teams_input_scope() FROM PUBLIC;

-- History cannot be rebound to another authority after it has accepted inputs.
CREATE FUNCTION openfox_private.preserve_teams_binding() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,openfox,openfox_private AS $$
BEGIN
 IF OLD.teams_context IS NOT NULL AND (TG_OP='DELETE' OR
 (NEW.bot_id,NEW.chat_id,NEW.chat_type,NEW.conversation_id,NEW.teams_context)
 IS DISTINCT FROM (OLD.bot_id,OLD.chat_id,OLD.chat_type,OLD.conversation_id,OLD.teams_context)) THEN
 RAISE EXCEPTION 'teams binding is immutable' USING ERRCODE='23514'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER teams_binding_immutable BEFORE UPDATE OR DELETE ON openfox.im_chat_bindings FOR EACH ROW EXECUTE FUNCTION openfox_private.preserve_teams_binding();
REVOKE ALL ON FUNCTION openfox_private.preserve_teams_binding() FROM PUBLIC;

DO $policies$ DECLARE table_name text; BEGIN
 FOREACH table_name IN ARRAY ARRAY['teams_binding_challenges','teams_scope_authorizations','teams_inputs'] LOOP
 EXECUTE format('ALTER TABLE openfox.%I ENABLE ROW LEVEL SECURITY',table_name);
 EXECUTE format('ALTER TABLE openfox.%I FORCE ROW LEVEL SECURITY',table_name);
 EXECUTE format('CREATE POLICY %I ON openfox.%I USING (organization_id=openfox_private.current_organization_id()) WITH CHECK (organization_id=openfox_private.current_organization_id())',table_name||'_tenant',table_name);
 END LOOP;
END $policies$;
RESET ROLE;
GRANT SELECT,INSERT,UPDATE,DELETE ON openfox.teams_binding_challenges TO openfox_app;
GRANT SELECT,INSERT,UPDATE ON openfox.teams_scope_authorizations TO openfox_app;
GRANT SELECT,INSERT ON openfox.teams_inputs TO openfox_app;

SET ROLE openfox_migrator;
-- Public bot UUID is only a route locator. No credential escapes this function.
-- Existing migrator SELECT policies on im_bots/connections/directory are reused.
CREATE FUNCTION openfox_private.resolve_teams_webhook(p_bot_id uuid)
RETURNS TABLE(organization_id uuid,bot_id uuid,tenant_id text,app_id text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,openfox,openfox_private AS $$
 SELECT b.organization_id,b.id,c.settings->>'tenant_id',c.external_id
 FROM openfox.im_bots b JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(b.organization_id,b.connection_id)
 JOIN openfox.organization_directory d ON d.organization_id=b.organization_id AND d.deleted_at IS NULL
 WHERE b.id=p_bot_id AND b.removed_at IS NULL AND c.provider='teams' AND c.scope='ORGANIZATION' AND c.status='ACTIVE'
$$;
ALTER FUNCTION openfox_private.resolve_teams_webhook(uuid) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.resolve_teams_webhook(uuid) FROM PUBLIC;
RESET ROLE;
-- Public function execute grants and cumulative enqueue_im_reply provider
-- exclusion are coordinated with the root-owned 148/149/150 shared wiring.

-- Preserve the complete 129 trigger and the 149 Discord exclusion. Teams
-- replies are inserted only by the actual terminal Run helper.
SET ROLE openfox_migrator;
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
 WHERE c.provider NOT IN ('discord','teams') AND n.author_kind IN ('AGENT','SYSTEM') AND (c.provider<>'wecom' OR b.chat_type<>'group' OR ga.version IS NOT NULL)
 ON CONFLICT DO NOTHING;
 RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_im_reply() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_im_reply() FROM PUBLIC;
RESET ROLE;
GRANT EXECUTE ON FUNCTION openfox_private.resolve_teams_webhook(uuid) TO openfox_context;
