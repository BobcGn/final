package domain

import (
	"fmt"
	"time"
)

// CommandType is the kind of control command sent to a device.
type CommandType string

// Frozen control command types. Remote mute (set_mute) was removed; the control
// topic only carries set_thresholds.
const (
	CommandSetThresholds CommandType = "set_thresholds"
)

// Valid reports whether t is a frozen command type.
func (t CommandType) Valid() bool {
	return t == CommandSetThresholds
}

// CommandState is the lifecycle state the backend records for a control command.
//
// The lifecycle spans two authorities. accepted and published are backend
// states; applied, rejected, expired, duplicate and failed are reported by the
// device over device/command-ack; timed_out and publish_failed are terminal
// backend outcomes when the device never answers or the broker refuses the
// publish. A REST 202 only ever means accepted, never applied.
type CommandState string

// Frozen command lifecycle states.
const (
	CommandAccepted      CommandState = "accepted"
	CommandPublished     CommandState = "published"
	CommandApplied       CommandState = "applied"
	CommandRejected      CommandState = "rejected"
	CommandExpired       CommandState = "expired"
	CommandDuplicate     CommandState = "duplicate"
	CommandFailed        CommandState = "failed"
	CommandTimedOut      CommandState = "timed_out"
	CommandPublishFailed CommandState = "publish_failed"
)

// Valid reports whether s is a frozen command state.
func (s CommandState) Valid() bool {
	switch s {
	case CommandAccepted, CommandPublished, CommandApplied, CommandRejected,
		CommandExpired, CommandDuplicate, CommandFailed, CommandTimedOut,
		CommandPublishFailed:
		return true
	default:
		return false
	}
}

// Terminal reports whether the state ends the command. Only terminal states may
// be reported to clients as a final result, and only terminal states stop the
// acknowledgement timeout.
func (s CommandState) Terminal() bool {
	switch s {
	case CommandApplied, CommandRejected, CommandExpired, CommandDuplicate,
		CommandFailed, CommandTimedOut, CommandPublishFailed:
		return true
	default:
		return false
	}
}

// AckStatus is the status a device reports for a command it received.
type AckStatus string

// Frozen device acknowledgement statuses.
const (
	AckApplied   AckStatus = "applied"
	AckRejected  AckStatus = "rejected"
	AckExpired   AckStatus = "expired"
	AckDuplicate AckStatus = "duplicate"
	AckFailed    AckStatus = "failed"
)

// Valid reports whether s is a frozen acknowledgement status.
func (s AckStatus) Valid() bool {
	switch s {
	case AckApplied, AckRejected, AckExpired, AckDuplicate, AckFailed:
		return true
	default:
		return false
	}
}

// State maps a device acknowledgement onto the backend command lifecycle state.
// The two vocabularies are kept separate on purpose: a device that answers
// "duplicate" is reporting that it already handled the request, which is not
// the same statement as the backend's own "applied".
func (s AckStatus) State() CommandState {
	return CommandState(s)
}

// RequiresErrorCode reports whether the acknowledgement must carry an errorCode.
// applied and duplicate are the only statuses that may omit it.
func (s AckStatus) RequiresErrorCode() bool {
	switch s {
	case AckRejected, AckFailed:
		return true
	default:
		return false
	}
}

// AckErrorCode enumerates the frozen device-side failure reasons.
type AckErrorCode string

// Frozen acknowledgement error codes.
const (
	AckErrSchemaUnsupported AckErrorCode = "schema_unsupported"
	AckErrDeviceMismatch    AckErrorCode = "device_mismatch"
	AckErrBadRequestType    AckErrorCode = "bad_request_type"
	AckErrOutOfRange        AckErrorCode = "out_of_range"
	AckErrStaleVersion      AckErrorCode = "stale_version"
	AckErrFlashWriteFailed  AckErrorCode = "flash_write_failed"
	AckErrFlashVerifyFailed AckErrorCode = "flash_verify_failed"
)

// Valid reports whether c is a frozen acknowledgement error code.
func (c AckErrorCode) Valid() bool {
	switch c {
	case AckErrSchemaUnsupported, AckErrDeviceMismatch, AckErrBadRequestType,
		AckErrOutOfRange, AckErrStaleVersion, AckErrFlashWriteFailed,
		AckErrFlashVerifyFailed:
		return true
	default:
		return false
	}
}

// CommandPayload carries the type-specific fields of a control command. Exactly
// one member is populated, selected by the owning Command's Type.
type CommandPayload struct {
	// Thresholds is set for set_thresholds.
	Thresholds *Thresholds
	// ThresholdVersion is the version the device must adopt for set_thresholds.
	ThresholdVersion *int
}

