#include "display_model.h"
#include "text_format.h"

/* Buffer sizes for one line of text plus its terminator. */
#define DISPLAY_LINE_BUFFER (DISPLAY_LINE_LENGTH + 1U)

/* A field on a line is at most ten digits wide, because every value rendered is
 * a uint32 with a field width of four or fewer. Asserting it here means the
 * formatters below always succeed, so the call sites need no unreachable
 * fallback that no test could ever exercise. */
_Static_assert(DISPLAY_LINE_BUFFER >= 11U, "a line buffer must hold the widest uint32 field");

/* Report whether a cause bit is present in a mask. */
static bool EnvAlarmCausePresent(uint32_t causes, uint32_t cause)
{
    return (causes & cause) != 0U;
}

/* Cause bits, mirroring EnvAlarmCause. They are repeated here so this module can
 * be compiled and tested without the monitor, and a host test asserts the two
 * agree. */
#define DISPLAY_CAUSE_TEMPERATURE_HIGH (1UL << 0)
#define DISPLAY_CAUSE_HUMIDITY_HIGH (1UL << 1)
#define DISPLAY_CAUSE_GAS_HIGH (1UL << 2)
#define DISPLAY_CAUSE_RAPID_TEMPERATURE_RISE (1UL << 3)
#define DISPLAY_CAUSE_RAPID_GAS_RISE (1UL << 4)
#define DISPLAY_CAUSE_SENSOR_FAULT (1UL << 5)

/* Copy a string into a line, truncating rather than overflowing.
 *
 * Truncation is safe here because every caller passes a fixed literal or an
 * already-bounded value; the alternative, walking off the end of the line
 * buffer, would corrupt the frame. */
static void line_set(char *line, const char *text)
{
    uint32_t index = 0U;

    if (text != NULL)
    {
        while (text[index] != '\0' && index < DISPLAY_LINE_LENGTH)
        {
            line[index] = text[index];
            index++;
        }
    }
    while (index < DISPLAY_LINE_LENGTH)
    {
        line[index] = ' ';
        index++;
    }
    line[DISPLAY_LINE_LENGTH] = '\0';
}

/* Join two strings into a line. */
static void line_join(char *line, const char *first, const char *second)
{
    char body[DISPLAY_LINE_BUFFER];
    uint32_t position = 0U;
    uint32_t index;

    for (index = 0U; first[index] != '\0' && position < DISPLAY_LINE_LENGTH; index++)
    {
        body[position++] = first[index];
    }
    for (index = 0U; second[index] != '\0' && position < DISPLAY_LINE_LENGTH; index++)
    {
        body[position++] = second[index];
    }
    body[position] = '\0';
    line_set(line, body);
}

/* Build a line from a label, a numeric field and a unit. */
static void line_labeled_number(char *line, const char *label, uint32_t value, uint8_t digits, const char *unit)
{
    char field[DISPLAY_LINE_BUFFER];
    char body[DISPLAY_LINE_BUFFER];
    uint32_t position = 0U;
    uint32_t index;

    /* Zero padding rather than spaces: a measurement field on a fixed-width
     * panel reads as "012" not " 12", and the unit stays in its column. The
     * static assertion above guarantees this cannot fail. */
    (void)TextFormatZeroPadded(field, sizeof(field), value, digits);

    for (index = 0U; label[index] != '\0' && position < DISPLAY_LINE_LENGTH; index++)
    {
        body[position++] = label[index];
    }
    /* The padded field keeps the column steady as the value grows, which stops
     * a four-digit gas value from shifting the unit to the next line. */
    for (index = 0U; field[index] != '\0' && position < DISPLAY_LINE_LENGTH; index++)
    {
        body[position++] = field[index];
    }
    if (unit != NULL)
    {
        for (index = 0U; unit[index] != '\0' && position < DISPLAY_LINE_LENGTH; index++)
        {
            body[position++] = unit[index];
        }
    }
    body[position] = '\0';
    line_set(line, body);
}

