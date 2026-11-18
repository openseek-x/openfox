SET ROLE openfox_migrator;
CREATE TABLE openfox.conversation_offers (
 organization_id uuid NOT NULL, id uuid NOT NULL DEFAULT gen_random_uuid(), conversation_id uuid NOT NULL,
 sender_user_id uuid NOT NULL, recipient_user_id uuid NOT NULL,
 kind text NOT NULL CHECK (kind IN ('INVITE','TRANSFER')),
 status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','ACCEPTED','DECLINED')),
 note text NOT NULL DEFAULT '' CHECK (char_length(note)<=1000), transfer_assignee boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), decided_at timestamptz,
 PRIMARY KEY (organization_id,id),
 FOREIGN KEY (organization_id,conversation_id) REFERENCES openfox.conversations(organization_id,id),
 FOREIGN KEY (organization_id,sender_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY (organization_id,recipient_user_id) REFERENCES openfox.organization_members(organization_id,user_id),
 CHECK (sender_user_id<>recipient_user_id AND (NOT transfer_assignee OR kind='TRANSFER') AND ((status='PENDING')=(decided_at IS NULL)))
);
CREATE UNIQUE INDEX conversation_offers_pending_unique ON openfox.conversation_offers(organization_id,conversation_id,kind,sender_user_id,recipient_user_id) WHERE status='PENDING';
CREATE INDEX conversation_offers_recipient_pending ON openfox.conversation_offers(organization_id,recipient_user_id,created_at DESC,id DESC) WHERE status='PENDING';
ALTER TABLE openfox.conversation_offers ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.conversation_offers FORCE ROW LEVEL SECURITY;
CREATE POLICY offer_participants_read ON openfox.conversation_offers FOR SELECT USING (organization_id=openfox_private.current_organization_id() AND openfox_private.current_actor_id() IN (sender_user_id,recipient_user_id));
CREATE POLICY offer_sender_insert ON openfox.conversation_offers FOR INSERT WITH CHECK (organization_id=openfox_private.current_organization_id() AND sender_user_id=openfox_private.current_actor_id() AND status='PENDING');
CREATE POLICY offer_recipient_update ON openfox.conversation_offers FOR UPDATE USING (organization_id=openfox_private.current_organization_id() AND recipient_user_id=openfox_private.current_actor_id()) WITH CHECK (organization_id=openfox_private.current_organization_id() AND recipient_user_id=openfox_private.current_actor_id());
GRANT SELECT,INSERT,UPDATE (status,decided_at) ON openfox.conversation_offers TO openfox_app;
CREATE TRIGGER offers_inbox_insert AFTER INSERT ON openfox.conversation_offers REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_inbox_events();
CREATE TRIGGER offers_inbox_update AFTER UPDATE ON openfox.conversation_offers REFERENCING NEW TABLE AS changed FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.emit_inbox_events();
RESET ROLE;
