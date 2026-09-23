package store_test

// This file is the shared conformance suite that every Store implementation must
// pass. It lives in a test file rather than a helper package on purpose: it is
// test-only code, so it must not appear in the coverage denominator the way a
// regular package would.
//
// The suite exists so that the in-memory store used in tests and local
// development cannot quietly diverge from the PostgreSQL store used in
// deployment. A behaviour asserted here is a behaviour of the contract, not of
// one implementation: deduplication, cursor stability, idempotent alert closing
// and the one-open-alert-per-device rule are all checked the same way against
// both.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/store"
)

// Factory builds an empty store for one test.
type Factory func(t *testing.T) store.Store

// runConformance exercises the whole Store contract. It is safe to call from
// multiple tests because the factory must return an isolated store.
func runConformance(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("TelemetryDeduplication", func(t *testing.T) { testTelemetryDeduplication(t, factory) })
	t.Run("TelemetryOrderingAndCursor", func(t *testing.T) { testTelemetryOrderingAndCursor(t, factory) })
	t.Run("TelemetryCursorScope", func(t *testing.T) { testTelemetryCursorScope(t, factory) })
	t.Run("LatestTelemetry", func(t *testing.T) { testLatestTelemetry(t, factory) })
	t.Run("AlertLifecycle", func(t *testing.T) { testAlertLifecycle(t, factory) })
	t.Run("AlertSingleActive", func(t *testing.T) { testAlertSingleActive(t, factory) })
	t.Run("AlertPaginationAndFilter", func(t *testing.T) { testAlertPaginationAndFilter(t, factory) })
	t.Run("DeviceState", func(t *testing.T) { testDeviceState(t, factory) })
	t.Run("Thresholds", func(t *testing.T) { testThresholds(t, factory) })
	t.Run("CommandLifecycle", func(t *testing.T) { testCommandLifecycle(t, factory) })
	t.Run("CommandIdempotency", func(t *testing.T) { testCommandIdempotency(t, factory) })
	t.Run("CommandExpiry", func(t *testing.T) { testCommandExpiry(t, factory) })
	t.Run("ThresholdCommandAdvancesVersion", func(t *testing.T) { testThresholdCommandAdvancesVersion(t, factory) })
}

// baseTime is a fixed instant so every test is reproducible.
var baseTime = time.Date(2026, 9, 18, 11, 20, 0, 0, time.UTC)

// sample builds a valid telemetry sample for one device.
func sample(deviceID, bootID string, sequence uint32, at time.Time) domain.Telemetry {
	gas := 25.0
	return domain.Telemetry{
		DeviceID:         deviceID,
		BootID:           bootID,
		Sequence:         sequence,
		Timestamp:        &at,
		UptimeMs:         uint64(sequence) * 1000,
		TemperatureC:     28,
		HumidityRh:       61,
		GasAdcRaw:        1350,
		GasAdcFiltered:   1328,
		GasPpm:           &gas,
		GasCalibrated:    false,
		LocalAlarm:       false,
		AlarmCauses:      []domain.AlarmCause{},
		Network:          domain.NetworkOnline,
		ThresholdVersion: 1,
		SensorFault:      false,
		ReceivedAt:       at,
	}
}

// evidence builds alert evidence with valid sample and window counts.
func evidence() domain.AlertEvidence {
	return domain.AlertEvidence{
		GasAdcRise:                         200,
		GasAdcRiseThreshold:                150,
		TemperatureRateCPerMinute:          4,
		TemperatureRateThresholdCPerMinute: 3,
		SampleCount:                        8,
		WindowSeconds:                      60,
	}
}

