#include "env_monitor.h"
#include "test_support.h"

/* Feed a number of ADC samples with the same value. */
static void push_gas(EnvMonitor *monitor, uint16_t raw_adc, uint16_t gas_ppm, unsigned count)
{
    unsigned index;

    for (index = 0U; index < count; index++)
    {
        EnvMonitorPushGas(monitor, raw_adc);
    }
    /* The estimate belongs to the filtered value, so it is set once the window
     * has taken the sample. */
    EnvMonitorSetGasEstimate(monitor, gas_ppm);
}

/* Fill the rise history with a constant climate reading so that subsequent
 * changes are measured against a known baseline.
 *
 * The device reads the DHT11 once per second, so 30 entries span 29 seconds,
 * which is past the minimum span rise detection requires. */
static void settle_climate(EnvMonitor *monitor, uint8_t temperature_c, uint8_t humidity_rh, uint32_t *now_ms)
{
    unsigned index;

    for (index = 0U; index < 30U; index++)
    {
        *now_ms += 1000U;
        EnvMonitorPushClimate(monitor, temperature_c, humidity_rh, 1U, *now_ms);
    }
}

/* Exercise the fixed-window mean. */
static void test_gas_filter(void)
{
    EnvGasFilter filter;
    int index;

    TEST_CASE("value is zero before the first sample");
    EnvGasFilterReset(&filter);
    CHECK_INT(0U, EnvGasFilterValue(&filter));
    CHECK_INT(0U, EnvGasFilterCount(&filter));

    TEST_CASE("a partial window averages only the samples seen");
    EnvGasFilterPush(&filter, 1000U);
    EnvGasFilterPush(&filter, 1200U);
    CHECK_INT(2U, EnvGasFilterCount(&filter));
    /* 1100 exactly: the mean must be usable during the first second rather
     * than reporting zero until the window fills. */
    CHECK_INT(1100U, EnvGasFilterValue(&filter));

    TEST_CASE("a full window averages all of its samples");
    EnvGasFilterReset(&filter);
    for (index = 0; index < (int)ENV_GAS_WINDOW_SIZE; index++)
    {
        EnvGasFilterPush(&filter, 1000U);
    }
    CHECK_INT(ENV_GAS_WINDOW_SIZE, EnvGasFilterCount(&filter));
    CHECK_INT(1000U, EnvGasFilterValue(&filter));

    TEST_CASE("a new sample evicts the oldest");
    EnvGasFilterReset(&filter);
    for (index = 0; index < (int)ENV_GAS_WINDOW_SIZE; index++)
    {
        EnvGasFilterPush(&filter, 1000U);
    }
    /* Nine samples at 1000 and one at 2000 averages 1100. */
    EnvGasFilterPush(&filter, 2000U);
    CHECK_INT(1100U, EnvGasFilterValue(&filter));
    /* A second 2000 evicts the only remaining 1000: eight at 1000, two at
     * 2000 averages 1200. */
    EnvGasFilterPush(&filter, 2000U);
    CHECK_INT(1200U, EnvGasFilterValue(&filter));
    CHECK_INT(ENV_GAS_WINDOW_SIZE, EnvGasFilterCount(&filter));

    TEST_CASE("the running sum stays correct across many window turns");
    EnvGasFilterReset(&filter);
    for (index = 0; index < 500; index++)
    {
        EnvGasFilterPush(&filter, (uint16_t)(2000 + (index % 2)));
    }
    /* The last ten samples alternate 2000 and 2001; the mean is 2000 or 2001
     * depending on which one the window ends on, and must never drift. */
    CHECK_MSG(EnvGasFilterValue(&filter) == 2000U || EnvGasFilterValue(&filter) == 2001U,
              "mean drifted to %u", (unsigned)EnvGasFilterValue(&filter));

    TEST_CASE("reset clears the window");
    EnvGasFilterReset(&filter);
    CHECK_INT(0U, EnvGasFilterValue(&filter));
    CHECK_INT(0U, EnvGasFilterCount(&filter));
}

