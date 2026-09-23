/*
 * Host tests for the downlink control path.
 *
 * Every branch here is one that decides whether a buzzer stays silent, so the
 * suite is written around the frozen contract rather than around the
 * implementation: the acknowledgement payloads are parsed as JSON, the subject
 * lines are compared against docs/device-protocol.md, and the alarm causes are
 * asserted alongside the threshold version so a command that silently drops an
 * alarm can never pass.
 */

#include "test_support.h"

#include "command_json.h"
#include "control_link.h"
#include "env_monitor.h"
#include "mqtt_packet.h"
#include "threshold_store.h"

#define CONTROL_JSON_MAX 512U
#define ACK_BUFFER_SIZE 384U
#define FRAME_BUFFER_SIZE (MQTT_MAX_PACKET_SIZE * 2U)

/* The backend issues a 60-second window. */
#define COMMAND_ISSUED_AT 1790246400000ULL
#define COMMAND_EXPIRES_AT (COMMAND_ISSUED_AT + 60000ULL)

/* ------------------------------------------------------------------ */
/* Flash fixture                                                       */
/* ------------------------------------------------------------------ */

/* RAM-backed Flash for the threshold store. It models the two outcomes the
 * control path has to tell apart — a write the part refused, and a record that
 * does not read back as it was written — and nothing else, because those are the
 * only two the store reports. */
typedef struct
{
    uint8_t memory[THRESHOLD_RECORD_SLOT_SIZE * THRESHOLD_RECORD_SLOTS];
    bool fail_erase;
    bool fail_write;
    /* Flip one byte on the way in, so the read-back disagrees. */
    bool corrupt_write;
    /* Counted so a test can prove that a refused command never reached the part
     * at all, rather than only that its result looked right. */
    uint32_t erase_calls;
    uint32_t write_calls;
} LinkFlash;

