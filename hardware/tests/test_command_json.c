#include "command_json.h"
#include "test_support.h"

/* A well-formed set_thresholds command. Also the generic parse vehicle for
 * cases that need any valid command at all. */
static const char *THRESHOLD_COMMAND =
    "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
    "\"requestId\":\"01K5H0PN0M1N9NB8B7RBTVWT8P\",\"issuedAt\":1790246400000,"
    "\"expiresAt\":1790246460000,\"type\":\"set_thresholds\",\"payload\":"
    "{\"thresholdVersion\":4,\"temperatureHighC\":30.0,\"humidityHighRh\":80.0,\"gasHighPpm\":80.0}}";

/* Parse a command with no threshold version in force. */
static CommandResult parse(const char *json, ControlCommand *command)
{
    return CommandJsonParse(json, (uint32_t)strlen(json), "MCU001", 0U, command);
}

/* Exercise the threshold command. */
static void test_threshold_command(void)
{
    ControlCommand command;
    CommandResult result;

    TEST_CASE("a well-formed threshold command is accepted");
    result = parse(THRESHOLD_COMMAND, &command);
    CHECK_INT(COMMAND_RESULT_APPLIED, result);
    CHECK_INT(COMMAND_SET_THRESHOLDS, command.type);
    CHECK_INT(4U, command.threshold_version);
    CHECK_INT(30U, command.thresholds.temperature_high_c);
    CHECK_INT(80U, command.thresholds.humidity_high_rh);
    CHECK_INT(80U, command.thresholds.gas_high_ppm);
    /* The rise thresholds are not in the frozen payload, so the compile-time
     * values stay in force. Zeroing them would alarm on every evaluation. */
    CHECK_INT(ENV_DEFAULT_TEMPERATURE_RISE_C, command.thresholds.temperature_rise_c);
    CHECK_INT(ENV_DEFAULT_GAS_RISE_ADC, command.thresholds.gas_rise_adc);

    TEST_CASE("a fractional limit is rounded to the sensor's resolution");
    {
        const char *fractional =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":30.6,\"humidityHighRh\":79.4,\"gasHighPpm\":80.5}}";
        /* The DHT11 resolves one degree, so 30.6 cannot be enforced as written.
         * Rounding is documented rather than truncating, which would move every
         * limit down without saying so. */
        result = parse(fractional, &command);
        CHECK_INT(COMMAND_RESULT_APPLIED, result);
        CHECK_INT(31U, command.thresholds.temperature_high_c);
        CHECK_INT(79U, command.thresholds.humidity_high_rh);
        CHECK_INT(81U, command.thresholds.gas_high_ppm);
    }

    TEST_CASE("a version that does not move forward is rejected as stale");
    result = CommandJsonParse(THRESHOLD_COMMAND, (uint32_t)strlen(THRESHOLD_COMMAND), "MCU001", 4U, &command);
    CHECK_INT(COMMAND_RESULT_REJECTED_STALE_VERSION, result);
    /* The requestId is still extracted, so the rejection can be acknowledged
     * with the identifier the backend is waiting on. */
    CHECK_INT(0, strcmp(command.request_id, "01K5H0PN0M1N9NB8B7RBTVWT8P"));

    TEST_CASE("an older version is rejected too");
    result = CommandJsonParse(THRESHOLD_COMMAND, (uint32_t)strlen(THRESHOLD_COMMAND), "MCU001", 5U, &command);
    CHECK_INT(COMMAND_RESULT_REJECTED_STALE_VERSION, result);

    TEST_CASE("a version of zero is rejected");
    {
        const char *zero =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":0,"
            "\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, parse(zero, &command));
    }
}

/* Exercise the envelope validation, which is what keeps a wrong-device command
 * from being acted on. */
