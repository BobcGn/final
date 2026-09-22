#include "command_json.h"
#include "json_writer.h"
#include "text_format.h"

/* Field presence and raw values collected from the envelope. */
typedef struct
{
    bool has_schema_version;
    bool has_message_type;
    bool has_device_id;
    bool has_request_id;
    bool has_issued_at;
    bool has_expires_at;
    bool has_type;
    bool has_payload;
    uint32_t payload_start;
    uint32_t payload_length;

    bool has_threshold_version;
    bool has_temperature_high;
    bool has_humidity_high;
    bool has_gas_high;

    bool schema_version_supported;
    bool message_type_control;
    char device_id[COMMAND_DEVICE_ID_MAX + 1U];
    char request_id[COMMAND_REQUEST_ID_MAX + 1U];
    uint64_t issued_at;
    uint64_t expires_at;
    char type[24];

    uint32_t threshold_version;
    /* Threshold values are read in tenths so a fractional value from the client
     * is not silently truncated by the parser; the rounding decision is made
     * once, after the range check. */
    uint32_t temperature_high_tenths;
    uint32_t humidity_high_tenths;
    uint32_t gas_high_tenths;
} CommandFields;

/* Cursor over the payload. */
typedef struct
{
    const char *text;
    uint32_t length;
    uint32_t position;
} Scanner;

/* Compare a key against an expected name.
 *
 * The comparison is over the whole key. Matching only a prefix would let an
 * unknown field be silently accepted as whichever known field shared its first
 * characters: "extra" would have been read as "expiresAt". */
static bool key_equals(const char *key, const char *expected)
{
    uint32_t index = 0U;

    while (key[index] != '\0' && expected[index] != '\0')
    {
        if (key[index] != expected[index])
        {
            return false;
        }
        index++;
    }
    return key[index] == '\0' && expected[index] == '\0';
}

/* Copy a string into `out`, bounding the copy. `out` is always terminated. */
static void copy_bounded(char *out, uint32_t capacity, const char *source, uint32_t length)
{
    uint32_t index = 0U;

    if (capacity == 0U)
    {
        return;
    }
    while (index < length && index + 1U < capacity && source[index] != '\0')
    {
        out[index] = source[index];
        index++;
    }
    out[index] = '\0';
}

static void skip_whitespace(Scanner *scanner)
{
    while (scanner->position < scanner->length)
    {
        char value = scanner->text[scanner->position];

        if (value != ' ' && value != '\t' && value != '\n' && value != '\r')
        {
            return;
        }
        scanner->position++;
    }
}

/* Consume `expected`, or fail. */
static bool expect_char(Scanner *scanner, char expected)
{
    skip_whitespace(scanner);
    if (scanner->position >= scanner->length || scanner->text[scanner->position] != expected)
    {
        return false;
    }
    scanner->position++;
    return true;
}

/* Peek the next non-whitespace character, or 0 at the end. */
static char peek_char(Scanner *scanner)
{
    skip_whitespace(scanner);
    if (scanner->position >= scanner->length)
    {
        return '\0';
    }
    return scanner->text[scanner->position];
}

/* Read a JSON string into `out`.
 *
 * The simple escapes are decoded. \u is rejected rather than interpreted: every
 * string this device handles is an ASCII identifier or type name, so a \u escape
 * means the sender is not speaking the frozen contract, and guessing at the code
 * point would be worse than refusing. */
static bool scan_string(Scanner *scanner, char *out, uint32_t capacity, uint32_t *out_length)
{
    uint32_t written = 0U;

    if (!expect_char(scanner, '"'))
    {
        return false;
    }
    while (scanner->position < scanner->length)
    {
        char value = scanner->text[scanner->position++];

        if (value == '"')
        {
            if (capacity > 0U)
            {
                out[written < capacity ? written : capacity - 1U] = '\0';
            }
            if (out_length != NULL)
            {
                *out_length = written;
            }
            return written < capacity;
        }
        if (value == '\\')
        {
            char escape;

            if (scanner->position >= scanner->length)
            {
                return false;
            }
            escape = scanner->text[scanner->position++];
            switch (escape)
            {
            case '"':
                value = '"';
                break;
            case '\\':
                value = '\\';
                break;
            case '/':
                value = '/';
                break;
            case 'b':
                value = '\b';
                break;
            case 'f':
                value = '\f';
                break;
            case 'n':
                value = '\n';
                break;
            case 'r':
                value = '\r';
                break;
            case 't':
                value = '\t';
                break;
            default:
                return false;
            }
        }
        if (written + 1U >= capacity)
        {
            return false;
        }
        out[written++] = value;
    }
    return false;
}

