-- D04: passive document pointers; retain task and prior message semantics.
SET ROLE openfox_migrator;

ALTER TABLE openfox.messages DROP CONSTRAINT messages_payload_shape_valid;
ALTER TABLE openfox.messages ADD CONSTRAINT messages_payload_shape_valid CHECK (
    (kind = 'TEXT' AND payload IS NULL)
    OR (kind IN ('ACTION_PROPOSAL', 'ACTION_EXECUTED') AND author_kind = 'AGENT'
        AND jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 65536)
    OR (kind = 'REFERENCE' AND (
        author_kind = 'USER' AND reply_to_id IS NULL
        AND reply_snapshot IS NULL AND assistant_action IS NULL
        AND jsonb_typeof(payload) = 'object'
        AND ((payload->>'type' = 'TASK' AND body = '任务引用')
          OR (payload->>'type' = 'DOC' AND body = '文档引用'))
        AND jsonb_typeof(payload->'id') = 'string'
        AND payload->>'id' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND payload - ARRAY['type', 'id'] = '{}'::jsonb
    ) IS TRUE)
);

RESET ROLE;
