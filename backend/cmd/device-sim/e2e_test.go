package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/alert"
	"github.com/BobcGn/final/backend/internal/app"
	"github.com/BobcGn/final/backend/internal/config"
	"github.com/BobcGn/final/backend/internal/mqtt/mqtttest"
	"github.com/BobcGn/final/backend/internal/store"
)

/*
These tests run the whole backend against a real broker and a simulated device.
The only thing missing from the deployed path is EMQX itself: the broker here is
the in-process one, which speaks the same protocol over a real socket, and the
store is the in-memory implementation. docs/integration-testing.md describes the
same run against EMQX and PostgreSQL, which is what the acceptance run uses.

What they demonstrate is that the contract holds end to end: the simulator emits
exactly the payloads the firmware must emit, the backend accepts and stores them,
the composite rule fires on a two-factor rise, and a control command issued over
REST reaches the device and comes back as an acknowledgement.
*/

// testEnvironment is a running backend plus the broker it is attached to.
type testEnvironment struct {
	baseURL string
	store   store.Store
	broker  *mqtttest.Broker
}

// startEnvironment starts the broker and the backend, and returns when the HTTP
// listener is up.
func startEnvironment(t *testing.T, alertConfig alert.Config, faults Faults) *testEnvironment {
	t.Helper()

	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	cfg := config.Config{
		Addr:                "127.0.0.1:0",
		BrokerURL:           broker.Addr(),
		MQTTClientID:        "lab-backend",
		MQTTKeepAlive:       5 * time.Second,
		AuthMode:            "none",
		OfflineAfter:        15 * time.Second,
		CommandTTL:          60 * time.Second,
		SweepInterval:       time.Hour,
		Alert:               alertConfig,
		MaxWebSocketClients: 8,
	}

	dataStore := store.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			Config: cfg,
			Logger: slog.New(slog.DiscardHandler),
			Store:  dataStore,
			Ready:  func(address string) { ready <- address },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("the backend did not stop within the timeout")
		}
	})

	select {
	case address := <-ready:
		return &testEnvironment{baseURL: "http://" + address, store: dataStore, broker: broker}
	case err := <-done:
		t.Fatalf("the backend stopped before it was ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the backend never reported a bound address")
	}
	// Unreachable: t.Fatal stops the test goroutine, but the compiler needs the
	// function to end on a return.
	return nil
}

// startSimulator starts a simulated device attached to the environment's broker.
func startSimulator(t *testing.T, env *testEnvironment, scenario func(int) Sample, faults Faults) *Simulator {
	t.Helper()
	return startSimulatorAs(t, env, "MCU001", "sim00001", scenario, faults)
}

// startSimulatorAs starts a simulated device with an explicit identity, so one
// environment can host several devices.
func startSimulatorAs(t *testing.T, env *testEnvironment, deviceID, bootID string, scenario func(int) Sample, faults Faults) *Simulator {
	t.Helper()

	simulator, err := New(Config{
		BrokerAddress: env.broker.Addr(),
		DeviceID:      deviceID,
		BootID:        bootID,
		Interval:      200 * time.Millisecond,
		KeepAlive:     5 * time.Second,
		Logger:        slog.New(slog.DiscardHandler),
		Scenario:      scenario,
		Faults:        faults,
	})
	if err != nil {
		t.Fatalf("new simulator: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := simulator.Run(ctx); err != nil && ctx.Err() == nil {
			t.Logf("simulator stopped: %v", err)
		}
	}()
	go func() {
		_ = simulator.RunReporting(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("the simulator did not stop within the timeout")
		}
	})

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	if err := simulator.WaitConnected(waitCtx); err != nil {
		t.Fatalf("the simulator never connected: %v", err)
	}
	return simulator
}

// getJSON fetches a URL and decodes the body.
func getJSON(t *testing.T, url string, target any) int {
	t.Helper()

	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if target != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("decode %s: %v (%s)", url, err, raw)
		}
	}
	return response.StatusCode
}

