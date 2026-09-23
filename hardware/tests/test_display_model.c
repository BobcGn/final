#include "display_model.h"
#include "env_monitor.h"
#include "test_support.h"

/* Cause bits as display_model.c defines them, used to check the two modules
 * agree. They are not exported from the header, so the values are repeated
 * here and asserted equal to the monitor's. */
#define DISPLAY_CAUSE_TEMPERATURE_HIGH (1UL << 0)
#define DISPLAY_CAUSE_HUMIDITY_HIGH (1UL << 1)
#define DISPLAY_CAUSE_GAS_HIGH (1UL << 2)
#define DISPLAY_CAUSE_RAPID_TEMPERATURE_RISE (1UL << 3)
#define DISPLAY_CAUSE_RAPID_GAS_RISE (1UL << 4)
#define DISPLAY_CAUSE_SENSOR_FAULT (1UL << 5)

/* A quiet device reading, used as the base for each case. */
static DisplayInput quiet_input(void)
{
    DisplayInput input;

    input.temperature_c = 25U;
    input.humidity_rh = 50U;
    input.gas_ppm = 12U;
    input.gas_adc_raw = 1350U;
    input.gas_adc_filtered = 1328U;
    input.alarm_causes = 0U;
    input.dht_error = 0U;
    input.buzzer_muted = false;
    input.buzzer_active = false;
    input.gas_uncalibrated = true;
    input.threshold_version = 1U;
    input.network = DISPLAY_NETWORK_LINKED;
    input.wifi_ssid = "Lab";
    input.server_message = "";
    /* Quiet by default: the receive-loss line only appears when the radio has
     * actually dropped something. */
    input.rx_discarded = 0U;
    input.rx_truncated = 0U;
    return input;
}

/* Verify that a rendered line is exactly the panel width and terminated. */
static void check_line_shape(const DisplayFrame *frame, uint32_t index)
{
    const char *line = frame->lines[index];

    CHECK_INT(DISPLAY_LINE_LENGTH, strlen(line));
}

/* Exercise the page rotation. */
static void test_rotation(void)
{
    DisplayPage page = DISPLAY_PAGE_CLIMATE;

    TEST_CASE("the carousel advances through every page and wraps");
    DisplayModelNextPage(&page);
    CHECK_INT(DISPLAY_PAGE_GAS, page);
    DisplayModelNextPage(&page);
    CHECK_INT(DISPLAY_PAGE_ALARM, page);
    DisplayModelNextPage(&page);
    CHECK_INT(DISPLAY_PAGE_NETWORK, page);
    DisplayModelNextPage(&page);
    CHECK_INT(DISPLAY_PAGE_CLIMATE, page);

    TEST_CASE("a null page pointer is ignored rather than dereferenced");
    DisplayModelNextPage(NULL);
}

/* Exercise the frame shape, which the panel driver depends on. */
static void test_frame_shape(void)
{
    DisplayFrame frame;
    DisplayInput input = quiet_input();
    uint32_t page;

    TEST_CASE("every line of every page is exactly the panel width");
    for (page = 0U; page < (uint32_t)DISPLAY_PAGE_COUNT; page++)
    {
        uint32_t line;

        DisplayModelRender((DisplayPage)page, &input, &frame);
        for (line = 0U; line < DISPLAY_LINE_COUNT; line++)
        {
            check_line_shape(&frame, line);
        }
    }

    TEST_CASE("a page never inherits text from the page rendered before it");
    /* Render a page with a long message, then a page with a short one. If the
     * frame were not cleared the tail would survive and the panel would show a
     * mixture. */
    input.server_message = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    input.server_message = "x";
    DisplayModelRender(DISPLAY_PAGE_CLIMATE, &input, &frame);
    CHECK_INT(0, strncmp(frame.lines[1], "Humi:", 5));
    CHECK_FALSE(strstr(frame.lines[1], "STUV") != NULL);

    TEST_CASE("a null frame is ignored rather than dereferenced");
    DisplayModelRender(DISPLAY_PAGE_CLIMATE, &input, NULL);
}

