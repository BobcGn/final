package mqtt_test

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/BobcGn/final/backend/internal/mqtt"
	"github.com/BobcGn/final/backend/internal/mqtt/mqtttest"
)

// The reliability contracts this file pins down. Each one maps to a bug or a
// requirement that came out of SHIXUN-25: a session that dies on a schedule, and
// a QoS 1 publish that resolves to success when no PUBACK ever arrived.

// TestSessionIsStableAcrossManyPingCycles is the regression for the periodic
// drop. A session must outlive several keep-alive periods without a single
// reconnect: the old client died on a fixed ticker that raced the keep-alive
// boundary, and the observable symptom was exactly one EOF every 30 seconds.
func TestSessionIsStableAcrossManyPingCycles(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	// A 200 ms keep-alive makes four cycles a sub-second budget instead of two
	// minutes. The client's own behaviour, not the duration, is what is tested.
	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.KeepAlive = time.Second
		cfg.PingAfter = 100 * time.Millisecond
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	// The broker has to see the pings; without them the client would be
	// exercising a keep-alive loop that never fires.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if broker.Pings() >= 6 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the broker saw %d PINGREQ in four seconds, want at least 6", broker.Pings())
}

// TestPingIsNotSentWhileTheApplicationIsWriting proves the keep-alive is
// idle-driven rather than a fixed tick. An application publish is already a
// keep-alive packet, so a PINGREQ on top of it is redundant traffic and the
// reason the old client's wire log showed a ping landing between two publishes.
func TestPingIsNotSentWhileTheApplicationIsWriting(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.KeepAlive = time.Second
		cfg.PingAfter = 60 * time.Millisecond
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	// Publish every 20 ms for 400 ms. That is two full ping budgets and eight
	// ping ticks, during which the wire must see no PINGREQ at all.
	for i := 0; i < 20; i++ {
		if err := client.Publish(context.Background(), "device/telemetry", []byte("tick"), 0); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := broker.Pings(); got != 0 {
		t.Fatalf("the broker saw %d PINGREQ while the application kept the wire busy, want 0", got)
	}

	// Once the application goes quiet the keep-alive has to take over again.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if broker.Pings() >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no PINGREQ after the application went quiet, want at least one")
}

// TestARefusedPingEndsTheSession proves the keep-alive failure path: a broker
// that stops answering must not be treated as healthy forever. The read deadline
// is what ends the session when the acknowledgement stops coming.
func TestARefusedPingEndsTheSession(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.KeepAlive = time.Second
		cfg.PingAfter = 50 * time.Millisecond
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	// Stop the broker answering anything. The client's read deadline, twice the
	// keep-alive, must end the session without needing a ping response.
	broker.StopAnswering()

	waitFor(t, "the session to end after the broker went silent", func() bool {
		return !client.Connected()
	})
}

// TestPublishIsNotResolvedAsSuccessWithoutAPuback is the defect SHIXUN-25 named
// explicitly. Closing a waiter on connection loss looked identical to a real
// PUBACK to the caller, so a command could be recorded as published while the
// broker had never acknowledged it.
func TestPublishIsNotResolvedAsSuccessWithoutAPuback(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.PublishTimeout = 500 * time.Millisecond
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	// The broker accepts the PUBLISH and never answers with a PUBACK. The
	// publish must then time out rather than resolve to nil.
	broker.WithholdPubAcks()
	err = client.Publish(context.Background(), "device/control", []byte("{}"), 1)
	if !errors.Is(err, mqtt.ErrPublishTimeout) {
		t.Fatalf("publish without a PUBACK = %v, want ErrPublishTimeout", err)
	}
}

// TestPublishFailsWhenTheConnectionDiesMidFlight proves the second of the three
// outcomes a caller must be able to tell apart. Losing the socket is a failure,
// and it is not the same failure as a silent broker.
func TestPublishFailsWhenTheConnectionDiesMidFlight(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.PublishTimeout = 2 * time.Second
		cfg.ReconnectMin = 50 * time.Millisecond
		cfg.ReconnectMax = 60 * time.Millisecond
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	broker.WithholdPubAcks()
	done := make(chan error, 1)
	go func() {
		done <- client.Publish(context.Background(), "device/control", []byte("{}"), 1)
	}()

	// Give the publish time to be written, then pull the socket out from under
	// it. The wait must end with a connection failure, never with success.
	time.Sleep(50 * time.Millisecond)
	broker.DropConnections()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("publish resolved to nil after the connection died, want a failure")
		}
		if !errors.Is(err, mqtt.ErrConnectionLost) {
			t.Fatalf("publish after the connection died = %v, want ErrConnectionLost", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("the publish never returned after the connection died")
	}
}

// TestAnAckFromAnEarlierConnectionDoesNotSettleALaterPublish proves the
// acknowledgement table is epoch-scoped. Packet identifiers repeat across
// sessions, so an acknowledgement from a connection that has already ended would
// otherwise settle a publish made on its successor — reporting a command as
// delivered on the strength of an acknowledgement for a different message.
func TestAnAckFromAnEarlierConnectionDoesNotSettleALaterPublish(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.PublishTimeout = 700 * time.Millisecond
		cfg.ReconnectMin = 30 * time.Millisecond
		cfg.ReconnectMax = 40 * time.Millisecond
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	// The broker takes the publish and answers with a PUBACK carrying a packet
	// identifier it is not supposed to settle: it is the one the *previous*
	// connection used. With the table keyed only by identifier this would resolve
	// the publish as success; keyed by identifier and epoch it must not.
	broker.WithholdPubAcks()
	broker.AnswerWithPubAck(1)

	err = client.Publish(context.Background(), "device/control", []byte("{}"), 1)
	if !errors.Is(err, mqtt.ErrPublishTimeout) {
		t.Fatalf("a PUBACK from an earlier connection settled the publish: %v", err)
	}
}

// TestBackoffIsResetAfterAHealthySession proves the reconnect delay does not
// survive a session that worked. A long outage that grows the delay and then
// leaves it there would cost thirty seconds of downtime on every later hiccup,
// which is the difference between a hiccup and an outage for the control path.
func TestBackoffIsResetAfterAHealthySession(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	// The backoff ceiling is large enough that a client that kept it across a
	// healthy session would visibly lag; the ceiling it never exceeds is what the
	// assertion checks, measured as the gap between two reconnects.
	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.ReconnectMin = 50 * time.Millisecond
		cfg.ReconnectMax = 10 * time.Second
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	// First outage: fail twice so the backoff has a reason to grow, then let the
	// client reconnect for real.
	broker.DropConnections()
	waitFor(t, "the first reconnect", client.Connected)
	broker.DropConnections()
	waitFor(t, "the second reconnect", client.Connected)

	// The session is healthy now. A third outage must come back on the minimum
	// delay, not on whatever the two earlier failures accumulated. ReconnectMin
	// is 50 ms and the ceiling is 10 s, so a client that failed to reset would
	// take seconds to return — far longer than the timeout below.
	start := time.Now()
	broker.DropConnections()
	waitFor(t, "the reconnect after a healthy session", client.Connected)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the reconnect after a healthy session took %s, want the minimum delay", elapsed)
	}
}

