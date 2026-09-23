package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/alert"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/events"
	"github.com/BobcGn/final/backend/internal/ingest"
	"github.com/BobcGn/final/backend/internal/liveness"
	"github.com/BobcGn/final/backend/internal/mqtt"
	"github.com/BobcGn/final/backend/internal/protocol"
	"github.com/BobcGn/final/backend/internal/store"
)

// start is a fixed instant so the cases are reproducible.
var start = time.Date(2026, 9, 24, 10, 40, 30, 0, time.UTC)

// recordingSink records the realtime events the ingest path publishes.
type recordingSink struct {
	mu        sync.Mutex
	telemetry []domain.Telemetry
	statuses  []events.DeviceStatusData
	alerts    []domain.AlertEvent
	commands  []domain.Command
	threshold []int
}

// PublishTelemetryUpdated implements events.Sink.
func (s *recordingSink) PublishTelemetryUpdated(_ context.Context, sample domain.Telemetry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.telemetry = append(s.telemetry, sample)
}

// PublishDeviceStatusChanged implements events.Sink.
func (s *recordingSink) PublishDeviceStatusChanged(_ context.Context, _ string, data events.DeviceStatusData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, data)
}

// PublishAlertStateChanged implements events.Sink.
func (s *recordingSink) PublishAlertStateChanged(_ context.Context, event domain.AlertEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, event)
}

// PublishCommandStatusChanged implements events.Sink.
func (s *recordingSink) PublishCommandStatusChanged(_ context.Context, command domain.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, command)
}

// PublishThresholdsConfirmed implements events.Sink.
func (s *recordingSink) PublishThresholdsConfirmed(_ context.Context, _ string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threshold = append(s.threshold, version)
}

// total returns how many events of every kind were published.
func (s *recordingSink) total() int {
	telemetry, statuses, alerts, thresholds := s.counts()
	return telemetry + statuses + alerts + thresholds
}

// counts returns how many events of each kind were published.
func (s *recordingSink) counts() (telemetry, statuses, alerts, thresholds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.telemetry), len(s.statuses), len(s.alerts), len(s.threshold)
}

// harness bundles the service under test with its collaborators.
type harness struct {
	service *ingest.Service
	store   store.Store
	tracker *liveness.Tracker
	sink    *recordingSink
	clock   time.Time
}