/* Read a number, returning its raw text range. */
static bool scan_number(Scanner *scanner, const char **start, uint32_t *text_length)
{
    uint32_t begin = scanner->position;

    skip_whitespace(scanner);
    begin = scanner->position;
    if (scanner->position < scanner->length && scanner->text[scanner->position] == '-')
    {
        scanner->position++;
    }
    while (scanner->position < scanner->length)
    {
        char value = scanner->text[scanner->position];

        if ((value >= '0' && value <= '9') || value == '.' || value == 'e' || value == 'E' ||
            value == '+' || value == '-')
        {
            scanner->position++;
            continue;
        }
        break;
    }
    if (scanner->position == begin)
    {
        return false;
    }
    *start = &scanner->text[begin];
    *text_length = scanner->position - begin;
    return true;
}

/* Skip one value of any kind, including nested objects and arrays. */
static bool skip_value(Scanner *scanner)
{
    char value = peek_char(scanner);

    if (value == '"')
    {
        /* The skip walks the string directly rather than reusing scan_string,
         * which would need somewhere to put the decoded value; a string being
         * skipped has no use for its contents. */
        scanner->position++;
        while (scanner->position < scanner->length)
        {
            char current = scanner->text[scanner->position++];

            if (current == '\\')
            {
                scanner->position++;
                continue;
            }
            if (current == '"')
            {
                return true;
            }
        }
        return false;
    }
    if (value == '{' || value == '[')
    {
        char closing = (value == '{') ? '}' : ']';
        /* Depth is signed so an unbalanced closer is caught rather than wrapping
         * to a huge value and letting the scan run off the end of the buffer. */
        int depth = 0;

        while (scanner->position < scanner->length)
        {
            char current = scanner->text[scanner->position++];

            if (current == '"')
            {
                /* Skip the string whole so a brace inside it does not change
                 * the nesting depth. */
                while (scanner->position < scanner->length)
                {
                    char inner = scanner->text[scanner->position++];

                    if (inner == '\\')
                    {
                        scanner->position++;
                        continue;
                    }
                    if (inner == '"')
                    {
                        break;
                    }
                }
                continue;
            }
            if (current == '{' || current == '[')
            {
                depth++;
                continue;
            }
            if (current == '}' || current == ']')
            {
                depth--;
                if (depth < 0)
                {
                    /* A closer with no opener is malformed. */
                    return false;
                }
                if (depth == 0)
                {
                    /* This is the closer of the value being skipped, and the
                     * scan stops here: continuing would swallow whatever follows
                     * it in the enclosing document. */
                    return current == closing;
                }
            }
        }
        return false;
    }
    if (value == 't' && scanner->position + 4U <= scanner->length &&
        scanner->text[scanner->position] == 't' && scanner->text[scanner->position + 1U] == 'r' &&
        scanner->text[scanner->position + 2U] == 'u' && scanner->text[scanner->position + 3U] == 'e')
    {
        scanner->position += 4U;
        return true;
    }
    if (value == 'f' && scanner->position + 5U <= scanner->length &&
        scanner->text[scanner->position + 1U] == 'a' && scanner->text[scanner->position + 2U] == 'l' &&
        scanner->text[scanner->position + 3U] == 's' && scanner->text[scanner->position + 4U] == 'e')
    {
        scanner->position += 5U;
        return true;
    }
    if (value == 'n' && scanner->position + 4U <= scanner->length &&
        scanner->text[scanner->position + 1U] == 'u' && scanner->text[scanner->position + 2U] == 'l' &&
        scanner->text[scanner->position + 3U] == 'l')
    {
        scanner->position += 4U;
        return true;
    }
    {
        const char *start = NULL;
        uint32_t text_length = 0U;

        return scan_number(scanner, &start, &text_length);
    }
}

