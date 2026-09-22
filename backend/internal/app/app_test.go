package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/alert"
	"github.com/BobcGn/final/backend/internal/command"
	"github.com/BobcGn/final/backend/internal/config"
	"github.com/BobcGn/final/backend/internal/domain"
	"github.com/BobcGn/final/backend/internal/ingest"
	"github.com/BobcGn/final/backend/internal/liveness"
	"github.com/BobcGn/final/backend/internal/mqtt"
	"github.com/BobcGn/final/backend/internal/mqtt/mqtttest"
	"github.com/BobcGn/final/backend/internal/protocol"
	"github.com/BobcGn/final/backend/internal/store"
)

// quietLogger discards log output so a passing test is silent.
func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// baseConfig returns a configuration that starts without a database or a broker.
func baseConfig() config.Config {
	return config.Config{
		Addr:                "127.0.0.1:0",
		MQTTClientID:        "lab-backend",
		MQTTKeepAlive:       30 * time.Second,
		AuthMode:            "none",
		OfflineAfter:        15 * time.Second,
		CommandTTL:          60 * time.Second,
		SweepInterval:       time.Hour,
		Alert:               alert.DefaultConfig(),
		MaxWebSocketClients: 8,
	}
}

// TestRunServesAndShutsDownCleanly verifies the composition root end to end: it
// binds a listener, serves the health route, and returns without error once the
// context is cancelled. A composition root that cannot shut down is a service
// that gets killed instead of stopped, which loses in-flight writes.
func TestRunServesAndShutsDownCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config: baseConfig(),
			Logger: quietLogger(),
			Store:  store.NewMemory(),
			Ready:  func(address string) { addresses <- address },
		})
	}()

	var address string
	select {
	case address = <-addresses:
	case err := <-done:
		t.Fatalf("Run returned before it was ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never reported a bound address")
	}

	response, err := http.Get("http://" + address + "/healthz")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// TestRunRejectsAnUnusableBrokerAddress verifies that a misconfigured broker is
// a start-up failure rather than a service that quietly ingests nothing.
func TestRunRejectsAnUnusableBrokerAddress(t *testing.T) {
	cfg := baseConfig()
	cfg.BrokerURL = "not-a-host-port"

	err := Run(context.Background(), Options{Config: cfg, Logger: quietLogger(), Store: store.NewMemory()})
	if err == nil {
		t.Fatal("an unusable broker address was accepted")
	}
	if !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("error = %v, want it to name the address problem", err)
	}
}

// TestRunRejectsAnUnusableListenAddress verifies that a bind failure is returned.
func TestRunRejectsAnUnusableListenAddress(t *testing.T) {
	cfg := baseConfig()
	cfg.Addr = "256.256.256.256:99999"

	if err := Run(context.Background(), Options{Config: cfg, Logger: quietLogger(), Store: store.NewMemory()}); err == nil {
		t.Fatal("an unusable listen address was accepted")
	}
}

