# Host test project for the pure firmware logic in ../core.
#
# It is a separate CMake project from the firmware on purpose: the firmware is
# cross-compiled for a Cortex-M3 with -nostdlib, so its objects cannot be linked
# into a host executable. The sources under test are shared, so the same code is
# what runs on the board and what the assertions exercise.
#
# Build and run:
#   cmake -S hardware/tests -B hardware/build/host-tests
#   cmake --build hardware/build/host-tests
#   ./hardware/build/host-tests/host_tests
#
# With line coverage:
#   cmake -S hardware/tests -B hardware/build/host-tests-coverage -DENABLE_COVERAGE=ON
#   cmake --build hardware/build/host-tests-coverage
#   hardware/scripts/host_coverage.sh hardware/build/host-tests-coverage

cmake_minimum_required(VERSION 3.20)

project(hardware_host_tests LANGUAGES C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

option(ENABLE_COVERAGE "Instrument the host tests for LLVM source-based line coverage" OFF)

set(CORE_SOURCES
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/text_format.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/env_monitor.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/display_model.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/json_writer.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/telemetry_json.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/command_json.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/mqtt_packet.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/control_link.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/session_dispatch.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/threshold_store.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/../core/boot_id.c"
)

set(TEST_SOURCES
    "${CMAKE_CURRENT_SOURCE_DIR}/main.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_support.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_text_format.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_env_monitor.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_display_model.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_json_writer.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_telemetry_json.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_command_json.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_mqtt_packet.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_control_link.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_session_dispatch.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_threshold_store.c"
    "${CMAKE_CURRENT_SOURCE_DIR}/test_boot_id.c"
)

add_executable(host_tests ${CORE_SOURCES} ${TEST_SOURCES})

target_include_directories(host_tests PRIVATE
    "${CMAKE_CURRENT_SOURCE_DIR}/../core"
    "${CMAKE_CURRENT_SOURCE_DIR}"
    # app_config.h holds the compile-time defaults the core derives from, so the
    # host tests exercise the same numbers the board uses.
    "${CMAKE_CURRENT_SOURCE_DIR}/../STM32_Project1/User"
)

# The firmware build uses the same warning set but not -Werror, because a
# warning in a vendor header must not block a release. Here every warning is an
# error: the core modules are ours, and a warning in test-visible code is a
# defect that would otherwise ship.
target_compile_options(host_tests PRIVATE -Wall -Wextra -Werror -Wshadow -Wconversion)

if(ENABLE_COVERAGE)
    if(NOT CMAKE_C_COMPILER_ID MATCHES "Clang")
        message(FATAL_ERROR "ENABLE_COVERAGE requires Clang and llvm-profdata/llvm-cov")
    endif()
    # Clang's own profile format rather than --coverage: on macOS the gcov
    # toolchain looks for "<name>.gcno" while clang writes "<name>.c.gcno", so
    # gcov cannot find the data. The profile format is the native path and is
    # read with llvm-profdata and llvm-cov, which ship with the toolchain.
    target_compile_options(host_tests PRIVATE -fprofile-instr-generate -fcoverage-mapping -O0 -g)
    target_link_options(host_tests PRIVATE -fprofile-instr-generate)
endif()

enable_testing()
add_test(NAME host_tests COMMAND host_tests)
