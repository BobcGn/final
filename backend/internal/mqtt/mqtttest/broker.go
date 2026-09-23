// Package mqtttest provides an in-process MQTT 3.1.1 broker for integration
// tests.
//
// It is a real broker over a real TCP socket speaking the real protocol, not a
// stub of the client under test: the client's framing, handshake, subscription,
// acknowledgement and keep-alive paths are all exercised end to end. The only
// thing it does not model is broker persistence across restarts, which the
// device contract does not rely on because control messages are never retained.
package mqtttest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/BobcGn/final/backend/internal/mqtt"
)

// Errors returned by the broker.
var (
	// ErrClosed reports an operation on a stopped broker.
	ErrClosed = errors.New("mqtttest: broker is closed")
)

// Message is a message the broker observed on a topic.
type Message struct {
	Topic   string
	Payload []byte
	QoS     byte
	Client  string
}

// Broker is a minimal MQTT 3.1.1 broker.
type Broker struct {
	listener net.Listener

	// Credentials, when non-nil, must match the CONNECT user name and password.
	// A mismatch is answered with CONNACK bad credentials.
	Credentials *Credentials
	// RejectSubscriptions makes every SUBSCRIBE fail, used to test that the
	// client surfaces a refused subscription instead of running blind.
	RejectSubscriptions bool
	// WithholdPubAck makes the broker accept a QoS 1 publish and never answer
	// with its PUBACK. It is how a test observes the client's behaviour for a
	// message the broker took but has not acknowledged, which is the state a
	// command must never resolve as success.
	WithholdPubAck bool
	// Silence makes the broker stop sending anything at all once a session is
	// established: no PUBACK, no PINGRESP, no delivery. It is how a test reaches
	// the client's read deadline without pulling the socket, which is what a
	// broker that has stopped answering looks like.
	Silence bool
	// SpuriousPubAck, when non-zero, is answered instead of the publish's own
	// packet identifier. It stands in for an acknowledgement belonging to a
	// different message — the only way a mis-scoped acknowledgement table can be
	// caught from outside the client.
	SpuriousPubAck uint16
	// TakeOver makes a new session with an existing client identifier discard
	// the old one, which is what a real broker does and what the client has to
	// recover from. Without it the broker would keep both, and a test could not
	// observe the client's behaviour across a duplicate identifier.
	TakeOver bool
	// rejectConnections makes the broker answer CONNECT with a rejection. It is
	// changed through RejectConnections so tests can safely turn a once-healthy
	// broker into a sequence of failed connection attempts.
	rejectConnections bool

	mu           sync.Mutex
	conns        map[net.Conn]struct{}
	sessions     map[*session]struct{}
	observed     []Message
	connectTimes []time.Time
	// pings counts every PINGREQ the broker received. A keep-alive loop that
	// never fires is indistinguishable from one that is never needed without a
	// count, and the distinction is the whole point of the idle-driven cadence.
	pings  int
	closed bool

	wg sync.WaitGroup
}

// Credentials are the user name and password the broker accepts.
type Credentials struct {
	Username string
	Password string
}

// session is one connected client.
type session struct {
	conn          net.Conn
	clientID      string
	subscriptions []mqtt.TopicFilter
	writeMu       sync.Mutex
}

// Start listens on a loopback port and serves until Close is called.
func Start() (*Broker, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	broker := &Broker{
		listener: listener,
		conns:    make(map[net.Conn]struct{}),
		sessions: make(map[*session]struct{}),
	}
	broker.wg.Add(1)
	go broker.serve()
	return broker, nil
}

// Addr returns the host:port the broker listens on.
func (b *Broker) Addr() string { return b.listener.Addr().String() }

// Observed returns a copy of every application message the broker routed,
// including messages published by the backend itself.
func (b *Broker) Observed() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]Message(nil), b.observed...)
}

// ObservedOn returns the messages published to one exact topic.
func (b *Broker) ObservedOn(topic string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	var matched []Message
	for _, message := range b.observed {
		if message.Topic == topic {
			matched = append(matched, message)
		}
	}
	return matched
}

// Pings returns how many PINGREQ the broker has received. A keep-alive that
// never fires and one that is never needed look identical without a count.
func (b *Broker) Pings() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pings
}

// RejectConnections controls whether CONNECT is refused. Existing sessions are
// not affected until they reconnect.
func (b *Broker) RejectConnections(reject bool) {
	b.mu.Lock()
	b.rejectConnections = reject
	b.mu.Unlock()
}

// ConnectTimes returns a copy of the times at which CONNECT packets arrived.
func (b *Broker) ConnectTimes() []time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]time.Time(nil), b.connectTimes...)
}

