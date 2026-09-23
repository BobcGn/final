#include "control_link.h"

/* The control topic as a byte array rather than a C string.
 *
 * A PUBLISH topic is a length-prefixed field on the wire and is not terminated
 * by the receiver, so the comparison must be made over an explicit length. Using
 * a literal with an explicit size keeps the expected length a compile-time
 * constant and removes any temptation to reach for strcmp on a buffer whose tail
 * belongs to the next field. */
static const uint8_t CONTROL_TOPIC_BYTES[] = CONTROL_TOPIC_COMMAND;
#define CONTROL_TOPIC_LENGTH (sizeof(CONTROL_TOPIC_BYTES) - 1U)

/* Extract the QoS from a PUBLISH's fixed-header flags. */
static uint8_t PublishQos(const MqttPacket *packet)
{
    return (uint8_t)((packet->flags >> 1U) & 0x03U);
}

/* Compare the packet's topic against device/control by exact length and bytes. */
static bool TopicIsControl(const MqttPacket *packet)
{
    uint32_t index;

    if (packet->topic == NULL)
    {
        return false;
    }
    if ((uint32_t)packet->topic_length != (uint32_t)CONTROL_TOPIC_LENGTH)
    {
        return false;
    }
    for (index = 0U; index < (uint32_t)CONTROL_TOPIC_LENGTH; index++)
    {
        if (packet->topic[index] != CONTROL_TOPIC_BYTES[index])
        {
            return false;
        }
    }
    return true;
}

void ControlLinkInit(ControlLink *link, const char *device_id, const char *boot_id,
                     EnvMonitor *monitor, ThresholdStore *store)
{
    if (link == NULL)
    {
        return;
    }
    link->device_id = device_id;
    link->boot_id = boot_id;
    link->monitor = monitor;
    link->store = store;
    link->online = false;
    link->sequence = 0U;
    CommandDedupInit(&link->dedup);
    link->counters.frames_decoded = 0U;
    link->counters.frames_undecodable = 0U;
    link->counters.partial_frames = 0U;
    link->counters.non_publish_frames = 0U;
    link->counters.foreign_topic_frames = 0U;
    link->counters.unaddressable_frames = 0U;
    link->counters.non_qos1_frames = 0U;
    link->counters.offline_frames = 0U;
    link->counters.applied = 0U;
    link->counters.duplicate = 0U;
    link->counters.expired = 0U;
    link->counters.rejected = 0U;
    link->counters.failed = 0U;
    link->counters.send_failures = 0U;
}

void ControlLinkSetOnline(ControlLink *link, bool online)
{
    if (link == NULL)
    {
        return;
    }
    link->online = online;
}

void ControlLinkSetSequence(ControlLink *link, uint32_t sequence)
{
    if (link == NULL)
    {
        return;
    }
    link->sequence = sequence;
}

uint32_t ControlLinkSequence(const ControlLink *link)
{
    return (link == NULL) ? 0U : link->sequence;
}

/* Add one to the counter that matches a result. The counters are the only place
 * the device records what it did with a command, so a path that produced no
 * acknowledgement is counted explicitly rather than inferred from silence. */static void CountResult(ControlLinkCounters *counters, CommandResult result)
{
    switch (result)
    {
    case COMMAND_RESULT_APPLIED:
        counters->applied++;
        break;
    case COMMAND_RESULT_DUPLICATE:
        counters->duplicate++;
        break;
    case COMMAND_RESULT_EXPIRED:
        counters->expired++;
        break;
    case COMMAND_RESULT_FAILED_FLASH_WRITE:
    case COMMAND_RESULT_FAILED_FLASH_VERIFY:
        counters->failed++;
        break;
    case COMMAND_RESULT_MALFORMED:
        break;
    default:
        counters->rejected++;
        break;
    }
}

/* Carry out a validated command and report the result the acknowledgement must
 * state.
 *
 * The parser has already rejected an unknown schema, a foreign deviceId, an
 * out-of-range value and a stale threshold version, so what remains is execution
 * and the two failures execution can have: a Flash write that did not complete
 * and a read-back that disagreed with it.
 *
 * A threshold command is only brought into force after the store reports the
 * record written and verified, so a power loss between the two leaves the device
 * enforcing the previous limits rather than a version number that describes
 * something it never stored. */
