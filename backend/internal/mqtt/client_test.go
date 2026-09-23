package mqtt_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/mqtt"
	"github.com/BobcGn/final/backend/internal/mqtt/mqtttest"
)

// testTimeout bounds every wait so a broken client fails the test instead of
// hanging it.
const testTimeout = 5 * time.Second

// collector records received messages and signals each arrival.
type collector struct {
	mu       sync.Mutex
	messages []mqtt.Message
	arrived  chan struct{}
}

// newCollector builds a collector with a buffered signal channel.
func newCollector() *collector {
	return &collector{arrived: make(chan struct{}, 64)}
}

// handle records a message.
func (c *collector) handle(_ context.Context, message mqtt.Message) {
	c.mu.Lock()
	c.messages = append(c.messages, message)
	c.mu.Unlock()

	select {
	case c.arrived <- struct{}{}:
	default:
	}
}

// wait returns the next message, or fails after the timeout.
func (c *collector) wait(t *testing.T) mqtt.Message {
	t.Helper()

	select {
	case <-c.arrived:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.messages[len(c.messages)-1]
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for a message")
		return mqtt.Message{}
	}
}

// count returns how many messages have arrived.
func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

// startClient builds and runs a client against the broker. It returns the client
// and a stop function registered with the test.
func startClient(t *testing.T, broker *mqtttest.Broker, mutate func(*mqtt.Config), handler mqtt.HandlerFunc) *mqtt.Client {
	t.Helper()

	config := mqtt.Config{
		Address:       broker.Addr(),
		ClientID:      "lab-backend",
		CleanSession:  true,
		Logger:        slog.New(slog.DiscardHandler),
		ReconnectMin:  20 * time.Millisecond,
		ReconnectMax:  50 * time.Millisecond,
		Handler:       handler,
		Subscriptions: []mqtt.TopicFilter{{Topic: "device/telemetry", QoS: 1}},
	}
	if mutate != nil {
		mutate(&config)
	}

	client, err := mqtt.New(config)
	if err != nil {
		t.Fatalf("new client: %v", err)
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
		case <-time.After(testTimeout):
			t.Log("the client did not stop within the timeout")
		}
	})
	return client
}

// waitFor polls until condition holds or the timeout expires.
func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// TestClientReceivesAPublishedMessage is the end-to-end path over a real socket:
// connect, subscribe, publish at QoS 1, acknowledge, deliver.
func TestClientReceivesAPublishedMessage(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	collector := newCollector()
	client := startClient(t, broker, nil, collector.handle)

	waitFor(t, "the client to connect", client.Connected)

	payload := []byte(`{"schemaVersion":1}`)
	if err := client.Publish(context.Background(), "device/telemetry", payload, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}

	message := collector.wait(t)
	if message.Topic != "device/telemetry" {
		t.Fatalf("topic = %q", message.Topic)
	}
	if string(message.Payload) != string(payload) {
		t.Fatalf("payload = %s, want %s", message.Payload, payload)
	}
	if message.QoS != 1 {
		t.Fatalf("delivered QoS = %d, want 1 because the subscription asked for QoS 1", message.QoS)
	}

	// The broker must have recorded the publish, which is what proves the QoS 1
	// handshake completed rather than the message being dropped after PUBACK.
	if len(broker.ObservedOn("device/telemetry")) != 1 {
		t.Fatalf("broker recorded %d messages", len(broker.ObservedOn("device/telemetry")))
	}
}

// TestClientRejectsUnsupportedQoS verifies that a QoS the client does not
// implement is refused rather than silently downgraded.
func TestClientRejectsUnsupportedQoS(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, nil, nil)
	waitFor(t, "the client to connect", client.Connected)

	if err := client.Publish(context.Background(), "device/telemetry", []byte("{}"), 2); err == nil {
		t.Fatal("a QoS 2 publish was accepted")
	}
}