static bool link_flash_read(void *context, uint32_t offset, uint8_t *out, uint32_t length)
{
    LinkFlash *flash = (LinkFlash *)context;
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

static bool link_flash_erase(void *context, uint32_t offset)
{
    LinkFlash *flash = (LinkFlash *)context;
    uint32_t index;

    flash->erase_calls++;
    if (flash->fail_erase || offset + THRESHOLD_RECORD_SLOT_SIZE > sizeof(flash->memory))
    {
        return false;
    }
    for (index = 0U; index < THRESHOLD_RECORD_SLOT_SIZE; index++)
    {
        flash->memory[offset + index] = 0xFFU;
    }
    return true;
}

static bool link_flash_write(void *context, uint32_t offset, const uint8_t *data, uint32_t length)
{
    LinkFlash *flash = (LinkFlash *)context;
    uint32_t index;

    flash->write_calls++;
    if (flash->fail_write || offset + length > sizeof(flash->memory))
    {
        return false;
    }
    for (index = 0U; index < length; index++)
    {
        flash->memory[offset + index] = data[index];
    }
    if (flash->corrupt_write && length > 0U)
    {
        flash->memory[offset] = (uint8_t)(flash->memory[offset] ^ 0x5AU);
    }
    return true;
}

/* ------------------------------------------------------------------ */
/* Link fixture                                                        */
/* ------------------------------------------------------------------ */

typedef struct
{
    LinkFlash flash;
    ThresholdFlashPort port;
    EnvMonitor monitor;
    ThresholdStore store;
    ControlLink link;
    uint8_t ack[ACK_BUFFER_SIZE];
} Link;

static void link_flash_reset(LinkFlash *flash)
{
    uint32_t index;

    flash->fail_erase = false;
    flash->fail_write = false;
    flash->corrupt_write = false;
    flash->erase_calls = 0U;
    flash->write_calls = 0U;
    for (index = 0U; index < sizeof(flash->memory); index++)
    {
        flash->memory[index] = 0xFFU;
    }
}

/* Build a link with an erased part, the compile-time thresholds in force and no
 * session established. Each test turns the session on explicitly, so a test that
 * forgot to would fail loudly rather than pass by accident. */
static void link_setup(Link *fixture)
{
    link_flash_reset(&fixture->flash);
    fixture->port.read = link_flash_read;
    fixture->port.erase = link_flash_erase;
    fixture->port.write = link_flash_write;
    fixture->port.slot_offset[0] = 0U;
    fixture->port.slot_offset[1] = THRESHOLD_RECORD_SLOT_SIZE;
    fixture->port.context = &fixture->flash;

    EnvMonitorInit(&fixture->monitor);
    ThresholdStoreInit(&fixture->store, &fixture->port);
    ControlLinkInit(&fixture->link, "MCU001", "9f3ac21b", &fixture->monitor, &fixture->store);
}

/* ------------------------------------------------------------------ */
/* Capture                                                             */
/* ------------------------------------------------------------------ */

/* Everything the callback was handed for one frame. The ACK text is copied out
 * because the link reuses one buffer across frames: a test that read the buffer
 * afterwards would be reading whatever the last frame left there. */
typedef struct
{
    uint32_t frames;
    uint32_t pubacks;
    uint32_t ack_publishes;
    uint16_t last_puback_id;
    char last_ack_topic[32];
    char last_ack[ACK_BUFFER_SIZE];
    uint32_t last_ack_length;
    MqttPacketType last_packet_type;
    /* Make the next publish fail, to exercise the send-failure counter. */
    bool fail_send;
} Capture;

static void capture_reset(Capture *capture)
{
    capture->frames = 0U;
    capture->pubacks = 0U;
    capture->ack_publishes = 0U;
    capture->last_puback_id = 0U;
    capture->last_ack_topic[0] = '\0';
    capture->last_ack[0] = '\0';
    capture->last_ack_length = 0U;
    capture->last_packet_type = MQTT_PACKET_CONNECT;
    capture->fail_send = false;
}

static bool capture_visit(void *context, const MqttPacket *packet,
                          const ControlOutcome *outcome, const uint8_t *ack_payload)
{
    Capture *capture = (Capture *)context;

    capture->frames++;
    capture->last_packet_type = packet->type;
    if (outcome->send_puback)
    {
        capture->pubacks++;
        capture->last_puback_id = outcome->puback_packet_id;
    }
    if (outcome->publish_ack)
    {
        capture->ack_publishes++;
        capture->last_ack_length = outcome->ack_length;
        if (outcome->ack_topic != NULL)
        {
            (void)snprintf(capture->last_ack_topic, sizeof(capture->last_ack_topic), "%s",
                           outcome->ack_topic);
        }
        if (outcome->ack_length < (uint32_t)sizeof(capture->last_ack))
        {
            memcpy(capture->last_ack, ack_payload, outcome->ack_length);
            capture->last_ack[outcome->ack_length] = '\0';
        }
    }
    return !capture->fail_send;
}

/* ------------------------------------------------------------------ */
/* Payload builders                                                    */
/* ------------------------------------------------------------------ */

static void build_thresholds_json(char *out, uint32_t capacity, const char *device_id,
                                  const char *request_id, uint32_t version, const char *temperature,
                                  const char *humidity, const char *gas)
{
    (void)snprintf(out, capacity,
                   "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"%s\","
                   "\"requestId\":\"%s\",\"issuedAt\":%llu,\"expiresAt\":%llu,"
                   "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":%u,"
                   "\"temperatureHighC\":%s,\"humidityHighRh\":%s,\"gasHighPpm\":%s}}",
                   device_id, request_id, (unsigned long long)COMMAND_ISSUED_AT,
                   (unsigned long long)COMMAND_EXPIRES_AT, version, temperature, humidity, gas);
}

/* A valid threshold command with an ordinary identity. Thresholds are the only
 * command type left, so this is what "send a command" means in these tests. */
static void build_default_command(char *out, uint32_t capacity, const char *request_id,
                                  uint32_t version)
{
    build_thresholds_json(out, capacity, "MCU001", request_id, version, "30.0", "80.0", "80.0");
}

static void check_ack(const Capture *capture, const char *status, const char *error_code,
                      const char *request_id)
{
    CHECK_TRUE(test_json_is_well_formed(capture->last_ack, capture->last_ack_length));
    CHECK_TRUE(test_json_has_number(capture->last_ack, "schemaVersion", "1"));
    CHECK_TRUE(test_json_has_string(capture->last_ack, "messageType", "command_ack"));
    CHECK_TRUE(test_json_has_string(capture->last_ack, "deviceId", "MCU001"));
    CHECK_TRUE(test_json_has_string(capture->last_ack, "bootId", "9f3ac21b"));
    CHECK_MSG(test_json_has_string(capture->last_ack, "status", status),
              "expected status %.32s in %.160s", status, capture->last_ack);
    CHECK_MSG(test_json_has_string(capture->last_ack, "requestId", request_id),
              "expected requestId %.32s in %.160s", request_id, capture->last_ack);
    CHECK_TRUE(test_json_has_number(capture->last_ack, "timestamp", "null"));
    if (error_code == NULL)
    {
        CHECK_TRUE(test_json_has_number(capture->last_ack, "errorCode", "null"));
    }
    else
    {
        CHECK_TRUE(test_json_has_string(capture->last_ack, "errorCode", error_code));
    }
}

/* ------------------------------------------------------------------ */
/* A frame that is not a control command                               */
/* ------------------------------------------------------------------ */

static void test_non_control_frames(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t length;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);
    capture_reset(&capture);

    TEST_CASE("a CONNACK carries no command and produces nothing to publish");
    frame[0] = (uint8_t)(MQTT_PACKET_CONNACK << 4);
    frame[1] = 2U;
    frame[2] = 0U;
    frame[3] = 0U;
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, frame, 4U, 0U, 0U, fixture.ack, sizeof(fixture.ack), capture_visit,
                                          &capture));
    CHECK_INT(0U, capture.pubacks);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, fixture.link.counters.non_publish_frames);

    TEST_CASE("a SUBACK produces no command acknowledgement");
    frame[0] = (uint8_t)(MQTT_PACKET_SUBACK << 4);
    frame[1] = 3U;
    frame[2] = 0U;
    frame[3] = 2U;
    frame[4] = 1U;
    (void)ControlLinkHandleBuffer(&fixture.link, frame, 5U, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(2U, fixture.link.counters.non_publish_frames);

    TEST_CASE("a PINGRESP produces nothing");
    frame[0] = (uint8_t)(MQTT_PACKET_PINGRESP << 4);
    frame[1] = 0U;
    (void)ControlLinkHandleBuffer(&fixture.link, frame, 2U, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(3U, fixture.link.counters.non_publish_frames);
    CHECK_INT(0U, capture.ack_publishes);

    TEST_CASE("a PUBLISH on another topic is acknowledged but not executed");
    build_default_command(json, sizeof(json), "REQ-TOPIC", 2U);
    length = MqttEncodePublish(frame, sizeof(frame), "device/telemetry", 9U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    CHECK_TRUE(length > 0U);
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    /* The PUBACK is owed for the QoS 1 delivery itself, whatever the topic: the
     * device only subscribes to device/control, so this is a broker-side
     * misconfiguration and withholding the acknowledgement would cost a
     * redelivery loop rather than change anything. */
    CHECK_INT(1U, capture.pubacks);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(1U, fixture.link.counters.foreign_topic_frames);
}

/* ------------------------------------------------------------------ */
/* Topic matching                                                      */
/* ------------------------------------------------------------------ */

static void test_topic_matching(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t length;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-A", 2U);

    TEST_CASE("a longer topic that starts with the control topic is refused");
    length = MqttEncodePublish(frame, sizeof(frame), "device/controlX", 1U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(1U, fixture.link.counters.foreign_topic_frames);

    TEST_CASE("a shorter topic that prefixes the control topic is refused");
    length = MqttEncodePublish(frame, sizeof(frame), "device/contro", 1U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(2U, fixture.link.counters.foreign_topic_frames);

    TEST_CASE("a topic differing in its last byte is refused");
    length = MqttEncodePublish(frame, sizeof(frame), "device/controk", 1U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(3U, fixture.link.counters.foreign_topic_frames);

    TEST_CASE("the exact control topic is accepted without relying on a terminator");
    /* The topic field on the wire carries its own length and is followed
     * immediately by the payload, so a comparison that walked past the declared
     * length — or that assumed a NUL — would either read the payload as part of
     * the topic or truncate the topic. This frame is what proves it does
     * neither. */
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 3U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, capture.ack_publishes);
    CHECK_INT(2U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "applied", NULL, "REQ-A");
    CHECK_INT(3U, fixture.link.counters.foreign_topic_frames);
}

/* ------------------------------------------------------------------ */
/* QoS and the two acknowledgements                                    */
/* ------------------------------------------------------------------ */

static void test_qos_and_puback(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    uint8_t puback[8];
    char json[CONTROL_JSON_MAX];
    uint32_t length;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-Q1", 2U);

    TEST_CASE("a QoS 1 PUBLISH yields a PUBACK carrying its packet identifier");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 0x1234U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, capture.pubacks);
    CHECK_INT(0x1234, capture.last_puback_id);
    /* One frame, two different acknowledgements: the transport one and the
     * business one. Collapsing them would either lose the command result or
     * tell the broker a command was understood when it was only received. */
    CHECK_INT(1U, capture.ack_publishes);
    CHECK_INT(2U, EnvMonitorThresholdVersion(&fixture.monitor));

    TEST_CASE("the PUBACK frame is a well-formed MQTT PUBACK");
    CHECK_INT(4U, MqttEncodePuback(puback, sizeof(puback), (uint16_t)capture.last_puback_id));
    CHECK_INT(MQTT_PACKET_PUBACK, puback[0] >> 4);
    CHECK_INT(2U, puback[1]);
    CHECK_INT(0x12, puback[2]);
    CHECK_INT(0x34, puback[3]);

    TEST_CASE("a QoS 0 PUBLISH owes no PUBACK and is still carried out");
    /* device/control is a QoS 1 topic in the frozen contract, so this is a
     * defensive branch rather than a supported mode: acknowledgements are the
     * recovery mechanism, which is exactly why a QoS 0 command gets none. */
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-Q0", 3U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 4U, 0U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.pubacks);
    CHECK_INT(1U, capture.ack_publishes);
    CHECK_INT(1U, fixture.link.counters.non_qos1_frames);
    CHECK_INT(3U, EnvMonitorThresholdVersion(&fixture.monitor));

    TEST_CASE("a PUBLISH with no established session is acknowledged but not acted on");
    ControlLinkSetOnline(&fixture.link, false);
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-OFFLINE", 4U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 5U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, capture.pubacks);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(3U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(1U, fixture.link.counters.offline_frames);
}

/* ------------------------------------------------------------------ */
/* Duplicate and expiry                                                */
/* ------------------------------------------------------------------ */

static void test_duplicate_and_expiry(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t length;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);
    capture_reset(&capture);

    TEST_CASE("the first threshold command is applied");
    build_default_command(json, sizeof(json), "REQ-DUP", 2U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 1U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(2U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "applied", NULL, "REQ-DUP");

    TEST_CASE("a redelivered requestId is reported duplicate and does not re-execute");
    /* The redelivery carries a higher version. If the device executed it the
     * second time, a broker retry would undo the operator's own command. */
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-DUP", 3U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 2U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(2U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "duplicate", NULL, "REQ-DUP");
    CHECK_INT(1U, fixture.link.counters.applied);
    CHECK_INT(1U, fixture.link.counters.duplicate);

    TEST_CASE("a redelivered threshold command reports the version already in force");
    capture_reset(&capture);
    build_thresholds_json(json, sizeof(json), "MCU001", "REQ-DUP-TH", 4U, "30.0", "80.0", "80.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 3U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "applied", NULL, "REQ-DUP-TH");
    CHECK_TRUE(test_json_has_number(capture.last_ack, "thresholdVersion", "4"));

    capture_reset(&capture);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 4U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "duplicate", NULL, "REQ-DUP-TH");
    /* The contract requires applied *and* duplicate to carry the version in
     * force, so a redelivery does not read to the backend as a device with no
     * configuration. */
    CHECK_TRUE(test_json_has_number(capture.last_ack, "thresholdVersion", "4"));

    TEST_CASE("a command whose window has elapsed is rejected as expired");
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-EXPIRED", 5U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 5U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    /* The frame arrived at 1000 ms and is being executed 70 s later, which is
     * past the 60 s window the backend declared. */
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 1000U, 71000U, fixture.ack, sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "expired", NULL, "REQ-EXPIRED");
    CHECK_INT(1U, fixture.link.counters.expired);

    TEST_CASE("a command still inside its window is executed");
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-IN-WINDOW", 5U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 6U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    /* Exactly at the window boundary: the contract measures from receipt, so a
     * command arriving 60 s into a 60 s window is still inside it. */
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 1000U, 61000U, fixture.ack, sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(5U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "applied", NULL, "REQ-IN-WINDOW");

    TEST_CASE("an expired command is still remembered, so it cannot be replayed later");
    /* Deduplication is checked before the deadline, so the replay answers
     * duplicate rather than expired. Either way it does not execute; what makes
     * duplicate the right answer is that the backend already holds the outcome
     * and a second, different-looking status would read as a new event. */
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-EXPIRED", 6U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 7U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 1000U, 200000U, fixture.ack, sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(5U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "duplicate", NULL, "REQ-EXPIRED");
}

/* ------------------------------------------------------------------ */
/* Rejections                                                          */
/* ------------------------------------------------------------------ */

static void test_rejections(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t length;
    uint16_t packet_id = 1U;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);

    TEST_CASE("a command addressed to another device is rejected");
    capture_reset(&capture);
    build_thresholds_json(json, sizeof(json), "MCU002", "REQ-DEV", 2U, "30.0", "80.0", "80.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "rejected", "device_mismatch", "REQ-DEV");

    TEST_CASE("an unsupported schemaVersion is rejected before anything else");
    capture_reset(&capture);
    (void)snprintf(json, sizeof(json),
                   "{\"schemaVersion\":2,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
                   "\"requestId\":\"REQ-SCHEMA\",\"issuedAt\":%llu,\"expiresAt\":%llu,"
                   "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
                   "\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}",
                   (unsigned long long)COMMAND_ISSUED_AT,
                   (unsigned long long)COMMAND_EXPIRES_AT);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "rejected", "schema_unsupported", "REQ-SCHEMA");

    TEST_CASE("an unknown command type is rejected");
    capture_reset(&capture);
    (void)snprintf(json, sizeof(json),
                   "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
                   "\"requestId\":\"REQ-TYPE\",\"issuedAt\":%llu,\"expiresAt\":%llu,"
                   "\"type\":\"reboot\",\"payload\":{\"thresholdVersion\":2}}",
                   (unsigned long long)COMMAND_ISSUED_AT,
                   (unsigned long long)COMMAND_EXPIRES_AT);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "rejected", "bad_request_type", "REQ-TYPE");

    TEST_CASE("a threshold outside its range is rejected and nothing is stored");
    capture_reset(&capture);
    build_thresholds_json(json, sizeof(json), "MCU001", "REQ-RANGE", 5U, "81.0", "80.0", "80.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "rejected", "out_of_range", "REQ-RANGE");
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    /* Nothing reached the part: a rejected command must leave no trace in Flash,
     * not merely report the right status. */
    CHECK_INT(0U, fixture.flash.erase_calls);
    CHECK_INT(0U, fixture.flash.write_calls);

    TEST_CASE("a payload with no requestId yields no acknowledgement at all");
    /* An acknowledgement has to carry the requestId of the command it answers.
     * Inventing one, or answering with an empty string, would tell the backend a
     * command was understood when the device never identified which one it was. */
    capture_reset(&capture);
    (void)snprintf(json, sizeof(json),
                   "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
                   "\"issuedAt\":%llu,\"expiresAt\":%llu,\"type\":\"set_thresholds\","
                   "\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,"
                   "\"humidityHighRh\":80,\"gasHighPpm\":80}}",
                   (unsigned long long)COMMAND_ISSUED_AT,
                   (unsigned long long)COMMAND_EXPIRES_AT);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    /* The transport acknowledgement is still owed: the frame was delivered. */
    CHECK_INT(1U, capture.pubacks);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(1U, fixture.link.counters.unaddressable_frames);

    TEST_CASE("a payload that is not JSON at all yields no acknowledgement");
    capture_reset(&capture);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)"not json at all", 15U);
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, capture.pubacks);
    CHECK_INT(2U, fixture.link.counters.unaddressable_frames);

    TEST_CASE("an empty payload yields no acknowledgement");
    capture_reset(&capture);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)"", 0U);
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_INT(1U, capture.pubacks);
    CHECK_INT(3U, fixture.link.counters.unaddressable_frames);

    TEST_CASE("a retired set_mute command is rejected as an unknown type");
    /* The remote-mute capability was deleted. An old client that still sends
     * set_mute is told the type is not recognized rather than having its
     * payload quietly reinterpreted as thresholds. */
    capture_reset(&capture);
    (void)snprintf(json, sizeof(json),
                   "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
                   "\"requestId\":\"REQ-RETIRED\",\"issuedAt\":%llu,\"expiresAt\":%llu,"
                   "\"type\":\"set_mute\",\"payload\":{\"muted\":true}}",
                   (unsigned long long)COMMAND_ISSUED_AT,
                   (unsigned long long)COMMAND_EXPIRES_AT);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, packet_id++, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(1U, EnvMonitorThresholdVersion(&fixture.monitor));
    check_ack(&capture, "rejected", "bad_request_type", "REQ-RETIRED");
}

