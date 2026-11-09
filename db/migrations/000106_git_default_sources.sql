SET ROLE openfox_migrator;

ALTER TABLE openfox.organization_git_settings
    ADD COLUMN author_name_mode text NOT NULL DEFAULT 'USER_AND_AGENT',
    ADD COLUMN author_email_mode text NOT NULL DEFAULT 'USER';
ALTER TABLE openfox.space_git_settings
    ADD COLUMN author_name_mode text NOT NULL DEFAULT 'INHERIT',
    ADD COLUMN author_email_mode text NOT NULL DEFAULT 'INHERIT';

CREATE POLICY organization_git_settings_source_read ON openfox.organization_git_settings
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY organization_git_settings_source_update ON openfox.organization_git_settings
    FOR UPDATE TO openfox_migrator USING (true) WITH CHECK (true);
CREATE POLICY space_git_settings_source_read ON openfox.space_git_settings
    FOR SELECT TO openfox_migrator USING (true);
CREATE POLICY space_git_settings_source_update ON openfox.space_git_settings
    FOR UPDATE TO openfox_migrator USING (true) WITH CHECK (true);

UPDATE openfox.organization_git_settings SET
    author_name_mode = CASE WHEN author_name <> '' THEN 'MANUAL' ELSE 'USER_AND_AGENT' END,
    author_email_mode = CASE WHEN author_email <> '' THEN 'MANUAL' ELSE 'USER' END
WHERE author_name <> '' OR author_email <> '';
UPDATE openfox.space_git_settings SET
    author_name_mode = CASE WHEN author_name <> '' THEN 'MANUAL' ELSE 'INHERIT' END,
    author_email_mode = CASE WHEN author_email <> '' THEN 'MANUAL' ELSE 'INHERIT' END
WHERE author_name <> '' OR author_email <> '';

DROP POLICY organization_git_settings_source_read ON openfox.organization_git_settings;
DROP POLICY organization_git_settings_source_update ON openfox.organization_git_settings;
DROP POLICY space_git_settings_source_read ON openfox.space_git_settings;
DROP POLICY space_git_settings_source_update ON openfox.space_git_settings;

ALTER TABLE openfox.organization_git_settings
    ADD CONSTRAINT organization_git_settings_name_mode_valid CHECK (
        author_name_mode IN ('USER_AND_AGENT','USER','OWNER_AND_AGENT','OWNER','MACHINE','MANUAL')
        AND (author_name_mode <> 'MANUAL' OR btrim(author_name) <> '')
    ),
    ADD CONSTRAINT organization_git_settings_email_mode_valid CHECK (
        author_email_mode IN ('USER','OWNER','MACHINE','MANUAL')
        AND (author_email_mode <> 'MANUAL' OR author_email <> '')
    );
ALTER TABLE openfox.space_git_settings
    ADD CONSTRAINT space_git_settings_name_mode_valid CHECK (
        author_name_mode IN ('INHERIT','USER_AND_AGENT','USER','OWNER_AND_AGENT','OWNER','MACHINE','MANUAL')
        AND (author_name_mode <> 'MANUAL' OR btrim(author_name) <> '')
    ),
    ADD CONSTRAINT space_git_settings_email_mode_valid CHECK (
        author_email_mode IN ('INHERIT','USER','OWNER','MACHINE','MANUAL')
        AND (author_email_mode <> 'MANUAL' OR author_email <> '')
    );