// waitFor polls until the condition holds or the timeout expires.
func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// TestTelemetryReachesTheAPI is the ingress half: the simulated device publishes
// the frozen payload and the backend answers over REST.
func TestTelemetryReachesTheAPI(t *testing.T) {
	env := startEnvironment(t, alert.DefaultConfig(), Faults{})
	startSimulator(t, env, func(int) Sample { return QuietSample() }, Faults{})

	waitFor(t, "the device to appear over REST", func() bool {
		return getJSON(t, env.baseURL+"/api/v1/devices/MCU001/status", nil) == http.StatusOK
	})

	var status struct {
		DeviceID     string `json:"deviceId"`
		Connectivity string `json:"connectivity"`
		LocalAlarm   bool   `json:"localAlarm"`
	}
	getJSON(t, env.baseURL+"/api/v1/devices/MCU001/status", &status)
	if status.Connectivity != "online" {
		t.Fatalf("connectivity = %q, want online", status.Connectivity)
	}
	if status.LocalAlarm {
		t.Fatal("a quiet device reported a local alarm")
	}

	var latest struct {
		DeviceID       string   `json:"deviceId"`
		BootID         string   `json:"bootId"`
		TemperatureC   float64  `json:"temperatureC"`
		HumidityRh     float64  `json:"humidityRh"`
		GasAdcFiltered int      `json:"gasAdcFiltered"`
		GasPpm         *float64 `json:"gasPpm"`
		AlarmCauses    []string `json:"alarmCauses"`
	}
	if code := getJSON(t, env.baseURL+"/api/v1/devices/MCU001/telemetry/latest", &latest); code != http.StatusOK {
		t.Fatalf("latest telemetry status = %d", code)
	}
	if latest.DeviceID != "MCU001" || latest.BootID != "sim00001" {
		t.Fatalf("identity = %+v", latest)
	}
	if latest.TemperatureC != 25 || latest.HumidityRh != 50 {
		t.Fatalf("readings = %v / %v", latest.TemperatureC, latest.HumidityRh)
	}
	if latest.GasAdcFiltered != 1000 {
		t.Fatalf("gasAdcFiltered = %d", latest.GasAdcFiltered)
	}
	if len(latest.AlarmCauses) != 0 {
		t.Fatalf("alarmCauses = %v, want empty", latest.AlarmCauses)
	}
}

// TestDuplicateDeliveryIsStoredOnce verifies the dedup key end to end: the
// simulator republishes whole frames, which is what a QoS 1 redelivery looks
// like, and the backend must store one sample per (deviceId, bootId, sequence).
func TestDuplicateDeliveryIsStoredOnce(t *testing.T) {
	env := startEnvironment(t, alert.DefaultConfig(), Faults{})
	// Duplicate every sample, so the ratio of published to stored frames is
	// unambiguous.
	startSimulator(t, env, func(int) Sample { return QuietSample() }, Faults{DuplicateEvery: 1})

	waitFor(t, "several samples to arrive", func() bool {
		return getJSON(t, env.baseURL+"/api/v1/devices/MCU001/status", nil) == http.StatusOK
	})
	// Let a few report periods elapse so a duplicate has certainly been sent.
	time.Sleep(1500 * time.Millisecond)

	var page struct {
		Items []struct {
			Sequence uint32 `json:"sequence"`
		} `json:"items"`
	}
	url := env.baseURL + "/api/v1/devices/MCU001/telemetry?from=" +
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + "&to=" +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + "&limit=1000"
	getJSON(t, url, &page)

	// Every stored sequence must be distinct.
	seen := make(map[uint32]int, len(page.Items))
	for _, item := range page.Items {
		seen[item.Sequence]++
	}
	for sequence, count := range seen {
		if count != 1 {
			t.Fatalf("sequence %d was stored %d times", sequence, count)
		}
	}
	if len(page.Items) < 3 {
		t.Fatalf("only %d samples were stored; the run was too short to prove anything", len(page.Items))
	}
}

