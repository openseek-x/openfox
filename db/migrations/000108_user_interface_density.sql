SET ROLE openfox_migrator;

ALTER TABLE openfox.user_preferences
    ADD COLUMN density text NOT NULL DEFAULT 'comfortable',
    ADD CONSTRAINT user_preferences_density_valid
        CHECK (density IN ('comfortable', 'compact'));

RESET ROLE;