/* Exercise the climate page. */
static void test_climate_page(void)
{
    DisplayFrame frame;
    DisplayInput input = quiet_input();

    TEST_CASE("the readings and the threshold version are shown");
    DisplayModelRender(DISPLAY_PAGE_CLIMATE, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Temp: 25C", 9) == 0);
    CHECK_TRUE(strncmp(frame.lines[1], "Humi: 050%", 10) == 0);
    CHECK_TRUE(strncmp(frame.lines[2], "Gas: 012ppm", 11) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], "Thr: v1", 7) == 0);

    TEST_CASE("a single-digit temperature keeps its column");
    input.temperature_c = 5U;
    DisplayModelRender(DISPLAY_PAGE_CLIMATE, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Temp: 05C", 9) == 0);

    TEST_CASE("a three-digit version is shown in full");
    input.threshold_version = 128U;
    DisplayModelRender(DISPLAY_PAGE_CLIMATE, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "Thr: v128", 9) == 0);

    TEST_CASE("an out-of-range page renders the climate page rather than nothing");
    /* A corrupted page counter must still show a reading. */
    DisplayModelRender((DisplayPage)99, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Temp:", 5) == 0);
}

/* Exercise the gas page. */
static void test_gas_page(void)
{
    DisplayFrame frame;
    DisplayInput input = quiet_input();

    TEST_CASE("raw and filtered ADC are shown separately");
    DisplayModelRender(DISPLAY_PAGE_GAS, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "ADC raw:1350", 12) == 0);
    CHECK_TRUE(strncmp(frame.lines[1], "ADC flt:1328", 12) == 0);

    TEST_CASE("an uncalibrated estimate is marked as such");
    /* The frozen contract requires clients to prefer a level over a precise
     * number while the sensor is uncalibrated, and the panel is a client. */
    CHECK_TRUE(strncmp(frame.lines[2], "Est: 012ppm*", 12) == 0);
    CHECK_TRUE(strstr(frame.lines[3], "*") != NULL);

    TEST_CASE("a calibrated reading is presented as a measurement");
    input.gas_uncalibrated = false;
    DisplayModelRender(DISPLAY_PAGE_GAS, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[2], "Gas: 012ppm", 11) == 0);
    CHECK_TRUE(strstr(frame.lines[3], "*") == NULL);

    TEST_CASE("the level follows the alarm causes");
    CHECK_TRUE(strncmp(frame.lines[3], "Level: OK", 9) == 0);

    input.alarm_causes = DISPLAY_CAUSE_RAPID_GAS_RISE;
    DisplayModelRender(DISPLAY_PAGE_GAS, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "Level: RISING", 13) == 0);

    input.alarm_causes = DISPLAY_CAUSE_GAS_HIGH;
    DisplayModelRender(DISPLAY_PAGE_GAS, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "Level: ALARM", 12) == 0);

    TEST_CASE("the absolute gas alarm outranks the rise when both are active");
    input.alarm_causes = DISPLAY_CAUSE_GAS_HIGH | DISPLAY_CAUSE_RAPID_GAS_RISE;
    DisplayModelRender(DISPLAY_PAGE_GAS, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "Level: ALARM", 12) == 0);
}

