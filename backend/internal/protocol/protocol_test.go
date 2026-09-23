package protocol_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/protocol"
)

// receivedAt is the server receive time used throughout.
//
// It sits just after the timestamps printed in docs/device-protocol.md, because
// the decoder rejects a device timestamp more than a day in the future as a
// broken clock. A test that used "now" would fail on a document example dated
// months earlier, and the failure would say nothing about the codec.
var receivedAt = time.Date(2026, 9, 24, 10, 40, 30, 0, time.UTC)

// reasonOf returns the stable reject reason of a decode error, or "" when the
// error is not a DecodeError.
func reasonOf(err error) string {
	var decodeErr *protocol.DecodeError
	if errors.As(err, &decodeErr) {
		return decodeErr.Reason
	}
	return ""
}

// TestDecodeTelemetryAcceptsTheContractExample is the anchor: the payload printed
// in docs/device-protocol.md must decode. If this test fails, the document and
// the codec have drifted apart.
func TestDecodeTelemetryAcceptsTheContractExample(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": 1,
		"messageType": "telemetry",
		"deviceId": "MCU001",
		"bootId": "9f3ac21b",
		"sequence": 42,
		"timestamp": 1790246400000,
		"uptimeMs": 125000,
		"temperatureC": 28.0,
		"humidityRh": 61.0,
		"gasAdcRaw": 1350,
		"gasAdcFiltered": 1328,
		"gasPpm": 25.0,
		"gasCalibrated": false,
		"localAlarm": true,
		"alarmCauses": ["gas_high"],
		"network": "online",
		"thresholdVersion": 3,
		"sensorFault": false
	}`)

	sample, err := protocol.DecodeTelemetry(raw, "", receivedAt)
	if err != nil {
		t.Fatalf("the documented example was rejected: %v", err)
	}
	if sample.DeviceID != "MCU001" || sample.BootID != "9f3ac21b" || sample.Sequence != 42 {
		t.Fatalf("identity decoded as %+v", sample)
	}
	if sample.TemperatureC != 28 || sample.HumidityRh != 61 {
		t.Fatalf("readings decoded as temperature %v humidity %v", sample.TemperatureC, sample.HumidityRh)
	}
	if sample.GasAdcRaw != 1350 || sample.GasAdcFiltered != 1328 {
		t.Fatalf("gas ADC decoded as raw %d filtered %d", sample.GasAdcRaw, sample.GasAdcFiltered)
	}
	if !sample.LocalAlarm {
		t.Fatalf("localAlarm decoded as %v", sample.LocalAlarm)
	}
	if len(sample.AlarmCauses) != 1 || sample.AlarmCauses[0] != domain.AlarmGasHigh {
		t.Fatalf("alarm causes decoded as %v", sample.AlarmCauses)
	}
	// receivedAt is the backend's own clock, never the payload's.
	if !sample.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("receivedAt = %s, want the server time %s", sample.ReceivedAt, receivedAt)
	}
	if err := sample.Validate(); err != nil {
		t.Fatalf("the decoded sample does not satisfy the domain rules: %v", err)
	}
}

// TestDecodeTelemetryRejectsImplausibleDeviceClock pins the future bound: a
// device whose clock runs ahead is a defect to report, not a reading to store,
// because it would place real samples at the end of every time-ordered query.
func TestDecodeTelemetryRejectsImplausibleDeviceClock(t *testing.T) {
	raw := telemetryJSON(func(fields map[string]any) {
		fields["timestamp"] = receivedAt.Add(25 * time.Hour).UnixMilli()
	})
	if _, err := protocol.DecodeTelemetry(raw, "", receivedAt); reasonOf(err) != protocol.ReasonOutOfRange {
		t.Fatalf("a clock 25 hours ahead reported %v, want %s", err, protocol.ReasonOutOfRange)
	}

	// A clock slightly ahead of the server is tolerated, because ordinary network
	// and scheduling delays make an exact match impossible.
	raw = telemetryJSON(func(fields map[string]any) {
		fields["timestamp"] = receivedAt.Add(2 * time.Minute).UnixMilli()
	})
	if _, err := protocol.DecodeTelemetry(raw, "", receivedAt); err != nil {
		t.Fatalf("a clock two minutes ahead was rejected: %v", err)
	}
}

// TestDecodeTelemetryAcceptsNullTimestamp covers a device whose clock is not yet
// synced, which the contract expresses as an explicit null rather than an epoch
// value.
func TestDecodeTelemetryAcceptsNullTimestamp(t *testing.T) {
	sample, err := protocol.DecodeTelemetry(telemetryJSON(func(fields map[string]any) {
		fields["timestamp"] = nil
	}), "", receivedAt)
	if err != nil {
		t.Fatalf("a null timestamp was rejected: %v", err)
	}
	if sample.Timestamp != nil {
		t.Fatalf("timestamp decoded as %s, want nil", sample.Timestamp)
	}
	if !sample.EventTime().Equal(receivedAt) {
		t.Fatalf("event time = %s, want the receive time", sample.EventTime())
	}
}

// TestDecodeTelemetryRejections covers every rejection reason the ingress path
// can report.
func TestDecodeTelemetryRejections(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		raw    string
		reason string
	}{
		"unknown field": {
			mutate: func(f map[string]any) { f["surprise"] = 1 },
			reason: protocol.ReasonUnknownField,
		},
		"missing bootId": {
			mutate: func(f map[string]any) { delete(f, "bootId") },
			reason: protocol.ReasonMissingField,
		},
		"missing deviceId": {
			mutate: func(f map[string]any) { delete(f, "deviceId") },
			reason: protocol.ReasonMissingField,
		},
		"missing sequence": {
			mutate: func(f map[string]any) { delete(f, "sequence") },
			reason: protocol.ReasonMissingField,
		},
		"missing alarmCauses": {
			mutate: func(f map[string]any) { delete(f, "alarmCauses") },
			reason: protocol.ReasonMissingField,
		},
		"unsupported schema version": {
			mutate: func(f map[string]any) { f["schemaVersion"] = 2 },
			reason: protocol.ReasonSchemaUnsupported,
		},
		"missing schema version": {
			mutate: func(f map[string]any) { delete(f, "schemaVersion") },
			reason: protocol.ReasonMissingField,
		},
		"wrong message type": {
			mutate: func(f map[string]any) { f["messageType"] = "command_ack" },
			reason: protocol.ReasonBadRequestType,
		},
		"device id with a space": {
			mutate: func(f map[string]any) { f["deviceId"] = "MCU 001" },
			reason: protocol.ReasonOutOfRange,
		},
		"gas ADC above range": {
			mutate: func(f map[string]any) { f["gasAdcRaw"] = 5000 },
			reason: protocol.ReasonOutOfRange,
		},
		"temperature above range": {
			mutate: func(f map[string]any) { f["temperatureC"] = 200 },
			reason: protocol.ReasonOutOfRange,
		},
		"humidity above range": {
			mutate: func(f map[string]any) { f["humidityRh"] = 150 },
			reason: protocol.ReasonOutOfRange,
		},
		"unknown network state": {
			mutate: func(f map[string]any) { f["network"] = "sporadic" },
			reason: protocol.ReasonOutOfRange,
		},
		"zero threshold version": {
			mutate: func(f map[string]any) { f["thresholdVersion"] = 0 },
			reason: protocol.ReasonOutOfRange,
		},
		"unknown alarm cause": {
			mutate: func(f map[string]any) { f["alarmCauses"] = []string{"meltdown"} },
			reason: protocol.ReasonOutOfRange,
		},
		"timestamp before the plausible window": {
			mutate: func(f map[string]any) { f["timestamp"] = 0 },
			reason: protocol.ReasonOutOfRange,
		},
		"timestamp far in the future": {
			mutate: func(f map[string]any) {
				f["timestamp"] = receivedAt.Add(72 * time.Hour).UnixMilli()
			},
			reason: protocol.ReasonOutOfRange,
		},
		"sequence as a string": {
			mutate: func(f map[string]any) { f["sequence"] = "42" },
			reason: protocol.ReasonOutOfRange,
		},
		"alarm flag without a cause": {
			mutate: func(f map[string]any) {
				f["localAlarm"] = true
				f["alarmCauses"] = []string{}
			},
			reason: protocol.ReasonOutOfRange,
		},
		"malformed JSON": {
			raw:    `{"schemaVersion": 1,`,
			reason: protocol.ReasonMalformedJSON,
		},
		"JSON array instead of an object": {
			raw:    `[1,2,3]`,
			reason: protocol.ReasonMalformedJSON,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			raw := []byte(testCase.raw)
			if testCase.raw == "" {
				raw = telemetryJSON(testCase.mutate)
			}
			_, err := protocol.DecodeTelemetry(raw, "", receivedAt)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if got := reasonOf(err); got != testCase.reason {
				t.Fatalf("%s reported reason %q, want %q (%v)", name, got, testCase.reason, err)
			}
		})
	}
}

// TestDecodeTelemetryRejectsOversizedPayload verifies the size bound, which is
// what stops a hostile publisher from allocating unbounded memory.
func TestDecodeTelemetryRejectsOversizedPayload(t *testing.T) {
	oversized := []byte(`{"schemax": "` + strings.Repeat("A", protocol.MaxPayloadBytes) + `"}`)
	_, err := protocol.DecodeTelemetry(oversized, "", receivedAt)
	if reasonOf(err) != protocol.ReasonTooLarge {
		t.Fatalf("oversized payload reported %v, want %s", err, protocol.ReasonTooLarge)
	}
}

// TestDecodeTelemetryEnforcesTheDeviceAllowlist verifies that a configured
// allowlist rejects a well-formed payload from another device.
func TestDecodeTelemetryEnforcesTheDeviceAllowlist(t *testing.T) {
	if _, err := protocol.DecodeTelemetry(telemetryJSON(nil), "MCU002", receivedAt); reasonOf(err) != protocol.ReasonDeviceMismatch {
		t.Fatalf("a mismatched device reported %v, want %s", err, protocol.ReasonDeviceMismatch)
	}
	if _, err := protocol.DecodeTelemetry(telemetryJSON(nil), "MCU001", receivedAt); err != nil {
		t.Fatalf("the configured device was rejected: %v", err)
	}
}

// TestTelemetryRoundTrip verifies that encode and decode agree, so a captured
// sample can be replayed by test tooling.
func TestTelemetryRoundTrip(t *testing.T) {
	original, err := protocol.DecodeTelemetry(telemetryJSON(nil), "", receivedAt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := protocol.EncodeTelemetry(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	replayed, err := protocol.DecodeTelemetry(encoded, "", receivedAt)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if replayed.BootID != original.BootID || replayed.Sequence != original.Sequence {
		t.Fatalf("round trip changed the identity: %+v vs %+v", replayed, original)
	}
	if replayed.TemperatureC != original.TemperatureC || replayed.GasAdcFiltered != original.GasAdcFiltered {
		t.Fatalf("round trip changed the readings: %+v vs %+v", replayed, original)
	}
}

// TestDecodeCommandAckAcceptsTheContractExample verifies the acknowledgement
// payload printed in the device protocol document.
func TestDecodeCommandAckAcceptsTheContractExample(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": 1,
		"messageType": "command_ack",
		"deviceId": "MCU001",
		"bootId": "9f3ac21b",
		"sequence": 43,
		"timestamp": 1790246401000,
		"uptimeMs": 126000,
		"requestId": "01K5H0PN0M1N9NB8B7RBTVWT8P",
		"status": "applied",
		"thresholdVersion": 4,
		"errorCode": null
	}`)

	ack, err := protocol.DecodeCommandAck(raw, "", receivedAt)
	if err != nil {
		t.Fatalf("the documented acknowledgement was rejected: %v", err)
	}
	if ack.RequestID != "01K5H0PN0M1N9NB8B7RBTVWT8P" {
		t.Fatalf("requestId decoded as %q", ack.RequestID)
	}
	if ack.Status != domain.AckApplied {
		t.Fatalf("status decoded as %q, want applied", ack.Status)
	}
	if ack.ThresholdVersion == nil || *ack.ThresholdVersion != 4 {
		t.Fatalf("thresholdVersion decoded as %v", ack.ThresholdVersion)
	}
	if ack.ErrorCode != "" {
		t.Fatalf("errorCode decoded as %q, want empty", ack.ErrorCode)
	}
}

