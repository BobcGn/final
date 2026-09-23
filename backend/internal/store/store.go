// Package store defines the persistence boundary of the backend and provides
// two implementations: an in-memory store for tests and local development, and
// a PostgreSQL store for deployments. Both are exercised by the same
// conformance suite in storetest so that the two cannot drift apart.
//
// The interface exists so that the broker and the database stay replaceable: no
// package above store imports a database driver.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
)

// Errors returned by every Store implementation. Callers classify failures with
// errors.Is rather than by matching driver-specific messages.
var (
	// ErrNotFound reports that the requested record does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict reports a uniqueness or version conflict.
	ErrConflict = errors.New("store: conflict")
)

// MaxPageLimit bounds how many rows one page may return, matching the frozen
// `limit` parameter maximum in docs/api/openapi.yaml.
const MaxPageLimit = 1000

// DefaultPageLimit is the page size used when the caller does not ask for one.
const DefaultPageLimit = 200

// MaxQuerySpan bounds a single time-range query. Longer ranges must use an
// aggregation or export path, so that one request cannot scan the whole table.
const MaxQuerySpan = 31 * 24 * time.Hour

// Order is the sort direction of a paginated query.
type Order string

// Frozen sort directions.
const (
	OrderAsc  Order = "asc"
	OrderDesc Order = "desc"
)

// Valid reports whether o is a frozen sort direction.
func (o Order) Valid() bool { return o == OrderAsc || o == OrderDesc }

// TimeRange is a half-open search window: From is inclusive, To is exclusive.
type TimeRange struct {
	From time.Time
	To   time.Time
}

// Validate checks the window and the maximum span.
func (r TimeRange) Validate() error {
	if r.To.Before(r.From) {
		return errors.New("store: range end precedes range start")
	}
	if r.To.Sub(r.From) > MaxQuerySpan {
		return errors.New("store: range exceeds the maximum query span")
	}
	return nil
}

// TelemetryQuery selects a page of telemetry samples.
type TelemetryQuery struct {
	DeviceID string
	Range    TimeRange
	Limit    int
	Order    Order
	// Cursor is an opaque token returned by the previous page. When set, Range
	// and Order must repeat the first page values; the store validates that
	// through the cursor contents instead of trusting the caller.
	Cursor string
}

// AlertQuery selects a page of alert events.
type AlertQuery struct {
	DeviceID string
	Range    TimeRange
	Limit    int
	Order    Order
	Cursor   string
	// State filters on a single alert state when non-empty.
	State domain.AlertState
	// ActiveOnly restricts the result to alerts that have not ended.
	ActiveOnly bool
}

// TelemetryPage is one page of telemetry samples.
type TelemetryPage struct {
	Items      []domain.Telemetry
	NextCursor string
}

// AlertPage is one page of alert events.
type AlertPage struct {
	Items      []domain.AlertEvent
	NextCursor string
}

// DeviceState is the cached, backend-computed view of one device. It is derived
// from telemetry and command traffic; the device never writes it directly.
type DeviceState struct {
	DeviceID   string
	LastSeenAt time.Time
	// BootID and Sequence of the newest accepted sample, used to reject samples
	// that arrive after a regression.
	LastBootID    string
	LastSequence  uint32
	Connectivity  domain.Connectivity
	AlarmState    domain.AlertState
	ActiveAlertID string
	LocalAlarm    bool
	SensorFault   bool
	GasCalibrated bool
	// ThresholdVersionConfirmed is the version the device reported over MQTT.
	ThresholdVersionConfirmed int
	ThresholdVersionDesired   int
	UpdatedAt                 time.Time
}

// ThresholdRecord pairs the desired thresholds with the device-confirmed version.
type ThresholdRecord struct {
	DeviceID         string
	Desired          domain.Thresholds
	DesiredVersion   int
	ConfirmedVersion *int
	// ConfirmationState is derived: confirmed when the device reports the
	// desired version, pending while it does not, timed_out once the owning
	// command has expired without confirmation.
	ConfirmationState ConfirmationState
	UpdatedAt         time.Time
}

// ConfirmationState summarises whether the device adopted the desired thresholds.
type ConfirmationState string

// Frozen confirmation states, matching docs/api/openapi.yaml.
const (
	ConfirmationConfirmed ConfirmationState = "confirmed"
	ConfirmationPending   ConfirmationState = "pending"
	ConfirmationRejected  ConfirmationState = "rejected"
	ConfirmationTimedOut  ConfirmationState = "timed_out"
)

