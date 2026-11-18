-- Email notifications are opt-in. Only notifications inserted after this migration
-- can enter the outbox; enabling a channel never backfills older notifications.
-- Apply before deploying the worker. Roll back the application first, then drop
-- the trigger; email preference rows must be removed before restoring the old
-- channel CHECK constraint.
SET ROLE openfox_migrator;

ALTER TABLE openfox.user_notification_channels
    DROP CONSTRAINT user_notification_channels_channel_valid;
ALTER TABLE openfox.user_notification_channels
    ADD CONSTRAINT user_notification_channels_channel_valid
    CHECK (channel IN ('desktop', 'browser', 'feishu', 'email'));

CREATE TABLE openfox.email_notification_outbox (
    organization_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    notification_id uuid NOT NULL,
    recipient_user_id uuid NOT NULL,
    kind text NOT NULL,
    safe_summary text NOT NULL,
    status text NOT NULL DEFAULT 'PENDING',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT (clock_timestamp() + interval '7 days'),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (organization_id, id),
    FOREIGN KEY (organization_id, notification_id)
        REFERENCES openfox.notifications (organization_id, id),
    FOREIGN KEY (organization_id, recipient_user_id)
        REFERENCES openfox.organization_members (organization_id, user_id),
    CONSTRAINT email_notification_outbox_unique UNIQUE (organization_id, notification_id),
    CONSTRAINT email_notification_outbox_status_valid
        CHECK (status IN ('PENDING', 'SENDING', 'DONE', 'FAILED', 'SKIPPED')),
    CONSTRAINT email_notification_outbox_finished_valid
        CHECK ((status IN ('DONE', 'FAILED', 'SKIPPED')) = (finished_at IS NOT NULL)),
    CONSTRAINT email_notification_outbox_text_valid
        CHECK (attempts >= 0 AND char_length(lease_owner) <= 128
            AND char_length(last_error) <= 500 AND char_length(kind) BETWEEN 1 AND 64
            AND char_length(safe_summary) BETWEEN 1 AND 512)
);
CREATE INDEX email_notification_outbox_due_idx
    ON openfox.email_notification_outbox (organization_id, next_attempt_at, created_at)
    WHERE status IN ('PENDING', 'SENDING');
CREATE INDEX email_notification_outbox_finished_idx
    ON openfox.email_notification_outbox (organization_id, finished_at)
    WHERE finished_at IS NOT NULL;

ALTER TABLE openfox.email_notification_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.email_notification_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY email_notification_outbox_tenant_isolation
    ON openfox.email_notification_outbox
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());
CREATE POLICY email_notification_outbox_scan
    ON openfox.email_notification_outbox FOR SELECT TO openfox_migrator USING (true);

CREATE FUNCTION openfox_private.enqueue_email_notification()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private AS $$
BEGIN
    INSERT INTO openfox.email_notification_outbox (
        organization_id, notification_id, recipient_user_id, kind, safe_summary)
    SELECT n.organization_id, n.id, n.recipient_user_id, n.kind, n.safe_summary
      FROM changed_new n
      JOIN openfox.user_notification_channels ch
        ON ch.user_id = n.recipient_user_id AND ch.channel = 'email' AND ch.enabled
     WHERE n.kind <> 'DIRECT_MESSAGE' OR ch.direct_messages IS TRUE
    ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
ALTER FUNCTION openfox_private.enqueue_email_notification() OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.enqueue_email_notification() FROM PUBLIC;
CREATE TRIGGER notifications_enqueue_email_delivery
    AFTER INSERT ON openfox.notifications REFERENCING NEW TABLE AS changed_new
    FOR EACH STATEMENT EXECUTE FUNCTION openfox_private.enqueue_email_notification();

CREATE FUNCTION openfox_private.list_email_notification_organizations(p_after uuid, p_limit integer, p_delivery_available boolean)
RETURNS TABLE (organization_id uuid)
LANGUAGE plpgsql STABLE SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private AS $$
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 500 OR p_delivery_available IS NULL THEN
        RAISE EXCEPTION 'invalid email notification scan limit' USING ERRCODE = '22023';
    END IF;
    RETURN QUERY
    SELECT DISTINCT q.organization_id
      FROM openfox.email_notification_outbox q
      JOIN openfox.organization_directory d
        ON d.organization_id = q.organization_id AND d.deleted_at IS NULL
     WHERE ((p_delivery_available AND ((q.status = 'PENDING' AND q.next_attempt_at <= clock_timestamp())
        OR (q.status = 'SENDING' AND
             (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))))
        OR (q.status = 'PENDING' AND q.expires_at <= clock_timestamp())
        OR (q.status = 'SENDING' AND q.expires_at <= clock_timestamp()
            AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= clock_timestamp()))
        OR (q.finished_at < clock_timestamp() - interval '30 days'))
       AND (p_after IS NULL OR q.organization_id > p_after)
     ORDER BY q.organization_id LIMIT p_limit;
END $$;
ALTER FUNCTION openfox_private.list_email_notification_organizations(uuid, integer, boolean) OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.list_email_notification_organizations(uuid, integer, boolean) FROM PUBLIC;

RESET ROLE;
GRANT SELECT, INSERT, UPDATE, DELETE ON openfox.email_notification_outbox TO openfox_app;
GRANT EXECUTE ON FUNCTION openfox_private.list_email_notification_organizations(uuid, integer, boolean) TO openfox_context;