// TestDecodeCommandAckRejections covers the acknowledgement validation rules.
func TestDecodeCommandAckRejections(t *testing.T) {
	base := map[string]any{
		"schemaVersion":    1,
		"messageType":      "command_ack",
		"deviceId":         "MCU001",
		"bootId":           "9f3ac21b",
		"sequence":         43,
		"timestamp":        nil,
		"uptimeMs":         126000,
		"requestId":        "01REQ",
		"status":           "applied",
		"thresholdVersion": 4,
		"errorCode":        nil,
	}

	cases := map[string]struct {
		mutate func(map[string]any)
		reason string
	}{
		"unknown status": {
			mutate: func(f map[string]any) { f["status"] = "maybe" },
			reason: protocol.ReasonBadRequestType,
		},
		"rejected without an error code": {
			mutate: func(f map[string]any) { f["status"] = "rejected" },
			reason: protocol.ReasonOutOfRange,
		},
		"unknown error code": {
			mutate: func(f map[string]any) {
				f["status"] = "failed"
				f["errorCode"] = "because"
			},
			reason: protocol.ReasonOutOfRange,
		},
		"applied with an error code": {
			mutate: func(f map[string]any) { f["errorCode"] = "out_of_range" },
			reason: protocol.ReasonOutOfRange,
		},
		"missing requestId": {
			mutate: func(f map[string]any) { delete(f, "requestId") },
			reason: protocol.ReasonMissingField,
		},
		"missing errorCode key": {
			mutate: func(f map[string]any) { delete(f, "errorCode") },
			reason: protocol.ReasonMissingField,
		},
		"telemetry payload on the ack topic": {
			mutate: func(f map[string]any) { f["messageType"] = "telemetry" },
			reason: protocol.ReasonBadRequestType,
		},
		"unknown field": {
			mutate: func(f map[string]any) { f["extra"] = true },
			reason: protocol.ReasonUnknownField,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fields := cloneFields(base)
			testCase.mutate(fields)
			_, err := protocol.DecodeCommandAck(mustJSON(t, fields), "", receivedAt)
			if err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if got := reasonOf(err); got != testCase.reason {
				t.Fatalf("%s reported reason %q, want %q (%v)", name, got, testCase.reason, err)
			}
		})
	}

	// A rejection with a valid error code is the positive case for the table.
	fields := cloneFields(base)
	fields["status"] = "rejected"
	fields["errorCode"] = "flash_write_failed"
	ack, err := protocol.DecodeCommandAck(mustJSON(t, fields), "", receivedAt)
	if err != nil {
		t.Fatalf("a valid rejection was refused: %v", err)
	}
	if ack.Status != domain.AckRejected || ack.ErrorCode != "flash_write_failed" {
		t.Fatalf("decoded %+v", ack)
	}
}