// WithholdPubAcks makes the broker stop answering QoS 1 publishes. It is how a
// test observes a message the broker took but has not acknowledged.
func (b *Broker) WithholdPubAcks() {
	b.mu.Lock()
	b.WithholdPubAck = true
	b.mu.Unlock()
}

// StopAnswering makes the broker go quiet after its sessions are established:
// no PUBACK, no PINGRESP, no delivery. The sockets stay open, so the client's
// read deadline is what ends the session.
func (b *Broker) StopAnswering() {
	b.mu.Lock()
	b.Silence = true
	b.mu.Unlock()
}

// AnswerWithPubAck makes the broker acknowledge QoS 1 publishes with the given
// packet identifier instead of the one the publish carried. It stands in for an
// acknowledgement belonging to a different message. A zero id turns the behaviour
// off again.
func (b *Broker) AnswerWithPubAck(id uint16) {
	b.mu.Lock()
	b.SpuriousPubAck = id
	b.mu.Unlock()
}

// SendUnsuback pushes a packet this client never sends and has no use for. It is
// how a test observes that an unknown packet is skipped rather than costing the
// session.
func (b *Broker) SendUnsuback() {
	b.mu.Lock()
	sessions := make([]*session, 0, len(b.sessions))
	for current := range b.sessions {
		sessions = append(sessions, current)
	}
	b.mu.Unlock()

	for _, current := range sessions {
		_ = current.write(&mqtt.Packet{Type: mqtt.PacketUNSUBACK, PacketID: 7})
	}
}

// DropConnections closes every accepted connection, simulating a broker restart
// from the client's point of view. The listener stays up so clients can
// reconnect.
//
// Every accepted connection is closed, not only the ones that completed their
// handshake. A peer that connects and then says nothing is a real scenario — a
// port scanner, a crashed client, a test that only checks the port is open — and
// leaving it open would block Close forever, because the handler goroutine would
// still be reading.
func (b *Broker) DropConnections() {
	b.mu.Lock()
	connections := make([]net.Conn, 0, len(b.conns))
	for conn := range b.conns {
		connections = append(connections, conn)
	}
	b.mu.Unlock()

	for _, conn := range connections {
		_ = conn.Close()
	}
}

// Close stops the broker and closes every connection.
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	err := b.listener.Close()
	b.DropConnections()
	b.wg.Wait()
	return err
}

// serve accepts connections until the listener is closed.
func (b *Broker) serve() {
	defer b.wg.Done()

	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		// Register under the same lock that Close uses to set closed. Otherwise
		// an accepted connection can be registered after DropConnections takes
		// its snapshot, leaving its handler blocked on a silent peer forever.
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			_ = conn.Close()
			return
		}
		b.conns[conn] = struct{}{}
		b.mu.Unlock()

		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.handle(conn)
		}()
	}
}

// handle serves one client connection.
func (b *Broker) handle(conn net.Conn) {
	current := &session{conn: conn}
	defer func() {
		b.mu.Lock()
		delete(b.sessions, current)
		delete(b.conns, conn)
		b.mu.Unlock()
		_ = conn.Close()
	}()

	if err := b.negotiate(current); err != nil {
		return
	}
	b.mu.Lock()
	b.sessions[current] = struct{}{}
	b.mu.Unlock()

	for {
		packet, err := mqtt.ReadPacket(conn, 0)
		if err != nil {
			return
		}
		if err := b.dispatch(current, packet); err != nil {
			return
		}
	}
}

// negotiate performs the CONNECT/CONNACK exchange.
func (b *Broker) negotiate(current *session) error {
	packet, err := mqtt.ReadPacket(current.conn, 0)
	if err != nil {
		return err
	}
	if packet.Type != mqtt.PacketCONNECT {
		return fmt.Errorf("mqtttest: expected CONNECT, got %s", packet.Type)
	}
	current.clientID = packet.ClientID
	b.mu.Lock()
	b.connectTimes = append(b.connectTimes, time.Now())
	reject := b.rejectConnections
	b.mu.Unlock()
	if reject {
		return current.write(&mqtt.Packet{Type: mqtt.PacketCONNACK, ReturnCode: mqtt.ConnackServerUnavailable})
	}

	if b.TakeOver {
		b.discardExisting(current.clientID)
	}

	if b.Credentials != nil {
		if packet.Username != b.Credentials.Username || string(packet.Password) != b.Credentials.Password {
			return current.write(&mqtt.Packet{Type: mqtt.PacketCONNACK, ReturnCode: mqtt.ConnackBadCredentials})
		}
	}
	return current.write(&mqtt.Packet{Type: mqtt.PacketCONNACK, ReturnCode: mqtt.ConnackAccepted})
}

