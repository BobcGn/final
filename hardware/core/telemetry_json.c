#include "json_writer.h"
#include "telemetry_json.h"
#include "text_format.h"

/* The alarm cause strings, in the frozen order of docs/device-protocol.md. The
 * bit positions match EnvAlarmCause, so the mapping is a direct lookup rather
 * than a translation table that could drift. */
static const char *const CAUSE_NAMES[TELEMETRY_CAUSE_COUNT] = {
    "temperature_high",
    "humidity_high",
    "gas_high",
    "rapid_temperature_rise",
    "rapid_gas_rise",
    "sensor_fault"
};

/* Write the alarmCauses array from a bitmask. An empty alarm still writes an
 * empty array rather than null: the contract types the field as an array, and a
 * client that had to handle both would treat them differently by accident. */
static void write_causes(JsonWriter *writer, uint32_t causes)
{
    uint32_t index;
    bool first = true;

    JsonWriterRaw(writer, "[");
    for (index = 0U; index < TELEMETRY_CAUSE_COUNT; index++)
    {
        if ((causes & (1UL << index)) == 0U)
        {
            continue;
        }
        if (!first)
        {
            JsonWriterRaw(writer, ",");
        }
        JsonWriterString(writer, CAUSE_NAMES[index]);
        first = false;
    }
    JsonWriterRaw(writer, "]");
}

uint32_t TelemetryJsonEncode(const TelemetryPayload *payload, char *buffer, uint32_t capacity)
{
    JsonWriter writer;

    if (payload == NULL || buffer == NULL || capacity == 0U)
    {
        return 0U;
    }

    JsonWriterInit(&writer, buffer, capacity);

    JsonWriterRaw(&writer, "{");
    JsonWriterKey(&writer, "schemaVersion");
    JsonWriterUnsigned(&writer, TELEMETRY_SCHEMA_VERSION);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "messageType");
    JsonWriterString(&writer, "telemetry");
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "deviceId");
    JsonWriterString(&writer, payload->device_id);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "bootId");
    JsonWriterString(&writer, payload->boot_id);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "sequence");
    JsonWriterUnsigned(&writer, payload->sequence);
    JsonWriterRaw(&writer, ",");

    /* The device has no time source that is known to be correct, so it reports
     * null rather than a plausible-looking number. The contract requires
     * uptimeMs alongside it precisely for this case, and the backend records its
     * own receive time. Sending a fabricated epoch would place every sample at
     * the wrong point of every time-ordered query. */
    JsonWriterKey(&writer, "timestamp");
    JsonWriterNull(&writer);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "uptimeMs");
    JsonWriterUnsigned(&writer, payload->uptime_ms);
    JsonWriterRaw(&writer, ",");

    JsonWriterKey(&writer, "temperatureC");
    JsonWriterUnsigned(&writer, payload->temperature_c);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "humidityRh");
    JsonWriterUnsigned(&writer, payload->humidity_rh);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "gasAdcRaw");
    JsonWriterUnsigned(&writer, payload->gas_adc_raw);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "gasAdcFiltered");
    JsonWriterUnsigned(&writer, payload->gas_adc_filtered);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "gasPpm");
    JsonWriterScaled1(&writer, payload->gas_ppm_tenths);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "gasCalibrated");
    JsonWriterBool(&writer, payload->gas_calibrated);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "localAlarm");
    JsonWriterBool(&writer, payload->local_alarm);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "alarmCauses");
    write_causes(&writer, payload->alarm_causes);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "network");
    JsonWriterString(&writer, payload->network_online ? "online" : "reconnecting");
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "thresholdVersion");
    JsonWriterUnsigned(&writer, payload->threshold_version);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "sensorFault");
    JsonWriterBool(&writer, payload->sensor_fault);
    JsonWriterRaw(&writer, "}");

    if (!JsonWriterOk(&writer))
    {
        return 0U;
    }
    return JsonWriterLength(&writer);
}