/* Parse a non-negative integer from raw text. Returns false on a sign, a
 * fraction or an overflow past `maximum`. */
static bool parse_unsigned(const char *text, uint32_t length, uint64_t maximum, uint64_t *value)
{
    uint64_t result = 0U;
    uint32_t index;

    if (length == 0U)
    {
        return false;
    }
    for (index = 0U; index < length; index++)
    {
        char digit = text[index];

        if (digit < '0' || digit > '9')
        {
            return false;
        }
        result = result * 10U + (uint64_t)(digit - '0');
        if (result > maximum)
        {
            return false;
        }
    }
    *value = result;
    return true;
}

/* Largest whole part accepted while parsing. Anything above it is certainly out
 * of range, and saturating there keeps the value from wrapping while still
 * letting the range check below produce the right reason. */
#define PARSE_TENTHS_CEILING 100000U

/* Parse a decimal into tenths, rounding half away from zero.
 *
 * A threshold may arrive as 30.0 or 30.5. The device's DHT11 resolves one
 * degree, so a fractional limit cannot be enforced as written; rounding here and
 * documenting it is better than truncating, which would move every limit down by
 * up to a degree without saying so.
 *
 * The value is not range-checked here. A syntactically valid number that is out
 * of range belongs in the range check, which can report out_of_range and have
 * the device acknowledge the command; failing the parse instead would leave the
 * backend with no answer at all. */
static bool parse_tenths(const char *text, uint32_t length, uint32_t *tenths)
{
    uint64_t whole = 0U;
    uint32_t fraction = 0U;
    uint32_t index = 0U;
    bool seen_point = false;
    uint32_t fraction_digits = 0U;

    if (length == 0U)
    {
        return false;
    }
    while (index < length)
    {
        char value = text[index];

        if (value == '.')
        {
            if (seen_point)
            {
                return false;
            }
            seen_point = true;
            index++;
            continue;
        }
        if (value < '0' || value > '9')
        {
            /* Exponent notation is not produced by the backend and would need
             * floating point to interpret, so it is refused. */
            return false;
        }
        if (!seen_point)
        {
            whole = whole * 10U + (uint64_t)(value - '0');
            if (whole > (uint64_t)PARSE_TENTHS_CEILING)
            {
                whole = PARSE_TENTHS_CEILING;
            }
        }
        else if (fraction_digits < 2U)
        {
            fraction = fraction * 10U + (uint32_t)(value - '0');
            fraction_digits++;
        }
        else
        {
            return false;
        }
        index++;
    }

    /* Round half up, with the rounding point taken from how many fraction
     * digits actually arrived: 0.5 and 0.50 must round the same way, and 0.49
     * must not round up merely because its two digits exceed five. */
    if (fraction_digits == 1U)
    {
        if (fraction >= 5U)
        {
            whole++;
        }
    }
    else if (fraction_digits == 2U)
    {
        if (fraction >= 50U)
        {
            whole++;
        }
    }
    *tenths = (uint32_t)(whole * 10U);
    return true;
}

