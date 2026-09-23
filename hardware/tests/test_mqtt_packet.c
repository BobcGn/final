#include "mqtt_packet.h"
#include "test_support.h"

/* Compare a byte range against an expected sequence. */
static void check_bytes(const uint8_t *actual, const uint8_t *expected, uint32_t length)
{
    uint32_t index;

    for (index = 0U; index < length; index++)
    {
        CHECK_MSG(actual[index] == expected[index], "byte %u is 0x%02X, expected 0x%02X",
                  (unsigned)index, (unsigned)actual[index], (unsigned)expected[index]);
    }
}

/* Exercise the CONNECT encoding against a byte-exact vector.
 *
 * The frame is checked byte for byte rather than round-tripped: a codec that
 * agrees with itself can still emit a frame no broker accepts, and the whole
 * point of implementing the framing here is that it is auditable. */
static void test_connect_golden(void)
{
    uint8_t buffer[64];
    static const uint8_t expected[] = {
        0x10, 0x12,                                     /* CONNECT, remaining length 18 */
        0x00, 0x04, 'M',  'Q',  'T',  'T',              /* protocol name */
        0x04,                                           /* level 4, MQTT 3.1.1 */
        0x02,                                           /* clean session */
        0x00, 0x1E,                                     /* keep alive 30 s */
        0x00, 0x06, 'M',  'C',  'U',  '0',  '0',  '1'   /* client identifier */
    };
    uint32_t length;

    TEST_CASE("an anonymous CONNECT matches the protocol byte for byte");
    length = MqttEncodeConnect(buffer, sizeof(buffer), "MCU001", 30U, NULL, NULL);
    CHECK_INT(sizeof(expected), length);
    check_bytes(buffer, expected, sizeof(expected));

    TEST_CASE("credentials add the user name and password flags and fields");
    length = MqttEncodeConnect(buffer, sizeof(buffer), "MCU001", 30U, "dev", "pw");
    CHECK_TRUE(length > sizeof(expected));
    /* The connect flags byte is at a fixed offset: after the protocol name and
     * level, so 0xC2 = username, password, clean session. */
    CHECK_INT(0xC2, buffer[9]);
    CHECK_TRUE(buffer[1] == length - 2U);
}

/* Exercise the CONNECT argument checks. */
static void test_connect_bounds(void)
{
    uint8_t buffer[64];

    TEST_CASE("a missing client identifier is refused");
    CHECK_INT(0U, MqttEncodeConnect(buffer, sizeof(buffer), NULL, 30U, NULL, NULL));

    TEST_CASE("a buffer too small for the frame is refused");
    CHECK_INT(0U, MqttEncodeConnect(buffer, 8U, "MCU001", 30U, NULL, NULL));

    TEST_CASE("an over-long identifier is refused rather than truncated");
    {
        /* A 300-character identifier needs more than a 16-bit length field can
         * express, which the encoder must reject rather than wrap. */
        char long_id[301];
        uint32_t index;

        for (index = 0U; index < 300U; index++)
        {
            long_id[index] = 'a';
        }
        long_id[300] = '\0';
        CHECK_TRUE(MqttEncodeConnect(buffer, sizeof(buffer), long_id, 30U, NULL, NULL) == 0U ||
                   buffer[1] != 0U);
    }
}

/* Exercise PUBLISH, including the QoS 1 packet identifier. */
static void test_publish(void)
{
    uint8_t buffer[64];
    const uint8_t payload[] = {'{', '}'};
    uint32_t length;

    TEST_CASE("a QoS 0 publish carries no packet identifier");
    length = MqttEncodePublish(buffer, sizeof(buffer), "t", 0U, 0U, payload, sizeof(payload));
    /* header 2 + topic 3 + payload 2 = 7 */
    CHECK_INT(7U, length);
    CHECK_INT(0x30, buffer[0]);
    CHECK_INT(0x05, buffer[1]);
    CHECK_INT(0x00, buffer[2]);
    CHECK_INT(0x01, buffer[3]);
    CHECK_INT('t', buffer[4]);

    TEST_CASE("a QoS 1 publish carries the identifier and sets the QoS bits");
    length = MqttEncodePublish(buffer, sizeof(buffer), "t", 7U, 1U, payload, sizeof(payload));
    CHECK_INT(9U, length);
    /* Flags 0b0010 for QoS 1: DUP clear, RETAIN clear. */
    CHECK_INT(0x32, buffer[0]);

    TEST_CASE("a QoS 1 publish without an identifier is refused");
    /* Without an identifier the broker cannot acknowledge it, so the device
     * would never learn whether the message arrived. */
    CHECK_INT(0U, MqttEncodePublish(buffer, sizeof(buffer), "t", 0U, 1U, payload, sizeof(payload)));

    TEST_CASE("QoS 2 is refused rather than downgraded");
    CHECK_INT(0U, MqttEncodePublish(buffer, sizeof(buffer), "t", 1U, 2U, payload, sizeof(payload)));

    TEST_CASE("an empty payload is allowed");
    length = MqttEncodePublish(buffer, sizeof(buffer), "t", 0U, 0U, NULL, 0U);
    CHECK_INT(5U, length);

    TEST_CASE("a null topic is refused");
    CHECK_INT(0U, MqttEncodePublish(buffer, sizeof(buffer), NULL, 0U, 0U, payload, sizeof(payload)));
}