static void test_envelope_rejections(void)
{
    ControlCommand command;

    TEST_CASE("an unsupported schema version is rejected");
    {
        const char *json =
            "{\"schemaVersion\":2,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_SCHEMA, parse(json, &command));
    }

    TEST_CASE("a command for another device is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU009\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_DEVICE, parse(json, &command));
    }

    TEST_CASE("a prefix of the device id is not a match");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU00\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_DEVICE, parse(json, &command));
    }

    TEST_CASE("an unknown command type is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_everything\",\"payload\":{}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_TYPE, parse(json, &command));
    }

    TEST_CASE("a retired set_mute command is rejected as an unknown type");
    {
        /* The remote-mute capability was deleted. An old client that still
         * sends set_mute is told the type is not recognized rather than having
         * its payload quietly reinterpreted as thresholds. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_mute\",\"payload\":{\"muted\":true}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_TYPE, parse(json, &command));
    }

    TEST_CASE("a telemetry message on the control topic is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"telemetry\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_TYPE, parse(json, &command));
    }

    TEST_CASE("a deadline that is not after the issue time is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":2000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, parse(json, &command));
    }

    TEST_CASE("a validity window longer than a day is rejected");
    {
        /* A command that stays valid for days is either a defect or an attempt
         * to have the device act long after the operator stopped watching. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":86401001,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, parse(json, &command));
    }

    TEST_CASE("a missing required field is rejected as malformed");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        /* Without a requestId there is nothing to acknowledge, so the payload is
         * counted rather than answered. */
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("an unknown field is rejected rather than ignored");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"extra\":1,\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("a malformed document is rejected");
    CHECK_INT(COMMAND_RESULT_MALFORMED, parse("{", &command));
    CHECK_INT(COMMAND_RESULT_MALFORMED, parse("", &command));
    CHECK_INT(COMMAND_RESULT_MALFORMED, parse("[]", &command));
    CHECK_INT(COMMAND_RESULT_MALFORMED, parse("{\"schemaVersion\":1", &command));

    TEST_CASE("a unicode escape is refused rather than guessed at");
    {
        /* Every string in this contract is an ASCII identifier, so a \\u escape
         * means the sender is not speaking the frozen contract. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU\\u0030\\u0030\\u0031\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("a null argument is rejected");
    CHECK_INT(COMMAND_RESULT_MALFORMED, CommandJsonParse(NULL, 10U, "MCU001", 0U, &command));
    CHECK_INT(COMMAND_RESULT_MALFORMED, CommandJsonParse(THRESHOLD_COMMAND, (uint32_t)strlen(THRESHOLD_COMMAND), "MCU001", 0U, NULL));
}

/* Exercise the payload validation. */
static void test_payload_rejections(void)
{
    ControlCommand command;

    TEST_CASE("a partial threshold set is rejected rather than partly applied");
    {
        /* Applying half the values would leave the device enforcing a mixture
         * the reported version could not describe. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":30,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, parse(json, &command));
    }

    TEST_CASE("a threshold above the accepted maximum is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":200,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, parse(json, &command));
    }

    TEST_CASE("a gas threshold that rounds to zero is rejected");
    {
        /* A zero gas limit would alarm on every sample, which is why the
         * rounded value is checked and not only the parsed one. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":0.2}}";
        CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, parse(json, &command));
    }

    TEST_CASE("an unknown payload field is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80,\"volume\":5}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("a payload that is not an object is rejected");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":[]}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }
}

/* Exercise the lexical details of the scanner: whitespace, escapes and
 * truncation. A client that pretty-prints its JSON, or escapes a character in an
 * identifier, must be handled the same way as one that does not. */
static void test_lexical_edges(void)
{
    ControlCommand command;

    TEST_CASE("a pretty-printed command is accepted");
    {
        /* Whitespace between tokens is ordinary JSON, so a client that formats
         * its output must not be rejected. */
        const char *json =
            "{\n"
            "  \"schemaVersion\": 1,\n"
            "  \"messageType\": \"control\",\n"
            "  \"deviceId\": \"MCU001\",\n"
            "  \"requestId\": \"01REQ\",\n"
            "  \"issuedAt\": 1000,\n"
            "  \"expiresAt\": 2000,\n"
            "  \"type\": \"set_thresholds\",\n"
            "  \"payload\": { \"thresholdVersion\": 2, \"temperatureHighC\": 30, \"humidityHighRh\": 80, \"gasHighPpm\": 80 }\n"
            "}";
        CHECK_INT(COMMAND_RESULT_APPLIED, parse(json, &command));
        CHECK_INT(COMMAND_SET_THRESHOLDS, command.type);
        CHECK_INT(0, strcmp(command.request_id, "01REQ"));
    }

    TEST_CASE("an escaped character inside an identifier is decoded");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"REQ\\/A\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_APPLIED, parse(json, &command));
        CHECK_INT(0, strcmp(command.request_id, "REQ/A"));
    }

    TEST_CASE("the other supported escapes are decoded too");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"A\\nB\\tC\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_APPLIED, parse(json, &command));
        CHECK_INT(0, strcmp(command.request_id, "A\nB\tC"));
    }

    TEST_CASE("an escape the scanner does not know is refused");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"A\\qB\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("an identifier too long for the field is refused");
    {
        /* Thirty-three characters where the field holds thirty-two: truncating
         * would acknowledge a different requestId than the one received. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"0123456789012345678901234567890123\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("an unterminated string is refused");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ,\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("a negative timestamp is refused");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":-1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("exponent notation in a threshold is refused");
    {
        /* The backend does not send one, and interpreting it would need floating
         * point, so it is refused rather than guessed at. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":3e1,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("more than two fraction digits are refused");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":30.123,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("a string value where a number belongs is refused");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,"
            "\"temperatureHighC\":\"30\",\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("a string with an escaped quote inside the skipped payload is handled");
    {
        /* The payload is skipped whole before it is scanned, so a string inside
         * it that contains an escaped quote must not be mistaken for the end of
         * the string, which would leave the skip at the wrong depth. */
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"01REQ\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80,\"note\":\"a\\\"b\"}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }

    TEST_CASE("an empty identifier is refused");
    {
        const char *json =
            "{\"schemaVersion\":1,\"messageType\":\"control\",\"deviceId\":\"MCU001\","
            "\"requestId\":\"\",\"issuedAt\":1000,\"expiresAt\":2000,"
            "\"type\":\"set_thresholds\",\"payload\":{\"thresholdVersion\":2,\"temperatureHighC\":30,\"humidityHighRh\":80,\"gasHighPpm\":80}}";
        CHECK_INT(COMMAND_RESULT_MALFORMED, parse(json, &command));
    }
}

/* Exercise the window check. */
static void test_window(void)
{
    ControlCommand command;

    TEST_CASE("a command inside its window is live");
    CHECK_INT(COMMAND_RESULT_APPLIED, parse(THRESHOLD_COMMAND, &command));
    CHECK_TRUE(CommandWithinWindow(&command, 1000U, 1000U));
    CHECK_TRUE(CommandWithinWindow(&command, 1000U, 61000U));

    TEST_CASE("a command past its window is not");
    CHECK_FALSE(CommandWithinWindow(&command, 1000U, 61001U));

    TEST_CASE("the window is measured from arrival, not from an absolute clock");
    /* The device has no trusted wall clock, which is exactly why the contract
     * expresses the allowance as a duration from receipt. */
    CHECK_TRUE(CommandWithinWindow(&command, 4000000000U, 4000059000U));

    TEST_CASE("a null command is not live");
    CHECK_FALSE(CommandWithinWindow(NULL, 0U, 0U));
}

/* Exercise the deduplication ring. */
static void test_dedup(void)
{
    CommandDedup dedup;
    CommandResult result = COMMAND_RESULT_APPLIED;

    TEST_CASE("an empty ring remembers nothing");
    CommandDedupInit(&dedup);
    CHECK_FALSE(CommandDedupLookup(&dedup, "01A", &result));

    TEST_CASE("a recorded request id is found with its result");
    CommandDedupRecord(&dedup, "01A", COMMAND_RESULT_APPLIED);
    CHECK_TRUE(CommandDedupLookup(&dedup, "01A", &result));
    CHECK_INT(COMMAND_RESULT_APPLIED, result);

    CommandDedupRecord(&dedup, "01B", COMMAND_RESULT_REJECTED_RANGE);
    CHECK_TRUE(CommandDedupLookup(&dedup, "01B", &result));
    CHECK_INT(COMMAND_RESULT_REJECTED_RANGE, result);

    TEST_CASE("a different id is not matched by a prefix");
    CHECK_FALSE(CommandDedupLookup(&dedup, "01", &result));
    CHECK_FALSE(CommandDedupLookup(&dedup, "01AA", &result));

    TEST_CASE("the ring evicts the oldest entry when it fills");
    {
        uint32_t index;

        for (index = 0U; index < COMMAND_DEDUP_SLOTS; index++)
        {
            char id[8];

            id[0] = 'A';
            id[1] = (char)('0' + (char)index);
            id[2] = '\0';
            CommandDedupRecord(&dedup, id, COMMAND_RESULT_APPLIED);
        }
        /* "01A" and "01B" have been pushed out by the eight new entries. */
        CHECK_FALSE(CommandDedupLookup(&dedup, "01A", &result));
        CHECK_TRUE(CommandDedupLookup(&dedup, "A0", &result));
        CHECK_TRUE(CommandDedupLookup(&dedup, "A7", &result));
    }

    TEST_CASE("a null ring or id is tolerated");
    CommandDedupInit(NULL);
    CommandDedupRecord(NULL, "01A", COMMAND_RESULT_APPLIED);
    CHECK_FALSE(CommandDedupLookup(NULL, "01A", &result));
    CommandDedupRecord(&dedup, NULL, COMMAND_RESULT_APPLIED);
    CHECK_FALSE(CommandDedupLookup(&dedup, NULL, &result));

    TEST_CASE("a lookup with no result pointer still reports presence");
    CHECK_TRUE(CommandDedupLookup(&dedup, "A7", NULL));
}

/* Exercise the acknowledgement vocabulary, which the backend matches
 * exhaustively. */
static void test_ack_vocabulary(void)
{
    TEST_CASE("each result maps to the frozen status");
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_APPLIED), "applied"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_DUPLICATE), "duplicate"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_EXPIRED), "expired"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_REJECTED_SCHEMA), "rejected"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_REJECTED_DEVICE), "rejected"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_REJECTED_TYPE), "rejected"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_REJECTED_RANGE), "rejected"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_REJECTED_STALE_VERSION), "rejected"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_FAILED_FLASH_WRITE), "failed"));
    CHECK_INT(0, strcmp(CommandAckStatus(COMMAND_RESULT_FAILED_FLASH_VERIFY), "failed"));

    TEST_CASE("each failure carries the frozen error code");
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_REJECTED_SCHEMA), "schema_unsupported"));
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_REJECTED_DEVICE), "device_mismatch"));
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_REJECTED_TYPE), "bad_request_type"));
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_REJECTED_RANGE), "out_of_range"));
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_REJECTED_STALE_VERSION), "stale_version"));
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_FAILED_FLASH_WRITE), "flash_write_failed"));
    CHECK_INT(0, strcmp(CommandAckErrorCode(COMMAND_RESULT_FAILED_FLASH_VERIFY), "flash_verify_failed"));

    TEST_CASE("a success carries no error code, as the contract requires");
    CHECK_TRUE(CommandAckErrorCode(COMMAND_RESULT_APPLIED) == NULL);
    CHECK_TRUE(CommandAckErrorCode(COMMAND_RESULT_DUPLICATE) == NULL);
    CHECK_TRUE(CommandAckErrorCode(COMMAND_RESULT_EXPIRED) == NULL);
}

