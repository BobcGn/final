#ifndef __COMMAND_JSON_H
#define __COMMAND_JSON_H

/*
 * Control command parsing, deduplication and acknowledgement for the device side.
 *
 * The device is the last line of defence for the control link, so the parser is
 * deliberately strict: every field it needs is required to be present, every
 * value is range-checked, and a payload whose shape it does not recognise is
 * counted and left unacknowledged rather than half-processed.
 *
 * Why an unrecognised payload gets no acknowledgement at all: the frozen
 * contract requires every acknowledgement to carry the requestId of the command
 * it answers. A payload that cannot be parsed far enough to yield a requestId
 * cannot produce a conforming acknowledgement, and inventing one would tell the
 * backend a command was rejected when the device never identified which command
 * it was. The caller counts these separately.
 *
 * This module performs no I/O: it turns bytes into a validated command and turns
 * a result into an acknowledgement payload. Flash writes, publication and the
 * clock stay with the caller, which is what makes all of it testable on a host.
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "env_monitor.h"

/* The only control schema version this firmware understands. */
#define COMMAND_SCHEMA_VERSION 1U

/* Upper bounds, matching the backend's parameter limits. */
#define COMMAND_REQUEST_ID_MAX 32U
#define COMMAND_DEVICE_ID_MAX 32U

/* Longest command validity window the device will accept. The backend issues
 * 60 seconds; a command claiming a day of validity is either a defect or an
 * attempt to have the device act long after the operator stopped watching. */
#define COMMAND_MAX_WINDOW_MS 86400000UL

/* Command types. */
typedef enum
{
    COMMAND_SET_THRESHOLDS
} CommandType;

/* Outcome of parsing and validating a command.
 *
 * The values map one for one onto the frozen acknowledgement vocabulary: the two
 * FailedFlash* values and Applied, Expired, Duplicate and the Rejected* values
 * are all expressible in the contract's status and errorCode fields, and the
 * mapping lives in one place so a change cannot be applied to only one of them. */
typedef enum
{
    /* Recorded for a command the device carried out. */
    COMMAND_RESULT_APPLIED = 0,
    /* The device already handled this requestId; the recorded result stands. */
    COMMAND_RESULT_DUPLICATE,
    /* The command's deadline had already passed when it was received. */
    COMMAND_RESULT_EXPIRED,
    /* schemaVersion is not the one this firmware speaks. */
    COMMAND_RESULT_REJECTED_SCHEMA,
    /* The command was addressed to a different device. */
    COMMAND_RESULT_REJECTED_DEVICE,
    /* The type is not one this firmware implements. */
    COMMAND_RESULT_REJECTED_TYPE,
    /* A value is outside its accepted range. */
    COMMAND_RESULT_REJECTED_RANGE,
    /* The command's threshold version does not move forward. */
    COMMAND_RESULT_REJECTED_STALE_VERSION,
    /* The Flash write of a new threshold set did not complete. */
    COMMAND_RESULT_FAILED_FLASH_WRITE,
    /* The Flash content did not read back as it was written. */
    COMMAND_RESULT_FAILED_FLASH_VERIFY,
    /* The payload could not be parsed, so it cannot be acknowledged. */
    COMMAND_RESULT_MALFORMED
} CommandResult;

/* Return the frozen `status` string for a result. */
const char *CommandAckStatus(CommandResult result);

/* Return the frozen `errorCode` string for a result, or NULL when the status
 * does not carry one. The contract forbids an errorCode on a success. */
const char *CommandAckErrorCode(CommandResult result);

/* A parsed and validated control command. */
typedef struct
{
    CommandType type;
    char request_id[COMMAND_REQUEST_ID_MAX + 1U];
    /* expiresAt - issuedAt, in milliseconds.
     *
     * The device has no wall clock it can trust, so the contract prescribes
     * using the window the backend declared and measuring it from the moment the
     * command was received. Storing the duration rather than an instant is what
     * makes that possible without a clock. */
    uint32_t window_ms;
    /* set_thresholds */
    uint32_t threshold_version;
    EnvThresholds thresholds;
} ControlCommand;