/* Record one top-level envelope field. */
static bool apply_envelope_field(CommandFields *fields, const char *key, Scanner *value)
{
    const char *number_start = NULL;
    uint32_t number_length = 0U;

    if (key_equals(key, "schemaVersion"))
    {
        uint64_t version = 0U;

        if (!scan_number(value, &number_start, &number_length) ||
            !parse_unsigned(number_start, number_length, 0xFFFFFFFFU, &version))
        {
            return false;
        }
        fields->has_schema_version = true;
        fields->schema_version_supported = version == COMMAND_SCHEMA_VERSION;
        return true;
    }
    if (key_equals(key, "messageType"))
    {
        char text[24];
        uint32_t text_length = 0U;

        if (!scan_string(value, text, sizeof(text), &text_length))
        {
            return false;
        }
        fields->has_message_type = true;
        fields->message_type_control = text_length == 7U && text[0] == 'c' && text[1] == 'o' &&
                                       text[2] == 'n' && text[3] == 't' && text[4] == 'r' &&
                                       text[5] == 'o' && text[6] == 'l';
        return true;
    }
    if (key_equals(key, "deviceId"))
    {
        uint32_t text_length = 0U;

        if (!scan_string(value, fields->device_id, sizeof(fields->device_id), &text_length))
        {
            return false;
        }
        fields->has_device_id = true;
        return true;
    }
    if (key_equals(key, "requestId"))
    {
        uint32_t text_length = 0U;

        if (!scan_string(value, fields->request_id, sizeof(fields->request_id), &text_length))
        {
            return false;
        }
        fields->has_request_id = text_length > 0U;
        return text_length > 0U;
    }
    if (key_equals(key, "issuedAt"))
    {
        if (!scan_number(value, &number_start, &number_length) ||
            !parse_unsigned(number_start, number_length, 0xFFFFFFFFFFFFULL, &fields->issued_at))
        {
            return false;
        }
        fields->has_issued_at = true;
        return true;
    }
    if (key_equals(key, "expiresAt"))
    {
        if (!scan_number(value, &number_start, &number_length) ||
            !parse_unsigned(number_start, number_length, 0xFFFFFFFFFFFFULL, &fields->expires_at))
        {
            return false;
        }
        fields->has_expires_at = true;
        return true;
    }
    if (key_equals(key, "type"))
    {
        uint32_t text_length = 0U;

        if (!scan_string(value, fields->type, sizeof(fields->type), &text_length))
        {
            return false;
        }
        fields->has_type = text_length > 0U;
        return text_length > 0U;
    }
    if (key_equals(key, "payload"))
    {
        /* The payload object is scanned in its own pass. Recording its byte
         * range here is what lets that second pass be an ordinary object scan
         * instead of a search through the envelope: a search could match the
         * word "payload" inside a string value and then parse the wrong bytes. */
        uint32_t start = value->position;
        uint32_t end = value->position;

        if (!skip_value(value))
        {
            return false;
        }
        end = value->position;
        fields->payload_start = start;
        fields->payload_length = end - start;
        fields->has_payload = true;
        return true;
    }
    /* An unrecognised field means the sender is not speaking the frozen
     * contract. Refusing is what keeps a field added on one side from being
     * silently ignored on the other. */
    return false;
}

/* Record one payload field. */
static bool apply_payload_field(CommandFields *fields, const char *key, Scanner *value)
{
    const char *number_start = NULL;
    uint32_t number_length = 0U;

    if (key_equals(key, "thresholdVersion"))
    {
        uint64_t version = 0U;

        if (!scan_number(value, &number_start, &number_length) ||
            !parse_unsigned(number_start, number_length, 0xFFFFFFFFU, &version))
        {
            return false;
        }
        fields->threshold_version = (uint32_t)version;
        fields->has_threshold_version = true;
        return true;
    }
    if (key_equals(key, "temperatureHighC"))
    {
        if (!scan_number(value, &number_start, &number_length) ||
            !parse_tenths(number_start, number_length, &fields->temperature_high_tenths))
        {
            return false;
        }
        fields->has_temperature_high = true;
        return true;
    }
    if (key_equals(key, "humidityHighRh"))
    {
        if (!scan_number(value, &number_start, &number_length) ||
            !parse_tenths(number_start, number_length, &fields->humidity_high_tenths))
        {
            return false;
        }
        fields->has_humidity_high = true;
        return true;
    }
    if (key_equals(key, "gasHighPpm"))
    {
        if (!scan_number(value, &number_start, &number_length) ||
            !parse_tenths(number_start, number_length, &fields->gas_high_tenths))
        {
            return false;
        }
        fields->has_gas_high = true;
        return true;
    }
    return false;
}

/* Scan one flat JSON object, dispatching each field. Returns false on any
 * structural problem, an unknown field, or a field the handler refused. */
