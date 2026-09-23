#ifndef __CONTROL_LINK_H
#define __CONTROL_LINK_H

/*
 * Downlink control path: from a received MQTT PUBLISH to the acknowledgement the
 * device owes the backend.
 *
 * This is the module that closes the loop the firmware previously left open.
 * Before it existed the main loop decoded CONNACK, SUBACK and PINGRESP and
 * discarded everything else, so `device/control` was subscribed but no command
 * was ever parsed, carried out or acknowledged, and the backend's command
 * records stayed at `accepted` forever.
 *
 * It performs no I/O. The caller owns the radio, the clock and the buffers, and
 * receives one callback per decoded frame carrying whatever that frame owes the
 * wire. That is what lets every safety-relevant branch — topic matching, schema
 * and range validation, deduplication, expiry and the mute and threshold
 * decisions — be exercised on a host without a board.
 *
 * Two acknowledgements are produced and they are not interchangeable:
 *
 *   - the MQTT PUBACK, which answers the transport-level delivery of a QoS 1
 *     PUBLISH and says nothing about whether the command was understood;
 *   - the command ACK published to `device/command-ack`, which carries the
 *     validate-and-execute result in the frozen status vocabulary.
 *
 * Sending the first does not imply the second, and a malformed payload produces
 * the first without the second, because an acknowledgement must carry the
 * requestId of the command it answers and an unparsable payload has none.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "command_json.h"
#include "env_monitor.h"
#include "mqtt_packet.h"
#include "threshold_store.h"

/* The one control topic this firmware acts on, and the one it answers on. */
#define CONTROL_TOPIC_COMMAND "device/control"
#define CONTROL_TOPIC_ACK "device/command-ack"

/* Everything the caller must put on the wire for one received frame. */
typedef struct
{
    /* True when an MQTT PUBACK is owed. Set for every QoS 1 PUBLISH, whatever
     * its topic or payload: the PUBACK discharges the transport obligation and
     * withholding it would make the broker redeliver a frame the device has
     * already consumed. */
    bool send_puback;
    uint16_t puback_packet_id;
    /* True when a command ACK must be published to `ack_topic`. */
    bool publish_ack;
    const char *ack_topic;
    uint32_t ack_length;
    /* The result behind the ACK payload, for counters and tests. Only meaningful
     * when `publish_ack` is set. */
    CommandResult result;
} ControlOutcome;

/* Called once per frame the codec decoded, in the order the frames appeared.
 *
 * `packet` points into the receive buffer and `ack_payload` into the caller's ACK
 * buffer; both are only valid until the callback returns. For a frame that is not
 * a control command, `outcome` is all-false and `ack_payload` must not be read.
 *
 * Publishing has to happen inside the call: the module reuses one ACK buffer and
 * one receive buffer, so an acknowledgement that is still being held when the
 * next frame is handled would be overwritten before it was sent. That single
 * rule is what keeps a command from being acknowledged after the frame it came
 * from has already been recycled.
 *
 * Return true when the caller put everything the outcome asked for on the wire.
 * The value only feeds the counters; a transport failure is not a command
 * failure. */
typedef bool (*ControlLinkVisitor)(void *context, const MqttPacket *packet,
                                   const ControlOutcome *outcome,
                                   const uint8_t *ack_payload);

/* Counters for every path, including the ones that produce no acknowledgement.
 * They exist so that a dropped or refused frame is visible rather than silent. */
typedef struct
{
    /* Frames the codec decoded from the receive buffers passed in. */
    uint32_t frames_decoded;
    /* Frames whose framing was complete but whose contents the codec refused. */
    uint32_t frames_undecodable;
    /* Buffers that ended inside a frame. The driver keeps no partial-frame
     * state, so such a fragment is lost and only a QoS 1 redelivery recovers
     * it. */
    uint32_t partial_frames;
    /* Decoded frames that were not a PUBLISH, so carried no command. */
    uint32_t non_publish_frames;
    /* PUBLISH frames on a topic other than device/control. */
    uint32_t foreign_topic_frames;
    /* Control PUBLISH frames whose payload yielded no requestId, so no
     * acknowledgement could be addressed to them. */
    uint32_t unaddressable_frames;
    /* PUBLISH frames that were not QoS 1 and so owe no PUBACK. */
    uint32_t non_qos1_frames;
    /* Control PUBLISH frames that arrived with no established session. */
    uint32_t offline_frames;
    uint32_t applied;
    uint32_t duplicate;
    uint32_t expired;
    uint32_t rejected;
    uint32_t failed;
    /* Frames whose acknowledgement the caller could not put on the wire. */
    uint32_t send_failures;
} ControlLinkCounters;

