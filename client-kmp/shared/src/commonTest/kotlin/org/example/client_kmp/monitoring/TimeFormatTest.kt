package org.example.client_kmp.monitoring

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull

/**
 * Timestamp formatting and window arithmetic.
 *
 * The expected strings are calendar values, not re-derivations of the same
 * algorithm: each one was computed independently, so a test cannot pass by
 * agreeing with a bug in the formatter. The leap-day and pre-epoch cases are
 * included because integer month arithmetic usually breaks on exactly those.
 */
class TimeFormatTest {

    @Test
    fun theEpochAndWholeSecondsRenderInTheContractForm() {
        assertEquals("1970-01-01T00:00:00Z", Rfc3339.utcFromEpochMillis(0L))
        assertEquals("1970-01-01T00:00:01Z", Rfc3339.utcFromEpochMillis(1_000L))
        assertEquals("1970-01-02T00:00:00Z", Rfc3339.utcFromEpochMillis(86_400_000L))
    }

    @Test
    fun aTimeInsideTheDayKeepsItsHourMinuteAndSecond() {
        // 2009-02-13T23:31:30Z, a value with a non-trivial time component.
        assertEquals("2009-02-13T23:31:30Z", Rfc3339.utcFromEpochMillis(1_234_567_890_123L))
        assertEquals("2026-09-22T01:30:45Z", Rfc3339.utcFromEpochMillis(1_790_040_645_000L))
    }

    @Test
    fun aLeapDayIsRenderedRatherThanRolledOver() {
        // 2000 is a leap year; a formatter that assumes 365-day years lands on
        // 01 March here, and one that forgets the 400-year rule lands on
        // 28 February.
        assertEquals("2000-02-29T00:00:00Z", Rfc3339.utcFromEpochMillis(951_782_400_000L))
        // 2100 is not a leap year under the 400-year rule, so this is New Year.
        assertEquals("2100-01-01T00:00:00Z", Rfc3339.utcFromEpochMillis(4_102_444_800_000L))
    }

    @Test
    fun aTimeBeforeTheEpochFloorsRatherThanTruncatingTowardZero() {
        // Truncating division would answer 1970-01-01T00:00:00Z for -1000 ms,
        // which is a whole second later than the instant it denotes.
        assertEquals("1969-12-31T23:59:59Z", Rfc3339.utcFromEpochMillis(-1_000L))
        assertEquals("1969-12-31T23:59:59Z", Rfc3339.utcFromEpochMillis(-1L))
    }

    @Test
    fun everyMonthBoundaryHasTheExpectedLength() {
        // The first instant of four months of 2026, chosen to bracket the
        // 31/30/31-day runs and the short February, so a wrong month length shows
        // up as a wrong date rather than as a plausible neighbouring one.
        assertEquals("2026-01-01T00:00:00Z", Rfc3339.utcFromEpochMillis(1_767_225_600_000L))
        assertEquals("2026-03-01T00:00:00Z", Rfc3339.utcFromEpochMillis(1_772_323_200_000L))
        assertEquals("2026-07-01T00:00:00Z", Rfc3339.utcFromEpochMillis(1_782_864_000_000L))
        assertEquals("2026-12-01T00:00:00Z", Rfc3339.utcFromEpochMillis(1_796_083_200_000L))
        assertEquals("2027-01-01T00:00:00Z", Rfc3339.utcFromEpochMillis(1_798_761_600_000L))
    }

    @Test
    fun hoursBecomeTheMillisecondSpanTheQueryUses() {
        assertEquals(3_600_000L, Rfc3339.hoursToMillis(1))
        assertEquals(21_600_000L, Rfc3339.hoursToMillis(6))
        assertEquals(86_400_000L, Rfc3339.hoursToMillis(24))
        assertEquals(0L, Rfc3339.hoursToMillis(0))
    }

    @Test
    fun parsesStandardRfc3339Timestamps() {
        assertEquals(0L, Rfc3339.parseEpochMillis("1970-01-01T00:00:00Z"))
        assertEquals(1_000L, Rfc3339.parseEpochMillis("1970-01-01T00:00:01Z"))
        assertEquals(1_790_040_645_000L, Rfc3339.parseEpochMillis("2026-09-22T01:30:45Z"))
        assertEquals(1_790_040_645_250L, Rfc3339.parseEpochMillis("2026-09-22T01:30:45.250Z"))
        // Offset +08:00
        assertEquals(1_790_040_645_000L, Rfc3339.parseEpochMillis("2026-09-22T09:30:45+08:00"))
        // Offset -05:00
        assertEquals(1_790_040_645_000L, Rfc3339.parseEpochMillis("2026-09-21T20:30:45-05:00"))
    }

    @Test
    fun invalidTimestampsReturnNull() {
        assertEquals(null, Rfc3339.parseEpochMillis("not-a-time"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-13-01T00:00:00Z"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-01-32T00:00:00Z"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-01-01T25:00:00Z"))
    }

    // --- strict RFC 3339 parsing ----------------------------------------------------

    @Test
    fun aTimezoneDesignatorIsRequired() {
        // A bare local time is not an instant: without Z or ±HH:MM there is no
        // unambiguous point on the timeline.
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45.250"))
    }

    @Test
    fun trailingCharactersAfterTheTimezoneAreRejected() {
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45Zextra"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45Z "))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45+08:00x"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45Z2026-09-22T01:30:45Z"))
    }

    @Test
    fun timezoneHourAndMinuteRangesAreEnforced() {
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45+24:00"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45-24:00"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45+08:60"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45+0800"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45+8:00"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45+08:00:00"))
    }

    @Test
    fun theCalendarDateMustActuallyExist() {
        // 2024 is a leap year: 29 February exists.
        assertNotNull(Rfc3339.parseEpochMillis("2024-02-29T00:00:00Z"))
        // 2026 is not: 29 February does not exist.
        assertEquals(null, Rfc3339.parseEpochMillis("2026-02-29T00:00:00Z"))
        // February never has 31 days.
        assertEquals(null, Rfc3339.parseEpochMillis("2026-02-31T00:00:00Z"))
        // April has 30 days, not 31.
        assertEquals(null, Rfc3339.parseEpochMillis("2026-04-31T00:00:00Z"))
    }

    @Test
    fun anEmptyFractionalSecondIsRejected() {
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45.Z"))
        assertEquals(null, Rfc3339.parseEpochMillis("2026-09-22T01:30:45.+08:00"))
    }

    @Test
    fun parsingNeverThrowsOnHostileInput() {
        val hostile = listOf(
            "",
            " ",
            "Z",
            "2026",
            "2026-09-22",
            "XXXX-XX-XXTXX:XX:XXZ",
            "2026-09-22T01:30:45.",
            "2026-09-22T01:30:45.Z",
        )
        for (value in hostile) {
            // Must return null or a Long — never throw.
            Rfc3339.parseEpochMillis(value)
        }
    }

    @Test
    fun validUtcAndOffsetFormsStillParse() {
        assertEquals(0L, Rfc3339.parseEpochMillis("1970-01-01T00:00:00Z"))
        assertEquals(0L, Rfc3339.parseEpochMillis("1970-01-01T00:00:00z"))
        assertEquals(1_790_040_645_000L, Rfc3339.parseEpochMillis("2026-09-22T09:30:45+08:00"))
        assertEquals(1_790_040_645_000L, Rfc3339.parseEpochMillis("2026-09-21T20:30:45-05:00"))
        assertEquals(1_790_040_645_250L, Rfc3339.parseEpochMillis("2026-09-22T01:30:45.250Z"))
    }
}