// TestControlRoundTrip verifies that the backend's control encoder produces a
// payload the documented device-side decoder accepts.
func TestControlRoundTrip(t *testing.T) {
	version := 4
	thresholds := domain.Thresholds{TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 80}

	cases := map[string]domain.Command{
		"set_thresholds": {
			RequestID: "01REQ2", DeviceID: "MCU001", Type: domain.CommandSetThresholds,
			State: domain.CommandAccepted,
			Payload: domain.CommandPayload{
				Thresholds: &thresholds, ThresholdVersion: &version,
			},
			DesiredVersion: &version,
			AcceptedAt:     receivedAt, ExpiresAt: receivedAt.Add(time.Minute),
		},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			encoded, err := protocol.EncodeControl(command)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			decoded, err := protocol.DecodeControl(encoded, receivedAt)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if decoded.Type != command.Type || decoded.RequestID != command.RequestID {
				t.Fatalf("round trip changed the command: %+v", decoded)
			}
			if !decoded.ExpiresAt.Equal(command.ExpiresAt) {
				t.Fatalf("expiresAt changed from %s to %s", command.ExpiresAt, decoded.ExpiresAt)
			}
			if decoded.Payload.Thresholds == nil || !decoded.Payload.Thresholds.Equal(thresholds) {
				t.Fatalf("thresholds decoded as %+v", decoded.Payload.Thresholds)
			}
		})
	}
}