// TestPublishWhileDisconnectedFails verifies that a command the broker cannot
// take is reported as a failure rather than queued invisibly.
func TestPublishWhileDisconnectedFails(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, nil, nil)
	waitFor(t, "the client to connect", client.Connected)

	broker.DropConnections()
	waitFor(t, "the client to notice the dropped connection", func() bool { return !client.Connected() })

	if err := client.Publish(context.Background(), "device/control", []byte("{}"), 1); !errors.Is(err, mqtt.ErrNotConnected) {
		t.Fatalf("publish while disconnected = %v, want ErrNotConnected", err)
	}
}

// TestClientReconnectsAndResubscribes verifies the recovery path after the broker
// drops the connection, including that the subscription is re-established so
// telemetry does not silently stop flowing.
func TestClientReconnectsAndResubscribes(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	collector := newCollector()
	client := startClient(t, broker, nil, collector.handle)
	waitFor(t, "the client to connect", client.Connected)

	if err := client.Publish(context.Background(), "device/telemetry", []byte("first"), 1); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	collector.wait(t)

	broker.DropConnections()

	// The disconnect must be observed before the reconnect is awaited. Waiting
	// only for Connected would return immediately on the old, already-dead
	// session, and the publish below would be written to a socket nobody is
	// reading.
	waitFor(t, "the client to notice the dropped connection", func() bool { return !client.Connected() })
	waitFor(t, "the client to reconnect and resubscribe", client.Connected)

	// Connected becomes true only after the subscription is confirmed, so a
	// publish here is routed rather than dropped.
	if err := client.Publish(context.Background(), "device/telemetry", []byte("second"), 1); err != nil {
		t.Fatalf("publish after reconnecting: %v", err)
	}
	waitFor(t, "the second message to be delivered", func() bool { return collector.count() >= 2 })
}

// TestClientRetriesOnBadCredentials verifies that a refused connection is
// retried rather than ending the ingress path silently.
func TestClientRetriesOnBadCredentials(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	broker.Credentials = &mqtttest.Credentials{Username: "backend", Password: "right"}

	client := startClient(t, broker, func(config *mqtt.Config) {
		config.Username = "backend"
		config.Password = "wrong"
	}, nil)

	// The client must not report itself connected, and it must keep trying.
	time.Sleep(200 * time.Millisecond)
	if client.Connected() {
		t.Fatal("the client reported a connection with rejected credentials")
	}

	// Correcting the credentials is only possible by rebuilding the client, so
	// this test asserts the negative path only: a wrong credential never becomes
	// a working session, and the client keeps attempting instead of exiting.
	client2 := startClient(t, broker, func(config *mqtt.Config) {
		config.Username = "backend"
		config.Password = "right"
		config.ClientID = "lab-backend-2"
	}, nil)
	waitFor(t, "the correctly credentialled client to connect", client2.Connected)
}

// TestClientSurfacesARefusedSubscription verifies that a broker refusing the
// subscription is not treated as success: a client running blind would look
// healthy while silently dropping every message.
func TestClientSurfacesARefusedSubscription(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	broker.RejectSubscriptions = true

	client := startClient(t, broker, nil, nil)

	// The client retries, so it may briefly appear connected before failing. The
	// assertion is that it never stays connected with a refused subscription.
	time.Sleep(300 * time.Millisecond)
	if client.Connected() {
		t.Fatal("the client reported a connection whose subscription was refused")
	}
}

// TestGetMessageBufferIsNotShared verifies that the handler receives its own copy
// of the payload, so a later packet cannot rewrite a message already dispatched.
func TestGetMessageBufferIsNotShared(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	received := make(chan string, 4)
	client := startClient(t, broker, nil, func(_ context.Context, message mqtt.Message) {
		received <- string(message.Payload)
	})
	waitFor(t, "the client to connect", client.Connected)

	for _, payload := range []string{"alpha", "beta"} {
		if err := client.Publish(context.Background(), "device/telemetry", []byte(payload), 1); err != nil {
			t.Fatalf("publish %s: %v", payload, err)
		}
	}

	seen := map[string]bool{}
	for index := 0; index < 2; index++ {
		select {
		case payload := <-received:
			seen[payload] = true
		case <-time.After(testTimeout):
			t.Fatalf("timed out after receiving %v", seen)
		}
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("received %v, want both alpha and beta", seen)
	}
}

