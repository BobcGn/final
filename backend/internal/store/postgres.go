package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BobcGn/final/backend/internal/domain"
)

// migrationsFS holds the schema. Embedding it keeps the migrations inside the
// binary, so a deployment cannot start against a schema that shipped separately
// from the code that expects it.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Postgres is the production Store implementation.
//
// Every method that writes more than one row runs inside a transaction, because
// the caller's correctness arguments assume atomicity: a sample that was stored
// while the device cache was not updated would make the next restart report a
// stale sequence, and an alert closed without clearing the active pointer would
// leave a device permanently "alarming".
type Postgres struct {
	pool *pgxpool.Pool
}

// compile-time assertion that Postgres satisfies Store.
var _ Store = (*Postgres)(nil)

// OpenPostgres connects to a PostgreSQL database, applies every pending
// migration and returns the store.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	store := &Postgres{pool: pool}
	if err := store.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

// Close releases every pooled connection.
func (p *Postgres) Close() { p.pool.Close() }

// Ping verifies the connection is usable.
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Exec runs a single administrative statement.
//
// It is deliberately not part of the Store interface: business code must go
// through the typed methods, where the atomicity rules live. Exec exists for
// schema management in tests and for operator runbooks, where the alternative
// would be a separate database client.
func (p *Postgres) Exec(ctx context.Context, statement string) error {
	if _, err := p.pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("store: exec administrative statement: %w", err)
	}
	return nil
}

// Migrate applies the embedded schema. The statements are idempotent, so running
// them on every start is safe and keeps a fresh deployment from needing a
// separate migration step.
//
// This is deliberately a forward-only runner over CREATE IF NOT EXISTS rather
// than a migration framework: phase 1 has one schema version, and adding a
// framework before there is a second version would be speculative. Introducing
// one later is a contained change.
func (p *Postgres) Migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("store: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		script, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read migration %s: %w", name, err)
		}
		if _, err := p.pool.Exec(ctx, string(script)); err != nil {
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
	}
	return nil
}

