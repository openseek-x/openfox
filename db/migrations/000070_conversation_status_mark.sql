-- 共享展示标识；历史行默认为 NULL。生命周期、交接与 Run 状态独立。
SET ROLE openfox_migrator;

ALTER TABLE openfox.conversations ADD COLUMN status_mark text;
ALTER TABLE openfox.conversations ADD CONSTRAINT conversations_status_mark_valid
    CHECK (status_mark IN ('DONE', 'DISCARDED')) NOT VALID;
ALTER TABLE openfox.conversations VALIDATE CONSTRAINT conversations_status_mark_valid;

CREATE OR REPLACE FUNCTION openfox_private.emit_conversation_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
BEGIN
    IF (NEW.lifecycle, NEW.handoff, NEW.title, NEW.archived_at, NEW.task_id, NEW.visibility, NEW.overrides_version, NEW.status_mark)
       IS NOT DISTINCT FROM
       (OLD.lifecycle, OLD.handoff, OLD.title, OLD.archived_at, OLD.task_id, OLD.visibility, OLD.overrides_version, OLD.status_mark) THEN
        RETURN NULL;
    END IF;
    INSERT INTO openfox.organization_events (
        organization_id, type, entity, entity_id, version, space_id, conversation_id, payload
    ) VALUES (
        NEW.organization_id, 'conversation.updated', 'conversation', NEW.id, NEW.version,
        NEW.space_id, NEW.id,
        jsonb_build_object('lifecycle', NEW.lifecycle, 'handoff', NEW.handoff)
    );
    RETURN NULL;
END
$$;

RESET ROLE;