/* Exercise the default thresholds and the update rules. */
static void test_thresholds(void)
{
    EnvMonitor monitor;
    EnvThresholds thresholds;
    EnvThresholds applied;

    TEST_CASE("init uses the compile-time defaults at version 1");
    EnvMonitorInit(&monitor);
    EnvMonitorThresholds(&monitor, &applied);
    CHECK_INT(ENV_DEFAULT_TEMPERATURE_HIGH_C, applied.temperature_high_c);
    CHECK_INT(ENV_DEFAULT_HUMIDITY_HIGH_RH, applied.humidity_high_rh);
    CHECK_INT(ENV_DEFAULT_GAS_HIGH_PPM, applied.gas_high_ppm);
    CHECK_INT(ENV_DEFAULT_TEMPERATURE_RISE_C, applied.temperature_rise_c);
    CHECK_INT(ENV_DEFAULT_GAS_RISE_ADC, applied.gas_rise_adc);
    /* Version 1 means "the compile-time default", so a device never reports 0. */
    CHECK_INT(ENV_INITIAL_THRESHOLD_VERSION, EnvMonitorThresholdVersion(&monitor));
    CHECK_FALSE(EnvMonitorMuted(&monitor));

    TEST_CASE("a valid update is applied and its version recorded");
    thresholds.temperature_high_c = 35U;
    thresholds.humidity_high_rh = 85U;
    thresholds.gas_high_ppm = 120U;
    thresholds.temperature_rise_c = 4U;
    thresholds.gas_rise_adc = 200U;
    CHECK_TRUE(EnvMonitorSetThresholds(&monitor, &thresholds, 4U));
    EnvMonitorThresholds(&monitor, &applied);
    CHECK_INT(35U, applied.temperature_high_c);
    CHECK_INT(120U, applied.gas_high_ppm);
    CHECK_INT(4U, EnvMonitorThresholdVersion(&monitor));

    TEST_CASE("an out-of-range temperature is rejected as a whole");
    thresholds.temperature_high_c = ENV_MAX_TEMPERATURE_HIGH_C + 1U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 5U));
    EnvMonitorThresholds(&monitor, &applied);
    /* The previous limits stay in force, and the version does not move: a
     * partly applied update would leave the device enforcing a mixture the
     * reported version cannot describe. */
    CHECK_INT(35U, applied.temperature_high_c);
    CHECK_INT(120U, applied.gas_high_ppm);
    CHECK_INT(4U, EnvMonitorThresholdVersion(&monitor));

    TEST_CASE("an out-of-range humidity is rejected");
    thresholds.temperature_high_c = 35U;
    thresholds.humidity_high_rh = ENV_MAX_HUMIDITY_HIGH_RH + 1U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 5U));

    TEST_CASE("a gas threshold below the accepted minimum is rejected");
    thresholds.humidity_high_rh = 85U;
    thresholds.gas_high_ppm = ENV_MIN_GAS_HIGH_PPM - 1U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 5U));

    TEST_CASE("a gas threshold above the accepted maximum is rejected");
    thresholds.gas_high_ppm = ENV_MAX_GAS_HIGH_PPM + 1U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 5U));

    TEST_CASE("the accepted bounds themselves are accepted");
    thresholds.gas_high_ppm = ENV_MAX_GAS_HIGH_PPM;
    thresholds.temperature_high_c = ENV_MAX_TEMPERATURE_HIGH_C;
    thresholds.humidity_high_rh = ENV_MAX_HUMIDITY_HIGH_RH;
    CHECK_TRUE(EnvMonitorSetThresholds(&monitor, &thresholds, 6U));

    TEST_CASE("a zero rise threshold is rejected rather than treated as most sensitive");
    thresholds.temperature_high_c = 30U;
    thresholds.humidity_high_rh = 80U;
    thresholds.gas_high_ppm = 20U;
    thresholds.temperature_rise_c = 0U;
    thresholds.gas_rise_adc = 150U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 7U));
    thresholds.temperature_rise_c = 3U;
    thresholds.gas_rise_adc = 0U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 7U));

    TEST_CASE("a version below the initial one is rejected");
    thresholds.gas_rise_adc = 150U;
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, &thresholds, 0U));

    TEST_CASE("a null threshold pointer is rejected");
    CHECK_FALSE(EnvMonitorSetThresholds(&monitor, NULL, 8U));
}

