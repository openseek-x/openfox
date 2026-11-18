-- Optional intent on a user's assistant message. Existing rows have NULL.
SET ROLE openfox_migrator;
ALTER TABLE openfox.messages
    ADD COLUMN assistant_action text,
    ADD CONSTRAINT messages_assistant_action_valid
        CHECK (assistant_action IS NULL OR
               (author_kind = 'USER' AND kind = 'TEXT' AND assistant_action IN ('create_task', 'create_agent'))) NOT VALID;
RESET ROLE;