// discardExisting closes any session already using the identifier, the way a
// real broker treats a second connection from the same client.
func (b *Broker) discardExisting(clientID string) {
	b.mu.Lock()
	victims := make([]*session, 0, len(b.sessions))
	for current := range b.sessions {
		if current.clientID == clientID {
			victims = append(victims, current)
		}
	}
	b.mu.Unlock()

	for _, victim := range victims {
		_ = victim.conn.Close()
	}
}

// dispatch handles one packet from a connected client.
func (b *Broker) dispatch(current *session, packet mqtt.Packet) error {
	b.mu.Lock()
	silent := b.Silence
	withhold := b.WithholdPubAck
	b.mu.Unlock()

	if silent {
		// The broker has gone quiet: the packet is consumed and nothing is
		// answered. What the client does next is the subject of the test.
		if packet.Type == mqtt.PacketPINGREQ {
			b.mu.Lock()
			b.pings++
			b.mu.Unlock()
		}
		return nil
	}

	switch packet.Type {
	case mqtt.PacketPUBLISH:
		b.mu.Lock()
		b.observed = append(b.observed, Message{
			Topic:   packet.Topic,
			Payload: append([]byte(nil), packet.Payload...),
			QoS:     packet.QoS(),
			Client:  current.clientID,
		})
		b.mu.Unlock()

		if packet.QoS() > 0 && !withhold {
			id := packet.PacketID
			b.mu.Lock()
			spurious := b.SpuriousPubAck
			b.mu.Unlock()
			if spurious != 0 {
				id = spurious
			}
			if err := current.write(&mqtt.Packet{Type: mqtt.PacketPUBACK, PacketID: id}); err != nil {
				return err
			}
		}
		b.route(packet)
		return nil

	case mqtt.PacketSUBSCRIBE:
		granted := make([]byte, 0, len(packet.Filters))
		for _, filter := range packet.Filters {
			if b.RejectSubscriptions {
				granted = append(granted, 0x80)
				continue
			}
			current.subscriptions = append(current.subscriptions, filter)
			granted = append(granted, filter.QoS)
		}
		return current.write(&mqtt.Packet{Type: mqtt.PacketSUBACK, PacketID: packet.PacketID, GrantedQoS: granted})

	case mqtt.PacketPINGREQ:
		b.mu.Lock()
		b.pings++
		b.mu.Unlock()
		return current.write(&mqtt.Packet{Type: mqtt.PacketPINGRESP})

	case mqtt.PacketDISCONNECT:
		return io.EOF

	case mqtt.PacketPUBACK:
		// Acknowledgement of a message the broker delivered; nothing to do
		// because the broker keeps no redelivery queue.
		return nil

	default:
		return fmt.Errorf("mqtttest: unsupported packet %s", packet.Type)
	}
}

// route delivers a publish to every matching subscriber.
func (b *Broker) route(packet mqtt.Packet) {
	b.mu.Lock()
	sessions := make([]*session, 0, len(b.sessions))
	for current := range b.sessions {
		sessions = append(sessions, current)
	}
	b.mu.Unlock()

	for _, current := range sessions {
		qos, matched := current.match(packet.Topic)
		if !matched {
			continue
		}
		_ = current.write(&mqtt.Packet{
			Type:     mqtt.PacketPUBLISH,
			Topic:    packet.Topic,
			Payload:  packet.Payload,
			PacketID: nextPacketID(),
			Flags:    (qos & 0x03) << 1,
		})
	}
}

// nextPacketID produces broker-side packet identifiers for outbound publishes.
var packetIDCounter struct {
	sync.Mutex
	value uint16
}

// nextPacketID returns the next non-zero identifier.
func nextPacketID() uint16 {
	packetIDCounter.Lock()
	defer packetIDCounter.Unlock()

	packetIDCounter.value++
	if packetIDCounter.value == 0 {
		packetIDCounter.value = 1
	}
	return packetIDCounter.value
}

// match reports the granted QoS when a subscription matches the topic.
func (s *session) match(topic string) (byte, bool) {
	for _, filter := range s.subscriptions {
		if MatchTopic(filter.Topic, topic) {
			return filter.QoS, true
		}
	}
	return 0, false
}

// write serialises a packet onto the session.
func (s *session) write(packet *mqtt.Packet) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return packet.Encode(s.conn)
}

// MatchTopic reports whether an MQTT topic filter matches a topic, supporting
// the `+` single-level and `#` multi-level wildcards.
func MatchTopic(filter, topic string) bool {
	filterLevels := strings.Split(filter, "/")
	topicLevels := strings.Split(topic, "/")

	for i, level := range filterLevels {
		if level == "#" {
			// `#` matches the parent level and everything below it, and is only
			// valid as the final level.
			return i == len(filterLevels)-1
		}
		if i >= len(topicLevels) {
			return false
		}
		if level == "+" {
			continue
		}
		if level != topicLevels[i] {
			return false
		}
	}
	return len(filterLevels) == len(topicLevels)
}
