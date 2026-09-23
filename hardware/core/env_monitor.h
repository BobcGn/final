#ifndef __ENV_MONITOR_H
#define __ENV_MONITOR_H

/*
 * Pure local monitoring logic: gas filtering, threshold evaluation, rapid-rise
 * detection and the buzzer mute state.
 *
 * This module is deliberately free of any STM32, GPIO or driver dependency so
 * that every decision it makes can be tested on a host. It receives numbers and
 * returns decisions; sampling, display and networking stay outside. That split
 * is what makes the safety-critical branch coverage required by hardware/AGENTS.md
 * achievable without a board on the bench.
 *
 * Units, and where they come from:
 *   - gas_raw / gas_filtered are 12-bit ADC codes, 0..4095, from PA1 (MQ135).
 *   - gas_ppm is the uncalibrated estimate produced by MQ135_GetData().
 *   - temperature is whole degrees Celsius and humidity whole %RH, which is all
 *     the DHT11 resolves.
 *
 * The absolute gas threshold is expressed in ppm to match the frozen device
 * contract (docs/device-protocol.md, `gasHighPpm`). The *rise* thresholds are
 * expressed in ADC codes because a rise is a difference: it stays meaningful
 * while the sensor is uncalibrated, and the ADC value is always available.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* The compile-time device configuration is the single source of the default
 * thresholds. app_config.h is plain macros with no platform dependency, so it
 * compiles on the host as well and the host tests exercise the same numbers the
 * board uses. Changing a limit there changes both. */
#include "app_config.h"

/* Fixed filter window. Ten samples at 100 ms cadence smooths the ADC noise
 * without delaying a real gas event by more than about one second. */
#define ENV_GAS_WINDOW_SIZE 10U

/* Climate history depth. One entry per DHT11 reading, so 64 entries cover more
 * than the rapid-rise window below at the 1 s device cadence. */
#define ENV_HISTORY_SIZE 64U

/* Window over which a rise counts as "rapid". */
#define ENV_RAPID_WINDOW_MS 60000U

/* Default thresholds, taken from app_config.h rather than repeated here. The
 * backend reports this set as threshold version 1. */
#define ENV_DEFAULT_TEMPERATURE_HIGH_C ((uint8_t)TEMP_HIGH_THRESHOLD_C)
#define ENV_DEFAULT_HUMIDITY_HIGH_RH ((uint8_t)HUMIDITY_HIGH_THRESHOLD_RH)
#define ENV_DEFAULT_GAS_HIGH_PPM ((uint16_t)GAS_HIGH_THRESHOLD_PPM)

/* Rise thresholds. The temperature rise is in whole degrees over the rapid
 * window; the gas rise is in ADC codes over the same window. */
#define ENV_DEFAULT_TEMPERATURE_RISE_C ((uint8_t)TEMP_RISE_THRESHOLD_C)
#define ENV_DEFAULT_GAS_RISE_ADC ((uint16_t)GAS_RISE_THRESHOLD_ADC)

/* Threshold version 1 is the compile-time default, matching the frozen
 * contract: a device never reports version 0. */
#define ENV_INITIAL_THRESHOLD_VERSION 1U

/* Accepted threshold ranges. They must stay compatible with the backend's
 * `ThresholdUpdate` bounds, or the device would reject commands the backend
 * accepted. */
#define ENV_MIN_TEMPERATURE_HIGH_C 0U
#define ENV_MAX_TEMPERATURE_HIGH_C 80U
#define ENV_MIN_HUMIDITY_HIGH_RH 0U
#define ENV_MAX_HUMIDITY_HIGH_RH 100U
#define ENV_MIN_GAS_HIGH_PPM 1U
#define ENV_MAX_GAS_HIGH_PPM 999U

/* A default outside the accepted range would make the device reject the very
 * configuration it boots with, so the relationship is checked at compile time
 * rather than discovered on the bench. */
_Static_assert(TEMP_HIGH_THRESHOLD_C <= 80U, "the default temperature limit must be inside the accepted range");
_Static_assert(HUMIDITY_HIGH_THRESHOLD_RH <= 100U, "the default humidity limit must be inside the accepted range");
_Static_assert(GAS_HIGH_THRESHOLD_PPM >= 1U && GAS_HIGH_THRESHOLD_PPM <= 999U, "the default gas limit must be inside the accepted range");
_Static_assert(TEMP_RISE_THRESHOLD_C > 0U, "a zero temperature rise threshold would alarm continuously");
_Static_assert(GAS_RISE_THRESHOLD_ADC > 0U, "a zero gas rise threshold would alarm continuously");
/* The buzzer cadence divides by its period, so a zero period would be a division
 * by zero on the device, and an on-time at least as long as the period would
 * turn the intermittent warning into a continuous one. */