// newHarness builds an ingest service backed by an in-memory store.
func newHarness(t *testing.T, allowed ...string) *harness {
	t.Helper()

	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}
	dataStore := store.NewMemory()
	sink := &recordingSink{}
	result := &harness{store: dataStore, tracker: tracker, sink: sink, clock: start}

	service, err := ingest.New(ingest.Deps{
		Store:          dataStore,
		Engine:         engine,
		Tracker:        tracker,
		Sink:           sink,
		Logger:         slog.New(slog.DiscardHandler),
		Now:            func() time.Time { return result.clock },
		AllowedDevices: allowed,
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	result.service = service
	return result
}

// telemetry builds a payload for the harness.
func telemetry(mutate func(map[string]any)) []byte {
	fields := map[string]any{
		"schemaVersion":    1,
		"messageType":      "telemetry",
		"deviceId":         "MCU001",
		"bootId":           "boot1",
		"sequence":         1,
		"timestamp":        start.UnixMilli(),
		"uptimeMs":         1000,
		"temperatureC":     28.0,
		"humidityRh":       61.0,
		"gasAdcRaw":        1000,
		"gasAdcFiltered":   1000,
		"gasPpm":           25.0,
		"gasCalibrated":    false,
		"localAlarm":       false,
		"alarmCauses":      []string{},
		"network":          "online",
		"thresholdVersion": 1,
		"sensorFault":      false,
	}
	if mutate != nil {
		mutate(fields)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestHandleTelemetryStoresAndPublishes verifies the happy path.
func TestHandleTelemetryStoresAndPublishes(t *testing.T) {
	h := newHarness(t)

	if err := h.service.HandleTelemetry(context.Background(), telemetry(nil)); err != nil {
		t.Fatalf("handle telemetry: %v", err)
	}

	latest, err := h.store.LatestTelemetry(context.Background(), "MCU001")
	if err != nil {
		t.Fatalf("read stored sample: %v", err)
	}
	if latest.Sequence != 1 || latest.TemperatureC != 28 {
		t.Fatalf("stored %+v", latest)
	}

	telemetryEvents, statusEvents, _, thresholdEvents := h.sink.counts()
	if telemetryEvents != 1 {
		t.Fatalf("published %d telemetry events, want 1", telemetryEvents)
	}
	// The first sample is also a transition from unknown to online.
	if statusEvents != 1 {
		t.Fatalf("published %d status events, want 1", statusEvents)
	}
	// The device reports version 1 while the stored record starts at version 1,
	// so the version is confirmed once and reported once.
	if thresholdEvents != 1 {
		t.Fatalf("published %d threshold confirmations, want 1", thresholdEvents)
	}

	state, err := h.store.Device(context.Background(), "MCU001")
	if err != nil {
		t.Fatalf("read device state: %v", err)
	}
	if state.Connectivity != domain.ConnectivityOnline {
		t.Fatalf("connectivity = %q, want online", state.Connectivity)
	}
	if state.LastSequence != 1 || state.LastBootID != "boot1" {
		t.Fatalf("device state cursor = (%s, %d)", state.LastBootID, state.LastSequence)
	}
}

// TestDuplicateIsCountedAndChangesNothing is the QoS 1 redelivery rule: a repeat
// must not extend uptime, move the alert window or publish an event.
func TestDuplicateIsCountedAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.service.HandleTelemetry(ctx, telemetry(nil)); err != nil {
		t.Fatalf("first sample: %v", err)
	}
	before := h.sink.total()

	if err := h.service.HandleTelemetry(ctx, telemetry(nil)); err != nil {
		t.Fatalf("duplicate sample: %v", err)
	}
	after := h.sink.total()
	if before != after {
		t.Fatalf("a duplicate published events: before %v after %v", before, after)
	}

	stats := h.service.Stats()
	if stats.Duplicates != 1 {
		t.Fatalf("duplicate count = %d, want 1", stats.Duplicates)
	}
	if stats.Accepted != 1 {
		t.Fatalf("accepted count = %d, want 1", stats.Accepted)
	}

	// The stored sample count is unchanged, which is what makes the dedup real
	// rather than cosmetic.
	page, err := h.store.ListTelemetry(ctx, store.TelemetryQuery{
		DeviceID: "MCU001",
		Range:    store.TimeRange{From: start.Add(-time.Hour), To: start.Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("list telemetry: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("stored %d samples after a duplicate, want 1", len(page.Items))
	}
}

// TestLateSampleFeedsTheTrendWithoutMovingState verifies that an out-of-order
// sample is stored and folded into the alert window but cannot change the device
// snapshot, so stale data cannot overwrite newer readings.
func TestLateSampleFeedsTheTrendWithoutMovingState(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A newer sample first, then a late one.
	h.clock = start.Add(30 * time.Second)
	if err := h.service.HandleTelemetry(ctx, telemetry(func(f map[string]any) {
		f["sequence"] = 3
		f["temperatureC"] = 40.0
		f["localAlarm"] = true
		f["alarmCauses"] = []string{"temperature_high"}
	})); err != nil {
		t.Fatalf("newer sample: %v", err)
	}

	h.clock = start
	if err := h.service.HandleTelemetry(ctx, telemetry(func(f map[string]any) {
		f["sequence"] = 2
		f["temperatureC"] = 20.0
	})); err != nil {
		t.Fatalf("late sample: %v", err)
	}

	state, err := h.store.Device(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read device state: %v", err)
	}
	if state.LastSequence != 3 {
		t.Fatalf("last sequence = %d, want the newer 3", state.LastSequence)
	}
	if state.LocalAlarm != true {
		t.Fatal("a late sample cleared the alarm flag set by a newer sample")
	}

	stats := h.service.Stats()
	if stats.LateSamples != 1 {
		t.Fatalf("late count = %d, want 1", stats.LateSamples)
	}
	// The late sample is still stored: dropping it would lose data.
	latest, err := h.store.LatestTelemetry(ctx, "MCU001")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Sequence != 3 {
		t.Fatalf("latest sequence = %d, want 3", latest.Sequence)
	}
}

// TestMalformedPayloadIsCountedByReason verifies that a rejected payload is
// counted under its stable reason and reported, without stopping the session.
func TestMalformedPayloadIsCountedByReason(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	cases := map[string]struct {
		raw    []byte
		reason string
	}{
		"unknown field": {
			raw:    telemetry(func(f map[string]any) { f["surprise"] = true }),
			reason: protocol.ReasonUnknownField,
		},
		"out of range": {
			raw:    telemetry(func(f map[string]any) { f["gasAdcRaw"] = 9999 }),
			reason: protocol.ReasonOutOfRange,
		},
		"malformed JSON": {
			raw:    []byte(`{"schemaVersion":`),
			reason: protocol.ReasonMalformedJSON,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fresh := newHarness(t)
			if err := fresh.service.HandleTelemetry(ctx, testCase.raw); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if got := fresh.service.Stats().Rejected[testCase.reason]; got != 1 {
				t.Fatalf("reject counter for %q = %d, want 1 (counters %v)", testCase.reason, got, fresh.service.Stats().Rejected)
			}
		})
	}

	// A rejected payload leaves nothing behind.
	if _, err := h.store.LatestTelemetry(ctx, "MCU001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a rejected payload stored something: %v", err)
	}
}

// TestAllowlistRejectsAnotherDevice verifies that the configured allowlist is
// enforced before anything is stored.
func TestAllowlistRejectsAnotherDevice(t *testing.T) {
	h := newHarness(t, "MCU002")
	ctx := context.Background()

	err := h.service.HandleTelemetry(ctx, telemetry(nil))
	if !errors.Is(err, ingest.ErrDeviceNotAllowed) {
		t.Fatalf("error = %v, want ErrDeviceNotAllowed", err)
	}
	if _, err := h.store.LatestTelemetry(ctx, "MCU001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a disallowed device was stored")
	}
	if got := h.service.Stats().Rejected[protocol.ReasonDeviceNotAllowed]; got != 1 {
		t.Fatalf("reject counter = %d, want 1", got)
	}

	if err := h.service.HandleTelemetry(ctx, telemetry(func(f map[string]any) { f["deviceId"] = "MCU002" })); err != nil {
		t.Fatalf("an allowed device was rejected: %v", err)
	}
}

// TestCompositeWarningOpensAnEpisode verifies the ingest side of the alert
// state machine: a confirmed composite condition creates an episode and
// publishes it.
func TestCompositeWarningOpensAnEpisode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Eight samples over 35 seconds, gas surging and temperature climbing at
	// twice the configured slope.
	for index := 0; index < 8; index++ {
		offset := time.Duration(index) * 5 * time.Second
		h.clock = start.Add(offset)
		adc := 1000
		if index == 7 {
			adc = 1600
		}
		raw := telemetry(func(f map[string]any) {
			f["sequence"] = index + 1
			f["timestamp"] = h.clock.UnixMilli()
			f["uptimeMs"] = offset.Milliseconds()
			f["gasAdcRaw"] = adc
			f["gasAdcFiltered"] = adc
			f["temperatureC"] = 25 + offset.Minutes()*6
		})
		if err := h.service.HandleTelemetry(ctx, raw); err != nil {
			t.Fatalf("sample %d: %v", index, err)
		}
	}

	active, err := h.store.ActiveAlert(ctx, "MCU001")
	if err != nil {
		t.Fatalf("no alert was opened: %v", err)
	}
	if active.State != domain.AlertFireWarning {
		t.Fatalf("alert state = %q, want fire_warning", active.State)
	}
	if active.Evidence.SampleCount != 8 || active.Evidence.GasAdcRise != 600 {
		t.Fatalf("evidence = %+v", active.Evidence)
	}

	state, err := h.store.Device(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read device state: %v", err)
	}
	if state.AlarmState != domain.AlertFireWarning || state.ActiveAlertID == "" {
		t.Fatalf("device state = %+v", state)
	}

	_, _, alerts, _ := h.sink.counts()
	if alerts == 0 {
		t.Fatal("no alert event was published")
	}
}

// TestSingleSampleCannotOpenAnEpisode is the safety property at the ingest
// level: one extreme reading must not produce a fire warning.
func TestSingleSampleCannotOpenAnEpisode(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	raw := telemetry(func(f map[string]any) {
		f["gasAdcRaw"] = 4095
		f["gasAdcFiltered"] = 4095
		f["temperatureC"] = 79.0
		f["localAlarm"] = true
		f["alarmCauses"] = []string{"temperature_high", "gas_high"}
	})
	if err := h.service.HandleTelemetry(ctx, raw); err != nil {
		t.Fatalf("handle: %v", err)
	}

	if _, err := h.store.ActiveAlert(ctx, "MCU001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a single sample opened an alert: %v", err)
	}
}

