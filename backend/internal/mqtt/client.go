package mqtt

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Defaults for the client. They match the values recorded in
// docs/device-protocol.md for the device side so that both ends of the link are
// configured from the same numbers.
const (
	DefaultKeepAlive      = 30 * time.Second
	DefaultWriteTimeout   = 10 * time.Second
	DefaultConnectTimeout = 10 * time.Second
	DefaultReconnectMin   = 1 * time.Second
	DefaultReconnectMax   = 30 * time.Second
	DefaultReadLimit      = 64 * 1024
	// DefaultPublishTimeout bounds how long a QoS 1 publish waits for PUBACK
	// before being reported as a failure to the caller.
	DefaultPublishTimeout = 5 * time.Second
)

// Errors returned by the client.
var (
	// ErrNotConnected reports a publish attempted while the session is down.
	ErrNotConnected = errors.New("mqtt: not connected")
	// ErrPublishTimeout reports a QoS 1 publish that was never acknowledged.
	ErrPublishTimeout = errors.New("mqtt: publish acknowledgement timed out")
	// ErrConnectionLost reports a publish that was written to a connection which
	// then ended before its PUBACK arrived. It is deliberately distinct from
	// ErrPublishTimeout: the caller must be able to tell "the broker never
	// answered" from "the link went away", because only the first is a reason to
	// suspect the message itself was refused.
	ErrConnectionLost = errors.New("mqtt: connection ended before the publish was acknowledged")
	// ErrRejected reports a broker that refused a subscribe or a connect.
	ErrRejected = errors.New("mqtt: broker rejected the request")
)

// Message is one received application message.
type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

// HandlerFunc processes one received message. It must not block for long: the
// read loop calls it inline so that acknowledgement order matches arrival order.
type HandlerFunc func(context.Context, Message)

// Config configures a Client.
type Config struct {
	// Address is the broker host:port.
	Address string
	// TLS enables TLS when non-nil. The address must then be the TLS endpoint.
	TLS *tls.Config
	// ClientID is the MQTT client identifier. It must be unique per connection.
	ClientID string
	// Username and Password are optional credentials.
	Username string
	Password string
	// KeepAlive is the negotiated keep-alive interval.
	KeepAlive time.Duration
	// PingAfter overrides how long the client waits before sending a PINGREQ.
	//
	// It defaults to a fraction of [Config.KeepAlive] rather than the whole of it.
	// MQTT 3.1.1 §3.1.2.10 requires *at least one* control packet to reach the
	// broker within 1.5 keep-alive intervals of each other, so sending exactly at
	// the interval leaves a single scheduling hiccup to break the link. A half
	// interval gives the wire twice the budget it strictly needs, which is the
	// margin a jittery scheduler and a slow broker both want. It exists as a
	// field rather than a constant so a test can run the keep-alive logic on a
	// millisecond budget instead of sleeping through whole keep-alive periods.
	PingAfter time.Duration
	// CleanSession requests a fresh session on every connect.
	CleanSession bool
	// Subscriptions are established after every successful connect.
	Subscriptions []TopicFilter
	// Handler receives inbound messages.
	Handler HandlerFunc
	// Logger receives connection lifecycle events. Nil disables logging.
	Logger *slog.Logger
	// ReconnectMin and ReconnectMax bound the exponential reconnect backoff.
	ReconnectMin time.Duration
	ReconnectMax time.Duration
	// PublishTimeout bounds the QoS 1 acknowledgement wait.
	PublishTimeout time.Duration
	// ReadLimit caps an inbound packet body.
	ReadLimit int
}