// TestEncodeControlRejectsMalformedCommands verifies that a command whose
// payload does not match its type cannot reach a device.
func TestEncodeControlRejectsMalformedCommands(t *testing.T) {
	version := 4
	thresholds := domain.Thresholds{TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 80}

	cases := map[string]domain.Command{
		"thresholds without a version": {
			RequestID: "02", DeviceID: "MCU001", Type: domain.CommandSetThresholds,
			State:   domain.CommandAccepted,
			Payload: domain.CommandPayload{Thresholds: &thresholds},
		},
		"thresholds outside range": {
			RequestID: "03", DeviceID: "MCU001", Type: domain.CommandSetThresholds,
			State:   domain.CommandAccepted,
			Payload: domain.CommandPayload{Thresholds: &domain.Thresholds{GasHighPpm: 5000}, ThresholdVersion: &version},
		},
		"empty request id": {
			RequestID: "", DeviceID: "MCU001", Type: domain.CommandSetThresholds,
			State: domain.CommandAccepted,
			Payload: domain.CommandPayload{
				Thresholds: &thresholds, ThresholdVersion: &version,
			},
		},
		"unknown type": {
			RequestID: "05", DeviceID: "MCU001", Type: "set_everything",
			State: domain.CommandAccepted,
		},
		"retired set_mute type": {
			RequestID: "06", DeviceID: "MCU001", Type: "set_mute",
			State: domain.CommandAccepted,
		},
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := protocol.EncodeControl(command); err == nil {
				t.Fatalf("%s was encoded", name)
			}
		})
	}
}

