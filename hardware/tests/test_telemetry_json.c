#include "env_monitor.h"
#include "mqtt_packet.h"
#include "telemetry_json.h"
#include "test_support.h"

/* A payload with every field at a known value. */
static TelemetryPayload sample_payload(void)
{
    TelemetryPayload payload;

    payload.device_id = "MCU001";
    payload.boot_id = "9f3ac21b";
    payload.sequence = 42U;
    payload.uptime_ms = 125000U;
    payload.temperature_c = 28U;
    payload.humidity_rh = 61U;
    payload.gas_adc_raw = 1350U;
    payload.gas_adc_filtered = 1328U;
    payload.gas_ppm_tenths = 250U;
    payload.gas_calibrated = false;
    payload.local_alarm = false;
    payload.alarm_causes = 0U;
    payload.network_online = true;
    payload.threshold_version = 1U;
    payload.sensor_fault = false;
    return payload;
}

/* Exercise the required field set, which is the part the backend depends on. */
static void test_required_fields(void)
{
    char buffer[512];
    TelemetryPayload payload = sample_payload();
    uint32_t length;

    TEST_CASE("the payload is well-formed JSON");
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(length > 0U);
    CHECK_TRUE(test_json_is_well_formed(buffer, length));

    TEST_CASE("every field the frozen contract marks required is present");
    CHECK_TRUE(test_json_has_number(buffer, "schemaVersion", "1"));
    CHECK_TRUE(test_json_has_string(buffer, "messageType", "telemetry"));
    CHECK_TRUE(test_json_has_string(buffer, "deviceId", "MCU001"));
    CHECK_TRUE(test_json_has_string(buffer, "bootId", "9f3ac21b"));
    CHECK_TRUE(test_json_has_number(buffer, "sequence", "42"));
    CHECK_TRUE(test_json_has_number(buffer, "uptimeMs", "125000"));
    CHECK_TRUE(test_json_has_number(buffer, "temperatureC", "28"));
    CHECK_TRUE(test_json_has_number(buffer, "humidityRh", "61"));
    CHECK_TRUE(test_json_has_number(buffer, "gasAdcRaw", "1350"));
    CHECK_TRUE(test_json_has_number(buffer, "gasAdcFiltered", "1328"));
    CHECK_TRUE(test_json_has_number(buffer, "gasPpm", "25.0"));
    CHECK_TRUE(test_json_has_number(buffer, "gasCalibrated", "false"));
    CHECK_TRUE(test_json_has_number(buffer, "localAlarm", "false"));
    CHECK_TRUE(test_json_has_number(buffer, "thresholdVersion", "1"));
    CHECK_TRUE(test_json_has_number(buffer, "sensorFault", "false"));

    TEST_CASE("the timestamp is null rather than a fabricated instant");
    /* The device has no trusted clock. Sending a plausible-looking number would
     * place every sample at the wrong point of every time-ordered query. */
    CHECK_TRUE(test_json_has_number(buffer, "timestamp", "null"));

    TEST_CASE("the network state uses the frozen spelling");
    CHECK_TRUE(test_json_has_string(buffer, "network", "online"));

    TEST_CASE("alarmCauses is an array even when empty");
    CHECK_TRUE(test_json_has_key(buffer, "alarmCauses"));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[]") != NULL);
}

