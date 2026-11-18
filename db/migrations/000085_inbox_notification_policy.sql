-- Snapshot delivery for new events only. NULL level keeps the legacy classification.
SET ROLE openfox_migrator;
CREATE TABLE openfox.inbox_policy_keys (
    key text PRIMARY KEY,
    group_key text REFERENCES openfox.inbox_policy_keys(key),
    default_level text NOT NULL CHECK (default_level IN ('off','important','normal')),
    position smallint NOT NULL UNIQUE
);
INSERT INTO openfox.inbox_policy_keys(key,group_key,default_level,position) VALUES
    ('assignments',NULL,'normal',0),
    ('task_assigned_assignee','assignments','important',1),
    ('task_unassigned_assignee','assignments','normal',2),
    ('task_assignee_updated_assignee','assignments','important',3),
    ('task_assignee_updated_follower','assignments','normal',4),
    ('task_subscribed_follower','assignments','normal',5),
    ('status_changes',NULL,'normal',6),
    ('task_status_updated_assignee','status_changes','important',7),
    ('task_status_updated_follower','status_changes','normal',8),
    ('task_priority_updated_assignee','status_changes','important',9),
    ('task_priority_updated_follower','status_changes','normal',10),
    ('task_start_date_updated_assignee','status_changes','important',11),
    ('task_start_date_updated_follower','status_changes','normal',12),
    ('task_due_date_updated_assignee','status_changes','important',13),
    ('task_due_date_updated_follower','status_changes','normal',14),
    ('task_completed_assignee','status_changes','important',15),
    ('task_completed_follower','status_changes','normal',16),
    ('comments',NULL,'normal',17),
    ('comment_created_member_assignee','comments','important',18),
    ('comment_replied_member_assignee','comments','important',19),
    ('comment_created_member_follower','comments','normal',20),
    ('comment_replied_member_follower','comments','normal',21),
    ('comment_replied_member_author','comments','normal',22),
    ('reaction_created_member_author','comments','normal',23),
    ('comment_created_agent_assignee','comments','normal',24),
    ('comment_replied_agent_assignee','comments','normal',25),
    ('comment_created_agent_follower','comments','normal',26),
    ('comment_replied_agent_follower','comments','normal',27),
    ('comment_replied_agent_author','comments','normal',28),
    ('reaction_created_agent_author','comments','normal',29),
    ('mentions',NULL,'normal',30),
    ('comment_mentioned_member_recipient','mentions','important',31),
    ('comment_mentioned_agent_recipient','mentions','important',32),
    ('chat_mentioned_member_recipient','mentions','important',33),
    ('chat_mentioned_agent_recipient','mentions','important',34),
    ('agent_activity',NULL,'normal',35),
    ('chat_replied_participant','agent_activity','off',36),
    ('task_agent_completed_participant','agent_activity','off',37),
    ('task_failed_participant','agent_activity','off',38),
    ('task_agent_blocked_participant','agent_activity','important',39),
    ('task_needs_human_input_participant','agent_activity','off',40),
    ('task_review_requested_participant','agent_activity','off',41),
    ('automation_runs',NULL,'normal',42),
    ('automation_run_completed_recipient','automation_runs','off',43),
    ('automation_run_failed_recipient','automation_runs','important',44),
    ('automation_run_cancelled_recipient','automation_runs','important',45),
    ('automation_run_skipped_recipient','automation_runs','important',46);
CREATE TABLE openfox.user_inbox_preferences (
    user_id uuid NOT NULL REFERENCES openfox.users(id),
    key text NOT NULL REFERENCES openfox.inbox_policy_keys(key),
    level text NOT NULL CHECK (level IN ('off','normal','important')),
    PRIMARY KEY (user_id,key)
);
GRANT SELECT ON openfox.inbox_policy_keys TO openfox_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.user_inbox_preferences TO openfox_app;
ALTER TABLE openfox.notifications
    ADD COLUMN inbox_event text REFERENCES openfox.inbox_policy_keys(key),
    ADD COLUMN inbox_level text CHECK (inbox_level IN ('off','important','normal'));

-- Some automation attempts finish before they have a conversation.
ALTER TABLE openfox.notifications
    ADD COLUMN automation_id uuid,
    ADD FOREIGN KEY (organization_id,automation_id) REFERENCES openfox.automations(organization_id,id) ON DELETE CASCADE,
    DROP CONSTRAINT notifications_source_present,
    ADD CONSTRAINT notifications_source_present CHECK (conversation_id IS NOT NULL OR task_id IS NOT NULL OR automation_id IS NOT NULL),
    DROP CONSTRAINT notifications_kind_valid,
    ADD CONSTRAINT notifications_kind_valid CHECK (kind IN ('MENTION','HANDOFF','HANDOFF_REVIEW','REPLY','TASK_COMMENT','RUN_FAILED','TASK_ASSIGNED','TRIAGE_NEW','AUTOMATION_RESULT','TASK_UPDATED','TASK_REACTION','RUN_BLOCKED','RUN_COMPLETED'));