// withDefaults fills every unset field.
func (c Config) withDefaults() Config {
	if c.KeepAlive <= 0 {
		c.KeepAlive = DefaultKeepAlive
	}
	if c.PingAfter <= 0 {
		// Half the keep-alive: see the field comment for why a fraction and not
		// the whole interval.
		c.PingAfter = c.KeepAlive / 2
	}
	if c.ReconnectMin <= 0 {
		c.ReconnectMin = DefaultReconnectMin
	}
	if c.ReconnectMax <= 0 {
		c.ReconnectMax = DefaultReconnectMax
	}
	if c.ReconnectMax < c.ReconnectMin {
		c.ReconnectMax = c.ReconnectMin
	}
	if c.PublishTimeout <= 0 {
		c.PublishTimeout = DefaultPublishTimeout
	}
	if c.ReadLimit <= 0 {
		c.ReadLimit = DefaultReadLimit
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// validate rejects configurations that cannot produce a working session.
func (c Config) validate() error {
	if c.Address == "" {
		return errors.New("mqtt: address is required")
	}
	if c.ClientID == "" {
		return errors.New("mqtt: client id is required")
	}
	if len(c.ClientID) > 23 {
		// MQTT 3.1.1 §3.1.3.1 requires brokers to accept at least 23 bytes and
		// leaves longer identifiers to broker discretion; staying inside the
		// guaranteed bound keeps the client portable across brokers.
		return fmt.Errorf("mqtt: client id %q exceeds 23 bytes", c.ClientID)
	}
	// The one-second floor is MQTT's own unit: the CONNECT field is whole
	// seconds, so a sub-second keep-alive is negotiated as zero — which means "no
	// keep-alive at all" — while the client would still be pinging at a fraction
	// of one. Refusing the configuration is the only outcome that keeps the
	// negotiated interval and the client's behaviour in agreement.
	if c.KeepAlive > 0 && c.KeepAlive < time.Second {
		return fmt.Errorf("mqtt: keep alive %s is below one second", c.KeepAlive)
	}
	return nil
}

// Client is a minimal MQTT 3.1.1 client with automatic reconnection.
//
// Run owns the connection lifecycle and blocks. Publish may be called from any
// goroutine while Run is active; it fails with ErrNotConnected between
// reconnects rather than queueing, so a caller that must not lose a command sees
// the failure and reports it instead of assuming delivery.
type Client struct {
	cfg Config

	writeMu sync.Mutex

	stateMu   sync.RWMutex
	conn      net.Conn
	connected bool
	connEpoch uint64

	// pendingMu guards the in-flight QoS 1 acknowledgement table. Entries are
	// closed exactly once, by whichever of the two outcomes happens first: the
	// matching PUBACK arriving on this epoch's connection, or the connection
	// ending. A waiter is closed with a result so the reader can tell those two
	// apart — see waiter.
	pendingMu sync.Mutex
	pending   map[uint16]*waiter
	nextID    uint16

	// lastSend is when this client last put any packet on the wire. The keep-alive
	// goroutine pings only once that is older than the ping cadence, so an
	// application publish pushes the next PINGREQ out instead of being followed
	// by one.
	lastSendMu sync.Mutex
	lastSend   time.Time
}

// waiter is the rendezvous between one in-flight QoS 1 publish and whatever
// settles it.
//
// It exists because a bare channel cannot say *why* it was closed. Closing on
// connection loss and closing on a real PUBACK look identical to a reader, and
// treating the first as the second is exactly the bug this replaced: it
// reported a command as published when the broker never acknowledged it. The
// buffered channel carries the outcome instead.
type waiter struct {
	// epoch is the connection epoch the publish was written on. A PUBACK only
	// completes a waiter whose epoch matches the connection it arrived on, so an
	// acknowledgement from an earlier session can never settle a later one.
	epoch   uint64
	outcome chan error
}

// New validates cfg and returns a client. It does not connect.
func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, pending: make(map[uint16]*waiter), nextID: 1}, nil
}

// KeepAlive returns the configured keep-alive interval.
func (c *Client) KeepAlive() time.Duration { return c.cfg.KeepAlive }

// Connected reports whether the session is currently established.
func (c *Client) Connected() bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.connected
}

