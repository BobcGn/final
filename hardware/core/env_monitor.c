#include "env_monitor.h"

/* The unsigned threshold fields cannot hold a negative value, so the lower
 * bounds of zero are enforced by the type rather than by a comparison. If a
 * non-zero minimum is ever introduced these fail to compile, which is the
 * reminder to add the corresponding run-time check. */
_Static_assert(ENV_MIN_TEMPERATURE_HIGH_C == 0U, "a non-zero temperature minimum needs a run-time check in EnvMonitorSetThresholds");
_Static_assert(ENV_MIN_HUMIDITY_HIGH_RH == 0U, "a non-zero humidity minimum needs a run-time check in EnvMonitorSetThresholds");

/* Minimum history span before rise detection is allowed to fire.
 *
 * The first readings after power-up are not a trend: the DHT11 needs a moment
 * to stabilise and the MQ135 heater is still warming, so a rise measured across
 * two consecutive samples minutes-old-but-seconds-apart would be an artefact
 * rather than an event. Ten seconds is short enough that a genuine fast fire is
 * still caught with the buzzer sounding well before the backend notices. */
#define ENV_RISE_MIN_SPAN_MS 10000U

/* Zero an entry so a stale slot is never mistaken for a reading. */
static void env_history_clear(EnvHistoryEntry *entry)
{
    entry->timestamp_ms = 0U;
    entry->temperature_c = 0U;
    entry->gas_adc = 0U;
    entry->valid = false;
}

/* Return true when `now_ms` is at or after `timestamp_ms`, accounting for the
 * 32-bit millisecond counter wrapping about every 49.7 days.
 *
 * Unsigned subtraction is the wrap-safe form: it is correct as long as the
 * elapsed time is below 2^31 ms, roughly 24 days. */
static bool env_time_reached(uint32_t now_ms, uint32_t timestamp_ms)
{
    return (uint32_t)(now_ms - timestamp_ms) < 0x80000000U;
}

/* Elapsed milliseconds from `then_ms` to `now_ms`, wrap-safe. */
static uint32_t env_elapsed_ms(uint32_t now_ms, uint32_t then_ms)
{
    return (uint32_t)(now_ms - then_ms);
}

void EnvGasFilterReset(EnvGasFilter *filter)
{
    uint8_t index;

    filter->sum = 0U;
    filter->count = 0U;
    filter->next = 0U;
    for (index = 0U; index < ENV_GAS_WINDOW_SIZE; index++)
    {
        filter->samples[index] = 0U;
    }
}

void EnvGasFilterPush(EnvGasFilter *filter, uint16_t raw_adc)
{
    /* Subtracting the evicted sample before adding the new one keeps the sum in
     * step with the window; recomputing it from scratch every push would cost
     * ENV_GAS_WINDOW_SIZE additions per sample on a 72 MHz core. */
    if (filter->count == ENV_GAS_WINDOW_SIZE)
    {
        filter->sum -= filter->samples[filter->next];
    }
    else
    {
        filter->count++;
    }

    filter->samples[filter->next] = raw_adc;
    filter->sum += raw_adc;
    filter->next = (uint8_t)((filter->next + 1U) % ENV_GAS_WINDOW_SIZE);
}

uint16_t EnvGasFilterValue(const EnvGasFilter *filter)
{
    if (filter->count == 0U)
    {
        return 0U;
    }
    return (uint16_t)(filter->sum / filter->count);
}

uint8_t EnvGasFilterCount(const EnvGasFilter *filter)
{
    return filter->count;
}

void EnvMonitorInit(EnvMonitor *monitor)
{
    uint8_t index;

    EnvGasFilterReset(&monitor->gas_filter);

    monitor->thresholds.temperature_high_c = ENV_DEFAULT_TEMPERATURE_HIGH_C;
    monitor->thresholds.humidity_high_rh = ENV_DEFAULT_HUMIDITY_HIGH_RH;
    monitor->thresholds.gas_high_ppm = ENV_DEFAULT_GAS_HIGH_PPM;
    monitor->thresholds.temperature_rise_c = ENV_DEFAULT_TEMPERATURE_RISE_C;
    monitor->thresholds.gas_rise_adc = ENV_DEFAULT_GAS_RISE_ADC;

    for (index = 0U; index < ENV_HISTORY_SIZE; index++)
    {
        env_history_clear(&monitor->history[index]);
    }
    monitor->history_count = 0U;
    monitor->history_next = 0U;

    monitor->gas_adc_latest = 0U;
    monitor->gas_ppm_latest = 0U;
    monitor->temperature_latest = 0U;
    monitor->humidity_latest = 0U;
    monitor->climate_valid = false;
    monitor->sensor_fault = false;

    monitor->previous_causes = ENV_ALARM_NONE;
    monitor->muted = false;
    monitor->threshold_version = ENV_INITIAL_THRESHOLD_VERSION;
}

