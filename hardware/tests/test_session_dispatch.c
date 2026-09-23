/*
 * Host tests for the MQTT downlink session dispatch state machine.
 *
 * Exercises the session transition from SUBACK to online, verifying that
 * commands buffered in the same receive segment as the SUBACK are executed
 * as active online sessions, that SUBACK failures or packetId mismatches
 * prevent commands from running, and that TCP drops immediately silence
 * the online session without waiting an extra cycle.
 */

#include "test_support.h"

#include <string.h>

#include "command_json.h"
#include "control_link.h"
#include "env_monitor.h"
#include "mqtt_packet.h"
#include "session_dispatch.h"
#include "threshold_store.h"

#define CONTROL_JSON_MAX 512U
#define ACK_BUFFER_SIZE 384U
#define TX_BUFFER_SIZE MQTT_MAX_PACKET_SIZE
#define FRAME_BUFFER_SIZE (MQTT_MAX_PACKET_SIZE * 2U)

/* ------------------------------------------------------------------ */
/* Mock IO Sender                                                      */
/* ------------------------------------------------------------------ */

typedef struct
{
    uint32_t send_calls;
    uint8_t last_sent[MQTT_MAX_PACKET_SIZE];
    uint32_t last_sent_length;
    bool fail_send;
    uint32_t puback_count;
    uint16_t last_puback_id;
    uint32_t sub_count;
    uint16_t last_sub_id;
    uint32_t pub_count;
    char last_pub_topic[32];
    char last_pub_payload[CONTROL_JSON_MAX];
} MockIo;

static void mock_io_reset(MockIo *mock)
{
    mock->send_calls = 0U;
    mock->last_sent_length = 0U;
    mock->fail_send = false;
    mock->puback_count = 0U;
    mock->last_puback_id = 0U;
    mock->sub_count = 0U;
    mock->last_sub_id = 0U;
    mock->pub_count = 0U;
    mock->last_pub_topic[0] = '\0';
    mock->last_pub_payload[0] = '\0';
}

static uint8_t mock_io_send(void *context, const uint8_t *data, uint32_t length)
{
    MockIo *mock = (MockIo *)context;
    MqttPacket packet;
    const char *reason = NULL;

    if (mock == NULL)
    {
        return 0U;
    }
    mock->send_calls++;
    if (mock->fail_send)
    {
        return 0U;
    }
    if (length > sizeof(mock->last_sent))
    {
        return 0U;
    }
    memcpy(mock->last_sent, data, length);
    mock->last_sent_length = length;

    if ((data[0] >> 4) == MQTT_PACKET_SUBSCRIBE)
    {
        mock->sub_count++;
        if (length >= 4U)
        {
            mock->last_sub_id = (uint16_t)(((uint16_t)data[2] << 8) | (uint16_t)data[3]);
        }
    }
    else if (MqttDecode(data, length, &packet, &reason))
    {
        if (packet.type == MQTT_PACKET_PUBACK)
        {
            mock->puback_count++;
            mock->last_puback_id = packet.packet_id;
        }
        else if (packet.type == MQTT_PACKET_PUBLISH)
        {
            mock->pub_count++;
            if (packet.topic != NULL && packet.topic_length < sizeof(mock->last_pub_topic))
            {
                memcpy(mock->last_pub_topic, packet.topic, packet.topic_length);
                mock->last_pub_topic[packet.topic_length] = '\0';
            }
            if (packet.payload != NULL && packet.payload_length < sizeof(mock->last_pub_payload))
            {
                memcpy(mock->last_pub_payload, packet.payload, packet.payload_length);
                mock->last_pub_payload[packet.payload_length] = '\0';
            }
        }
    }
    return 1U;
}

/* ------------------------------------------------------------------ */
/* Test Fixture                                                        */
/* ------------------------------------------------------------------ */

/* RAM-backed Flash for the threshold store. A threshold command is only
 * reported applied after the store has written and verified a record, so a
 * fixture without a working port would report failed and never exercise the
 * session transition this suite is about. */
typedef struct
{
    uint8_t memory[THRESHOLD_RECORD_SLOT_SIZE * THRESHOLD_RECORD_SLOTS];
} SessionFlash;

static bool session_flash_read(void *context, uint32_t offset, uint8_t *out, uint32_t length)
{
    SessionFlash *flash = (SessionFlash *)context;
    uint32_t index;

    if (offset + length > sizeof(flash->memory))
    {
        return false;
    }
    for (index = 0U; index < length; index++)
    {
        out[index] = flash->memory[offset + index];
    }
    return true;
}