static bool scan_object(Scanner *scanner, CommandFields *fields, bool payload_level)
{
    bool first = true;

    if (!expect_char(scanner, '{'))
    {
        return false;
    }
    if (peek_char(scanner) == '}')
    {
        /* An empty object is well formed. Whether it is *acceptable* is decided
         * by the field checks that follow, which can name the missing field
         * instead of reporting only that the payload was empty. */
        scanner->position++;
        return true;
    }

    while (true)
    {
        char key[32];
        uint32_t key_length = 0U;
        Scanner value;

        if (!first && !expect_char(scanner, ','))
        {
            return false;
        }
        first = false;

        if (!scan_string(scanner, key, sizeof(key), &key_length))
        {
            return false;
        }
        if (!expect_char(scanner, ':'))
        {
            return false;
        }

        value.text = scanner->text;
        value.length = scanner->length;
        value.position = scanner->position;

        if (payload_level)
        {
            if (!apply_payload_field(fields, key, &value))
            {
                return false;
            }
        }
        else if (!apply_envelope_field(fields, key, &value))
        {
            return false;
        }
        scanner->position = value.position;

        if (peek_char(scanner) == ',')
        {
            continue;
        }
        if (peek_char(scanner) == '}')
        {
            scanner->position++;
            return true;
        }
        return false;
    }
}

/* Scan the payload object using the byte range the envelope pass recorded. */
static bool scan_payload_object(const char *json, uint32_t length, CommandFields *fields)
{
    Scanner scanner;

    if (!fields->has_payload || fields->payload_length == 0U ||
        fields->payload_start + fields->payload_length > length)
    {
        return false;
    }
    scanner.text = json;
    scanner.length = fields->payload_start + fields->payload_length;
    scanner.position = fields->payload_start;
    return scan_object(&scanner, fields, true);
}

void CommandDedupInit(CommandDedup *dedup)
{
    uint32_t slot;

    if (dedup == NULL)
    {
        return;
    }
    for (slot = 0U; slot < COMMAND_DEDUP_SLOTS; slot++)
    {
        dedup->request_ids[slot][0] = '\0';
        dedup->results[slot] = (uint8_t)COMMAND_RESULT_APPLIED;
    }
    dedup->count = 0U;
    dedup->next = 0U;
}

bool CommandDedupLookup(const CommandDedup *dedup, const char *request_id, CommandResult *result)
{
    uint32_t slot;

    if (dedup == NULL || request_id == NULL)
    {
        return false;
    }
    for (slot = 0U; slot < dedup->count; slot++)
    {
        uint32_t index = 0U;
        bool equal = true;

        while (true)
        {
            char left = dedup->request_ids[slot][index];
            char right = request_id[index];

            if (left != right)
            {
                equal = false;
                break;
            }
            if (left == '\0')
            {
                break;
            }
            index++;
        }
        if (equal)
        {
            if (result != NULL)
            {
                *result = (CommandResult)dedup->results[slot];
            }
            return true;
        }
    }
    return false;
}

void CommandDedupRecord(CommandDedup *dedup, const char *request_id, CommandResult result)
{
    uint32_t index = 0U;
    uint32_t slot;

    if (dedup == NULL || request_id == NULL)
    {
        return;
    }
    slot = dedup->next;
    while (index < COMMAND_REQUEST_ID_MAX && request_id[index] != '\0')
    {
        dedup->request_ids[slot][index] = request_id[index];
        index++;
    }
    dedup->request_ids[slot][index] = '\0';
    dedup->results[slot] = (uint8_t)result;

    dedup->next = (uint8_t)((dedup->next + 1U) % COMMAND_DEDUP_SLOTS);
    if (dedup->count < COMMAND_DEDUP_SLOTS)
    {
        dedup->count++;
    }
}