// Store is the persistence boundary. Every method must be safe for concurrent
// use, and every method that mutates more than one record must do so atomically;
// a partially applied alert transition or a deduplicated sample that still
// updates the device cache would corrupt the evidence trail.
type Store interface {
	// InsertTelemetry stores one sample, keyed by (deviceID, bootID, sequence).
	// It returns inserted=false when the sample was already present, in which
	// case no other state may be modified by the caller's transaction.
	InsertTelemetry(ctx context.Context, sample domain.Telemetry) (inserted bool, err error)

	// LatestTelemetry returns the newest stored sample by event time.
	LatestTelemetry(ctx context.Context, deviceID string) (domain.Telemetry, error)

	// ListTelemetry returns one page in the requested order.
	ListTelemetry(ctx context.Context, query TelemetryQuery) (TelemetryPage, error)

	// InsertAlert stores a new alert event and returns it with its assigned id.
	InsertAlert(ctx context.Context, event domain.AlertEvent) (domain.AlertEvent, error)

	// UpdateAlert advances an open alert event to a new state and replaces its
	// evidence. It is used when a suspect condition is confirmed as a fire
	// warning inside the same episode, so that one episode stays one row.
	UpdateAlert(ctx context.Context, deviceID, alertID string, state domain.AlertState, evidence domain.AlertEvidence) (domain.AlertEvent, error)

	// CloseAlert marks an active alert as ended at the given time.
	CloseAlert(ctx context.Context, deviceID, alertID string, state domain.AlertState, endedAt time.Time) error

	// ActiveAlert returns the running alert for a device, or ErrNotFound.
	ActiveAlert(ctx context.Context, deviceID string) (domain.AlertEvent, error)

	// ListAlerts returns one page of alert events.
	ListAlerts(ctx context.Context, query AlertQuery) (AlertPage, error)

	// UpsertDevice writes the whole cached device state.
	UpsertDevice(ctx context.Context, state DeviceState) error

	// Device returns the cached state, or ErrNotFound when the device has never
	// produced a valid sample.
	Device(ctx context.Context, deviceID string) (DeviceState, error)

	// ListDevices returns every known device ordered by id.
	ListDevices(ctx context.Context) ([]DeviceState, error)

	// Thresholds returns the desired thresholds and confirmed version, falling
	// back to the compile-time defaults for a device with no stored record.
	Thresholds(ctx context.Context, deviceID string) (ThresholdRecord, error)

	// SetDesiredThresholds stores a new desired threshold set and version.
	SetDesiredThresholds(ctx context.Context, deviceID string, thresholds domain.Thresholds, version int, at time.Time) error

	// ConfirmThresholdVersion records the version a device reported.
	ConfirmThresholdVersion(ctx context.Context, deviceID string, version int, at time.Time) error

	// InsertCommand stores a newly accepted control command.
	//
	// For a set_thresholds command it must also advance the stored desired
	// threshold set and version to the command's, atomically. Splitting the two
	// writes would leave a window in which GET /thresholds reports a version
	// that no command ever carried, and a crash in that window would strand the
	// device on thresholds the backend does not believe it sent.
	InsertCommand(ctx context.Context, command domain.Command) error

	// Command returns one command by device and request id.
	Command(ctx context.Context, deviceID, requestID string) (domain.Command, error)

	// CommandByIdempotencyKey returns the command previously accepted for an
	// idempotency key, or ErrNotFound.
	CommandByIdempotencyKey(ctx context.Context, deviceID, key string) (domain.Command, error)

	// MarkCommandPublished records a successful broker publish.
	MarkCommandPublished(ctx context.Context, deviceID, requestID string, at time.Time) (domain.Command, error)

	// ApplyCommandPatch moves a command into a terminal state. It returns
	// ErrConflict when the command already reached a terminal state, so that a
	// late acknowledgement cannot reopen a finished command.
	ApplyCommandPatch(ctx context.Context, deviceID, requestID string, patch domain.CommandPatch) (domain.Command, error)

	// ExpireCommands marks every published command past its expiry as timed_out
	// and returns the commands it closed.
	ExpireCommands(ctx context.Context, now time.Time) ([]domain.Command, error)

	// PendingCommands returns commands that are accepted or published.
	PendingCommands(ctx context.Context) ([]domain.Command, error)
}

// NormalizeLimit clamps a requested page size into the allowed range.
func NormalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageLimit
	}
	if limit > MaxPageLimit {
		return MaxPageLimit
	}
	return limit
}

// NormalizeOrder defaults an unset or unknown order to ascending.
func NormalizeOrder(order Order) Order {
	if order == OrderDesc {
		return OrderDesc
	}
	return OrderAsc
}

// NewID returns a time-ordered identifier for an alert or a command. Sorting the
// identifiers as strings reproduces creation order, which is what keeps cursor
// pagination stable when several records share a timestamp.
func NewID(now time.Time) string {
	return newID(now)
}