_Static_assert(GAS_BUZZER_PERIOD_TICKS > 0U, "a zero buzzer period would divide by zero");
_Static_assert(GAS_BUZZER_ON_TICKS < GAS_BUZZER_PERIOD_TICKS, "the buzzer would sound continuously");

/* Alarm causes. The values are a bitmask and the names match the frozen
 * `alarmCauses` strings one for one, so the payload encoder is a direct mapping
 * rather than a translation table that could drift. */
typedef enum
{
    ENV_ALARM_NONE = 0U,
    ENV_ALARM_TEMPERATURE_HIGH = 1U << 0,
    ENV_ALARM_HUMIDITY_HIGH = 1U << 1,
    ENV_ALARM_GAS_HIGH = 1U << 2,
    ENV_ALARM_RAPID_TEMPERATURE_RISE = 1U << 3,
    ENV_ALARM_RAPID_GAS_RISE = 1U << 4,
    ENV_ALARM_SENSOR_FAULT = 1U << 5
} EnvAlarmCause;

/* Thresholds in force on the device. */
typedef struct
{
    uint8_t temperature_high_c;
    uint8_t humidity_high_rh;
    uint16_t gas_high_ppm;
    uint8_t temperature_rise_c;
    uint16_t gas_rise_adc;
} EnvThresholds;

/* Fixed-window mean over raw ADC samples.
 *
 * A mean over a fixed number of samples is enough here: the MQ135 output is
 * already slow, and the host tests can assert the exact value, which a
 * floating-point or exponentially weighted filter would make harder to pin
 * down. The window is a ring buffer with a running sum, so adding a sample is
 * O(1) and involves no division until the value is read. */
typedef struct
{
    uint16_t samples[ENV_GAS_WINDOW_SIZE];
    uint32_t sum;
    uint8_t count;
    uint8_t next;
} EnvGasFilter;

/* One climate history entry, used only for rise detection. */
typedef struct
{
    uint32_t timestamp_ms;
    uint8_t temperature_c;
    uint16_t gas_adc;
    bool valid;
} EnvHistoryEntry;

/* Result of one evaluation. */
typedef struct
{
    /* Bitmask of EnvAlarmCause. Zero means no local alarm. */
    uint32_t alarm_causes;
    /* True when any cause is set. */
    bool local_alarm;
    /* True when the buzzer must sound: an alarm is active and not muted. */
    bool buzzer_on;
    /* True when a cause appeared that was not present in the previous
     * evaluation, which is what clears a mute. */
    bool new_cause;
    uint16_t gas_adc_raw;
    uint16_t gas_adc_filtered;
    uint16_t gas_ppm;
    uint8_t temperature_c;
    uint8_t humidity_rh;
} EnvEvaluation;

/* Complete monitor state. It holds no pointers and allocates nothing, so it can
 * live as a single static object in the firmware. */
typedef struct
{
    EnvGasFilter gas_filter;
    EnvThresholds thresholds;
    EnvHistoryEntry history[ENV_HISTORY_SIZE];
    uint8_t history_count;
    uint8_t history_next;

    uint16_t gas_adc_latest;
    uint16_t gas_ppm_latest;
    uint8_t temperature_latest;
    uint8_t humidity_latest;
    bool climate_valid;
    bool sensor_fault;

    /* Causes from the previous evaluation, used to detect a new cause. */
    uint32_t previous_causes;
    bool muted;
    uint32_t threshold_version;
} EnvMonitor;

/* Initialise the monitor with the compile-time default thresholds.
 *
 * `muted` starts false: a device that has just powered up must be able to
 * sound its buzzer, because silence after a reboot is indistinguishable from a
 * failed alarm. */
void EnvMonitorInit(EnvMonitor *monitor);

/* Replace the thresholds. Values outside the accepted ranges are rejected as a
 * whole and the previous thresholds stay in force, so a partly valid update
 * cannot leave the device enforcing a mixture. Returns true when applied.
 *
 * `version` is recorded only on success. The caller is responsible for having
 * durably stored the same values before calling this, so that the reported
 * version never claims a configuration the device would lose on reset. */
