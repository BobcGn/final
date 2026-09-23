#include "mqtt_packet.h"

const char *const MQTT_REJECT_MALFORMED = "malformed_packet";
const char *const MQTT_REJECT_UNSUPPORTED = "unsupported_packet";
const char *const MQTT_REJECT_TOO_LARGE = "packet_too_large";

/* MQTT 3.1.1 protocol level and name, as required by the CONNECT variable
 * header. The backend speaks the same level. */
#define MQTT_PROTOCOL_LEVEL 4U

/* A control packet type is four bits, so anything above DISCONNECT is invalid. */
#define MQTT_MAX_PACKET_TYPE 14U

/* Smallest and largest value the remaining-length field can carry. */
#define MQTT_MAX_REMAINING_LENGTH 268435455U

/* Writer over the outgoing buffer. It mirrors the JSON writer's contract: an
 * overflowing write is recorded, never written past the end. */
typedef struct
{
    uint8_t *buffer;
    uint32_t capacity;
    uint32_t length;
    bool overflow;
} PacketWriter;

/* Append one byte. */
static void writer_byte(PacketWriter *writer, uint8_t value)
{
    if (writer->overflow)
    {
        return;
    }
    if (writer->length >= writer->capacity)
    {
        writer->overflow = true;
        return;
    }
    writer->buffer[writer->length] = value;
    writer->length++;
}

/* Append a big-endian 16-bit value. */
static void writer_uint16(PacketWriter *writer, uint16_t value)
{
    writer_byte(writer, (uint8_t)((value >> 8) & 0xFFU));
    writer_byte(writer, (uint8_t)(value & 0xFFU));
}

/* Append a length-prefixed byte string. */
static void writer_bytes(PacketWriter *writer, const uint8_t *data, uint32_t length)
{
    uint32_t index;

    if (length > 0xFFFFU)
    {
        writer->overflow = true;
        return;
    }
    writer_uint16(writer, (uint16_t)length);
    for (index = 0U; index < length; index++)
    {
        writer_byte(writer, data[index]);
    }
}

/* Append a length-prefixed C string. A NULL pointer writes a zero-length field,
 * which is how the protocol expresses "no value". */
static void writer_string(PacketWriter *writer, const char *text)
{
    uint32_t length = 0U;

    if (text != NULL)
    {
        while (text[length] != '\0')
        {
            length++;
        }
    }
    writer_bytes(writer, (const uint8_t *)text, length);
}

/* Length of a C string, or 0 for a NULL pointer. */
static uint32_t string_length(const char *text)
{
    uint32_t length = 0U;

    while (text != NULL && text[length] != '\0')
    {
        length++;
    }
    return length;
}

/* Append the fixed header and, later, the body.
 *
 * The remaining length is not known until the body is built, so the header is
 * written into a small scratch area and the body immediately after it; the
 * header length is at most five bytes and its own length depends only on the
 * body length, which the caller computes first. */
static void writer_fixed_header(PacketWriter *writer, uint8_t type, uint8_t flags, uint32_t remaining)
{
    uint32_t value = remaining;

    writer_byte(writer, (uint8_t)(((uint32_t)type << 4) | (uint32_t)(flags & 0x0FU)));
    do
    {
        uint8_t digit = (uint8_t)(value % 128U);
        value /= 128U;
        if (value > 0U)
        {
            digit |= 0x80U;
        }
        writer_byte(writer, digit);
    } while (value > 0U);
}

/* Number of bytes the remaining-length field needs for `length`. */
static uint32_t variable_length_size(uint32_t length)
{
    uint32_t size = 1U;

    while (length >= 128U)
    {
        length /= 128U;
        size++;
    }
    return size;
}

/* Finish a packet whose body has already been written.
 *
 * `body_start` is the offset at which the body began. The fixed header is
 * inserted by shifting the body right, which avoids a second buffer: the part
 * has no spare RAM for one. */