// InsertTelemetry implements Store with an ON CONFLICT DO NOTHING insert keyed
// by the dedup constraint.
func (p *Postgres) InsertTelemetry(ctx context.Context, sample domain.Telemetry) (bool, error) {
	if err := sample.Validate(); err != nil {
		return false, err
	}
	causes := make([]string, 0, len(sample.AlarmCauses))
	for _, cause := range sample.AlarmCauses {
		causes = append(causes, string(cause))
	}

	tag, err := p.pool.Exec(ctx, `
		INSERT INTO telemetry (
			device_id, boot_id, sequence, event_time, received_at, event_time_source,
			device_timestamp, uptime_ms, temperature_c, humidity_rh, gas_adc_raw,
			gas_adc_filtered, gas_ppm, gas_calibrated, local_alarm, alarm_causes,
			network, threshold_version, sensor_fault)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		ON CONFLICT (device_id, boot_id, sequence) DO NOTHING`,
		sample.DeviceID, sample.BootID, int64(sample.Sequence), sample.EventTime(), sample.ReceivedAt,
		sample.EventTimeSource(), sample.Timestamp, int64(sample.UptimeMs), sample.TemperatureC,
		sample.HumidityRh, sample.GasAdcRaw, sample.GasAdcFiltered, sample.GasPpm,
		sample.GasCalibrated, sample.LocalAlarm, causes, string(sample.Network),
		sample.ThresholdVersion, sample.SensorFault)
	if err != nil {
		return false, fmt.Errorf("store: insert telemetry: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// telemetryColumns is the projection used by every telemetry read, kept in one
// place so that scan order cannot drift from the query.
const telemetryColumns = `device_id, boot_id, sequence, device_timestamp, received_at,
	uptime_ms, temperature_c, humidity_rh, gas_adc_raw, gas_adc_filtered, gas_ppm,
	gas_calibrated, local_alarm, alarm_causes, network,
	threshold_version, sensor_fault`

// LatestTelemetry implements Store.
func (p *Postgres) LatestTelemetry(ctx context.Context, deviceID string) (domain.Telemetry, error) {
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return domain.Telemetry{}, err
	}
	row := p.pool.QueryRow(ctx, `SELECT `+telemetryColumns+` FROM telemetry
		WHERE device_id = $1
		ORDER BY event_time DESC, boot_id DESC, sequence DESC LIMIT 1`, deviceID)

	sample, err := scanTelemetry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Telemetry{}, fmt.Errorf("%w: no telemetry for device %s", ErrNotFound, deviceID)
	}
	if err != nil {
		return domain.Telemetry{}, fmt.Errorf("store: latest telemetry: %w", err)
	}
	return sample, nil
}

// ListTelemetry implements Store using keyset pagination over the frozen sort
// key (event_time, boot_id, sequence).
func (p *Postgres) ListTelemetry(ctx context.Context, query TelemetryQuery) (TelemetryPage, error) {
	limit := NormalizeLimit(query.Limit)
	order := NormalizeOrder(query.Order)
	if err := query.Range.Validate(); err != nil {
		return TelemetryPage{}, err
	}

	args := []any{query.DeviceID, query.Range.From, query.Range.To}
	filter := `device_id = $1 AND event_time >= $2 AND event_time < $3`

	if query.Cursor != "" {
		cursor, err := decodeCursor(query.Cursor, queryScope(query.DeviceID, query.Range, order))
		if err != nil {
			return TelemetryPage{}, err
		}
		comparison := ">"
		if order == OrderDesc {
			comparison = "<"
		}
		// Row comparison over the whole sort key is what makes pagination stable
		// when many samples share a timestamp.
		filter += fmt.Sprintf(` AND (event_time, boot_id, sequence) %s ($4, $5, $6)`, comparison)
		args = append(args, cursor.cursorTime(), cursor.BootID, int64(cursor.Sequence))
	}
	direction := "ASC"
	if order == OrderDesc {
		direction = "DESC"
	}
	// One extra row decides whether a next page exists without a second query.
	args = append(args, limit+1)

	sql := fmt.Sprintf(`SELECT `+telemetryColumns+` FROM telemetry
		WHERE %s
		ORDER BY event_time %s, boot_id %s, sequence %s
		LIMIT $%d`, filter, direction, direction, direction, len(args))

	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return TelemetryPage{}, fmt.Errorf("store: list telemetry: %w", err)
	}
	defer rows.Close()

	page := TelemetryPage{Items: make([]domain.Telemetry, 0, limit)}
	for rows.Next() {
		sample, err := scanTelemetry(rows)
		if err != nil {
			return TelemetryPage{}, fmt.Errorf("store: scan telemetry: %w", err)
		}
		page.Items = append(page.Items, sample)
	}
	if err := rows.Err(); err != nil {
		return TelemetryPage{}, fmt.Errorf("store: read telemetry rows: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(cursorPayload{
			EventTimeMillis: last.EventTime().UnixMilli(),
			BootID:          last.BootID,
			Sequence:        last.Sequence,
			Scope:           queryScope(query.DeviceID, query.Range, order),
		})
	}
	return page, nil
}

// InsertAlert implements Store.
func (p *Postgres) InsertAlert(ctx context.Context, event domain.AlertEvent) (domain.AlertEvent, error) {
	if event.ID == "" {
		event.ID = NewID(event.StartedAt)
	}
	if err := event.Validate(); err != nil {
		return domain.AlertEvent{}, err
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO alert_events (
			id, device_id, state, started_at, ended_at, gas_adc_rise, gas_adc_rise_threshold,
			temperature_rate_c_per_minute, temperature_rate_threshold_c_per_minute,
			sample_count, window_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		event.ID, event.DeviceID, string(event.State), event.StartedAt, event.EndedAt,
		event.Evidence.GasAdcRise, event.Evidence.GasAdcRiseThreshold,
		event.Evidence.TemperatureRateCPerMinute, event.Evidence.TemperatureRateThresholdCPerMinute,
		event.Evidence.SampleCount, event.Evidence.WindowSeconds)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.AlertEvent{}, fmt.Errorf("%w: only one alert may be open per device", ErrConflict)
		}
		return domain.AlertEvent{}, fmt.Errorf("store: insert alert: %w", err)
	}
	return event, nil
}

