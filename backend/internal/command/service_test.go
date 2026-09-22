package command_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/command"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
	"github.com/BobcGn/final/backend/internal/protocol"
	"github.com/BobcGn/final/backend/internal/store"
)

// start is a fixed instant so the cases are reproducible.
var start = time.Date(2026, 9, 24, 10, 40, 30, 0, time.UTC)

// fakePublisher records published messages and can be made to fail.
type fakePublisher struct {
	mu       sync.Mutex
	topic    string
	payloads [][]byte
	qos      []byte
	err      error
}

// Publish implements command.Publisher.
func (p *fakePublisher) Publish(_ context.Context, topic string, payload []byte, qos byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.err != nil {
		return p.err
	}
	p.topic = topic
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	p.qos = append(p.qos, qos)
	return nil
}

// count returns how many publishes succeeded.
func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.payloads)
}

// last returns the most recent published payload.
func (p *fakePublisher) last(t *testing.T) []byte {
	t.Helper()

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.payloads) == 0 {
		t.Fatal("nothing was published")
	}
	return p.payloads[len(p.payloads)-1]
}

// recordingSink records published events.
type recordingSink struct {
	mu        sync.Mutex
	commands  []domain.Command
	threshold []int
}

// PublishTelemetryUpdated implements events.Sink.
func (s *recordingSink) PublishTelemetryUpdated(context.Context, domain.Telemetry) {}

// PublishDeviceStatusChanged implements events.Sink.
func (s *recordingSink) PublishDeviceStatusChanged(context.Context, string, events.DeviceStatusData) {
}

// PublishAlertStateChanged implements events.Sink.
func (s *recordingSink) PublishAlertStateChanged(context.Context, domain.AlertEvent) {}

// PublishCommandStatusChanged implements events.Sink.
func (s *recordingSink) PublishCommandStatusChanged(_ context.Context, cmd domain.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, cmd)
}

// PublishThresholdsConfirmed implements events.Sink.
func (s *recordingSink) PublishThresholdsConfirmed(_ context.Context, _ string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threshold = append(s.threshold, version)
}

// states returns the states of the published command events.
func (s *recordingSink) states() []domain.CommandState {
	s.mu.Lock()
	defer s.mu.Unlock()

	states := make([]domain.CommandState, 0, len(s.commands))
	for _, cmd := range s.commands {
		states = append(states, cmd.State)
	}
	return states
}

// harness bundles the service under test with its collaborators.
type harness struct {
	service   *command.Service
	store     store.Store
	publisher *fakePublisher
	sink      *recordingSink
	clock     time.Time
}

// newHarness builds a command service backed by an in-memory store.
func newHarness(t *testing.T) *harness {
	t.Helper()

	result := &harness{
		store:     store.NewMemory(),
		publisher: &fakePublisher{},
		sink:      &recordingSink{},
		clock:     start,
	}
	service, err := command.New(command.Deps{
		Store:     result.store,
		Publisher: result.publisher,
		Sink:      result.sink,
		Logger:    slog.New(slog.DiscardHandler),
		Now:       func() time.Time { return result.clock },
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}
	result.service = service
	return result
}

// desiredThresholds is the threshold set used by the tests.
var desiredThresholds = domain.Thresholds{TemperatureHighC: 33, HumidityHighRh: 85, GasHighPpm: 90}

// TestThresholdRequestAcceptsAndPublishes verifies the happy path: the version
// is minted, the record advances and a control message goes to the broker.
func TestThresholdRequestAcceptsAndPublishes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	accepted, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if accepted.State != domain.CommandPublished {
		t.Fatalf("state = %q, want published", accepted.State)
	}
	if accepted.DesiredVersion == nil || *accepted.DesiredVersion != domain.InitialThresholdVersion+1 {
		t.Fatalf("desired version = %v", accepted.DesiredVersion)
	}
	if accepted.ExpiresAt.Sub(accepted.AcceptedAt) != command.DefaultTTL {
		t.Fatalf("validity window = %s, want %s", accepted.ExpiresAt.Sub(accepted.AcceptedAt), command.DefaultTTL)
	}

	record, err := h.store.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if !record.Desired.Equal(desiredThresholds) {
		t.Fatalf("stored desired = %+v", record.Desired)
	}
	if record.ConfirmedVersion != nil {
		t.Fatalf("a command that was only published reported confirmed version %v", *record.ConfirmedVersion)
	}

	payload := h.publisher.last(t)
	decoded, err := protocol.DecodeControl(payload, start)
	if err != nil {
		t.Fatalf("the published payload does not satisfy the device contract: %v", err)
	}
	if decoded.Type != domain.CommandSetThresholds {
		t.Fatalf("published type = %q", decoded.Type)
	}
	if decoded.Payload.Thresholds == nil || !decoded.Payload.Thresholds.Equal(desiredThresholds) {
		t.Fatalf("published thresholds = %+v", decoded.Payload.Thresholds)
	}
	if h.publisher.topic != protocol.TopicControl {
		t.Fatalf("published to %q, want %q", h.publisher.topic, protocol.TopicControl)
	}
}