// TestSweepOfflineMarksTheDevice verifies the heartbeat path that no inbound
// message can trigger.
func TestSweepOfflineMarksTheDevice(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.service.HandleTelemetry(ctx, telemetry(nil)); err != nil {
		t.Fatalf("handle: %v", err)
	}

	// Inside the offline window: nothing happens.
	if envelopes, err := h.service.SweepOffline(ctx, start.Add(10*time.Second)); err != nil {
		t.Fatalf("early sweep: %v", err)
	} else if len(envelopes) != 0 {
		t.Fatalf("early sweep produced %d events", len(envelopes))
	}

	envelopes, err := h.service.SweepOffline(ctx, start.Add(time.Minute))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(envelopes) != 1 {
		t.Fatalf("sweep produced %d events, want 1", len(envelopes))
	}
	if envelopes[0].Type != events.TypeDeviceStatusChanged {
		t.Fatalf("event type = %q", envelopes[0].Type)
	}
	data, ok := envelopes[0].Data.(events.DeviceStatusData)
	if !ok || data.Connectivity != domain.ConnectivityOffline {
		t.Fatalf("event data = %+v", envelopes[0].Data)
	}

	state, err := h.store.Device(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read device state: %v", err)
	}
	if state.Connectivity != domain.ConnectivityOffline {
		t.Fatalf("stored connectivity = %q, want offline", state.Connectivity)
	}

	// A repeated sweep must not repeat the event.
	if again, err := h.service.SweepOffline(ctx, start.Add(2*time.Minute)); err != nil {
		t.Fatalf("second sweep: %v", err)
	} else if len(again) != 0 {
		t.Fatalf("second sweep repeated %d events", len(again))
	}
}

