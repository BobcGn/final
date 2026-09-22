// Package ingest turns inbound MQTT messages into stored state and realtime
// events.
//
// It owns the ordering rules that make the rest of the system sound: a sample is
// stored once, late samples feed the trend but never drive a state change,
// duplicates never refresh liveness, and an alert transition is persisted before
// it is published. Every step that can fail is reported to the caller as a typed
// error so the MQTT layer can log and count it without tearing down the session.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/BobcGn/final/backend/internal/alert"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
	"github.com/BobcGn/final/backend/internal/liveness"
	"github.com/BobcGn/final/backend/internal/mqtt"
	"github.com/BobcGn/final/backend/internal/protocol"
	"github.com/BobcGn/final/backend/internal/store"
)

// ErrDeviceNotAllowed reports a device outside the configured allowlist. It is
// separate from a decode failure because it is an authorization decision, not a
// malformed message.
var ErrDeviceNotAllowed = errors.New("ingest: device is not in the allowlist")

// Deps are the collaborators of a Service.
type Deps struct {
	// Store persists telemetry, alerts, device state and commands.
	Store store.Store
	// Engine evaluates the composite fire warning rule.
	Engine *alert.Engine
	// Tracker decides device connectivity from telemetry arrivals.
	Tracker *liveness.Tracker
	// Sink receives realtime events. A nil sink disables publishing, which keeps
	// the ingest path usable in tests that only assert persisted state.
	Sink events.Sink
	// AckHandler applies validated command acknowledgements. The command state
	// machine lives in the command service; ingest only performs the shared
	// decoding, allowlist and diagnostics around it. A nil handler records the
	// acknowledgement without applying it.
	AckHandler AckHandler
	// Logger receives structured diagnostics.
	Logger *slog.Logger
	// Now overrides the clock. Nil uses time.Now.
	Now func() time.Time
	// AllowedDevices, when non-empty, restricts accepted device identifiers. MQTT
	// topics are fixed for phase 1, so the topic cannot express which device is
	// allowed to publish; this allowlist is the application-level substitute
	// until per-device credentials and ACLs are in place.
	AllowedDevices []string
}

// Stats is a snapshot of ingest counters. Rejected counts are keyed by the
// stable protocol reject reason so that a spike can be attributed to one cause.
type Stats struct {
	Accepted    uint64
	Duplicates  uint64
	LateSamples uint64
	Rejected    map[string]uint64
}

// Service processes inbound device traffic. It is safe for concurrent use.
type Service struct {
	store   store.Store
	engine  *alert.Engine
	tracker *liveness.Tracker
	sink    events.Sink
	acks    AckHandler
	logger  *slog.Logger
	now     func() time.Time

	allowed map[string]struct{}

	mu         sync.Mutex
	accepted   uint64
	duplicates uint64
	late       uint64
	rejected   map[string]uint64
}

// New validates the dependencies and returns a Service.
func New(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("ingest: store is required")
	}
	if deps.Engine == nil {
		return nil, errors.New("ingest: alert engine is required")
	}
	if deps.Tracker == nil {
		return nil, errors.New("ingest: liveness tracker is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	service := &Service{
		store:    deps.Store,
		engine:   deps.Engine,
		tracker:  deps.Tracker,
		sink:     deps.Sink,
		acks:     deps.AckHandler,
		logger:   logger,
		now:      now,
		rejected: make(map[string]uint64),
	}
	if len(deps.AllowedDevices) > 0 {
		service.allowed = make(map[string]struct{}, len(deps.AllowedDevices))
		for _, deviceID := range deps.AllowedDevices {
			if err := domain.ValidateDeviceID(deviceID); err != nil {
				return nil, fmt.Errorf("ingest: allowlist entry is invalid: %w", err)
			}
			service.allowed[deviceID] = struct{}{}
		}
	}
	return service, nil
}

// Stats returns a snapshot of the service counters.
func (s *Service) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	rejected := make(map[string]uint64, len(s.rejected))
	for reason, count := range s.rejected {
		rejected[reason] = count
	}
	return Stats{
		Accepted:    s.accepted,
		Duplicates:  s.duplicates,
		LateSamples: s.late,
		Rejected:    rejected,
	}
}