/* Exercise the acknowledgement payload. */
static void test_ack_payload(void)
{
    char buffer[384];
    CommandAckPayload payload;
    uint32_t length;

    payload.device_id = "MCU001";
    payload.boot_id = "9f3ac21b";
    payload.sequence = 43U;
    payload.uptime_ms = 126000U;
    payload.request_id = "01K5H0PN0M1N9NB8B7RBTVWT8P";
    payload.result = COMMAND_RESULT_APPLIED;
    payload.threshold_version = 4U;

    TEST_CASE("an applied command reports the version it adopted");
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(length > 0U);
    CHECK_TRUE(test_json_is_well_formed(buffer, length));
    CHECK_TRUE(test_json_has_string(buffer, "messageType", "command_ack"));
    CHECK_TRUE(test_json_has_string(buffer, "requestId", "01K5H0PN0M1N9NB8B7RBTVWT8P"));
    CHECK_TRUE(test_json_has_string(buffer, "status", "applied"));
    CHECK_TRUE(test_json_has_number(buffer, "thresholdVersion", "4"));
    CHECK_TRUE(test_json_has_number(buffer, "errorCode", "null"));
    CHECK_TRUE(test_json_has_number(buffer, "timestamp", "null"));
    CHECK_TRUE(test_json_has_number(buffer, "sequence", "43"));

    TEST_CASE("a rejected command carries the reason and no version");
    payload.result = COMMAND_RESULT_REJECTED_RANGE;
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_is_well_formed(buffer, length));
    CHECK_TRUE(test_json_has_string(buffer, "status", "rejected"));
    CHECK_TRUE(test_json_has_string(buffer, "errorCode", "out_of_range"));
    /* Reporting a version for a command that did not change it would tell the
     * backend the device adopted a configuration it refused. */
    CHECK_TRUE(test_json_has_number(buffer, "thresholdVersion", "null"));

    TEST_CASE("a failed Flash write is reported as failed");
    payload.result = COMMAND_RESULT_FAILED_FLASH_WRITE;
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_string(buffer, "status", "failed"));
    CHECK_TRUE(test_json_has_string(buffer, "errorCode", "flash_write_failed"));

    TEST_CASE("an expired command carries no error code");
    payload.result = COMMAND_RESULT_EXPIRED;
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_string(buffer, "status", "expired"));
    CHECK_TRUE(test_json_has_number(buffer, "errorCode", "null"));

    TEST_CASE("a duplicate threshold command still reports the version in force");
    payload.result = COMMAND_RESULT_DUPLICATE;
    payload.threshold_version = 4U;
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_string(buffer, "status", "duplicate"));
    /* docs/device-protocol.md §6 requires applied *and* duplicate to carry the
     * device's current version, so a redelivered threshold command does not read
     * to the backend as a device with no configuration. */
    CHECK_TRUE(test_json_has_number(buffer, "thresholdVersion", "4"));

    TEST_CASE("a result with no adopted version reports null");
    payload.result = COMMAND_RESULT_DUPLICATE;
    payload.threshold_version = 0U;
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_string(buffer, "status", "duplicate"));
    CHECK_TRUE(test_json_has_number(buffer, "thresholdVersion", "null"));

    TEST_CASE("an applied command that adopted no version reports null");
    /* Only a threshold command is applied from here on, and every one of those
     * carries a version, so this is the shape a future non-threshold result
     * would have to use. */
    payload.result = COMMAND_RESULT_APPLIED;
    payload.threshold_version = 0U;
    length = CommandAckJsonEncode(&payload, buffer, sizeof(buffer));
    CHECK_TRUE(test_json_has_string(buffer, "status", "applied"));
    CHECK_TRUE(test_json_has_number(buffer, "thresholdVersion", "null"));

    TEST_CASE("a small buffer is refused and null arguments are tolerated");
    CHECK_INT(0U, CommandAckJsonEncode(&payload, buffer, 16U));
    CHECK_INT(0U, CommandAckJsonEncode(NULL, buffer, sizeof(buffer)));
    CHECK_INT(0U, CommandAckJsonEncode(&payload, NULL, sizeof(buffer)));
}

/* Entry point for the command_json suite. */
void test_command_json_suite(void)
{
    test_threshold_command();
    test_envelope_rejections();
    test_payload_rejections();
    test_window();
    test_lexical_edges();
    test_dedup();
    test_ack_vocabulary();
    test_ack_payload();
}