// Run connects, subscribes and serves messages until ctx is cancelled. It
// reconnects with exponential backoff after any failure, including a rejected
// connect, so that a broker restart or a wrong credential is retried instead of
// silently stopping the ingress path.
//
// The backoff grows only across *consecutive* failures. A session that reached
// the connected state, however briefly, proves the broker is reachable again and
// the delay is reset to ReconnectMin before the next attempt is scheduled —
// otherwise one long outage would leave the client waiting ReconnectMax after
// every later hiccup, and a recovered broker would sit idle for thirty seconds
// per restart.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.ReconnectMin
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		epochBefore := c.epoch()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.epoch() != epochBefore {
			// The session got as far as reporting itself connected before it
			// ended. That is a healthy link going quiet on us, not a persistent
			// failure, so the delay must not keep growing across retries.
			backoff = c.cfg.ReconnectMin
		}
		c.cfg.Logger.LogAttrs(ctx, slog.LevelWarn, "mqtt session ended; reconnecting",
			slog.String("error", errorString(err)),
			slog.Duration("retry_in", backoff))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if err != nil && backoff < c.cfg.ReconnectMax {
			backoff *= 2
			if backoff > c.cfg.ReconnectMax {
				backoff = c.cfg.ReconnectMax
			}
		}
		if err == nil {
			backoff = c.cfg.ReconnectMin
		}
	}
}

// session runs one connection attempt end to end. It returns when the connection
// fails, when ctx is cancelled, or when the read loop exits.
func (c *Client) session(ctx context.Context) error {
	dialer := &net.Dialer{Timeout: DefaultConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.cfg.Address)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.cfg.Address, err)
	}
	if c.cfg.TLS != nil {
		conn = tls.Client(conn, c.cfg.TLS)
	}
	defer func() {
		// Every in-flight acknowledgement wait belongs to this connection and
		// must end with it. Ending them here rather than in the epoch bump below
		// keeps a waiter from outliving the socket it was written on.
		c.failPending()
		_ = conn.Close()
	}()

	if err := c.handshake(conn); err != nil {
		return err
	}

	// The subscription is established before the session is reported connected.
	// Connected is what a caller checks before publishing a control command or
	// expecting telemetry to flow, so it must mean "ready", not "the CONNECT was
	// acknowledged": a session that reports connected while its subscription is
	// still in flight drops every message published in that window.
	if err := c.subscribe(conn); err != nil {
		return err
	}
	c.setState(conn, true)
	defer c.setState(nil, false)
	c.cfg.Logger.LogAttrs(ctx, slog.LevelInfo, "mqtt connected",
		slog.String("address", c.cfg.Address), slog.String("client_id", c.cfg.ClientID))

	// The ping goroutine is stopped before the connection is closed by the
	// deferred Close above, so a ping cannot outlive its socket.
	pingCtx, stopPing := context.WithCancel(ctx)
	defer stopPing()
	go c.pingLoop(pingCtx, conn)

	// A blocking socket read cannot observe a context, so cancellation has to
	// close the socket to unblock it. Without this the client would keep serving
	// a cancelled context until the read deadline expired, which is twice the
	// keep-alive interval — long enough for a shutdown to look hung.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchDone:
		}
	}()

	if err := c.readLoop(ctx, conn, c.epoch()); err != nil {
		return err
	}
	return nil
}