// testTelemetryDeduplication verifies that the (deviceID, bootID, sequence) key
// is unique and that a reboot does not collide with the previous boot.
func testTelemetryDeduplication(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	inserted, err := subject.InsertTelemetry(ctx, sample("MCU001", "boot1", 1, baseTime))
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !inserted {
		t.Fatal("first insert reported a duplicate")
	}

	inserted, err = subject.InsertTelemetry(ctx, sample("MCU001", "boot1", 1, baseTime.Add(time.Second)))
	if err != nil {
		t.Fatalf("duplicate insert: %v", err)
	}
	if inserted {
		t.Fatal("an identical (deviceId, bootId, sequence) was stored twice")
	}

	// The same sequence after a reboot must be accepted: sequence alone is not
	// the identity of a sample.
	inserted, err = subject.InsertTelemetry(ctx, sample("MCU001", "boot2", 1, baseTime.Add(time.Minute)))
	if err != nil {
		t.Fatalf("post-reboot insert: %v", err)
	}
	if !inserted {
		t.Fatal("a sample from a new bootId was treated as a duplicate")
	}

	page, err := subject.ListTelemetry(ctx, store.TelemetryQuery{
		DeviceID: "MCU001",
		Range:    store.TimeRange{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("list telemetry: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("stored %d samples, want 2", len(page.Items))
	}
}

// testTelemetryOrderingAndCursor verifies ascending and descending order and
// that cursor pagination neither skips nor repeats rows, including when several
// samples share a timestamp.
func testTelemetryOrderingAndCursor(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)
	window := store.TimeRange{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)}

	// Ten samples: five share one timestamp so the tie-break on (bootID,
	// sequence) is exercised rather than only the fast path.
	for index := 0; index < 5; index++ {
		if _, err := subject.InsertTelemetry(ctx, sample("MCU001", "boot1", uint32(index), baseTime.Add(time.Duration(index)*time.Second))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	for index := 0; index < 5; index++ {
		if _, err := subject.InsertTelemetry(ctx, sample("MCU001", "boot1", uint32(100+index), baseTime.Add(10*time.Second))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	for _, order := range []store.Order{store.OrderAsc, store.OrderDesc} {
		var (
			seen      []domain.TelemetryKey
			sequences []uint32
			cursor    string
		)
		for page := 0; page < 20; page++ {
			result, err := subject.ListTelemetry(ctx, store.TelemetryQuery{
				DeviceID: "MCU001", Range: window, Limit: 3, Order: order, Cursor: cursor,
			})
			if err != nil {
				t.Fatalf("%s page %d: %v", order, page, err)
			}
			if len(result.Items) == 0 && result.NextCursor == "" {
				break
			}
			for _, item := range result.Items {
				seen = append(seen, item.Key())
				sequences = append(sequences, item.Sequence)
			}
			if result.NextCursor == "" {
				break
			}
			cursor = result.NextCursor
		}

		if len(seen) != 10 {
			t.Fatalf("%s order returned %d samples across pages, want 10", order, len(seen))
		}
		unique := make(map[domain.TelemetryKey]struct{}, len(seen))
		for _, key := range seen {
			if _, duplicate := unique[key]; duplicate {
				t.Fatalf("%s order repeated sample %+v across pages", order, key)
			}
			unique[key] = struct{}{}
		}
		// Sequence 104 is the newest sample and 0 the oldest, so the endpoints
		// pin the direction without depending on how the ties were broken.
		switch order {
		case store.OrderAsc:
			if sequences[0] != 0 || sequences[len(sequences)-1] != 104 {
				t.Fatalf("ascending order ran %v, want 0 first and 104 last", sequences)
			}
		case store.OrderDesc:
			if sequences[0] != 104 || sequences[len(sequences)-1] != 0 {
				t.Fatalf("descending order ran %v, want 104 first and 0 last", sequences)
			}
		}
	}
}

// testTelemetryCursorScope verifies that a cursor bound to one query is rejected
// when presented with a different range, which is the frozen contract's rule
// that a cursored request repeats the first page's from, to and order.
func testTelemetryCursorScope(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)
	for index := 0; index < 5; index++ {
		if _, err := subject.InsertTelemetry(ctx, sample("MCU001", "boot1", uint32(index), baseTime.Add(time.Duration(index)*time.Second))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	first, err := subject.ListTelemetry(ctx, store.TelemetryQuery{
		DeviceID: "MCU001",
		Range:    store.TimeRange{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)},
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}

	_, err = subject.ListTelemetry(ctx, store.TelemetryQuery{
		DeviceID: "MCU001",
		Range:    store.TimeRange{From: baseTime.Add(-2 * time.Hour), To: baseTime.Add(time.Hour)},
		Limit:    2,
		Cursor:   first.NextCursor,
	})
	if !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("cursor presented with a different range returned %v, want ErrInvalidCursor", err)
	}

	if _, err := subject.ListTelemetry(ctx, store.TelemetryQuery{
		DeviceID: "MCU001",
		Range:    store.TimeRange{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)},
		Limit:    2,
		Cursor:   "not-a-cursor",
	}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Fatalf("malformed cursor returned %v, want ErrInvalidCursor", err)
	}
}

// testLatestTelemetry verifies the newest-by-event-time read and the empty case.
func testLatestTelemetry(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	if _, err := subject.LatestTelemetry(ctx, "MCU001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("latest on an empty store returned %v, want ErrNotFound", err)
	}

	for index := 0; index < 3; index++ {
		if _, err := subject.InsertTelemetry(ctx, sample("MCU001", "boot1", uint32(index), baseTime.Add(time.Duration(index)*time.Second))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	latest, err := subject.LatestTelemetry(ctx, "MCU001")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Sequence != 2 {
		t.Fatalf("latest sequence is %d, want 2", latest.Sequence)
	}
}

// testAlertLifecycle verifies insert, advance and close.
func testAlertLifecycle(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	created, err := subject.InsertAlert(ctx, domain.AlertEvent{
		DeviceID: "MCU001", State: domain.AlertSuspect, StartedAt: baseTime, Evidence: evidence(),
	})
	if err != nil {
		t.Fatalf("insert alert: %v", err)
	}
	if created.ID == "" {
		t.Fatal("insert alert returned an empty id")
	}

	active, err := subject.ActiveAlert(ctx, "MCU001")
	if err != nil {
		t.Fatalf("active alert: %v", err)
	}
	if active.State != domain.AlertSuspect {
		t.Fatalf("active state is %q, want suspect", active.State)
	}

	advanced, err := subject.UpdateAlert(ctx, "MCU001", created.ID, domain.AlertFireWarning, evidence())
	if err != nil {
		t.Fatalf("update alert: %v", err)
	}
	if advanced.State != domain.AlertFireWarning {
		t.Fatalf("advanced state is %q, want fire_warning", advanced.State)
	}

	if err := subject.CloseAlert(ctx, "MCU001", created.ID, domain.AlertRecovered, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("close alert: %v", err)
	}
	// Closing twice is idempotent so a retried recovery cannot fail.
	if err := subject.CloseAlert(ctx, "MCU001", created.ID, domain.AlertRecovered, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := subject.ActiveAlert(ctx, "MCU001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("active alert after close returned %v, want ErrNotFound", err)
	}

	// A closed episode is history and must not be advanced.
	if _, err := subject.UpdateAlert(ctx, "MCU001", created.ID, domain.AlertFireWarning, evidence()); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("updating a closed alert returned %v, want ErrConflict", err)
	}
}

// testAlertSingleActive verifies the one-open-alert-per-device rule, which is
// what keeps ActiveAlert unambiguous.
func testAlertSingleActive(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	if _, err := subject.InsertAlert(ctx, domain.AlertEvent{
		DeviceID: "MCU001", State: domain.AlertSuspect, StartedAt: baseTime, Evidence: evidence(),
	}); err != nil {
		t.Fatalf("first alert: %v", err)
	}
	_, err := subject.InsertAlert(ctx, domain.AlertEvent{
		DeviceID: "MCU001", State: domain.AlertFireWarning, StartedAt: baseTime.Add(time.Second), Evidence: evidence(),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second concurrent alert returned %v, want ErrConflict", err)
	}
}

// testAlertPaginationAndFilter verifies ordering, cursor pagination and the
// state and active filters.
func testAlertPaginationAndFilter(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)
	window := store.TimeRange{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)}

	var ids []string
	for index := 0; index < 4; index++ {
		event := domain.AlertEvent{
			DeviceID:  "MCU001",
			State:     domain.AlertFireWarning,
			StartedAt: baseTime.Add(time.Duration(index) * time.Minute),
			Evidence:  evidence(),
		}
		created, err := subject.InsertAlert(ctx, event)
		if err != nil {
			t.Fatalf("insert alert %d: %v", index, err)
		}
		ids = append(ids, created.ID)
		// Only one episode may be open per device, so every episode but the last
		// is closed before the next one starts. That is also what the real
		// sequence looks like: an episode ends before another begins.
		if index < 3 {
			if err := subject.CloseAlert(ctx, "MCU001", created.ID, domain.AlertRecovered, event.StartedAt.Add(30*time.Second)); err != nil {
				t.Fatalf("close alert %d: %v", index, err)
			}
		}
	}

	recovered, err := subject.ListAlerts(ctx, store.AlertQuery{
		DeviceID: "MCU001", Range: window, State: domain.AlertRecovered,
	})
	if err != nil {
		t.Fatalf("filter by state: %v", err)
	}
	if len(recovered.Items) != 3 {
		t.Fatalf("state filter returned %d alerts, want 3", len(recovered.Items))
	}

	activeOnly, err := subject.ListAlerts(ctx, store.AlertQuery{
		DeviceID: "MCU001", Range: window, ActiveOnly: true,
	})
	if err != nil {
		t.Fatalf("active filter: %v", err)
	}
	if len(activeOnly.Items) != 1 {
		t.Fatalf("active filter returned %d alerts, want 1", len(activeOnly.Items))
	}

	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		result, err := subject.ListAlerts(ctx, store.AlertQuery{
			DeviceID: "MCU001", Range: window, Limit: 2, Order: store.OrderDesc, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("alert page %d: %v", page, err)
		}
		for _, item := range result.Items {
			seen = append(seen, item.ID)
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	if len(seen) != 4 {
		t.Fatalf("paged over %d alerts, want 4", len(seen))
	}
	// Descending order must start with the newest episode.
	if seen[0] != ids[3] {
		t.Fatalf("descending page started with %s, want %s", seen[0], ids[3])
	}
}

// testDeviceState verifies device upsert and read-back.
func testDeviceState(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	if _, err := subject.Device(ctx, "MCU001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("device on an empty store returned %v, want ErrNotFound", err)
	}

	state := store.DeviceState{
		DeviceID:                  "MCU001",
		LastSeenAt:                baseTime,
		LastBootID:                "boot1",
		LastSequence:              7,
		Connectivity:              domain.ConnectivityOnline,
		AlarmState:                domain.AlertSuspect,
		ActiveAlertID:             "01ABC",
		LocalAlarm:                true,
		ThresholdVersionConfirmed: 3,
		ThresholdVersionDesired:   3,
		UpdatedAt:                 baseTime,
	}
	if err := subject.UpsertDevice(ctx, state); err != nil {
		t.Fatalf("upsert device: %v", err)
	}
	state.LastSequence = 8
	state.Connectivity = domain.ConnectivityOffline
	if err := subject.UpsertDevice(ctx, state); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	readBack, err := subject.Device(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read device: %v", err)
	}
	if readBack.LastSequence != 8 || readBack.Connectivity != domain.ConnectivityOffline {
		t.Fatalf("device state did not update: %+v", readBack)
	}
	if !readBack.LocalAlarm {
		t.Fatalf("boolean flags were lost: %+v", readBack)
	}

	devices, err := subject.ListDevices(ctx)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(devices) != 1 || devices[0].DeviceID != "MCU001" {
		t.Fatalf("list devices returned %+v", devices)
	}
}

// testThresholds verifies the default fallback, the monotonic version rule and
// the confirmation state.
func testThresholds(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	initial, err := subject.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("thresholds on an unknown device: %v", err)
	}
	if initial.DesiredVersion != domain.InitialThresholdVersion {
		t.Fatalf("initial version is %d, want %d", initial.DesiredVersion, domain.InitialThresholdVersion)
	}
	if initial.Desired != domain.DefaultThresholds() {
		t.Fatalf("initial thresholds are %+v, want the compile-time defaults", initial.Desired)
	}
	if initial.ConfirmedVersion != nil {
		t.Fatalf("a device that never reported has confirmed version %v", *initial.ConfirmedVersion)
	}

	raised := domain.Thresholds{TemperatureHighC: 35, HumidityHighRh: 85, GasHighPpm: 120}
	if err := subject.SetDesiredThresholds(ctx, "MCU001", raised, domain.InitialThresholdVersion+1, baseTime); err != nil {
		t.Fatalf("set desired thresholds: %v", err)
	}
	// A version that does not move forward must be refused, or a replay could
	// undo a newer configuration.
	if err := subject.SetDesiredThresholds(ctx, "MCU001", domain.DefaultThresholds(), domain.InitialThresholdVersion, baseTime); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale version write returned %v, want ErrConflict", err)
	}

	if err := subject.ConfirmThresholdVersion(ctx, "MCU001", domain.InitialThresholdVersion+1, baseTime); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	confirmed, err := subject.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if confirmed.ConfirmedVersion == nil || *confirmed.ConfirmedVersion != domain.InitialThresholdVersion+1 {
		t.Fatalf("confirmed version is %v", confirmed.ConfirmedVersion)
	}
	if confirmed.ConfirmationState != store.ConfirmationConfirmed {
		t.Fatalf("confirmation state is %q, want confirmed", confirmed.ConfirmationState)
	}

	// An older device report must not roll the confirmed version back.
	if err := subject.ConfirmThresholdVersion(ctx, "MCU001", domain.InitialThresholdVersion, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("stale confirm: %v", err)
	}
	after, err := subject.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds again: %v", err)
	}
	if after.ConfirmedVersion == nil || *after.ConfirmedVersion != domain.InitialThresholdVersion+1 {
		t.Fatalf("a stale confirmation rolled the version back to %v", after.ConfirmedVersion)
	}
}

// thresholdsCommand builds a valid accepted set_thresholds command.
func thresholdsCommand(requestID, deviceID, key string, at time.Time) domain.Command {
	version := domain.InitialThresholdVersion
	thresholds := domain.Thresholds{TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 80}
	return domain.Command{
		RequestID:      requestID,
		DeviceID:       deviceID,
		Type:           domain.CommandSetThresholds,
		State:          domain.CommandAccepted,
		Payload:        domain.CommandPayload{Thresholds: &thresholds, ThresholdVersion: &version},
		DesiredVersion: &version,
		IdempotencyKey: key,
		AcceptedAt:     at,
		ExpiresAt:      at.Add(time.Minute),
		Actor:          "tester",
	}
}

// testCommandLifecycle verifies the accepted to published to applied path and
// that a terminal command cannot be reopened.
func testCommandLifecycle(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	command := thresholdsCommand("01REQ1", "MCU001", "01IDEM1", baseTime)
	if err := subject.InsertCommand(ctx, command); err != nil {
		t.Fatalf("insert command: %v", err)
	}

	published, err := subject.MarkCommandPublished(ctx, "MCU001", "01REQ1", baseTime.Add(time.Second))
	if err != nil {
		t.Fatalf("mark published: %v", err)
	}
	if published.State != domain.CommandPublished || published.PublishedAt == nil {
		t.Fatalf("published command is %+v", published)
	}

	applied, err := subject.ApplyCommandPatch(ctx, "MCU001", "01REQ1", domain.CommandPatch{
		State: domain.CommandApplied, CompletedAt: baseTime.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	if applied.State != domain.CommandApplied || applied.CompletedAt == nil {
		t.Fatalf("applied command is %+v", applied)
	}

	// Replaying the same patch is idempotent: MQTT delivers at least once.
	same, err := subject.ApplyCommandPatch(ctx, "MCU001", "01REQ1", domain.CommandPatch{
		State: domain.CommandApplied, CompletedAt: baseTime.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if !same.CompletedAt.Equal(*applied.CompletedAt) {
		t.Fatalf("replay moved the completion time from %s to %s", applied.CompletedAt, same.CompletedAt)
	}

	// A different terminal state is a conflict: the first recorded outcome is
	// what a client may already have observed.
	if _, err := subject.ApplyCommandPatch(ctx, "MCU001", "01REQ1", domain.CommandPatch{
		State: domain.CommandRejected, CompletedAt: baseTime.Add(4 * time.Second), ErrorCode: "out_of_range",
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflicting terminal patch returned %v, want ErrConflict", err)
	}

	if _, err := subject.Command(ctx, "MCU001", "01MISSING"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing command returned %v, want ErrNotFound", err)
	}
}

// testCommandIdempotency verifies the per-device idempotency index.
func testCommandIdempotency(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	if err := subject.InsertCommand(ctx, thresholdsCommand("01REQ1", "MCU001", "01IDEM1", baseTime)); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	found, err := subject.CommandByIdempotencyKey(ctx, "MCU001", "01IDEM1")
	if err != nil {
		t.Fatalf("lookup by idempotency key: %v", err)
	}
	if found.RequestID != "01REQ1" {
		t.Fatalf("lookup returned %s, want 01REQ1", found.RequestID)
	}

	// The same key on another device is a different logical request.
	if err := subject.InsertCommand(ctx, thresholdsCommand("01REQ2", "MCU002", "01IDEM1", baseTime)); err != nil {
		t.Fatalf("same key on another device: %v", err)
	}
	if err := subject.InsertCommand(ctx, thresholdsCommand("01REQ3", "MCU001", "01IDEM1", baseTime)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reused key returned %v, want ErrConflict", err)
	}
	if _, err := subject.CommandByIdempotencyKey(ctx, "MCU001", "01NOPE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown key returned %v, want ErrNotFound", err)
	}
}

// testCommandExpiry verifies that only unfinished, due commands expire.
func testCommandExpiry(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	subject := factory(t)

	if err := subject.InsertCommand(ctx, thresholdsCommand("01REQ1", "MCU001", "01IDEM1", baseTime)); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	if _, err := subject.MarkCommandPublished(ctx, "MCU001", "01REQ1", baseTime); err != nil {
		t.Fatalf("mark published: %v", err)
	}

	// A sweep before the expiry must not close anything.
	early, err := subject.ExpireCommands(ctx, baseTime.Add(30*time.Second))
	if err != nil {
		t.Fatalf("early sweep: %v", err)
	}
	if len(early) != 0 {
		t.Fatalf("early sweep closed %d commands", len(early))
	}

	late, err := subject.ExpireCommands(ctx, baseTime.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("late sweep: %v", err)
	}
	if len(late) != 1 || late[0].State != domain.CommandTimedOut {
		t.Fatalf("late sweep returned %+v", late)
	}

	// A second sweep must not report the same command again.
	again, err := subject.ExpireCommands(ctx, baseTime.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("repeat sweep: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("repeat sweep closed %d commands", len(again))
	}

	pending, err := subject.PendingCommands(ctx)
	if err != nil {
		t.Fatalf("pending commands: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending commands returned %+v after expiry", pending)
	}
}

// testThresholdCommandAdvancesVersion verifies that inserting a set_thresholds
// command moves the desired threshold record in the same step, which is what
// keeps GET /thresholds from reporting a version no command ever carried.
func testThresholdCommandAdvancesVersion(t *testing.T, factory Factory) {
	ctx := context.Background()
	subject := factory(t)

	desired := domain.Thresholds{TemperatureHighC: 33, HumidityHighRh: 88, GasHighPpm: 90}
	version := domain.InitialThresholdVersion + 1
	command := domain.Command{
		RequestID:      "01REQ1",
		DeviceID:       "MCU001",
		Type:           domain.CommandSetThresholds,
		State:          domain.CommandAccepted,
		Payload:        domain.CommandPayload{Thresholds: &desired, ThresholdVersion: &version},
		IdempotencyKey: "01IDEM1",
		AcceptedAt:     baseTime,
		ExpiresAt:      baseTime.Add(time.Minute),
		DesiredVersion: &version,
	}
	if err := subject.InsertCommand(ctx, command); err != nil {
		t.Fatalf("insert thresholds command: %v", err)
	}

	record, err := subject.Thresholds(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read thresholds: %v", err)
	}
	if record.DesiredVersion != version {
		t.Fatalf("desired version is %d, want %d", record.DesiredVersion, version)
	}
	if record.Desired != desired {
		t.Fatalf("desired thresholds are %+v, want %+v", record.Desired, desired)
	}
	if record.ConfirmationState != store.ConfirmationPending {
		t.Fatalf("confirmation state is %q, want pending before the device answers", record.ConfirmationState)
	}
}