-- Older producers keep their signature. Explicit producers supply the event;
-- the insert hook also covers the automation outbox and classifies exactly once.
CREATE FUNCTION openfox_private.snapshot_inbox_delivery() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, openfox AS $$
DECLARE leaf text; author_type text; audience text;
BEGIN
    leaf := NEW.inbox_event;
    IF leaf IS NULL THEN
        CASE NEW.kind
        WHEN 'MENTION' THEN
            IF NEW.comment_id IS NOT NULL THEN
                SELECT CASE WHEN author_agent_id IS NULL THEN 'member' ELSE 'agent' END INTO author_type
                  FROM openfox.task_comments WHERE organization_id=NEW.organization_id AND id=NEW.comment_id;
                leaf := 'comment_mentioned_' || author_type || '_recipient';
            ELSE
                SELECT CASE WHEN author_kind='AGENT' THEN 'agent' ELSE 'member' END INTO author_type
                  FROM openfox.messages WHERE organization_id=NEW.organization_id AND id=NEW.message_id;
                leaf := 'chat_mentioned_' || coalesce(author_type,'member') || '_recipient';
            END IF;
        WHEN 'TASK_COMMENT' THEN
            SELECT CASE WHEN c.author_agent_id IS NULL THEN 'member' ELSE 'agent' END,
                   CASE WHEN parent.author_user_id=NEW.recipient_user_id THEN 'author'
                        WHEN t.assignee_id=NEW.recipient_user_id OR EXISTS (SELECT 1 FROM openfox.task_assignees a
                         WHERE a.organization_id=c.organization_id AND a.task_id=c.task_id AND a.user_id=NEW.recipient_user_id) THEN 'assignee'
                        ELSE 'follower' END,
                   CASE WHEN c.reply_to_id IS NULL THEN 'comment_created_' ELSE 'comment_replied_' END
              INTO author_type,audience,leaf FROM openfox.task_comments c
              JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id
              LEFT JOIN openfox.task_comments parent ON parent.organization_id=c.organization_id AND parent.id=c.reply_to_id
             WHERE c.organization_id=NEW.organization_id AND c.id=NEW.comment_id;
            leaf := leaf || author_type || '_' || audience;
        WHEN 'TASK_ASSIGNED' THEN leaf := 'task_assigned_assignee';
        WHEN 'REPLY' THEN leaf := 'chat_replied_participant';
        WHEN 'RUN_FAILED' THEN leaf := 'task_failed_participant';
        WHEN 'HANDOFF' THEN leaf := 'task_needs_human_input_participant';
        WHEN 'HANDOFF_REVIEW' THEN leaf := 'task_review_requested_participant';
        WHEN 'RUN_BLOCKED' THEN leaf := 'task_agent_blocked_participant';
        WHEN 'RUN_COMPLETED' THEN leaf := 'task_agent_completed_participant';
        ELSE NULL;
        END CASE;
    END IF;
    IF leaf IS NULL THEN RETURN NEW; END IF;
    SELECT CASE WHEN g.level='off' THEN 'off' ELSE COALESCE(p.level,k.default_level) END
      INTO STRICT NEW.inbox_level FROM openfox.inbox_policy_keys k
      LEFT JOIN openfox.user_inbox_preferences p ON p.user_id=NEW.recipient_user_id AND p.key=k.key
      LEFT JOIN openfox.user_inbox_preferences g ON g.user_id=NEW.recipient_user_id AND g.key=k.group_key
     WHERE k.key=leaf AND k.group_key IS NOT NULL;
    NEW.inbox_event := leaf;
    IF NEW.inbox_level='off' AND leaf IN ('chat_replied_participant','task_failed_participant','task_needs_human_input_participant','task_review_requested_participant') THEN
        RETURN NULL; -- no row, no desktop signal and no IM outbox entry
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION openfox_private.snapshot_inbox_delivery() FROM PUBLIC;
CREATE TRIGGER notifications_snapshot_delivery BEFORE INSERT ON openfox.notifications
 FOR EACH ROW EXECUTE FUNCTION openfox_private.snapshot_inbox_delivery();