// handshake sends CONNECT and verifies the CONNACK return code.
func (c *Client) handshake(conn net.Conn) error {
	connect := &Packet{
		Type:         PacketCONNECT,
		ClientID:     c.cfg.ClientID,
		KeepAlive:    uint16(c.cfg.KeepAlive / time.Second),
		CleanSession: c.cfg.CleanSession,
		HasUsername:  c.cfg.Username != "",
		Username:     c.cfg.Username,
		HasPassword:  c.cfg.Password != "",
		Password:     []byte(c.cfg.Password),
	}
	if err := c.writePacket(conn, connect); err != nil {
		return fmt.Errorf("send CONNECT: %w", err)
	}

	// A broker that never answers must not hold the ingress down forever.
	if err := conn.SetReadDeadline(time.Now().Add(DefaultConnectTimeout)); err != nil {
		return err
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	reply, err := ReadPacket(conn, c.cfg.ReadLimit)
	if err != nil {
		return fmt.Errorf("read CONNACK: %w", err)
	}
	if reply.Type != PacketCONNACK {
		return fmt.Errorf("%w: expected CONNACK, got %s", ErrMalformedPacket, reply.Type)
	}
	if reply.ReturnCode != ConnackAccepted {
		return fmt.Errorf("%w: %s", ErrRejected, reply.ReturnCode)
	}
	return nil
}

// subscribe establishes every configured subscription and requires the broker to
// grant the requested QoS, so a silently downgraded subscription is visible.
func (c *Client) subscribe(conn net.Conn) error {
	if len(c.cfg.Subscriptions) == 0 {
		return nil
	}
	packetID := c.allocatePacketID()
	subscribe := &Packet{
		Type:     PacketSUBSCRIBE,
		Flags:    0x02,
		PacketID: packetID,
		Filters:  c.cfg.Subscriptions,
	}
	if err := c.writePacket(conn, subscribe); err != nil {
		return fmt.Errorf("send SUBSCRIBE: %w", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(DefaultConnectTimeout)); err != nil {
		return err
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	reply, err := ReadPacket(conn, c.cfg.ReadLimit)
	if err != nil {
		return fmt.Errorf("read SUBACK: %w", err)
	}
	if reply.Type != PacketSUBACK {
		return fmt.Errorf("%w: expected SUBACK, got %s", ErrMalformedPacket, reply.Type)
	}
	if reply.PacketID != packetID {
		return fmt.Errorf("%w: SUBACK packet id %d does not match the request %d", ErrMalformedPacket, reply.PacketID, packetID)
	}
	if len(reply.GrantedQoS) != len(c.cfg.Subscriptions) {
		return fmt.Errorf("%w: SUBACK granted %d of %d subscriptions", ErrRejected, len(reply.GrantedQoS), len(c.cfg.Subscriptions))
	}
	for i, granted := range reply.GrantedQoS {
		if granted == 0x80 {
			return fmt.Errorf("%w: subscription to %q was refused", ErrRejected, c.cfg.Subscriptions[i].Topic)
		}
		if granted > c.cfg.Subscriptions[i].QoS {
			return fmt.Errorf("%w: subscription to %q granted QoS %d above the requested %d",
				ErrMalformedPacket, c.cfg.Subscriptions[i].Topic, granted, c.cfg.Subscriptions[i].QoS)
		}
	}
	return nil
}

// readLoop serves packets until the connection fails.
//
// There is exactly one reader per connection: the handshake, the subscription
// exchange and this loop all run on the same goroutine and never overlap. Two
// readers on one MQTT stream would interleave frames and corrupt the session.
func (c *Client) readLoop(ctx context.Context, conn net.Conn, epoch uint64) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The read deadline is twice the keep alive: one missed ping is
		// tolerated, two in a row means the peer is gone.
		if err := conn.SetReadDeadline(time.Now().Add(2 * c.cfg.KeepAlive)); err != nil {
			return err
		}
		packet, err := ReadPacket(conn, c.cfg.ReadLimit)
		if err != nil {
			if errors.Is(err, io.EOF) || isTimeout(err) {
				return fmt.Errorf("connection lost: %w", err)
			}
			return fmt.Errorf("read packet: %w", err)
		}

		switch packet.Type {
		case PacketPUBLISH:
			if err := c.handlePublish(ctx, conn, packet); err != nil {
				return err
			}
		case PacketPUBACK:
			c.completePending(packet.PacketID, epoch)
		case PacketPINGRESP:
			// The acknowledgement is what matters, and only in that it arrived at
			// all: it proves the peer is reading. Recording it would need a
			// second clock to give the fact any force, and the read deadline
			// already ends the session when one stops coming.
		default:
			c.cfg.Logger.LogAttrs(ctx, slog.LevelWarn, "ignoring unexpected packet",
				slog.String("type", packet.Type.String()))
		}
	}
}

// handlePublish acknowledges an inbound publish and dispatches it.
func (c *Client) handlePublish(ctx context.Context, conn net.Conn, packet Packet) error {
	if packet.QoS() > 0 {
		// The PUBACK is sent before the handler runs so that a slow handler
		// cannot cause the broker to redeliver the message.
		if err := c.writePacket(conn, &Packet{Type: PacketPUBACK, PacketID: packet.PacketID}); err != nil {
			return fmt.Errorf("send PUBACK: %w", err)
		}
	}
	if c.cfg.Handler != nil {
		c.cfg.Handler(ctx, Message{
			Topic:   packet.Topic,
			Payload: packet.Payload,
			QoS:     packet.QoS(),
			Retain:  packet.Retain(),
		})
	}
	return nil
}

// pingLoop keeps the session alive by putting a control packet on the wire well
// inside the negotiated keep-alive window.
//
// The cadence is idle-driven, not a fixed tick: a ping is sent only once nothing
// has been written for PingAfter. Any outbound packet is a keep-alive packet as
// far as MQTT is concerned, so an application publish pushes the next PINGREQ
// out instead of being followed by a redundant one. Inbound traffic does not —
// the client is responsible for keeping *its own* send half alive.
//
// Sending at the keep-alive interval itself is what the protocol forbids: one
// tick landing late means the broker sees a gap wider than 1.5 intervals and
// closes the link. Pinging at half the interval leaves the wire twice the margin
// it needs.
func (c *Client) pingLoop(ctx context.Context, conn net.Conn) {
	// Short ticks rather than one long timer: the deadline is about idle time,
	// and a fixed timer cannot know that the application has been writing in the
	// meantime.
	tick := c.cfg.PingAfter / 4
	if tick <= 0 {
		tick = time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.sinceLastSend() < c.cfg.PingAfter {
				continue
			}
			if err := c.writePacket(conn, &Packet{Type: PacketPINGREQ}); err != nil {
				c.cfg.Logger.LogAttrs(ctx, slog.LevelWarn, "keep-alive ping failed",
					slog.String("error", err.Error()))
				// Closing forces the read loop to notice and reconnect.
				_ = conn.Close()
				return
			}
		}
	}
}

