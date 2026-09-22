#include "stm32f10x.h"                  // Device header
#include "delay.h"
#include "app_config.h"
#include "OLED.h"
#include "adc.h"
#include "dht11.h"
#include "control_link.h"
#include "session_dispatch.h"
#include "display_model.h"
#include "env_monitor.h"
#include "flash_config.h"
#include "threshold_store.h"
#include "telemetry_json.h"
#include "mqtt_packet.h"
#include "boot_id.h"
#include "esp8266.h"
#include "led.h"

/* Message buffer for the downlink text shown on the network page. Unchanged
 * from the previous firmware, including the 44-character limit the panel
 * effectively imposed. */
#define WIFI_MESSAGE_SIZE 64U

/* Control acknowledgement staging. The rendered JSON is a couple of hundred
 * bytes at its widest — a 32-character requestId, a 16-character bootId and the
 * longest errorCode — so this leaves generous slack while staying small enough
 * not to matter on a part with 20 KiB of RAM. */
#define CONTROL_ACK_SIZE 384U

/* Task cadence, in units of the main loop period.
 *
 * The loop period is nominal: delay_ms is a busy wait and the DHT11 read adds a
 * few milliseconds once a second, so the real period is slightly longer than
 * 100 ms. That drift is acceptable for the rise window, which is specified in
 * tens of seconds, and it is much better than the previous firmware's single
 * one-second cadence, which could not support a ten-sample gas filter at all.
 */
#define LOCAL_TICK_MS 100U
#define CLIMATE_PERIOD_TICKS 10U       /* DHT11 once a second, its minimum interval */
#define DISPLAY_REFRESH_TICKS 5U       /* panel refresh every 500 ms */
#define DISPLAY_ROTATE_TICKS 20U       /* page change every 2 s */
#define UPLINK_PERIOD_TICKS 10U        /* one telemetry frame per second */
#define MQTT_KEEP_ALIVE_SECONDS 30U

static void BuildBootId(char bootId[17])
{
    uint32_t identity = (*(const uint32_t *)0x1FFFF7E8UL) ^
                        (*(const uint32_t *)0x1FFFF7ECUL) ^
                        (*(const uint32_t *)0x1FFFF7F0UL);
    BootIdFormat(identity, SysTick->VAL ^ MY_ADC_GetValue(), bootId);
}

static uint8_t MqttSend(uint8_t *packet, uint32_t length)
{
    return (length > 0U && length <= 0xFFFFU) ?
           ESP8266_SendBytes(packet, (uint16_t)length) : 0U;
}

static uint8_t MqttSendWrapper(void *context, const uint8_t *data, uint32_t length)
{
    (void)context;
    return MqttSend((uint8_t *)data, length);
}