// HandleMessage dispatches one inbound MQTT message by topic. An unknown topic
// is reported but not treated as fatal: the broker ACL is expected to keep
// unexpected topics out, and a subscription change must not take the process
// down.
func (s *Service) HandleMessage(ctx context.Context, message mqtt.Message) error {
	switch message.Topic {
	case protocol.TopicTelemetry:
		return s.HandleTelemetry(ctx, message.Payload)
	case protocol.TopicCommandAck:
		return s.HandleCommandAck(ctx, message.Payload)
	default:
		return fmt.Errorf("ingest: no handler for topic %q", message.Topic)
	}
}

// HandleTelemetry validates, stores and evaluates one telemetry payload.
//
// A malformed payload is counted and returned; the caller logs it and keeps the
// session alive, because one bad message must not stop the ingress path.
func (s *Service) HandleTelemetry(ctx context.Context, raw []byte) error {
	receivedAt := s.now().UTC()
	sample, err := protocol.DecodeTelemetry(raw, "", receivedAt)
	if err != nil {
		s.recordReject(err)
		return err
	}
	if !s.deviceAllowed(sample.DeviceID) {
		s.recordRejectReason(protocol.ReasonDeviceNotAllowed)
		return fmt.Errorf("%w: %s", ErrDeviceNotAllowed, sample.DeviceID)
	}

	inserted, err := s.store.InsertTelemetry(ctx, sample)
	if err != nil {
		return fmt.Errorf("ingest: store telemetry: %w", err)
	}
	if !inserted {
		// A QoS 1 redelivery is expected. Counting it and returning early keeps
		// the alert windows and liveness untouched, so a repeated message cannot
		// extend a device's apparent uptime or change an alert decision.
		s.mu.Lock()
		s.duplicates++
		s.mu.Unlock()
		return nil
	}
	s.mu.Lock()
	s.accepted++
	s.mu.Unlock()

	state, err := s.deviceState(ctx, sample.DeviceID)
	if err != nil {
		return err
	}
	if !isNewest(state, sample) {
		// Late or out-of-order: keep the trend complete without letting stale
		// data move the alert state or overwrite the current device snapshot.
		s.engine.Ingest(sample)
		s.mu.Lock()
		s.late++
		s.mu.Unlock()
		return nil
	}

	transition, connectivityChanged := s.tracker.Observe(sample.DeviceID, sample.ReceivedAt)
	if connectivityChanged {
		state.Connectivity = transition.Connectivity
	}
	state.LastSeenAt = sample.ReceivedAt

	alertEvent, alertChanged, err := s.applyAlertRules(ctx, sample, &state)
	if err != nil {
		return err
	}
	if err := s.applyThresholdConfirmation(ctx, sample, &state); err != nil {
		return err
	}

	state.LastBootID = sample.BootID
	state.LastSequence = sample.Sequence
	state.LocalAlarm = sample.LocalAlarm
	state.SensorFault = sample.SensorFault
	state.GasCalibrated = sample.GasCalibrated
	state.UpdatedAt = sample.ReceivedAt
	if err := s.store.UpsertDevice(ctx, state); err != nil {
		return fmt.Errorf("ingest: upsert device: %w", err)
	}

	if s.sink == nil {
		return nil
	}
	s.sink.PublishTelemetryUpdated(ctx, sample)
	if alertChanged && alertEvent != nil {
		s.sink.PublishAlertStateChanged(ctx, *alertEvent)
	}
	if connectivityChanged {
		s.sink.PublishDeviceStatusChanged(ctx, sample.DeviceID, events.DeviceStatusData{
			Connectivity: state.Connectivity,
			LastSeenAt:   state.LastSeenAt,
			AlarmState:   state.AlarmState,
		})
	}
	return nil
}

