// Package command owns the control-command lifecycle: validating a request,
// minting the threshold version, publishing to the broker, and folding the
// device acknowledgement back into the recorded state.
//
// Two rules shape the whole package. First, an HTTP 202 means "accepted for
// publication" and never "the device did it" — the only thing that moves a
// command to applied is a device acknowledgement. Second, a command that was
// never applied must not be silently retried at reconnect time: every command
// carries an expiry, and an expired command is reported as timed_out instead of
// being re-published.
package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
	"github.com/BobcGn/final/backend/internal/protocol"
	"github.com/BobcGn/final/backend/internal/store"
)

// Errors returned by the service. The HTTP layer maps them onto the frozen
// error codes in docs/api/openapi.yaml.
var (
	// ErrIdempotencyConflict reports a reused idempotency key carrying a
	// different payload. It maps to 409 version_conflict.
	ErrIdempotencyConflict = errors.New("command: idempotency key reused with a different payload")
	// ErrPublishFailed reports that the broker refused or never acknowledged the
	// command. It maps to 503 broker_unavailable.
	ErrPublishFailed = errors.New("command: could not publish to the broker")
)

// DefaultTTL is how long a command stays valid. It matches the 60-second window
// in docs/device-protocol.md §4.4.
const DefaultTTL = 60 * time.Second

// Publisher sends a message to the broker.
type Publisher interface {
	// Publish sends payload to topic. It must return an error when the broker
	// did not take responsibility for the message, so the caller can record a
	// publish failure instead of assuming delivery.
	Publish(ctx context.Context, topic string, payload []byte, qos byte) error
}

// Deps are the collaborators of a Service.
type Deps struct {
	Store     store.Store
	Publisher Publisher
	Sink      events.Sink
	Logger    *slog.Logger
	Now       func() time.Time
	TTL       time.Duration
}

// Service records and publishes control commands. It is safe for concurrent use.
type Service struct {
	store     store.Store
	publisher Publisher
	sink      events.Sink
	logger    *slog.Logger
	now       func() time.Time
	ttl       time.Duration
}

// New validates the dependencies and returns a Service.
func New(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("command: store is required")
	}
	if deps.Publisher == nil {
		return nil, errors.New("command: publisher is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	ttl := deps.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{
		store:     deps.Store,
		publisher: deps.Publisher,
		sink:      deps.Sink,
		logger:    logger,
		now:       now,
		ttl:       ttl,
	}, nil
}

// TTL returns the command validity window.
func (s *Service) TTL() time.Duration { return s.ttl }