CommandResult CommandJsonParse(const char *json, uint32_t length, const char *device_id,
                              uint32_t current_threshold_version, ControlCommand *command)
{
    CommandFields fields;
    Scanner scanner;

    if (json == NULL || command == NULL)
    {
        return COMMAND_RESULT_MALFORMED;
    }

    fields.has_schema_version = false;
    fields.has_message_type = false;
    fields.has_device_id = false;
    fields.has_request_id = false;
    fields.has_issued_at = false;
    fields.has_expires_at = false;
    fields.has_type = false;
    fields.has_payload = false;
    fields.payload_start = 0U;
    fields.payload_length = 0U;
    fields.has_threshold_version = false;
    fields.has_temperature_high = false;
    fields.has_humidity_high = false;
    fields.has_gas_high = false;
    fields.schema_version_supported = false;
    fields.message_type_control = false;
    fields.device_id[0] = '\0';
    fields.request_id[0] = '\0';
    fields.issued_at = 0U;
    fields.expires_at = 0U;
    fields.type[0] = '\0';
    fields.threshold_version = 0U;
    fields.temperature_high_tenths = 0U;
    fields.humidity_high_tenths = 0U;
    fields.gas_high_tenths = 0U;

    scanner.text = json;
    scanner.length = length;
    scanner.position = 0U;

    if (!scan_object(&scanner, &fields, false))
    {
        return COMMAND_RESULT_MALFORMED;
    }
    /* The payload object is scanned only for a recognized type. An unknown type
     * (the retired set_mute, for example) must be refused as bad_request_type
     * before its payload fields are interpreted, which is the frozen order in
     * docs/device-protocol.md §4.1: type is validated before range. Scanning the
     * payload first would report a mute body as malformed and never answer with
     * the rejection the contract promises. */
    if (!fields.has_schema_version || !fields.has_message_type || !fields.has_device_id ||
        !fields.has_request_id || !fields.has_issued_at || !fields.has_expires_at ||
        !fields.has_type || !fields.has_payload)
    {
        /* The envelope is incomplete, so there is no requestId to answer with a
         * precise reason. Counted as malformed rather than answered. */
        return COMMAND_RESULT_MALFORMED;
    }

    /* Copy the identity out before any decision so that a rejection can still be
     * acknowledged with the requestId the backend is waiting on. */
    copy_bounded(command->request_id, sizeof(command->request_id), fields.request_id, COMMAND_REQUEST_ID_MAX);

    if (!fields.schema_version_supported)
    {
        return COMMAND_RESULT_REJECTED_SCHEMA;
    }
    if (!fields.message_type_control)
    {
        return COMMAND_RESULT_REJECTED_TYPE;
    }
    if (device_id == NULL || fields.device_id[0] == '\0')
    {
        return COMMAND_RESULT_MALFORMED;
    }
    {
        uint32_t index = 0U;

        while (index < COMMAND_DEVICE_ID_MAX)
        {
            if (fields.device_id[index] != device_id[index])
            {
                return COMMAND_RESULT_REJECTED_DEVICE;
            }
            if (fields.device_id[index] == '\0')
            {
                break;
            }
            index++;
        }
        if (index >= COMMAND_DEVICE_ID_MAX)
        {
            return COMMAND_RESULT_REJECTED_DEVICE;
        }
    }
    if (fields.expires_at <= fields.issued_at)
    {
        return COMMAND_RESULT_REJECTED_RANGE;
    }
    {
        uint64_t window = fields.expires_at - fields.issued_at;

        if (window > (uint64_t)COMMAND_MAX_WINDOW_MS)
        {
            return COMMAND_RESULT_REJECTED_RANGE;
        }
        command->window_ms = (uint32_t)window;
    }

    if (fields.type[0] == 's' && fields.type[1] == 'e' && fields.type[2] == 't' && fields.type[3] == '_' &&
        fields.type[4] == 't' && fields.type[5] == 'h' && fields.type[6] == 'r' && fields.type[7] == 'e' &&
        fields.type[8] == 's' && fields.type[9] == 'h' && fields.type[10] == 'o' && fields.type[11] == 'l' &&
        fields.type[12] == 'd' && fields.type[13] == 's' && fields.type[14] == '\0')
    {
        if (!scan_payload_object(json, length, &fields))
        {
            return COMMAND_RESULT_MALFORMED;
        }
        if (!fields.has_threshold_version || !fields.has_temperature_high ||
            !fields.has_humidity_high || !fields.has_gas_high)
        {
            /* Every threshold field is required. Applying a partial set would
             * leave the device enforcing a mixture of old and new limits that
             * the reported version could not describe. */
            return COMMAND_RESULT_REJECTED_RANGE;
        }
        if (fields.threshold_version < ENV_INITIAL_THRESHOLD_VERSION)
        {
            return COMMAND_RESULT_REJECTED_RANGE;
        }

        command->type = COMMAND_SET_THRESHOLDS;
        command->threshold_version = fields.threshold_version;
        /* A version that does not move forward would let a replay undo a newer
         * configuration, so it is refused rather than applied silently. The
         * comparison lives in its own function because the control path has to
         * apply it later than this, after the deduplication check. */
        if (current_threshold_version != 0U &&
            CommandCheckThresholdVersion(command, current_threshold_version) !=
                COMMAND_RESULT_APPLIED)
        {
            return COMMAND_RESULT_REJECTED_STALE_VERSION;
        }

        command->thresholds.temperature_high_c = (uint8_t)(fields.temperature_high_tenths / 10U);
        command->thresholds.humidity_high_rh = (uint8_t)(fields.humidity_high_tenths / 10U);
        command->thresholds.gas_high_ppm = (uint16_t)(fields.gas_high_tenths / 10U);
        /* The bounds are the device's, and a value outside them is refused with
         * a reason the backend can act on. The gas minimum is checked on the
         * rounded value as well: a limit that rounds to zero would alarm on
         * every sample. */
        if (command->thresholds.temperature_high_c > ENV_MAX_TEMPERATURE_HIGH_C)
        {
            return COMMAND_RESULT_REJECTED_RANGE;
        }
        if (command->thresholds.humidity_high_rh > ENV_MAX_HUMIDITY_HIGH_RH)
        {
            return COMMAND_RESULT_REJECTED_RANGE;
        }
        if (command->thresholds.gas_high_ppm < ENV_MIN_GAS_HIGH_PPM ||
            command->thresholds.gas_high_ppm > ENV_MAX_GAS_HIGH_PPM)
        {
            return COMMAND_RESULT_REJECTED_RANGE;
        }
        /* The rise thresholds are not part of the frozen control payload yet, so
         * the values in force are preserved rather than zeroed: zeroing them
         * would make every later evaluation alarm on the rise test. */
        command->thresholds.temperature_rise_c = ENV_DEFAULT_TEMPERATURE_RISE_C;
        command->thresholds.gas_rise_adc = ENV_DEFAULT_GAS_RISE_ADC;
        return COMMAND_RESULT_APPLIED;
    }

    return COMMAND_RESULT_REJECTED_TYPE;
}