static bool session_flash_erase(void *context, uint32_t offset)
{
    SessionFlash *flash = (SessionFlash *)context;
    uint32_t index;

    if (offset + THRESHOLD_RECORD_SLOT_SIZE > sizeof(flash->memory))
    {
        return false;
    }
    for (index = 0U; index < THRESHOLD_RECORD_SLOT_SIZE; index++)
    {
        flash->memory[offset + index] = 0xFFU;
    }
    return true;
}

static bool session_flash_write(void *context, uint32_t offset, const uint8_t *data, uint32_t length)
{
    SessionFlash *flash = (SessionFlash *)context;
    uint32_t index;

    if (offset + length > sizeof(flash->memory))
    {
        return false;
    }
    for (index = 0U; index < length; index++)
    {
        flash->memory[offset + index] = data[index];
    }
    return true;
}

typedef struct
{
    SessionFlash flash;
    ThresholdFlashPort port;
    EnvMonitor monitor;
    ThresholdStore store;
    ControlLink link;
    SessionDispatcher dispatcher;
    MockIo mock_io;
    uint8_t tx[TX_BUFFER_SIZE];
    uint8_t ack[ACK_BUFFER_SIZE];
    uint16_t packet_id;
} SessionFixture;

static void fixture_setup(SessionFixture *fix)
{
    uint32_t index;

    for (index = 0U; index < sizeof(fix->flash.memory); index++)
    {
        fix->flash.memory[index] = 0xFFU;
    }
    fix->port.read = session_flash_read;
    fix->port.erase = session_flash_erase;
    fix->port.write = session_flash_write;
    fix->port.slot_offset[0] = 0U;
    fix->port.slot_offset[1] = THRESHOLD_RECORD_SLOT_SIZE;
    fix->port.context = &fix->flash;

    EnvMonitorInit(&fix->monitor);
    ThresholdStoreInit(&fix->store, &fix->port);
    ControlLinkInit(&fix->link, "MCU001", "9f3ac21b", &fix->monitor, &fix->store);
    mock_io_reset(&fix->mock_io);
    fix->packet_id = 1U;
    SessionDispatchInit(&fix->dispatcher, fix->tx, sizeof(fix->tx),
                        &fix->packet_id, &fix->link, mock_io_send, &fix->mock_io);
}

/* A control command whose only job is to be an identifiable side effect: a
 * threshold update that must move the monitor's threshold version if, and
 * only if, the session state machine actually let it execute. */
static void build_thresholds_json(char *out, uint32_t capacity, const char *request_id,
                                  uint32_t version)
{
    (void)snprintf(out, capacity,
                   "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
                   "\"requestId\":\"%s\",\"issuedAt\":1790246400000,"
                   "\"expiresAt\":1790246460000,\"type\":\"set_thresholds\","
                   "\"payload\":{\"thresholdVersion\":%u,"
                   "\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":20}}",
                   request_id, version);
}

/* ------------------------------------------------------------------ */
/* Suite Cases                                                         */
/* ------------------------------------------------------------------ */

static void test_suback_and_publish_same_buffer(void)
{
    SessionFixture fix;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t suback_len;
    uint32_t pub_len;

    TEST_CASE("a receive buffer with SUBACK and control PUBLISH goes online and executes immediately");
    fixture_setup(&fix);

    /* Simulate link waiting for SUBACK after subscribing with packet id 0x002A (42) */
    fix.dispatcher.state = MQTT_LINK_WAIT_SUBACK;
    fix.dispatcher.pending_sub_packet_id = 42U;
    ControlLinkSetOnline(&fix.link, false);

    /* Frame 1: SUBACK for packet 42, granted QoS 1 */
    frame[0] = (uint8_t)(MQTT_PACKET_SUBACK << 4);
    frame[1] = 3U;
    frame[2] = 0x00U;
    frame[3] = 42U;
    frame[4] = 1U; /* Granted QoS 1 */
    suback_len = 5U;

    /* Frame 2: QoS 1 PUBLISH to device/control with a threshold command */
    build_thresholds_json(json, sizeof(json), "REQ-SUBACK-CO-BUF", 2U);
    pub_len = MqttEncodePublish(&frame[suback_len], sizeof(frame) - suback_len,
                                CONTROL_TOPIC_COMMAND, 101U, 1U,
                                (const uint8_t *)json, (uint32_t)strlen(json));
    CHECK_TRUE(pub_len > 0U);

    /* Process the composite buffer */
    CHECK_INT(1U, ControlLinkHandleBuffer(&fix.link, frame, suback_len + pub_len, 0U, 0U,
                                          fix.ack, sizeof(fix.ack),
                                          SessionDispatchFrame, &fix.dispatcher));

    /* 1. State must now be ONLINE */
    CHECK_INT(MQTT_LINK_ONLINE, fix.dispatcher.state);
    CHECK_TRUE(fix.link.online);
    CHECK_INT(0U, fix.dispatcher.pending_sub_packet_id);

    /* 2. Command must be executed as applied, NOT offline */
    CHECK_INT(1U, fix.link.counters.applied);
    CHECK_INT(0U, fix.link.counters.offline_frames);
    CHECK_INT(2U, EnvMonitorThresholdVersion(&fix.monitor));

    /* 3. Wire outputs: PUBACK sent for QoS 1 PUBLISH, command ACK published */
    CHECK_INT(1U, fix.mock_io.puback_count);
    CHECK_INT(101U, fix.mock_io.last_puback_id);
    CHECK_INT(1U, fix.mock_io.pub_count);
    CHECK_INT(0, strcmp(fix.mock_io.last_pub_topic, CONTROL_TOPIC_ACK));
    CHECK_TRUE(strstr(fix.mock_io.last_pub_payload, "\"status\":\"applied\"") != NULL);
    CHECK_TRUE(strstr(fix.mock_io.last_pub_payload, "\"requestId\":\"REQ-SUBACK-CO-BUF\"") != NULL);
}

