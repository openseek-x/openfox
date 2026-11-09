SET ROLE openfox_migrator;

CREATE OR REPLACE FUNCTION openfox_private.validate_conversation_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    agent_space_id uuid;
    agent_status text;
    agent_archived_at timestamptz;
    agent_builtin text;
    agent_owner uuid;
    task_space_id uuid;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.organization_id = OLD.organization_id
       AND NEW.space_id = OLD.space_id
       AND NEW.agent_id = OLD.agent_id
       AND (NEW.owner_user_id = OLD.owner_user_id OR NEW.space_id =
           (SELECT space_id FROM openfox.agents WHERE organization_id=NEW.organization_id AND id=NEW.agent_id))
       AND NEW.task_id IS NOT DISTINCT FROM OLD.task_id THEN
        RETURN NEW;
    END IF;
    SELECT space_id, status, archived_at, builtin_kind, owner_user_id
      INTO agent_space_id, agent_status, agent_archived_at, agent_builtin, agent_owner
      FROM openfox.agents
     WHERE organization_id = NEW.organization_id AND id = NEW.agent_id;
    IF NOT FOUND OR agent_space_id IS NULL
       OR (agent_space_id <> NEW.space_id AND NOT
           (agent_builtin IS NOT DISTINCT FROM 'ASSISTANT' AND agent_owner IS NOT DISTINCT FROM NEW.owner_user_id))
       OR agent_status <> 'ACTIVE' OR agent_archived_at IS NOT NULL THEN
        RAISE EXCEPTION 'conversation Agent is not active in the requested scope'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.task_id IS NOT NULL THEN
        SELECT space_id INTO task_space_id FROM openfox.tasks
         WHERE organization_id = NEW.organization_id AND id = NEW.task_id;
        IF NOT FOUND OR task_space_id <> NEW.space_id THEN
            RAISE EXCEPTION 'conversation task is outside the requested space'
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

RESET ROLE;