// TestCompositeWarningFiresOnTwoFactors is the alerting half. It runs two
// simulated devices side by side: one whose gas rises with a flat temperature,
// and one whose gas and temperature both rise. Neither factor alone may produce
// a fire warning; the combination must.
//
// Running both at once is the point. Testing them in separate runs could pass
// with the rule accidentally reading only one factor, because the negative case
// would also pass.
func TestCompositeWarningFiresOnTwoFactors(t *testing.T) {
	// A short window keeps the test fast. The rule is the deployed one, only with
	// tighter timing.
	alertConfig := alert.DefaultConfig()
	alertConfig.Window = 30 * time.Second
	alertConfig.MinSamples = 3
	alertConfig.MinDuration = 500 * time.Millisecond

	env := startEnvironment(t, alertConfig, Faults{})

	// Gas rises; temperature stays flat. Neither the absolute nor the rate limit
	// is met by the temperature.
	gasOnly := func(step int) Sample {
		sample := QuietSample()
		sample.GasAdcFiltered = 1000 + step*120
		sample.GasAdcRaw = sample.GasAdcFiltered
		gas := 20.0 + float64(step)*12
		sample.GasPpm = &gas
		return sample
	}
	// Gas rises and so does the temperature, by far more than the configured
	// slope.
	bothFactors := func(step int) Sample {
		sample := gasOnly(step)
		sample.TemperatureC = 25 + float64(step)*1.5
		return sample
	}

	startSimulatorAs(t, env, "MCU010", "sim01001", gasOnly, Faults{})
	startSimulatorAs(t, env, "MCU011", "sim01101", bothFactors, Faults{})

	readAlarmState := func(deviceID string) string {
		var status struct {
			AlarmState string `json:"alarmState"`
		}
		if code := getJSON(t, env.baseURL+"/api/v1/devices/"+deviceID+"/status", &status); code != http.StatusOK {
			return ""
		}
		return status.AlarmState
	}

	waitFor(t, "both devices to report", func() bool {
		return readAlarmState("MCU010") != "" && readAlarmState("MCU011") != ""
	})

	waitFor(t, "the two-factor device to reach a fire warning", func() bool {
		return readAlarmState("MCU011") == "fire_warning"
	})
	if state := readAlarmState("MCU010"); state == "fire_warning" {
		t.Fatal("a gas rise alone produced a fire warning; the rule is meant to need both factors")
	}

	// The evidence must explain which two factors triggered, so a reader can tell
	// the rule was the composite one rather than something else.
	var page struct {
		Items []struct {
			State    string `json:"state"`
			Evidence struct {
				GasAdcRise                         int     `json:"gasAdcRise"`
				GasAdcRiseThreshold                int     `json:"gasAdcRiseThreshold"`
				TemperatureRateCPerMinute          float64 `json:"temperatureRateCPerMinute"`
				TemperatureRateThresholdCPerMinute float64 `json:"temperatureRateThresholdCPerMinute"`
				SampleCount                        int     `json:"sampleCount"`
			} `json:"evidence"`
		} `json:"items"`
	}
	getJSON(t, env.baseURL+"/api/v1/devices/MCU011/alerts", &page)
	if len(page.Items) == 0 {
		t.Fatal("the two-factor device produced no alert event")
	}
	evidence := page.Items[0].Evidence
	if evidence.GasAdcRise < evidence.GasAdcRiseThreshold {
		t.Fatalf("the recorded gas rise %d is below its threshold %d", evidence.GasAdcRise, evidence.GasAdcRiseThreshold)
	}
	if evidence.TemperatureRateCPerMinute < evidence.TemperatureRateThresholdCPerMinute {
		t.Fatalf("the recorded temperature rate %v is below its threshold %v",
			evidence.TemperatureRateCPerMinute, evidence.TemperatureRateThresholdCPerMinute)
	}
	if evidence.SampleCount < 3 {
		t.Fatalf("the recorded sample count %d is below the minimum", evidence.SampleCount)
	}
}

