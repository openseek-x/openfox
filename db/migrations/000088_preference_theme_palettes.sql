SET ROLE openfox_migrator;
ALTER TABLE openfox.user_preferences
    DROP CONSTRAINT user_preferences_theme_valid,
    ADD CONSTRAINT user_preferences_theme_valid
        CHECK (theme IN ('system', 'light', 'dark', 'magic-blue', 'classic-dark'));
RESET ROLE;