/* Exercise the absolute thresholds, including their boundaries. */
static void test_absolute_alarms(void)
{
    EnvMonitor monitor;
    EnvEvaluation result;
    uint32_t now_ms = 0U;

    TEST_CASE("a quiet device reports no alarm");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_INT(ENV_ALARM_NONE, result.alarm_causes);
    CHECK_FALSE(result.local_alarm);
    CHECK_FALSE(result.buzzer_on);

    TEST_CASE("temperature exactly at the threshold alarms");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, ENV_DEFAULT_TEMPERATURE_HIGH_C, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_TEMPERATURE_HIGH));
    CHECK_TRUE(result.local_alarm);

    TEST_CASE("temperature one degree below the threshold does not alarm");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, (uint8_t)(ENV_DEFAULT_TEMPERATURE_HIGH_C - 1U), 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_TEMPERATURE_HIGH));

    TEST_CASE("humidity exactly at the threshold alarms");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, 25U, ENV_DEFAULT_HUMIDITY_HIGH_RH, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_HUMIDITY_HIGH));

    TEST_CASE("gas exactly at the threshold alarms");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, ENV_DEFAULT_GAS_HIGH_PPM);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));

    TEST_CASE("gas one ppm below the threshold does not alarm");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, (uint16_t)(ENV_DEFAULT_GAS_HIGH_PPM - 1U));
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));

    TEST_CASE("several simultaneous causes are all reported");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 100U);
    EnvMonitorPushClimate(&monitor, 40U, 90U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_TEMPERATURE_HIGH));
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_HUMIDITY_HIGH));
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));

    TEST_CASE("the gas alarm follows the filtered value");
    EnvMonitorInit(&monitor);
    /* Nine clean samples then one very high one: the mean is 1300, below the
     * 20 ppm equivalent only in ppm terms, so the ppm estimate drives the
     * alarm. The filtered ADC is what the report shows. */
    push_gas(&monitor, 1000U, 5U, 9U);
    EnvMonitorPushGas(&monitor, 4000U);
    EnvMonitorSetGasEstimate(&monitor, 900U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_INT(1300U, result.gas_adc_filtered);
    CHECK_INT(4000U, result.gas_adc_raw);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));
}

/* Exercise the temperature and gas rise windows. */
static void test_rapid_rise(void)
{
    EnvMonitor monitor;
    EnvEvaluation result;
    uint32_t now_ms;

    TEST_CASE("no rise is reported before the minimum span elapses");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 0U);
    /* Five seconds later the temperature has jumped far, but the history is too
     * short to call it a trend: this is the power-on window. */
    EnvMonitorPushClimate(&monitor, 40U, 50U, 1U, 5000U);
    result = EnvMonitorEvaluate(&monitor, 5000U);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));

    TEST_CASE("a temperature rise of exactly the threshold alarms");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    settle_climate(&monitor, 25U, 50U, &now_ms);
    EnvMonitorPushClimate(&monitor, (uint8_t)(25U + ENV_DEFAULT_TEMPERATURE_RISE_C), 50U, 1U, now_ms += 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));

    TEST_CASE("a temperature rise below the threshold does not alarm");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    settle_climate(&monitor, 25U, 50U, &now_ms);
    EnvMonitorPushClimate(&monitor, (uint8_t)(25U + ENV_DEFAULT_TEMPERATURE_RISE_C - 1U), 50U, 1U, now_ms += 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));

    TEST_CASE("a gas rise of exactly the threshold alarms");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    /* Settle the gas filter at 1000 so a filter-to-filter comparison is exact. */
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    settle_climate(&monitor, 25U, 50U, &now_ms);
    /* A step to 1000 + threshold keeps the filtered value at the step once the
     * window turns; push enough samples for the window to fully adopt it. */
    push_gas(&monitor, (uint16_t)(1000U + ENV_DEFAULT_GAS_RISE_ADC), 5U, ENV_GAS_WINDOW_SIZE);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms += 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_INT(1000U + ENV_DEFAULT_GAS_RISE_ADC, result.gas_adc_filtered);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_GAS_RISE));

    TEST_CASE("a gas rise below the threshold does not alarm");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    settle_climate(&monitor, 25U, 50U, &now_ms);
    push_gas(&monitor, (uint16_t)(1000U + ENV_DEFAULT_GAS_RISE_ADC - 1U), 5U, ENV_GAS_WINDOW_SIZE);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms += 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_GAS_RISE));

    TEST_CASE("a rise older than the window no longer alarms");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    settle_climate(&monitor, 25U, 50U, &now_ms);
    EnvMonitorPushClimate(&monitor, 40U, 50U, 1U, now_ms += 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));
    /* Past the rapid window the baseline moves forward, so the same absolute
     * temperature is no longer a rise. Without this the alarm could never
     * clear while the room stayed warm. */
    now_ms += ENV_RAPID_WINDOW_MS + 1000U;
    EnvMonitorPushClimate(&monitor, 40U, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));

    TEST_CASE("a negative change is not reported as a rise");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    settle_climate(&monitor, 40U, 50U, &now_ms);
    EnvMonitorPushClimate(&monitor, 20U, 50U, 1U, now_ms += 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));
    /* The absolute threshold must clear too: this is a cooling room. */
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_TEMPERATURE_HIGH));
}