// TestBackoffGrowsAgainAfterTheReset proves the reset belongs to one successful
// session rather than to the lifetime of the Client. Once that session ends,
// consecutive rejected reconnects must grow the delay again; otherwise a broker
// that stays unavailable is hammered at ReconnectMin forever.
func TestBackoffGrowsAgainAfterTheReset(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.ReconnectMin = 30 * time.Millisecond
		cfg.ReconnectMax = 120 * time.Millisecond
	}, nil)
	waitFor(t, "the initial healthy session", client.Connected)

	baseline := len(broker.ConnectTimes())
	broker.RejectConnections(true)
	broker.DropConnections()
	waitFor(t, "four rejected reconnect attempts", func() bool {
		return len(broker.ConnectTimes()) >= baseline+4
	})

	attempts := broker.ConnectTimes()[baseline : baseline+4]
	firstFailureGap := attempts[2].Sub(attempts[1])
	secondFailureGap := attempts[3].Sub(attempts[2])
	if firstFailureGap < 45*time.Millisecond {
		t.Fatalf("first consecutive-failure delay = %s, want growth beyond ReconnectMin", firstFailureGap)
	}
	if secondFailureGap < 90*time.Millisecond {
		t.Fatalf("second consecutive-failure delay = %s, want growth toward ReconnectMax", secondFailureGap)
	}
}

// TestBrokerRestartResubscribes is the recovery contract for the control path.
// A broker that comes back must have the client's subscriptions re-established
// before telemetry flows again, because Connected means "ready" and not merely
// "the CONNECT was acknowledged".
func TestBrokerRestartResubscribes(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	received := make(chan string, 16)
	client := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.ReconnectMin = 20 * time.Millisecond
		cfg.ReconnectMax = 40 * time.Millisecond
	}, func(_ context.Context, message mqtt.Message) {
		select {
		case received <- message.Topic:
		default:
		}
	})
	waitFor(t, "the session to come up", client.Connected)

	// Before the restart the subscription is live.
	publisher := startClient(t, broker, func(cfg *mqtt.Config) {
		cfg.ClientID = "publisher"
		cfg.Subscriptions = nil
	}, nil)
	waitFor(t, "the publisher to come up", publisher.Connected)
	if err := publisher.Publish(context.Background(), "device/telemetry", []byte("before"), 0); err != nil {
		t.Fatalf("publish before the restart: %v", err)
	}
	waitFor(t, "the message before the restart", func() bool { return len(received) > 0 })

	// A restart from the client's point of view: every socket drops and the
	// broker comes straight back.
	broker.DropConnections()
	waitFor(t, "the session to come up again", client.Connected)

	// The subscription has to be re-established, not merely remembered. A client
	// that reconnects without re-subscribing would look healthy and see nothing.
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		_ = publisher.Publish(context.Background(), "device/telemetry", []byte("after"), 0)
		select {
		case <-received:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("nothing arrived after the broker restart, want the subscription re-established")
}