bool CommandWithinWindow(const ControlCommand *command, uint32_t received_uptime_ms, uint32_t now_ms)
{
    if (command == NULL)
    {
        return false;
    }
    /* Unsigned subtraction is the wrap-safe form; the counter wraps after about
     * 49.7 days, which the device cannot distinguish from a long uptime. */
    return (uint32_t)(now_ms - received_uptime_ms) <= command->window_ms;
}

CommandResult CommandCheckThresholdVersion(const ControlCommand *command,
                                          uint32_t current_threshold_version)
{
    if (command == NULL || command->type != COMMAND_SET_THRESHOLDS)
    {
        return COMMAND_RESULT_APPLIED;
    }
    if (current_threshold_version != 0U &&
        command->threshold_version <= current_threshold_version)
    {
        return COMMAND_RESULT_REJECTED_STALE_VERSION;
    }
    return COMMAND_RESULT_APPLIED;
}

const char *CommandAckStatus(CommandResult result)
{
    switch (result)
    {
    case COMMAND_RESULT_APPLIED:
        return "applied";
    case COMMAND_RESULT_DUPLICATE:
        return "duplicate";
    case COMMAND_RESULT_EXPIRED:
        return "expired";
    case COMMAND_RESULT_FAILED_FLASH_WRITE:
    case COMMAND_RESULT_FAILED_FLASH_VERIFY:
        return "failed";
    case COMMAND_RESULT_MALFORMED:
        /* Malformed commands are never acknowledged, so this value is a marker
         * for the caller's counters rather than a status sent on the wire. */
        return "rejected";
    default:
        return "rejected";
    }
}