// TestDecodeControlRejections covers the device-side decoder's checks, which the
// simulator and HIL tooling rely on.
func TestDecodeControlRejections(t *testing.T) {
	valid := map[string]any{
		"schemaVersion": 1,
		"messageType":   "control",
		"deviceId":      "MCU001",
		"requestId":     "01REQ",
		"issuedAt":      receivedAt.UnixMilli(),
		"expiresAt":     receivedAt.Add(time.Minute).UnixMilli(),
		"type":          "set_thresholds",
		"payload": map[string]any{
			"thresholdVersion": 2, "temperatureHighC": 30, "humidityHighRh": 80, "gasHighPpm": 80,
		},
	}

	cases := map[string]struct {
		mutate func(map[string]any)
	}{
		"missing payload": {mutate: func(f map[string]any) { delete(f, "payload") }},
		"missing thresholds fields": {
			mutate: func(f map[string]any) { f["payload"] = map[string]any{} },
		},
		"unknown type":        {mutate: func(f map[string]any) { f["type"] = "set_everything" }},
		"unknown field":       {mutate: func(f map[string]any) { f["extra"] = 1 }},
		"unsupported version": {mutate: func(f map[string]any) { f["schemaVersion"] = 9 }},
		"thresholds out of range": {
			mutate: func(f map[string]any) {
				f["payload"] = map[string]any{
					"thresholdVersion": 2, "temperatureHighC": 30,
					"humidityHighRh": 80, "gasHighPpm": 5000,
				}
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fields := cloneFields(valid)
			testCase.mutate(fields)
			if _, err := protocol.DecodeControl(mustJSON(t, fields), receivedAt); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}

	// The retired remote-mute command must be rejected as bad_request_type.
	t.Run("retired set_mute is bad_request_type", func(t *testing.T) {
		fields := cloneFields(valid)
		fields["type"] = "set_mute"
		fields["payload"] = map[string]any{"muted": true}
		_, err := protocol.DecodeControl(mustJSON(t, fields), receivedAt)
		if err == nil {
			t.Fatal("set_mute was accepted")
		}
		if reason := reasonOf(err); reason != protocol.ReasonBadRequestType {
			t.Fatalf("set_mute was rejected as %q, want bad_request_type", reason)
		}
	})

	// The positive case: a valid set_thresholds command.
	fields := cloneFields(valid)
	command, err := protocol.DecodeControl(mustJSON(t, fields), receivedAt)
	if err != nil {
		t.Fatalf("a valid thresholds command was refused: %v", err)
	}
	if command.Payload.Thresholds == nil || command.Payload.ThresholdVersion == nil {
		t.Fatalf("decoded %+v", command.Payload)
	}
}

// TestTopicsAreFrozen pins the three topic strings, because a change here is a
// broker ACL change as well.
func TestTopicsAreFrozen(t *testing.T) {
	if protocol.TopicTelemetry != "device/telemetry" {
		t.Fatalf("TopicTelemetry = %q", protocol.TopicTelemetry)
	}
	if protocol.TopicCommandAck != "device/command-ack" {
		t.Fatalf("TopicCommandAck = %q", protocol.TopicCommandAck)
	}
	if protocol.TopicControl != "device/control" {
		t.Fatalf("TopicControl = %q", protocol.TopicControl)
	}
}

// telemetryJSON renders the documented telemetry payload with optional changes.
func telemetryJSON(mutate func(map[string]any)) []byte {
	fields := map[string]any{
		"schemaVersion":    1,
		"messageType":      "telemetry",
		"deviceId":         "MCU001",
		"bootId":           "9f3ac21b",
		"sequence":         42,
		"timestamp":        receivedAt.UnixMilli(),
		"uptimeMs":         125000,
		"temperatureC":     28.0,
		"humidityRh":       61.0,
		"gasAdcRaw":        1350,
		"gasAdcFiltered":   1328,
		"gasPpm":           25.0,
		"gasCalibrated":    false,
		"localAlarm":       false,
		"alarmCauses":      []string{},
		"network":          "online",
		"thresholdVersion": 3,
		"sensorFault":      false,
	}
	if mutate != nil {
		mutate(fields)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		// The map holds only JSON-safe primitives, so this cannot fail.
		panic(err)
	}
	return raw
}

// cloneFields copies a payload map so a case cannot affect another.
func cloneFields(fields map[string]any) map[string]any {
	clone := make(map[string]any, len(fields))
	for key, value := range fields {
		clone[key] = value
	}
	return clone
}

// mustJSON marshals a test payload, failing the test on an impossible error.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test payload: %v", err)
	}
	return raw
}

// boolPtr returns a pointer to value.
func boolPtr(value bool) *bool { return &value }