// AckHandler applies a validated command acknowledgement to the command state
// machine.
type AckHandler interface {
	// HandleAck applies one acknowledgement. It must be idempotent: MQTT QoS 1
	// redelivers messages, so the same acknowledgement can arrive more than once.
	HandleAck(ctx context.Context, ack protocol.CommandAck) error
}

// HandleCommandAck validates and applies a device acknowledgement. The command
// state machine itself lives in the command service; ingest performs the shared
// decoding, allowlist check and diagnostics around it.
//
// An acknowledgement for an unknown requestId is not an error here: the command
// may have expired and been pruned, or have been issued before this process
// started. The command service decides how to report that.
func (s *Service) HandleCommandAck(ctx context.Context, raw []byte) error {
	receivedAt := s.now().UTC()
	ack, err := protocol.DecodeCommandAck(raw, "", receivedAt)
	if err != nil {
		s.recordReject(err)
		return err
	}
	if !s.deviceAllowed(ack.DeviceID) {
		s.recordRejectReason(protocol.ReasonDeviceNotAllowed)
		return fmt.Errorf("%w: %s", ErrDeviceNotAllowed, ack.DeviceID)
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "command acknowledgement received",
		slog.String("device_id", ack.DeviceID),
		slog.String("request_id", ack.RequestID),
		slog.String("status", string(ack.Status)))

	if s.acks == nil {
		return nil
	}
	return s.acks.HandleAck(ctx, ack)
}

// applyAlertRules runs the composite rule and persists the resulting transition.
//
// state is a pointer because the alert bookkeeping has to reach the caller: the
// device snapshot is written once, after the thresholds and alert rules have had
// their say, and a copy would drop the new active alert id and leave the device
// permanently pointing at an episode that no longer exists.
func (s *Service) applyAlertRules(ctx context.Context, sample domain.Telemetry, state *store.DeviceState) (*domain.AlertEvent, bool, error) {
	var active *domain.AlertEvent
	if state.ActiveAlertID != "" {
		event, err := s.store.ActiveAlert(ctx, sample.DeviceID)
		switch {
		case err == nil:
			active = &event
		case errors.Is(err, store.ErrNotFound):
			// The cached pointer is stale; treat the device as having no open
			// alert rather than failing the whole sample.
			state.ActiveAlertID = ""
		default:
			return nil, false, fmt.Errorf("ingest: load active alert: %w", err)
		}
	}

	current := state.AlarmState
	if !current.Valid() {
		current = domain.AlertNormal
	}
	decision := s.engine.Evaluate(alert.Input{
		Sample:       sample,
		CurrentState: current,
		ActiveAlert:  active,
	})
	if !decision.Evaluated && !decision.Changed {
		return nil, false, nil
	}
	state.AlarmState = decision.State

	eventTime := sample.EventTime()
	switch {
	case decision.Opened:
		created, err := s.store.InsertAlert(ctx, domain.AlertEvent{
			DeviceID:  sample.DeviceID,
			State:     decision.State,
			StartedAt: eventTime,
			Evidence:  decision.Evidence,
		})
		if err != nil {
			return nil, false, fmt.Errorf("ingest: open alert: %w", err)
		}
		state.ActiveAlertID = created.ID
		return &created, true, nil

	case decision.Escalated && active != nil:
		updated, err := s.store.UpdateAlert(ctx, sample.DeviceID, active.ID, decision.State, decision.Evidence)
		if errors.Is(err, store.ErrNotFound) {
			state.ActiveAlertID = ""
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("ingest: advance alert: %w", err)
		}
		return &updated, true, nil

	case decision.Closed && active != nil:
		if err := s.store.CloseAlert(ctx, sample.DeviceID, active.ID, decision.State, eventTime); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, false, fmt.Errorf("ingest: close alert: %w", err)
		}
		closed := *active
		closed.State = decision.State
		ended := eventTime
		closed.EndedAt = &ended
		state.ActiveAlertID = ""
		return &closed, true, nil

	default:
		return nil, false, nil
	}
}