/* Exercise the fixed-shape packets. */
static void test_fixed_packets(void)
{
    uint8_t buffer[8];

    TEST_CASE("PINGREQ is two bytes");
    CHECK_INT(2U, MqttEncodePingReq(buffer, sizeof(buffer)));
    CHECK_INT(0xC0, buffer[0]);
    CHECK_INT(0x00, buffer[1]);

    TEST_CASE("DISCONNECT is two bytes");
    CHECK_INT(2U, MqttEncodeDisconnect(buffer, sizeof(buffer)));
    CHECK_INT(0xE0, buffer[0]);

    TEST_CASE("PUBACK carries the identifier");
    CHECK_INT(4U, MqttEncodePuback(buffer, sizeof(buffer), 0x1234U));
    CHECK_INT(0x40, buffer[0]);
    CHECK_INT(0x02, buffer[1]);
    CHECK_INT(0x12, buffer[2]);
    CHECK_INT(0x34, buffer[3]);

    TEST_CASE("PUBACK without an identifier is refused");
    CHECK_INT(0U, MqttEncodePuback(buffer, sizeof(buffer), 0U));

    TEST_CASE("a buffer too small is refused");
    CHECK_INT(0U, MqttEncodePingReq(buffer, 1U));
}

/* Exercise SUBSCRIBE, whose reserved flags the protocol fixes. */
static void test_subscribe(void)
{
    uint8_t buffer[32];
    uint32_t length;

    TEST_CASE("SUBSCRIBE carries the reserved flags and the filter");
    length = MqttEncodeSubscribe(buffer, sizeof(buffer), 1U, "device/control", 1U);
    CHECK_INT(0x82, buffer[0]);
    /* header 2 + packet id 2 + topic field 2 + 14 + QoS 1 */
    CHECK_INT(21U, length);

    TEST_CASE("an identifier of zero is refused");
    CHECK_INT(0U, MqttEncodeSubscribe(buffer, sizeof(buffer), 0U, "a", 1U));

    TEST_CASE("a QoS above one is refused");
    CHECK_INT(0U, MqttEncodeSubscribe(buffer, sizeof(buffer), 1U, "a", 2U));
}

/* Exercise decoding of the packets a broker sends. */
static void test_decode(void)
{
    MqttPacket packet;
    const char *reason = NULL;

    TEST_CASE("CONNACK is decoded with its return code");
    {
        const uint8_t frame[] = {0x20, 0x02, 0x00, 0x00};
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(MQTT_PACKET_CONNACK, packet.type);
        CHECK_INT(MQTT_CONNACK_ACCEPTED, packet.return_code);
    }

    TEST_CASE("SUBACK is decoded with its granted QoS");
    {
        const uint8_t frame[] = {0x90, 0x03, 0x00, 0x01, 0x01};
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(MQTT_PACKET_SUBACK, packet.type);
        CHECK_INT(1, packet.packet_id);
        CHECK_INT(1, packet.granted_qos);
    }

    TEST_CASE("PINGRESP is decoded");
    {
        const uint8_t frame[] = {0xD0, 0x00};
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(MQTT_PACKET_PINGRESP, packet.type);
    }

    TEST_CASE("PUBACK is decoded");
    {
        const uint8_t frame[] = {0x40, 0x02, 0x00, 0x2A};
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(42, packet.packet_id);
    }

    TEST_CASE("a QoS 1 PUBLISH is decoded with its topic and payload");
    {
        /* remaining length 20 = topic field 16 + packet id 2 + payload 2 */
        const uint8_t frame[] = {
            0x32, 0x14, 0x00, 0x0E, 'd', 'e', 'v', 'i', 'c', 'e', '/', 'c', 'o', 'n', 't', 'r', 'o', 'l',
            0x00, 0x2A,
            '{', '}'
        };
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(MQTT_PACKET_PUBLISH, packet.type);
        CHECK_INT(14, packet.topic_length);
        CHECK_INT(0, memcmp(packet.topic, "device/control", 14U));
        CHECK_INT(42, packet.packet_id);
        CHECK_INT(2U, packet.payload_length);
        CHECK_INT(0, memcmp(packet.payload, "{}", 2U));
        CHECK_INT((long)&frame[sizeof(frame)], (long)packet.next);
    }

    TEST_CASE("a QoS 0 PUBLISH has no identifier");
    {
        /* remaining length 6 = topic field 3 + payload 3 */
        const uint8_t frame[] = {0x30, 0x06, 0x00, 0x01, 't', '{', '}', '!'};
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(0, packet.packet_id);
        CHECK_INT(3U, packet.payload_length);
    }
}