// UpdateAlert implements Store.
func (p *Postgres) UpdateAlert(ctx context.Context, deviceID, alertID string, state domain.AlertState, evidence domain.AlertEvidence) (domain.AlertEvent, error) {
	if !state.Valid() {
		return domain.AlertEvent{}, fmt.Errorf("%w: unknown state %q", domain.ErrInvalidAlert, state)
	}
	row := p.pool.QueryRow(ctx, `
		UPDATE alert_events SET
			state = $3,
			gas_adc_rise = $4,
			gas_adc_rise_threshold = $5,
			temperature_rate_c_per_minute = $6,
			temperature_rate_threshold_c_per_minute = $7,
			sample_count = $8,
			window_seconds = $9
		WHERE device_id = $1 AND id = $2 AND ended_at IS NULL
		RETURNING id, device_id, state, started_at, ended_at, gas_adc_rise, gas_adc_rise_threshold,
			temperature_rate_c_per_minute, temperature_rate_threshold_c_per_minute,
			sample_count, window_seconds`,
		deviceID, alertID, string(state), evidence.GasAdcRise, evidence.GasAdcRiseThreshold,
		evidence.TemperatureRateCPerMinute, evidence.TemperatureRateThresholdCPerMinute,
		evidence.SampleCount, evidence.WindowSeconds)

	event, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the alert does not exist, or it already ended. Telling the two
		// apart matters to the caller, so the existence is checked separately.
		var ended *time.Time
		lookupErr := p.pool.QueryRow(ctx, `SELECT ended_at FROM alert_events WHERE device_id = $1 AND id = $2`, deviceID, alertID).Scan(&ended)
		if lookupErr == nil {
			return domain.AlertEvent{}, fmt.Errorf("%w: alert %s already ended", ErrConflict, alertID)
		}
		return domain.AlertEvent{}, fmt.Errorf("%w: alert %s", ErrNotFound, alertID)
	}
	if err != nil {
		return domain.AlertEvent{}, fmt.Errorf("store: update alert: %w", err)
	}
	return event, nil
}