-- Build a deduplicated audience first; an assigned member may also follow the task.
CREATE FUNCTION openfox_private.task_notice_audience(org uuid, task uuid, extra uuid)
RETURNS TABLE(user_id uuid, audience text) LANGUAGE sql STABLE SET search_path=pg_catalog,openfox AS $$
    WITH candidates AS (
        SELECT a.user_id, 1 AS priority FROM openfox.task_assignees a WHERE a.organization_id=org AND a.task_id=task AND a.user_id IS NOT NULL
        UNION ALL SELECT t.assignee_id,1 FROM openfox.tasks t WHERE t.organization_id=org AND t.id=task
        UNION ALL SELECT f.user_id,2 FROM openfox.task_followers f WHERE f.organization_id=org AND f.task_id=task AND f.user_id IS NOT NULL
        UNION ALL SELECT extra,3
    )
    SELECT m.user_id, CASE min(c.priority) WHEN 1 THEN 'assignee' WHEN 2 THEN 'follower' ELSE 'author' END
      FROM candidates c JOIN openfox.organization_members m ON m.organization_id=org AND m.user_id=c.user_id AND m.status='ACTIVE'
      JOIN openfox.tasks t ON t.organization_id=org AND t.id=task AND t.deleted_at IS NULL
      JOIN openfox.spaces s ON s.organization_id=org AND s.id=t.space_id AND s.deleted_at IS NULL
      LEFT JOIN openfox.space_members sm ON sm.organization_id=org AND sm.space_id=s.id AND sm.user_id=m.user_id
     WHERE sm.status='ACTIVE' OR (s.visibility='SHARED' AND m.role<>'GUEST' AND sm.user_id IS NULL)
     GROUP BY m.user_id
$$;
REVOKE ALL ON FUNCTION openfox_private.task_notice_audience(uuid,uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION openfox_private.task_notice_audience(uuid,uuid,uuid) TO openfox_app;

CREATE FUNCTION openfox_private.task_activity_notices() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
DECLARE base text; target uuid; label text;
BEGIN
    base := CASE NEW.kind WHEN 'task.status_changed' THEN
             CASE WHEN EXISTS(SELECT 1 FROM openfox.task_statuses WHERE organization_id=NEW.organization_id AND id=NULLIF(NEW.safe_diff->>'to_status_id','')::uuid AND category='DONE')
                  THEN 'task_completed' ELSE 'task_status_updated' END
           WHEN 'task.priority_changed' THEN 'task_priority_updated'
           WHEN 'task.start_date_changed' THEN 'task_start_date_updated'
           WHEN 'task.due_date_changed' THEN 'task_due_date_updated'
           ELSE 'task_assignee_updated' END;
    IF base='task_assignee_updated' AND NEW.safe_diff->>'kind'='HUMAN' THEN target := NULLIF(NEW.safe_diff->>'id','')::uuid; END IF;
    label := CASE base WHEN 'task_completed' THEN 'Task completed.' WHEN 'task_status_updated' THEN 'Task status changed.'
           WHEN 'task_priority_updated' THEN 'Task priority changed.' WHEN 'task_start_date_updated' THEN 'Task start date changed.'
           WHEN 'task_due_date_updated' THEN 'Task due date changed.' ELSE 'Task assignees changed.' END;
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,task_id,dedupe_key,safe_summary,actor_user_id,inbox_event)
    SELECT NEW.organization_id,a.user_id,'TASK_UPDATED',NEW.task_id,'activity:'||NEW.id,label,NEW.actor_id,
           CASE WHEN a.user_id=target THEN 'task_unassigned_assignee' ELSE base||'_'||a.audience END
      FROM openfox_private.task_notice_audience(NEW.organization_id,NEW.task_id,target) a
     WHERE a.user_id IS DISTINCT FROM NEW.actor_id AND (a.user_id IS DISTINCT FROM target OR NEW.kind='task.assignee_removed')
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION openfox_private.task_activity_notices() FROM PUBLIC;
CREATE TRIGGER task_activity_notices AFTER INSERT ON openfox.task_activities FOR EACH ROW
 WHEN (NEW.kind IN ('task.status_changed','task.priority_changed','task.start_date_changed','task.due_date_changed','task.assignee_added','task.assignee_removed'))
 EXECUTE FUNCTION openfox_private.task_activity_notices();