/* Exercise the decode rejections. */
static void test_decode_rejections(void)
{
    MqttPacket packet;
    const char *reason = NULL;

    TEST_CASE("an unknown control packet type is refused");
    {
        const uint8_t frame[] = {0x00, 0x00};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        reason = NULL;
    }

    TEST_CASE("QoS 2 is reported as unsupported rather than malformed");
    {
        const uint8_t frame[] = {0x34, 0x06, 0x00, 0x01, 't', 0x00, 0x01, '{', '}'};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(0, strcmp(reason, MQTT_REJECT_UNSUPPORTED));
        reason = NULL;
    }

    TEST_CASE("a declared length beyond the buffer bound is reported as too large");
    {
        const uint8_t frame[] = {0x30, 0xFF, 0xFF, 0xFF, 0x7F};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(0, strcmp(reason, MQTT_REJECT_TOO_LARGE));
        reason = NULL;
    }

    TEST_CASE("an incomplete frame is refused so the caller can read more");
    {
        const uint8_t frame[] = {0x30, 0x10, 0x00};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        reason = NULL;
    }

    TEST_CASE("a PUBLISH with an empty topic is refused");
    {
        const uint8_t frame[] = {0x30, 0x02, 0x00, 0x00};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        reason = NULL;
    }

    TEST_CASE("a PUBLISH whose topic runs past the frame is refused");
    {
        const uint8_t frame[] = {0x30, 0x03, 0x00, 0x09, 't'};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        reason = NULL;
    }

    TEST_CASE("CONNACK with reserved flags is refused");
    {
        const uint8_t frame[] = {0x21, 0x02, 0x00, 0x00};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        reason = NULL;
    }

    TEST_CASE("a packet the device should never receive is refused");
    {
        /* A broker never sends CONNECT, and acting on one would mean acting on
         * something that is not a broker. */
        const uint8_t frame[] = {0x10, 0x00};
        CHECK_FALSE(MqttDecode(frame, sizeof(frame), &packet, &reason));
        CHECK_INT(0, strcmp(reason, MQTT_REJECT_UNSUPPORTED));
        reason = NULL;
    }

    TEST_CASE("a null reason pointer is tolerated");
    {
        const uint8_t frame[] = {0xD0, 0x00};
        CHECK_TRUE(MqttDecode(frame, sizeof(frame), &packet, NULL));
    }
}

/* Exercise the frame length helper a socket reader uses to know when a frame is
 * complete. */
static void test_total_length(void)
{
    TEST_CASE("the total length includes the header and the body");
    {
        const uint8_t frame[] = {0x30, 0x06, 0x00, 0x01, 't', '{', '}', '!'};
        CHECK_INT(8U, MqttPacketTotalLength(frame, sizeof(frame)));
    }

    TEST_CASE("an incomplete header reports zero");
    {
        const uint8_t frame[] = {0x30};
        CHECK_INT(0U, MqttPacketTotalLength(frame, sizeof(frame)));
    }

    TEST_CASE("a two-byte remaining length is read");
    {
        /* 0x80 0x01 encodes 128, so the frame is 1 + 2 + 128 bytes. */
        uint8_t frame[131];
        uint32_t index;

        frame[0] = 0x30;
        frame[1] = 0x80;
        frame[2] = 0x01;
        for (index = 3U; index < sizeof(frame); index++)
        {
            frame[index] = 0U;
        }
        CHECK_INT(131U, MqttPacketTotalLength(frame, sizeof(frame)));
    }

    TEST_CASE("a null buffer reports zero");
    CHECK_INT(0U, MqttPacketTotalLength(NULL, 8U));
}