const char *CommandAckErrorCode(CommandResult result)
{
    switch (result)
    {
    case COMMAND_RESULT_APPLIED:
    case COMMAND_RESULT_DUPLICATE:
    case COMMAND_RESULT_MALFORMED:
        /* The contract forbids an errorCode on a success, and a malformed
         * command is never acknowledged at all. */
        return NULL;
    case COMMAND_RESULT_EXPIRED:
        return NULL;
    case COMMAND_RESULT_REJECTED_SCHEMA:
        return "schema_unsupported";
    case COMMAND_RESULT_REJECTED_DEVICE:
        return "device_mismatch";
    case COMMAND_RESULT_REJECTED_TYPE:
        return "bad_request_type";
    case COMMAND_RESULT_REJECTED_RANGE:
        return "out_of_range";
    case COMMAND_RESULT_REJECTED_STALE_VERSION:
        return "stale_version";
    case COMMAND_RESULT_FAILED_FLASH_WRITE:
        return "flash_write_failed";
    case COMMAND_RESULT_FAILED_FLASH_VERIFY:
        return "flash_verify_failed";
    default:
        return "out_of_range";
    }
}

uint32_t CommandAckJsonEncode(const CommandAckPayload *payload, char *buffer, uint32_t capacity)
{
    JsonWriter writer;
    const char *error_code;
    bool report_version;

    if (payload == NULL || buffer == NULL || capacity == 0U)
    {
        return 0U;
    }
    /* docs/device-protocol.md §6: the version is reported for set_thresholds when
     * the result is applied *or* duplicate, and is null otherwise. A duplicate of
     * a threshold command must therefore still carry the version in force, because
     * the backend reconciles its record from the acknowledgement and a null there
     * would read as "this device has no threshold configuration". Every other
     * result — carries neither a version nor a
     * claim to have changed one. */
    report_version = (payload->result == COMMAND_RESULT_APPLIED ||
                      payload->result == COMMAND_RESULT_DUPLICATE) &&
                     payload->threshold_version != 0U;

    JsonWriterInit(&writer, buffer, capacity);
    JsonWriterRaw(&writer, "{");
    JsonWriterKey(&writer, "schemaVersion");
    JsonWriterUnsigned(&writer, COMMAND_SCHEMA_VERSION);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "messageType");
    JsonWriterString(&writer, "command_ack");
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "deviceId");
    JsonWriterString(&writer, payload->device_id);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "bootId");
    JsonWriterString(&writer, payload->boot_id);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "sequence");
    JsonWriterUnsigned(&writer, payload->sequence);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "timestamp");
    JsonWriterNull(&writer);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "uptimeMs");
    JsonWriterUnsigned(&writer, payload->uptime_ms);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "requestId");
    JsonWriterString(&writer, payload->request_id);
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "status");
    JsonWriterString(&writer, CommandAckStatus(payload->result));
    JsonWriterRaw(&writer, ",");
    JsonWriterKey(&writer, "thresholdVersion");
    if (report_version)
    {
        JsonWriterUnsigned(&writer, payload->threshold_version);
    }
    else
    {
        JsonWriterNull(&writer);
    }
    JsonWriterRaw(&writer, ",");

    error_code = CommandAckErrorCode(payload->result);
    JsonWriterKey(&writer, "errorCode");
    if (error_code != NULL)
    {
        JsonWriterString(&writer, error_code);
    }
    else
    {
        JsonWriterNull(&writer);
    }
    JsonWriterRaw(&writer, "}");

    if (!JsonWriterOk(&writer))
    {
        return 0U;
    }
    return JsonWriterLength(&writer);
}