/* Exercise sensor fault handling. */
static void test_sensor_fault(void)
{
    EnvMonitor monitor;
    EnvEvaluation result;
    uint32_t now_ms = 0U;

    TEST_CASE("a failed read raises the fault cause");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms);
    EnvMonitorPushClimate(&monitor, 0U, 0U, 0U, now_ms + 1000U);
    result = EnvMonitorEvaluate(&monitor, now_ms + 1000U);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_SENSOR_FAULT));
    CHECK_TRUE(result.local_alarm);

    TEST_CASE("a failed read keeps the last valid readings instead of zeroing them");
    CHECK_INT(25U, result.temperature_c);
    CHECK_INT(50U, result.humidity_rh);

    TEST_CASE("stale readings do not keep the absolute alarms asserted");
    /* The device reported 25 C before the fault, so neither the temperature nor
     * the humidity threshold is met; only the fault cause should be present. A
     * device that re-alarmed on the last known reading every cycle would report a
     * condition it can no longer observe. */
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_TEMPERATURE_HIGH));
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_HUMIDITY_HIGH));

    TEST_CASE("a recovered read clears the fault cause");
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms + 2000U);
    result = EnvMonitorEvaluate(&monitor, now_ms + 2000U);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_SENSOR_FAULT));
    CHECK_FALSE(result.local_alarm);

    TEST_CASE("the gas alarm still fires while the climate sensor is faulted");
    /* The two sensors are independent: a DHT11 failure must not suppress a gas
     * alarm, which is the safety-relevant half of the instrument. */
    EnvMonitorPushGas(&monitor, 3000U);
    EnvMonitorSetGasEstimate(&monitor, 500U);
    EnvMonitorPushClimate(&monitor, 0U, 0U, 0U, now_ms + 3000U);
    result = EnvMonitorEvaluate(&monitor, now_ms + 3000U);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_SENSOR_FAULT));
}