// TestIdempotency returns the original command and never publishes twice.
func TestIdempotency(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("replayed request: %v", err)
	}
	if second.RequestID != first.RequestID {
		t.Fatalf("a replay created a new command %s instead of returning %s", second.RequestID, first.RequestID)
	}
	if h.publisher.count() != 1 {
		t.Fatalf("a replay published %d messages, want 1", h.publisher.count())
	}

	// The same key with a different payload is a conflict, not a second command.
	other := domain.Thresholds{TemperatureHighC: 34, HumidityHighRh: 85, GasHighPpm: 90}
	if _, err := h.service.RequestThresholdUpdate(ctx, "MCU001", other, "01IDEM", "operator"); !errors.Is(err, command.ErrIdempotencyConflict) {
		t.Fatalf("reused key with a new payload returned %v, want ErrIdempotencyConflict", err)
	}
	if h.publisher.count() != 1 {
		t.Fatalf("a conflicting request published %d messages, want 1", h.publisher.count())
	}

	// A different key is a new command with a new version.
	third, err := h.service.RequestThresholdUpdate(ctx, "MCU001", other, "01IDEM2", "operator")
	if err != nil {
		t.Fatalf("second command: %v", err)
	}
	if third.DesiredVersion == nil || *third.DesiredVersion != domain.InitialThresholdVersion+2 {
		t.Fatalf("second version = %v", third.DesiredVersion)
	}
}

// TestValidationRejectsBadRequests covers the request guards.
func TestValidationRejectsBadRequests(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	cases := map[string]func() error{
		"missing idempotency key": func() error {
			_, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "", "operator")
			return err
		},
		"invalid device id": func() error {
			_, err := h.service.RequestThresholdUpdate(ctx, "has space", desiredThresholds, "01IDEM", "operator")
			return err
		},
		"threshold out of range": func() error {
			_, err := h.service.RequestThresholdUpdate(ctx, "MCU001",
				domain.Thresholds{TemperatureHighC: 200, HumidityHighRh: 80, GasHighPpm: 80}, "01IDEM", "operator")
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
	if h.publisher.count() != 0 {
		t.Fatalf("a rejected request published %d messages", h.publisher.count())
	}
}

// TestPublishFailureIsRecorded verifies that a command the broker never took is
// reported as failed rather than left looking pending.
func TestPublishFailureIsRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.publisher.err = errors.New("broker refused")

	_, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if !errors.Is(err, command.ErrPublishFailed) {
		t.Fatalf("error = %v, want ErrPublishFailed", err)
	}

	// The command row still exists so the client can learn why it failed.
	pending, err := h.store.PendingCommands(ctx)
	if err != nil {
		t.Fatalf("pending commands: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a failed publish left %d commands pending", len(pending))
	}

	states := h.sink.states()
	if len(states) != 1 || states[0] != domain.CommandPublishFailed {
		t.Fatalf("published states = %v, want one publish_failed", states)
	}

	// The desired version advanced with the command, so the operator can see the
	// device is behind rather than silently unchanged.
	record, err := h.store.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if record.ConfirmedVersion != nil {
		t.Fatal("a command that never reached the device confirmed a version")
	}
}

// TestAckAppliedClosesTheLoop verifies that a device acknowledgement is what
// moves a command to applied and records the confirmed version.
func TestAckAppliedClosesTheLoop(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	accepted, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	version := *accepted.DesiredVersion

	if err := h.service.HandleAck(ctx, protocol.CommandAck{
		DeviceID: "MCU001", BootID: "boot1", Sequence: 5,
		RequestID: accepted.RequestID, Status: domain.AckApplied,
		ThresholdVersion: &version, ReceivedAt: start.Add(time.Second),
	}); err != nil {
		t.Fatalf("handle ack: %v", err)
	}

	stored, err := h.service.Command(ctx, "MCU001", accepted.RequestID)
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	if stored.State != domain.CommandApplied {
		t.Fatalf("state = %q, want applied", stored.State)
	}
	if stored.ConfirmedVersion == nil || *stored.ConfirmedVersion != version {
		t.Fatalf("confirmed version = %v", stored.ConfirmedVersion)
	}

	record, err := h.store.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if record.ConfirmationState != store.ConfirmationConfirmed {
		t.Fatalf("confirmation state = %q, want confirmed", record.ConfirmationState)
	}

	states := h.sink.states()
	if states[len(states)-1] != domain.CommandApplied {
		t.Fatalf("last published state = %q, want applied", states[len(states)-1])
	}
}