static void test_suback_failure_and_mismatch(void)
{
    SessionFixture fix;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t suback_len;
    uint32_t pub_len;

    TEST_CASE("a SUBACK reporting failure (0x80) rejects the session and leaves trailing command offline");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_WAIT_SUBACK;
    fix.dispatcher.pending_sub_packet_id = 42U;
    ControlLinkSetOnline(&fix.link, false);

    /* Frame 1: SUBACK with 0x80 failure */
    frame[0] = (uint8_t)(MQTT_PACKET_SUBACK << 4);
    frame[1] = 3U;
    frame[2] = 0x00U;
    frame[3] = 42U;
    frame[4] = 0x80U; /* Failure */
    suback_len = 5U;

    /* Frame 2: control PUBLISH */
    build_thresholds_json(json, sizeof(json), "REQ-FAIL-SUBACK", 2U);
    pub_len = MqttEncodePublish(&frame[suback_len], sizeof(frame) - suback_len,
                                CONTROL_TOPIC_COMMAND, 102U, 1U,
                                (const uint8_t *)json, (uint32_t)strlen(json));

    CHECK_INT(0U, ControlLinkHandleBuffer(&fix.link, frame, suback_len + pub_len, 0U, 0U,
                                          fix.ack, sizeof(fix.ack),
                                          SessionDispatchFrame, &fix.dispatcher));

    /* State remains WAIT_SUBACK, link remains offline */
    CHECK_INT(MQTT_LINK_WAIT_SUBACK, fix.dispatcher.state);
    CHECK_FALSE(fix.link.online);
    CHECK_INT(0U, fix.link.counters.applied);
    CHECK_INT(1U, fix.link.counters.offline_frames);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fix.monitor));

    /* PUBACK is sent to fulfill transport delivery, but NO command ACK is published */
    CHECK_INT(1U, fix.mock_io.puback_count);
    CHECK_INT(102U, fix.mock_io.last_puback_id);
    CHECK_INT(0U, fix.mock_io.pub_count);

    TEST_CASE("a SUBACK with mismatched packet ID is rejected and command is not executed");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_WAIT_SUBACK;
    fix.dispatcher.pending_sub_packet_id = 42U;
    ControlLinkSetOnline(&fix.link, false);

    /* Frame 1: SUBACK with packetId 99 (expected 42) */
    frame[0] = (uint8_t)(MQTT_PACKET_SUBACK << 4);
    frame[1] = 3U;
    frame[2] = 0x00U;
    frame[3] = 99U; /* Mismatch */
    frame[4] = 1U;
    suback_len = 5U;

    build_thresholds_json(json, sizeof(json), "REQ-MISMATCH-ID", 2U);
    pub_len = MqttEncodePublish(&frame[suback_len], sizeof(frame) - suback_len,
                                CONTROL_TOPIC_COMMAND, 103U, 1U,
                                (const uint8_t *)json, (uint32_t)strlen(json));

    CHECK_INT(0U, ControlLinkHandleBuffer(&fix.link, frame, suback_len + pub_len, 0U, 0U,
                                          fix.ack, sizeof(fix.ack),
                                          SessionDispatchFrame, &fix.dispatcher));

    CHECK_INT(MQTT_LINK_WAIT_SUBACK, fix.dispatcher.state);
    CHECK_FALSE(fix.link.online);
    CHECK_INT(0U, fix.link.counters.applied);
    CHECK_INT(1U, fix.link.counters.offline_frames);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fix.monitor));

    TEST_CASE("a SUBACK arriving in unexpected state does not alter session");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_WAIT_CONNACK;
    fix.dispatcher.pending_sub_packet_id = 0U;
    ControlLinkSetOnline(&fix.link, false);

    CHECK_INT(0U, ControlLinkHandleBuffer(&fix.link, frame, suback_len, 0U, 0U,
                                          fix.ack, sizeof(fix.ack),
                                          SessionDispatchFrame, &fix.dispatcher));
    CHECK_INT(MQTT_LINK_WAIT_CONNACK, fix.dispatcher.state);
    CHECK_FALSE(fix.link.online);
}

