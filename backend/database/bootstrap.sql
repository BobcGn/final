-- Lab monitoring database bootstrap for the existing postgres-dev container.
-- Run with: docker exec -i postgres-dev psql -U postgres -d postgres -v ON_ERROR_STOP=1 < backend/database/bootstrap.sql
-- This script uses psql meta-commands, so it is not executed by the Go migration runner.
-- It creates only the dedicated lab database and never alters other databases in postgres-dev.

SELECT 'CREATE DATABASE lab'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'lab')
\gexec

\connect lab

-- Schema for the lab environment monitoring backend (SHIXUN-4).
--
-- Units are encoded in the column names: *_c is degrees Celsius, *_rh is percent
-- relative humidity, gas_adc_* is a 12-bit ADC code in 0..4095, and gas_ppm is
-- an uncalibrated estimate unless gas_calibrated is true.
--
-- Time is stored in two columns per sample: event_time is the device event time
-- when the device clock is synced, and received_at is always the server time.
-- event_time falls back to received_at when the device sends a null timestamp,
-- which keeps every ordering and cursor key non-null.

CREATE TABLE IF NOT EXISTS devices (
    device_id                   text PRIMARY KEY,
    last_seen_at                timestamptz,
    last_boot_id                text NOT NULL DEFAULT '',
    last_sequence               bigint NOT NULL DEFAULT 0,
    connectivity                text NOT NULL DEFAULT 'unknown'
        CHECK (connectivity IN ('online', 'offline', 'unknown')),
    alarm_state                 text NOT NULL DEFAULT 'normal'
        CHECK (alarm_state IN ('normal', 'suspect', 'fire_warning', 'recovered')),
    active_alert_id             text,
    local_alarm                 boolean NOT NULL DEFAULT false,
    sensor_fault                boolean NOT NULL DEFAULT false,
    gas_calibrated              boolean NOT NULL DEFAULT false,
    threshold_version_confirmed integer NOT NULL DEFAULT 0,
    threshold_version_desired   integer NOT NULL DEFAULT 1,
    updated_at                  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS telemetry (
    id                bigserial PRIMARY KEY,
    device_id         text NOT NULL,
    boot_id           text NOT NULL,
    sequence          bigint NOT NULL CHECK (sequence >= 0),
    event_time        timestamptz NOT NULL,
    received_at       timestamptz NOT NULL,
    event_time_source text NOT NULL CHECK (event_time_source IN ('device', 'receivedAt')),
    device_timestamp  timestamptz,
    uptime_ms         bigint NOT NULL CHECK (uptime_ms >= 0),
    temperature_c     double precision NOT NULL,
    humidity_rh       double precision NOT NULL,
    gas_adc_raw       integer NOT NULL CHECK (gas_adc_raw BETWEEN 0 AND 4095),
    gas_adc_filtered  integer NOT NULL CHECK (gas_adc_filtered BETWEEN 0 AND 4095),
    gas_ppm           double precision,
    gas_calibrated    boolean NOT NULL,
    local_alarm       boolean NOT NULL,
    alarm_causes      text[] NOT NULL DEFAULT '{}',
    network           text NOT NULL CHECK (network IN ('online', 'reconnecting')),
    threshold_version integer NOT NULL CHECK (threshold_version >= 1),
    sensor_fault      boolean NOT NULL,
    -- The dedup key. sequence alone resets on every device power cycle, so the
    -- uniqueness constraint must include boot_id or a restart would silently
    -- discard legitimate new samples as duplicates.
    CONSTRAINT telemetry_dedup_key UNIQUE (device_id, boot_id, sequence)
);

-- Serves both the history query and the keyset cursor, in either direction.
CREATE INDEX IF NOT EXISTS telemetry_series_idx
    ON telemetry (device_id, event_time, boot_id, sequence);

CREATE TABLE IF NOT EXISTS alert_events (
    id                  text PRIMARY KEY,
    device_id           text NOT NULL,
    state               text NOT NULL
        CHECK (state IN ('normal', 'suspect', 'fire_warning', 'recovered')),
    started_at          timestamptz NOT NULL,
    ended_at            timestamptz,
    gas_adc_rise        integer NOT NULL,
    gas_adc_rise_threshold integer NOT NULL,
    temperature_rate_c_per_minute double precision NOT NULL,
    temperature_rate_threshold_c_per_minute double precision NOT NULL,
    sample_count        integer NOT NULL CHECK (sample_count >= 1),
    window_seconds      integer NOT NULL CHECK (window_seconds >= 1),
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE INDEX IF NOT EXISTS alert_events_series_idx
    ON alert_events (device_id, started_at, id);

-- At most one open alert per device: a second concurrent episode would make
-- ActiveAlert ambiguous and let two evidence trails describe one condition.
CREATE UNIQUE INDEX IF NOT EXISTS alert_events_one_active_idx
    ON alert_events (device_id) WHERE ended_at IS NULL;

CREATE TABLE IF NOT EXISTS device_thresholds (
    device_id         text PRIMARY KEY,
    desired_version   integer NOT NULL CHECK (desired_version >= 1),
    temperature_high_c double precision NOT NULL
        CHECK (temperature_high_c BETWEEN 0 AND 80),
    humidity_high_rh  double precision NOT NULL
        CHECK (humidity_high_rh BETWEEN 0 AND 100),
    gas_high_ppm      double precision NOT NULL
        CHECK (gas_high_ppm BETWEEN 1 AND 999),
    confirmed_version integer CHECK (confirmed_version IS NULL OR confirmed_version >= 1),
    confirmation_state text NOT NULL DEFAULT 'pending'
        CHECK (confirmation_state IN ('confirmed', 'pending', 'rejected', 'timed_out')),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS device_commands (
    request_id        text PRIMARY KEY,
    device_id         text NOT NULL,
    type              text NOT NULL CHECK (type = 'set_thresholds'),
    state             text NOT NULL CHECK (state IN (
        'accepted', 'published', 'applied', 'rejected', 'expired',
        'duplicate', 'failed', 'timed_out', 'publish_failed')),
    idempotency_key   text NOT NULL DEFAULT '',
    actor             text NOT NULL DEFAULT '',
    accepted_at       timestamptz NOT NULL,
    published_at      timestamptz,
    completed_at      timestamptz,
    expires_at        timestamptz NOT NULL,
    temperature_high_c double precision,
    humidity_high_rh  double precision,
    gas_high_ppm      double precision,
    desired_version   integer CHECK (desired_version IS NULL OR desired_version >= 1),
    confirmed_version integer CHECK (confirmed_version IS NULL OR confirmed_version >= 1),
    error_code        text NOT NULL DEFAULT '',
    -- Exactly one payload shape per command type, so a malformed command cannot
    -- be stored and later published as a confusing mixture.
    CHECK (
        temperature_high_c IS NOT NULL
        AND humidity_high_rh IS NOT NULL AND gas_high_ppm IS NOT NULL
        AND desired_version IS NOT NULL
    )
);

CREATE INDEX IF NOT EXISTS device_commands_device_idx
    ON device_commands (device_id, accepted_at DESC);

-- Enforces idempotency per device without blocking commands that carry no key.
CREATE UNIQUE INDEX IF NOT EXISTS device_commands_idempotency_idx
    ON device_commands (device_id, idempotency_key) WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS device_commands_pending_idx
    ON device_commands (expires_at)
    WHERE state IN ('accepted', 'published');
