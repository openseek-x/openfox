-- Document history: additive; snapshots follow the current node's RLS and
-- survive moves. No bulk backfill: preserve legacy content on its first edit.
SET ROLE openfox_migrator;

CREATE TABLE openfox.doc_revisions (
    organization_id uuid NOT NULL,
    node_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    node_version bigint NOT NULL CHECK (node_version > 0),
    title text NOT NULL,
    body text NOT NULL CHECK (octet_length(body) <= 2097152),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, node_id, revision),
    UNIQUE (organization_id, node_id, node_version),
    FOREIGN KEY (organization_id, node_id) REFERENCES openfox.doc_nodes (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, created_by) REFERENCES openfox.organization_members (organization_id, user_id)
);
ALTER TABLE openfox.doc_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE openfox.doc_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY doc_revisions_visibility ON openfox.doc_revisions
    USING (organization_id = openfox_private.current_organization_id()
        AND EXISTS (SELECT 1 FROM openfox.doc_nodes n
                    WHERE n.organization_id = doc_revisions.organization_id AND n.id = doc_revisions.node_id))
    WITH CHECK (organization_id = openfox_private.current_organization_id()
        AND EXISTS (SELECT 1 FROM openfox.doc_nodes n
                    WHERE n.organization_id = doc_revisions.organization_id AND n.id = doc_revisions.node_id));

-- The node write already holds its row lock, serializing revision allocation.
CREATE FUNCTION openfox_private.snapshot_doc_revision() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, openfox, openfox_private AS $$
DECLARE last_revision bigint;
BEGIN
    IF NEW.kind NOT IN ('doc', 'sheet') THEN RETURN NEW; END IF;
    IF TG_OP = 'UPDATE' AND NEW.title = OLD.title AND NEW.body = OLD.body THEN RETURN NEW; END IF;
    SELECT COALESCE(MAX(revision), 0) INTO last_revision FROM openfox.doc_revisions
      WHERE organization_id = NEW.organization_id AND node_id = NEW.id;
    IF TG_OP = 'UPDATE' AND last_revision = 0 AND (OLD.title <> '' OR OLD.body <> '') THEN
        INSERT INTO openfox.doc_revisions VALUES
          (OLD.organization_id, OLD.id, 1, OLD.version, OLD.title, OLD.body, OLD.updated_by, OLD.updated_at);
        last_revision := 1;
    END IF;
    IF TG_OP = 'INSERT' AND NEW.title = '' AND NEW.body = '' THEN RETURN NEW; END IF;
    INSERT INTO openfox.doc_revisions VALUES
      (NEW.organization_id, NEW.id, last_revision + 1, NEW.version, NEW.title, NEW.body, NEW.updated_by, NEW.updated_at);
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION openfox_private.snapshot_doc_revision() FROM PUBLIC;
CREATE TRIGGER doc_revision_snapshot AFTER INSERT OR UPDATE ON openfox.doc_nodes
FOR EACH ROW EXECUTE FUNCTION openfox_private.snapshot_doc_revision();
RESET ROLE;
GRANT SELECT, INSERT ON openfox.doc_revisions TO openfox_app;