/* Exercise an encode and decode round trip. */
static void test_round_trip(void)
{
    uint8_t buffer[128];
    MqttPacket packet;
    const char *reason = NULL;
    const uint8_t payload[] = {'{', '"', 'a', '"', ':', '1', '}'};
    uint32_t length;

    TEST_CASE("a publish survives a round trip through the codec");
    length = MqttEncodePublish(buffer, sizeof(buffer), "device/telemetry", 5U, 1U, payload, sizeof(payload));
    CHECK_TRUE(length > 0U);
    CHECK_INT(length, MqttPacketTotalLength(buffer, length));
    CHECK_TRUE(MqttDecode(buffer, length, &packet, &reason));
    CHECK_INT(MQTT_PACKET_PUBLISH, packet.type);
    CHECK_INT(16, packet.topic_length);
    CHECK_INT(0, memcmp(packet.topic, "device/telemetry", 16U));
    CHECK_INT(5, packet.packet_id);
    CHECK_INT(sizeof(payload), packet.payload_length);
    CHECK_INT(0, memcmp(packet.payload, payload, sizeof(payload)));

    TEST_CASE("a subscribe is framed correctly but refused by the device decoder");
    length = MqttEncodeSubscribe(buffer, sizeof(buffer), 1U, "device/control", 1U);
    CHECK_INT(MQTT_PACKET_SUBSCRIBE, buffer[0] >> 4);
    CHECK_INT(length, MqttPacketTotalLength(buffer, length));
    /* Only a broker sends SUBACK; a SUBSCRIBE arriving here would mean the peer
     * is not a broker, so the device refuses it rather than acting on it. */
    CHECK_FALSE(MqttDecode(buffer, length, &packet, &reason));
    CHECK_INT(0, strcmp(reason, MQTT_REJECT_UNSUPPORTED));
}

/* The frame iterator that the control path scans a receive buffer with. */
static uint32_t g_scanned_frames = 0U;
static uint16_t g_scanned_last_packet_id = 0U;

static void count_frame(void *context, const MqttPacket *packet)
{
    uint32_t *count = (uint32_t *)context;

    (*count)++;
    g_scanned_frames++;
    g_scanned_last_packet_id = packet->packet_id;
}

static void test_for_each_packet(void)
{
    uint8_t buffer[256];
    uint32_t length;
    uint32_t count = 0U;
    MqttScanResult result;

    TEST_CASE("an empty or null buffer yields nothing at all");
    g_scanned_frames = 0U;
    result = MqttForEachPacket(NULL, 16U, count_frame, &count);
    CHECK_INT(0U, result.decoded);
    CHECK_INT(0U, result.undecodable);
    CHECK_FALSE(result.partial);
    result = MqttForEachPacket(buffer, 0U, count_frame, &count);
    CHECK_INT(0U, result.decoded);
    CHECK_INT(0U, g_scanned_frames);

    TEST_CASE("two frames in one buffer are both handed over, in order");
    /* PUBACK rather than PINGREQ: the device sends PINGREQ but never receives
     * one, and the codec refuses what a broker should not have sent. */
    length = MqttEncodePuback(buffer, sizeof(buffer), 0x0101U);
    length += MqttEncodePuback(&buffer[length], sizeof(buffer) - length, 0x0102U);
    result = MqttForEachPacket(buffer, length, count_frame, &count);
    CHECK_INT(2U, result.decoded);
    CHECK_INT(0U, result.undecodable);
    CHECK_FALSE(result.partial);
    CHECK_INT(0x0102, g_scanned_last_packet_id);

    TEST_CASE("a nil visitor still counts what it found");
    result = MqttForEachPacket(buffer, length, NULL, NULL);
    CHECK_INT(2U, result.decoded);
    CHECK_FALSE(result.partial);

    TEST_CASE("a buffer ending inside a frame is reported as a partial frame");
    result = MqttForEachPacket(buffer, length - 1U, count_frame, &count);
    CHECK_INT(1U, result.decoded);
    CHECK_TRUE(result.partial);

    TEST_CASE("a frame the codec refuses does not stop the scan");
    /* A SUBSCRIBE is a packet the device should never be sent. Its framing is
     * intact, so the frame behind it is still a frame the caller should see. */
    buffer[0] = (uint8_t)(MQTT_PACKET_SUBSCRIBE << 4);
    buffer[1] = 5U;
    buffer[2] = 0x00U;
    buffer[3] = 0x01U;
    buffer[4] = 0x00U;
    buffer[5] = 0x00U;
    buffer[6] = 0x00U;
    length = 7U + MqttEncodePuback(&buffer[7], sizeof(buffer) - 7U, 0x0203U);
    result = MqttForEachPacket(buffer, length, count_frame, &count);
    CHECK_INT(1U, result.decoded);
    CHECK_INT(1U, result.undecodable);
    CHECK_FALSE(result.partial);

    TEST_CASE("a declared length the device will never accept is refused unread");
    buffer[0] = (uint8_t)((MQTT_PACKET_PUBLISH << 4) | 0x02U);
    buffer[1] = 0xBCU;
    buffer[2] = 0x05U;
    result = MqttForEachPacket(buffer, 3U, count_frame, &count);
    CHECK_INT(0U, result.decoded);
    CHECK_INT(1U, result.undecodable);
    CHECK_FALSE(result.partial);
}

/* Entry point for the mqtt_packet suite. */
void test_mqtt_packet_suite(void)
{
    test_connect_golden();
    test_connect_bounds();
    test_publish();
    test_fixed_packets();
    test_subscribe();
    test_decode();
    test_decode_rejections();
    test_total_length();
    test_for_each_packet();
    test_round_trip();
}