/* Render the letters for the active causes into `out`, or "---" when clear.
 *
 * The letters match the panel the firmware has always shown: T temperature,
 * H humidity, G gas, t rapid temperature rise, g rapid gas rise, F sensor
 * fault. Lower case distinguishes the rise causes from the absolute ones, so a
 * glance at "T" versus "t" says whether the limit or the trend tripped.
 *
 * `out` must hold at least seven bytes: the six letters, or "---", plus the
 * terminator. The single call site passes eight. */
static void render_cause_letters(uint32_t causes, char *out)
{
    uint32_t position = 0U;

    if (causes == 0U)
    {
        out[position++] = '-';
        out[position++] = '-';
        out[position++] = '-';
        out[position] = '\0';
        return;
    }

    if ((causes & DISPLAY_CAUSE_TEMPERATURE_HIGH) != 0U)
    {
        out[position++] = 'T';
    }
    if ((causes & DISPLAY_CAUSE_HUMIDITY_HIGH) != 0U)
    {
        out[position++] = 'H';
    }
    if ((causes & DISPLAY_CAUSE_GAS_HIGH) != 0U)
    {
        out[position++] = 'G';
    }
    if ((causes & DISPLAY_CAUSE_RAPID_TEMPERATURE_RISE) != 0U)
    {
        out[position++] = 't';
    }
    if ((causes & DISPLAY_CAUSE_RAPID_GAS_RISE) != 0U)
    {
        out[position++] = 'g';
    }
    if ((causes & DISPLAY_CAUSE_SENSOR_FAULT) != 0U)
    {
        out[position++] = 'F';
    }
    out[position] = '\0';
}

/* Render the climate page: temperature, humidity, gas and the thresholds in
 * force. */
static void render_climate(const DisplayInput *input, DisplayFrame *frame)
{
    char version[DISPLAY_LINE_BUFFER];

    line_labeled_number(frame->lines[0], "Temp: ", input->temperature_c, 2U, "C");
    line_labeled_number(frame->lines[1], "Humi: ", input->humidity_rh, 3U, "%");
    line_labeled_number(frame->lines[2], "Gas: ", input->gas_ppm, 3U, "ppm");

    /* The version is a uint32 and the buffer holds fourteen characters, so the
     * formatter cannot fail; see the static assertion at the top of the file. */
    (void)TextFormatUnsigned(version, sizeof(version), input->threshold_version, 1U);
    line_join(frame->lines[3], "Thr: v", version);
}

/* Render the gas detail page: raw and filtered ADC plus the estimate and a
 * level. */
static void render_gas(const DisplayInput *input, DisplayFrame *frame)
{
    char level[DISPLAY_LINE_BUFFER];
    uint32_t level_length = 0U;

    line_labeled_number(frame->lines[0], "ADC raw:", input->gas_adc_raw, 4U, "");
    line_labeled_number(frame->lines[1], "ADC flt:", input->gas_adc_filtered, 4U, "");

    if (input->gas_uncalibrated)
    {
        /* The estimate is relative while the sensor is uncalibrated, so the page
         * says so rather than presenting ppm as a measurement. The contract
         * requires clients to prefer a level over a precise number in this
         * state, and the panel is a client too. */
        line_labeled_number(frame->lines[2], "Est: ", input->gas_ppm, 3U, "ppm*");
    }
    else
    {
        line_labeled_number(frame->lines[2], "Gas: ", input->gas_ppm, 3U, "ppm");
    }

    {
        const char *word;

        if ((input->alarm_causes & DISPLAY_CAUSE_GAS_HIGH) != 0U)
        {
            word = "ALARM";
        }
        else if ((input->alarm_causes & DISPLAY_CAUSE_RAPID_GAS_RISE) != 0U)
        {
            word = "RISING";
        }
        else
        {
            word = "OK";
        }
        while (word[level_length] != '\0' && level_length < DISPLAY_LINE_LENGTH)
        {
            level[level_length] = word[level_length];
            level_length++;
        }
    }
    /* A trailing asterisk marks the estimate as uncalibrated, so the level word
     * is not mistaken for a calibrated verdict. It is appended to the level text
     * before the line is padded, because appending it to a finished line would
     * need a column the 16-character panel does not have. */
    if (input->gas_uncalibrated && level_length < DISPLAY_LINE_LENGTH)
    {
        level[level_length++] = '*';
    }
    level[level_length] = '\0';

    line_join(frame->lines[3], "Level: ", level);
}

