package events_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
)

// instant is a fixed time so the cases are reproducible.
var instant = time.Date(2026, 9, 24, 10, 40, 30, 0, time.UTC)

// TestTypesAreValid verifies that the frozen list and the validator agree. A type
// that is listed but does not validate, or the reverse, would mean the contract
// test and the runtime disagree about what the stream may carry.
func TestTypesAreValid(t *testing.T) {
	for _, eventType := range events.Types() {
		if !eventType.Valid() {
			t.Errorf("%q is listed but does not validate", eventType)
		}
	}
	if events.Type("alert.exploded").Valid() {
		t.Error("an invented event type validated")
	}
	if events.Type("").Valid() {
		t.Error("the empty event type validated")
	}
}

// TestEnvelopeSerialisesTheContractShape verifies the JSON the stream sends.
func TestEnvelopeSerialisesTheContractShape(t *testing.T) {
	envelope := events.Envelope{
		Type:       events.TypeTelemetryUpdated,
		EventID:    "01EVENT",
		OccurredAt: instant,
		DeviceID:   "MCU001",
		Data: events.TelemetryData{
			Sequence:       42,
			BootID:         "9f3ac21b",
			ReceivedAt:     instant,
			TemperatureC:   28,
			GasAdcFiltered: 1328,
			LocalAlarm:     true,
			AlarmCauses:    []domain.AlarmCause{domain.AlarmGasHigh},
		},
	}

	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Type       string `json:"type"`
		EventID    string `json:"eventId"`
		OccurredAt string `json:"occurredAt"`
		DeviceID   string `json:"deviceId"`
		Data       struct {
			Sequence   uint32   `json:"sequence"`
			AlarmCause []string `json:"alarmCauses"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}

	if decoded.Type != "telemetry.updated" || decoded.EventID != "01EVENT" || decoded.DeviceID != "MCU001" {
		t.Fatalf("decoded %+v", decoded)
	}
	if decoded.OccurredAt == "" {
		t.Fatal("occurredAt is missing")
	}
	if decoded.Data.Sequence != 42 || len(decoded.Data.AlarmCause) != 1 {
		t.Fatalf("data = %+v", decoded.Data)
	}
}

// TestDataBuildersCarryEveryField verifies that a builder does not silently drop
// a field the client needs. A missing field in a realtime event is not
// recoverable by the client, because the stream carries increments only.
func TestDataBuildersCarryEveryField(t *testing.T) {
	gas := 25.0
	timestamp := instant
	sample := domain.Telemetry{
		DeviceID:         "MCU001",
		BootID:           "9f3ac21b",
		Sequence:         42,
		Timestamp:        &timestamp,
		UptimeMs:         125000,
		TemperatureC:     28,
		HumidityRh:       61,
		GasAdcRaw:        1350,
		GasAdcFiltered:   1328,
		GasPpm:           &gas,
		GasCalibrated:    false,
		LocalAlarm:       true,
		AlarmCauses:      []domain.AlarmCause{domain.AlarmGasHigh},
		Network:          domain.NetworkOnline,
		ThresholdVersion: 3,
		SensorFault:      false,
		ReceivedAt:       instant,
	}

	data := events.TelemetryDataFrom(sample)
	if data.Sequence != sample.Sequence || data.BootID != sample.BootID {
		t.Fatalf("identity was not carried: %+v", data)
	}
	if data.TemperatureC != sample.TemperatureC || data.HumidityRh != sample.HumidityRh {
		t.Fatalf("readings were not carried: %+v", data)
	}
	if data.GasAdcRaw != sample.GasAdcRaw || data.GasAdcFiltered != sample.GasAdcFiltered {
		t.Fatalf("gas readings were not carried: %+v", data)
	}
	if data.GasPpm == nil || *data.GasPpm != gas {
		t.Fatalf("gasPpm was not carried: %+v", data.GasPpm)
	}
	if !data.LocalAlarm || data.SensorFault != sample.SensorFault {
		t.Fatalf("flags were not carried: %+v", data)
	}
	if len(data.AlarmCauses) != 1 || data.AlarmCauses[0] != domain.AlarmGasHigh {
		t.Fatalf("alarm causes were not carried: %+v", data.AlarmCauses)
	}
	if data.Timestamp == nil || !data.Timestamp.Equal(timestamp) {
		t.Fatalf("the device timestamp was not carried: %v", data.Timestamp)
	}
	if !data.ReceivedAt.Equal(sample.ReceivedAt) {
		t.Fatalf("receivedAt was not carried: %v", data.ReceivedAt)
	}

	ended := instant.Add(time.Minute)
	alert := domain.AlertEvent{
		ID: "01ALERT", DeviceID: "MCU001", State: domain.AlertFireWarning,
		StartedAt: instant, EndedAt: &ended,
		Evidence: domain.AlertEvidence{
			GasAdcRise: 200, GasAdcRiseThreshold: 150,
			TemperatureRateCPerMinute: 4, TemperatureRateThresholdCPerMinute: 3,
			SampleCount: 8, WindowSeconds: 60,
		},
	}
	alertData := events.AlertDataFrom(alert)
	if alertData.ID != alert.ID || alertData.State != alert.State {
		t.Fatalf("the alert identity was not carried: %+v", alertData)
	}
	if alertData.EndedAt == nil || !alertData.EndedAt.Equal(ended) {
		t.Fatalf("the end time was not carried: %v", alertData.EndedAt)
	}
	if alertData.Evidence.GasAdcRise != 200 || alertData.Evidence.SampleCount != 8 {
		t.Fatalf("the evidence was not carried: %+v", alertData.Evidence)
	}

	version := 4
	command := domain.Command{
		RequestID: "01REQ", DeviceID: "MCU001", Type: domain.CommandSetThresholds,
		State:          domain.CommandApplied,
		Payload:        domain.CommandPayload{ThresholdVersion: &version},
		AcceptedAt:     instant,
		ExpiresAt:      instant.Add(time.Minute),
		DesiredVersion: &version,
		ErrorCode:      "",
	}
	commandData := events.CommandStatusDataFrom(command)
	if commandData.RequestID != command.RequestID || commandData.State != command.State {
		t.Fatalf("the command identity was not carried: %+v", commandData)
	}
	if commandData.DesiredVersion == nil || *commandData.DesiredVersion != version {
		t.Fatalf("the desired version was not carried: %+v", commandData.DesiredVersion)
	}
	if !commandData.ExpiresAt.Equal(command.ExpiresAt) {
		t.Fatalf("the expiry was not carried: %v", commandData.ExpiresAt)
	}
}

// TestAlertDataUsesContractEvidenceKeys checks the raw realtime payload rather
// than decoding into a tagged Go struct. encoding/json matches field names
// loosely on input, so only the raw key set catches a regression to PascalCase.
func TestAlertDataUsesContractEvidenceKeys(t *testing.T) {
	event := domain.AlertEvent{
		ID: "01ALERT", DeviceID: "MCU001", State: domain.AlertFireWarning,
		StartedAt: instant,
		Evidence: domain.AlertEvidence{
			GasAdcRise: 200, GasAdcRiseThreshold: 150,
			TemperatureRateCPerMinute: 4, TemperatureRateThresholdCPerMinute: 3,
			SampleCount: 8, WindowSeconds: 60,
		},
	}
	raw, err := json.Marshal(events.AlertDataFrom(event))
	if err != nil {
		t.Fatalf("marshal alert data: %v", err)
	}
	var data struct {
		Evidence map[string]json.RawMessage `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal alert data %s: %v", raw, err)
	}
	want := map[string]bool{
		"gasAdcRise": true, "gasAdcRiseThreshold": true,
		"temperatureRateCPerMinute": true, "temperatureRateThresholdCPerMinute": true,
		"sampleCount": true, "windowSeconds": true,
	}
	if len(data.Evidence) != len(want) {
		t.Fatalf("evidence keys = %v, want exactly the six contract keys", data.Evidence)
	}
	for key := range want {
		if _, ok := data.Evidence[key]; !ok {
			t.Errorf("evidence is missing contract key %q; got %v", key, data.Evidence)
		}
	}
	for key := range data.Evidence {
		if !want[key] {
			t.Errorf("evidence contains non-contract key %q", key)
		}
	}
}

// TestConfirmedDataCarriesTheVersion verifies the thresholds.confirmed body, which
// is how a client learns that the device adopted a new threshold version.
func TestConfirmedDataCarriesTheVersion(t *testing.T) {
	raw, err := json.Marshal(events.ThresholdsConfirmedData{ConfirmedVersion: 4})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"confirmedVersion":4}` {
		t.Fatalf("encoded as %s", raw)
	}
}

// TestDeviceStatusDataUsesTheFrozenEnums verifies that the status body encodes
// the contract's spellings rather than Go's type names.
func TestDeviceStatusDataUsesTheFrozenEnums(t *testing.T) {
	raw, err := json.Marshal(events.DeviceStatusData{
		Connectivity: domain.ConnectivityOffline,
		LastSeenAt:   instant,
		AlarmState:   domain.AlertFireWarning,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Connectivity string `json:"connectivity"`
		AlarmState   string `json:"alarmState"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Connectivity != "offline" {
		t.Fatalf("connectivity = %q, want offline", decoded.Connectivity)
	}
	if decoded.AlarmState != "fire_warning" {
		t.Fatalf("alarmState = %q, want fire_warning", decoded.AlarmState)
	}
}