// CloseAlert implements Store.
func (p *Postgres) CloseAlert(ctx context.Context, deviceID, alertID string, state domain.AlertState, endedAt time.Time) error {
	if !state.Valid() {
		return fmt.Errorf("%w: unknown state %q", domain.ErrInvalidAlert, state)
	}
	tag, err := p.pool.Exec(ctx, `
		UPDATE alert_events SET state = $3, ended_at = $4
		WHERE device_id = $1 AND id = $2 AND ended_at IS NULL`,
		deviceID, alertID, string(state), endedAt)
	if err != nil {
		return fmt.Errorf("store: close alert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Closing an already closed alert is idempotent, so the caller's retried
		// recovery must not fail. Only a genuinely missing alert is an error.
		var exists bool
		if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alert_events WHERE device_id = $1 AND id = $2)`, deviceID, alertID).Scan(&exists); err != nil {
			return fmt.Errorf("store: verify alert existence: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: alert %s", ErrNotFound, alertID)
		}
	}
	return nil
}

// alertColumns is the projection used by every alert read.
const alertColumns = `id, device_id, state, started_at, ended_at, gas_adc_rise,
	gas_adc_rise_threshold, temperature_rate_c_per_minute,
	temperature_rate_threshold_c_per_minute, sample_count, window_seconds`

// ActiveAlert implements Store.
func (p *Postgres) ActiveAlert(ctx context.Context, deviceID string) (domain.AlertEvent, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+alertColumns+` FROM alert_events
		WHERE device_id = $1 AND ended_at IS NULL
		ORDER BY started_at DESC LIMIT 1`, deviceID)

	event, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AlertEvent{}, fmt.Errorf("%w: no active alert for device %s", ErrNotFound, deviceID)
	}
	if err != nil {
		return domain.AlertEvent{}, fmt.Errorf("store: active alert: %w", err)
	}
	return event, nil
}

// ListAlerts implements Store.
func (p *Postgres) ListAlerts(ctx context.Context, query AlertQuery) (AlertPage, error) {
	limit := NormalizeLimit(query.Limit)
	order := NormalizeOrder(query.Order)
	if err := query.Range.Validate(); err != nil {
		return AlertPage{}, err
	}

	args := []any{query.DeviceID, query.Range.From, query.Range.To}
	filter := `device_id = $1 AND started_at >= $2 AND started_at < $3`
	if query.State != "" {
		args = append(args, string(query.State))
		filter += fmt.Sprintf(` AND state = $%d`, len(args))
	}
	if query.ActiveOnly {
		filter += ` AND ended_at IS NULL`
	}
	if query.Cursor != "" {
		cursor, err := decodeCursor(query.Cursor, queryScope(query.DeviceID, query.Range, order))
		if err != nil {
			return AlertPage{}, err
		}
		comparison := ">"
		if order == OrderDesc {
			comparison = "<"
		}
		args = append(args, cursor.cursorTime(), cursor.ID)
		filter += fmt.Sprintf(` AND (started_at, id) %s ($%d, $%d)`, comparison, len(args)-1, len(args))
	}
	direction := "ASC"
	if order == OrderDesc {
		direction = "DESC"
	}
	args = append(args, limit+1)

	sql := fmt.Sprintf(`SELECT `+alertColumns+` FROM alert_events
		WHERE %s
		ORDER BY started_at %s, id %s
		LIMIT $%d`, filter, direction, direction, len(args))

	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return AlertPage{}, fmt.Errorf("store: list alerts: %w", err)
	}
	defer rows.Close()

	page := AlertPage{Items: make([]domain.AlertEvent, 0, limit)}
	for rows.Next() {
		event, err := scanAlert(rows)
		if err != nil {
			return AlertPage{}, fmt.Errorf("store: scan alert: %w", err)
		}
		page.Items = append(page.Items, event)
	}
	if err := rows.Err(); err != nil {
		return AlertPage{}, fmt.Errorf("store: read alert rows: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(cursorPayload{
			EventTimeMillis: last.StartedAt.UnixMilli(),
			ID:              last.ID,
			Scope:           queryScope(query.DeviceID, query.Range, order),
		})
	}
	return page, nil
}

// UpsertDevice implements Store.
func (p *Postgres) UpsertDevice(ctx context.Context, state DeviceState) error {
	if err := domain.ValidateDeviceID(state.DeviceID); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO devices (
			device_id, last_seen_at, last_boot_id, last_sequence, connectivity,
			alarm_state, active_alert_id, local_alarm, sensor_fault,
			gas_calibrated, threshold_version_confirmed, threshold_version_desired,
			updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (device_id) DO UPDATE SET
			last_seen_at = EXCLUDED.last_seen_at,
			last_boot_id = EXCLUDED.last_boot_id,
			last_sequence = EXCLUDED.last_sequence,
			connectivity = EXCLUDED.connectivity,
			alarm_state = EXCLUDED.alarm_state,
			active_alert_id = EXCLUDED.active_alert_id,
			local_alarm = EXCLUDED.local_alarm,
			sensor_fault = EXCLUDED.sensor_fault,
			gas_calibrated = EXCLUDED.gas_calibrated,
			threshold_version_confirmed = EXCLUDED.threshold_version_confirmed,
			threshold_version_desired = EXCLUDED.threshold_version_desired,
			updated_at = EXCLUDED.updated_at`,
		state.DeviceID, nullableTime(state.LastSeenAt), state.LastBootID, int64(state.LastSequence),
		string(state.Connectivity), string(state.AlarmState), nullableString(state.ActiveAlertID),
		state.LocalAlarm, state.SensorFault, state.GasCalibrated,
		state.ThresholdVersionConfirmed, state.ThresholdVersionDesired, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: upsert device: %w", err)
	}
	return nil
}