// TestThresholdVersionConfirmation verifies that the version a device reports is
// recorded, which closes the desired/confirmed loop even when the command
// acknowledgement was lost.
func TestThresholdVersionConfirmation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.store.SetDesiredThresholds(ctx, "MCU001",
		domain.Thresholds{TemperatureHighC: 33, HumidityHighRh: 85, GasHighPpm: 90},
		domain.InitialThresholdVersion+1, start); err != nil {
		t.Fatalf("set desired thresholds: %v", err)
	}

	if err := h.service.HandleTelemetry(ctx, telemetry(func(f map[string]any) {
		f["thresholdVersion"] = domain.InitialThresholdVersion + 1
	})); err != nil {
		t.Fatalf("handle: %v", err)
	}

	record, err := h.store.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if record.ConfirmedVersion == nil || *record.ConfirmedVersion != domain.InitialThresholdVersion+1 {
		t.Fatalf("confirmed version = %v", record.ConfirmedVersion)
	}
	if record.ConfirmationState != store.ConfirmationConfirmed {
		t.Fatalf("confirmation state = %q, want confirmed", record.ConfirmationState)
	}

	// A repeat of the same version must not re-publish the confirmation event.
	_, _, _, before := h.sink.counts()
	if err := h.service.HandleTelemetry(ctx, telemetry(func(f map[string]any) {
		f["sequence"] = 2
		f["thresholdVersion"] = domain.InitialThresholdVersion + 1
	})); err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if _, _, _, after := h.sink.counts(); after != before {
		t.Fatalf("a repeated version published another confirmation: %d then %d", before, after)
	}
}