/* Exercise the mute rules, which are the ones most likely to hide an alarm. */
static void test_mute_semantics(void)
{
    EnvMonitor monitor;
    EnvEvaluation result;
    uint32_t now_ms = 0U;

    TEST_CASE("mute suppresses the buzzer but not the alarm");
    EnvMonitorInit(&monitor);
    EnvMonitorPushGas(&monitor, 3000U);
    EnvMonitorSetGasEstimate(&monitor, 500U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(result.buzzer_on);

    EnvMonitorSetMuted(&monitor, true);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(EnvMonitorMuted(&monitor));
    /* The alarm state, the LED and the reported causes are unaffected: a mute
     * that cleared local_alarm would remove the operator's local indication. */
    CHECK_TRUE(result.local_alarm);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));
    CHECK_FALSE(result.buzzer_on);

    TEST_CASE("the mute holds while the same cause continues");
    result = EnvMonitorEvaluate(&monitor, now_ms + 1000U);
    CHECK_FALSE(result.buzzer_on);
    CHECK_FALSE(result.new_cause);

    TEST_CASE("a newly appearing cause cancels the mute");
    /* The gas alarm is still present and muted; a humidity alarm now appears as
     * well. Silencing it would be the dangerous outcome, so the mute clears. */
    EnvMonitorPushClimate(&monitor, 25U, 95U, 1U, now_ms + 2000U);
    result = EnvMonitorEvaluate(&monitor, now_ms + 2000U);
    CHECK_TRUE(result.new_cause);
    CHECK_TRUE(result.buzzer_on);
    CHECK_FALSE(EnvMonitorMuted(&monitor));

    TEST_CASE("a fresh episode after the alarm cleared cancels the mute");
    EnvMonitorInit(&monitor);
    now_ms = 0U;
    /* Alarm, then mute it. */
    EnvMonitorPushGas(&monitor, 3000U);
    EnvMonitorSetGasEstimate(&monitor, 500U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, now_ms);
    result = EnvMonitorEvaluate(&monitor, now_ms);
    CHECK_TRUE(result.buzzer_on);
    EnvMonitorSetMuted(&monitor, true);

    /* The room clears. */
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    for (unsigned index = 0U; index < ENV_GAS_WINDOW_SIZE; index++)
    {
        EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    }
    result = EnvMonitorEvaluate(&monitor, now_ms + 10000U);
    CHECK_FALSE(result.local_alarm);

    /* The same alarm returns later. The operator expects to hear it: a mute
     * remembered from a previous episode would silently suppress this one. */
    EnvMonitorPushGas(&monitor, 3000U);
    EnvMonitorSetGasEstimate(&monitor, 500U);
    for (unsigned index = 0U; index < ENV_GAS_WINDOW_SIZE; index++)
    {
        EnvMonitorPushGas(&monitor, 3000U);
    EnvMonitorSetGasEstimate(&monitor, 500U);
    }
    result = EnvMonitorEvaluate(&monitor, now_ms + 20000U);
    CHECK_TRUE(result.local_alarm);
    CHECK_TRUE(result.buzzer_on);

    TEST_CASE("unmuting explicitly restores the buzzer");
    EnvMonitorSetMuted(&monitor, false);
    result = EnvMonitorEvaluate(&monitor, now_ms + 21000U);
    CHECK_TRUE(result.buzzer_on);

    TEST_CASE("muting a quiet device has no effect on the reported state");
    EnvMonitorInit(&monitor);
    EnvMonitorSetMuted(&monitor, true);
    EnvMonitorPushGas(&monitor, 1000U);
    EnvMonitorSetGasEstimate(&monitor, 5U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 1000U);
    result = EnvMonitorEvaluate(&monitor, 1000U);
    CHECK_FALSE(result.local_alarm);
    CHECK_FALSE(result.buzzer_on);
    CHECK_TRUE(EnvMonitorMuted(&monitor));

    TEST_CASE("updating thresholds resets mute and clears previous causes so new alarm sounds");
    {
        EnvThresholds new_thresholds;
        EnvMonitorInit(&monitor);
        EnvMonitorPushGas(&monitor, 1000U);
        EnvMonitorSetGasEstimate(&monitor, 100U);
        EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 1000U);
        /* Alarm at 100 ppm when default is 20 ppm */
        result = EnvMonitorEvaluate(&monitor, 1000U);
        CHECK_TRUE(result.buzzer_on);
        /* Mute the active alarm */
        EnvMonitorSetMuted(&monitor, true);
        result = EnvMonitorEvaluate(&monitor, 1100U);
        CHECK_FALSE(result.buzzer_on);
        CHECK_TRUE(EnvMonitorMuted(&monitor));

        /* Now update thresholds: gas_high_ppm = 80 ppm.
         * The new thresholds must reset mute and clear previous causes. */
        EnvMonitorThresholds(&monitor, &new_thresholds);
        new_thresholds.gas_high_ppm = 80U;
        CHECK_TRUE(EnvMonitorSetThresholds(&monitor, &new_thresholds, 2U));
        CHECK_FALSE(EnvMonitorMuted(&monitor));

        /* Next evaluation at 100 ppm must immediately alarm and sound buzzer */
        result = EnvMonitorEvaluate(&monitor, 1200U);
        CHECK_TRUE(result.local_alarm);
        CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_GAS_HIGH));
        CHECK_TRUE(result.buzzer_on);
        CHECK_TRUE(result.new_cause);
    }
}

