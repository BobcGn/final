#!/bin/sh
#
# Line coverage for the pure firmware logic in hardware/core.
#
# Usage:
#   scripts/host_coverage.sh [build-directory]
#
# The build directory must have been configured with -DENABLE_COVERAGE=ON. The
# script builds nothing itself, so it can be pointed at an existing instrumented
# tree:
#
#   cmake -S hardware/tests -B hardware/build/host-tests-coverage -DENABLE_COVERAGE=ON
#   cmake --build hardware/build/host-tests-coverage
#   hardware/scripts/host_coverage.sh hardware/build/host-tests-coverage
#
# Only the modules under hardware/core are reported: the test sources are not
# part of the deliverable and their coverage says nothing about the firmware.
#
# llvm-profdata and llvm-cov are used rather than gcov because clang writes
# "<name>.c.gcno" while gcov looks for "<name>.gcno", so gcov cannot find the
# instrumentation it just produced.
#
# Exits non-zero when the covered fraction of core lines falls below the
# threshold hardware/AGENTS.md requires.

set -eu

BUILD_DIR="${1:-hardware/build/host-tests-coverage}"
THRESHOLD=80

if [ ! -d "$BUILD_DIR" ]; then
    echo "error: build directory '$BUILD_DIR' does not exist" >&2
    echo "hint: cmake -S hardware/tests -B $BUILD_DIR -DENABLE_COVERAGE=ON" >&2
    exit 2
fi

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CORE_DIR="$REPO_ROOT/hardware/core"

# Every module under hardware/core. Kept in one place so a module cannot be added
# to the build and forgotten here, which would quietly understate the coverage.
CORE_MODULES="\
    $CORE_DIR/text_format.c \
    $CORE_DIR/env_monitor.c \
    $CORE_DIR/display_model.c \
    $CORE_DIR/json_writer.c \
    $CORE_DIR/telemetry_json.c \
    $CORE_DIR/command_json.c \
    $CORE_DIR/mqtt_packet.c \
    $CORE_DIR/control_link.c \
    $CORE_DIR/session_dispatch.c \
    $CORE_DIR/threshold_store.c \
    $CORE_DIR/boot_id.c"

if ! [ -f "$BUILD_DIR/host_tests" ]; then
    echo "error: '$BUILD_DIR/host_tests' not found; build the test target first" >&2
    exit 2
fi

PROFDATA="$BUILD_DIR/host_tests.profdata"

echo "Running the host tests under instrumentation..."
( cd "$BUILD_DIR" && LLVM_PROFILE_FILE=host_tests.profraw ./host_tests >/dev/null )

if command -v xcrun >/dev/null 2>&1 && xcrun --find llvm-profdata >/dev/null 2>&1; then
    PROFATA="xcrun llvm-profdata"
    COV="xcrun llvm-cov"
else
    PROFATA="llvm-profdata"
    COV="llvm-cov"
fi

# shellcheck disable=SC2086
$PROFATA merge -sparse "$BUILD_DIR/host_tests.profraw" -o "$PROFDATA"

echo
echo "Line coverage for hardware/core:"
echo
# shellcheck disable=SC2086
# shellcheck disable=SC2086
$COV report "$BUILD_DIR/host_tests" -instr-profile="$PROFDATA" $CORE_MODULES

echo
echo "Lines with no execution (llvm-cov prints 'line| 0|source'):"
echo
# shellcheck disable=SC2086
$COV show "$BUILD_DIR/host_tests" -instr-profile="$PROFDATA" $CORE_MODULES \
    2>/dev/null | grep '|  *0|' || echo "  (none)"

echo
# Compute the aggregate over every core file from the report table. In llvm-cov's
# TOTAL row column 8 is the total line count and column 9 is the missed line
# count; treating column 8 as covered would inflate both the denominator and the
# percentage reported by this gate.
# shellcheck disable=SC2086
SUMMARY=$(# shellcheck disable=SC2086
$COV report "$BUILD_DIR/host_tests" -instr-profile="$PROFDATA" $CORE_MODULES \
    --format=text 2>/dev/null | tail -1)

LINE_TOTAL=$(echo "$SUMMARY" | awk '{print $8}')
MISSED=$(echo "$SUMMARY" | awk '{print $9}')
COVERED=$((LINE_TOTAL - MISSED))
if [ "$LINE_TOTAL" -eq 0 ]; then
    echo "error: no core lines were instrumented" >&2
    exit 2
fi
PERCENT=$((COVERED * 100 / LINE_TOTAL))

echo "Aggregate core line coverage: $COVERED/$LINE_TOTAL = ${PERCENT}% (threshold ${THRESHOLD}%)"

if [ "$PERCENT" -lt "$THRESHOLD" ]; then
    echo "FAIL: core line coverage is below the required ${THRESHOLD}%" >&2
    exit 1
fi

echo "OK: core line coverage meets the required ${THRESHOLD}%"