// TestHandleMessageRoutesByTopic verifies the MQTT dispatch table and that an
// unexpected topic is reported rather than ignored silently.
func TestHandleMessageRoutesByTopic(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if err := h.service.HandleMessage(ctx, mqtt.Message{
		Topic: protocol.TopicTelemetry, Payload: telemetry(nil),
	}); err != nil {
		t.Fatalf("telemetry topic: %v", err)
	}

	ack := []byte(`{
		"schemaVersion": 1, "messageType": "command_ack", "deviceId": "MCU001",
		"bootId": "boot1", "sequence": 2, "timestamp": null, "uptimeMs": 2000,
		"requestId": "01REQ", "status": "applied", "thresholdVersion": 1, "errorCode": null
	}`)
	if err := h.service.HandleMessage(ctx, mqtt.Message{
		Topic: protocol.TopicCommandAck, Payload: ack,
	}); err != nil {
		t.Fatalf("ack topic: %v", err)
	}

	err := h.service.HandleMessage(ctx, mqtt.Message{Topic: "device/unknown", Payload: []byte("{}")})
	if err == nil {
		t.Fatal("an unhandled topic was accepted")
	}
}

// TestAckHandlerReceivesValidatedAcknowledgements verifies that the command
// service port is only called with an acknowledgement that already passed
// decoding and the allowlist.
func TestAckHandlerReceivesValidatedAcknowledgements(t *testing.T) {
	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}
	recorder := &ackRecorder{}
	service, err := ingest.New(ingest.Deps{
		Store:          store.NewMemory(),
		Engine:         engine,
		Tracker:        tracker,
		Logger:         slog.New(slog.DiscardHandler),
		Now:            func() time.Time { return start },
		AckHandler:     recorder,
		AllowedDevices: []string{"MCU001"},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	valid := []byte(`{
		"schemaVersion": 1, "messageType": "command_ack", "deviceId": "MCU001",
		"bootId": "boot1", "sequence": 2, "timestamp": null, "uptimeMs": 2000,
		"requestId": "01REQ", "status": "applied", "thresholdVersion": 1, "errorCode": null
	}`)
	if err := service.HandleCommandAck(context.Background(), valid); err != nil {
		t.Fatalf("valid ack: %v", err)
	}
	if len(recorder.acks) != 1 {
		t.Fatalf("the ack handler saw %d acknowledgements, want 1", len(recorder.acks))
	}

	// A malformed acknowledgement must never reach the state machine.
	if err := service.HandleCommandAck(context.Background(), []byte(`{"status":"applied"}`)); err == nil {
		t.Fatal("a malformed acknowledgement was accepted")
	}
	// Neither must one from a device outside the allowlist.
	other := []byte(`{
		"schemaVersion": 1, "messageType": "command_ack", "deviceId": "MCU009",
		"bootId": "boot1", "sequence": 2, "timestamp": null, "uptimeMs": 2000,
		"requestId": "01REQ", "status": "applied", "thresholdVersion": 1, "errorCode": null
	}`)
	if err := service.HandleCommandAck(context.Background(), other); !errors.Is(err, ingest.ErrDeviceNotAllowed) {
		t.Fatalf("error = %v, want ErrDeviceNotAllowed", err)
	}
	if len(recorder.acks) != 1 {
		t.Fatalf("the ack handler saw %d acknowledgements after rejections, want 1", len(recorder.acks))
	}
}

// ackRecorder counts the acknowledgements handed to the command port.
type ackRecorder struct {
	acks []protocol.CommandAck
}

// HandleAck implements ingest.AckHandler.
func (r *ackRecorder) HandleAck(_ context.Context, ack protocol.CommandAck) error {
	r.acks = append(r.acks, ack)
	return nil
}

// TestNewValidatesDependencies covers the construction guards.
func TestNewValidatesDependencies(t *testing.T) {
	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}

	cases := map[string]ingest.Deps{
		"no store":   {Engine: engine, Tracker: tracker},
		"no engine":  {Store: store.NewMemory(), Tracker: tracker},
		"no tracker": {Store: store.NewMemory(), Engine: engine},
		"bad allowlist entry": {
			Store: store.NewMemory(), Engine: engine, Tracker: tracker,
			AllowedDevices: []string{"not valid"},
		},
	}
	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ingest.New(deps); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}
