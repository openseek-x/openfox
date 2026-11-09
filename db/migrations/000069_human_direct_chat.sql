-- 真人单聊：仅新增表，不修改 Agent 会话与执行数据。部署顺序：迁移，再应用。
-- 回滚应用可保留这些表与附件，旧应用忽略它们；不要删除用户聊天数据。
SET ROLE openfox_migrator;
CREATE TABLE openfox.direct_chats (
 organization_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 user_low uuid NOT NULL,
 user_high uuid NOT NULL,
 last_seq bigint NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
 read_low bigint NOT NULL DEFAULT 0 CHECK (read_low >= 0),
 read_high bigint NOT NULL DEFAULT 0 CHECK (read_high >= 0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (organization_id,id),
 UNIQUE (organization_id,user_low,user_high),
 CHECK (user_low < user_high),
 CHECK (read_low <= last_seq AND read_high <= last_seq),
 FOREIGN KEY (organization_id,user_low) REFERENCES openfox.organization_members(organization_id,user_id),
 FOREIGN KEY (organization_id,user_high) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE INDEX direct_chats_low_list ON openfox.direct_chats(organization_id,user_low,updated_at DESC,id);
CREATE INDEX direct_chats_high_list ON openfox.direct_chats(organization_id,user_high,updated_at DESC,id);
CREATE TABLE openfox.direct_messages (
 organization_id uuid NOT NULL,
 chat_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 seq bigint NOT NULL CHECK (seq > 0),
 author_id uuid NOT NULL,
 client_message_id uuid NOT NULL,
 body text NOT NULL CHECK (char_length(body) <= 65536),
 request_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (organization_id,id),
 UNIQUE (organization_id,chat_id,id),
 UNIQUE (organization_id,chat_id,seq),
 UNIQUE (organization_id,chat_id,author_id,client_message_id),
 FOREIGN KEY (organization_id,chat_id) REFERENCES openfox.direct_chats(organization_id,id),
 FOREIGN KEY (organization_id,author_id) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE TABLE openfox.direct_attachments (
 organization_id uuid NOT NULL,
 chat_id uuid NOT NULL,
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 message_id uuid,
 uploaded_by uuid NOT NULL,
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 255),
 file_key text NOT NULL,
 content_type text NOT NULL,
 size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 20971520),
 sha256 text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (organization_id,id),
 FOREIGN KEY (organization_id,chat_id) REFERENCES openfox.direct_chats(organization_id,id),
 FOREIGN KEY (organization_id,chat_id,message_id) REFERENCES openfox.direct_messages(organization_id,chat_id,id),
 FOREIGN KEY (organization_id,uploaded_by) REFERENCES openfox.organization_members(organization_id,user_id)
);
CREATE INDEX direct_attachments_pending ON openfox.direct_attachments(organization_id,chat_id,uploaded_by) WHERE message_id IS NULL;
CREATE INDEX direct_attachments_message ON openfox.direct_attachments(organization_id,message_id) WHERE message_id IS NOT NULL;
ALTER TABLE openfox.direct_chats ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.direct_chats FORCE ROW LEVEL SECURITY;
CREATE POLICY direct_chats_participant ON openfox.direct_chats
 USING (organization_id=openfox_private.current_organization_id() AND openfox_private.current_actor_id() IN (user_low,user_high))
 WITH CHECK (organization_id=openfox_private.current_organization_id() AND openfox_private.current_actor_id() IN (user_low,user_high));
ALTER TABLE openfox.direct_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.direct_messages FORCE ROW LEVEL SECURITY;
CREATE POLICY direct_messages_participant ON openfox.direct_messages
 USING (organization_id=openfox_private.current_organization_id() AND EXISTS (SELECT 1 FROM openfox.direct_chats c WHERE c.organization_id=direct_messages.organization_id AND c.id=chat_id))
 WITH CHECK (organization_id=openfox_private.current_organization_id() AND author_id=openfox_private.current_actor_id() AND EXISTS (SELECT 1 FROM openfox.direct_chats c WHERE c.organization_id=direct_messages.organization_id AND c.id=chat_id));
ALTER TABLE openfox.direct_attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.direct_attachments FORCE ROW LEVEL SECURITY;
CREATE POLICY direct_attachments_participant ON openfox.direct_attachments
 USING (organization_id=openfox_private.current_organization_id() AND (message_id IS NOT NULL OR uploaded_by=openfox_private.current_actor_id()) AND EXISTS (SELECT 1 FROM openfox.direct_chats c WHERE c.organization_id=direct_attachments.organization_id AND c.id=chat_id))
 WITH CHECK (organization_id=openfox_private.current_organization_id() AND uploaded_by=openfox_private.current_actor_id() AND EXISTS (SELECT 1 FROM openfox.direct_chats c WHERE c.organization_id=direct_attachments.organization_id AND c.id=chat_id));
RESET ROLE;
GRANT SELECT,INSERT ON openfox.direct_chats,openfox.direct_messages,openfox.direct_attachments TO openfox_app;
GRANT UPDATE (last_seq,read_low,read_high,updated_at) ON openfox.direct_chats TO openfox_app;
GRANT UPDATE (message_id),DELETE ON openfox.direct_attachments TO openfox_app;