bool EnvMonitorSetThresholds(EnvMonitor *monitor, const EnvThresholds *thresholds, uint32_t version)
{
    if (thresholds == NULL)
    {
        return false;
    }
    if (monitor == NULL)
    {
        return false;
    }

    /* Every field is checked before any is applied. A partially applied update
     * would leave the device enforcing a mixture of the old and new limits, and
     * the operator would have no way to tell which limit was actually in force
     * from the reported version. */
    /* Only the upper bounds are checked at run time. The lower bounds for the
     * temperature and humidity are zero and their fields are unsigned, so a
     * comparison against them is provably false; writing it anyway would add a
     * branch no test can reach. The assertions above fail to compile if a
     * non-zero minimum is ever introduced, which is the signal to add the check
     * back rather than to discover the gap on a bench. */
    if (thresholds->temperature_high_c > ENV_MAX_TEMPERATURE_HIGH_C)
    {
        return false;
    }
    if (thresholds->humidity_high_rh > ENV_MAX_HUMIDITY_HIGH_RH)
    {
        return false;
    }
    if ((uint32_t)thresholds->gas_high_ppm < (uint32_t)ENV_MIN_GAS_HIGH_PPM ||
        (uint32_t)thresholds->gas_high_ppm > (uint32_t)ENV_MAX_GAS_HIGH_PPM)
    {
        return false;
    }
    /* The rise thresholds were not part of the frozen control payload, so they
     * are validated here but cannot be set remotely yet. A zero rise threshold
     * would alarm constantly, so it is rejected rather than accepted as "most
     * sensitive". */
    if (thresholds->temperature_rise_c == 0U)
    {
        return false;
    }
    if (thresholds->gas_rise_adc == 0U)
    {
        return false;
    }
    if (version < ENV_INITIAL_THRESHOLD_VERSION)
    {
        return false;
    }

    monitor->thresholds = *thresholds;
    monitor->threshold_version = version;
    /* Updating thresholds establishes a new baseline for alarm evaluation.
     * Reset any prior mute and clear previous causes so that if the current
     * environment violates the newly configured limits, the alarm is evaluated
     * fresh as a new cause and the buzzer is immediately active. */
    monitor->muted = false;
    monitor->previous_causes = ENV_ALARM_NONE;
    return true;
}

void EnvMonitorSetMuted(EnvMonitor *monitor, bool muted)
{
    monitor->muted = muted;
}

bool EnvMonitorMuted(const EnvMonitor *monitor)
{
    return monitor->muted;
}

uint32_t EnvMonitorThresholdVersion(const EnvMonitor *monitor)
{
    return monitor->threshold_version;
}

void EnvMonitorThresholds(const EnvMonitor *monitor, EnvThresholds *out)
{
    if (out != NULL)
    {
        *out = monitor->thresholds;
    }
}

void EnvMonitorPushGas(EnvMonitor *monitor, uint16_t raw_adc)
{
    EnvGasFilterPush(&monitor->gas_filter, raw_adc);
    monitor->gas_adc_latest = raw_adc;
}

uint16_t EnvMonitorGasFiltered(const EnvMonitor *monitor)
{
    return EnvGasFilterValue(&monitor->gas_filter);
}

void EnvMonitorSetGasEstimate(EnvMonitor *monitor, uint16_t gas_ppm)
{
    monitor->gas_ppm_latest = gas_ppm;
}

void EnvMonitorPushClimate(EnvMonitor *monitor, uint8_t temperature_c, uint8_t humidity_rh, uint8_t dht_ok, uint32_t now_ms)
{
    EnvHistoryEntry *entry;

    if (dht_ok == 0U)
    {
        /* A failed read keeps the last valid readings and raises the fault
         * cause. Substituting zero would look like a very cold, very dry room
         * and could hide a real gas alarm behind an apparently calm sample. */
        monitor->sensor_fault = true;
        return;
    }

    monitor->sensor_fault = false;
    monitor->temperature_latest = temperature_c;
    monitor->humidity_latest = humidity_rh;
    monitor->climate_valid = true;

    entry = &monitor->history[monitor->history_next];
    entry->timestamp_ms = now_ms;
    entry->temperature_c = temperature_c;
    entry->gas_adc = EnvGasFilterValue(&monitor->gas_filter);
    entry->valid = true;

    monitor->history_next = (uint8_t)((monitor->history_next + 1U) % ENV_HISTORY_SIZE);
    if (monitor->history_count < ENV_HISTORY_SIZE)
    {
        monitor->history_count++;
    }
}

/* Return the oldest history entry still inside the rapid window, or NULL when
 * the history cannot support a rise measurement yet.
 *
 * The oldest entry inside the window is the right baseline: comparing against
 * the newest gives the rise over the whole window, which is what "rapid" means
 * here. Requiring a minimum span is what stops two power-on readings from
 * looking like a fast fire. */