// TestControlCommandReachesTheDevice closes the loop: a command issued over REST
// is published, the simulated device acts on it and acknowledges, and the
// backend records the acknowledgement.
func TestControlCommandReachesTheDevice(t *testing.T) {
	env := startEnvironment(t, alert.DefaultConfig(), Faults{})
	simulator := startSimulator(t, env, func(int) Sample { return QuietSample() }, Faults{})

	// The device must have reported first: the backend provisions a device on
	// its first valid telemetry, and the command routes require it to exist.
	waitFor(t, "the device to appear over REST", func() bool {
		return getJSON(t, env.baseURL+"/api/v1/devices/MCU001/status", nil) == http.StatusOK
	})

	request, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/devices/MCU001/commands/mute",
		strings.NewReader(`{"muted":true}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "01E2EMUTE")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("mute request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("mute status = %d, want 202: %s", response.StatusCode, body)
	}
	var accepted struct {
		RequestID string `json:"requestId"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode the acceptance: %v", err)
	}
	if accepted.Status != "pending" {
		t.Fatalf("acceptance status = %q, want pending", accepted.Status)
	}

	waitFor(t, "the device to receive the command", func() bool {
		return len(simulator.ReceivedCommands()) > 0
	})
	waitFor(t, "the device to mute", simulator.Muted)

	// The device's acknowledgement must reach the backend and move the command
	// out of pending.
	waitFor(t, "the command to be recorded as applied", func() bool {
		var command struct {
			State string `json:"state"`
		}
		if code := getJSON(t, env.baseURL+"/api/v1/devices/MCU001/commands/"+accepted.RequestID, &command); code != http.StatusOK {
			return false
		}
		return command.State == "applied"
	})

	var command struct {
		State            string `json:"state"`
		ConfirmedVersion *int   `json:"confirmedVersion"`
	}
	getJSON(t, env.baseURL+"/api/v1/devices/MCU001/commands/"+accepted.RequestID, &command)
	if command.State != "applied" {
		t.Fatalf("command state = %q, want applied", command.State)
	}
	// A mute does not concern thresholds, so it must not report a version.
	if command.ConfirmedVersion != nil {
		t.Fatalf("a mute command reported confirmed version %v", *command.ConfirmedVersion)
	}
}

