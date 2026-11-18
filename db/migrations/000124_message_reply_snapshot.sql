-- C01: preserve the quoted text at send time. Existing reply_to_id and its
-- same-conversation FK remain unchanged. No historical snapshot is invented.
SET ROLE openfox_migrator;

ALTER TABLE openfox.messages ADD COLUMN reply_snapshot jsonb;
ALTER TABLE openfox.messages ADD CONSTRAINT messages_reply_snapshot_valid CHECK (
    reply_snapshot IS NULL OR (
        reply_to_id IS NOT NULL
        AND jsonb_typeof(reply_snapshot) = 'object'
        AND reply_snapshot ?& ARRAY['message_id', 'seq', 'author_kind', 'body', 'created_at', 'truncated']
        AND reply_snapshot->>'message_id' = reply_to_id::text
        AND jsonb_typeof(reply_snapshot->'body') = 'string'
        AND length(reply_snapshot->>'body') <= 2000
        AND octet_length(reply_snapshot::text) <= 16384
    )
);

RESET ROLE;