/* Control path state. It holds no I/O handle, so the firmware can keep it as a
 * single static object. */
typedef struct
{
    const char *device_id;
    const char *boot_id;
    EnvMonitor *monitor;
    ThresholdStore *store;
    CommandDedup dedup;
    /* Whether an MQTT session is established. Commands are only acted on while it
     * is, because a PUBLISH that arrives outside one was not subscribed for at
     * the time it was issued. */
    bool online;
    /* The boot-scoped counter the acknowledgements report.
     *
     * docs/device-protocol.md §5.1 defines one monotonic counter per boot, not
     * one per topic, so this is the same number space as the telemetry sequence:
     * the caller seeds it from the telemetry counter before each receive scan and
     * reads it back afterwards. Two independent counters would both emit 1, 2, 3
     * and make the field useless for ordering the two streams against each
     * other. */
    uint32_t sequence;
    ControlLinkCounters counters;
} ControlLink;

/* Bind the path to the device identity and to the state a command acts on.
 *
 * `monitor` and `store` are borrowed and must outlive the link. Both are
 * required: the mute command writes the monitor's mute flag, and a threshold
 * command is only reported as applied after the store has written and verified
 * the record. */
void ControlLinkInit(ControlLink *link, const char *device_id, const char *boot_id,
                     EnvMonitor *monitor, ThresholdStore *store);

/* Tell the path whether an MQTT session is established.
 *
 * Until this is set, a PUBLISH is acknowledged at the transport level but never
 * carried out. The reason is not defensiveness: the device has just connected
 * and a frame delivered at that moment is at best a redelivery of a command
 * issued while it was away, and the contract forbids executing one of those. */
void ControlLinkSetOnline(ControlLink *link, bool online);

/* Seed the acknowledgement counter, and read it back.
 *
 * The caller owns the boot-scoped counter and shares it with telemetry, so it
 * passes the current value in before each receive scan and reads the advanced
 * value out afterwards. */
void ControlLinkSetSequence(ControlLink *link, uint32_t sequence);
uint32_t ControlLinkSequence(const ControlLink *link);

/* Handle one decoded packet.
 *
 * Fills `outcome`, rendering any command ACK into `ack_buffer`. The ACK is left
 * in the buffer for the caller; `outcome.ack_length` is zero when there is
 * nothing to publish.
 *
 * `received_uptime_ms` is when the frame arrived and `now_ms` is the moment of
 * execution; both are monotonic milliseconds since reset, and the difference is
 * what the command's validity window is measured against.
 *
 * Returns the number of frames that produced a publishable acknowledgement: one
 * for a control command the device could identify, zero otherwise. */
uint32_t ControlLinkHandlePacket(ControlLink *link, const MqttPacket *packet,
                                 uint32_t received_uptime_ms, uint32_t now_ms,
                                 uint8_t *ack_buffer, uint32_t ack_capacity,
                                 ControlOutcome *outcome);

/* Handle every MQTT frame packed into one receive buffer.
 *
 * A TCP segment can carry more than one MQTT packet and the driver hands the
 * whole `+IPD` payload up in one piece, so the buffer is scanned frame by frame
 * rather than read only at its head. A frame the codec refuses is skipped, and a
 * trailing fragment is counted as loss. Both are recorded in the link's
 * counters, and both are recovered only by the broker's QoS 1 redelivery —
 * which is the reason device/control is a QoS 1 topic in the frozen contract.
 *
 * `ack_buffer` is reused for each frame and is only valid inside the callback.
 *
 * Returns the number of frames that produced a publishable acknowledgement. */
uint32_t ControlLinkHandleBuffer(ControlLink *link, const uint8_t *data, uint32_t length,
                                 uint32_t received_uptime_ms, uint32_t now_ms,
                                 uint8_t *ack_buffer, uint32_t ack_capacity,
                                 ControlLinkVisitor visit, void *context);

#endif /* __CONTROL_LINK_H */
