package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
)

// errMissing tags a payload that omitted a field the contract marks required.
var errMissing = fmt.Errorf("required field is absent")

// telemetryKeys is the frozen key set of the telemetry payload. It is compared
// against incoming JSON so an unexpected field is reported as unknown instead of
// being silently ignored.
var telemetryKeys = keySet(
	"schemaVersion", "messageType", "deviceId", "bootId", "sequence", "timestamp",
	"uptimeMs", "temperatureC", "humidityRh", "gasAdcRaw", "gasAdcFiltered",
	"gasPpm", "gasCalibrated", "localAlarm", "alarmCauses",
	"network", "thresholdVersion", "sensorFault",
)

// keySet builds the allowlist used by decodeStrict.
func keySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

// telemetryPayload mirrors the wire format. Required scalar fields are pointers
// so that an absent field is distinguishable from a legitimate zero value; a
// missing temperature must not be read as 0 °C.
type telemetryPayload struct {
	SchemaVersion    *int      `json:"schemaVersion"`
	MessageType      string    `json:"messageType"`
	DeviceID         string    `json:"deviceId"`
	BootID           string    `json:"bootId"`
	Sequence         *uint32   `json:"sequence"`
	Timestamp        *int64    `json:"timestamp"`
	UptimeMs         *uint64   `json:"uptimeMs"`
	TemperatureC     *float64  `json:"temperatureC"`
	HumidityRh       *float64  `json:"humidityRh"`
	GasAdcRaw        *int      `json:"gasAdcRaw"`
	GasAdcFiltered   *int      `json:"gasAdcFiltered"`
	GasPpm           *float64  `json:"gasPpm"`
	GasCalibrated    *bool     `json:"gasCalibrated"`
	LocalAlarm       *bool     `json:"localAlarm"`
	AlarmCauses      *[]string `json:"alarmCauses"`
	Network          string    `json:"network"`
	ThresholdVersion *int      `json:"thresholdVersion"`
	SensorFault      *bool     `json:"sensorFault"`
}

// DecodeTelemetry parses and validates a device/telemetry payload into a domain
// sample. receivedAt is the backend's own receive time and is never read from
// the payload.
//
// expectedDeviceID restricts the accepted sender when the deployment configures
// an allowlist; an empty value accepts any well-formed deviceId.
func DecodeTelemetry(raw []byte, expectedDeviceID string, receivedAt time.Time) (domain.Telemetry, error) {
	var payload telemetryPayload
	keys, err := decodeStrict(raw, telemetryKeys, &payload)
	if err != nil {
		return domain.Telemetry{}, err
	}
	// Presence is checked against the decoded key set rather than against the
	// pointer fields, because a field that is present but null and a field that
	// is absent both unmarshal to a nil pointer. timestamp is required and
	// nullable, so the distinction matters.
	if missing := requireKeys(keys, []string{
		"schemaVersion", "messageType", "deviceId", "bootId", "sequence", "timestamp",
		"uptimeMs", "temperatureC", "humidityRh", "gasAdcRaw", "gasAdcFiltered",
		"gasCalibrated", "localAlarm", "alarmCauses", "network",
		"thresholdVersion", "sensorFault",
	}); missing != "" {
		return domain.Telemetry{}, newDecodeError(ReasonMissingField, missing, errMissing)
	}
	if err := checkSchemaVersion(*payload.SchemaVersion, payload.MessageType, "telemetry"); err != nil {
		return domain.Telemetry{}, err
	}

	if expectedDeviceID != "" && payload.DeviceID != expectedDeviceID {
		return domain.Telemetry{}, newDecodeError(ReasonDeviceMismatch, "deviceId",
			fmt.Errorf("payload deviceId %q does not match the configured device", payload.DeviceID))
	}

	timestamp, decodeErr := parseDeviceTimestamp(payload.Timestamp, receivedAt)
	if decodeErr != nil {
		return domain.Telemetry{}, decodeErr
	}

	causes := make([]domain.AlarmCause, 0, len(*payload.AlarmCauses))
	for _, raw := range *payload.AlarmCauses {
		causes = append(causes, domain.AlarmCause(raw))
	}

	sample := domain.Telemetry{
		DeviceID:         payload.DeviceID,
		BootID:           payload.BootID,
		Sequence:         *payload.Sequence,
		Timestamp:        timestamp,
		UptimeMs:         *payload.UptimeMs,
		TemperatureC:     *payload.TemperatureC,
		HumidityRh:       *payload.HumidityRh,
		GasAdcRaw:        *payload.GasAdcRaw,
		GasAdcFiltered:   *payload.GasAdcFiltered,
		GasPpm:           payload.GasPpm,
		GasCalibrated:    *payload.GasCalibrated,
		LocalAlarm:       *payload.LocalAlarm,
		AlarmCauses:      causes,
		Network:          domain.NetworkState(payload.Network),
		ThresholdVersion: *payload.ThresholdVersion,
		SensorFault:      *payload.SensorFault,
		ReceivedAt:       receivedAt,
	}
	if err := sample.Validate(); err != nil {
		// A domain violation is reported with the field-level reason the device
		// contract uses, so an operator can tell a range problem from a shape
		// problem without parsing prose.
		return domain.Telemetry{}, newDecodeError(ReasonOutOfRange, "", err)
	}
	return sample, nil
}

// EncodeTelemetry renders a domain sample as the frozen wire payload. The backend
// only needs this for tests and for tooling that replays captured samples, but
// keeping encode and decode in one place stops the two from drifting.
func EncodeTelemetry(sample domain.Telemetry) ([]byte, error) {
	network := string(sample.Network)
	causes := make([]string, 0, len(sample.AlarmCauses))
	for _, cause := range sample.AlarmCauses {
		causes = append(causes, string(cause))
	}
	var timestamp *int64
	if sample.Timestamp != nil {
		ms := sample.Timestamp.UnixMilli()
		timestamp = &ms
	}
	wire := telemetryPayload{
		SchemaVersion:    intPtr(domain.SchemaVersion),
		MessageType:      "telemetry",
		DeviceID:         sample.DeviceID,
		BootID:           sample.BootID,
		Sequence:         &sample.Sequence,
		Timestamp:        timestamp,
		UptimeMs:         &sample.UptimeMs,
		TemperatureC:     &sample.TemperatureC,
		HumidityRh:       &sample.HumidityRh,
		GasAdcRaw:        &sample.GasAdcRaw,
		GasAdcFiltered:   &sample.GasAdcFiltered,
		GasPpm:           sample.GasPpm,
		GasCalibrated:    &sample.GasCalibrated,
		LocalAlarm:       &sample.LocalAlarm,
		AlarmCauses:      &causes,
		Network:          network,
		ThresholdVersion: &sample.ThresholdVersion,
		SensorFault:      &sample.SensorFault,
	}
	return json.Marshal(wire)
}

// intPtr is a tiny helper for building the pointer-shaped wire fields.
func intPtr(value int) *int { return &value }
