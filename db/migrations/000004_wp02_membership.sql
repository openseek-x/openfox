-- WP-02 membership management: audit records, stable listing indexes, and
-- database-side invariants for member identity and authorization versions.

SET ROLE openfox_migrator;

CREATE TABLE openfox.audit_events (
    id uuid PRIMARY KEY,
    organization_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    action text NOT NULL,
    decision text NOT NULL,
    reason text,
    resource_kind text NOT NULL,
    resource_id text NOT NULL,
    request_id text NOT NULL,
    authorization_version bigint NOT NULL,
    safe_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (organization_id) REFERENCES openfox.organizations (id),
    FOREIGN KEY (actor_id) REFERENCES openfox.users (id),
    CONSTRAINT audit_events_action_valid CHECK (action ~ '^[A-Za-z0-9_.-]{1,128}$'),
    CONSTRAINT audit_events_decision_valid CHECK (decision ~ '^[A-Za-z0-9_.-]{1,64}$'),
    CONSTRAINT audit_events_reason_valid CHECK (reason IS NULL OR reason ~ '^[A-Za-z0-9_.-]{1,128}$'),
    CONSTRAINT audit_events_resource_kind_valid CHECK (resource_kind ~ '^[A-Za-z0-9_.-]{1,64}$'),
    CONSTRAINT audit_events_resource_id_not_blank CHECK (length(btrim(resource_id)) > 0),
    CONSTRAINT audit_events_request_id_not_blank CHECK (length(btrim(request_id)) > 0),
    CONSTRAINT audit_events_authorization_version_positive CHECK (authorization_version > 0),
    CONSTRAINT audit_events_metadata_object CHECK (jsonb_typeof(safe_metadata) = 'object')
);

CREATE INDEX organization_members_listing_idx
    ON openfox.organization_members (organization_id, created_at, user_id);

CREATE INDEX organization_members_active_owner_idx
    ON openfox.organization_members (organization_id, user_id)
    WHERE status = 'ACTIVE' AND role = 'OWNER';

CREATE INDEX audit_events_organization_created_idx
    ON openfox.audit_events (organization_id, created_at, id);

-- Member changes are serialized by the organization row. This protects the
-- last-owner invariant across concurrent transactions, while the trigger also
-- protects direct application-role updates that bypass the Go service.
CREATE OR REPLACE FUNCTION openfox_private.guard_organization_membership_change()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, openfox, openfox_private
AS $$
DECLARE
    active_owners bigint;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.organization_id <> OLD.organization_id OR NEW.user_id <> OLD.user_id THEN
            RAISE EXCEPTION 'membership identity is immutable'
                USING ERRCODE = '23514', CONSTRAINT = 'organization_members_identity_immutable';
        END IF;

        IF NEW.role IS DISTINCT FROM OLD.role OR NEW.status IS DISTINCT FROM OLD.status THEN
            PERFORM id
              FROM openfox.organizations
             WHERE id = OLD.organization_id
             FOR UPDATE;

            NEW.authorization_version := OLD.authorization_version + 1;

            IF OLD.role = 'OWNER' AND OLD.status = 'ACTIVE'
               AND (NEW.role <> 'OWNER' OR NEW.status <> 'ACTIVE') THEN
                SELECT count(*)
                  INTO active_owners
                  FROM openfox.organization_members
                 WHERE organization_id = OLD.organization_id
                   AND user_id <> OLD.user_id
                   AND role = 'OWNER'
                   AND status = 'ACTIVE';
                IF active_owners = 0 THEN
                    RAISE EXCEPTION 'organization requires an active owner'
                        USING ERRCODE = '23514', CONSTRAINT = 'organization_members_owner_required';
                END IF;
            END IF;
        ELSE
            NEW.authorization_version := OLD.authorization_version;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_OP = 'DELETE' THEN
        IF OLD.role = 'OWNER' AND OLD.status = 'ACTIVE' THEN
            PERFORM id
              FROM openfox.organizations
             WHERE id = OLD.organization_id
             FOR UPDATE;

            SELECT count(*)
              INTO active_owners
              FROM openfox.organization_members
             WHERE organization_id = OLD.organization_id
               AND user_id <> OLD.user_id
               AND role = 'OWNER'
               AND status = 'ACTIVE';
            IF active_owners = 0 THEN
                RAISE EXCEPTION 'organization requires an active owner'
                    USING ERRCODE = '23514', CONSTRAINT = 'organization_members_owner_required';
            END IF;
        END IF;
        RETURN OLD;
    END IF;

    RETURN NEW;
END
$$;

ALTER FUNCTION openfox_private.guard_organization_membership_change()
    OWNER TO openfox_migrator;
REVOKE ALL ON FUNCTION openfox_private.guard_organization_membership_change() FROM PUBLIC;

DROP TRIGGER IF EXISTS organization_members_change_guard ON openfox.organization_members;
CREATE TRIGGER organization_members_change_guard
BEFORE UPDATE OR DELETE ON openfox.organization_members
FOR EACH ROW EXECUTE FUNCTION openfox_private.guard_organization_membership_change();

GRANT SELECT, INSERT ON openfox.audit_events TO openfox_app;

ALTER TABLE openfox.audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.audit_events FORCE ROW LEVEL SECURITY;

CREATE POLICY audit_events_tenant_isolation ON openfox.audit_events
    USING (organization_id = openfox_private.current_organization_id())
    WITH CHECK (organization_id = openfox_private.current_organization_id());

RESET ROLE;
