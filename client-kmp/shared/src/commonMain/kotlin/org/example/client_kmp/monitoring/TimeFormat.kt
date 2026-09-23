package org.example.client_kmp.monitoring

/**
 * UTC timestamp formatting for the query parameters the contract expects.
 *
 * The contract's history endpoint takes RFC 3339 `from`/`to` values, and the
 * project has no date-time dependency: `kotlinx-datetime` is not on the shared
 * classpath, and adding it would pull a platform date layer into a module whose
 * whole point is to hold nothing but business rules. The conversion from an
 * epoch millisecond count to a calendar date is therefore written out here,
 * where it is total, dependency-free and unit-testable on every target.
 *
 * Only the UTC form is produced. A local-offset timestamp would need the
 * device's timezone, which neither host passes in, and the argument is used for
 * a range query whose bounds are relative to "now" — an offset would only add a
 * way for the two hosts to disagree.
 */
internal object Rfc3339 {

    private const val MILLIS_PER_SECOND = 1_000L
    private const val SECONDS_PER_MINUTE = 60L
    private const val MINUTES_PER_HOUR = 60L
    private const val HOURS_PER_DAY = 24L
    private const val MILLIS_PER_DAY = HOURS_PER_DAY * MINUTES_PER_HOUR * SECONDS_PER_MINUTE * MILLIS_PER_SECOND

    /** Milliseconds in [hours]; used to turn a trend window into a `from` bound. */
    fun hoursToMillis(hours: Int): Long = hours.toLong() * MINUTES_PER_HOUR * SECONDS_PER_MINUTE * MILLIS_PER_SECOND

    /**
     * Renders `millis` since the Unix epoch as `YYYY-MM-DDTHH:MM:SSZ`.
     *
     * Negative values (before 1970) are supported because a device with a wrong
     * clock can legitimately report one, and a formatter that silently mangled it
     * would turn a clock fault into a confusing server error.
     */
    fun utcFromEpochMillis(millis: Long): String {
        val days = floorDiv(millis, MILLIS_PER_DAY)
        val millisOfDay = millis - days * MILLIS_PER_DAY
        val date = civilFromDays(days)
        val hour = millisOfDay / (MINUTES_PER_HOUR * SECONDS_PER_MINUTE * MILLIS_PER_SECOND)
        val minute = (millisOfDay / (SECONDS_PER_MINUTE * MILLIS_PER_SECOND)) % MINUTES_PER_HOUR
        val second = (millisOfDay / MILLIS_PER_SECOND) % SECONDS_PER_MINUTE
        return buildString {
            appendPadded(date.year, 4)
            append('-')
            appendPadded(date.month, 2)
            append('-')
            appendPadded(date.day, 2)
            append('T')
            appendPadded(hour, 2)
            append(':')
            appendPadded(minute, 2)
            append(':')
            appendPadded(second, 2)
            append('Z')
        }
    }

    /**
     * Parses an RFC 3339 timestamp into epoch milliseconds. Returns null if invalid.
     *
     * Strictness rules — every violation returns null rather than throwing:
     * - a timezone designator is mandatory (`Z`/`z`, or a `±HH:MM` offset with
     *   hour 0..23 and minute 0..59); a bare local time is not an instant;
     * - nothing may follow the timezone designator;
     * - a fractional second, once introduced by `.`, must contain at least one digit;
     * - the calendar date must exist: 2024-02-29 is a leap day, 2026-02-29 is not,
     *   and month lengths are enforced, so 2026-02-31 and 2026-04-31 are rejected.
     */
    fun parseEpochMillis(iso: String): Long? {
        return try {
            parseStrict(iso)
        } catch (e: Exception) {
            null
        }
    }