/* ------------------------------------------------------------------ */
/* Thresholds                                                          */
/* ------------------------------------------------------------------ */

static void test_threshold_commands(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t length;
    EnvThresholds in_force;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);

    TEST_CASE("a valid threshold set is stored before it is reported applied");
    capture_reset(&capture);
    build_thresholds_json(json, sizeof(json), "MCU001", "REQ-TH-1", 4U, "30.5", "80.0", "80.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 1U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "applied", NULL, "REQ-TH-1");
    CHECK_TRUE(test_json_has_number(capture.last_ack, "thresholdVersion", "4"));
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));
    EnvMonitorThresholds(&fixture.monitor, &in_force);
    /* The device resolves to whole units, so 30.5 is carried out as 31: rounding
     * up rather than truncating keeps every limit from drifting downward. */
    CHECK_INT(31U, in_force.temperature_high_c);
    CHECK_INT(80U, in_force.humidity_high_rh);
    CHECK_INT(80U, in_force.gas_high_ppm);

    TEST_CASE("the stored record survives a reload, so the version is not a claim");
    {
        ThresholdStore reloaded;
        EnvThresholds stored;
        uint32_t version = 0U;

        ThresholdStoreInit(&reloaded, &fixture.port);
        CHECK_TRUE(ThresholdStoreLoad(&reloaded, &stored, &version));
        CHECK_INT(4U, version);
        CHECK_INT(31U, stored.temperature_high_c);
    }

    TEST_CASE("a version that does not move forward is rejected as stale");
    capture_reset(&capture);
    build_thresholds_json(json, sizeof(json), "MCU001", "REQ-TH-STALE", 4U, "40.0", "90.0", "90.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 2U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "rejected", "stale_version", "REQ-TH-STALE");
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));
    EnvMonitorThresholds(&fixture.monitor, &in_force);
    CHECK_INT(31U, in_force.temperature_high_c);

    TEST_CASE("a Flash write the part refuses is reported as failed, not applied");
    capture_reset(&capture);
    fixture.flash.fail_erase = true;
    build_thresholds_json(json, sizeof(json), "MCU001", "REQ-TH-WRITE", 5U, "40.0", "90.0", "90.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 3U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "failed", "flash_write_failed", "REQ-TH-WRITE");
    /* The previous configuration stays in force, and the version must not move:
     * a version that did would tell the backend a set the device never stored. */
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));
    EnvMonitorThresholds(&fixture.monitor, &in_force);
    CHECK_INT(31U, in_force.temperature_high_c);

    TEST_CASE("a record that does not read back is reported as a verify failure");
    capture_reset(&capture);
    fixture.flash.fail_erase = false;
    fixture.flash.corrupt_write = true;
    build_thresholds_json(json, sizeof(json), "MCU001", "REQ-TH-VERIFY", 6U, "40.0", "90.0", "90.0");
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 4U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "failed", "flash_verify_failed", "REQ-TH-VERIFY");
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));

    TEST_CASE("a threshold command is refused, not half-applied, when a field is missing");
    capture_reset(&capture);
    fixture.flash.corrupt_write = false;
    (void)snprintf(json, sizeof(json),
                   "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
                   "\"requestId\":\"REQ-TH-PARTIAL\",\"issuedAt\":%llu,\"expiresAt\":%llu,"
                   "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":7,"
                   "\"temperatureHighC\":40.0,\"humidityHighRh\":90.0}}",
                   (unsigned long long)COMMAND_ISSUED_AT,
                   (unsigned long long)COMMAND_EXPIRES_AT);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 5U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "rejected", "out_of_range", "REQ-TH-PARTIAL");
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));
}

