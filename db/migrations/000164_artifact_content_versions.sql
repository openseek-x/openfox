-- Artifacts are immutable byte revisions; content_id groups explicit revisions.
-- New uploads are independent contents, even when names or hashes match.
-- The highest content_revision is the head (including unavailable revisions).
SET ROLE openfox_migrator;

ALTER TABLE openfox.artifacts
 ADD COLUMN content_id uuid NOT NULL DEFAULT gen_random_uuid(),
 ADD COLUMN content_revision bigint NOT NULL DEFAULT 1 CHECK (content_revision > 0),
 ADD COLUMN parent_artifact_id uuid,
 ADD CONSTRAINT artifact_content_revision_unique UNIQUE (organization_id, content_id, content_revision),
 ADD CONSTRAINT artifact_parent_unique UNIQUE (organization_id, parent_artifact_id),
 ADD CONSTRAINT artifact_parent_fk FOREIGN KEY (organization_id, parent_artifact_id)
 REFERENCES openfox.artifacts (organization_id, id);

-- Existing development artifacts receive independent identities. We do not
-- reconstruct a version history from filenames, hashes or old message links.
CREATE FUNCTION openfox_private.artifact_revision_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, openfox AS $$
DECLARE base openfox.artifacts; latest uuid;
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF ROW(NEW.organization_id,NEW.id,NEW.run_id,NEW.name,NEW.storage_key,NEW.size_bytes,NEW.sha256,NEW.mime_type,NEW.content_id,NEW.content_revision,NEW.parent_artifact_id,NEW.created_at)
     IS DISTINCT FROM
     ROW(OLD.organization_id,OLD.id,OLD.run_id,OLD.name,OLD.storage_key,OLD.size_bytes,OLD.sha256,OLD.mime_type,OLD.content_id,OLD.content_revision,OLD.parent_artifact_id,OLD.created_at) THEN
   RAISE EXCEPTION 'artifact bytes and revision identity are immutable' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
 END IF;
 IF NEW.parent_artifact_id IS NULL THEN
  NEW.content_revision := 1;
  RETURN NEW;
 END IF;
 SELECT organization_id,id,run_id,content_id,content_revision INTO base.organization_id,base.id,base.run_id,base.content_id,base.content_revision FROM openfox.artifacts
 WHERE organization_id=NEW.organization_id AND id=NEW.parent_artifact_id;
 IF NOT FOUND OR NOT EXISTS (
  SELECT 1 FROM openfox.runs source JOIN openfox.runs target
   ON target.organization_id=source.organization_id AND target.conversation_id=source.conversation_id
  WHERE source.organization_id=NEW.organization_id AND source.id=base.run_id AND target.id=NEW.run_id
 ) THEN
  RAISE EXCEPTION 'artifact revision source is not in this conversation' USING ERRCODE = '23514';
 END IF;
 -- Serialize publications for this content, across different runs/requests.
 PERFORM 1 FROM openfox.artifacts WHERE organization_id=NEW.organization_id
  AND content_id=base.content_id AND content_revision=1 FOR UPDATE;
 SELECT id INTO latest FROM openfox.artifacts WHERE organization_id=NEW.organization_id
  AND content_id=base.content_id ORDER BY content_revision DESC LIMIT 1;
 IF latest IS DISTINCT FROM base.id THEN
  RAISE EXCEPTION 'artifact revision base is no longer current' USING ERRCODE = '23505', CONSTRAINT = 'artifact_parent_unique';
 END IF;
 NEW.content_id := base.content_id;
 NEW.content_revision := base.content_revision + 1;
 RETURN NEW;
END;
$$;
CREATE TRIGGER artifact_revision_guard BEFORE INSERT OR UPDATE ON openfox.artifacts
 FOR EACH ROW EXECUTE FUNCTION openfox_private.artifact_revision_guard();
RESET ROLE;