/* Exercise the wrap-safe history scan. The millisecond counter wraps about
 * every 49.7 days, which is well inside the deployment window for a device that
 * is expected to run unattended. */
static void test_history_timestamp_wrap(void)
{
    EnvMonitor monitor;
    EnvEvaluation result;

    TEST_CASE("a wrapped counter still measures the rise");
    EnvMonitorInit(&monitor);
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    /* History just below the wrap point, then a reading just past it. */
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 0xFFFF8000U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 0xFFFFC000U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 0xFFFFF000U);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 0xFFFFF800U);
    /* The 15-degree jump is 34 seconds after the oldest entry, so it is inside
     * both the rapid window and the minimum span. */
    EnvMonitorPushClimate(&monitor, 40U, 50U, 1U, 0x00000400U);
    result = EnvMonitorEvaluate(&monitor, 0x00000800U);
    CHECK_TRUE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));

    TEST_CASE("an entry further back than the window is ignored across the wrap");
    /* The same wrap, but the jump is now more than a window ahead of the oldest
     * entry, so the baseline moves forward and the earlier value no longer
     * counts as a rise. */
    EnvMonitorInit(&monitor);
    push_gas(&monitor, 1000U, 5U, ENV_GAS_WINDOW_SIZE);
    EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 0xFFFC0000U);
    EnvMonitorPushClimate(&monitor, 40U, 50U, 1U, 0x00000400U);
    result = EnvMonitorEvaluate(&monitor, 0x00000800U);
    CHECK_FALSE(EnvAlarmHas(result.alarm_causes, ENV_ALARM_RAPID_TEMPERATURE_RISE));
}

/* Exercise the caller-visible helpers. */
static void test_helpers(void)
{
    EnvMonitor monitor;
    EnvThresholds copy;
    uint32_t causes = (uint32_t)ENV_ALARM_GAS_HIGH | (uint32_t)ENV_ALARM_SENSOR_FAULT;

    TEST_CASE("cause bits are queried, not compared wholesale");
    CHECK_TRUE(EnvAlarmHas(causes, ENV_ALARM_GAS_HIGH));
    CHECK_TRUE(EnvAlarmHas(causes, ENV_ALARM_SENSOR_FAULT));
    CHECK_FALSE(EnvAlarmHas(causes, ENV_ALARM_TEMPERATURE_HIGH));
    CHECK_FALSE(EnvAlarmHas(ENV_ALARM_NONE, ENV_ALARM_GAS_HIGH));

    TEST_CASE("the cause values are distinct single bits");
    CHECK_INT(1, ENV_ALARM_TEMPERATURE_HIGH);
    CHECK_INT(2, ENV_ALARM_HUMIDITY_HIGH);
    CHECK_INT(4, ENV_ALARM_GAS_HIGH);
    CHECK_INT(8, ENV_ALARM_RAPID_TEMPERATURE_RISE);
    CHECK_INT(16, ENV_ALARM_RAPID_GAS_RISE);
    CHECK_INT(32, ENV_ALARM_SENSOR_FAULT);

    TEST_CASE("a null output pointer is ignored rather than dereferenced");
    EnvMonitorInit(&monitor);
    EnvMonitorThresholds(&monitor, NULL);
    EnvMonitorThresholds(&monitor, &copy);
    CHECK_INT(ENV_DEFAULT_TEMPERATURE_HIGH_C, copy.temperature_high_c);
}

/* Entry point for the env_monitor suites. */
void test_env_monitor_suite(void)
{
    test_gas_filter();
    test_thresholds();
    test_absolute_alarms();
    test_rapid_rise();
    test_sensor_fault();
    test_mute_semantics();
    test_history_timestamp_wrap();
    test_helpers();
}