static uint32_t writer_finish(PacketWriter *writer, uint8_t type, uint8_t flags, uint32_t body_start)
{
    uint32_t body_length;
    uint32_t header_length;
    uint32_t index;

    if (writer->overflow)
    {
        return 0U;
    }
    body_length = writer->length - body_start;
    header_length = 1U + variable_length_size(body_length);
    if (writer->length + header_length > writer->capacity)
    {
        return 0U;
    }

    /* Shift the body right by header_length, then write the header in front. */
    index = writer->length;
    while (index > body_start)
    {
        index--;
        writer->buffer[index + header_length] = writer->buffer[index];
    }
    writer->length += header_length;
    {
        PacketWriter header;
        header.buffer = writer->buffer;
        header.capacity = header_length;
        header.length = 0U;
        header.overflow = false;
        writer_fixed_header(&header, type, flags, body_length);
        if (header.overflow)
        {
            return 0U;
        }
    }
    return writer->length;
}

uint32_t MqttEncodeConnect(uint8_t *buffer, uint32_t capacity, const char *client_id,
                           uint16_t keep_alive_seconds, const char *username, const char *password)
{
    PacketWriter writer;
    uint32_t body_start;
    uint8_t connect_flags = 0x02U; /* Clean session: the device keeps no server-side state. */

    if (buffer == NULL || client_id == NULL || string_length(client_id) > 0xFFFFU)
    {
        return 0U;
    }

    writer.buffer = buffer;
    writer.capacity = capacity;
    writer.length = 0U;
    writer.overflow = false;

    /* The body is written first and the header inserted afterwards, so reserve
     * the maximum header size by starting the body past it and shifting later. */
    body_start = 0U;
    writer_string(&writer, "MQTT");
    writer_byte(&writer, MQTT_PROTOCOL_LEVEL);
    if (username != NULL)
    {
        connect_flags |= 0x80U;
    }
    if (password != NULL)
    {
        connect_flags |= 0x40U;
    }
    writer_byte(&writer, connect_flags);
    writer_uint16(&writer, keep_alive_seconds);
    writer_string(&writer, client_id);
    if (username != NULL)
    {
        writer_string(&writer, username);
    }
    if (password != NULL)
    {
        writer_string(&writer, password);
    }
    return writer_finish(&writer, MQTT_PACKET_CONNECT, 0U, body_start);
}

uint32_t MqttEncodeSubscribe(uint8_t *buffer, uint32_t capacity, uint16_t packet_id,
                             const char *topic_filter, uint8_t qos)
{
    PacketWriter writer;
    uint32_t body_start = 0U;

    if (buffer == NULL || topic_filter == NULL || packet_id == 0U || qos > 1U)
    {
        return 0U;
    }

    writer.buffer = buffer;
    writer.capacity = capacity;
    writer.length = 0U;
    writer.overflow = false;

    writer_uint16(&writer, packet_id);
    writer_string(&writer, topic_filter);
    writer_byte(&writer, qos);
    /* SUBSCRIBE carries reserved flags 0b0010; the protocol requires exactly
     * that value and a broker may drop a session that gets it wrong. */
    return writer_finish(&writer, MQTT_PACKET_SUBSCRIBE, 0x02U, body_start);
}

uint32_t MqttEncodePublish(uint8_t *buffer, uint32_t capacity, const char *topic,
                           uint16_t packet_id, uint8_t qos, const uint8_t *payload,
                           uint32_t payload_length)
{
    PacketWriter writer;
    uint32_t body_start = 0U;
    uint8_t flags;

    if (buffer == NULL || topic == NULL || qos > 1U)
    {
        return 0U;
    }
    if (qos > 0U && packet_id == 0U)
    {
        /* A QoS 1 publish without an identifier cannot be acknowledged, so it
         * would be delivered at most once without the sender knowing. */
        return 0U;
    }
    if (payload == NULL && payload_length > 0U)
    {
        return 0U;
    }

    writer.buffer = buffer;
    writer.capacity = capacity;
    writer.length = 0U;
    writer.overflow = false;

    writer_string(&writer, topic);
    if (qos > 0U)
    {
        writer_uint16(&writer, packet_id);
    }
    if (payload != NULL)
    {
        uint32_t index;

        for (index = 0U; index < payload_length; index++)
        {
            writer_byte(&writer, payload[index]);
        }
    }
    flags = (uint8_t)((qos & 0x03U) << 1);
    return writer_finish(&writer, MQTT_PACKET_PUBLISH, flags, body_start);
}