static void test_tcp_disconnect_behavior(void)
{
    SessionFixture fix;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t pub_len;

    TEST_CASE("TCP disconnect immediately drops session state and silences command execution");
    fixture_setup(&fix);

    /* Link was online */
    fix.dispatcher.state = MQTT_LINK_ONLINE;
    ControlLinkSetOnline(&fix.link, true);

    /* TCP disconnect occurs */
    SessionDispatchTcpDisconnected(&fix.dispatcher);

    CHECK_INT(MQTT_LINK_TCP, fix.dispatcher.state);
    CHECK_FALSE(fix.link.online);
    CHECK_INT(0U, fix.dispatcher.pending_sub_packet_id);

    /* A buffered PUBLISH arriving after disconnect must not execute */
    build_thresholds_json(json, sizeof(json), "REQ-AFTER-DISC", 2U);
    pub_len = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 201U, 1U,
                                (const uint8_t *)json, (uint32_t)strlen(json));
    CHECK_TRUE(pub_len > 0U);

    CHECK_INT(0U, ControlLinkHandleBuffer(&fix.link, frame, pub_len, 0U, 0U,
                                          fix.ack, sizeof(fix.ack),
                                          SessionDispatchFrame, &fix.dispatcher));

    CHECK_INT(1U, fix.link.counters.offline_frames);
    CHECK_INT(0U, fix.link.counters.applied);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fix.monitor));
    CHECK_INT(0U, fix.mock_io.pub_count);

    TEST_CASE("SessionDispatchSyncOnline keeps ControlLink updated");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_ONLINE;
    ControlLinkSetOnline(&fix.link, false);
    SessionDispatchSyncOnline(&fix.dispatcher);
    CHECK_TRUE(fix.link.online);

    fix.dispatcher.state = MQTT_LINK_TCP;
    SessionDispatchSyncOnline(&fix.dispatcher);
    CHECK_FALSE(fix.link.online);
}

static void test_connack_and_pingresp(void)
{
    SessionFixture fix;
    uint8_t frame[16];
    MqttPacket packet;
    ControlOutcome outcome;
    const char *reason = NULL;

    TEST_CASE("CONNACK accepted triggers SUBSCRIBE and moves to WAIT_SUBACK");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_WAIT_CONNACK;
    fix.dispatcher.now_ms = 500U;

    /* CONNACK Accepted: return_code = 0 */
    frame[0] = (uint8_t)(MQTT_PACKET_CONNACK << 4);
    frame[1] = 2U;
    frame[2] = 0U;
    frame[3] = 0U; /* Accepted */

    CHECK_TRUE(MqttDecode(frame, 4U, &packet, &reason));
    memset(&outcome, 0, sizeof(outcome));

    CHECK_TRUE(SessionDispatchFrame(&fix.dispatcher, &packet, &outcome, NULL));
    CHECK_INT(MQTT_LINK_WAIT_SUBACK, fix.dispatcher.state);
    CHECK_INT(1U, fix.dispatcher.pending_sub_packet_id);
    CHECK_INT(500U, fix.dispatcher.last_activity_ms);
    CHECK_INT(1U, fix.mock_io.sub_count);
    CHECK_INT(1U, fix.mock_io.last_sub_id);

    TEST_CASE("CONNACK refused does not subscribe or advance state");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_WAIT_CONNACK;

    frame[3] = 5U; /* Not authorized */
    CHECK_TRUE(MqttDecode(frame, 4U, &packet, &reason));
    CHECK_TRUE(SessionDispatchFrame(&fix.dispatcher, &packet, &outcome, NULL));
    CHECK_INT(MQTT_LINK_WAIT_CONNACK, fix.dispatcher.state);
    CHECK_INT(0U, fix.mock_io.sub_count);

    TEST_CASE("PINGRESP updates last_activity_ms");
    fixture_setup(&fix);
    fix.dispatcher.state = MQTT_LINK_ONLINE;
    fix.dispatcher.now_ms = 12000U;

    frame[0] = (uint8_t)(MQTT_PACKET_PINGRESP << 4);
    frame[1] = 0U;
    CHECK_TRUE(MqttDecode(frame, 2U, &packet, &reason));
    CHECK_TRUE(SessionDispatchFrame(&fix.dispatcher, &packet, &outcome, NULL));
    CHECK_INT(12000U, fix.dispatcher.last_activity_ms);
}

