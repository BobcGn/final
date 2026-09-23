package api

import (
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
	"github.com/BobcGn/final/backend/internal/store"
)

// Timestamps in the frozen contract are RFC 3339 with second precision. Go's
// encoder omits the fractional part when it is zero, so truncating here makes
// every response byte-stable instead of varying with the stored nanoseconds.
func timestamp(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// timestampPtr truncates an optional timestamp.
func timestampPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	truncated := timestamp(*t)
	return &truncated
}

// healthResponse is the body of GET /healthz.
type healthResponse struct {
	Status string `json:"status"`
}

// errorEnvelope is the shared error body.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// errorBody describes one failure.
type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// deviceStatusResponse is the body of GET /status.
type deviceStatusResponse struct {
	DeviceID            string              `json:"deviceId"`
	Connectivity        domain.Connectivity `json:"connectivity"`
	AlarmState          domain.AlertState   `json:"alarmState"`
	BuzzerMuted         bool                `json:"buzzerMuted"`
	LocalAlarm          bool                `json:"localAlarm"`
	OfflineAfterSeconds int                 `json:"offlineAfterSeconds"`
	LastSeenAt          *time.Time          `json:"lastSeenAt"`
	ThresholdVersion    thresholdVersion    `json:"thresholdVersion"`
}

// thresholdVersion reports desired and device-confirmed threshold versions.
type thresholdVersion struct {
	Desired   int  `json:"desired"`
	Confirmed *int `json:"confirmed"`
}

// telemetryPoint is one sample in an API response.
type telemetryPoint struct {
	DeviceID       string              `json:"deviceId"`
	BootID         string              `json:"bootId,omitempty"`
	Sequence       uint32              `json:"sequence,omitempty"`
	Timestamp      *time.Time          `json:"timestamp"`
	ReceivedAt     time.Time           `json:"receivedAt"`
	TemperatureC   float64             `json:"temperatureC"`
	HumidityRh     float64             `json:"humidityRh"`
	GasAdcRaw      int                 `json:"gasAdcRaw"`
	GasAdcFiltered int                 `json:"gasAdcFiltered"`
	GasPpm         *float64            `json:"gasPpm"`
	GasCalibrated  bool                `json:"gasCalibrated"`
	LocalAlarm     bool                `json:"localAlarm"`
	AlarmCauses    []domain.AlarmCause `json:"alarmCauses"`
	BuzzerMuted    bool                `json:"buzzerMuted"`
	Network        domain.NetworkState `json:"network,omitempty"`
	SensorFault    bool                `json:"sensorFault"`
}

// newTelemetryPoint converts a stored sample into its API representation.
func newTelemetryPoint(sample domain.Telemetry) telemetryPoint {
	causes := sample.AlarmCauses
	if causes == nil {
		// An absent alarm cause list encodes as null, which the contract forbids;
		// an empty array is the documented representation of "no causes".
		causes = []domain.AlarmCause{}
	}
	return telemetryPoint{
		DeviceID:       sample.DeviceID,
		BootID:         sample.BootID,
		Sequence:       sample.Sequence,
		Timestamp:      timestampPtr(sample.Timestamp),
		ReceivedAt:     timestamp(sample.ReceivedAt),
		TemperatureC:   sample.TemperatureC,
		HumidityRh:     sample.HumidityRh,
		GasAdcRaw:      sample.GasAdcRaw,
		GasAdcFiltered: sample.GasAdcFiltered,
		GasPpm:         sample.GasPpm,
		GasCalibrated:  sample.GasCalibrated,
		LocalAlarm:     sample.LocalAlarm,
		AlarmCauses:    causes,
		BuzzerMuted:    sample.BuzzerMuted,
		Network:        sample.Network,
		SensorFault:    sample.SensorFault,
	}
}