    private fun parseStrict(iso: String): Long? {
        // Minimum shape: YYYY-MM-DDTHH:MM:SSZ
        if (iso.length < 20) return null
        val year = iso.substring(0, 4).toIntOrNull() ?: return null
        if (iso[4] != '-') return null
        val month = iso.substring(5, 7).toIntOrNull() ?: return null
        if (iso[7] != '-') return null
        val day = iso.substring(8, 10).toIntOrNull() ?: return null
        if (iso[10] != 'T' && iso[10] != 't') return null
        val hour = iso.substring(11, 13).toIntOrNull() ?: return null
        if (iso[13] != ':') return null
        val minute = iso.substring(14, 16).toIntOrNull() ?: return null
        if (iso[16] != ':') return null
        val second = iso.substring(17, 19).toIntOrNull() ?: return null

        if (month !in 1..12 || day !in 1..31 || hour !in 0..23 || minute !in 0..59 || second !in 0..59) {
            return null
        }

        // Round-trip through day counting so impossible dates (2026-02-29, 2026-04-31)
        // fold into a neighbouring month and are rejected instead of silently shifted.
        val days = daysFromCivil(year.toLong(), month.toLong(), day.toLong())
        val roundTrip = civilFromDays(days)
        if (roundTrip.year != year.toLong() ||
            roundTrip.month != month.toLong() ||
            roundTrip.day != day.toLong()
        ) {
            return null
        }

        var idx = 19
        var fractionMillis = 0L
        if (idx < iso.length && iso[idx] == '.') {
            idx++
            val start = idx
            while (idx < iso.length && iso[idx].isDigit()) {
                idx++
            }
            val fracStr = iso.substring(start, idx)
            // A trailing '.' with no digits is not a fractional second and not a
            // legal separator either — reject rather than read it as zero.
            if (fracStr.isEmpty()) return null
            fractionMillis = fracStr.padEnd(3, '0').take(3).toLongOrNull() ?: return null
        }

        // The timezone designator is part of the instant: without it there is no
        // unambiguous point in time, so its absence is an invalid timestamp.
        if (idx >= iso.length) return null
        var offsetMinutes = 0
        when (val tzChar = iso[idx]) {
            'Z', 'z' -> idx += 1
            '+', '-' -> {
                val sign = if (tzChar == '+') 1 else -1
                val tzPart = iso.substring(idx + 1)
                // Exactly ±HH:MM — no seconds, no bare ±HH, no extra text.
                if (tzPart.length != 5 || tzPart[2] != ':') return null
                val tzH = tzPart.substring(0, 2).toIntOrNull() ?: return null
                val tzM = tzPart.substring(3, 5).toIntOrNull() ?: return null
                if (tzH !in 0..23 || tzM !in 0..59) return null
                offsetMinutes = sign * (tzH * 60 + tzM)
                idx += 6
            }
            else -> return null
        }
        // Anything after the timezone designator means the value is not exactly
        // one RFC 3339 timestamp (e.g. two concatenated values).
        if (idx != iso.length) return null

        val timeMillis = hour * 3600_000L + minute * 60_000L + second * 1000L + fractionMillis
        return days * MILLIS_PER_DAY + timeMillis - offsetMinutes * 60_000L
    }

    private data class CivilDate(val year: Long, val month: Long, val day: Long)

    /**
     * Converts a day count since 1970-01-01 into a proleptic Gregorian date.
     *
     * This is Howard Hinnant's `civil_from_days`: it works entirely in integer
     * arithmetic by shifting the year to start in March, which makes the leap day
     * the last day of the year and removes it from the middle of the calculation.
     * Writing it out is cheaper than a dependency and, unlike a hand-rolled
     * month-length table, it is correct for every year rather than for the ones
     * that happen to have been tested.
     */
    private fun civilFromDays(days: Long): CivilDate {
        val shifted = days + 719_468L
        val era = floorDiv(shifted, 146_097L)
        val dayOfEra = shifted - era * 146_097L
        val yearOfEra = (dayOfEra - dayOfEra / 1_460L + dayOfEra / 36_524L - dayOfEra / 146_096L) / 365L
        val year = yearOfEra + era * 400L
        val dayOfYear = dayOfEra - (365L * yearOfEra + yearOfEra / 4L - yearOfEra / 100L)
        val monthPrime = (5L * dayOfYear + 2L) / 153L
        val day = dayOfYear - (153L * monthPrime + 2L) / 5L + 1L
        val month = if (monthPrime < 10L) monthPrime + 3L else monthPrime - 9L
        return CivilDate(
            year = if (month <= 2L) year + 1L else year,
            month = month,
            day = day,
        )
    }

    /**
     * Howard Hinnant's `days_from_civil`: converts (year, month, day) into days since 1970-01-01.
     */
    private fun daysFromCivil(year: Long, month: Long, day: Long): Long {
        val y = if (month <= 2L) year - 1L else year
        val era = floorDiv(y, 400L)
        val yoe = y - era * 400L
        val m = if (month > 2L) month - 3L else month + 9L
        val doy = (153L * m + 2L) / 5L + day - 1L
        val doe = yoe * 365L + yoe / 4L - yoe / 100L + doy
        return era * 146_097L + doe - 719_468L
    }

    /** Floors toward negative infinity, which is what splitting a timeline needs. */
    private fun floorDiv(value: Long, divisor: Long): Long {
        val quotient = value / divisor
        return if (value % divisor != 0L && (value xor divisor) < 0L) quotient - 1L else quotient
    }

    private fun StringBuilder.appendPadded(value: Long, width: Int) {
        val text = value.toString()
        repeat(width - text.length) { append('0') }
        append(text)
    }
}