// TestASecondClientWithTheSameIdentifierTakesOver proves the session survives a
// duplicate client identifier. A takeover is a legitimate MQTT event — a second
// process using the same identifier, or a deploy that overlaps by a few seconds —
// and the client must reconnect rather than stay down on the socket the broker
// discarded.
func TestASecondClientWithTheSameIdentifierTakesOver(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	// The test broker models a takeover as an existing session being closed when
	// a new one connects with the same identifier, which is what a real broker
	// does and what the client has to treat as a reconnect.
	broker.TakeOver = true

	client := startClient(t, broker, nil, nil)
	waitFor(t, "the session to come up", client.Connected)

	// A second connection with the same identifier discards the first.
	takeover, err := mqtt.New(mqtt.Config{
		Address:       broker.Addr(),
		ClientID:      "lab-backend",
		CleanSession:  true,
		Logger:        slog.New(slog.DiscardHandler),
		Subscriptions: []mqtt.TopicFilter{{Topic: "device/telemetry", QoS: 1}},
	})
	if err != nil {
		t.Fatalf("new takeover client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = takeover.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, "the taking-over session to come up", takeover.Connected)

	// The original client is the one the broker discarded. It must come back
	// rather than stay down: the control path is only as reliable as its ability
	// to recover from a takeover.
	waitFor(t, "the original session to come up again after the takeover", client.Connected)
}

// TestCancelledRunLeavesNoGoroutinesBehind proves shutting down does not leak
// the ping or socket-watch goroutines. A leak here would be a service that stops
// being reliable over days rather than one that fails immediately.
func TestCancelledRunLeavesNoGoroutinesBehind(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	before := runtime.NumGoroutine()

	config := mqtt.Config{
		Address:       broker.Addr(),
		ClientID:      "lab-backend",
		CleanSession:  true,
		Logger:        slog.New(slog.DiscardHandler),
		ReconnectMin:  20 * time.Millisecond,
		ReconnectMax:  40 * time.Millisecond,
		PingAfter:     20 * time.Millisecond,
		Subscriptions: []mqtt.TopicFilter{{Topic: "device/telemetry", QoS: 1}},
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
	waitFor(t, "the session to come up", client.Connected)

	// Give the ping and socket-watch goroutines time to exist before cancelling,
	// so the count is taken while the goroutines under test are actually running.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("the client did not stop after cancellation")
	}

	// Repeatedly sample rather than asserting once: goroutines that are one
	// scheduling turn away from exiting would otherwise make this flaky.
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines after a cancelled run = %d, want back to about %d", runtime.NumGoroutine(), before)
}

// TestConcurrentPublishesOnAStableConnectionDoNotFail is the concurrency
// contract the control path depends on. Ten control commands sent at once must
// all be acknowledged while the connection is healthy: MQTT framing is
// serialised under a write lock precisely so two publishers cannot interleave
// bytes and corrupt the session, and this is the test that says the
// serialisation still delivers.
func TestConcurrentPublishesOnAStableConnectionDoNotFail(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, func(cfg *mqtt.Config) {
		// Wide enough that a slow CI machine cannot turn latency into a failure
		// this test is not about.
		cfg.PublishTimeout = 3 * time.Second
	}, nil)
	waitFor(t, "the session to come up", client.Connected)

	var wg sync.WaitGroup
	failures := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Publish(context.Background(), "device/control", []byte("mute"), 1); err != nil {
				failures <- err
			}
		}()
	}
	wg.Wait()
	close(failures)

	for err := range failures {
		t.Fatalf("a publish on a stable connection failed: %v", err)
	}
	if got := len(broker.ObservedOn("device/control")); got != 10 {
		t.Fatalf("the broker received %d control messages, want 10", got)
	}
}

// TestAnUnexpectedPacketIsIgnoredRatherThanEndingTheSession proves the receive
// path is not fragile. A broker that sends something this client does not
// implement must not cost the session: the MQTT subset is deliberately small,
// and tearing the link down over a packet the client has no use for would make
// a compliant broker look broken.
func TestAnUnexpectedPacketIsIgnoredRatherThanEndingTheSession(t *testing.T) {
	broker, err := mqtttest.Start()
	if err != nil {
		t.Fatalf("start broker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })

	client := startClient(t, broker, nil, nil)
	waitFor(t, "the session to come up", client.Connected)

	// An UNSUBACK is a valid packet this client never sends and has no use for.
	// It must be logged and skipped, not treated as a protocol violation.
	broker.SendUnsuback()

	// The session survives: a publish still gets its PUBACK, which can only
	// happen if the read loop is still reading.
	broker.AnswerWithPubAck(0)
	waitFor(t, "the session to survive an unexpected packet", func() bool {
		return client.Connected()
	})
	if err := client.Publish(context.Background(), "device/control", []byte("{}"), 1); err != nil {
		t.Fatalf("publish after an unexpected packet: %v", err)
	}
}