uint32_t MqttEncodePuback(uint8_t *buffer, uint32_t capacity, uint16_t packet_id)
{
    if (buffer == NULL || packet_id == 0U || capacity < 4U)
    {
        return 0U;
    }
    buffer[0] = (uint8_t)(MQTT_PACKET_PUBACK << 4);
    buffer[1] = 2U;
    buffer[2] = (uint8_t)((packet_id >> 8) & 0xFFU);
    buffer[3] = (uint8_t)(packet_id & 0xFFU);
    return 4U;
}

uint32_t MqttEncodePingReq(uint8_t *buffer, uint32_t capacity)
{
    if (buffer == NULL || capacity < 2U)
    {
        return 0U;
    }
    buffer[0] = (uint8_t)(MQTT_PACKET_PINGREQ << 4);
    buffer[1] = 0U;
    return 2U;
}

uint32_t MqttEncodeDisconnect(uint8_t *buffer, uint32_t capacity)
{
    if (buffer == NULL || capacity < 2U)
    {
        return 0U;
    }
    buffer[0] = (uint8_t)(MQTT_PACKET_DISCONNECT << 4);
    buffer[1] = 0U;
    return 2U;
}

/* Read the remaining length at `offset`. Returns false when the field is
 * incomplete or encoded in more than four bytes. */
static bool read_remaining_length(const uint8_t *data, uint32_t length, uint32_t offset,
                                  uint32_t *remaining, uint32_t *field_size)
{
    uint32_t value = 0U;
    uint32_t multiplier = 1U;
    uint32_t index;

    for (index = 0U; index < 4U; index++)
    {
        if (offset + index >= length)
        {
            return false;
        }
        value += (uint32_t)(data[offset + index] & 0x7FU) * multiplier;
        if ((data[offset + index] & 0x80U) == 0U)
        {
            *remaining = value;
            *field_size = index + 1U;
            return true;
        }
        multiplier *= 128U;
    }
    return false;
}

uint32_t MqttPacketTotalLength(const uint8_t *data, uint32_t length)
{
    uint32_t remaining = 0U;
    uint32_t field_size = 0U;

    if (data == NULL || length < 2U)
    {
        return 0U;
    }
    if (!read_remaining_length(data, length, 1U, &remaining, &field_size))
    {
        return 0U;
    }
    if (remaining > MQTT_MAX_PACKET_SIZE)
    {
        /* Report the frame as too large by returning its declared length; the
         * decoder produces the specific reason. */
        return 1U + field_size + remaining;
    }
    return 1U + field_size + remaining;
}

MqttScanResult MqttForEachPacket(const uint8_t *data, uint32_t length, MqttVisitor visit,
                                 void *context)
{
    MqttScanResult result;
    uint32_t offset = 0U;

    result.decoded = 0U;
    result.undecodable = 0U;
    result.partial = false;

    if (data == NULL || length == 0U)
    {
        return result;
    }

    while (offset < length)
    {
        uint32_t frame_length = MqttPacketTotalLength(&data[offset], length - offset);
        MqttPacket packet;
        const char *reason = MQTT_REJECT_MALFORMED;

        if (frame_length == 0U)
        {
            /* Fewer than two bytes, or a remaining-length field that has not
             * arrived in full. The tail is a fragment, and the driver keeps no
             * partial-frame state to reassemble it with. */
            result.partial = true;
            break;
        }
        if (frame_length > MQTT_MAX_PACKET_SIZE)
        {
            /* A length the device will never accept, so the frame is refused
             * before its bytes have to be present. */
            result.undecodable++;
            break;
        }
        if (frame_length > (length - offset))
        {
            /* The frame is declared in full but the buffer stops inside it. */
            result.partial = true;
            break;
        }

        if (MqttDecode(&data[offset], frame_length, &packet, &reason))
        {
            result.decoded++;
            if (visit != NULL)
            {
                visit(context, &packet);
            }
        }
        else
        {
            /* The framing was intact, so whatever follows is still a frame the
             * caller should get to see. Skipping rather than stopping is what
             * keeps one packet type the device should not have been sent from
             * hiding the command queued behind it. */
            result.undecodable++;
        }

        offset += frame_length;
    }

    return result;
}
static bool read_bytes(const uint8_t *data, uint32_t length, uint32_t offset,
                       const uint8_t **value, uint16_t *value_length, uint32_t *consumed)
{
    uint16_t size;

    if (offset + 2U > length)
    {
        return false;
    }
    size = (uint16_t)(((uint16_t)data[offset] << 8) | (uint16_t)data[offset + 1U]);
    if (offset + 2U + (uint32_t)size > length)
    {
        return false;
    }
    *value = &data[offset + 2U];
    *value_length = size;
    *consumed = 2U + (uint32_t)size;
    return true;
}

