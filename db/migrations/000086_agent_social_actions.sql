-- Add an Agent author without reattributing any existing human reaction.
SET ROLE openfox_migrator;
ALTER TABLE openfox.task_reactions
    ALTER COLUMN user_id DROP NOT NULL,
    ADD COLUMN agent_id uuid,
    ADD COLUMN run_id uuid,
    ADD FOREIGN KEY (organization_id,agent_id) REFERENCES openfox.agents(organization_id,id),
    ADD FOREIGN KEY (organization_id,run_id) REFERENCES openfox.runs(organization_id,id),
    ADD CONSTRAINT reaction_author_valid CHECK (
      (user_id IS NOT NULL AND agent_id IS NULL AND run_id IS NULL) OR
      (user_id IS NULL AND agent_id IS NOT NULL AND run_id IS NOT NULL));
CREATE UNIQUE INDEX task_reaction_agent_unique ON openfox.task_reactions
    (organization_id,task_id,COALESCE(comment_id,'00000000-0000-0000-0000-000000000000'::uuid),agent_id,emoji) WHERE agent_id IS NOT NULL;
ALTER TABLE openfox.notifications ADD COLUMN actor_agent_id uuid,
    ADD FOREIGN KEY (organization_id,actor_agent_id) REFERENCES openfox.agents(organization_id,id);
CREATE FUNCTION openfox_private.agent_reaction_notice() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
DECLARE recipient uuid;
BEGIN
    IF NEW.comment_id IS NULL THEN
        SELECT actor_id INTO recipient FROM openfox.task_activities WHERE organization_id=NEW.organization_id AND task_id=NEW.task_id AND kind='task.created' ORDER BY created_at LIMIT 1;
    ELSE
        SELECT author_user_id INTO recipient FROM openfox.task_comments WHERE organization_id=NEW.organization_id AND id=NEW.comment_id;
    END IF;
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,task_id,comment_id,dedupe_key,safe_summary,actor_agent_id,inbox_event)
    SELECT NEW.organization_id,recipient,'TASK_REACTION',NEW.task_id,NEW.comment_id,'agent-reaction:'||NEW.id,'An Agent reacted to your content.',NEW.agent_id,'reaction_created_agent_author'
     WHERE recipient IS NOT NULL AND EXISTS(SELECT 1 FROM openfox_private.task_notice_audience(NEW.organization_id,NEW.task_id,recipient) a WHERE a.user_id=recipient)
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION openfox_private.agent_reaction_notice() FROM PUBLIC;
DROP TRIGGER reaction_notice ON openfox.task_reactions;
CREATE TRIGGER reaction_notice AFTER INSERT ON openfox.task_reactions FOR EACH ROW WHEN (NEW.user_id IS NOT NULL)
    EXECUTE FUNCTION openfox_private.task_social_notices();
CREATE TRIGGER agent_reaction_notice AFTER INSERT ON openfox.task_reactions FOR EACH ROW WHEN (NEW.agent_id IS NOT NULL)
    EXECUTE FUNCTION openfox_private.agent_reaction_notice();
RESET ROLE;