// Device implements Store.
func (p *Postgres) Device(ctx context.Context, deviceID string) (DeviceState, error) {
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return DeviceState{}, err
	}
	var (
		state         DeviceState
		lastSeenAt    *time.Time
		activeAlertID *string
	)
	err := p.pool.QueryRow(ctx, `
		SELECT device_id, last_seen_at, last_boot_id, last_sequence, connectivity,
			alarm_state, active_alert_id, local_alarm, sensor_fault,
			gas_calibrated, threshold_version_confirmed, threshold_version_desired, updated_at
		FROM devices WHERE device_id = $1`, deviceID).
		Scan(&state.DeviceID, &lastSeenAt, &state.LastBootID, &state.LastSequence, &state.Connectivity,
			&state.AlarmState, &activeAlertID, &state.LocalAlarm, &state.SensorFault,
			&state.GasCalibrated, &state.ThresholdVersionConfirmed, &state.ThresholdVersionDesired, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceState{}, fmt.Errorf("%w: device %s", ErrNotFound, deviceID)
	}
	if err != nil {
		return DeviceState{}, fmt.Errorf("store: read device: %w", err)
	}
	if lastSeenAt != nil {
		state.LastSeenAt = *lastSeenAt
	}
	if activeAlertID != nil {
		state.ActiveAlertID = *activeAlertID
	}
	return state, nil
}

// ListDevices implements Store.
func (p *Postgres) ListDevices(ctx context.Context) ([]DeviceState, error) {
	rows, err := p.pool.Query(ctx, `SELECT device_id FROM devices ORDER BY device_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan device id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read device rows: %w", err)
	}

	devices := make([]DeviceState, 0, len(ids))
	for _, id := range ids {
		state, err := p.Device(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		devices = append(devices, state)
	}
	return devices, nil
}

// Thresholds implements Store, falling back to the compile-time defaults for a
// device that has never been configured.
func (p *Postgres) Thresholds(ctx context.Context, deviceID string) (ThresholdRecord, error) {
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return ThresholdRecord{}, err
	}
	var (
		record           ThresholdRecord
		confirmedVersion *int
	)
	err := p.pool.QueryRow(ctx, `
		SELECT device_id, desired_version, temperature_high_c, humidity_high_rh,
			gas_high_ppm, confirmed_version, confirmation_state, updated_at
		FROM device_thresholds WHERE device_id = $1`, deviceID).
		Scan(&record.DeviceID, &record.DesiredVersion, &record.Desired.TemperatureHighC,
			&record.Desired.HumidityHighRh, &record.Desired.GasHighPpm, &confirmedVersion,
			&record.ConfirmationState, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ThresholdRecord{
			DeviceID:          deviceID,
			Desired:           domain.DefaultThresholds(),
			DesiredVersion:    domain.InitialThresholdVersion,
			ConfirmationState: ConfirmationPending,
		}, nil
	}
	if err != nil {
		return ThresholdRecord{}, fmt.Errorf("store: read thresholds: %w", err)
	}
	record.ConfirmedVersion = confirmedVersion
	return record, nil
}

// SetDesiredThresholds implements Store.
func (p *Postgres) SetDesiredThresholds(ctx context.Context, deviceID string, thresholds domain.Thresholds, version int, at time.Time) error {
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return err
	}
	if err := thresholds.Validate(); err != nil {
		return err
	}
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO device_thresholds (
			device_id, desired_version, temperature_high_c, humidity_high_rh, gas_high_ppm,
			confirmation_state, updated_at)
		VALUES ($1,$2,$3,$4,$5,'pending',$6)
		ON CONFLICT (device_id) DO UPDATE SET
			desired_version = EXCLUDED.desired_version,
			temperature_high_c = EXCLUDED.temperature_high_c,
			humidity_high_rh = EXCLUDED.humidity_high_rh,
			gas_high_ppm = EXCLUDED.gas_high_ppm,
			confirmation_state = 'pending',
			updated_at = EXCLUDED.updated_at
		WHERE device_thresholds.desired_version < EXCLUDED.desired_version`,
		deviceID, version, thresholds.TemperatureHighC, thresholds.HumidityHighRh,
		thresholds.GasHighPpm, at)
	if err != nil {
		return fmt.Errorf("store: set desired thresholds: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: threshold version %d is not greater than the stored version", ErrConflict, version)
	}
	return nil
}