static CommandResult ExecuteCommand(ControlLink *link, const ControlCommand *command)
{
    if (link->store == NULL)
    {
        /* Without a store the device cannot honour the contract's "survives a
         * reset" promise for a new threshold set. Reporting failure is the honest
         * outcome; reporting applied would claim durability the device does not
         * have. */
        return COMMAND_RESULT_FAILED_FLASH_WRITE;
    }

    switch (ThresholdStoreSave(link->store, &command->thresholds, command->threshold_version))
    {
    case THRESHOLD_STORE_OK:
        break;
    case THRESHOLD_STORE_ERASE_FAILED:
    case THRESHOLD_STORE_WRITE_FAILED:
    case THRESHOLD_STORE_READ_FAILED:
        return COMMAND_RESULT_FAILED_FLASH_WRITE;
    case THRESHOLD_STORE_VERIFY_FAILED:
        return COMMAND_RESULT_FAILED_FLASH_VERIFY;
    case THRESHOLD_STORE_STALE_VERSION:
        /* The store's ordering check disagreed with the parser's, which can only
         * happen if a newer set was stored in between. Refusing is the safe side
         * of that race: the newer configuration stays in force. */
        return COMMAND_RESULT_REJECTED_STALE_VERSION;
    default:
        return COMMAND_RESULT_FAILED_FLASH_WRITE;
    }

    if (!EnvMonitorSetThresholds(link->monitor, &command->thresholds, command->threshold_version))
    {
        /* The stored record is valid but the monitor refused it, which means the
         * two disagreed about the accepted range. The command is not in force, so
         * it must not be reported as applied. */
        return COMMAND_RESULT_REJECTED_RANGE;
    }
    return COMMAND_RESULT_APPLIED;
}

/* Handle one already-decoded PUBLISH that is addressed to device/control. */
static void HandleControlPublish(ControlLink *link, const MqttPacket *packet,
                                 uint32_t received_uptime_ms, uint32_t now_ms,
                                 uint8_t *ack_buffer, uint32_t ack_capacity,
                                 ControlOutcome *outcome)
{
    ControlCommand command;
    CommandAckPayload ack;
    CommandResult result;
    CommandResult remembered = COMMAND_RESULT_APPLIED;
    uint32_t ack_length;

    command.type = COMMAND_SET_THRESHOLDS;
    command.request_id[0] = '\0';
    command.window_ms = 0U;
    command.threshold_version = 0U;
    command.thresholds.temperature_high_c = 0U;
    command.thresholds.humidity_high_rh = 0U;
    command.thresholds.gas_high_ppm = 0U;
    command.thresholds.temperature_rise_c = 0U;
    command.thresholds.gas_rise_adc = 0U;

    result = CommandJsonParse((const char *)packet->payload, packet->payload_length,
                             link->device_id, 0U, &command);
    if (result == COMMAND_RESULT_MALFORMED)
    {
        /* No requestId could be recovered, so there is nothing to address an
         * acknowledgement to. Counted, and the PUBACK already discharged the
         * transport obligation. */
        link->counters.unaddressable_frames++;
        return;
    }

    /* docs/device-protocol.md §4.1 orders the checks, and the order matters
     * twice over. Deduplication comes before the range and version checks, so a
     * redelivered command answers with the outcome the device already recorded;
     * asking the parser to compare the version first would answer
     * stale_version, which the backend would read as a fresh failure for a
     * command it already applied. The deadline comes after deduplication for the
     * same reason: a redelivery is duplicate, not expired.
     *
     * A command the parser already refused keeps its refusal. Falling through to
     * the execution branch on any result other than malformed would carry out a
     * command addressed to another device, which is the one mistake this whole
     * path must not make. */
    if (CommandDedupLookup(&link->dedup, command.request_id, &remembered))
    {
        /* Already handled. The first result stands and is not recomputed, so a
         * redelivery can never write Flash a second time. */
        result = COMMAND_RESULT_DUPLICATE;
    }
    else if (result == COMMAND_RESULT_APPLIED)
    {
        if (!CommandWithinWindow(&command, received_uptime_ms, now_ms))
        {
            result = COMMAND_RESULT_EXPIRED;
        }
        else
        {
            result = CommandCheckThresholdVersion(&command,
                                                 EnvMonitorThresholdVersion(link->monitor));
            if (result == COMMAND_RESULT_APPLIED)
            {
                result = ExecuteCommand(link, &command);
            }
        }
    }
    else
    {
        /* The parser's refusal stands and no side effect may follow from it. */
    }

    if (result != COMMAND_RESULT_DUPLICATE)
    {
        CommandDedupRecord(&link->dedup, command.request_id, result);
    }

    ack.device_id = link->device_id;
    ack.boot_id = link->boot_id;
    ack.sequence = link->sequence;
    ack.uptime_ms = now_ms;
    ack.request_id = command.request_id;
    ack.result = result;
    /* A threshold command reports the version it left in force: the newly applied
     * one, or the one already stored when the command turned out to be a
     * redelivery. */
    if (command.type == COMMAND_SET_THRESHOLDS)
    {
        ack.threshold_version = (result == COMMAND_RESULT_APPLIED) ?
                                command.threshold_version :
                                EnvMonitorThresholdVersion(link->monitor);
    }
    else
    {
        ack.threshold_version = 0U;
    }

    ack_length = CommandAckJsonEncode(&ack, (char *)ack_buffer, ack_capacity);
    CountResult(&link->counters, result);
    if (ack_length == 0U)
    {
        /* The ACK did not fit. The command may already have taken effect, so this
         * is reported as a handler failure rather than a command failure; the
         * backend's own timeout is what resolves the resulting uncertainty. */
        link->counters.send_failures++;
        return;
    }

    outcome->publish_ack = true;
    outcome->ack_topic = CONTROL_TOPIC_ACK;
    outcome->ack_length = ack_length;
    outcome->result = result;

    /* Advance the shared boot counter so the next message — telemetry or
     * acknowledgement — reports a number no earlier one used. It steps only once
     * the acknowledgement exists, so a frame that could not be answered leaves
     * the counter where it was and the two streams stay consistent. */
    link->sequence++;
}

