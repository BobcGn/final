package domain

import (
	"fmt"
	"math"
	"time"
)

// AlarmCause enumerates the frozen local alarm reasons a device may report.
type AlarmCause string

// Frozen alarm cause values. They are shared with the device contract in
// docs/device-protocol.md and must be changed there first.
const (
	AlarmTemperatureHigh      AlarmCause = "temperature_high"
	AlarmHumidityHigh         AlarmCause = "humidity_high"
	AlarmGasHigh              AlarmCause = "gas_high"
	AlarmRapidTemperatureRise AlarmCause = "rapid_temperature_rise"
	AlarmRapidGasRise         AlarmCause = "rapid_gas_rise"
	AlarmSensorFault          AlarmCause = "sensor_fault"
)

// alarmCauses is the closed set of accepted causes.
var alarmCauses = map[AlarmCause]struct{}{
	AlarmTemperatureHigh:      {},
	AlarmHumidityHigh:         {},
	AlarmGasHigh:              {},
	AlarmRapidTemperatureRise: {},
	AlarmRapidGasRise:         {},
	AlarmSensorFault:          {},
}

// Valid reports whether c is one of the frozen alarm causes.
func (c AlarmCause) Valid() bool {
	_, ok := alarmCauses[c]
	return ok
}

// NetworkState is the device's own view of its uplink. It is informational only:
// online/offline is decided by the backend from the last valid telemetry time.
type NetworkState string

// Frozen device-reported network states.
const (
	NetworkOnline       NetworkState = "online"
	NetworkReconnecting NetworkState = "reconnecting"
)

// Valid reports whether n is a frozen network state.
func (n NetworkState) Valid() bool {
	return n == NetworkOnline || n == NetworkReconnecting
}

// Telemetry range limits. They mirror the device contract; the device enforces
// them before publishing and the backend enforces them again on receipt.
const (
	MinTemperatureC = -40.0
	MaxTemperatureC = 80.0
	MinHumidityRh   = 0.0
	MaxHumidityRh   = 100.0
	MinGasAdc       = 0
	MaxGasAdc       = 4095
)

// Telemetry is one validated sample from a device. ReceivedAt is always filled
// by the backend and is never taken from the payload.
type Telemetry struct {
	DeviceID         string
	BootID           string
	Sequence         uint32
	Timestamp        *time.Time
	UptimeMs         uint64
	TemperatureC     float64
	HumidityRh       float64
	GasAdcRaw        int
	GasAdcFiltered   int
	GasPpm           *float64
	GasCalibrated    bool
	LocalAlarm       bool
	AlarmCauses      []AlarmCause
	Network          NetworkState
	ThresholdVersion int
	SensorFault      bool
	ReceivedAt       time.Time
}

// TelemetryKey identifies a sample uniquely across device restarts. Sequence
// alone resets on every power cycle, so BootID is part of the key; omitting it
// would drop legitimate samples after a restart as duplicates.
type TelemetryKey struct {
	DeviceID string
	BootID   string
	Sequence uint32
}

// Key returns the dedup and uniqueness key of the sample.
func (t Telemetry) Key() TelemetryKey {
	return TelemetryKey{DeviceID: t.DeviceID, BootID: t.BootID, Sequence: t.Sequence}
}

// EventTime returns the device event time when the device clock is synced and
// falls back to the server receive time otherwise. Ordering and alert windows
// use this value so that a device without a synced clock still produces a
// monotonic series.
func (t Telemetry) EventTime() time.Time {
	if t.Timestamp != nil {
		return *t.Timestamp
	}
	return t.ReceivedAt
}

// EventTimeSource reports whether EventTime came from the device ("device") or
// from the server receive time ("receivedAt"). It is recorded alongside stored
// samples so that clock skew stays diagnosable.
func (t Telemetry) EventTimeSource() string {
	if t.Timestamp != nil {
		return "device"
	}
	return "receivedAt"
}

// Validate checks the sample against the frozen contract. It returns an error
// wrapping ErrInvalidTelemetry describing the first offending field, so callers
// can classify the failure with errors.Is and log the field for diagnosis.
//
// Validate is intentionally strict: an out-of-range or inconsistent sample is
// rejected as a whole rather than partially stored, because a partially stored
// sample would silently corrupt the alert evidence trail.
func (t Telemetry) Validate() error {
	if err := ValidateDeviceID(t.DeviceID); err != nil {
		return err
	}
	if err := ValidateBootID(t.BootID); err != nil {
		return err
	}
	if !isFinite(t.TemperatureC) || t.TemperatureC < MinTemperatureC || t.TemperatureC > MaxTemperatureC {
		return fmt.Errorf("%w: temperatureC %v outside [%v,%v]", ErrInvalidTelemetry, t.TemperatureC, MinTemperatureC, MaxTemperatureC)
	}
	if !isFinite(t.HumidityRh) || t.HumidityRh < MinHumidityRh || t.HumidityRh > MaxHumidityRh {
		return fmt.Errorf("%w: humidityRh %v outside [%v,%v]", ErrInvalidTelemetry, t.HumidityRh, MinHumidityRh, MaxHumidityRh)
	}
	if t.GasAdcRaw < MinGasAdc || t.GasAdcRaw > MaxGasAdc {
		return fmt.Errorf("%w: gasAdcRaw %d outside [%d,%d]", ErrInvalidTelemetry, t.GasAdcRaw, MinGasAdc, MaxGasAdc)
	}
	if t.GasAdcFiltered < MinGasAdc || t.GasAdcFiltered > MaxGasAdc {
		return fmt.Errorf("%w: gasAdcFiltered %d outside [%d,%d]", ErrInvalidTelemetry, t.GasAdcFiltered, MinGasAdc, MaxGasAdc)
	}
	if t.GasPpm != nil && (!isFinite(*t.GasPpm) || *t.GasPpm < 0) {
		return fmt.Errorf("%w: gasPpm %v must be finite and non-negative", ErrInvalidTelemetry, *t.GasPpm)
	}
	if !t.Network.Valid() {
		return fmt.Errorf("%w: network %q is not a known state", ErrInvalidTelemetry, t.Network)
	}
	if t.ThresholdVersion < 1 {
		return fmt.Errorf("%w: thresholdVersion %d must be >= 1", ErrInvalidTelemetry, t.ThresholdVersion)
	}
	for _, cause := range t.AlarmCauses {
		if !cause.Valid() {
			return fmt.Errorf("%w: alarmCauses contains unknown value %q", ErrInvalidTelemetry, cause)
		}
	}
	if t.SensorFault && !containsCause(t.AlarmCauses, AlarmSensorFault) {
		return fmt.Errorf("%w: sensorFault is true but alarmCauses does not contain %q", ErrInvalidTelemetry, AlarmSensorFault)
	}
	if t.LocalAlarm && len(t.AlarmCauses) == 0 {
		return fmt.Errorf("%w: localAlarm is true but alarmCauses is empty", ErrInvalidTelemetry)
	}
	if t.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: receivedAt must be set by the backend", ErrInvalidTelemetry)
	}
	return nil
}

// containsCause reports whether cause is present in causes.
func containsCause(causes []AlarmCause, cause AlarmCause) bool {
	for _, c := range causes {
		if c == cause {
			return true
		}
	}
	return false
}

// isFinite reports whether f is neither NaN nor an infinity. JSON decoding can
// produce both through bare literals, so every float field is checked.
func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
