#ifndef __SESSION_DISPATCH_H
#define __SESSION_DISPATCH_H

/*
 * Session dispatch state machine and frame visitor for the MQTT downlink.
 *
 * Coordinates MQTT connection and subscription handshakes (CONNACK, SUBACK,
 * PINGRESP) and puts obligations owed to the wire (PUBACK, command ACK) on the
 * wire.
 *
 * Extracted as a pure-logic module so that the transition from SUBACK to an
 * active online session and the handling of multiple frames in a single receive
 * buffer can be fully verified in host tests.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "control_link.h"
#include "mqtt_packet.h"

/* Link state for the MQTT connection and subscription handshake. */
typedef enum
{
    MQTT_LINK_TCP = 0U,
    MQTT_LINK_WAIT_CONNACK,
    MQTT_LINK_WAIT_SUBACK,
    MQTT_LINK_ONLINE
} MqttLinkState;

/* Output byte transmission callback: returns non-zero on success, 0 on failure. */
typedef uint8_t (*SessionSendBytesFn)(void *io_context, const uint8_t *data, uint32_t length);

/* Session dispatch state machine. */
typedef struct
{
    uint8_t *tx;
    uint32_t capacity;
    uint16_t *next_packet_id;
    uint16_t pending_sub_packet_id;
    MqttLinkState state;
    uint32_t last_activity_ms;
    uint32_t now_ms;
    ControlLink *link;
    SessionSendBytesFn send_fn;
    void *io_context;
} SessionDispatcher;

/* Take the next non-zero 16-bit packet identifier. */
uint16_t SessionTakePacketId(uint16_t *next);

/* Initialize the session dispatcher. */
void SessionDispatchInit(SessionDispatcher *dispatcher, uint8_t *tx, uint32_t capacity,
                         uint16_t *next_packet_id, ControlLink *link,
                         SessionSendBytesFn send_fn, void *io_context);

/* Notify dispatcher that TCP connection dropped. Immediately sets state to
 * MQTT_LINK_TCP, clears pending packet IDs, and marks ControlLink offline. */
void SessionDispatchTcpDisconnected(SessionDispatcher *dispatcher);

/* Synchronize online status from current dispatcher state to ControlLink. */
void SessionDispatchSyncOnline(SessionDispatcher *dispatcher);

/* Visitor callback to pass into ControlLinkHandleBuffer. */
bool SessionDispatchFrame(void *context, const MqttPacket *packet,
                          const ControlOutcome *outcome, const uint8_t *ack_payload);

#endif /* __SESSION_DISPATCH_H */