uint32_t ControlLinkHandlePacket(ControlLink *link, const MqttPacket *packet,
                                 uint32_t received_uptime_ms, uint32_t now_ms,
                                 uint8_t *ack_buffer, uint32_t ack_capacity,
                                 ControlOutcome *outcome)
{
    uint8_t qos;

    if (link == NULL || packet == NULL || outcome == NULL)
    {
        return 0U;
    }

    outcome->send_puback = false;
    outcome->puback_packet_id = 0U;
    outcome->publish_ack = false;
    outcome->ack_topic = NULL;
    outcome->ack_length = 0U;
    outcome->result = COMMAND_RESULT_MALFORMED;

    if (packet->type != MQTT_PACKET_PUBLISH)
    {
        link->counters.non_publish_frames++;
        return 0U;
    }

    /* The MQTT acknowledgement is owed for the QoS 1 delivery itself, before and
     * independently of anything the payload says. Acknowledging a foreign topic
     * too is deliberate: the PUBACK is what stops the broker redelivering a frame
     * the device has already consumed, and the device only subscribes to
     * device/control, so a foreign topic here is a broker-side misconfiguration
     * rather than a command. */
    qos = PublishQos(packet);
    if (qos == 1U)
    {
        outcome->send_puback = true;
        outcome->puback_packet_id = packet->packet_id;
    }
    else
    {
        link->counters.non_qos1_frames++;
    }

    if (!TopicIsControl(packet))
    {
        link->counters.foreign_topic_frames++;
        return 0U;
    }
    if (!link->online)
    {
        /* Acknowledged above, never carried out: the command was issued to a
         * session this device was not part of. */
        link->counters.offline_frames++;
        return 0U;
    }
    if (packet->payload == NULL || packet->payload_length == 0U)
    {
        link->counters.unaddressable_frames++;
        return 0U;
    }

    HandleControlPublish(link, packet, received_uptime_ms, now_ms, ack_buffer,
                         ack_capacity, outcome);
    return outcome->publish_ack ? 1U : 0U;
}

/* Everything the frame trampoline needs to turn one decoded packet into a
 * callback. */
typedef struct
{
    ControlLink *link;
    ControlLinkVisitor visit;
    void *context;
    uint32_t received_uptime_ms;
    uint32_t now_ms;
    uint8_t *ack_buffer;
    uint32_t ack_capacity;
    uint32_t handled;
} ControlScan;

/* Adapt one decoded frame to the command path and then to the caller. */
static void ScanFrame(void *context, const MqttPacket *packet)
{
    ControlScan *scan = (ControlScan *)context;
    ControlOutcome outcome;

    if (ControlLinkHandlePacket(scan->link, packet, scan->received_uptime_ms, scan->now_ms,
                               scan->ack_buffer, scan->ack_capacity, &outcome) != 0U)
    {
        scan->handled++;
    }
    if (scan->visit != NULL)
    {
        /* The callback publishes before returning. Nothing here retains the
         * outcome or the ACK payload across frames, because both are recycled by
         * the next iteration. */
        if (!scan->visit(scan->context, packet, &outcome, scan->ack_buffer))
        {
            scan->link->counters.send_failures++;
        }
    }
}

uint32_t ControlLinkHandleBuffer(ControlLink *link, const uint8_t *data, uint32_t length,
                                 uint32_t received_uptime_ms, uint32_t now_ms,
                                 uint8_t *ack_buffer, uint32_t ack_capacity,
                                 ControlLinkVisitor visit, void *context)
{
    ControlScan scan;
    MqttScanResult result;

    if (link == NULL || data == NULL || length == 0U)
    {
        return 0U;
    }

    scan.link = link;
    scan.visit = visit;
    scan.context = context;
    scan.received_uptime_ms = received_uptime_ms;
    scan.now_ms = now_ms;
    scan.ack_buffer = ack_buffer;
    scan.ack_capacity = ack_capacity;
    scan.handled = 0U;

    result = MqttForEachPacket(data, length, ScanFrame, &scan);
    link->counters.frames_decoded += result.decoded;
    link->counters.frames_undecodable += result.undecodable;
    if (result.partial)
    {
        /* The driver hands up one `+IPD` payload at a time and keeps no
         * partial-frame state, so a frame the broker split across two segments
         * cannot be reassembled here. It is counted rather than ignored, and the
         * broker's QoS 1 redelivery is what recovers it. */
        link->counters.partial_frames++;
    }

    return scan.handled;
}