// ConfirmThresholdVersion implements Store.
func (p *Postgres) ConfirmThresholdVersion(ctx context.Context, deviceID string, version int, at time.Time) error {
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return err
	}
	// The confirmation only ever moves forward, so an acknowledgement that
	// arrives after a newer one cannot roll the recorded version back.
	tag, err := p.pool.Exec(ctx, `
		INSERT INTO device_thresholds (
			device_id, desired_version, temperature_high_c, humidity_high_rh, gas_high_ppm,
			confirmed_version, confirmation_state, updated_at)
		VALUES ($1, $2, $3, $4, $5, $2, 'confirmed', $6)
		ON CONFLICT (device_id) DO UPDATE SET
			confirmed_version = GREATEST(COALESCE(device_thresholds.confirmed_version, 0), EXCLUDED.confirmed_version),
			confirmation_state = CASE
				WHEN GREATEST(COALESCE(device_thresholds.confirmed_version, 0), EXCLUDED.confirmed_version) >= device_thresholds.desired_version
				THEN 'confirmed' ELSE 'pending' END,
			updated_at = EXCLUDED.updated_at`,
		deviceID, version, domain.DefaultThresholds().TemperatureHighC,
		domain.DefaultThresholds().HumidityHighRh, domain.DefaultThresholds().GasHighPpm, at)
	if err != nil {
		return fmt.Errorf("store: confirm threshold version: %w", err)
	}
	_ = tag
	return nil
}

