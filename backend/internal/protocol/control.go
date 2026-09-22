package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
)

// controlKeys is the frozen key set of the control payload.
var controlKeys = keySet(
	"schemaVersion", "messageType", "deviceId", "requestId", "issuedAt",
	"expiresAt", "type", "payload",
)

// thresholdPayload is the set_thresholds body.
type thresholdPayload struct {
	ThresholdVersion *int     `json:"thresholdVersion"`
	TemperatureHighC *float64 `json:"temperatureHighC"`
	HumidityHighRh   *float64 `json:"humidityHighRh"`
	GasHighPpm       *float64 `json:"gasHighPpm"`
}

// controlPayload mirrors the wire format of device/control.
type controlPayload struct {
	SchemaVersion *int            `json:"schemaVersion"`
	MessageType   string          `json:"messageType"`
	DeviceID      string          `json:"deviceId"`
	RequestID     string          `json:"requestId"`
	IssuedAt      *int64          `json:"issuedAt"`
	ExpiresAt     *int64          `json:"expiresAt"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
}

// EncodeControl renders a stored command as the frozen device/control payload.
// It refuses to encode a command whose payload does not match its type, so a
// malformed command cannot reach a device.
func EncodeControl(cmd domain.Command) ([]byte, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	var body any
	switch cmd.Type {
	case domain.CommandSetThresholds:
		thresholds := cmd.Payload.Thresholds
		body = thresholdPayload{
			ThresholdVersion: cmd.Payload.ThresholdVersion,
			TemperatureHighC: &thresholds.TemperatureHighC,
			HumidityHighRh:   &thresholds.HumidityHighRh,
			GasHighPpm:       &thresholds.GasHighPpm,
		}
	default:
		return nil, fmt.Errorf("%w: unknown type %q", domain.ErrInvalidCommand, cmd.Type)
	}

	encodedBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode %s payload: %w", cmd.Type, err)
	}
	// issuedAt is the moment the backend took responsibility for the command,
	// which is AcceptedAt. publishedAt would drift on a retry and would make the
	// device-side expiry window depend on broker latency.
	issuedAt := cmd.AcceptedAt.UnixMilli()
	expiresAt := cmd.ExpiresAt.UnixMilli()
	wire := controlPayload{
		SchemaVersion: intPtr(domain.SchemaVersion),
		MessageType:   "control",
		DeviceID:      cmd.DeviceID,
		RequestID:     cmd.RequestID,
		IssuedAt:      &issuedAt,
		ExpiresAt:     &expiresAt,
		Type:          string(cmd.Type),
		Payload:       encodedBody,
	}
	return json.Marshal(wire)
}

// DecodeControl parses a device/control payload back into a domain command. The
// backend never receives this topic, so the decoder exists for the device
// simulator used by integration tests and HIL runs; it applies the same strict
// field and range checks the firmware must apply.
func DecodeControl(raw []byte, receivedAt time.Time) (domain.Command, error) {
	var payload controlPayload
	if _, err := decodeStrict(raw, controlKeys, &payload); err != nil {
		return domain.Command{}, err
	}
	if payload.SchemaVersion == nil || payload.IssuedAt == nil || payload.ExpiresAt == nil {
		return domain.Command{}, newDecodeError(ReasonMissingField, "schemaVersion/issuedAt/expiresAt", errMissing)
	}
	if err := checkSchemaVersion(*payload.SchemaVersion, payload.MessageType, "control"); err != nil {
		return domain.Command{}, err
	}
	if payload.RequestID == "" || payload.DeviceID == "" {
		return domain.Command{}, newDecodeError(ReasonMissingField, "requestId/deviceId", errMissing)
	}

	cmd := domain.Command{
		RequestID:  payload.RequestID,
		DeviceID:   payload.DeviceID,
		Type:       domain.CommandType(payload.Type),
		State:      domain.CommandAccepted,
		AcceptedAt: time.UnixMilli(*payload.IssuedAt).UTC(),
		ExpiresAt:  time.UnixMilli(*payload.ExpiresAt).UTC(),
	}
	if len(payload.Payload) == 0 {
		return domain.Command{}, newDecodeError(ReasonMissingField, "payload", errMissing)
	}

	switch cmd.Type {
	case domain.CommandSetThresholds:
		var body thresholdPayload
		if _, err := decodeStrict(payload.Payload, keySet("thresholdVersion", "temperatureHighC", "humidityHighRh", "gasHighPpm"), &body); err != nil {
			return domain.Command{}, err
		}
		if body.ThresholdVersion == nil || body.TemperatureHighC == nil || body.HumidityHighRh == nil || body.GasHighPpm == nil {
			return domain.Command{}, newDecodeError(ReasonMissingField, "payload", errMissing)
		}
		thresholds := domain.Thresholds{
			TemperatureHighC: *body.TemperatureHighC,
			HumidityHighRh:   *body.HumidityHighRh,
			GasHighPpm:       *body.GasHighPpm,
		}
		cmd.Payload.Thresholds = &thresholds
		cmd.Payload.ThresholdVersion = body.ThresholdVersion
		cmd.DesiredVersion = body.ThresholdVersion
	default:
		// set_mute was removed with the remote-mute capability. Any other type is
		// likewise not a frozen control command; both are rejected as
		// bad_request_type so the device can answer with the frozen error code.
		return domain.Command{}, newDecodeError(ReasonBadRequestType, "type",
			fmt.Errorf("type %q is not a frozen control command", payload.Type))
	}

	if err := cmd.Validate(); err != nil {
		return domain.Command{}, newDecodeError(ReasonOutOfRange, "", err)
	}
	return cmd, nil
}