/* ------------------------------------------------------------------ */
/* Buffer handling                                                     */
/* ------------------------------------------------------------------ */

static void test_buffer_handling(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    uint32_t offset;
    char json[CONTROL_JSON_MAX];
    uint32_t length;
    uint32_t first;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);
    capture_reset(&capture);

    TEST_CASE("two frames in one segment are both handled");
    /* The driver hands up a whole TCP segment, and a segment can hold more than
     * one MQTT packet. Reading only the head would drop the command behind it
     * without leaving a trace. */
    build_default_command(json, sizeof(json), "REQ-MULTI-1", 2U);
    first = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 1U, 1U,
                              (const uint8_t *)json, (uint32_t)strlen(json));
    offset = first;
    build_default_command(json, sizeof(json), "REQ-MULTI-2", 3U);
    length = MqttEncodePublish(&frame[offset], (uint32_t)sizeof(frame) - offset,
                               CONTROL_TOPIC_COMMAND, 2U, 1U, (const uint8_t *)json,
                               (uint32_t)strlen(json));
    CHECK_TRUE(length > 0U);
    CHECK_INT(2U, ControlLinkHandleBuffer(&fixture.link, frame, first + length, 0U, 0U, fixture.ack, sizeof(fixture.ack), capture_visit,
                                          &capture));
    CHECK_INT(2U, capture.pubacks);
    CHECK_INT(2U, capture.ack_publishes);
    /* Two distinct requestIds, so both are new commands and both execute; the
     * last state is the one the second frame asked for. */
    CHECK_INT(2U, fixture.link.counters.applied);
    CHECK_INT(0U, fixture.link.counters.duplicate);
    CHECK_INT(3U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(2U, fixture.link.counters.frames_decoded);

    TEST_CASE("a frame the codec refuses does not hide the command behind it");
    capture_reset(&capture);
    /* A SUBSCRIBE is a packet the device should never receive from a broker, so
     * the codec refuses it. Its framing is intact, so the scan must continue. */
    frame[0] = (uint8_t)(MQTT_PACKET_SUBSCRIBE << 4);
    frame[1] = 5U;
    frame[2] = 0x00U;
    frame[3] = 0x01U;
    frame[4] = 0x00U;
    frame[5] = 0x00U;
    frame[6] = 0x00U;
    offset = 7U;
    build_default_command(json, sizeof(json), "REQ-AFTER-BAD", 4U);
    length = MqttEncodePublish(&frame[offset], (uint32_t)sizeof(frame) - offset,
                               CONTROL_TOPIC_COMMAND, 3U, 1U, (const uint8_t *)json,
                               (uint32_t)strlen(json));
    CHECK_TRUE(length > 0U);
    CHECK_INT(1U, ControlLinkHandleBuffer(&fixture.link, frame, offset + length, 0U, 0U, fixture.ack, sizeof(fixture.ack), capture_visit,
                                          &capture));
    CHECK_INT(1U, capture.ack_publishes);
    CHECK_INT(4U, EnvMonitorThresholdVersion(&fixture.monitor));
    CHECK_INT(1U, fixture.link.counters.frames_undecodable);

    TEST_CASE("a segment that ends inside a frame is counted as a partial frame");
    /* The driver keeps no partial-frame state, so a frame the broker split
     * across two segments cannot be reassembled. Counting it is what keeps the
     * loss visible instead of looking like a broker that sent nothing. */
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-PARTIAL", 5U);
    first = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 4U, 1U,
                              (const uint8_t *)json, (uint32_t)strlen(json));
    CHECK_TRUE(first > 8U);
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, frame, first - 8U, 0U, 0U, fixture.ack, sizeof(fixture.ack), capture_visit,
                                          &capture));
    CHECK_INT(0U, capture.frames);
    CHECK_INT(1U, fixture.link.counters.partial_frames);

    TEST_CASE("a truncated frame is not mistaken for a whole one");
    capture_reset(&capture);
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, frame, 1U, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), capture_visit, &capture));
    CHECK_INT(0U, capture.frames);

    TEST_CASE("a frame declaring more than the device accepts is refused unread");
    /* The declared length is enough to decide: 700 bytes exceeds
     * MQTT_MAX_PACKET_SIZE, so the frame is refused without waiting for bytes
     * the device would never buffer. */
    capture_reset(&capture);
    frame[0] = (uint8_t)((MQTT_PACKET_PUBLISH << 4) | 0x02U);
    frame[1] = 0xBCU;
    frame[2] = 0x05U;
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, frame, 3U, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), capture_visit, &capture));
    CHECK_INT(0U, capture.frames);
    /* One for the refused SUBSCRIBE earlier in this suite, one for this frame. */
    CHECK_INT(2U, fixture.link.counters.frames_undecodable);

    TEST_CASE("an empty buffer is refused rather than scanned");
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, NULL, 0U, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), capture_visit, &capture));

    TEST_CASE("a payload at the largest size the device accepts is handled");
    capture_reset(&capture);
    {
        /* A 32-character requestId is the longest the contract allows, which is
         * what makes the acknowledgement payload the widest it can be. */
        char request_id[COMMAND_REQUEST_ID_MAX + 1U];

        for (length = 0U; length < COMMAND_REQUEST_ID_MAX; length++)
        {
            request_id[length] = 'R';
        }
        request_id[COMMAND_REQUEST_ID_MAX] = '\0';
        build_default_command(json, sizeof(json), request_id, 6U);
        CHECK_TRUE((uint32_t)strlen(json) < sizeof(json));
        first = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 5U, 1U,
                                  (const uint8_t *)json, (uint32_t)strlen(json));
        CHECK_TRUE(first > 0U);
        CHECK_INT(1U, ControlLinkHandleBuffer(&fixture.link, frame, first, 0U, 0U, fixture.ack, sizeof(fixture.ack), capture_visit,
                                              &capture));
        CHECK_INT(1U, capture.ack_publishes);
        check_ack(&capture, "applied", NULL, request_id);
        CHECK_TRUE(capture.last_ack_length < ACK_BUFFER_SIZE);
    }

    TEST_CASE("an acknowledgement that does not fit causes no publish");
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-SMALL", 7U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 6U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    /* The command still takes effect — it is applied before the payload is
     * rendered — so the failure is reported as a handler failure and left to the
     * backend's timeout rather than silently claiming success. */
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack, 16U, capture_visit, &capture));
    CHECK_INT(0U, capture.ack_publishes);
    CHECK_TRUE(fixture.link.counters.send_failures >= 1U);

    TEST_CASE("a publish the caller could not complete is counted");
    capture_reset(&capture);
    capture.fail_send = true;
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 7U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_TRUE(fixture.link.counters.send_failures >= 2U);

    TEST_CASE("null and empty arguments are refused rather than dereferenced");
    CHECK_INT(0U, ControlLinkHandleBuffer(NULL, frame, 16U, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), capture_visit, &capture));
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, NULL, 16U, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), capture_visit, &capture));
    CHECK_INT(0U, ControlLinkHandleBuffer(&fixture.link, frame, 0U, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), capture_visit, &capture));
    CHECK_INT(0U, ControlLinkHandlePacket(NULL, NULL, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), NULL));
    ControlLinkSetOnline(NULL, true);
    ControlLinkInit(NULL, "MCU001", "9f3ac21b", &fixture.monitor, &fixture.store);
}