// Publish sends an application message. QoS 1 waits for the broker's PUBACK and
// returns ErrPublishTimeout when it never arrives; QoS 0 returns once the bytes
// are written and therefore reports only a successful hand-off to the socket.
//
// A QoS 1 publish resolves to exactly one of four outcomes, and a caller that
// must not lose a command needs to tell them apart:
//
//   - nil: the broker sent a PUBACK for this packet identifier on this
//     connection. The message is the broker's responsibility from here.
//   - ErrPublishTimeout: nothing acknowledged it in the time allowed. The
//     message may or may not have been accepted.
//   - ErrConnectionLost: the socket ended before an acknowledgement arrived.
//     The message may or may not have been accepted.
//   - ctx.Err(): the caller stopped waiting.
//
// None of the failure cases can be resolved as nil: an unacknowledged publish is
// never reported as delivered.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, qos byte) error {
	conn, epoch, ok := c.connection()
	if !ok {
		return ErrNotConnected
	}
	if qos > 1 {
		return fmt.Errorf("%w: publish QoS %d is not supported", ErrUnsupportedPacket, qos)
	}

	packet := &Packet{Type: PacketPUBLISH, Topic: topic, Payload: payload}
	if qos == 1 {
		packet.SetPublishFlags(false, 1, false)
		packet.PacketID = c.allocatePacketID()
		w := c.registerPending(packet.PacketID, epoch)
		defer c.cancelPending(packet.PacketID)

		if err := c.writePacket(conn, packet); err != nil {
			return fmt.Errorf("publish %s: %w", topic, err)
		}
		select {
		case outcome := <-w.outcome:
			// A nil outcome is a real PUBACK; anything else is a connection
			// failure or a lost socket. Either way the caller sees the truth.
			if outcome != nil {
				return outcome
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.cfg.PublishTimeout):
			return fmt.Errorf("%w: topic %s packet id %d", ErrPublishTimeout, topic, packet.PacketID)
		}
	}

	packet.SetPublishFlags(false, 0, false)
	if err := c.writePacket(conn, packet); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