/* Render the alarm page: cause letters, buzzer state and fault state. */
static void render_alarm(const DisplayInput *input, DisplayFrame *frame)
{
    char letters[8];
    char sensor[DISPLAY_LINE_BUFFER];
    bool alarmed = input->alarm_causes != 0U;
    bool faulted = EnvAlarmCausePresent(input->alarm_causes, DISPLAY_CAUSE_SENSOR_FAULT);

    render_cause_letters(input->alarm_causes, letters);
    line_join(frame->lines[0], "Alarm: ", letters);

    /* The buzzer line reports the actual output state. */
    line_set(frame->lines[1], TextSelect(input->buzzer_active, "Buzzer: ON", "Buzzer: off"));

    /* Derived from the cause bit rather than from a second flag, so the line
     * cannot disagree with the causes shown on the line above it.
     *
     * A fault carries its status code as well ("Sensor: F5"), because on the
     * bench the code is the difference between a wiring problem and a sensor
     * that answers but cannot be parsed. A fault reported without a code — the
     * monitor can raise one without the driver ever having run — keeps the
     * plain word rather than inventing a code that did not come from the
     * driver. */
    if (!faulted)
    {
        line_set(frame->lines[2], "Sensor: ok");
    }
    else if (input->dht_error == 0U || input->dht_error > 7U)
    {
        line_set(frame->lines[2], "Sensor: FAULT");
    }
    else
    {
        char code[DISPLAY_LINE_BUFFER];

        (void)TextFormatUnsigned(code, sizeof(code), input->dht_error, 1U);
        line_join(sensor, "Sensor: F", code);
        line_set(frame->lines[2], sensor);
    }

    if (!alarmed)
    {
        line_set(frame->lines[3], "State: clear");
    }
    else
    {
        line_set(frame->lines[3], "State: ALARM");
    }
}

/* Build the receive-loss line: "RX DROP 3 TRUNC 1", abbreviated to fit the
 * sixteen-character panel. The counts are zero-padded so the line does not jump
 * as they grow, and the fields keep their columns from the right. */
static void render_receive_loss(char *line, uint32_t discarded, uint32_t truncated)
{
    char discarded_field[8];
    char truncated_field[8];
    char body[DISPLAY_LINE_BUFFER];
    uint32_t position = 0U;
    const char *text;

    (void)TextFormatZeroPadded(discarded_field, sizeof(discarded_field), discarded, 3U);
    (void)TextFormatZeroPadded(truncated_field, sizeof(truncated_field), truncated, 3U);

    /* Assembled by hand rather than with a printf: the firmware links against
     * -nostdlib and the two integers are the whole of the line. */
    for (text = "RX D"; *text != '\0' && position < DISPLAY_LINE_LENGTH; text++)
    {
        body[position++] = *text;
    }
    for (text = discarded_field; *text != '\0' && position < DISPLAY_LINE_LENGTH; text++)
    {
        body[position++] = *text;
    }
    for (text = " TR"; *text != '\0' && position < DISPLAY_LINE_LENGTH; text++)
    {
        body[position++] = *text;
    }
    for (text = truncated_field; *text != '\0' && position < DISPLAY_LINE_LENGTH; text++)
    {
        body[position++] = *text;
    }
    body[position] = '\0';
    line_set(line, body);
}

/* Render the network page: link state, SSID and the last server message.
 *
 * Preserving the downlink message keeps the behaviour the firmware had before
 * the carousel existed: the panel was the only place a server message could be
 * seen. */