// TestRunIngestsFromABrokerAndControlsBack is the full loop over real sockets: a
// device publishes telemetry to the in-process broker, the backend stores it and
// answers over REST, and a control command issued over REST reaches the device.
func TestRunIngestsFromABrokerAndControlsBack(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := baseConfig()
	cfg.BrokerURL = broker.Addr()
	cfg.MQTTKeepAlive = 5 * time.Second

	addresses := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config: cfg,
			Logger: quietLogger(),
			Store:  store.NewMemory(),
			Ready:  func(address string) { addresses <- address },
		})
	}()

	var address string
	select {
	case address = <-addresses:
	case err := <-done:
		t.Fatalf("Run returned before it was ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never reported a bound address")
	}
	base := "http://" + address

	// The device connects and subscribes to the control topic.
	device, delivered := newDeviceClient(t, broker)
	waitFor(t, "the device to connect", device.Connected)

	// The device reports one sample.
	sample := map[string]any{
		"schemaVersion": 1, "messageType": "telemetry", "deviceId": "MCU001",
		"bootId": "9f3ac21b", "sequence": 1, "timestamp": nil, "uptimeMs": 1000,
		"temperatureC": 28.0, "humidityRh": 61.0, "gasAdcRaw": 1350,
		"gasAdcFiltered": 1328, "gasPpm": 25.0, "gasCalibrated": false,
		"localAlarm": true, "alarmCauses": []string{"gas_high"},
		"network": "online", "thresholdVersion": 1, "sensorFault": false,
	}
	payload, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	if err := device.Publish(ctx, protocol.TopicTelemetry, payload, 1); err != nil {
		t.Fatalf("publish telemetry: %v", err)
	}

	// The backend answers over REST, which proves the whole ingress path ran.
	waitFor(t, "the device to appear over REST", func() bool {
		response, err := http.Get(base + "/api/v1/devices/MCU001/status")
		if err != nil {
			return false
		}
		defer func() { _ = response.Body.Close() }()
		return response.StatusCode == http.StatusOK
	})

	var status struct {
		Connectivity string `json:"connectivity"`
		LocalAlarm   bool   `json:"localAlarm"`
	}
	getJSON(t, base+"/api/v1/devices/MCU001/status", &status)
	if status.Connectivity != string(domain.ConnectivityOnline) {
		t.Fatalf("connectivity = %q, want online", status.Connectivity)
	}
	if !status.LocalAlarm {
		t.Fatal("the device-reported local alarm was lost through the broker")
	}

	// A control command issued over REST must reach the device on its topic.
	request, err := http.NewRequest(http.MethodPut, base+"/api/v1/devices/MCU001/thresholds",
		strings.NewReader(`{"temperatureHighC":36,"humidityHighRh":85,"gasHighPpm":120}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "01IDEMPOTENCY")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("threshold request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("threshold status = %d, want 202: %s", response.StatusCode, body)
	}

	controlPayload := waitForControl(t, delivered)
	decoded, err := protocol.DecodeControl(controlPayload, time.Now().UTC())
	if err != nil {
		t.Fatalf("the delivered control payload does not satisfy the device contract: %v", err)
	}
	if decoded.Type != domain.CommandSetThresholds {
		t.Fatalf("delivered type = %q", decoded.Type)
	}
	if decoded.Payload.Thresholds == nil {
		t.Fatalf("delivered payload = %+v", decoded.Payload)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// newDeviceClient builds and runs a client that stands in for the firmware: it
// subscribes to the control topic and records what it receives. The returned
// channel yields the payload of each delivered control message.
func newDeviceClient(t *testing.T, broker *mqtttest.Broker) (*mqtt.Client, chan []byte) {
	t.Helper()

	delivered := make(chan []byte, 8)
	client, err := mqtt.New(mqtt.Config{
		Address:       broker.Addr(),
		ClientID:      "MCU001",
		CleanSession:  true,
		Logger:        quietLogger(),
		ReconnectMin:  20 * time.Millisecond,
		ReconnectMax:  50 * time.Millisecond,
		Subscriptions: []mqtt.TopicFilter{{Topic: protocol.TopicControl, QoS: 1}},
		Handler: func(_ context.Context, message mqtt.Message) {
			if message.Topic != protocol.TopicControl {
				return
			}
			select {
			case delivered <- append([]byte(nil), message.Payload...):
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("new device client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Log("the device client did not stop within the timeout")
		}
	})
	return client, delivered
}

// waitForControl waits for the device to receive a control message.
func waitForControl(t *testing.T, delivered chan []byte) []byte {
	t.Helper()

	select {
	case payload := <-delivered:
		return payload
	case <-time.After(10 * time.Second):
		t.Fatal("the device never received the control command")
		return nil
	}
}

// getJSON fetches a URL and decodes its body.
func getJSON(t *testing.T, url string, target any) {
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
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v (%s)", url, err, raw)
	}
}

// waitFor polls a condition until it holds or the timeout expires.
func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// TestRunSweeperMarksSilentDevicesOffline verifies the timer-driven check: an
// offline device sends nothing, so nothing can trigger the detection but a
// sweep.
func TestRunSweeperMarksSilentDevicesOffline(t *testing.T) {
	now := time.Now().UTC()
	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}
	dataStore := store.NewMemory()

	// The device reported once, well before the offline window.
	tracker.Observe("MCU001", now.Add(-time.Minute))
	if err := dataStore.UpsertDevice(context.Background(), store.DeviceState{
		DeviceID: "MCU001", LastSeenAt: now.Add(-time.Minute),
		Connectivity: domain.ConnectivityOnline, AlarmState: domain.AlertNormal,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}

	ingestService, err := ingest.New(ingest.Deps{
		Store: dataStore, Engine: engine, Tracker: tracker, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	commandService, err := command.New(command.Deps{
		Store: dataStore, Publisher: noopPublisher{}, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSweeper(ctx, 20*time.Millisecond, ingestService, commandService, quietLogger())
	}()

	waitFor(t, "the device to be marked offline", func() bool {
		state, err := dataStore.Device(context.Background(), "MCU001")
		return err == nil && state.Connectivity == domain.ConnectivityOffline
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not stop when the context was cancelled")
	}
}

// TestRunSweeperExpiresUnacknowledgedCommands verifies the second timer-driven
// check: a command the device never answered must not stay pending forever.
func TestRunSweeperExpiresUnacknowledgedCommands(t *testing.T) {
	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}
	dataStore := store.NewMemory()

	ingestService, err := ingest.New(ingest.Deps{
		Store: dataStore, Engine: engine, Tracker: tracker, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}

	// A command whose expiry is already behind us: the sweeper must close it on
	// its first tick.
	commandService, err := command.New(command.Deps{
		Store: dataStore, Publisher: noopPublisher{}, Logger: quietLogger(),
		TTL: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("new command service: %v", err)
	}
	accepted, err := commandService.RequestThresholdUpdate(context.Background(), "MCU001",
		domain.Thresholds{TemperatureHighC: 30, HumidityHighRh: 80, GasHighPpm: 80}, "01IDEM", "operator")
	if err != nil {
		t.Fatalf("request thresholds: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSweeper(ctx, 20*time.Millisecond, ingestService, commandService, quietLogger())
	}()

	waitFor(t, "the command to expire", func() bool {
		stored, err := commandService.Command(context.Background(), "MCU001", accepted.RequestID)
		return err == nil && stored.State == domain.CommandTimedOut
	})

	cancel()
	<-done
}

// noopPublisher accepts every publish.
type noopPublisher struct{}

// Publish implements command.Publisher.
func (noopPublisher) Publish(context.Context, string, []byte, byte) error { return nil }

// TestBrokerAddressNormalisesURLForms verifies the scheme stripping an operator
// may rely on when pasting a broker URL from documentation.
func TestBrokerAddressNormalisesURLForms(t *testing.T) {
	cases := map[string]string{
		"emqx.lab:1883":         "emqx.lab:1883",
		"tcp://emqx.lab:1883":   "emqx.lab:1883",
		"mqtt://emqx.lab:1883":  "emqx.lab:1883",
		"ssl://emqx.lab:8883":   "emqx.lab:8883",
		"mqtts://emqx.lab:8883": "emqx.lab:8883",
		"127.0.0.1:1883":        "127.0.0.1:1883",
		"[::1]:1883":            "[::1]:1883",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := brokerAddress(input)
			if err != nil {
				t.Fatalf("brokerAddress(%q) = %v", input, err)
			}
			if got != want {
				t.Fatalf("brokerAddress(%q) = %q, want %q", input, got, want)
			}
		})
	}

	// A missing port is a configuration error, not a default: MQTT has no
	// standard port the backend may assume on the operator's behalf.
	for _, input := range []string{"emqx.lab", "", "http://emqx.lab:1883", "emqx.lab:not-a-port", ":1883", "emqx.lab:0", "emqx.lab:70000"} {
		if _, err := brokerAddress(input); err == nil {
			t.Errorf("brokerAddress(%q) was accepted", input)
		}
	}
}

// TestBrokerPublisherWithoutAClient verifies the honest failure when the
// deployment runs without MQTT.
func TestBrokerPublisherWithoutAClient(t *testing.T) {
	publisher := &brokerPublisher{}

	err := publisher.Publish(context.Background(), protocol.TopicControl, []byte("{}"), 1)
	if !errors.Is(err, errNoBroker) {
		t.Fatalf("Publish = %v, want errNoBroker", err)
	}
}

// TestNewBrokerClientSubscribesToTheDeviceTopics verifies the subscription list
// against the frozen contract: the backend reads the two device-to-cloud topics
// and must not subscribe to the topic it publishes on.
func TestNewBrokerClientSubscribesToTheDeviceTopics(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	engine, err := alert.NewEngine(alert.DefaultConfig())
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	tracker, err := liveness.New(liveness.DefaultConfig())
	if err != nil {
		t.Fatalf("new tracker: %v", err)
	}
	ingestService, err := ingest.New(ingest.Deps{
		Store: store.NewMemory(), Engine: engine, Tracker: tracker, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}

	cfg := baseConfig()
	cfg.BrokerURL = broker.Addr()

	client, err := newBrokerClient(cfg, ingestService, quietLogger())
	if err != nil {
		t.Fatalf("new broker client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()
	waitFor(t, "the client to subscribe", client.Connected)

	// A telemetry publish must be observed, which proves the subscription is
	// live rather than merely configured.
	publisher := &brokerTestPublisher{t: t}
	payload := fmt.Sprintf(`{"schemaVersion":1,"messageType":"telemetry","deviceId":"MCU001",` +
		`"bootId":"9f3ac21b","sequence":1,"timestamp":null,"uptimeMs":1000,"temperatureC":28,` +
		`"humidityRh":61,"gasAdcRaw":1350,"gasAdcFiltered":1328,"gasPpm":25,"gasCalibrated":false,` +
		`"localAlarm":false,"alarmCauses":[],"network":"online",` +
		`"thresholdVersion":1,"sensorFault":false}`)
	if err := publisher.publish(ctx, broker, payload); err != nil {
		t.Fatalf("publish telemetry: %v", err)
	}

	waitFor(t, "the message to be acknowledged", func() bool {
		return len(broker.ObservedOn(protocol.TopicTelemetry)) > 0
	})

	cancel()
	<-done
}

// brokerTestPublisher publishes through a short-lived second client so the test
// does not depend on the client under test to produce its own input.
type brokerTestPublisher struct{ t *testing.T }

// publish sends a payload on the telemetry topic.
func (p *brokerTestPublisher) publish(ctx context.Context, broker *mqtttest.Broker, payload string) error {
	p.t.Helper()

	client, err := mqtt.New(mqtt.Config{
		Address: broker.Addr(), ClientID: "test-publisher", Logger: quietLogger(),
		ReconnectMin: 20 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = client.Run(runCtx) }()

	waitFor(p.t, "the test publisher to connect", client.Connected)
	return client.Publish(ctx, protocol.TopicTelemetry, []byte(payload), 1)
}
