#include "session_dispatch.h"

uint16_t SessionTakePacketId(uint16_t *next)
{
    uint16_t current;

    if (next == NULL)
    {
        return 1U;
    }
    current = *next;
    (*next)++;
    if (*next == 0U)
    {
        *next = 1U;
    }
    return current;
}

void SessionDispatchInit(SessionDispatcher *dispatcher, uint8_t *tx, uint32_t capacity,
                         uint16_t *next_packet_id, ControlLink *link,
                         SessionSendBytesFn send_fn, void *io_context)
{
    if (dispatcher == NULL)
    {
        return;
    }
    dispatcher->tx = tx;
    dispatcher->capacity = capacity;
    dispatcher->next_packet_id = next_packet_id;
    dispatcher->pending_sub_packet_id = 0U;
    dispatcher->state = MQTT_LINK_TCP;
    dispatcher->last_activity_ms = 0U;
    dispatcher->now_ms = 0U;
    dispatcher->link = link;
    dispatcher->send_fn = send_fn;
    dispatcher->io_context = io_context;
}

void SessionDispatchTcpDisconnected(SessionDispatcher *dispatcher)
{
    if (dispatcher == NULL)
    {
        return;
    }
    dispatcher->state = MQTT_LINK_TCP;
    dispatcher->pending_sub_packet_id = 0U;
    if (dispatcher->link != NULL)
    {
        ControlLinkSetOnline(dispatcher->link, false);
    }
}

void SessionDispatchSyncOnline(SessionDispatcher *dispatcher)
{
    if (dispatcher == NULL || dispatcher->link == NULL)
    {
        return;
    }
    ControlLinkSetOnline(dispatcher->link, dispatcher->state == MQTT_LINK_ONLINE);
}

bool SessionDispatchFrame(void *context, const MqttPacket *packet,
                          const ControlOutcome *outcome, const uint8_t *ack_payload)
{
    SessionDispatcher *dispatcher = (SessionDispatcher *)context;
    bool sent = true;

    if (dispatcher == NULL || packet == NULL || outcome == NULL)
    {
        return false;
    }

    if (dispatcher->state == MQTT_LINK_WAIT_CONNACK &&
        packet->type == MQTT_PACKET_CONNACK &&
        packet->return_code == MQTT_CONNACK_ACCEPTED)
    {
        uint16_t sub_packet_id = SessionTakePacketId(dispatcher->next_packet_id);
        uint32_t length = MqttEncodeSubscribe(dispatcher->tx, dispatcher->capacity,
                                              sub_packet_id,
                                              CONTROL_TOPIC_COMMAND, 1U);
        if (length != 0U && dispatcher->send_fn != NULL &&
            dispatcher->send_fn(dispatcher->io_context, dispatcher->tx, length) != 0U)
        {
            dispatcher->state = MQTT_LINK_WAIT_SUBACK;
            dispatcher->pending_sub_packet_id = sub_packet_id;
            dispatcher->last_activity_ms = dispatcher->now_ms;
        }
    }
    else if (dispatcher->state == MQTT_LINK_WAIT_SUBACK && packet->type == MQTT_PACKET_SUBACK)
    {
        bool suback_valid = true;

        /* Verify that SUBACK does not indicate failure (0x80) and granted QoS is valid (0..2) */
        if (packet->granted_qos > 2U || packet->granted_qos == 0x80U)
        {
            suback_valid = false;
        }
        /* If a SUBSCRIBE was issued with a non-zero packetId, verify that SUBACK matches it */
        if (dispatcher->pending_sub_packet_id != 0U &&
            packet->packet_id != dispatcher->pending_sub_packet_id)
        {
            suback_valid = false;
        }

        if (suback_valid)
        {
            dispatcher->state = MQTT_LINK_ONLINE;
            dispatcher->pending_sub_packet_id = 0U;
            dispatcher->last_activity_ms = dispatcher->now_ms;
            if (dispatcher->link != NULL)
            {
                ControlLinkSetOnline(dispatcher->link, true);
            }
        }
    }
    else if (packet->type == MQTT_PACKET_PINGRESP)
    {
        dispatcher->last_activity_ms = dispatcher->now_ms;
    }

    if (outcome->send_puback)
    {
        uint32_t length = MqttEncodePuback(dispatcher->tx, dispatcher->capacity,
                                           outcome->puback_packet_id);
        if (length == 0U || dispatcher->send_fn == NULL ||
            dispatcher->send_fn(dispatcher->io_context, dispatcher->tx, length) == 0U)
        {
            sent = false;
        }
    }

    if (outcome->publish_ack)
    {
        uint16_t ack_packet_id = SessionTakePacketId(dispatcher->next_packet_id);
        uint32_t length = MqttEncodePublish(dispatcher->tx, dispatcher->capacity,
                                            outcome->ack_topic,
                                            ack_packet_id, 1U,
                                            ack_payload, outcome->ack_length);
        if (length == 0U || dispatcher->send_fn == NULL ||
            dispatcher->send_fn(dispatcher->io_context, dispatcher->tx, length) == 0U)
        {
            sent = false;
        }
    }

    return sent;
}