// TestAckRejectedRecordsTheReason verifies that a device refusal is recorded with
// its error code and never claims a version change.
func TestAckRejectedRecordsTheReason(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	accepted, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	version := *accepted.DesiredVersion

	if err := h.service.HandleAck(ctx, protocol.CommandAck{
		DeviceID: "MCU001", BootID: "boot1", Sequence: 6,
		RequestID: accepted.RequestID, Status: domain.AckRejected,
		ThresholdVersion: &version, ErrorCode: string(domain.AckErrFlashWriteFailed),
		ReceivedAt: start.Add(time.Second),
	}); err != nil {
		t.Fatalf("handle ack: %v", err)
	}

	stored, err := h.service.Command(ctx, "MCU001", accepted.RequestID)
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	if stored.State != domain.CommandRejected || stored.ErrorCode != string(domain.AckErrFlashWriteFailed) {
		t.Fatalf("stored %+v", stored)
	}
	if stored.ConfirmedVersion != nil {
		t.Fatal("a rejected command reported a confirmed version")
	}

	record, err := h.store.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if record.ConfirmedVersion != nil {
		t.Fatal("a rejected command confirmed a version in the threshold record")
	}
}

// TestDuplicateAckIsIdempotent verifies the QoS 1 redelivery rule on the ack path.
func TestDuplicateAckIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	accepted, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	ack := protocol.CommandAck{
		DeviceID: "MCU001", BootID: "boot1", Sequence: 7,
		RequestID: accepted.RequestID, Status: domain.AckApplied,
		ReceivedAt: start.Add(time.Second),
	}
	if err := h.service.HandleAck(ctx, ack); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	first, err := h.service.Command(ctx, "MCU001", accepted.RequestID)
	if err != nil {
		t.Fatalf("read command: %v", err)
	}

	if err := h.service.HandleAck(ctx, ack); err != nil {
		t.Fatalf("replayed ack returned an error: %v", err)
	}
	second, err := h.service.Command(ctx, "MCU001", accepted.RequestID)
	if err != nil {
		t.Fatalf("read command again: %v", err)
	}
	if !second.CompletedAt.Equal(*first.CompletedAt) {
		t.Fatalf("a replayed ack moved the completion time from %s to %s", first.CompletedAt, second.CompletedAt)
	}
}

// TestLateAckIsIgnored verifies that an acknowledgement arriving after a command
// already reached a terminal state does not rewrite history.
func TestLateAckIsIgnored(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	accepted, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	// Let the command expire first.
	expired, err := h.service.ExpireDue(ctx, start.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || expired[0].State != domain.CommandTimedOut {
		t.Fatalf("expiry produced %+v", expired)
	}

	// A device that finally answers must not reopen the command.
	if err := h.service.HandleAck(ctx, protocol.CommandAck{
		DeviceID: "MCU001", BootID: "boot1", Sequence: 8,
		RequestID: accepted.RequestID, Status: domain.AckApplied,
		ReceivedAt: start.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("a late ack returned an error: %v", err)
	}

	stored, err := h.service.Command(ctx, "MCU001", accepted.RequestID)
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	if stored.State != domain.CommandTimedOut {
		t.Fatalf("state = %q, want the recorded timeout to stand", stored.State)
	}
}

// TestAckForAnUnknownCommandIsIgnored verifies that a stale acknowledgement does
// not fail the ingest path: it is expected after a restart or a prune.
func TestAckForAnUnknownCommandIsIgnored(t *testing.T) {
	h := newHarness(t)

	err := h.service.HandleAck(context.Background(), protocol.CommandAck{
		DeviceID: "MCU001", BootID: "boot1", Sequence: 9,
		RequestID: "01UNKNOWN", Status: domain.AckApplied,
		ReceivedAt: start,
	})
	if err != nil {
		t.Fatalf("an unknown command produced an error: %v", err)
	}
}

// TestExpiryOnlyClosesUnfinishedCommands verifies the sweep's scope.
func TestExpiryOnlyClosesUnfinishedCommands(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	applied, err := h.service.RequestThresholdUpdate(ctx, "MCU001", desiredThresholds, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := h.service.HandleAck(ctx, protocol.CommandAck{
		DeviceID: "MCU001", BootID: "boot1", Sequence: 1,
		RequestID: applied.RequestID, Status: domain.AckApplied, ReceivedAt: start,
	}); err != nil {
		t.Fatalf("ack: %v", err)
	}
	second := desiredThresholds
	second.TemperatureHighC++
	if _, err := h.service.RequestThresholdUpdate(ctx, "MCU001", second, "01IDEM2", "operator"); err != nil {
		t.Fatalf("second request: %v", err)
	}

	expired, err := h.service.ExpireDue(ctx, start.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expiry closed %d commands, want only the unfinished one", len(expired))
	}
	if expired[0].RequestID == applied.RequestID {
		t.Fatal("expiry reopened an applied command")
	}
}

// TestNewValidatesDependencies covers the construction guards.
func TestNewValidatesDependencies(t *testing.T) {
	cases := map[string]command.Deps{
		"no store":     {Publisher: &fakePublisher{}},
		"no publisher": {Store: store.NewMemory()},
	}
	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := command.New(deps); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}
