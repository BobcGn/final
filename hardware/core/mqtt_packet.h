#ifndef __MQTT_PACKET_H
#define __MQTT_PACKET_H

/*
 * MQTT 3.1.1 packet codec for the device side.
 *
 * The framing is implemented on the MCU rather than delegated to the ESP8266's
 * AT command set. The reason is testability and portability: the AT firmware
 * MQTT command set differs between module versions and cannot be verified
 * without the module in hand, whereas transparent TCP pass-through is what the
 * existing driver already uses and works on every version. The plan in
 * docs/implementation-plan.md §4.5 lists this as the fallback; it is the only
 * one of the two that can be verified before the hardware is on the bench.
 *
 * The subset matches docs/device-protocol.md: CONNECT, SUBSCRIBE, PUBLISH at
 * QoS 0 and 1, PUBACK, PINGREQ and DISCONNECT in, CONNACK, SUBACK, PUBLISH and
 * PINGRESP out. Retained messages, wills, QoS 2 and MQTT 5 are not produced and
 * are rejected on receipt rather than half-handled.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* Largest frame the device will build or accept.
 *
 * The telemetry payload is the largest thing it sends. Measured with a maximum
 * length device and boot identifier, all six alarm causes and the widest
 * readings, the payload is about 500 bytes, and the fixed header and topic add
 * about 20. The bound leaves room for that plus a control command and an
 * acknowledgement, and it keeps both the send and the receive buffer a fixed
 * size on a part with 20 KiB of RAM: two 640-byte buffers are under a tenth of
 * it. */
#define MQTT_MAX_PACKET_SIZE 640U

/* Control packet types. */
typedef enum
{
    MQTT_PACKET_CONNECT = 1,
    MQTT_PACKET_CONNACK = 2,
    MQTT_PACKET_PUBLISH = 3,
    MQTT_PACKET_PUBACK = 4,
    MQTT_PACKET_SUBSCRIBE = 8,
    MQTT_PACKET_SUBACK = 9,
    MQTT_PACKET_PINGREQ = 12,
    MQTT_PACKET_PINGRESP = 13,
    MQTT_PACKET_DISCONNECT = 14
} MqttPacketType;

/* Connection acknowledgement return codes. */
#define MQTT_CONNACK_ACCEPTED 0U

/* Reject reasons for a decode failure. They are stable strings so the caller can
 * log a reason without matching prose, mirroring the backend's vocabulary. */
extern const char *const MQTT_REJECT_MALFORMED;
extern const char *const MQTT_REJECT_UNSUPPORTED;
extern const char *const MQTT_REJECT_TOO_LARGE;

/* A decoded packet. `topic` and `payload` point into the caller's receive
 * buffer and are valid until it is refilled: the codec copies nothing, because
 * every extra buffer costs RAM the part does not have to spare. */
typedef struct
{
    MqttPacketType type;
    uint8_t flags;
    uint16_t packet_id;
    /* CONNACK */
    uint8_t return_code;
    /* SUBACK */
    uint8_t granted_qos;
    /* PUBLISH: the topic is a byte range rather than a C string, because the
     * frame carries a length and the topic is not NUL-terminated on the wire. */
    const uint8_t *topic;
    uint16_t topic_length;
    const uint8_t *payload;
    uint32_t payload_length;
    /* First byte after this packet, for a caller that has more than one frame
     * buffered. */
    const uint8_t *next;
} MqttPacket;

/* Encode a CONNECT. Username and password may be NULL for an anonymous
 * connection. Returns the number of bytes written, or 0 when the packet does
 * not fit or an argument is invalid. */
uint32_t MqttEncodeConnect(uint8_t *buffer, uint32_t capacity, const char *client_id,
                           uint16_t keep_alive_seconds, const char *username, const char *password);

/* Encode a SUBSCRIBE for one topic filter. */
uint32_t MqttEncodeSubscribe(uint8_t *buffer, uint32_t capacity, uint16_t packet_id,
                             const char *topic_filter, uint8_t qos);

/* Encode a PUBLISH. A QoS above zero requires a non-zero packet identifier. */
uint32_t MqttEncodePublish(uint8_t *buffer, uint32_t capacity, const char *topic,
                           uint16_t packet_id, uint8_t qos, const uint8_t *payload,
                           uint32_t payload_length);

/* Encode a PUBACK. */
uint32_t MqttEncodePuback(uint8_t *buffer, uint32_t capacity, uint16_t packet_id);

/* Encode a PINGREQ. */
uint32_t MqttEncodePingReq(uint8_t *buffer, uint32_t capacity);

/* Encode a DISCONNECT. */
uint32_t MqttEncodeDisconnect(uint8_t *buffer, uint32_t capacity);

/* Return the total length of the packet at the start of `data`, or 0 when the
 * buffer does not yet hold the whole packet. A caller feeding bytes from a
 * socket uses this to decide whether it has a complete frame before decoding. */
uint32_t MqttPacketTotalLength(const uint8_t *data, uint32_t length);

/* Decode the packet at the start of `data`.
 *
 * Returns true and fills `packet` on success. On failure it returns false and
 * sets `*reason` to one of the MQTT_REJECT_* strings, so the caller can log and
 * count the failure without inspecting the bytes. */
bool MqttDecode(const uint8_t *data, uint32_t length, MqttPacket *packet, const char **reason);

/* What one pass over a receive buffer found.
 *
 * The receive path hands up one TCP segment at a time and the driver keeps a
 * single buffer, so a segment can hold several frames and its tail can hold a
 * fragment of the next one. Separating the counts is what lets a caller tell
 * "the broker sent more than we read" from "the broker sent something we could
 * not parse": both are losses, and neither is visible if they are folded into a
 * single total. */
typedef struct
{
    /* Frames the codec accepted. */
    uint32_t decoded;
    /* Frames whose framing was complete but whose contents the codec refused. */
    uint32_t undecodable;
    /* True when the buffer ends inside a frame. The driver cannot reassemble
     * one, so the fragment is counted rather than retained. */
    bool partial;
} MqttScanResult;

/* Called once per frame the codec accepted. `packet` points into `data` and is
 * only valid for the duration of the call. */
typedef void (*MqttVisitor)(void *context, const MqttPacket *packet);

/* Decode every frame in `data` and hand each to `visit` in order.
 *
 * A frame the codec refuses is skipped rather than ending the scan, because its
 * framing is intact: whatever follows it is still a frame the caller should see.
 * The scan only stops early when the tail is not a complete frame. */
MqttScanResult MqttForEachPacket(const uint8_t *data, uint32_t length, MqttVisitor visit,
                                 void *context);

#endif /* __MQTT_PACKET_H */