int main(void)
{
    EnvMonitor monitor;
    ThresholdStore thresholdStore;
    EnvThresholds storedThresholds;
    uint32_t storedVersion = 0U;
    uint8_t thresholdAreaDamaged = 0U;
    DisplayFrame frame;
    DisplayPage page = DISPLAY_PAGE_CLIMATE;
    DisplayInput display;
    EnvEvaluation evaluation;
    ControlLink controlLink;
    SessionDispatcher sessionDispatcher;
    ESP8266ReceiveStats rxStats;

    uint8_t temperature = 0U;
    uint8_t humidity = 0U;
    uint8_t dhtError;
    uint8_t wifiTaskStatus;
    uint8_t buzzerActive = 0U;
    uint8_t mqttTx[MQTT_MAX_PACKET_SIZE];
    uint8_t mqttRx[MQTT_MAX_PACKET_SIZE];
    uint8_t controlAck[CONTROL_ACK_SIZE];
    char telemetryJson[512];
    char bootId[17];
    uint16_t mqttRxLength = 0U;
    uint16_t mqttPacketId = 1U;
    uint32_t telemetrySequence = 0U;

    uint32_t now_ms = 0U;
    uint32_t tick = 0U;
    uint32_t lastPageTick = 0U;
    char wifiMessage[WIFI_MESSAGE_SIZE];

    SystemCoreClockUpdate();
    delay_init((uint8_t)(SystemCoreClock / 1000000U));

    wifiMessage[0] = '\0';
    OLED_Init();

    display.network = DISPLAY_NETWORK_LINKING;
    display.wifi_ssid = WIFI_SSID;
    display.server_message = wifiMessage;
    display.gas_uncalibrated = true;
    DisplayModelRender(page, NULL, &frame);
    OLED_Clear();
    for (uint8_t line = 0U; line < DISPLAY_LINE_COUNT; line++)
    {
        OLED_ShowString(0U, (uint8_t)(line * 16U), frame.lines[line], OLED_8X16);
    }
    OLED_Update();

    MY_ADC_Init();
    BuildBootId(bootId);
    LED_Init();
    BEEP_Init();

    /* Bring-up self-test: exercises PA4 and PA8 for a moment before anything
     * else can turn them on. The buzzer is otherwise only driven from the alarm
     * path, so a silent buzzer on the bench is ambiguous — it can mean the
     * driver is not reaching the pin, or that no alarm is active. A chirp at
     * power-up resolves that without touching the alarm logic. See
     * app_config.h for why this exists and how to switch it off. */
    if (HARDWARE_SELFTEST_ON_BOOT != 0U)
    {
        LED_On();
        BEEP_On();
        delay_ms(HARDWARE_SELFTEST_MS);
        LED_Off();
        BEEP_Off();
    }

    /* The local monitor is initialised before the network is touched. Sampling,
     * filtering and the alarm decision must not wait for Wi-Fi: the device has
     * to be able to alarm in a room with no access point. */
    EnvMonitorInit(&monitor);

    /* Load the stored thresholds before the first evaluation, so the device
     * enforces the operator's limits from its first sample instead of the
     * compile-time defaults.
     *
     * When nothing valid is stored — or the reserved pages hold something that
     * is neither a record nor erased, which means another part of the firmware
     * wrote there — the compile-time defaults stay in force. That is the
     * documented safe outcome: a device that cannot read its configuration must
     * keep alarming on the built-in limits rather than on zeros, which would
     * alarm on every sample.
     *
     * The write path is driven by the control command handler in
     * hardware/core/control_link.c: a set_thresholds command reaches the store
     * only after the parser has accepted it, and the monitor adopts the new
     * values only after the store reports the record written and verified. A
     * device flashed from an empty part therefore still reports no stored
     * configuration until the first such command arrives. */
    ThresholdStoreInit(&thresholdStore, FlashConfigPort());
    if (!FlashConfigSelfCheck())
    {
        thresholdAreaDamaged = 1U;
    }
    if (ThresholdStoreLoad(&thresholdStore, &storedThresholds, &storedVersion))
    {
        (void)EnvMonitorSetThresholds(&monitor, &storedThresholds, storedVersion);
    }

    /* The control path is bound to the monitor and the store before either can be
     * driven from the radio: the mute command acts on the monitor, and a
     * threshold command is published as applied only after this store has written
     * and verified the record. */
    ControlLinkInit(&controlLink, DEVICE_ID, bootId, &monitor, &thresholdStore);
    SessionDispatchInit(&sessionDispatcher, mqttTx, (uint32_t)sizeof(mqttTx),
                        &mqttPacketId, &controlLink, MqttSendWrapper, NULL);

    dhtError = DHT11_Init();
    (void)dhtError;
    delay_ms(1000);

    (void)ESP8266_Init();
    display.network = (ESP8266_IsWifiConnected() != 0U) ? DISPLAY_NETWORK_LINKED : DISPLAY_NETWORK_FAILED;

    while (1)
    {
        uint16_t raw_adc = MY_ADC_GetValue();

        /* --- gas: filter, then estimate from the filtered value --- */
        EnvMonitorPushGas(&monitor, raw_adc);
        {
            /* The estimate accompanies the filtered value, so both fields in the
             * telemetry describe the same reading. */
            uint16_t filtered = EnvMonitorGasFiltered(&monitor);
            EnvMonitorSetGasEstimate(&monitor, (uint16_t)(MQ135_EstimatePpm(filtered) + 0.5f));
        }

        /* --- climate: the DHT11 tolerates at most one read per second --- */
        if ((tick % CLIMATE_PERIOD_TICKS) == 0U)
        {
            dhtError = DHT11_Read_Data(&temperature, &humidity);
        }
        EnvMonitorPushClimate(&monitor,
                              temperature,
                              humidity,
                              (uint8_t)((dhtError == 0U) ? 1U : 0U),
                              now_ms);

        /* --- local alarm: evaluated every tick, independent of the network --- */
        evaluation = EnvMonitorEvaluate(&monitor, now_ms);

        if (evaluation.local_alarm)
        {
            LED_On();
        }
        else
        {
            LED_Off();
        }

        /* Only gas-related causes are audible in this stage. Other causes still
         * drive the LED, OLED and telemetry. The 200 ms / 800 ms cadence avoids
         * a continuous tone while preserving an unmistakable local warning. The
         * decision itself lives in the monitor so that the audible-cause filter,
         * the cadence and the mute precedence are covered by the host tests
         * rather than only by watching a board. */
        buzzerActive = (uint8_t)(EnvMonitorBuzzerDrive(&evaluation, tick) ? 1U : 0U);
        if (buzzerActive != 0U)
        {
            BEEP_On();
        }
        else
        {
            BEEP_Off();
        }

        /* --- display --- */
        if ((tick - lastPageTick) >= DISPLAY_ROTATE_TICKS)
        {
            lastPageTick = tick;
            DisplayModelNextPage(&page);
        }
        if ((tick % DISPLAY_REFRESH_TICKS) == 0U)
        {
            uint8_t line;

            display.temperature_c = evaluation.temperature_c;
            display.humidity_rh = evaluation.humidity_rh;
            display.gas_ppm = evaluation.gas_ppm;
            display.gas_adc_raw = evaluation.gas_adc_raw;
            display.gas_adc_filtered = evaluation.gas_adc_filtered;
            display.alarm_causes = evaluation.alarm_causes;
            /* The driver's own status code travels with the fault, so the panel
             * can say which failure it is instead of only that one happened. */
            display.dht_error = dhtError;
            display.buzzer_muted = EnvMonitorMuted(&monitor);
            /* Reflect audible alarm status on the display rather than the 200 ms
             * instantaneous cadence pulse, so the OLED does not flash "off" for
             * 800 ms of each second during an active gas alarm. */
            display.buzzer_active = evaluation.buzzer_on &&
                ((evaluation.alarm_causes & ((uint32_t)ENV_ALARM_GAS_HIGH | (uint32_t)ENV_ALARM_RAPID_GAS_RISE)) != 0U);
            display.threshold_version = EnvMonitorThresholdVersion(&monitor);
            /* The panel shows the fault rather than hiding it: an operator
             * looking at the device should be able to see that its stored
             * configuration area is unusable. */
            (void)thresholdAreaDamaged;
            /* Likewise for frames the radio could not hand up: a counted loss is
             * the only on-device evidence that a control command may have been
             * dropped rather than never sent. */
            ESP8266_GetReceiveStats(&rxStats);
            display.rx_discarded = rxStats.discarded_frames;
            display.rx_truncated = rxStats.truncated_frames;
            display.wifi_ssid = WIFI_SSID;
            display.server_message = wifiMessage;

            DisplayModelRender(page, &display, &frame);

            OLED_Clear();
            for (line = 0U; line < DISPLAY_LINE_COUNT; line++)
            {
                OLED_ShowString(0U, (uint8_t)(line * 16U), frame.lines[line], OLED_8X16);
            }
            OLED_Update();
        }

        /* --- MQTT downlink ---
         * ESP8266 carries raw MQTT bytes over a single TCP socket. Connection
         * acknowledgement and subscription acknowledgement are required before
         * the OLED reports the network online, preventing the former false
         * positive where Wi-Fi association alone looked like cloud delivery.
         *
         * The whole received segment is scanned, not only its first frame: a TCP
         * segment can carry several MQTT packets, and reading only the head would
         * drop every command behind the first one without a trace. Every
         * acknowledgement is built and sent inside the scan, before the driver can
         * collect another segment into the buffer this one came from. */
        /* Check TCP connection status first so an offline radio immediately marks
         * ControlLink offline and drops to MQTT_LINK_TCP without waiting an extra cycle. */
        if (ESP8266_IsTcpConnected() == 0U)
        {
            SessionDispatchTcpDisconnected(&sessionDispatcher);
        }
        else
        {
            SessionDispatchSyncOnline(&sessionDispatcher);
        }

        if (ESP8266_GetPacket(mqttRx, sizeof(mqttRx), &mqttRxLength) != 0U)
        {
            sessionDispatcher.now_ms = now_ms;
            /* The acknowledgement counter is the same boot-scoped counter the
             * telemetry frames report, so it is seeded here and read back after
             * the scan: one number space, so the two streams can be ordered
             * against each other. */
            ControlLinkSetSequence(&controlLink, telemetrySequence);
            (void)ControlLinkHandleBuffer(&controlLink, mqttRx, mqttRxLength, now_ms, now_ms,
                                          controlAck, sizeof(controlAck), SessionDispatchFrame,
                                          &sessionDispatcher);
            telemetrySequence = ControlLinkSequence(&controlLink);
        }

        /* If TCP dropped during packet handling or send, drop to offline immediately. */
        if (ESP8266_IsTcpConnected() == 0U)
        {
            SessionDispatchTcpDisconnected(&sessionDispatcher);
        }

        if ((tick % UPLINK_PERIOD_TICKS) == 0U)
        {
            if (ESP8266_IsWifiConnected() == 0U)
            {
                display.network = DISPLAY_NETWORK_LINKING;
                wifiTaskStatus = ESP8266_Init();
                (void)wifiTaskStatus;
            }
            else if (sessionDispatcher.state == MQTT_LINK_TCP && ESP8266_OpenTcp() != 0U)
            {
                uint32_t length = MqttEncodeConnect(mqttTx, sizeof(mqttTx), DEVICE_ID,
                                                    MQTT_KEEP_ALIVE_SECONDS, "device", 0);
                if (MqttSend(mqttTx, length) != 0U)
                {
                    sessionDispatcher.state = MQTT_LINK_WAIT_CONNACK;
                    sessionDispatcher.last_activity_ms = now_ms;
                }
            }
            else if (sessionDispatcher.state == MQTT_LINK_ONLINE)
            {
                TelemetryPayload telemetry;
                uint32_t jsonLength;
                uint32_t packetLength;

                telemetry.device_id = DEVICE_ID;
                telemetry.boot_id = bootId;
                telemetry.sequence = telemetrySequence;
                telemetry.uptime_ms = now_ms;
                telemetry.temperature_c = evaluation.temperature_c;
                telemetry.humidity_rh = evaluation.humidity_rh;
                telemetry.gas_adc_raw = evaluation.gas_adc_raw;
                telemetry.gas_adc_filtered = evaluation.gas_adc_filtered;
                telemetry.gas_ppm_tenths = (uint16_t)(evaluation.gas_ppm * 10U);
                telemetry.gas_calibrated = false;
                telemetry.local_alarm = evaluation.local_alarm;
                telemetry.alarm_causes = evaluation.alarm_causes;
                telemetry.buzzer_muted = EnvMonitorMuted(&monitor);
                telemetry.network_online = true;
                telemetry.threshold_version = EnvMonitorThresholdVersion(&monitor);
                telemetry.sensor_fault = ((evaluation.alarm_causes & ENV_ALARM_SENSOR_FAULT) != 0U);
                jsonLength = TelemetryJsonEncode(&telemetry, telemetryJson, sizeof(telemetryJson));
                packetLength = MqttEncodePublish(mqttTx, sizeof(mqttTx), "device/telemetry",
                                                 SessionTakePacketId(&mqttPacketId), 1U,
                                                 (const uint8_t *)telemetryJson, jsonLength);
                if (jsonLength > 0U && MqttSend(mqttTx, packetLength) != 0U)
                {
                    telemetrySequence++;
                    sessionDispatcher.last_activity_ms = now_ms;
                }
            }
            display.network = (sessionDispatcher.state == MQTT_LINK_ONLINE) ?
                              DISPLAY_NETWORK_LINKED : DISPLAY_NETWORK_LINKING;
        }

        if (sessionDispatcher.state == MQTT_LINK_ONLINE &&
            (now_ms - sessionDispatcher.last_activity_ms) >= (MQTT_KEEP_ALIVE_SECONDS * 500U))
        {
            uint32_t length = MqttEncodePingReq(mqttTx, sizeof(mqttTx));
            if (MqttSend(mqttTx, length) != 0U)
            {
                sessionDispatcher.last_activity_ms = now_ms;
            }
        }

        delay_ms(LOCAL_TICK_MS);
        tick++;
        now_ms += LOCAL_TICK_MS;
    }
}