static const EnvHistoryEntry *env_rise_baseline(const EnvMonitor *monitor, uint32_t now_ms)
{
    const EnvHistoryEntry *oldest = NULL;
    uint8_t index;

    for (index = 0U; index < ENV_HISTORY_SIZE; index++)
    {
        const EnvHistoryEntry *entry = &monitor->history[index];

        if (!entry->valid)
        {
            continue;
        }
        if (!env_time_reached(now_ms, entry->timestamp_ms))
        {
            /* Timestamp is in the future relative to now, which happens only
             * while a counter wraps; skip it rather than treating it as the
             * baseline. */
            continue;
        }
        if (env_elapsed_ms(now_ms, entry->timestamp_ms) > ENV_RAPID_WINDOW_MS)
        {
            continue;
        }
        if (oldest == NULL || !env_time_reached(entry->timestamp_ms, oldest->timestamp_ms))
        {
            oldest = entry;
        }
    }

    if (oldest == NULL)
    {
        return NULL;
    }
    if (env_elapsed_ms(now_ms, oldest->timestamp_ms) < ENV_RISE_MIN_SPAN_MS)
    {
        return NULL;
    }
    return oldest;
}

EnvEvaluation EnvMonitorEvaluate(EnvMonitor *monitor, uint32_t now_ms)
{
    EnvEvaluation result;
    const EnvHistoryEntry *baseline;
    uint32_t causes = ENV_ALARM_NONE;
    const EnvThresholds *thresholds = &monitor->thresholds;

    result.gas_adc_raw = monitor->gas_adc_latest;
    result.gas_adc_filtered = EnvGasFilterValue(&monitor->gas_filter);
    result.gas_ppm = monitor->gas_ppm_latest;
    result.temperature_c = monitor->temperature_latest;
    result.humidity_rh = monitor->humidity_latest;

    if (monitor->sensor_fault)
    {
        causes |= (uint32_t)ENV_ALARM_SENSOR_FAULT;
    }

    /* Absolute thresholds only apply to a valid climate reading: with a fault
     * the readings are the last known ones, and re-alarming on them every cycle
     * would be reporting a stale condition as current. */
    if (monitor->climate_valid && !monitor->sensor_fault)
    {
        if (monitor->temperature_latest >= thresholds->temperature_high_c)
        {
            causes |= (uint32_t)ENV_ALARM_TEMPERATURE_HIGH;
        }
        if (monitor->humidity_latest >= thresholds->humidity_high_rh)
        {
            causes |= (uint32_t)ENV_ALARM_HUMIDITY_HIGH;
        }
    }

    /* The gas threshold is compared against the filtered ppm estimate, which is
     * the same quantity the backend receives as `gasPpm`. */
    if (monitor->gas_ppm_latest >= thresholds->gas_high_ppm)
    {
        causes |= (uint32_t)ENV_ALARM_GAS_HIGH;
    }

    baseline = env_rise_baseline(monitor, now_ms);
    if (baseline != NULL)
    {
        if (monitor->temperature_latest >= baseline->temperature_c &&
            (uint16_t)(monitor->temperature_latest - baseline->temperature_c) >= thresholds->temperature_rise_c)
        {
            causes |= (uint32_t)ENV_ALARM_RAPID_TEMPERATURE_RISE;
        }

        /* Compared against the filtered ADC from the baseline's own reading, so
         * the rise is measured filter-to-filter rather than raw-to-filtered. */
        if (result.gas_adc_filtered >= baseline->gas_adc &&
            (uint16_t)(result.gas_adc_filtered - baseline->gas_adc) >= thresholds->gas_rise_adc)
        {
            causes |= (uint32_t)ENV_ALARM_RAPID_GAS_RISE;
        }
    }

    /* A cause that was not present before is a new event, and a new event
     * cancels a previous mute so that the buzzer sounds. Without this rule a
     * mute issued for one episode would silently suppress the next one, which is
     * the failure mode that makes remote mute dangerous.
     *
     * docs/implementation-plan.md §4.3 records this as a product rule that needs
     * confirmation; it is implemented fail-safe — towards sounding — until then. */
    result.new_cause = (causes & ~monitor->previous_causes) != 0U;
    if (result.new_cause)
    {
        monitor->muted = false;
    }
    monitor->previous_causes = causes;

    result.alarm_causes = causes;
    result.local_alarm = causes != ENV_ALARM_NONE;

    /* The buzzer is the only thing mute affects. The LED, the OLED indication
     * and the telemetry flag all follow local_alarm, because a mute that also
     * hid the alarm would remove the operator's ability to see it locally. */
    result.buzzer_on = result.local_alarm && !monitor->muted;
    return result;
}

bool EnvAlarmHas(uint32_t causes, EnvAlarmCause cause)
{
    return (causes & (uint32_t)cause) != 0U;
}

bool EnvMonitorBuzzerDrive(const EnvEvaluation *evaluation, uint32_t tick)
{
    uint32_t audible_causes;

    if (evaluation == NULL)
    {
        return false;
    }
    /* `buzzer_on` is false whenever the device is muted, so the mute is checked
     * through the evaluation rather than against the monitor: there is then one
     * place a mute can be lost, and it is the one the mute tests exercise. */
    if (!evaluation->buzzer_on)
    {
        return false;
    }

    audible_causes = evaluation->alarm_causes &
                     (uint32_t)(ENV_ALARM_GAS_HIGH | ENV_ALARM_RAPID_GAS_RISE);
    if (audible_causes == 0U)
    {
        return false;
    }

    return (tick % GAS_BUZZER_PERIOD_TICKS) < GAS_BUZZER_ON_TICKS;
}