// writePacket serialises a packet under the write lock and applies the write
// deadline. Concurrent writers are serialised because MQTT framing is a byte
// stream: interleaving two packets would corrupt the session. It also records
// the send time, which is what keeps the keep-alive goroutine from pinging on
// top of traffic the application is already putting on the wire.
func (c *Client) writePacket(conn net.Conn, packet *Packet) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := conn.SetWriteDeadline(time.Now().Add(DefaultWriteTimeout)); err != nil {
		return err
	}
	if err := packet.Encode(conn); err != nil {
		return err
	}
	c.lastSendMu.Lock()
	c.lastSend = time.Now()
	c.lastSendMu.Unlock()
	return nil
}

// sinceLastSend reports how long it has been since this client last wrote
// anything to the broker.
func (c *Client) sinceLastSend() time.Duration {
	c.lastSendMu.Lock()
	defer c.lastSendMu.Unlock()
	return time.Since(c.lastSend)
}

// setState publishes the current connection.
//
// Establishing a connection bumps connEpoch. Any acknowledgement still in flight
// is then answered as ErrConnectionLost rather than left hanging: a PUBACK from
// an earlier socket has no bearing on a later one, and a waiter that never
// resolves is indistinguishable from a hang to its caller.
func (c *Client) setState(conn net.Conn, connected bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	c.conn = conn
	c.connected = connected
	if connected {
		c.connEpoch++
	}
}

// epoch returns the connection epoch currently in flight. It is read after the
// connection has been published so a publisher and the read loop always agree on
// which socket the in-flight acknowledgements belong to.
func (c *Client) epoch() uint64 {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.connEpoch
}

// connection returns the live connection and its epoch, or false when the
// session is down. The epoch travels with the connection so a publisher can
// notice that the socket it wrote to is no longer the one in flight.
func (c *Client) connection() (net.Conn, uint64, bool) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	if !c.connected {
		return nil, 0, false
	}
	return c.conn, c.connEpoch, true
}

// allocatePacketID returns the next non-zero packet identifier.
func (c *Client) allocatePacketID() uint16 {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	c.nextID++
	if c.nextID == 0 {
		c.nextID = 1
	}
	return c.nextID
}

// registerPending creates the waiter a PUBACK will settle. It is registered with
// the connection epoch so an acknowledgement arriving later on a different
// socket is not mistaken for one belonging to this publish.
func (c *Client) registerPending(id uint16, epoch uint64) *waiter {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	w := &waiter{epoch: epoch, outcome: make(chan error, 1)}
	c.pending[id] = w
	return w
}

// cancelPending removes a waiter that is no longer needed.
func (c *Client) cancelPending(id uint16) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	delete(c.pending, id)
}

// completePending settles the waiter for a matching PUBACK.
//
// The epoch is what makes this safe across reconnects: a PUBACK only settles a
// publish that was written on the same connection. An identifier reused across
// sessions is a new message, and letting an old acknowledgement settle it would
// report an unacknowledged command as published.
func (c *Client) completePending(id uint16, epoch uint64) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	if w, ok := c.pending[id]; ok && w.epoch == epoch {
		delete(c.pending, id)
		// Buffered, and closed exactly once by whoever removes it above, so this
		// send cannot block or panic.
		w.outcome <- nil
	}
}

// failPending ends every in-flight acknowledgement wait with a connection loss.
//
// A waiter removed here has no PUBACK to its name, so it must never resolve
// successfully: the message may have been lost with the socket. Callers see
// ErrConnectionLost and report the command as unconfirmed.
func (c *Client) failPending() {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	for id, w := range c.pending {
		delete(c.pending, id)
		w.outcome <- ErrConnectionLost
	}
}

// isTimeout reports whether err is a deadline expiry.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// errorString renders an error for logging without a nil pointer dereference.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