// InsertCommand implements Store. The command row and, for set_thresholds, the
// desired threshold version are written in one transaction.
func (p *Postgres) InsertCommand(ctx context.Context, command domain.Command) error {
	if err := command.Validate(); err != nil {
		return err
	}
	return p.inTx(ctx, func(tx pgx.Tx) error {
		var temperature *float64
		var humidity *float64
		var gas *float64
		var desiredVersion *int
		if command.Payload.Thresholds != nil {
			temperature = &command.Payload.Thresholds.TemperatureHighC
			humidity = &command.Payload.Thresholds.HumidityHighRh
			gas = &command.Payload.Thresholds.GasHighPpm
		}
		if command.DesiredVersion != nil {
			desiredVersion = command.DesiredVersion
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO device_commands (
				request_id, device_id, type, state, idempotency_key, actor, accepted_at,
				expires_at, temperature_high_c, humidity_high_rh, gas_high_ppm,
				desired_version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			command.RequestID, command.DeviceID, string(command.Type), string(command.State),
			command.IdempotencyKey, command.Actor, command.AcceptedAt, command.ExpiresAt,
			temperature, humidity, gas, desiredVersion); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: command or idempotency key already exists", ErrConflict)
			}
			return fmt.Errorf("store: insert command: %w", err)
		}

		if command.Type != domain.CommandSetThresholds || command.Payload.Thresholds == nil || desiredVersion == nil {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO device_thresholds (
				device_id, desired_version, temperature_high_c, humidity_high_rh, gas_high_ppm,
				confirmation_state, updated_at)
			VALUES ($1,$2,$3,$4,$5,'pending',$6)
			ON CONFLICT (device_id) DO UPDATE SET
				desired_version = EXCLUDED.desired_version,
				temperature_high_c = EXCLUDED.temperature_high_c,
				humidity_high_rh = EXCLUDED.humidity_high_rh,
				gas_high_ppm = EXCLUDED.gas_high_ppm,
				confirmation_state = 'pending',
				updated_at = EXCLUDED.updated_at
			WHERE device_thresholds.desired_version < EXCLUDED.desired_version`,
			command.DeviceID, *desiredVersion, command.Payload.Thresholds.TemperatureHighC,
			command.Payload.Thresholds.HumidityHighRh, command.Payload.Thresholds.GasHighPpm,
			command.AcceptedAt)
		if err != nil {
			return fmt.Errorf("store: record desired thresholds with command: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: threshold version %d is not greater than the stored version", ErrConflict, *desiredVersion)
		}
		return nil
	})
}

// commandColumns is the projection used by every command read.
const commandColumns = `request_id, device_id, type, state, idempotency_key, actor,
	accepted_at, published_at, completed_at, expires_at, temperature_high_c,
	humidity_high_rh, gas_high_ppm, desired_version, confirmed_version, error_code`

// Command implements Store.
func (p *Postgres) Command(ctx context.Context, deviceID, requestID string) (domain.Command, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+commandColumns+`
		FROM device_commands WHERE device_id = $1 AND request_id = $2`, deviceID, requestID)

	command, err := scanCommand(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Command{}, fmt.Errorf("%w: command %s", ErrNotFound, requestID)
	}
	if err != nil {
		return domain.Command{}, fmt.Errorf("store: read command: %w", err)
	}
	return command, nil
}

// CommandByIdempotencyKey implements Store.
func (p *Postgres) CommandByIdempotencyKey(ctx context.Context, deviceID, key string) (domain.Command, error) {
	row := p.pool.QueryRow(ctx, `SELECT `+commandColumns+`
		FROM device_commands WHERE device_id = $1 AND idempotency_key = $2`, deviceID, key)

	command, err := scanCommand(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Command{}, fmt.Errorf("%w: idempotency key for device %s", ErrNotFound, deviceID)
	}
	if err != nil {
		return domain.Command{}, fmt.Errorf("store: read command by idempotency key: %w", err)
	}
	return command, nil
}

// MarkCommandPublished implements Store.
func (p *Postgres) MarkCommandPublished(ctx context.Context, deviceID, requestID string, at time.Time) (domain.Command, error) {
	row := p.pool.QueryRow(ctx, `UPDATE device_commands SET state = 'published', published_at = $3
		WHERE device_id = $1 AND request_id = $2 AND state = 'accepted'
		RETURNING `+commandColumns, deviceID, requestID, at)

	command, err := scanCommand(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Command{}, fmt.Errorf("%w: command %s is not in the accepted state", ErrConflict, requestID)
	}
	if err != nil {
		return domain.Command{}, fmt.Errorf("store: mark command published: %w", err)
	}
	return command, nil
}

// terminalStates is the set of states a command cannot leave.
var terminalStates = []string{
	string(domain.CommandApplied), string(domain.CommandRejected), string(domain.CommandExpired),
	string(domain.CommandDuplicate), string(domain.CommandFailed), string(domain.CommandTimedOut),
	string(domain.CommandPublishFailed),
}

// ApplyCommandPatch implements Store.
func (p *Postgres) ApplyCommandPatch(ctx context.Context, deviceID, requestID string, patch domain.CommandPatch) (domain.Command, error) {
	row := p.pool.QueryRow(ctx, `UPDATE device_commands SET
			state = $3, completed_at = $4, confirmed_version = $5, error_code = $6
		WHERE device_id = $1 AND request_id = $2 AND state <> ALL($7::text[])
		RETURNING `+commandColumns,
		deviceID, requestID, string(patch.State), patch.CompletedAt, patch.ConfirmedVersion,
		patch.ErrorCode, terminalStates)

	command, err := scanCommand(row)
	if err == nil {
		return command, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Command{}, fmt.Errorf("store: apply command patch: %w", err)
	}

	// No row changed: either the command does not exist, or it is already
	// terminal. An identical replay is idempotent, a different terminal state is
	// a conflict, and the caller needs to tell them apart.
	existing, lookupErr := p.Command(ctx, deviceID, requestID)
	if lookupErr != nil {
		return domain.Command{}, lookupErr
	}
	if existing.State == patch.State {
		return existing, nil
	}
	return domain.Command{}, fmt.Errorf("%w: cannot move command %s from %s to %s",
		ErrConflict, requestID, existing.State, patch.State)
}

// ExpireCommands implements Store.
func (p *Postgres) ExpireCommands(ctx context.Context, now time.Time) ([]domain.Command, error) {
	rows, err := p.pool.Query(ctx, `UPDATE device_commands
		SET state = 'timed_out', completed_at = $1
		WHERE state IN ('accepted', 'published') AND expires_at <= $1
		RETURNING `+commandColumns, now)
	if err != nil {
		return nil, fmt.Errorf("store: expire commands: %w", err)
	}
	defer rows.Close()

	var expired []domain.Command
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan expired command: %w", err)
		}
		expired = append(expired, command)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read expired commands: %w", err)
	}
	return expired, nil
}

// PendingCommands implements Store.
func (p *Postgres) PendingCommands(ctx context.Context) ([]domain.Command, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+commandColumns+`
		FROM device_commands WHERE state IN ('accepted', 'published') ORDER BY accepted_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list pending commands: %w", err)
	}
	defer rows.Close()

	var pending []domain.Command
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan pending command: %w", err)
		}
		pending = append(pending, command)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read pending commands: %w", err)
	}
	return pending, nil
}