// telemetryPage is one page of samples.
type telemetryPage struct {
	Items      []telemetryPoint `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

// alertEvidenceResource is the wire shape of an alert's trigger evidence.
//
// It exists rather than marshalling domain.AlertEvidence directly, for one
// reason: the domain type has no JSON tags, so encoding/json would emit its
// exported Go names — GasAdcRise, SampleCount and so on. docs/api/openapi.yaml
// fixes the names as camelCase, and every client decodes them by those names.
// A domain type is a statement about the business; a wire type is a statement
// about a contract. Keeping them separate is what stops a rename in one from
// silently breaking the other.
//
// All six fields are always emitted. The contract marks only three of them
// required, so a reader must still cope with a future where the other three
// become optional — but this backend never omits any of them.
type alertEvidenceResource struct {
	GasAdcRise                         int     `json:"gasAdcRise"`
	GasAdcRiseThreshold                int     `json:"gasAdcRiseThreshold"`
	TemperatureRateCPerMinute          float64 `json:"temperatureRateCPerMinute"`
	TemperatureRateThresholdCPerMinute float64 `json:"temperatureRateThresholdCPerMinute"`
	SampleCount                        int     `json:"sampleCount"`
	WindowSeconds                      int     `json:"windowSeconds"`
}

// newAlertEvidenceResource maps the stored evidence onto its wire shape. The
// values are copied field by field so that adding a field to the domain type
// cannot silently start appearing on the wire.
func newAlertEvidenceResource(evidence domain.AlertEvidence) alertEvidenceResource {
	return alertEvidenceResource{
		GasAdcRise:                         evidence.GasAdcRise,
		GasAdcRiseThreshold:                evidence.GasAdcRiseThreshold,
		TemperatureRateCPerMinute:          evidence.TemperatureRateCPerMinute,
		TemperatureRateThresholdCPerMinute: evidence.TemperatureRateThresholdCPerMinute,
		SampleCount:                        evidence.SampleCount,
		WindowSeconds:                      evidence.WindowSeconds,
	}
}

// alertEventResource is one alert episode in an API response.
type alertEventResource struct {
	ID        string                `json:"id"`
	DeviceID  string                `json:"deviceId"`
	State     domain.AlertState     `json:"state"`
	StartedAt time.Time             `json:"startedAt"`
	EndedAt   *time.Time            `json:"endedAt"`
	Evidence  alertEvidenceResource `json:"evidence"`
}

// newAlertEventResource converts a stored alert into its API representation.
func newAlertEventResource(event domain.AlertEvent) alertEventResource {
	return alertEventResource{
		ID:        event.ID,
		DeviceID:  event.DeviceID,
		State:     event.State,
		StartedAt: timestamp(event.StartedAt),
		EndedAt:   timestampPtr(event.EndedAt),
		Evidence:  newAlertEvidenceResource(event.Evidence),
	}
}

// alertPage is one page of alert episodes.
type alertPage struct {
	Items      []alertEventResource `json:"items"`
	NextCursor *string              `json:"nextCursor"`
}

// thresholdsResource is the body of GET /thresholds.
type thresholdsResource struct {
	DesiredVersion    int                     `json:"desiredVersion"`
	ConfirmedVersion  *int                    `json:"confirmedVersion"`
	TemperatureHighC  float64                 `json:"temperatureHighC"`
	HumidityHighRh    float64                 `json:"humidityHighRh"`
	GasHighPpm        float64                 `json:"gasHighPpm"`
	UpdatedAt         *time.Time              `json:"updatedAt,omitempty"`
	ConfirmationState store.ConfirmationState `json:"confirmationState"`
}

// newThresholdsResource converts a stored record into its API representation.
func newThresholdsResource(record store.ThresholdRecord) thresholdsResource {
	var updatedAt *time.Time
	if !record.UpdatedAt.IsZero() {
		value := timestamp(record.UpdatedAt)
		updatedAt = &value
	}
	return thresholdsResource{
		DesiredVersion:    record.DesiredVersion,
		ConfirmedVersion:  record.ConfirmedVersion,
		TemperatureHighC:  record.Desired.TemperatureHighC,
		HumidityHighRh:    record.Desired.HumidityHighRh,
		GasHighPpm:        record.Desired.GasHighPpm,
		UpdatedAt:         updatedAt,
		ConfirmationState: record.ConfirmationState,
	}
}

// thresholdUpdateRequest is the body of PUT /thresholds.
type thresholdUpdateRequest struct {
	TemperatureHighC *float64 `json:"temperatureHighC"`
	HumidityHighRh   *float64 `json:"humidityHighRh"`
	GasHighPpm       *float64 `json:"gasHighPpm"`
}

// muteCommandRequest is the body of POST /commands/mute.
type muteCommandRequest struct {
	Muted *bool `json:"muted"`
}

// commandAcceptedResponse is the 202 body of a control request.
type commandAcceptedResponse struct {
	RequestID      string     `json:"requestId"`
	Status         string     `json:"status"`
	DesiredVersion *int       `json:"desiredVersion,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
}

// commandStatusResponse is the body of GET /commands/{requestId}.
type commandStatusResponse struct {
	RequestID        string              `json:"requestId"`
	DeviceID         string              `json:"deviceId"`
	Type             domain.CommandType  `json:"type"`
	State            domain.CommandState `json:"state"`
	AcceptedAt       time.Time           `json:"acceptedAt"`
	CompletedAt      *time.Time          `json:"completedAt"`
	ExpiresAt        time.Time           `json:"expiresAt"`
	DesiredVersion   *int                `json:"desiredVersion"`
	ConfirmedVersion *int                `json:"confirmedVersion"`
	ErrorCode        *string             `json:"errorCode"`
}

// newCommandStatusResponse converts a stored command into its API representation.
func newCommandStatusResponse(command domain.Command) commandStatusResponse {
	var errorCode *string
	if command.ErrorCode != "" {
		value := command.ErrorCode
		errorCode = &value
	}
	return commandStatusResponse{
		RequestID:        command.RequestID,
		DeviceID:         command.DeviceID,
		Type:             command.Type,
		State:            command.State,
		AcceptedAt:       timestamp(command.AcceptedAt),
		CompletedAt:      timestampPtr(command.CompletedAt),
		ExpiresAt:        timestamp(command.ExpiresAt),
		DesiredVersion:   command.DesiredVersion,
		ConfirmedVersion: command.ConfirmedVersion,
		ErrorCode:        errorCode,
	}
}

// wsEnvelope mirrors events.Envelope for the wire. It exists so that the
// transport owns its JSON shape rather than depending on the producer's tags.
type wsEnvelope struct {
	Type       events.Type `json:"type"`
	EventID    string      `json:"eventId"`
	OccurredAt time.Time   `json:"occurredAt"`
	DeviceID   string      `json:"deviceId"`
	Data       any         `json:"data"`
}
