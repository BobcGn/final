#ifndef __TELEMETRY_JSON_H
#define __TELEMETRY_JSON_H

/*
 * Device telemetry payload builder, following docs/device-protocol.md §3.
 *
 * The payload is built into a caller-owned buffer with no allocation. Every
 * field the frozen contract marks required is always written, including the ones
 * that are null, because a client parsing the payload should not have to infer a
 * missing field from its absence.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* The only schema version this firmware emits. */
#define TELEMETRY_SCHEMA_VERSION 1U

/* Number of alarm cause bits, matching EnvAlarmCause. */
#define TELEMETRY_CAUSE_COUNT 6U

/* Everything the payload carries.
 *
 * The gas estimate is in tenths of a ppm so the payload stays integer-only: the
 * firmware has no floating-point formatter, and the value already comes from a
 * float conversion that rounds to a whole ppm. Tenths keeps one decimal of
 * resolution for the display without pulling float printing into the device. */
typedef struct
{
    const char *device_id;
    /* Generated once per power cycle. It is what makes (deviceId, bootId,
     * sequence) unique across restarts, because the sequence restarts at zero. */
    const char *boot_id;
    uint32_t sequence;
    uint32_t uptime_ms;
    uint8_t temperature_c;
    uint8_t humidity_rh;
    uint16_t gas_adc_raw;
    uint16_t gas_adc_filtered;
    /* Gas estimate in tenths of a ppm, so 250 means 25.0 ppm. */
    uint16_t gas_ppm_tenths;
    bool gas_calibrated;
    bool local_alarm;
    /* Bitmask of EnvAlarmCause. */
    uint32_t alarm_causes;
    bool network_online;
    uint32_t threshold_version;
    bool sensor_fault;
} TelemetryPayload;

/* Write the payload into `buffer` and return its length, or 0 when it does not
 * fit or an argument is invalid.
 *
 * The returned length excludes the NUL terminator, which is written for
 * debugging convenience; the MQTT frame carries only the first `length` bytes. */
uint32_t TelemetryJsonEncode(const TelemetryPayload *payload, char *buffer, uint32_t capacity);

#endif /* __TELEMETRY_JSON_H */
