package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/BobcGn/final/backend/internal/alert"
	"github.com/BobcGn/final/backend/internal/api"
	"github.com/BobcGn/final/backend/internal/command"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/ingest"
	"github.com/BobcGn/final/backend/internal/liveness"
	"github.com/BobcGn/final/backend/internal/store"
)

// start is a fixed instant so every response body is reproducible.
var start = time.Date(2026, 9, 24, 10, 40, 30, 0, time.UTC)

// env bundles the server under test with the collaborators a test needs to
// arrange state and assert side effects.
type env struct {
	server    *httptest.Server
	store     store.Store
	ingest    *ingest.Service
	tracker   *liveness.Tracker
	publisher *publisher
	clock     time.Time
}

// publisher is a command publisher that succeeds unless told otherwise.
type publisher struct {
	mu  sync.Mutex
	err error
}

// Publish implements command.Publisher.
func (p *publisher) Publish(context.Context, string, []byte, byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// fail makes every later publish fail.
func (p *publisher) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// newEnv builds the full HTTP stack over an in-memory store.
func newEnv(t *testing.T, mutate func(*api.Config)) *env {
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
	result := &env{store: dataStore, tracker: tracker, publisher: &publisher{}, clock: start}

	hub := api.NewHub(api.Deps{
		Logger: slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return result.clock },
	})
	commandService, err := command.New(command.Deps{
		Store:     dataStore,
		Publisher: result.publisher,
		Sink:      hub,
		Logger:    slog.New(slog.DiscardHandler),
		Now:       func() time.Time { return result.clock },
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}
	ingestService, err := ingest.New(ingest.Deps{
		Store:      dataStore,
		Engine:     engine,
		Tracker:    tracker,
		Sink:       hub,
		AckHandler: commandService,
		Logger:     slog.New(slog.DiscardHandler),
		Now:        func() time.Time { return result.clock },
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	result.ingest = ingestService

	cfg := api.Config{
		Store:        dataStore,
		Ingest:       ingestService,
		Commands:     commandService,
		Hub:          hub,
		Tracker:      tracker,
		Logger:       slog.New(slog.DiscardHandler),
		Now:          func() time.Time { return result.clock },
		OfflineAfter: 15 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	server, err := api.NewServer(cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	result.server = httptest.NewServer(server)
	t.Cleanup(result.server.Close)
	return result
}

// do issues a request against the test server.
func (e *env) do(t *testing.T, method, path string, body string, headers map[string]string) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

// decode reads and unmarshals a response body.
func decode(t *testing.T, response *http.Response, target any) {
	t.Helper()

	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}
}

// errorCode extracts the error code from a response body.
func errorCode(t *testing.T, response *http.Response) string {
	t.Helper()

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decode(t, response, &envelope)
	return envelope.Error.Code
}

// seedTelemetry pushes a valid sample through the ingest path.
func (e *env) seedTelemetry(t *testing.T, mutate func(map[string]any)) {
	t.Helper()

	fields := map[string]any{
		"schemaVersion":    1,
		"messageType":      "telemetry",
		"deviceId":         "MCU001",
		"bootId":           "9f3ac21b",
		"sequence":         42,
		"timestamp":        e.clock.UnixMilli(),
		"uptimeMs":         125000,
		"temperatureC":     28.0,
		"humidityRh":       61.0,
		"gasAdcRaw":        1350,
		"gasAdcFiltered":   1328,
		"gasPpm":           25.0,
		"gasCalibrated":    false,
		"localAlarm":       true,
		"alarmCauses":      []string{"gas_high"},
		"buzzerMuted":      false,
		"network":          "online",
		"thresholdVersion": 1,
		"sensorFault":      false,
	}
	if mutate != nil {
		mutate(fields)
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal telemetry: %v", err)
	}
	if err := e.ingest.HandleTelemetry(context.Background(), raw); err != nil {
		t.Fatalf("seed telemetry: %v", err)
	}
}

// TestHealthIsUnauthenticated verifies that liveness needs no credential and
// returns only a constant.
func TestHealthIsUnauthenticated(t *testing.T) {
	e := newEnv(t, func(cfg *api.Config) {
		cfg.AuthMode = api.AuthBearer
		cfg.Tokens = map[string]string{"secret": "operator"}
	})

	response := e.do(t, http.MethodGet, "/healthz", "", nil)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
	}
	decode(t, e.do(t, http.MethodGet, "/healthz", "", nil), &body)
	if body.Status != "ok" {
		t.Fatalf("body = %+v", body)
	}
}

// TestRoutesRequireABearerToken verifies the authentication boundary on every
// business route.
func TestRoutesRequireABearerToken(t *testing.T) {
	e := newEnv(t, func(cfg *api.Config) {
		cfg.AuthMode = api.AuthBearer
		cfg.Tokens = map[string]string{"secret": "operator"}
	})

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/devices/MCU001/status"},
		{http.MethodGet, "/api/v1/devices/MCU001/telemetry/latest"},
		{http.MethodGet, "/api/v1/devices/MCU001/telemetry"},
		{http.MethodGet, "/api/v1/devices/MCU001/alerts"},
		{http.MethodGet, "/api/v1/devices/MCU001/thresholds"},
		{http.MethodPut, "/api/v1/devices/MCU001/thresholds"},
		{http.MethodPost, "/api/v1/devices/MCU001/commands/mute"},
		{http.MethodGet, "/api/v1/devices/MCU001/commands/01REQ"},
		{http.MethodGet, "/ws/v1/devices/MCU001/telemetry"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := e.do(t, route.method, route.path, "", nil)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.StatusCode)
			}
			if code := errorCode(t, e.do(t, route.method, route.path, "", nil)); code != "unauthenticated" {
				t.Fatalf("code = %q, want unauthenticated", code)
			}
		})
	}

	// A wrong token is rejected too.
	response := e.do(t, http.MethodGet, "/api/v1/devices/MCU001/status", "", map[string]string{
		"Authorization": "Bearer wrong",
	})
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong token returned %d, want 401", response.StatusCode)
	}

	// The right token is accepted.
	response = e.do(t, http.MethodGet, "/api/v1/devices/MCU001/thresholds", "", map[string]string{
		"Authorization": "Bearer secret",
	})
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a valid token returned %d, want 200", response.StatusCode)
	}
}