/* Parse and validate a device/control payload.
 *
 * `device_id` is this device's own identifier; a command addressed to another
 * device is refused rather than acted on.
 *
 * Scope is checked by the caller, which owns the clock: the parser reports the
 * window, and compares `threshold_version` against the version in force only
 * when the caller passes a non-zero `current_threshold_version`. Pass 0 to skip
 * that comparison, for example when the command is being inspected without being
 * applied.
 *
 * The control path passes 0 and applies CommandCheckThresholdVersion itself,
 * because docs/device-protocol.md §4.1 orders the requestId deduplication check
 * before the range and version checks: comparing here would answer a redelivered
 * threshold command with stale_version instead of duplicate, and a backend that
 * already recorded the command as applied would read that as a fresh failure. */
CommandResult CommandJsonParse(const char *json, uint32_t length, const char *device_id,
                              uint32_t current_threshold_version, ControlCommand *command);

/* Report whether a threshold command's version moves the configuration forward.
 *
 * Returns COMMAND_RESULT_REJECTED_STALE_VERSION when the command claims a version
 * that is not newer than the one in force, and COMMAND_RESULT_APPLIED otherwise —
 * including for a command that is not a threshold set at all, which has no
 * version to order. A zero `current_threshold_version` skips the comparison, for
 * a caller inspecting a command without applying it.
 *
 * The parser applies this too, so the ordering rule has one implementation and
 * the control path only decides *when* to ask. */
CommandResult CommandCheckThresholdVersion(const ControlCommand *command,
                                          uint32_t current_threshold_version);

/* Return true when a received command is still inside its window. The caller
 * passes the uptime recorded when the command arrived. */
bool CommandWithinWindow(const ControlCommand *command, uint32_t received_uptime_ms, uint32_t now_ms);

/* How many requestIds the device remembers. The contract requires the device to
 * remember enough to answer a redelivered command with its original result; a
 * battery of eight covers any plausible redelivery window at the 60-second
 * validity the backend issues. */
#define COMMAND_DEDUP_SLOTS 8U

/* Ring of remembered requestIds and their results. */
typedef struct
{
    char request_ids[COMMAND_DEDUP_SLOTS][COMMAND_REQUEST_ID_MAX + 1U];
    uint8_t results[COMMAND_DEDUP_SLOTS];
    uint8_t count;
    uint8_t next;
} CommandDedup;

/* Clear the ring. */
void CommandDedupInit(CommandDedup *dedup);

/* Look up a requestId. Returns true and fills `result` when it is remembered. */
bool CommandDedupLookup(const CommandDedup *dedup, const char *request_id, CommandResult *result);

/* Remember a requestId and its result, evicting the oldest entry when full.
 *
 * The ring survives only in RAM, so a reboot forgets it. The contract accepts
 * that: a redelivered command after a restart is treated as new, and the
 * deadline still stops an old one from being carried out. */
void CommandDedupRecord(CommandDedup *dedup, const char *request_id, CommandResult result);

/* Everything an acknowledgement carries. */
typedef struct
{
    const char *device_id;
    const char *boot_id;
    uint32_t sequence;
    uint32_t uptime_ms;
    const char *request_id;
    CommandResult result;
    /* Threshold version to report. It is emitted for a threshold command whose
     * result is applied or duplicate — the newly applied version and the version
     * currently in force respectively — and is written as null for every other
     * combination. Pass 0 to force null. */
    uint32_t threshold_version;
} CommandAckPayload;

/* Write the acknowledgement payload following docs/device-protocol.md §6.
 * Returns the length written, or 0 when it does not fit. */
uint32_t CommandAckJsonEncode(const CommandAckPayload *payload, char *buffer, uint32_t capacity);

#endif /* __COMMAND_JSON_H */