// TestThresholdCommandIsAcknowledgedWithAVersion verifies the thresholds half of
// the control path, including that the device reports the version it adopted.
func TestThresholdCommandIsAcknowledgedWithAVersion(t *testing.T) {
	env := startEnvironment(t, alert.DefaultConfig(), Faults{})
	simulator := startSimulator(t, env, func(int) Sample { return QuietSample() }, Faults{})

	waitFor(t, "the device to appear over REST", func() bool {
		return getJSON(t, env.baseURL+"/api/v1/devices/MCU001/status", nil) == http.StatusOK
	})

	request, err := http.NewRequest(http.MethodPut, env.baseURL+"/api/v1/devices/MCU001/thresholds",
		strings.NewReader(`{"temperatureHighC":35,"humidityHighRh":85,"gasHighPpm":120}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "01E2ETHRESH")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("threshold request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("threshold status = %d, want 202: %s", response.StatusCode, body)
	}
	var accepted struct {
		RequestID      string `json:"requestId"`
		DesiredVersion int    `json:"desiredVersion"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode the acceptance: %v", err)
	}
	if accepted.DesiredVersion != 2 {
		t.Fatalf("desired version = %d, want 2", accepted.DesiredVersion)
	}

	waitFor(t, "the device to adopt the version", func() bool {
		return simulator.ThresholdVersion() == 2
	})

	// The backend must distinguish the version it asked for from the one the
	// device confirmed, and end up with the two equal.
	waitFor(t, "the backend to record the confirmation", func() bool {
		var record struct {
			ConfirmedVersion *int `json:"confirmedVersion"`
		}
		if code := getJSON(t, env.baseURL+"/api/v1/devices/MCU001/thresholds", &record); code != http.StatusOK {
			return false
		}
		return record.ConfirmedVersion != nil && *record.ConfirmedVersion == 2
	})

	var record struct {
		DesiredVersion    int     `json:"desiredVersion"`
		ConfirmedVersion  *int    `json:"confirmedVersion"`
		ConfirmationState string  `json:"confirmationState"`
		TemperatureHighC  float64 `json:"temperatureHighC"`
	}
	getJSON(t, env.baseURL+"/api/v1/devices/MCU001/thresholds", &record)
	if record.DesiredVersion != 2 {
		t.Fatalf("desiredVersion = %d", record.DesiredVersion)
	}
	if record.ConfirmedVersion == nil || *record.ConfirmedVersion != 2 {
		t.Fatalf("confirmedVersion = %v, want 2", record.ConfirmedVersion)
	}
	if record.ConfirmationState != "confirmed" {
		t.Fatalf("confirmationState = %q, want confirmed", record.ConfirmationState)
	}
	if record.TemperatureHighC != 35 {
		t.Fatalf("temperatureHighC = %v, want the value the device adopted", record.TemperatureHighC)
	}
}

// TestCommandIdempotencyOverTheAPI verifies that a replayed control request
// returns the original command rather than publishing a second one, which is the
// property a retrying client depends on.
func TestCommandIdempotencyOverTheAPI(t *testing.T) {
	env := startEnvironment(t, alert.DefaultConfig(), Faults{})
	simulator := startSimulator(t, env, func(int) Sample { return QuietSample() }, Faults{})

	waitFor(t, "the device to appear over REST", func() bool {
		return getJSON(t, env.baseURL+"/api/v1/devices/MCU001/status", nil) == http.StatusOK
	})

	send := func() (int, string) {
		request, err := http.NewRequest(http.MethodPost, env.baseURL+"/api/v1/devices/MCU001/commands/mute",
			strings.NewReader(`{"muted":true}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "01E2EREPLAY")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = response.Body.Close() }()
		var accepted struct {
			RequestID string `json:"requestId"`
		}
		_ = json.NewDecoder(response.Body).Decode(&accepted)
		return response.StatusCode, accepted.RequestID
	}

	firstStatus, firstID := send()
	secondStatus, secondID := send()
	if firstStatus != http.StatusAccepted || secondStatus != http.StatusAccepted {
		t.Fatalf("statuses = %d and %d, want 202 for both", firstStatus, secondStatus)
	}
	if firstID != secondID {
		t.Fatalf("a replay produced a second command: %s then %s", firstID, secondID)
	}
	// One command means one delivery. A short pause lets a second delivery show
	// up if one were published.
	time.Sleep(500 * time.Millisecond)
	if got := len(simulator.ReceivedCommands()); got != 1 {
		t.Fatalf("the device received %d commands, want 1", got)
	}
}

// TestOfflineDetectionOverTheAPI verifies the heartbeat path: a device whose
// application loop stops is reported offline while its MQTT session stays up.
//
// That is the point of the rule. A device can hold a broker connection and still
// have a stalled main loop, so the backend must decide from the telemetry it
// receives rather than from the connection it cannot see. The test cancels only
// the reporting loop and leaves the session connected, which is what makes the
// assertion meaningful.
//
// The offline window is the contract's fifteen seconds, so this test takes about
// that long. It is the only slow test in the suite and it is worth the time: the
// alternative is not testing the rule at all.
func TestOfflineDetectionOverTheAPI(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	backendConfig := config.Config{
		Addr:          "127.0.0.1:0",
		BrokerURL:     broker.Addr(),
		MQTTClientID:  "lab-backend",
		MQTTKeepAlive: 5 * time.Second,
		AuthMode:      "none",
		OfflineAfter:  15 * time.Second,
		CommandTTL:    60 * time.Second,
		// The sweep runs often so the transition is noticed promptly once the
		// silence has lasted long enough.
		SweepInterval:       200 * time.Millisecond,
		Alert:               alert.DefaultConfig(),
		MaxWebSocketClients: 8,
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx, app.Options{
			Config: backendConfig,
			Logger: slog.New(slog.DiscardHandler),
			Store:  store.NewMemory(),
			Ready:  func(address string) { ready <- address },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("the backend did not stop within the timeout")
		}
	})

	var baseURL string
	select {
	case address := <-ready:
		baseURL = "http://" + address
	case err := <-done:
		t.Fatalf("the backend stopped before it was ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the backend never reported a bound address")
	}

	simulator, err := New(Config{
		BrokerAddress: broker.Addr(),
		DeviceID:      "MCU002",
		BootID:        "sim00002",
		Interval:      200 * time.Millisecond,
		KeepAlive:     5 * time.Second,
		Logger:        slog.New(slog.DiscardHandler),
		Scenario:      func(int) Sample { return QuietSample() },
	})
	if err != nil {
		t.Fatalf("new simulator: %v", err)
	}

	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	sessionDone := make(chan struct{})
	go func() {
		defer close(sessionDone)
		_ = simulator.Run(sessionCtx)
	}()
	// The reporting loop has its own context, so it can be stopped while the
	// session stays connected.
	reportCtx, reportCancel := context.WithCancel(context.Background())
	go func() { _ = simulator.RunReporting(reportCtx) }()
	t.Cleanup(func() {
		reportCancel()
		sessionCancel()
		<-sessionDone
	})

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	if err := simulator.WaitConnected(waitCtx); err != nil {
		t.Fatalf("the simulator never connected: %v", err)
	}
	if err := simulator.PublishOnce(sessionCtx); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, "the device to appear online", func() bool {
		var status struct {
			Connectivity string `json:"connectivity"`
		}
		if code := getJSON(t, baseURL+"/api/v1/devices/MCU002/status", &status); code != http.StatusOK {
			return false
		}
		return status.Connectivity == "online"
	})

	// Stop reporting but leave the session up.
	reportCancel()
	if !simulator.clientConnected() {
		t.Fatal("precondition failed: the MQTT session dropped, so the test would not prove the rule")
	}

	// The silence must exceed the contract's fifteen-second window.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var status struct {
			Connectivity string `json:"connectivity"`
			LastSeenAt   string `json:"lastSeenAt"`
		}
		if code := getJSON(t, baseURL+"/api/v1/devices/MCU002/status", &status); code == http.StatusOK && status.Connectivity == "offline" {
			if status.LastSeenAt == "" {
				t.Fatal("an offline device reported no last-seen time")
			}
			if _, err := time.Parse(time.RFC3339, status.LastSeenAt); err != nil {
				t.Fatalf("lastSeenAt %q is not RFC 3339: %v", status.LastSeenAt, err)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the device to be reported offline")
}

// TestScenarioFor verifies all built-in simulator scenario generators.
func TestScenarioFor(t *testing.T) {
	cases := []struct {
		name      string
		shouldErr bool
	}{
		{name: "quiet", shouldErr: false},
		{name: "gas-surge", shouldErr: false},
		{name: "warm-up", shouldErr: false},
		{name: "fire-alarm", shouldErr: false},
		{name: "invalid-name", shouldErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen, err := scenarioFor(tc.name)
			if tc.shouldErr {
				if err == nil {
					t.Fatalf("scenarioFor(%q) expected error, got nil", tc.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("scenarioFor(%q) unexpected error: %v", tc.name, err)
			}
			// Verify generator produces valid samples across steps
			s0 := gen(0)
			s5 := gen(5)
			s10 := gen(10)
			if s0.TemperatureC <= 0 || s5.TemperatureC <= 0 || s10.TemperatureC <= 0 {
				t.Fatalf("scenarioFor(%q) produced non-positive temperature", tc.name)
			}
		})
	}
}