// inTx runs fn inside a transaction, rolling back on any error.
func (p *Postgres) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	return nil
}

// rowScanner is the shared surface of pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTelemetry reads one telemetry row.
func scanTelemetry(row rowScanner) (domain.Telemetry, error) {
	var (
		sample        domain.Telemetry
		deviceTime    *time.Time
		receivedAt    time.Time
		uptimeMs      int64
		sequence      int64
		causes        []string
		network       string
		deviceTimeSet bool
	)
	err := row.Scan(
		&sample.DeviceID, &sample.BootID, &sequence, &deviceTime, &receivedAt,
		&uptimeMs, &sample.TemperatureC, &sample.HumidityRh, &sample.GasAdcRaw,
		&sample.GasAdcFiltered, &sample.GasPpm, &sample.GasCalibrated, &sample.LocalAlarm,
		&causes, &network, &sample.ThresholdVersion, &sample.SensorFault)
	if err != nil {
		return domain.Telemetry{}, err
	}
	deviceTimeSet = deviceTime != nil
	_ = deviceTimeSet

	sample.Sequence = uint32(sequence)
	sample.UptimeMs = uint64(uptimeMs)
	sample.ReceivedAt = receivedAt.UTC()
	sample.Timestamp = utcPtr(deviceTime)
	sample.Network = domain.NetworkState(network)
	sample.AlarmCauses = make([]domain.AlarmCause, 0, len(causes))
	for _, cause := range causes {
		sample.AlarmCauses = append(sample.AlarmCauses, domain.AlarmCause(cause))
	}
	return sample, nil
}

// scanAlert reads one alert row.
func scanAlert(row rowScanner) (domain.AlertEvent, error) {
	var (
		event     domain.AlertEvent
		state     string
		endedAt   *time.Time
		startedAt time.Time
	)
	err := row.Scan(
		&event.ID, &event.DeviceID, &state, &startedAt, &endedAt,
		&event.Evidence.GasAdcRise, &event.Evidence.GasAdcRiseThreshold,
		&event.Evidence.TemperatureRateCPerMinute, &event.Evidence.TemperatureRateThresholdCPerMinute,
		&event.Evidence.SampleCount, &event.Evidence.WindowSeconds)
	if err != nil {
		return domain.AlertEvent{}, err
	}
	event.State = domain.AlertState(state)
	event.StartedAt = startedAt.UTC()
	event.EndedAt = utcPtr(endedAt)
	return event, nil
}

// scanCommand reads one command row.
func scanCommand(row rowScanner) (domain.Command, error) {
	var (
		command          domain.Command
		commandType      string
		state            string
		publishedAt      *time.Time
		completedAt      *time.Time
		acceptedAt       time.Time
		expiresAt        time.Time
		temperature      *float64
		humidity         *float64
		gas              *float64
		desiredVersion   *int
		confirmedVersion *int
	)
	err := row.Scan(
		&command.RequestID, &command.DeviceID, &commandType, &state, &command.IdempotencyKey,
		&command.Actor, &acceptedAt, &publishedAt, &completedAt, &expiresAt,
		&temperature, &humidity, &gas, &desiredVersion, &confirmedVersion, &command.ErrorCode)
	if err != nil {
		return domain.Command{}, err
	}
	command.Type = domain.CommandType(commandType)
	command.State = domain.CommandState(state)
	command.AcceptedAt = acceptedAt.UTC()
	command.PublishedAt = utcPtr(publishedAt)
	command.CompletedAt = utcPtr(completedAt)
	command.ExpiresAt = expiresAt.UTC()
	command.DesiredVersion = desiredVersion
	command.ConfirmedVersion = confirmedVersion
	if temperature != nil && humidity != nil && gas != nil {
		command.Payload.Thresholds = &domain.Thresholds{
			TemperatureHighC: *temperature,
			HumidityHighRh:   *humidity,
			GasHighPpm:       *gas,
		}
	}
	command.Payload.ThresholdVersion = desiredVersion
	return command, nil
}

// utcPtr converts a nullable timestamp to UTC.
func utcPtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// nullableTime renders a zero time as SQL NULL.
func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// nullableString renders an empty string as SQL NULL.
func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