// TestDeviceStatusBeforeAndAfterTelemetry verifies the 404-then-200 transition
// that provisioning relies on.
func TestDeviceStatusBeforeAndAfterTelemetry(t *testing.T) {
	e := newEnv(t, nil)

	response := e.do(t, http.MethodGet, "/api/v1/devices/MCU001/status", "", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status before telemetry = %d, want 404", response.StatusCode)
	}
	if code := errorCode(t, response); code != "device_not_found" {
		t.Fatalf("code = %q, want device_not_found", code)
	}

	e.seedTelemetry(t, nil)

	var status struct {
		DeviceID            string `json:"deviceId"`
		Connectivity        string `json:"connectivity"`
		AlarmState          string `json:"alarmState"`
		LocalAlarm          bool   `json:"localAlarm"`
		BuzzerMuted         bool   `json:"buzzerMuted"`
		OfflineAfterSeconds int    `json:"offlineAfterSeconds"`
		LastSeenAt          string `json:"lastSeenAt"`
		ThresholdVersion    struct {
			Desired   int  `json:"desired"`
			Confirmed *int `json:"confirmed"`
		} `json:"thresholdVersion"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/status", "", nil), &status)

	if status.DeviceID != "MCU001" || status.Connectivity != "online" {
		t.Fatalf("status = %+v", status)
	}
	if status.AlarmState != "normal" {
		t.Fatalf("alarmState = %q, want normal for a single sample", status.AlarmState)
	}
	if !status.LocalAlarm {
		t.Fatal("the device-reported local alarm was lost")
	}
	if status.OfflineAfterSeconds != 15 {
		t.Fatalf("offlineAfterSeconds = %d, want 15", status.OfflineAfterSeconds)
	}
	// The device reports threshold version 1, which equals the stored default, so
	// the version is confirmed.
	if status.ThresholdVersion.Confirmed == nil || *status.ThresholdVersion.Confirmed != 1 {
		t.Fatalf("threshold version = %+v", status.ThresholdVersion)
	}
	// lastSeenAt must be RFC 3339 with second precision.
	if _, err := time.Parse(time.RFC3339, status.LastSeenAt); err != nil {
		t.Fatalf("lastSeenAt %q is not RFC 3339: %v", status.LastSeenAt, err)
	}
}

// TestLatestTelemetryShape verifies the response fields and units.
func TestLatestTelemetryShape(t *testing.T) {
	e := newEnv(t, nil)

	if response := e.do(t, http.MethodGet, "/api/v1/devices/MCU001/telemetry/latest", "", nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("latest before telemetry = %d, want 404", response.StatusCode)
	} else {
		_ = response.Body.Close()
	}

	e.seedTelemetry(t, func(fields map[string]any) { fields["timestamp"] = nil })

	var point struct {
		DeviceID       string   `json:"deviceId"`
		BootID         string   `json:"bootId"`
		Sequence       uint32   `json:"sequence"`
		Timestamp      *string  `json:"timestamp"`
		ReceivedAt     string   `json:"receivedAt"`
		TemperatureC   float64  `json:"temperatureC"`
		HumidityRh     float64  `json:"humidityRh"`
		GasAdcRaw      int      `json:"gasAdcRaw"`
		GasAdcFiltered int      `json:"gasAdcFiltered"`
		GasPpm         *float64 `json:"gasPpm"`
		LocalAlarm     bool     `json:"localAlarm"`
		AlarmCauses    []string `json:"alarmCauses"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/telemetry/latest", "", nil), &point)

	if point.DeviceID != "MCU001" || point.BootID != "9f3ac21b" || point.Sequence != 42 {
		t.Fatalf("identity = %+v", point)
	}
	// A device with an unsynced clock reports null, and the backend still fills
	// its own receive time.
	if point.Timestamp != nil {
		t.Fatalf("timestamp = %v, want null", *point.Timestamp)
	}
	if point.ReceivedAt == "" {
		t.Fatal("receivedAt is empty")
	}
	if point.GasAdcRaw != 1350 || point.GasAdcFiltered != 1328 {
		t.Fatalf("gas = %d / %d", point.GasAdcRaw, point.GasAdcFiltered)
	}
	if len(point.AlarmCauses) != 1 || point.AlarmCauses[0] != "gas_high" {
		t.Fatalf("alarmCauses = %v", point.AlarmCauses)
	}
}

// TestTelemetryHistoryPagination verifies the page shape, the stable ordering
// and the cursor round-trip end to end.
func TestTelemetryHistoryPagination(t *testing.T) {
	e := newEnv(t, nil)

	for index := 0; index < 5; index++ {
		e.clock = start.Add(time.Duration(index) * 10 * time.Second)
		e.seedTelemetry(t, func(fields map[string]any) {
			fields["sequence"] = index + 1
			fields["timestamp"] = e.clock.UnixMilli()
		})
	}
	e.clock = start.Add(time.Minute)

	from := start.Add(-time.Hour).Format(time.RFC3339)
	to := start.Add(time.Hour).Format(time.RFC3339)

	var page struct {
		Items []struct {
			Sequence uint32 `json:"sequence"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}
	path := "/api/v1/devices/MCU001/telemetry?from=" + from + "&to=" + to + "&limit=2&order=asc"
	decode(t, e.do(t, http.MethodGet, path, "", nil), &page)

	if len(page.Items) != 2 || page.Items[0].Sequence != 1 || page.Items[1].Sequence != 2 {
		t.Fatalf("first page = %+v", page.Items)
	}
	if page.NextCursor == nil {
		t.Fatal("a full page did not return a cursor")
	}

	// The cursor must be repeatable and must continue where the page ended.
	var second struct {
		Items []struct {
			Sequence uint32 `json:"sequence"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}
	next := path + "&cursor=" + *page.NextCursor
	decode(t, e.do(t, http.MethodGet, next, "", nil), &second)
	if len(second.Items) != 2 || second.Items[0].Sequence != 3 {
		t.Fatalf("second page = %+v", second.Items)
	}

	// Presenting the cursor without repeating the range is a 400, not a silently
	// different series.
	response := e.do(t, http.MethodGet, "/api/v1/devices/MCU001/telemetry?limit=2&cursor="+*page.NextCursor, "", nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("a cursor without its range returned %d, want 400", response.StatusCode)
	}
	_ = response.Body.Close()

	// The last page reports no further cursor.
	var last struct {
		NextCursor *string `json:"nextCursor"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/telemetry?from="+from+"&to="+to+"&limit=100", "", nil), &last)
	if last.NextCursor != nil {
		t.Fatalf("a short page returned cursor %q", *last.NextCursor)
	}
}

// TestQueryParameterValidation verifies the documented 400 cases.
func TestQueryParameterValidation(t *testing.T) {
	e := newEnv(t, nil)
	base := "/api/v1/devices/MCU001/telemetry?"

	cases := map[string]string{
		"unparsable from":    base + "from=yesterday",
		"unparsable to":      base + "to=soon",
		"limit zero":         base + "limit=0",
		"limit too large":    base + "limit=1001",
		"limit not a number": base + "limit=many",
		"order unknown":      base + "order=sideways",
		"reversed range":     base + "from=2026-09-24T12:00:00Z&to=2026-09-24T11:00:00Z",
		"range too long":     base + "from=2026-01-01T00:00:00Z&to=2026-09-24T00:00:00Z",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			response := e.do(t, http.MethodGet, path, "", nil)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
			if code := errorCode(t, response); code != "invalid_request" {
				t.Fatalf("code = %q, want invalid_request", code)
			}
		})
	}
}

// TestInvalidDeviceIDIsRejected verifies the path parameter contract.
func TestInvalidDeviceIDIsRejected(t *testing.T) {
	e := newEnv(t, nil)

	for _, deviceID := range []string{"has%20space", "has%2Fslash", strings.Repeat("a", 33)} {
		response := e.do(t, http.MethodGet, "/api/v1/devices/"+deviceID+"/status", "", nil)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("deviceID %q returned %d, want 400", deviceID, response.StatusCode)
		}
		if code := errorCode(t, response); code != "invalid_request" {
			t.Fatalf("deviceID %q code = %q, want invalid_request", deviceID, code)
		}
	}
}

// TestThresholdsGetAndPut verifies the desired/confirmed distinction end to end.
func TestThresholdsGetAndPut(t *testing.T) {
	e := newEnv(t, nil)

	var initial struct {
		DesiredVersion    int     `json:"desiredVersion"`
		ConfirmedVersion  *int    `json:"confirmedVersion"`
		TemperatureHighC  float64 `json:"temperatureHighC"`
		HumidityHighRh    float64 `json:"humidityHighRh"`
		GasHighPpm        float64 `json:"gasHighPpm"`
		ConfirmationState string  `json:"confirmationState"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/thresholds", "", nil), &initial)

	// Before the device ever reports, the honest answer is the firmware defaults
	// at version 1 with nothing confirmed.
	if initial.DesiredVersion != 1 || initial.ConfirmedVersion != nil {
		t.Fatalf("initial = %+v", initial)
	}
	if initial.TemperatureHighC != 30 || initial.HumidityHighRh != 80 || initial.GasHighPpm != 20 {
		t.Fatalf("the defaults were not reported: %+v", initial)
	}

	body := `{"temperatureHighC":35,"humidityHighRh":85,"gasHighPpm":120}`
	response := e.do(t, http.MethodPut, "/api/v1/devices/MCU001/thresholds", body, map[string]string{
		"Idempotency-Key": "01IDEMPOTENCY",
	})
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}
	var accepted struct {
		RequestID      string `json:"requestId"`
		Status         string `json:"status"`
		DesiredVersion int    `json:"desiredVersion"`
		ExpiresAt      string `json:"expiresAt"`
	}
	decode(t, response, &accepted)
	if accepted.Status != "pending" {
		t.Fatalf("status field = %q, want pending: the device has not answered", accepted.Status)
	}
	if accepted.DesiredVersion != 2 {
		t.Fatalf("desired version = %d, want 2", accepted.DesiredVersion)
	}

	var after struct {
		DesiredVersion    int     `json:"desiredVersion"`
		ConfirmedVersion  *int    `json:"confirmedVersion"`
		ConfirmationState string  `json:"confirmationState"`
		GasHighPpm        float64 `json:"gasHighPpm"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/thresholds", "", nil), &after)
	if after.DesiredVersion != 2 || after.GasHighPpm != 120 {
		t.Fatalf("after the update = %+v", after)
	}
	// The desired value must never be presented as confirmed.
	if after.ConfirmedVersion != nil {
		t.Fatalf("confirmed version = %v before the device answered", *after.ConfirmedVersion)
	}
	if after.ConfirmationState != "pending" {
		t.Fatalf("confirmation state = %q, want pending", after.ConfirmationState)
	}
}

// TestThresholdUpdateErrors covers the documented control failures.
func TestThresholdUpdateErrors(t *testing.T) {
	e := newEnv(t, nil)
	path := "/api/v1/devices/MCU001/thresholds"

	cases := map[string]struct {
		body   string
		header map[string]string
		status int
		code   string
	}{
		"missing idempotency key": {
			body:   `{"temperatureHighC":30,"humidityHighRh":80,"gasHighPpm":80}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		"value out of range": {
			body:   `{"temperatureHighC":200,"humidityHighRh":80,"gasHighPpm":80}`,
			header: map[string]string{"Idempotency-Key": "01A"},
			status: http.StatusUnprocessableEntity,
			code:   "invalid_threshold",
		},
		"missing field": {
			body:   `{"temperatureHighC":30,"gasHighPpm":80}`,
			header: map[string]string{"Idempotency-Key": "01B"},
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		"unknown field": {
			body:   `{"temperatureHighC":30,"humidityHighRh":80,"gasHighPpm":80,"buzzer":true}`,
			header: map[string]string{"Idempotency-Key": "01C"},
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		"malformed JSON": {
			body:   `{"temperatureHighC":`,
			header: map[string]string{"Idempotency-Key": "01D"},
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := e.do(t, http.MethodPut, path, testCase.body, testCase.header)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != testCase.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, testCase.status)
			}
			if code := errorCode(t, response); code != testCase.code {
				t.Fatalf("code = %q, want %q", code, testCase.code)
			}
		})
	}
}

// TestIdempotencyConflictIsA409 verifies the frozen conflict mapping.
func TestIdempotencyConflictIsA409(t *testing.T) {
	e := newEnv(t, nil)
	path := "/api/v1/devices/MCU001/thresholds"
	headers := map[string]string{"Idempotency-Key": "01IDEM"}

	first := e.do(t, http.MethodPut, path, `{"temperatureHighC":35,"humidityHighRh":85,"gasHighPpm":120}`, headers)
	_ = first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first request = %d, want 202", first.StatusCode)
	}

	// Replaying the same key with the same payload returns the original command
	// rather than publishing again.
	replay := e.do(t, http.MethodPut, path, `{"temperatureHighC":35,"humidityHighRh":85,"gasHighPpm":120}`, headers)
	var replayed struct {
		RequestID string `json:"requestId"`
	}
	decode(t, replay, &replayed)
	var original struct {
		RequestID string `json:"requestId"`
	}
	decode(t, e.do(t, http.MethodPut, path, `{"temperatureHighC":35,"humidityHighRh":85,"gasHighPpm":120}`, headers), &original)
	if replayed.RequestID != original.RequestID {
		t.Fatalf("a replay returned a different command: %s vs %s", replayed.RequestID, original.RequestID)
	}

	// The same key with a different payload is a conflict.
	conflict := e.do(t, http.MethodPut, path, `{"temperatureHighC":36,"humidityHighRh":85,"gasHighPpm":120}`, headers)
	defer func() { _ = conflict.Body.Close() }()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409", conflict.StatusCode)
	}
	if code := errorCode(t, conflict); code != "version_conflict" {
		t.Fatalf("code = %q, want version_conflict", code)
	}
}