static void test_error_and_send_failures(void)
{
    SessionFixture fix;
    MqttPacket packet;
    ControlOutcome outcome;

    TEST_CASE("null and invalid arguments are safely rejected");
    CHECK_INT(1U, SessionTakePacketId(NULL));
    SessionDispatchInit(NULL, NULL, 0U, NULL, NULL, NULL, NULL);
    SessionDispatchTcpDisconnected(NULL);
    SessionDispatchSyncOnline(NULL);
    CHECK_FALSE(SessionDispatchFrame(NULL, NULL, NULL, NULL));

    fixture_setup(&fix);
    memset(&packet, 0, sizeof(packet));
    memset(&outcome, 0, sizeof(outcome));
    CHECK_FALSE(SessionDispatchFrame(&fix.dispatcher, NULL, &outcome, NULL));
    CHECK_FALSE(SessionDispatchFrame(&fix.dispatcher, &packet, NULL, NULL));

    TEST_CASE("send failure in outcome sends returns false");
    fixture_setup(&fix);
    fix.mock_io.fail_send = true;
    outcome.send_puback = true;
    outcome.puback_packet_id = 99U;
    packet.type = MQTT_PACKET_PUBLISH;
    CHECK_FALSE(SessionDispatchFrame(&fix.dispatcher, &packet, &outcome, NULL));

    outcome.send_puback = false;
    outcome.publish_ack = true;
    outcome.ack_topic = CONTROL_TOPIC_ACK;
    outcome.ack_length = 5U;
    CHECK_FALSE(SessionDispatchFrame(&fix.dispatcher, &packet, &outcome, (const uint8_t *)"hello"));
}

static void test_broker_silence_requires_reconnect(void)
{
    SessionFixture fix;

    TEST_CASE("offline link never triggers a forced TCP reconnect");
    fixture_setup(&fix);
    CHECK_FALSE(SessionDispatchNeedsReconnect(&fix.dispatcher, 60000U));
    CHECK_FALSE(SessionDispatchNeedsReconnect(NULL, 60000U));

    TEST_CASE("CONNECT and SUBSCRIBE handshakes time out at ten seconds");
    fix.dispatcher.state = MQTT_LINK_WAIT_CONNACK;
    fix.dispatcher.last_rx_ms = 1000U;
    CHECK_FALSE(SessionDispatchNeedsReconnect(&fix.dispatcher, 10999U));
    CHECK_TRUE(SessionDispatchNeedsReconnect(&fix.dispatcher, 11000U));
    fix.dispatcher.state = MQTT_LINK_WAIT_SUBACK;
    CHECK_TRUE(SessionDispatchNeedsReconnect(&fix.dispatcher, 11000U));

    TEST_CASE("online link requires a broker frame within forty-five seconds");
    fix.dispatcher.state = MQTT_LINK_ONLINE;
    fix.dispatcher.last_rx_ms = 1000U;
    CHECK_FALSE(SessionDispatchNeedsReconnect(&fix.dispatcher, 45999U));
    CHECK_TRUE(SessionDispatchNeedsReconnect(&fix.dispatcher, 46000U));
    fix.dispatcher.last_rx_ms = 45500U; /* PUBACK or PINGRESP received */
    CHECK_FALSE(SessionDispatchNeedsReconnect(&fix.dispatcher, 46000U));

    TEST_CASE("timeout comparison remains valid across millisecond counter wrap");
    fix.dispatcher.last_rx_ms = UINT32_MAX - 1000U;
    CHECK_FALSE(SessionDispatchNeedsReconnect(&fix.dispatcher, 43998U));
    CHECK_TRUE(SessionDispatchNeedsReconnect(&fix.dispatcher, 43999U));
}

/* Entry point for the session_dispatch suite. */
void test_session_dispatch_suite(void)
{
    test_suback_and_publish_same_buffer();
    test_suback_failure_and_mismatch();
    test_tcp_disconnect_behavior();
    test_connack_and_pingresp();
    test_error_and_send_failures();
    test_broker_silence_requires_reconnect();
}
