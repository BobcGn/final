// Package events defines the realtime event vocabulary the backend pushes to
// clients over WebSocket.
//
// It exists as its own package so that the ingest and command services can emit
// events without importing the HTTP layer, and so that the envelope shape has a
// single definition shared by the producer, the transport and the tests.
package events

import (
	"context"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
)

// Type is the frozen WebSocket event type.
type Type string

// Frozen event types, matching docs/api/openapi.yaml.
const (
	TypeTelemetryUpdated     Type = "telemetry.updated"
	TypeDeviceStatusChanged  Type = "device.status_changed"
	TypeAlertStateChanged    Type = "alert.state_changed"
	TypeCommandStatusChanged Type = "command.status_changed"
	TypeThresholdsConfirmed  Type = "thresholds.confirmed"
)

// Valid reports whether t is a frozen event type.
func (t Type) Valid() bool {
	switch t {
	case TypeTelemetryUpdated, TypeDeviceStatusChanged, TypeAlertStateChanged,
		TypeCommandStatusChanged, TypeThresholdsConfirmed:
		return true
	default:
		return false
	}
}

// Types returns every frozen event type, sorted. The contract test compares this
// with the type enum in docs/api/openapi.yaml.
func Types() []Type {
	return []Type{
		TypeAlertStateChanged,
		TypeCommandStatusChanged,
		TypeDeviceStatusChanged,
		TypeTelemetryUpdated,
		TypeThresholdsConfirmed,
	}
}

// Envelope is the JSON message the server sends on the realtime stream.
//
// Data carries the event-specific body. It is typed as any so that the envelope
// stays one shape; the concrete types are documented per event type in
// backend/docs/api.md.
type Envelope struct {
	Type       Type      `json:"type"`
	EventID    string    `json:"eventId"`
	OccurredAt time.Time `json:"occurredAt"`
	DeviceID   string    `json:"deviceId"`
	Data       any       `json:"data"`
}

// TelemetryData is the payload of telemetry.updated.
type TelemetryData struct {
	Sequence       uint32              `json:"sequence"`
	BootID         string              `json:"bootId"`
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
	SensorFault    bool                `json:"sensorFault"`
}

// DeviceStatusData is the payload of device.status_changed.
type DeviceStatusData struct {
	Connectivity domain.Connectivity `json:"connectivity"`
	LastSeenAt   time.Time           `json:"lastSeenAt"`
	AlarmState   domain.AlertState   `json:"alarmState"`
}

// ThresholdsConfirmedData is the payload of thresholds.confirmed.
type ThresholdsConfirmedData struct {
	ConfirmedVersion int `json:"confirmedVersion"`
}

// Sink receives the realtime events the backend produces. Implementations must
// not block: they are called from the MQTT read loop, and a slow consumer must
// be dropped by the implementation rather than stalling telemetry intake.
type Sink interface {
	PublishTelemetryUpdated(ctx context.Context, sample domain.Telemetry)
	PublishDeviceStatusChanged(ctx context.Context, deviceID string, data DeviceStatusData)
	PublishAlertStateChanged(ctx context.Context, event domain.AlertEvent)
	PublishCommandStatusChanged(ctx context.Context, command domain.Command)
	PublishThresholdsConfirmed(ctx context.Context, deviceID string, version int)
}

// AlertEvidenceData is the wire shape of an alert's trigger evidence in the
// realtime stream.
//
// It mirrors api.alertEvidenceResource rather than reusing it, because the two
// packages are separate contracts: one is a REST response body and one is a
// WebSocket event body, and coupling them through a shared type would mean a
// change to either shape is silently applied to both. The names are the ones
// docs/api/openapi.yaml fixes, written out as tags so a Go rename cannot change
// what a client sees.
type AlertEvidenceData struct {
	GasAdcRise                         int     `json:"gasAdcRise"`
	GasAdcRiseThreshold                int     `json:"gasAdcRiseThreshold"`
	TemperatureRateCPerMinute          float64 `json:"temperatureRateCPerMinute"`
	TemperatureRateThresholdCPerMinute float64 `json:"temperatureRateThresholdCPerMinute"`
	SampleCount                        int     `json:"sampleCount"`
	WindowSeconds                      int     `json:"windowSeconds"`
}

// AlertData is the payload of alert.state_changed.
type AlertData struct {
	ID        string            `json:"id"`
	State     domain.AlertState `json:"state"`
	StartedAt time.Time         `json:"startedAt"`
	EndedAt   *time.Time        `json:"endedAt"`
	Evidence  AlertEvidenceData `json:"evidence"`
}

// CommandStatusData is the payload of command.status_changed.
type CommandStatusData struct {
	RequestID        string              `json:"requestId"`
	Type             domain.CommandType  `json:"type"`
	State            domain.CommandState `json:"state"`
	AcceptedAt       time.Time           `json:"acceptedAt"`
	CompletedAt      *time.Time          `json:"completedAt"`
	ExpiresAt        time.Time           `json:"expiresAt"`
	DesiredVersion   *int                `json:"desiredVersion"`
	ConfirmedVersion *int                `json:"confirmedVersion"`
	ErrorCode        string              `json:"errorCode,omitempty"`
}

// TelemetryDataFrom builds the telemetry event body from a stored sample.
func TelemetryDataFrom(sample domain.Telemetry) TelemetryData {
	return TelemetryData{
		Sequence:       sample.Sequence,
		BootID:         sample.BootID,
		Timestamp:      sample.Timestamp,
		ReceivedAt:     sample.ReceivedAt,
		TemperatureC:   sample.TemperatureC,
		HumidityRh:     sample.HumidityRh,
		GasAdcRaw:      sample.GasAdcRaw,
		GasAdcFiltered: sample.GasAdcFiltered,
		GasPpm:         sample.GasPpm,
		GasCalibrated:  sample.GasCalibrated,
		LocalAlarm:     sample.LocalAlarm,
		AlarmCauses:    sample.AlarmCauses,
		BuzzerMuted:    sample.BuzzerMuted,
		SensorFault:    sample.SensorFault,
	}
}

// AlertDataFrom builds the alert event body. Evidence is copied field by field so
// that a new domain field cannot reach the wire before it is given a name in the
// contract.
func AlertDataFrom(event domain.AlertEvent) AlertData {
	return AlertData{
		ID:        event.ID,
		State:     event.State,
		StartedAt: event.StartedAt,
		EndedAt:   event.EndedAt,
		Evidence: AlertEvidenceData{
			GasAdcRise:                         event.Evidence.GasAdcRise,
			GasAdcRiseThreshold:                event.Evidence.GasAdcRiseThreshold,
			TemperatureRateCPerMinute:          event.Evidence.TemperatureRateCPerMinute,
			TemperatureRateThresholdCPerMinute: event.Evidence.TemperatureRateThresholdCPerMinute,
			SampleCount:                        event.Evidence.SampleCount,
			WindowSeconds:                      event.Evidence.WindowSeconds,
		},
	}
}

// CommandStatusDataFrom builds the command event body.
func CommandStatusDataFrom(command domain.Command) CommandStatusData {
	return CommandStatusData{
		RequestID:        command.RequestID,
		Type:             command.Type,
		State:            command.State,
		AcceptedAt:       command.AcceptedAt,
		CompletedAt:      command.CompletedAt,
		ExpiresAt:        command.ExpiresAt,
		DesiredVersion:   command.DesiredVersion,
		ConfirmedVersion: command.ConfirmedVersion,
		ErrorCode:        command.ErrorCode,
	}
}