bool EnvMonitorSetThresholds(EnvMonitor *monitor, const EnvThresholds *thresholds, uint32_t version);

/* Set the buzzer mute state. Muting never changes the alarm causes, the LED or
 * the reported state: it suppresses the buzzer only. */
void EnvMonitorSetMuted(EnvMonitor *monitor, bool muted);

/* Report the mute state. */
bool EnvMonitorMuted(const EnvMonitor *monitor);

/* Return the threshold version currently in force. */
uint32_t EnvMonitorThresholdVersion(const EnvMonitor *monitor);

/* Copy the thresholds in force. */
void EnvMonitorThresholds(const EnvMonitor *monitor, EnvThresholds *out);

/* Push one raw ADC sample into the gas filter. Called every 100 ms by the
 * sampling loop. */
void EnvGasFilterPush(EnvGasFilter *filter, uint16_t raw_adc);

/* Return the current window mean, or 0 before the first sample.
 *
 * The mean is over the samples seen so far, so the value is usable during the
 * first second rather than reporting zero until the window fills. */
uint16_t EnvGasFilterValue(const EnvGasFilter *filter);

/* Report how many samples the window holds, 0..ENV_GAS_WINDOW_SIZE. */
uint8_t EnvGasFilterCount(const EnvGasFilter *filter);

/* Reset the window, for example after a detected sensor fault. */
void EnvGasFilterReset(EnvGasFilter *filter);

/* Push one raw ADC sample into the gas filter.
 *
 * The ppm estimate is set separately, with EnvMonitorSetGasEstimate, because it
 * is derived from the *filtered* value rather than from the raw one: the payload
 * reports both, and an estimate computed from a different sample than the
 * filtered value it accompanies would make the alarm and the reported number
 * disagree. */
void EnvMonitorPushGas(EnvMonitor *monitor, uint16_t raw_adc);

/* Return the current filtered gas value. */
uint16_t EnvMonitorGasFiltered(const EnvMonitor *monitor);

/* Set the ppm estimate that accompanies the current filtered gas value.
 *
 * The estimate is a property of the filtered value, not of the raw sample, so a
 * caller that never sets it reports no ppm; that is the honest state for a
 * device whose calibration step has not run. */
void EnvMonitorSetGasEstimate(EnvMonitor *monitor, uint16_t gas_ppm);

/* Push one climate reading.
 *
 * `dht_ok` is zero when the DHT11 read failed. A failed read raises the sensor
 * fault cause and keeps the last valid readings, because zeroing them would
 * look like a very cold, very dry room and could mask a real gas alarm behind a
 * false "all clear". */
void EnvMonitorPushClimate(EnvMonitor *monitor, uint8_t temperature_c, uint8_t humidity_rh, uint8_t dht_ok, uint32_t now_ms);

/* Evaluate the thresholds and rise windows and return the verdict.
 *
 * Calling this repeatedly with no new data is safe: the verdict is derived from
 * the state, not accumulated. */
EnvEvaluation EnvMonitorEvaluate(EnvMonitor *monitor, uint32_t now_ms);

/* Report whether a cause bit is set in a cause mask. */
bool EnvAlarmHas(uint32_t causes, EnvAlarmCause cause);

/* Decide whether the buzzer must be driven on this main-loop tick.
 *
 * The rule has three parts and all three are safety-relevant, which is why it
 * lives here rather than in the loop where it could only be checked by watching
 * a board:
 *
 *   1. only gas-related causes are audible. Temperature, humidity and sensor
 *      faults keep their LED, OLED and telemetry causes but stay silent, because
 *      the audible warning is reserved for the failure that needs someone in the
 *      room immediately.
 *   2. while such a cause is active and not muted, the buzzer sounds for
 *      GAS_BUZZER_ON_TICKS of every GAS_BUZZER_PERIOD_TICKS loop ticks. At the
 *      firmware's 100 ms tick that is 200 ms on and 800 ms off, which is
 *      unmistakable without being a continuous tone.
 *   3. a mute suppresses the output entirely, and `evaluation.buzzer_on` is what
 *      carries that, so a mute can never be bypassed by the cadence.
 *
 * `tick` is the main-loop counter, which is why the cadence is expressed in
 * ticks: the loop's period is nominal and a millisecond timer would suggest a
 * precision the busy-wait loop does not have. */
bool EnvMonitorBuzzerDrive(const EnvEvaluation *evaluation, uint32_t tick);

#endif /* __ENV_MONITOR_H */