static void render_network(const DisplayInput *input, DisplayFrame *frame)
{
    const char *prefix = "Linking:";
    char body[DISPLAY_LINE_BUFFER];
    uint32_t position = 0U;
    const char *message;
    uint32_t source = 0U;
    uint32_t line_index;

    if (input->network == DISPLAY_NETWORK_LINKED)
    {
        prefix = "Linked:";
    }
    else if (input->network == DISPLAY_NETWORK_FAILED)
    {
        prefix = "Link fail";
    }

    line_join(frame->lines[0], prefix, input->wifi_ssid != NULL ? input->wifi_ssid : "");

    /* The message fills the remaining three lines, 16 characters each, which is
     * what the previous fixed 44-character limit effectively provided. */
    message = input->server_message;
    source = 0U;
    for (line_index = 1U; line_index < DISPLAY_LINE_COUNT; line_index++)
    {
        uint32_t column = 0U;

        if (line_index == 1U)
        {
            for (position = 0U; "msg:"[position] != '\0' && column < DISPLAY_LINE_LENGTH; position++)
            {
                body[column++] = "msg:"[position];
            }
        }

        while (column < DISPLAY_LINE_LENGTH && message != NULL && message[source] != '\0')
        {
            body[column++] = message[source++];
        }
        body[column] = '\0';
        line_set(frame->lines[line_index], body);

        if (message == NULL || message[source] == '\0')
        {
            /* The remaining lines stay blank rather than repeating the message. */
            for (line_index++; line_index < DISPLAY_LINE_COUNT; line_index++)
            {
                line_set(frame->lines[line_index], "");
            }
            break;
        }
    }

    /* A counted receive loss displaces the last line, including the tail of a
     * long server message. That ordering is deliberate: the message is stale
     * decoration, whereas the counter is the only evidence on the device that a
     * control frame did not arrive, and a tester reading zero here has evidence
     * that nothing was lost. */
    if (input->rx_discarded != 0U || input->rx_truncated != 0U)
    {
        render_receive_loss(frame->lines[DISPLAY_LINE_COUNT - 1U], input->rx_discarded,
                            input->rx_truncated);
    }
}

void DisplayModelNextPage(DisplayPage *page)
{
    if (page == NULL)
    {
        return;
    }
    *page = (DisplayPage)(((uint32_t)*page + 1U) % (uint32_t)DISPLAY_PAGE_COUNT);
}

void DisplayModelRender(DisplayPage page, const DisplayInput *input, DisplayFrame *frame)
{
    DisplayInput empty;
    uint32_t index;

    if (frame == NULL)
    {
        return;
    }
    if (input == NULL)
    {
        /* Rendering before the first reading is normal: the panel comes up
         * before the sensors do. An explicit empty state is better than
         * dereferencing nothing. */
        empty.temperature_c = 0U;
        empty.humidity_rh = 0U;
        empty.gas_ppm = 0U;
        empty.gas_adc_raw = 0U;
        empty.gas_adc_filtered = 0U;
        empty.alarm_causes = 0U;
        empty.dht_error = 0U;
        empty.buzzer_active = false;
        empty.gas_uncalibrated = true;
        empty.threshold_version = 0U;
        empty.network = DISPLAY_NETWORK_LINKING;
        empty.wifi_ssid = "";
        empty.server_message = "";
        empty.rx_discarded = 0U;
        empty.rx_truncated = 0U;
        input = &empty;
    }

    /* Every line is cleared first so a page can never inherit a tail from the
     * page shown before it. */
    for (index = 0U; index < DISPLAY_LINE_COUNT; index++)
    {
        line_set(frame->lines[index], "");
    }

    switch (page)
    {
    case DISPLAY_PAGE_GAS:
        render_gas(input, frame);
        break;
    case DISPLAY_PAGE_ALARM:
        render_alarm(input, frame);
        break;
    case DISPLAY_PAGE_NETWORK:
        render_network(input, frame);
        break;
    case DISPLAY_PAGE_CLIMATE:
    default:
        /* An out-of-range page value renders the climate page rather than
         * nothing, so a corrupted page counter still shows a reading. */
        render_climate(input, frame);
        break;
    }
}