CREATE FUNCTION openfox_private.task_social_notices() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
DECLARE recipient uuid; actor uuid; leaf text; comment uuid; label text;
BEGIN
    IF TG_TABLE_NAME='task_followers' THEN
        recipient:=NEW.user_id; actor:=NEW.created_by; leaf:='task_subscribed_follower'; label:='You were added as a task follower.';
        -- Assignment automatically follows the task; the assignment already notifies the recipient.
        IF EXISTS(SELECT 1 FROM openfox.task_assignees WHERE organization_id=NEW.organization_id AND task_id=NEW.task_id AND user_id=recipient) THEN RETURN NULL; END IF;
    ELSE
        comment:=NEW.comment_id; actor:=NEW.user_id; leaf:='reaction_created_member_author'; label:='A team member reacted to your content.';
        IF comment IS NULL THEN
            SELECT actor_id INTO recipient FROM openfox.task_activities WHERE organization_id=NEW.organization_id AND task_id=NEW.task_id AND kind='task.created' ORDER BY created_at LIMIT 1;
        ELSE
            SELECT author_user_id INTO recipient FROM openfox.task_comments WHERE organization_id=NEW.organization_id AND id=comment;
        END IF;
    END IF;
    IF recipient IS NULL OR actor=recipient THEN RETURN NULL; END IF;
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,task_id,comment_id,dedupe_key,safe_summary,actor_user_id,inbox_event)
    SELECT NEW.organization_id,recipient,CASE WHEN TG_TABLE_NAME='task_followers' THEN 'TASK_UPDATED' ELSE 'TASK_REACTION' END,
           NEW.task_id,comment,leaf||':'||NEW.id,label,actor,leaf
     WHERE EXISTS(SELECT 1 FROM openfox_private.task_notice_audience(NEW.organization_id,NEW.task_id,recipient) a WHERE a.user_id=recipient)
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION openfox_private.task_social_notices() FROM PUBLIC;
CREATE TRIGGER follower_notice AFTER INSERT ON openfox.task_followers FOR EACH ROW
 WHEN (NEW.user_id IS NOT NULL AND NEW.user_id <> NEW.created_by) EXECUTE FUNCTION openfox_private.task_social_notices();
CREATE TRIGGER reaction_notice AFTER INSERT ON openfox.task_reactions FOR EACH ROW EXECUTE FUNCTION openfox_private.task_social_notices();

CREATE OR REPLACE FUNCTION openfox_private.notify_automation_result() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
DECLARE source uuid; result text; conversation uuid;
BEGIN
    IF TG_TABLE_NAME='automation_runs' THEN
        source:=NEW.automation_id; result:=NEW.status;
    ELSE
        SELECT ar.automation_id INTO source FROM openfox.conversation_inputs ci JOIN openfox.automation_runs ar
          ON ar.organization_id=ci.organization_id AND ar.message_id=ci.message_id
         WHERE ci.organization_id=NEW.organization_id AND ci.run_id=NEW.id LIMIT 1;
        IF source IS NULL THEN RETURN NULL; END IF;
        result:=CASE NEW.state WHEN 'SUCCEEDED' THEN 'completed' WHEN 'FAILED' THEN 'failed' ELSE 'cancelled' END;
        conversation:=NEW.conversation_id;
    END IF;
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,conversation_id,automation_id,dedupe_key,safe_summary,inbox_event)
    SELECT a.organization_id,m.user_id,'AUTOMATION_RESULT',conversation,a.id,'automation-run:'||NEW.id,
           left(a.title||' · '||result,512),'automation_run_'||result||'_recipient'
      FROM openfox.automations a JOIN openfox.spaces s ON s.organization_id=a.organization_id AND s.id=a.space_id AND s.deleted_at IS NULL
      JOIN openfox.organization_members m ON m.organization_id=a.organization_id AND m.user_id=ANY(a.notify_user_ids) AND m.status='ACTIVE'
      LEFT JOIN openfox.space_members sm ON sm.organization_id=m.organization_id AND sm.space_id=a.space_id AND sm.user_id=m.user_id
     WHERE a.organization_id=NEW.organization_id AND a.id=source
       AND (sm.status='ACTIVE' OR (s.visibility='SHARED' AND m.role<>'GUEST' AND sm.user_id IS NULL))
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
CREATE TRIGGER automation_attempt_notices AFTER INSERT ON openfox.automation_runs FOR EACH ROW
 WHEN (NEW.status IN ('skipped','failed')) EXECUTE FUNCTION openfox_private.notify_automation_result();

CREATE FUNCTION openfox_private.run_blocked_notice() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,openfox AS $$
BEGIN
    INSERT INTO openfox.notifications(organization_id,recipient_user_id,kind,conversation_id,task_id,dedupe_key,safe_summary)
    SELECT NEW.organization_id,m.user_id,'RUN_BLOCKED',NEW.conversation_id,NEW.task_id,'blocked:'||NEW.id||':'||NEW.wait_reason,'Agent execution needs your attention.'
      FROM openfox.organization_members m WHERE m.organization_id=NEW.organization_id AND m.user_id=NEW.actor_id AND m.status='ACTIVE'
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION openfox_private.run_blocked_notice() FROM PUBLIC;
CREATE TRIGGER run_blocked_notice AFTER UPDATE OF wait_reason ON openfox.runs FOR EACH ROW
 WHEN (NEW.state='QUEUED' AND OLD.wait_reason IS DISTINCT FROM NEW.wait_reason AND NEW.wait_reason IN ('COMPUTER_OFFLINE','TASK_GATE','RUNTIME_UNAVAILABLE'))
 EXECUTE FUNCTION openfox_private.run_blocked_notice();
RESET ROLE;
