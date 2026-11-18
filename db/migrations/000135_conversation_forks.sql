SET ROLE openfox_migrator;
ALTER TABLE openfox.conversations
  ADD COLUMN parent_conversation_id uuid,
  ADD COLUMN fork_message_id uuid,
  ADD COLUMN fork_prefix_seq bigint,
  ADD CONSTRAINT conversations_fork_metadata_valid CHECK (
    (parent_conversation_id IS NULL AND fork_message_id IS NULL AND fork_prefix_seq IS NULL)
    OR
    (parent_conversation_id IS NOT NULL AND fork_message_id IS NOT NULL
     AND fork_prefix_seq IS NOT NULL AND fork_prefix_seq > 0
     AND fork_prefix_seq < 9223372036854775807
     AND parent_conversation_id <> id)
  );
RESET ROLE;