/* Exercise the alarm page, which is the one that must never be ambiguous. */
static void test_alarm_page(void)
{
    DisplayFrame frame;
    DisplayInput input = quiet_input();

    TEST_CASE("a clear device shows no cause letters");
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: ---", 10) == 0);
    CHECK_TRUE(strncmp(frame.lines[1], "Buzzer: off", 11) == 0);
    CHECK_TRUE(strncmp(frame.lines[2], "Sensor: ok", 10) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], "State: clear", 12) == 0);

    TEST_CASE("each cause has its own letter");
    input.alarm_causes = DISPLAY_CAUSE_TEMPERATURE_HIGH;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: T", 8) == 0);

    input.alarm_causes = DISPLAY_CAUSE_HUMIDITY_HIGH;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: H", 8) == 0);

    input.alarm_causes = DISPLAY_CAUSE_GAS_HIGH;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: G", 8) == 0);

    TEST_CASE("a rise cause uses a lower-case letter so it reads apart from the limit");
    input.alarm_causes = DISPLAY_CAUSE_RAPID_TEMPERATURE_RISE;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: t", 8) == 0);

    input.alarm_causes = DISPLAY_CAUSE_RAPID_GAS_RISE;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: g", 8) == 0);

    input.alarm_causes = DISPLAY_CAUSE_SENSOR_FAULT;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: F", 8) == 0);
    /* The sensor line is derived from the same cause bit, so the two lines can
     * never disagree about whether the instrument is faulted. */
    CHECK_TRUE(strncmp(frame.lines[2], "Sensor: FAULT", 13) == 0);

    TEST_CASE("a faulted sensor reports the driver status code");
    /* "the sensor never answered" and "the sensor answered but the bytes did not
     * parse" are different bench faults with different fixes, so the panel has
     * to name which one it is rather than only that something failed. */
    input.dht_error = 5U;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[2], "Sensor: F5", 10) == 0);

    TEST_CASE("a code outside the driver range falls back to the plain word");
    /* The monitor can raise the fault without the driver ever having produced a
     * code, and a stored code is never trusted blindly onto the panel. */
    input.dht_error = 9U;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[2], "Sensor: FAULT", 13) == 0);

    input.dht_error = 0U;

    TEST_CASE("several causes are all shown in a fixed order");
    input.alarm_causes = DISPLAY_CAUSE_GAS_HIGH | DISPLAY_CAUSE_TEMPERATURE_HIGH |
                         DISPLAY_CAUSE_HUMIDITY_HIGH;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: THG", 10) == 0);

    TEST_CASE("an active alarm shows the buzzer as on");
    input.alarm_causes = DISPLAY_CAUSE_GAS_HIGH;
    input.buzzer_muted = false;
    input.buzzer_active = true;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[1], "Buzzer: ON", 10) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], "State: ALARM", 12) == 0);

    TEST_CASE("a non-gas alarm can remain silent");
    input.alarm_causes = DISPLAY_CAUSE_TEMPERATURE_HIGH;
    input.buzzer_active = false;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: T", 8) == 0);
    CHECK_TRUE(strncmp(frame.lines[1], "Buzzer: off", 11) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], "State: ALARM", 12) == 0);

    TEST_CASE("a muted alarm still reports the alarm, not silence");
    /* This is the state an operator has to be able to notice: the alarm is
     * active and the buzzer is suppressed. The page must say both, because a
     * page that showed only "muted" would look like a clear room. */
    input.alarm_causes = DISPLAY_CAUSE_GAS_HIGH;
    input.buzzer_muted = true;
    input.buzzer_active = false;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: G", 8) == 0);
    CHECK_TRUE(strncmp(frame.lines[1], "Buzzer: off", 11) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], "State: MUTED", 12) == 0);

    TEST_CASE("muting a clear device does not claim an alarm");
    input.alarm_causes = 0U;
    DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "State: clear", 12) == 0);
}

/* Exercise the network page, which preserves the downlink message the panel
 * showed before the carousel existed. */
static void test_network_page(void)
{
    DisplayFrame frame;
    DisplayInput input = quiet_input();

    TEST_CASE("a linked device shows its SSID");
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Linked:Lab", 10) == 0);

    TEST_CASE("the linking and failure states are distinct");
    input.network = DISPLAY_NETWORK_LINKING;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Linking:", 8) == 0);

    input.network = DISPLAY_NETWORK_FAILED;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Link fail", 9) == 0);

    TEST_CASE("a null SSID is tolerated");
    input.wifi_ssid = NULL;
    input.network = DISPLAY_NETWORK_LINKED;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Linked:", 7) == 0);

    TEST_CASE("a short message occupies the first message line only");
    input.server_message = "hello";
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[1], "msg:hello", 9) == 0);
    CHECK_TRUE(strncmp(frame.lines[2], " ", 1) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], " ", 1) == 0);

    TEST_CASE("a long message wraps across the three message lines");
    /* 12 + 16 + 16 = 44 characters, which is the limit the previous firmware
     * documented for the panel. */
    input.server_message = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqr";
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[1], "msg:ABCDEFGHIJKL", 16) == 0);
    CHECK_TRUE(strncmp(frame.lines[2], "MNOPQRSTUVWXYZab", 16) == 0);
    CHECK_TRUE(strncmp(frame.lines[3], "cdefghijklmnopqr", 16) == 0);

    TEST_CASE("a message longer than the panel fits is truncated, not overflowed");
    input.server_message = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    check_line_shape(&frame, 1U);
    check_line_shape(&frame, 2U);
    check_line_shape(&frame, 3U);
    CHECK_TRUE(strncmp(frame.lines[1], "msg:ABCDEFGHIJKL", 16) == 0);

    TEST_CASE("a null message is tolerated");
    input.server_message = NULL;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[1], "msg:", 4) == 0);

    TEST_CASE("a counted receive loss is shown on the last line");
    /* The message tail is displaced on purpose: the counter is the only
     * on-device evidence that a control frame did not arrive, whereas the
     * message is stale decoration. */
    input.server_message = "hello";
    input.rx_discarded = 3U;
    input.rx_truncated = 1U;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    check_line_shape(&frame, 3U);
    CHECK_TRUE(strncmp(frame.lines[3], "RX D003 TR001", 13) == 0);

    TEST_CASE("a loss on its own does not need a server message");
    input.server_message = NULL;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "RX D003 TR001", 13) == 0);

    TEST_CASE("a truncation on its own is shown too");
    input.rx_discarded = 0U;
    input.rx_truncated = 2U;
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], "RX D000 TR002", 13) == 0);

    TEST_CASE("a quiet radio leaves the last message line alone");
    input.rx_truncated = 0U;
    input.server_message = "hello";
    DisplayModelRender(DISPLAY_PAGE_NETWORK, &input, &frame);
    CHECK_TRUE(strncmp(frame.lines[3], " ", 1) == 0);
}

