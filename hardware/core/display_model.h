#ifndef __DISPLAY_MODEL_H
#define __DISPLAY_MODEL_H

/*
 * Carousel content for the SSD1306 panel.
 *
 * The model decides what the four 16-character lines say; the firmware module
 * owns the OLED_* calls that put them on the glass. Splitting it this way means
 * the page content, the page rotation and the alarm rendering are all testable
 * on a host, which is the part most likely to be wrong: a panel that shows a
 * plausible but incorrect reading is worse than one that shows nothing.
 *
 * The panel is 128x64. With OLED_8X16 text that is 16 characters across and
 * four lines down, which is what DISPLAY_LINE_* describes.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define DISPLAY_LINE_LENGTH 16U
#define DISPLAY_LINE_COUNT 4U

/* Pages of the carousel, in rotation order. */
typedef enum
{
    DISPLAY_PAGE_CLIMATE = 0,
    DISPLAY_PAGE_GAS,
    DISPLAY_PAGE_ALARM,
    DISPLAY_PAGE_NETWORK,
    DISPLAY_PAGE_COUNT
} DisplayPage;

/* Link state as the firmware knows it, mirroring the previous behaviour where
 * the panel showed "Linking:", "Linked:" or "Link failed". */
typedef enum
{
    DISPLAY_NETWORK_LINKING = 0,
    DISPLAY_NETWORK_LINKED,
    DISPLAY_NETWORK_FAILED
} DisplayNetworkState;

/* Everything a page may need. It is passed by pointer and never retained, so a
 * caller can build it on the stack each refresh. */
typedef struct
{
    uint8_t temperature_c;
    uint8_t humidity_rh;
    uint16_t gas_ppm;
    uint16_t gas_adc_raw;
    uint16_t gas_adc_filtered;
    /* Bitmask of EnvAlarmCause. The model reads bits, so this module does not
     * depend on env_monitor.h and the two can be tested apart. */
    uint32_t alarm_causes;
    /* Status code from the last DHT11 read, as returned by DHT11_Read_Data:
     * zero means the read succeeded, non-zero is the failure code. Shown next to
     * the sensor-fault line so a bench fault can be told apart — "no response"
     * is wiring or power, a checksum failure is the sensor or the bit timing.
     * Only meaningful while the sensor-fault cause bit is set. */
    uint8_t dht_error;
    bool buzzer_muted;
    /* Actual instantaneous output after cause filtering and beep cadence. */
    bool buzzer_active;
    /* True while the gas estimate comes from an uncalibrated curve, which the
     * gas page says out loud instead of presenting the number as a measurement. */
    bool gas_uncalibrated;
    uint32_t threshold_version;
    DisplayNetworkState network;
    const char *wifi_ssid;
    /* The most recent message pushed from the server, shown on the network page.
     * May be NULL or empty. */
    const char *server_message;
    /* Receive frames the radio driver could not hand up: one dropped because an
     * earlier frame was still unread, and one longer than the receive buffer.
     * They are shown on the network page when non-zero, because a dropped
     * device/control frame is otherwise invisible and the operator would have no
     * way to tell a silent broker from a lost command. */
    uint32_t rx_discarded;
    uint32_t rx_truncated;
} DisplayInput;

/* One rendered page: four NUL-terminated lines of at most
 * DISPLAY_LINE_LENGTH characters. */
typedef struct
{
    char lines[DISPLAY_LINE_COUNT][DISPLAY_LINE_LENGTH + 1U];
} DisplayFrame;

/* Advance to the next page, wrapping at the end of the carousel. */
void DisplayModelNextPage(DisplayPage *page);

/* Render one page.
 *
 * Rendering is total: every line is always filled, so the caller can hand the
 * frame straight to the panel without clearing first. A NULL input renders the
 * empty-state page rather than being undefined, because the display refresh runs
 * before the first sensor reading is available. */
void DisplayModelRender(DisplayPage page, const DisplayInput *input, DisplayFrame *frame);

#endif /* __DISPLAY_MODEL_H */