// RequestThresholdUpdate validates a threshold request, mints the next version
// and publishes a set_thresholds command. It returns the accepted command; the
// caller must still wait for the device acknowledgement.
func (s *Service) RequestThresholdUpdate(ctx context.Context, deviceID string, desired domain.Thresholds, idempotencyKey, actor string) (domain.Command, error) {
	if err := domain.ValidateDeviceID(deviceID); err != nil {
		return domain.Command{}, err
	}
	if err := desired.Validate(); err != nil {
		return domain.Command{}, err
	}
	if idempotencyKey == "" {
		return domain.Command{}, fmt.Errorf("%w: Idempotency-Key is required for control requests", domain.ErrInvalidCommand)
	}

	// Replaying a key with the same payload must return the original command and
	// must not publish again; replaying it with a different payload is a
	// conflict rather than a second command.
	if existing, err := s.store.CommandByIdempotencyKey(ctx, deviceID, idempotencyKey); err == nil {
		if existing.Payload.Thresholds == nil || !existing.Payload.Thresholds.Equal(desired) {
			return domain.Command{}, fmt.Errorf("%w: device %s key %s", ErrIdempotencyConflict, deviceID, idempotencyKey)
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Command{}, fmt.Errorf("command: look up idempotency key: %w", err)
	}

	record, err := s.store.Thresholds(ctx, deviceID)
	if err != nil {
		return domain.Command{}, fmt.Errorf("command: load thresholds: %w", err)
	}
	version := record.DesiredVersion + 1

	now := s.now().UTC()
	command := s.newCommand(deviceID, idempotencyKey, actor, now, domain.CommandSetThresholds)
	command.Payload.Thresholds = &desired
	command.Payload.ThresholdVersion = &version
	command.DesiredVersion = &version

	if err := s.store.InsertCommand(ctx, command); err != nil {
		return domain.Command{}, fmt.Errorf("command: record thresholds command: %w", err)
	}
	return s.publish(ctx, command)
}

// newCommand builds a freshly accepted command.
func (s *Service) newCommand(deviceID, idempotencyKey, actor string, now time.Time, commandType domain.CommandType) domain.Command {
	return domain.Command{
		RequestID:      store.NewID(now),
		DeviceID:       deviceID,
		Type:           commandType,
		State:          domain.CommandAccepted,
		IdempotencyKey: idempotencyKey,
		AcceptedAt:     now,
		ExpiresAt:      now.Add(s.ttl),
		Actor:          actor,
	}
}

// publish sends the command to the broker and records the outcome. A publish
// failure is a terminal command state, not a silent drop: the client is entitled
// to learn that the command never left the backend.
func (s *Service) publish(ctx context.Context, command domain.Command) (domain.Command, error) {
	payload, err := protocol.EncodeControl(command)
	if err != nil {
		if _, patchErr := s.store.ApplyCommandPatch(ctx, command.DeviceID, command.RequestID, domain.CommandPatch{
			State:       domain.CommandPublishFailed,
			CompletedAt: s.now().UTC(),
			ErrorCode:   "encode_failed",
		}); patchErr != nil {
			s.logger.LogAttrs(ctx, slog.LevelError, "could not record encode failure",
				slog.String("request_id", command.RequestID), slog.String("error", patchErr.Error()))
		}
		return domain.Command{}, fmt.Errorf("command: encode control payload: %w", err)
	}

	if err := s.publisher.Publish(ctx, protocol.TopicControl, payload, 1); err != nil {
		patch := domain.CommandPatch{State: domain.CommandPublishFailed, CompletedAt: s.now().UTC(), ErrorCode: "publish_failed"}
		failed, patchErr := s.store.ApplyCommandPatch(ctx, command.DeviceID, command.RequestID, patch)
		if patchErr != nil {
			s.logger.LogAttrs(ctx, slog.LevelError, "could not record publish failure",
				slog.String("request_id", command.RequestID), slog.String("error", patchErr.Error()))
			return domain.Command{}, fmt.Errorf("%w: %v", ErrPublishFailed, err)
		}
		s.emit(ctx, failed)
		return failed, fmt.Errorf("%w: %v", ErrPublishFailed, err)
	}

	published, err := s.store.MarkCommandPublished(ctx, command.DeviceID, command.RequestID, s.now().UTC())
	if err != nil {
		s.logger.LogAttrs(ctx, slog.LevelError, "could not record publish",
			slog.String("request_id", command.RequestID), slog.String("error", err.Error()))
		return command, nil
	}
	s.emit(ctx, published)
	return published, nil
}

// HandleAck applies a device acknowledgement. It implements the ingest
// acknowledgement port.
//
// Duplicate and late acknowledgements are expected: MQTT QoS 1 delivers at least
// once, and a device may answer after the backend already gave up. Both are
// logged and ignored rather than treated as errors, because neither indicates a
// defect in the message itself.
func (s *Service) HandleAck(ctx context.Context, ack protocol.CommandAck) error {
	command, err := s.store.Command(ctx, ack.DeviceID, ack.RequestID)
	if errors.Is(err, store.ErrNotFound) {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "acknowledgement for an unknown command",
			slog.String("device_id", ack.DeviceID), slog.String("request_id", ack.RequestID))
		return nil
	}
	if err != nil {
		return fmt.Errorf("command: load command for acknowledgement: %w", err)
	}

	patch, err := domain.ApplyAck(ack.Status, ack.ErrorCode, ack.ThresholdVersion, ack.ReceivedAt)
	if err != nil {
		return err
	}
	updated, err := s.store.ApplyCommandPatch(ctx, ack.DeviceID, ack.RequestID, patch)
	if errors.Is(err, store.ErrConflict) {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "late acknowledgement ignored; command already final",
			slog.String("request_id", ack.RequestID),
			slog.String("recorded_state", string(command.State)),
			slog.String("ack_status", string(ack.Status)))
		return nil
	}
	if err != nil {
		return fmt.Errorf("command: apply acknowledgement: %w", err)
	}

	// A device that applied new thresholds reports the version it now has in
	// force. Recording it here closes the desired/confirmed loop even if the
	// per-command bookkeeping is later pruned.
	if updated.State == domain.CommandApplied && updated.Type == domain.CommandSetThresholds && updated.ConfirmedVersion != nil {
		if err := s.store.ConfirmThresholdVersion(ctx, ack.DeviceID, *updated.ConfirmedVersion, ack.ReceivedAt); err != nil {
			return fmt.Errorf("command: confirm threshold version: %w", err)
		}
		if s.sink != nil {
			s.sink.PublishThresholdsConfirmed(ctx, ack.DeviceID, *updated.ConfirmedVersion)
		}
	}
	s.emit(ctx, updated)
	return nil
}

// ExpireDue closes every command past its expiry and returns the closed records.
// It is meant to be called from a timer: a device that never answers produces no
// message that could trigger the timeout.
func (s *Service) ExpireDue(ctx context.Context, now time.Time) ([]domain.Command, error) {
	expired, err := s.store.ExpireCommands(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("command: expire commands: %w", err)
	}
	for _, command := range expired {
		s.emit(ctx, command)
	}
	return expired, nil
}

// Command reads back one command.
func (s *Service) Command(ctx context.Context, deviceID, requestID string) (domain.Command, error) {
	return s.store.Command(ctx, deviceID, requestID)
}

// emit publishes a command status change to the realtime stream.
func (s *Service) emit(ctx context.Context, command domain.Command) {
	if s.sink == nil {
		return
	}
	s.sink.PublishCommandStatusChanged(ctx, command)
}