/* Exercise the empty state used before the first reading. */
static void test_empty_state(void)
{
    DisplayFrame frame;

    TEST_CASE("a null input renders the empty state instead of crashing");
    /* The display refresh runs before the first sensor reading is available. */
    DisplayModelRender(DISPLAY_PAGE_CLIMATE, NULL, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Temp: 00C", 9) == 0);

    DisplayModelRender(DISPLAY_PAGE_NETWORK, NULL, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Linking:", 8) == 0);

    DisplayModelRender(DISPLAY_PAGE_ALARM, NULL, &frame);
    CHECK_TRUE(strncmp(frame.lines[0], "Alarm: ---", 10) == 0);
}

/* Verify the cause bit values agree with the monitor, since this module repeats
 * them rather than including env_monitor.h. */
static void test_cause_bits_agree_with_the_monitor(void)
{
    TEST_CASE("the display cause bits match the monitor's");
    CHECK_INT(ENV_ALARM_TEMPERATURE_HIGH, DISPLAY_CAUSE_TEMPERATURE_HIGH);
    CHECK_INT(ENV_ALARM_HUMIDITY_HIGH, DISPLAY_CAUSE_HUMIDITY_HIGH);
    CHECK_INT(ENV_ALARM_GAS_HIGH, DISPLAY_CAUSE_GAS_HIGH);
    CHECK_INT(ENV_ALARM_RAPID_TEMPERATURE_RISE, DISPLAY_CAUSE_RAPID_TEMPERATURE_RISE);
    CHECK_INT(ENV_ALARM_RAPID_GAS_RISE, DISPLAY_CAUSE_RAPID_GAS_RISE);
    CHECK_INT(ENV_ALARM_SENSOR_FAULT, DISPLAY_CAUSE_SENSOR_FAULT);

    TEST_CASE("a real evaluation feeds the alarm page unchanged");
    {
        EnvMonitor monitor;
        EnvEvaluation evaluation;
        DisplayInput input = quiet_input();
        DisplayFrame frame;

        EnvMonitorInit(&monitor);
        EnvMonitorPushGas(&monitor, 3000U);
    EnvMonitorSetGasEstimate(&monitor, 500U);
        EnvMonitorPushClimate(&monitor, 25U, 50U, 1U, 1000U);
        evaluation = EnvMonitorEvaluate(&monitor, 1000U);

        input.alarm_causes = evaluation.alarm_causes;
        input.buzzer_muted = EnvMonitorMuted(&monitor);
        input.buzzer_active = evaluation.buzzer_on;
        DisplayModelRender(DISPLAY_PAGE_ALARM, &input, &frame);
        CHECK_TRUE(strncmp(frame.lines[0], "Alarm: G", 8) == 0);
        CHECK_TRUE(strncmp(frame.lines[1], "Buzzer: ON", 10) == 0);
    }
}

/* Entry point for the display_model suite. */
void test_display_model_suite(void)
{
    test_rotation();
    test_frame_shape();
    test_climate_page();
    test_gas_page();
    test_alarm_page();
    test_network_page();
    test_empty_state();
    test_cause_bits_agree_with_the_monitor();
}