/* Exercise the alarm cause list, whose order and spelling the backend parses. */
static void test_alarm_causes(void)
{
    char buffer[512];
    TelemetryPayload payload = sample_payload();
    uint32_t length;

    TEST_CASE("each cause maps to its frozen name");
    payload.alarm_causes = (uint32_t)ENV_ALARM_TEMPERATURE_HIGH;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_is_well_formed(buffer, length));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"temperature_high\"]") != NULL);

    payload.alarm_causes = (uint32_t)ENV_ALARM_HUMIDITY_HIGH;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"humidity_high\"]") != NULL);

    payload.alarm_causes = (uint32_t)ENV_ALARM_GAS_HIGH;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"gas_high\"]") != NULL);

    payload.alarm_causes = (uint32_t)ENV_ALARM_RAPID_TEMPERATURE_RISE;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"rapid_temperature_rise\"]") != NULL);

    payload.alarm_causes = (uint32_t)ENV_ALARM_RAPID_GAS_RISE;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"rapid_gas_rise\"]") != NULL);

    payload.alarm_causes = (uint32_t)ENV_ALARM_SENSOR_FAULT;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"sensor_fault\"]") != NULL);

    TEST_CASE("several causes are listed in the frozen order");
    payload.alarm_causes = (uint32_t)ENV_ALARM_GAS_HIGH | (uint32_t)ENV_ALARM_TEMPERATURE_HIGH |
                           (uint32_t)ENV_ALARM_SENSOR_FAULT;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_is_well_formed(buffer, length));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"temperature_high\",\"gas_high\",\"sensor_fault\"]") != NULL);

    TEST_CASE("a cause bit outside the known set is ignored rather than named");
    /* The device never sets one, but a corrupted mask must not produce a
     * string the backend would reject. */
    payload.alarm_causes = 1UL << 20;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_is_well_formed(buffer, length));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[]") != NULL);
}

/* Exercise the state flags. */
static void test_state_flags(void)
{
    char buffer[512];
    TelemetryPayload payload = sample_payload();
    uint32_t length;

    TEST_CASE("a local alarm is reported");
    payload.local_alarm = true;
    payload.alarm_causes = (uint32_t)ENV_ALARM_GAS_HIGH;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_is_well_formed(buffer, length));
    CHECK_TRUE(test_json_has_number(buffer, "localAlarm", "true"));
    CHECK_TRUE(strstr(buffer, "\"alarmCauses\":[\"gas_high\"]") != NULL);

    TEST_CASE("a disconnected device reports reconnecting");
    payload.network_online = false;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_string(buffer, "network", "reconnecting"));

    TEST_CASE("a calibrated sensor is reported as calibrated");
    payload.gas_calibrated = true;
    payload.sensor_fault = true;
    length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_number(buffer, "gasCalibrated", "true"));
    CHECK_TRUE(test_json_has_number(buffer, "sensorFault", "true"));
}

/* Exercise the encoder's bounds and argument checks. */
static void test_bounds(void)
{
    char small[32];
    char buffer[512];
    TelemetryPayload payload = sample_payload();

    TEST_CASE("a buffer that is too small is refused rather than truncated");
    CHECK_INT(0U, TelemetryJsonEncode(&payload, small, sizeof(small)));

    TEST_CASE("a null payload or buffer is refused");
    CHECK_INT(0U, TelemetryJsonEncode(NULL, buffer, sizeof(buffer)));
    CHECK_INT(0U, TelemetryJsonEncode(&payload, NULL, sizeof(buffer)));
    CHECK_INT(0U, TelemetryJsonEncode(&payload, buffer, 0U));

    TEST_CASE("the encoded length matches the string written");
    {
        /* Split into two statements: the order in which the operands of a
         * comparison are evaluated is unspecified, so the length must be read
         * after the encode has run. */
        uint32_t length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));

        CHECK_INT((uint32_t)strlen(buffer), length);
    }

    TEST_CASE("the largest representable readings still fit the frame that carries them");
    payload.gas_adc_raw = 4095U;
    payload.gas_adc_filtered = 4095U;
    payload.gas_ppm_tenths = 9990U;
    payload.uptime_ms = 4294967295U;
    payload.sequence = 4294967295U;
    {
        uint32_t length = TelemetryJsonEncode(&payload, buffer, sizeof(buffer));

        CHECK_TRUE(length > 0U);
        /* The payload plus the MQTT fixed header and topic must fit the frame
         * the device is willing to send, or the largest legitimate reading would
         * be silently dropped at publish time. */
        CHECK_TRUE(length + 64U < MQTT_MAX_PACKET_SIZE);
        CHECK_TRUE(test_json_is_well_formed(buffer, length));
    }
}

/* Entry point for the telemetry_json suite. */
void test_telemetry_json_suite(void)
{
    test_required_fields();
    test_alarm_causes();
    test_state_flags();
    test_bounds();
}
