-- Remove the remote-mute capability from an already-initialised database.
--
-- How history is preserved
-- ------------------------
-- * device_commands rows of retired type 'set_mute' are kept intact. They are
--   part of the command audit trail and must stay readable and updatable
--   (publish / acknowledge / expire) after this migration. The original
--   device_commands.type CHECK constraint is therefore left alone: tightening
--   it to 'set_thresholds' would make PostgreSQL re-evaluate the CHECK on every
--   UPDATE and would break lifecycle transitions on historical mute rows.
-- * device_commands.muted keeps the payload of those retired rows. It is
--   marked DEPRECATED and is never written again.
-- * telemetry.buzzer_muted keeps historical sample values. It is marked
--   DEPRECATED, given a DEFAULT so that inserts that omit it keep working, and
--   is never written again (new rows store false).
-- * devices.buzzer_muted is live status, not history, so it is dropped.
--
-- Removal plan
-- ------------
-- Once the retention window for telemetry samples and device command history
-- has expired, a later forward-only migration may DROP COLUMN the deprecated
-- telemetry.buzzer_muted and device_commands.muted. Until then they stay so no
-- existing data is lost or rewritten.
--
-- Idempotency
-- -----------
-- Every statement is guarded so the script can be re-run safely: DROP COLUMN /
-- COMMENT are effectively no-ops when repeated, and the trigger is recreated
-- from scratch (DROP TRIGGER IF EXISTS + CREATE OR REPLACE FUNCTION).
-- Fresh databases created from migrations/0001_init.sql have none of the legacy
-- columns, so the ALTER blocks below are skipped and only the trigger is added
-- (harmless redundancy with the strict type CHECK in 0001_init.sql).

ALTER TABLE devices DROP COLUMN IF EXISTS buzzer_muted;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'telemetry'
          AND column_name = 'buzzer_muted'
    ) THEN
        -- Older installs wrote this column NOT NULL without a default. Give it
        -- one so the application can stop supplying it.
        ALTER TABLE telemetry ALTER COLUMN buzzer_muted SET DEFAULT false;
        COMMENT ON COLUMN telemetry.buzzer_muted IS
            'DEPRECATED read-only: remote mute removed. Historical samples keep their recorded value; new rows store false. Drop after history retention expires.';
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'device_commands'
          AND column_name = 'muted'
    ) THEN
        COMMENT ON COLUMN device_commands.muted IS
            'DEPRECATED read-only: payload of retired set_mute commands. Drop after command-history retention expires.';
    END IF;
END $$;

-- Block new set_mute rows while leaving historical ones fully mutable.
CREATE OR REPLACE FUNCTION device_commands_reject_set_mute() RETURNS trigger AS $$
BEGIN
    IF NEW.type = 'set_mute' THEN
        RAISE EXCEPTION 'set_mute commands are no longer supported';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS device_commands_no_new_set_mute ON device_commands;
CREATE TRIGGER device_commands_no_new_set_mute
    BEFORE INSERT ON device_commands
    FOR EACH ROW EXECUTE FUNCTION device_commands_reject_set_mute();
