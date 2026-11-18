-- 按运行或消息回查输入时保留租户前缀，空关联不占索引空间。
SET ROLE openfox_migrator;
CREATE INDEX conversation_inputs_run_lookup_idx
 ON openfox.conversation_inputs (organization_id, run_id)
 WHERE run_id IS NOT NULL;
CREATE INDEX conversation_inputs_message_lookup_idx
 ON openfox.conversation_inputs (organization_id, message_id)
 WHERE message_id IS NOT NULL;
RESET ROLE;