bool MqttDecode(const uint8_t *data, uint32_t length, MqttPacket *packet, const char **reason)
{
    uint32_t remaining = 0U;
    uint32_t field_size = 0U;
    uint32_t offset;
    uint8_t type;
    uint8_t flags;

    if (reason != NULL)
    {
        *reason = MQTT_REJECT_MALFORMED;
    }
    if (data == NULL || packet == NULL || length < 2U)
    {
        return false;
    }

    type = (uint8_t)(data[0] >> 4);
    flags = (uint8_t)(data[0] & 0x0FU);
    if (type == 0U || type > MQTT_MAX_PACKET_TYPE)
    {
        return false;
    }
    if (!read_remaining_length(data, length, 1U, &remaining, &field_size))
    {
        return false;
    }
    if (remaining > MQTT_MAX_PACKET_SIZE)
    {
        *reason = MQTT_REJECT_TOO_LARGE;
        return false;
    }
    if (1U + field_size + remaining > length)
    {
        /* The frame is not complete yet; the caller should read more. */
        return false;
    }

    offset = 1U + field_size;
    packet->type = (MqttPacketType)type;
    packet->flags = flags;
    packet->packet_id = 0U;
    packet->return_code = 0U;
    packet->granted_qos = 0U;
    packet->topic = NULL;
    packet->topic_length = 0U;
    packet->payload = NULL;
    packet->payload_length = 0U;
    packet->next = &data[offset + remaining];

    switch (packet->type)
    {
    case MQTT_PACKET_CONNACK:
        if (flags != 0U || remaining != 2U)
        {
            return false;
        }
        packet->return_code = data[offset + 1U];
        return true;

    case MQTT_PACKET_PUBACK:
        if (flags != 0U || remaining != 2U)
        {
            return false;
        }
        packet->packet_id = (uint16_t)(((uint16_t)data[offset] << 8) | (uint16_t)data[offset + 1U]);
        return packet->packet_id != 0U;

    case MQTT_PACKET_PUBLISH:
    {
        const uint8_t *topic = NULL;
        uint16_t topic_length = 0U;
        uint32_t consumed = 0U;
        uint8_t qos = (uint8_t)((flags >> 1) & 0x03U);

        if (qos > 1U)
        {
            *reason = MQTT_REJECT_UNSUPPORTED;
            return false;
        }
        if (!read_bytes(data, offset + remaining, offset, &topic, &topic_length, &consumed))
        {
            return false;
        }
        if (topic_length == 0U)
        {
            return false;
        }
        packet->topic = topic;
        packet->topic_length = topic_length;
        offset += consumed;
        if (qos > 0U)
        {
            if (offset + 2U > 1U + field_size + remaining)
            {
                return false;
            }
            packet->packet_id = (uint16_t)(((uint16_t)data[offset] << 8) | (uint16_t)data[offset + 1U]);
            if (packet->packet_id == 0U)
            {
                return false;
            }
            offset += 2U;
        }
        packet->payload = &data[offset];
        packet->payload_length = (1U + field_size + remaining) - offset;
        return true;
    }

    case MQTT_PACKET_SUBACK:
        if (flags != 0U || remaining < 3U)
        {
            return false;
        }
        packet->packet_id = (uint16_t)(((uint16_t)data[offset] << 8) | (uint16_t)data[offset + 1U]);
        packet->granted_qos = data[offset + 2U];
        return packet->packet_id != 0U;

    case MQTT_PACKET_PINGRESP:
        return flags == 0U && remaining == 0U;

    default:
        /* The device never receives CONNECT, SUBSCRIBE, PINGREQ or DISCONNECT
         * from a broker. Rejecting them keeps the receive path able to say why
         * rather than acting on a packet it should not have been sent. */
        *reason = MQTT_REJECT_UNSUPPORTED;
        return false;
    }
}