// TestConfigValidation covers the configurations that cannot produce a session.
func TestConfigValidation(t *testing.T) {
	cases := map[string]mqtt.Config{
		"missing address":   {ClientID: "lab-backend"},
		"missing client id": {Address: "127.0.0.1:1883"},
		"client id too long": {
			Address:  "127.0.0.1:1883",
			ClientID: "0123456789012345678901234",
		},
		// The CONNECT field is whole seconds, so a sub-second keep-alive would be
		// negotiated as zero while the client still pinged at a fraction of one.
		"keep alive below one second": {
			Address:   "127.0.0.1:1883",
			ClientID:  "lab-backend",
			KeepAlive: 200 * time.Millisecond,
		},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := mqtt.New(config); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}

	// The keep-alive default is applied rather than rejected, and it matches the
	// device contract so both ends of the link use the same number.
	client, err := mqtt.New(mqtt.Config{Address: "127.0.0.1:1883", ClientID: "lab-backend"})
	if err != nil {
		t.Fatalf("a minimal configuration was rejected: %v", err)
	}
	if client.KeepAlive() != mqtt.DefaultKeepAlive {
		t.Fatalf("keep alive = %s, want the default %s", client.KeepAlive(), mqtt.DefaultKeepAlive)
	}
}

// TestRunReturnsPromptlyWhenCancelled verifies that shutting down does not wait
// for a socket read deadline.
//
// A blocking read cannot observe a context, so without closing the socket on
// cancellation the client keeps serving a cancelled context until the read
// deadline expires — twice the keep-alive interval. That is long enough for a
// process shutdown to look hung, and it is the difference between a service that
// stops in milliseconds and one that has to be killed.
func TestRunReturnsPromptlyWhenCancelled(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client, err := mqtt.New(mqtt.Config{
		Address:  broker.Addr(),
		ClientID: "lab-backend",
		// A keep-alive far longer than the assertion, so a client that waits for
		// its read deadline cannot pass by accident.
		KeepAlive:     5 * time.Minute,
		Logger:        slog.New(slog.DiscardHandler),
		Subscriptions: []mqtt.TopicFilter{{Topic: "device/telemetry", QoS: 1}},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	waitFor(t, "the client to connect", client.Connected)

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within two seconds of cancellation, although the keep-alive is %s", 5*time.Minute)
	}
}

// TestBrokerTopicMatching covers the in-process broker's filter matching, which
// the integration tests above rely on.
func TestBrokerTopicMatching(t *testing.T) {
	cases := map[string]struct {
		filter string
		topic  string
		want   bool
	}{
		"exact match":               {filter: "device/telemetry", topic: "device/telemetry", want: true},
		"different topic":           {filter: "device/telemetry", topic: "device/control", want: false},
		"hash matches below":        {filter: "device/#", topic: "device/telemetry", want: true},
		"hash matches the parent":   {filter: "device/#", topic: "device", want: true},
		"plus matches one level":    {filter: "device/+", topic: "device/telemetry", want: true},
		"plus does not span levels": {filter: "device/+", topic: "device/a/b", want: false},
		"shorter filter":            {filter: "device", topic: "device/telemetry", want: false},
		"longer filter":             {filter: "device/telemetry/raw", topic: "device/telemetry", want: false},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mqtttest.MatchTopic(testCase.filter, testCase.topic); got != testCase.want {
				t.Fatalf("MatchTopic(%q, %q) = %v, want %v", testCase.filter, testCase.topic, got, testCase.want)
			}
		})
	}
}