/* ------------------------------------------------------------------ */
/* Single-packet entry point                                           */
/* ------------------------------------------------------------------ */

static void test_single_packet_entry(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    MqttPacket packet;
    const char *reason = NULL;
    ControlOutcome outcome;
    char json[CONTROL_JSON_MAX];
    uint32_t length;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);
    capture_reset(&capture);

    TEST_CASE("the single-packet entry point reports the same acknowledgements");
    build_default_command(json, sizeof(json), "REQ-SINGLE", 2U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 0x0102U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    CHECK_TRUE(MqttDecode(frame, length, &packet, &reason));
    CHECK_INT(1U, ControlLinkHandlePacket(&fixture.link, &packet, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), &outcome));
    CHECK_TRUE(outcome.send_puback);
    CHECK_INT(0x0102, outcome.puback_packet_id);
    CHECK_TRUE(outcome.publish_ack);
    CHECK_INT(0, strcmp(outcome.ack_topic, CONTROL_TOPIC_ACK));
    CHECK_INT(COMMAND_RESULT_APPLIED, outcome.result);
    CHECK_INT(2U, EnvMonitorThresholdVersion(&fixture.monitor));

    TEST_CASE("a packet that is not a PUBLISH reports nothing");
    frame[0] = (uint8_t)(MQTT_PACKET_PINGRESP << 4);
    frame[1] = 0U;
    CHECK_TRUE(MqttDecode(frame, 2U, &packet, &reason));
    CHECK_INT(0U, ControlLinkHandlePacket(&fixture.link, &packet, 0U, 0U, fixture.ack,
                                          sizeof(fixture.ack), &outcome));
    CHECK_FALSE(outcome.send_puback);
    CHECK_FALSE(outcome.publish_ack);
}

