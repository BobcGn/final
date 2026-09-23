/*
 * Host test runner for the pure firmware logic.
 *
 * It is a separate CMake project from the firmware so that it builds with the
 * host compiler: the firmware is cross-compiled for a Cortex-M3 with no libc,
 * which cannot run the assertions. Keeping the two apart also means the firmware
 * build stays exactly what is flashed.
 */

#include "test_support.h"

/* Suites, one per module under test. Add the new suite here when a module is
 * added to hardware/core. */
void test_text_format_suite(void);
void test_env_monitor_suite(void);
void test_display_model_suite(void);
void test_json_writer_suite(void);
void test_telemetry_json_suite(void);
void test_command_json_suite(void);
void test_mqtt_packet_suite(void);
void test_control_link_suite(void);
void test_session_dispatch_suite(void);
void test_threshold_store_suite(void);
void test_boot_id_suite(void);

int main(void)
{
    test_run_suite("text_format", test_text_format_suite);
    test_run_suite("env_monitor", test_env_monitor_suite);
    test_run_suite("display_model", test_display_model_suite);
    test_run_suite("json_writer", test_json_writer_suite);
    test_run_suite("telemetry_json", test_telemetry_json_suite);
    test_run_suite("command_json", test_command_json_suite);
    test_run_suite("mqtt_packet", test_mqtt_packet_suite);
    test_run_suite("control_link", test_control_link_suite);
    test_run_suite("session_dispatch", test_session_dispatch_suite);
    test_run_suite("threshold_store", test_threshold_store_suite);
    test_run_suite("boot_id", test_boot_id_suite);
    return test_finish();
}