// applyThresholdConfirmation records the threshold version the device reports it
// has in force. Doing it here keeps GET /thresholds able to distinguish desired
// from confirmed even when the command acknowledgement was lost.
func (s *Service) applyThresholdConfirmation(ctx context.Context, sample domain.Telemetry, state *store.DeviceState) error {
	if sample.ThresholdVersion == state.ThresholdVersionConfirmed {
		return nil
	}
	if err := s.store.ConfirmThresholdVersion(ctx, sample.DeviceID, sample.ThresholdVersion, sample.ReceivedAt); err != nil {
		return fmt.Errorf("ingest: confirm threshold version: %w", err)
	}
	state.ThresholdVersionConfirmed = sample.ThresholdVersion
	if s.sink != nil {
		s.sink.PublishThresholdsConfirmed(ctx, sample.DeviceID, sample.ThresholdVersion)
	}
	return nil
}

// SweepOffline marks silent devices offline and returns the resulting events. It
// is meant to be called from a timer, because an offline device produces no
// message that could trigger the check.
func (s *Service) SweepOffline(ctx context.Context, now time.Time) ([]events.Envelope, error) {
	transitions := s.tracker.Sweep(now)
	envelopes := make([]events.Envelope, 0, len(transitions))
	for _, transition := range transitions {
		state, err := s.store.Device(ctx, transition.DeviceID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return envelopes, fmt.Errorf("ingest: load device for offline sweep: %w", err)
		}
		state.DeviceID = transition.DeviceID
		state.Connectivity = transition.Connectivity
		state.LastSeenAt = transition.LastSeenAt
		state.UpdatedAt = transition.Since
		if err := s.store.UpsertDevice(ctx, state); err != nil {
			return envelopes, fmt.Errorf("ingest: mark device offline: %w", err)
		}
		data := events.DeviceStatusData{
			Connectivity: transition.Connectivity,
			LastSeenAt:   transition.LastSeenAt,
			AlarmState:   state.AlarmState,
		}
		if s.sink != nil {
			s.sink.PublishDeviceStatusChanged(ctx, transition.DeviceID, data)
		}
		envelopes = append(envelopes, events.Envelope{
			Type:       events.TypeDeviceStatusChanged,
			DeviceID:   transition.DeviceID,
			OccurredAt: transition.Since,
			Data:       data,
		})
	}
	return envelopes, nil
}

// deviceState loads the cached device state, returning a provisioned zero state
// for a device seen for the first time. A first-time device is normal, not an
// error: the contract provisions devices on their first valid telemetry.
func (s *Service) deviceState(ctx context.Context, deviceID string) (store.DeviceState, error) {
	state, err := s.store.Device(ctx, deviceID)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.DeviceState{}, fmt.Errorf("ingest: load device: %w", err)
	}
	thresholds, err := s.store.Thresholds(ctx, deviceID)
	if err != nil {
		return store.DeviceState{}, fmt.Errorf("ingest: load thresholds: %w", err)
	}
	return store.DeviceState{
		DeviceID:                deviceID,
		Connectivity:            domain.ConnectivityUnknown,
		AlarmState:              domain.AlertNormal,
		ThresholdVersionDesired: thresholds.DesiredVersion,
	}, nil
}

// isNewest reports whether the sample is the newest one seen for the device.
// A different BootID means the device restarted, and the new boot is by
// definition the current one.
func isNewest(state store.DeviceState, sample domain.Telemetry) bool {
	switch {
	case state.LastBootID == "":
		return true
	case state.LastBootID != sample.BootID:
		return true
	default:
		return sample.Sequence >= state.LastSequence
	}
}

// deviceAllowed applies the optional allowlist.
func (s *Service) deviceAllowed(deviceID string) bool {
	if s.allowed == nil {
		return true
	}
	_, ok := s.allowed[deviceID]
	return ok
}

// recordReject counts a rejected payload by its stable reason.
func (s *Service) recordReject(err error) {
	var decodeErr *protocol.DecodeError
	if errors.As(err, &decodeErr) {
		s.recordRejectReason(decodeErr.Reason)
		return
	}
	s.recordRejectReason("unclassified")
}

// recordRejectReason increments one reject counter.
func (s *Service) recordRejectReason(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rejected[reason]++
}