// Command is one control command and its recorded lifecycle.
type Command struct {
	RequestID string
	DeviceID  string
	Type      CommandType
	State     CommandState
	Payload   CommandPayload

	// IdempotencyKey is the client-supplied key that prevents duplicate
	// publication of the same logical command.
	IdempotencyKey string
	AcceptedAt     time.Time
	PublishedAt    *time.Time
	CompletedAt    *time.Time
	ExpiresAt      time.Time

	// DesiredVersion is the threshold version minted for set_thresholds.
	DesiredVersion *int
	// ConfirmedVersion is the version the device acknowledged.
	ConfirmedVersion *int
	// ErrorCode is the device-reported failure reason, empty when none applied.
	ErrorCode string

	// Actor records who issued the command, for the audit trail.
	Actor string
}

// Validate checks the command payload against its type. It returns an error
// wrapping ErrInvalidCommand.
func (c Command) Validate() error {
	if c.RequestID == "" {
		return fmt.Errorf("%w: requestId must not be empty", ErrInvalidCommand)
	}
	if err := ValidateDeviceID(c.DeviceID); err != nil {
		return err
	}
	if !c.Type.Valid() {
		return fmt.Errorf("%w: unknown type %q", ErrInvalidCommand, c.Type)
	}
	if !c.State.Valid() {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidCommand, c.State)
	}
	switch c.Type {
	case CommandSetThresholds:
		if c.Payload.Thresholds == nil {
			return fmt.Errorf("%w: %s requires payload.thresholds", ErrInvalidCommand, c.Type)
		}
		if err := c.Payload.Thresholds.Validate(); err != nil {
			return err
		}
		if c.Payload.ThresholdVersion == nil || *c.Payload.ThresholdVersion < InitialThresholdVersion {
			return fmt.Errorf("%w: %s requires thresholdVersion >= %d", ErrInvalidCommand, c.Type, InitialThresholdVersion)
		}
	}
	return nil
}

// CanTransition reports whether moving from state from to state to is allowed.
// The function is the single source of truth for the state machine, so the
// command service and its tests cannot drift apart.
func CanTransition(from, to CommandState) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	// Terminal states are final: a late or duplicated acknowledgement must not
	// reopen a completed command.
	if from.Terminal() {
		return false
	}
	switch from {
	case CommandAccepted:
		switch to {
		case CommandPublished, CommandPublishFailed, CommandTimedOut:
			return true
		default:
			return false
		}
	case CommandPublished:
		// A published command can only end: either the device answers, or the
		// backend gives up on it. publish_failed is deliberately unreachable from
		// here, because the publish already succeeded and rewriting that would
		// record a failure that never happened.
		switch to {
		case CommandApplied, CommandRejected, CommandExpired, CommandDuplicate,
			CommandFailed, CommandTimedOut:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// CommandPatch describes the result of processing a device acknowledgement. It
// is applied to a stored Command by the store inside a single transaction.
type CommandPatch struct {
	State            CommandState
	CompletedAt      time.Time
	ConfirmedVersion *int
	ErrorCode        string
}

// ApplyAck returns the patch that a device acknowledgement produces, or an error
// if the acknowledgement itself is malformed. It performs no I/O so that the
// acknowledgement rules stay unit-testable.
func ApplyAck(ack AckStatus, errorCode string, confirmedVersion *int, at time.Time) (CommandPatch, error) {
	if !ack.Valid() {
		return CommandPatch{}, fmt.Errorf("%w: unknown ack status %q", ErrInvalidCommand, ack)
	}
	if ack.RequiresErrorCode() && errorCode == "" {
		return CommandPatch{}, fmt.Errorf("%w: ack status %q requires errorCode", ErrInvalidCommand, ack)
	}
	if ack.RequiresErrorCode() && !AckErrorCode(errorCode).Valid() {
		return CommandPatch{}, fmt.Errorf("%w: unknown ack error code %q", ErrInvalidCommand, errorCode)
	}
	if !ack.RequiresErrorCode() && errorCode != "" {
		// An error code on a success status would be a contradictory report; the
		// device contract forbids it, so the backend refuses to store it.
		return CommandPatch{}, fmt.Errorf("%w: ack status %q must not carry errorCode", ErrInvalidCommand, ack)
	}
	if ack != AckApplied && ack != AckDuplicate {
		// rejected, expired and failed never changed the device configuration, so
		// the confirmed version must stay unset instead of echoing the request.
		confirmedVersion = nil
	}
	return CommandPatch{
		State:            ack.State(),
		CompletedAt:      at,
		ConfirmedVersion: confirmedVersion,
		ErrorCode:        errorCode,
	}, nil
}