// TestMuteRoute verifies the command acceptance and its documented meaning.
func TestMuteRoute(t *testing.T) {
	e := newEnv(t, nil)

	var accepted struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	decode(t, e.do(t, http.MethodPost, "/api/v1/devices/MCU001/commands/mute",
		`{"muted":true}`, map[string]string{"Idempotency-Key": "01MUTE"}), &accepted)

	if accepted.RequestID == "" || accepted.Status != "pending" {
		t.Fatalf("accepted = %+v", accepted)
	}

	// The command resource reports the lifecycle state, and it is not applied:
	// only a device acknowledgement can say that.
	var status struct {
		RequestID string `json:"requestId"`
		Type      string `json:"type"`
		State     string `json:"state"`
		ExpiresAt string `json:"expiresAt"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/commands/"+accepted.RequestID, "", nil), &status)
	if status.State != "published" {
		t.Fatalf("state = %q, want published", status.State)
	}
	if status.Type != "set_mute" {
		t.Fatalf("type = %q", status.Type)
	}

	// An unknown command is a 404 with its own code, distinct from a missing
	// device.
	missing := e.do(t, http.MethodGet, "/api/v1/devices/MCU001/commands/01MISSING", "", nil)
	defer func() { _ = missing.Body.Close() }()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", missing.StatusCode)
	}
	if code := errorCode(t, missing); code != "command_not_found" {
		t.Fatalf("code = %q, want command_not_found", code)
	}
}

// TestBrokerFailureIsA503 verifies that a command the broker never took is
// reported as such instead of being recorded as accepted.
func TestBrokerFailureIsA503(t *testing.T) {
	e := newEnv(t, nil)
	e.publisher.fail(errors.New("broker down"))

	response := e.do(t, http.MethodPost, "/api/v1/devices/MCU001/commands/mute",
		`{"muted":true}`, map[string]string{"Idempotency-Key": "01MUTE"})
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}
	if code := errorCode(t, response); code != "broker_unavailable" {
		t.Fatalf("code = %q, want broker_unavailable", code)
	}
}

// TestAlertsListingAndFilters verifies the alert route, including the exact
// evidence key names written on the wire. The raw-key assertion is deliberate:
// encoding/json accepts PascalCase input for a camelCase-tagged Go field, which
// is how the original contract violation escaped a struct-based test.
func TestAlertsListingAndFilters(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	// Drive the composite rule: eight samples over 35 seconds with a gas surge
	// and a rising temperature.
	for index := 0; index < 8; index++ {
		offset := time.Duration(index) * 5 * time.Second
		e.clock = start.Add(offset)
		adc := 1000
		if index == 7 {
			adc = 1600
		}
		e.seedTelemetry(t, func(fields map[string]any) {
			fields["sequence"] = index + 1
			fields["timestamp"] = e.clock.UnixMilli()
			fields["gasAdcRaw"] = adc
			fields["gasAdcFiltered"] = adc
			fields["temperatureC"] = 25 + offset.Minutes()*6
			fields["localAlarm"] = false
			fields["alarmCauses"] = []string{}
		})
	}
	e.clock = start.Add(time.Minute)

	// The device state must reflect the episode.
	var status struct {
		AlarmState string `json:"alarmState"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/status", "", nil), &status)
	if status.AlarmState != "fire_warning" {
		t.Fatalf("alarmState = %q, want fire_warning", status.AlarmState)
	}

	var page struct {
		Items []struct {
			ID        string  `json:"id"`
			State     string  `json:"state"`
			StartedAt string  `json:"startedAt"`
			EndedAt   *string `json:"endedAt"`
			Evidence  struct {
				GasAdcRise                         int     `json:"gasAdcRise"`
				GasAdcRiseThreshold                int     `json:"gasAdcRiseThreshold"`
				TemperatureRateCPerMinute          float64 `json:"temperatureRateCPerMinute"`
				TemperatureRateThresholdCPerMinute float64 `json:"temperatureRateThresholdCPerMinute"`
				SampleCount                        int     `json:"sampleCount"`
				WindowSeconds                      int     `json:"windowSeconds"`
			} `json:"evidence"`
		} `json:"items"`
		NextCursor *string `json:"nextCursor"`
	}
	decodeAlertPage(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/alerts", "", nil), &page)

	if len(page.Items) != 1 {
		t.Fatalf("alerts = %+v", page.Items)
	}
	event := page.Items[0]
	if event.State != "fire_warning" || event.EndedAt != nil {
		t.Fatalf("event = %+v", event)
	}
	if event.Evidence.GasAdcRise != 600 || event.Evidence.SampleCount != 8 {
		t.Fatalf("evidence = %+v", event.Evidence)
	}
	if event.Evidence.TemperatureRateCPerMinute < 5.9 || event.Evidence.TemperatureRateCPerMinute > 6.1 {
		t.Fatalf("temperature rate = %v, want about 6 °C/min", event.Evidence.TemperatureRateCPerMinute)
	}

	// The active filter keeps it; a state filter for a different state drops it.
	var active struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeAlertPage(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/alerts?active=true", "", nil), &active)
	if len(active.Items) != 1 {
		t.Fatalf("active filter returned %d alerts", len(active.Items))
	}

	var recovered struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeAlertPage(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/alerts?state=recovered", "", nil), &recovered)
	if len(recovered.Items) != 0 {
		t.Fatalf("state filter returned %d alerts, want none", len(recovered.Items))
	}

	// The stored evidence is what the alert describes, not a recomputation.
	stored, err := e.store.ActiveAlert(ctx, "MCU001")
	if err != nil {
		t.Fatalf("read the active alert: %v", err)
	}
	if stored.ID != event.ID {
		t.Fatalf("the API reported %s while the store holds %s", event.ID, stored.ID)
	}
}

// decodeAlertPage reads a real GET /alerts response, checks its raw JSON keys,
// then decodes the same bytes into the caller's semantic assertion type.
func decodeAlertPage(t *testing.T, response *http.Response, target any) {
	t.Helper()

	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read alert page: %v", err)
	}
	var page struct {
		Items []struct {
			Evidence map[string]json.RawMessage `json:"evidence"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode raw alert page %s: %v", raw, err)
	}
	want := map[string]bool{
		"gasAdcRise": true, "gasAdcRiseThreshold": true,
		"temperatureRateCPerMinute": true, "temperatureRateThresholdCPerMinute": true,
		"sampleCount": true, "windowSeconds": true,
	}
	for _, item := range page.Items {
		if len(item.Evidence) != len(want) {
			t.Errorf("evidence keys = %v, want exactly the six contract keys", item.Evidence)
		}
		for key := range want {
			if _, ok := item.Evidence[key]; !ok {
				t.Errorf("evidence is missing contract key %q; got %v", key, item.Evidence)
			}
		}
		for key := range item.Evidence {
			if !want[key] {
				t.Errorf("evidence contains non-contract key %q", key)
			}
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode alert page %s: %v", raw, err)
	}
}

// TestWebSocketStreamsTelemetry verifies the realtime path over a real socket.
func TestWebSocketStreamsTelemetry(t *testing.T) {
	e := newEnv(t, nil)
	e.seedTelemetry(t, nil)

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(e.server.URL, "http")+"/ws/v1/devices/MCU001/telemetry", nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("dial failed with status %d: %v", status, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// A new sample must arrive as an envelope.
	e.clock = start.Add(5 * time.Second)
	e.seedTelemetry(t, func(fields map[string]any) {
		fields["sequence"] = 43
		fields["timestamp"] = e.clock.UnixMilli()
	})

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var envelope struct {
		Type       string `json:"type"`
		EventID    string `json:"eventId"`
		OccurredAt string `json:"occurredAt"`
		DeviceID   string `json:"deviceId"`
		Data       struct {
			Sequence     uint32  `json:"sequence"`
			TemperatureC float64 `json:"temperatureC"`
		} `json:"data"`
	}
	if err := conn.ReadJSON(&envelope); err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if envelope.Type != "telemetry.updated" {
		t.Fatalf("type = %q, want telemetry.updated", envelope.Type)
	}
	if envelope.EventID == "" || envelope.DeviceID != "MCU001" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Data.Sequence != 43 {
		t.Fatalf("sequence = %d, want 43", envelope.Data.Sequence)
	}
	if _, err := time.Parse(time.RFC3339, envelope.OccurredAt); err != nil {
		t.Fatalf("occurredAt %q is not RFC 3339: %v", envelope.OccurredAt, err)
	}
}

// TestWebSocketRejectsAnUnknownDevice verifies that subscribing to a device that
// has never reported is an actionable 404 rather than an empty stream.
func TestWebSocketRejectsAnUnknownDevice(t *testing.T) {
	e := newEnv(t, nil)

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(e.server.URL, "http")+"/ws/v1/devices/MCU999/telemetry", nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("the upgrade succeeded for an unknown device")
	}
	if response == nil || response.StatusCode != http.StatusNotFound {
		got := 0
		if response != nil {
			got = response.StatusCode
		}
		t.Fatalf("status = %d, want 404", got)
	}
	_ = response.Body.Close()
}

// TestWebSocketRequiresAuth verifies that authentication is enforced before the
// upgrade rather than after, so a client is not handed a stream it may not read.
func TestWebSocketRequiresAuth(t *testing.T) {
	e := newEnv(t, func(cfg *api.Config) {
		cfg.AuthMode = api.AuthBearer
		cfg.Tokens = map[string]string{"secret": "operator"}
	})
	e.seedTelemetry(t, nil)

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(e.server.URL, "http")+"/ws/v1/devices/MCU001/telemetry", nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("the upgrade succeeded without a token")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		got := 0
		if response != nil {
			got = response.StatusCode
		}
		t.Fatalf("status = %d, want 401", got)
	}
	_ = response.Body.Close()

	// With the token the same request succeeds.
	header := http.Header{}
	header.Set("Authorization", "Bearer secret")
	conn, response, err = dialer.Dial("ws"+strings.TrimPrefix(e.server.URL, "http")+"/ws/v1/devices/MCU001/telemetry", header)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("authenticated dial failed with status %d: %v", status, err)
	}
	_ = conn.Close()
}

// TestWebSocketSlowClientIsDropped verifies that a client which stops reading is
// disconnected instead of growing the server's memory without bound.
func TestWebSocketSlowClientIsDropped(t *testing.T) {
	e := newEnv(t, nil)
	e.seedTelemetry(t, nil)

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(e.server.URL, "http")+"/ws/v1/devices/MCU001/telemetry", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Stop reading and flood the stream well past the queue bound. The server
	// must close the connection rather than buffer indefinitely.
	for index := 0; index < 400; index++ {
		e.clock = start.Add(time.Duration(index+1) * time.Second)
		e.seedTelemetry(t, func(fields map[string]any) {
			fields["sequence"] = index + 100
			fields["timestamp"] = e.clock.UnixMilli()
		})
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	closed := false
	for attempt := 0; attempt < 500; attempt++ {
		if _, _, err := conn.ReadMessage(); err != nil {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatal("the server kept streaming to a client that never read")
	}
}

// TestRouteTableMatchesTheContract is the machine-checked half of the contract
// test: the routes the server registers must be exactly the routes the frozen
// OpenAPI document declares.
func TestRouteTableMatchesTheContract(t *testing.T) {
	e := newEnv(t, nil)

	// The document is the fact source, so the expected list is written out here
	// and cross-checked against docs/api/openapi.yaml by the contract test in the
	// main package.
	expected := map[string]bool{
		"GET /healthz":                                        true,
		"GET /api/v1/devices/{deviceId}/status":               true,
		"GET /api/v1/devices/{deviceId}/telemetry/latest":     true,
		"GET /api/v1/devices/{deviceId}/telemetry":            true,
		"GET /api/v1/devices/{deviceId}/alerts":               true,
		"GET /api/v1/devices/{deviceId}/thresholds":           true,
		"PUT /api/v1/devices/{deviceId}/thresholds":           true,
		"POST /api/v1/devices/{deviceId}/commands/mute":       true,
		"GET /api/v1/devices/{deviceId}/commands/{requestId}": true,
		"GET /ws/v1/devices/{deviceId}/telemetry":             true,
	}

	server, err := api.NewServer(api.Config{
		Store:    e.store,
		Ingest:   e.ingest,
		Commands: nil,
		Tracker:  e.tracker,
	})
	if err == nil {
		t.Fatal("a server was built without a command service")
	}
	_ = server

	for _, route := range registeredRoutes(t, e) {
		key := route.Method + " " + route.Path
		if !expected[key] {
			t.Errorf("the server registers %s, which the contract does not declare", key)
		}
		delete(expected, key)
	}
	for key := range expected {
		t.Errorf("the contract declares %s, which the server does not register", key)
	}
}

// registeredRoutes reads the route table from a server built from the
// environment's dependencies.
func registeredRoutes(t *testing.T, e *env) []api.Route {
	t.Helper()

	commandService, err := command.New(command.Deps{
		Store:     e.store,
		Publisher: e.publisher,
		Logger:    slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}
	server, err := api.NewServer(api.Config{
		Store:    e.store,
		Ingest:   e.ingest,
		Commands: commandService,
		Hub:      api.NewHub(api.Deps{Logger: slog.New(slog.DiscardHandler)}),
		Tracker:  e.tracker,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server.Routes()
}

// TestUnknownRouteIsNotFound verifies the default ServeMux behaviour is not
// replaced by something that would invent a response.
func TestUnknownRouteIsNotFound(t *testing.T) {
	e := newEnv(t, nil)

	response := e.do(t, http.MethodGet, "/api/v1/nonsense", "", nil)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

// TestWrongMethodIsRejected verifies that a route only answers its declared verb.
func TestWrongMethodIsRejected(t *testing.T) {
	e := newEnv(t, nil)

	response := e.do(t, http.MethodDelete, "/api/v1/devices/MCU001/thresholds", "", nil)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 405 or 404", response.StatusCode)
	}
}

// TestThresholdsAreNotReturnedBeforeTheDeviceAnswers is the honesty check: the
// desired value must never be presented as confirmed.
func TestThresholdsAreNotReturnedBeforeTheDeviceAnswers(t *testing.T) {
	e := newEnv(t, nil)

	response := e.do(t, http.MethodPut, "/api/v1/devices/MCU001/thresholds",
		`{"temperatureHighC":35,"humidityHighRh":85,"gasHighPpm":120}`,
		map[string]string{"Idempotency-Key": "01IDEM"})
	_ = response.Body.Close()

	var record struct {
		DesiredVersion    int     `json:"desiredVersion"`
		ConfirmedVersion  *int    `json:"confirmedVersion"`
		ConfirmationState string  `json:"confirmationState"`
		TemperatureHighC  float64 `json:"temperatureHighC"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/thresholds", "", nil), &record)

	if record.DesiredVersion != 2 {
		t.Fatalf("desiredVersion = %d, want 2", record.DesiredVersion)
	}
	if record.ConfirmedVersion != nil {
		t.Fatalf("confirmedVersion = %v, want null until the device answers", *record.ConfirmedVersion)
	}
	if record.ConfirmationState != "pending" {
		t.Fatalf("confirmationState = %q, want pending", record.ConfirmationState)
	}
	// The desired values are still reported, because that is what the operator
	// asked for and the field name says so.
	if record.TemperatureHighC != 35 {
		t.Fatalf("temperatureHighC = %v, want the desired value", record.TemperatureHighC)
	}
}

// TestInvalidAuthModeIsRejected covers the construction guard.
func TestInvalidAuthModeIsRejected(t *testing.T) {
	e := newEnv(t, nil)
	commandService, err := command.New(command.Deps{
		Store:     e.store,
		Publisher: e.publisher,
		Logger:    slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}
	if _, err := api.NewServer(api.Config{
		Store:    e.store,
		Ingest:   e.ingest,
		Commands: commandService,
		Hub:      api.NewHub(api.Deps{Logger: slog.New(slog.DiscardHandler)}),
		Tracker:  e.tracker,
		AuthMode: "magic",
	}); err == nil {
		t.Fatal("an unknown auth mode was accepted")
	}
}

// TestAlertStateIsNormalForAQuietDevice verifies that a device which never
// alarmed is not reported as recovered.
func TestAlertStateIsNormalForAQuietDevice(t *testing.T) {
	e := newEnv(t, nil)
	e.seedTelemetry(t, func(fields map[string]any) {
		fields["localAlarm"] = false
		fields["alarmCauses"] = []string{}
	})

	var status struct {
		AlarmState string `json:"alarmState"`
	}
	decode(t, e.do(t, http.MethodGet, "/api/v1/devices/MCU001/status", "", nil), &status)
	if status.AlarmState != string(domain.AlertNormal) {
		t.Fatalf("alarmState = %q, want normal", status.AlarmState)
	}
}