/* The acknowledgement counter shares one boot-scoped number space with the
 * telemetry sequence, so a reader can order the two streams against each other.
 * Two independent counters would both emit 1, 2, 3 and make the field useless. */
static void test_shared_sequence(void)
{
    Link fixture;
    Capture capture;
    uint8_t frame[FRAME_BUFFER_SIZE];
    char json[CONTROL_JSON_MAX];
    uint32_t length;

    link_setup(&fixture);
    ControlLinkSetOnline(&fixture.link, true);

    TEST_CASE("the counter the caller seeds is the one the acknowledgement reports");
    capture_reset(&capture);
    ControlLinkSetSequence(&fixture.link, 500U);
    build_default_command(json, sizeof(json), "REQ-SEQ-1", 2U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 1U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    check_ack(&capture, "applied", NULL, "REQ-SEQ-1");
    CHECK_TRUE(test_json_has_number(capture.last_ack, "sequence", "500"));

    TEST_CASE("the counter advances so the next message cannot reuse it");
    CHECK_INT(501U, ControlLinkSequence(&fixture.link));
    capture_reset(&capture);
    build_default_command(json, sizeof(json), "REQ-SEQ-2", 3U);
    length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 2U, 1U,
                               (const uint8_t *)json, (uint32_t)strlen(json));
    (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_TRUE(test_json_has_number(capture.last_ack, "sequence", "501"));
    CHECK_INT(502U, ControlLinkSequence(&fixture.link));

    TEST_CASE("a frame that owes no acknowledgement does not consume a number");
    capture_reset(&capture);
    frame[0] = (uint8_t)(MQTT_PACKET_PINGRESP << 4);
    frame[1] = 0U;
    (void)ControlLinkHandleBuffer(&fixture.link, frame, 2U, 0U, 0U, fixture.ack,
                                  sizeof(fixture.ack), capture_visit, &capture);
    CHECK_INT(502U, ControlLinkSequence(&fixture.link));

    TEST_CASE("a null link reports and accepts the counter without crashing");
    ControlLinkSetSequence(NULL, 7U);
    CHECK_INT(0U, ControlLinkSequence(NULL));
}

/* ------------------------------------------------------------------ */
/* Buzzer policy                                                       */
/* ------------------------------------------------------------------ */

static void make_gas_alarm(Link *fixture)
{
    EnvMonitorPushClimate(&fixture->monitor, 25U, 50U, 1U, 0U);
    EnvMonitorPushGas(&fixture->monitor, 4000U);
    EnvMonitorSetGasEstimate(&fixture->monitor, 900U);
}

static void test_buzzer_policy(void)
{
    Link fixture;
    EnvEvaluation evaluation;

    link_setup(&fixture);

    TEST_CASE("a non-gas alarm keeps its cause but does not sound");
    /* Temperature, humidity and sensor faults stay visible on the LED, the OLED
     * and in telemetry. The audible warning is reserved for the gas causes,
     * because those are the ones that need someone in the room now. */
    EnvMonitorPushGas(&fixture.monitor, 100U);
    EnvMonitorSetGasEstimate(&fixture.monitor, 1U);
    EnvMonitorPushClimate(&fixture.monitor, 60U, 95U, 1U, 0U);
    evaluation = EnvMonitorEvaluate(&fixture.monitor, 0U);
    CHECK_TRUE(evaluation.local_alarm);
    CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_TEMPERATURE_HIGH));
    CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_HUMIDITY_HIGH));
    CHECK_FALSE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_GAS_HIGH));
    CHECK_FALSE(EnvMonitorBuzzerDrive(&evaluation, 0U));
    CHECK_FALSE(EnvMonitorBuzzerDrive(&evaluation, 1U));

    TEST_CASE("a gas alarm sounds 200 ms on and 800 ms off at the 100 ms tick");
    link_setup(&fixture);
    make_gas_alarm(&fixture);
    evaluation = EnvMonitorEvaluate(&fixture.monitor, 0U);
    CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_GAS_HIGH));
    CHECK_TRUE(evaluation.buzzer_on);
    /* Two ticks on out of every ten: 200 ms of sound in each one-second period. */
    {
        uint32_t tick;
        uint32_t on_ticks = 0U;

        for (tick = 0U; tick < GAS_BUZZER_PERIOD_TICKS; tick++)
        {
            if (EnvMonitorBuzzerDrive(&evaluation, tick))
            {
                on_ticks++;
            }
        }
        CHECK_INT(GAS_BUZZER_ON_TICKS, on_ticks);
        CHECK_INT(2U, on_ticks);
        CHECK_INT(10U, GAS_BUZZER_PERIOD_TICKS);
        CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 0U));
        CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 1U));
        CHECK_FALSE(EnvMonitorBuzzerDrive(&evaluation, 2U));
        CHECK_FALSE(EnvMonitorBuzzerDrive(&evaluation, 9U));
        CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 10U));
    }

    TEST_CASE("a gas alarm stays audible across every evaluation while the gas is unsafe");
    evaluation = EnvMonitorEvaluate(&fixture.monitor, 100U);
    CHECK_TRUE(evaluation.local_alarm);
    CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_GAS_HIGH));
    CHECK_TRUE(evaluation.buzzer_on);
    {
        uint32_t tick;
        uint32_t on_ticks = 0U;

        for (tick = 0U; tick < 30U; tick++)
        {
            if (EnvMonitorBuzzerDrive(&evaluation, tick))
            {
                on_ticks++;
            }
        }
        /* The gas is still above the limit on this evaluation, so the buzzer
         * keeps the 2-in-10 cadence across three full periods. */
        CHECK_INT(6U, on_ticks);
    }

    TEST_CASE("a new cause does not silence a gas alarm already sounding");
    /* The gas alarm is still active, then a humidity alarm appears as well.
     * After mute removal nothing can silence a gas alarm that is already
     * sounding, so the buzzer keeps going on the strength of the gas cause. */
    EnvMonitorPushClimate(&fixture.monitor, 60U, 95U, 1U, 1000U);
    evaluation = EnvMonitorEvaluate(&fixture.monitor, 1000U);
    CHECK_TRUE(evaluation.new_cause);
    CHECK_TRUE(evaluation.buzzer_on);
    CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 0U));

    TEST_CASE("a threshold update takes effect on the very next evaluation");
    {
        uint8_t frame[FRAME_BUFFER_SIZE];
        char json[CONTROL_JSON_MAX];
        Capture capture;
        uint32_t length;

        /* Gas reading is 100 ppm against a 20 ppm limit: the alarm is on. */
        EnvMonitorPushGas(&fixture.monitor, 1000U);
        EnvMonitorSetGasEstimate(&fixture.monitor, 100U);
        evaluation = EnvMonitorEvaluate(&fixture.monitor, 0U);
        CHECK_TRUE(evaluation.buzzer_on);

        /* Backend pushes new thresholds (gasHighPpm: 50, version 10). The new
         * limit is still below the 100 ppm reading, so the gas alarm has to
         * stay on — and after mute removal nothing can keep the buzzer quiet
         * across the update. */
        ControlLinkSetOnline(&fixture.link, true);
        capture_reset(&capture);
        build_thresholds_json(json, sizeof(json), "MCU001", "REQ-GAS-UPDATE", 10U, "30.0", "80.0", "50.0");
        length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 20U, 1U,
                                   (const uint8_t *)json, (uint32_t)strlen(json));
        (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                      sizeof(fixture.ack), capture_visit, &capture);
        check_ack(&capture, "applied", NULL, "REQ-GAS-UPDATE");
        CHECK_INT(10U, EnvMonitorThresholdVersion(&fixture.monitor));

        /* Immediately evaluate at 100 ppm: buzzer_on and BuzzerDrive must be active */
        evaluation = EnvMonitorEvaluate(&fixture.monitor, 200U);
        CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_GAS_HIGH));
        CHECK_TRUE(evaluation.buzzer_on);
        CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 0U));
    }

    TEST_CASE("a threshold command that lowers the gas limit alarms on the next evaluation");
    {
        uint8_t frame[FRAME_BUFFER_SIZE];
        char json[CONTROL_JSON_MAX];
        Capture capture;
        uint32_t length;
        EnvThresholds in_force;

        link_setup(&fixture);
        EnvMonitorPushGas(&fixture.monitor, 1000U);
        EnvMonitorSetGasEstimate(&fixture.monitor, 15U);
        EnvMonitorPushClimate(&fixture.monitor, 25U, 50U, 1U, 0U);
        evaluation = EnvMonitorEvaluate(&fixture.monitor, 0U);
        CHECK_FALSE(evaluation.local_alarm);
        CHECK_FALSE(evaluation.buzzer_on);

        ControlLinkSetOnline(&fixture.link, true);
        capture_reset(&capture);
        build_thresholds_json(json, sizeof(json), "MCU001", "REQ-GAS-LOWER", 2U, "30.0", "80.0", "10.0");
        length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 20U, 1U,
                                   (const uint8_t *)json, (uint32_t)strlen(json));
        (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                      sizeof(fixture.ack), capture_visit, &capture);
        check_ack(&capture, "applied", NULL, "REQ-GAS-LOWER");
        EnvMonitorThresholds(&fixture.monitor, &in_force);
        CHECK_INT(10U, in_force.gas_high_ppm);

        evaluation = EnvMonitorEvaluate(&fixture.monitor, 100U);
        CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_GAS_HIGH));
        CHECK_TRUE(evaluation.new_cause);
        CHECK_TRUE(evaluation.buzzer_on);
        CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 1U));
    }

    TEST_CASE("an applied dynamic limit still sounds after the link goes offline");
    {
        uint8_t frame[FRAME_BUFFER_SIZE];
        char json[CONTROL_JSON_MAX];
        Capture capture;
        uint32_t length;

        link_setup(&fixture);
        EnvMonitorPushGas(&fixture.monitor, 1000U);
        EnvMonitorSetGasEstimate(&fixture.monitor, 45U);
        EnvMonitorPushClimate(&fixture.monitor, 25U, 50U, 1U, 0U);

        ControlLinkSetOnline(&fixture.link, true);
        capture_reset(&capture);
        build_thresholds_json(json, sizeof(json), "MCU001", "REQ-GAS-OFFLINE", 3U, "30.0", "80.0", "40.0");
        length = MqttEncodePublish(frame, sizeof(frame), CONTROL_TOPIC_COMMAND, 20U, 1U,
                                   (const uint8_t *)json, (uint32_t)strlen(json));
        (void)ControlLinkHandleBuffer(&fixture.link, frame, length, 0U, 0U, fixture.ack,
                                      sizeof(fixture.ack), capture_visit, &capture);
        check_ack(&capture, "applied", NULL, "REQ-GAS-OFFLINE");

        ControlLinkSetOnline(&fixture.link, false);
        evaluation = EnvMonitorEvaluate(&fixture.monitor, 100U);
        CHECK_TRUE(EnvAlarmHas(evaluation.alarm_causes, ENV_ALARM_GAS_HIGH));
        CHECK_TRUE(evaluation.buzzer_on);
        CHECK_TRUE(EnvMonitorBuzzerDrive(&evaluation, 0U));
        CHECK_FALSE(EnvMonitorBuzzerDrive(&evaluation, 3U));
    }

    TEST_CASE("a null evaluation is refused rather than dereferenced");
    CHECK_FALSE(EnvMonitorBuzzerDrive(NULL, 0U));
}

/* Entry point for the control_link suite. */
void test_control_link_suite(void)
{
    test_non_control_frames();
    test_topic_matching();
    test_qos_and_puback();
    test_duplicate_and_expiry();
    test_rejections();
    test_threshold_commands();
    test_buffer_handling();
    test_single_packet_entry();
    test_shared_sequence();
    test_buzzer_policy();
}
