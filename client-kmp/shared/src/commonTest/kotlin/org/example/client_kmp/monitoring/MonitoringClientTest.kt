package org.example.client_kmp.monitoring

import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.double
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

/**
 * Covers the request shapes, response decoding and error mapping that both
 * hosts depend on.
 *
 * The assertions on paths, verbs, headers and JSON bodies are deliberately
 * literal: they are the contract wire format, and a host cannot fix a mistake
 * here.
 */
class MonitoringClientTest {

    private val statusBody =
        """{"deviceId":"MCU001","connectivity":"online","alarmState":"fire_warning",""" +
            """"localAlarm":true,"lastSeenAt":"2026-09-21T09:00:00Z"}"""

    private val telemetryBody =
        """{"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:00Z","temperatureC":40,"humidityRh":50,""" +
            """"gasAdcRaw":500,"gasAdcFiltered":480,"gasPpm":100,"localAlarm":true,""" +
            """"alarmCauses":["gas_high","rapid_temperature_rise"],"network":"reconnecting"}"""

    // --- dashboard ---------------------------------------------------------------------

    @Test
    fun dashboardAsksForDeviceStatusAndTheLatestSample() = runTest {
        val platform = FakePlatform { request ->
            when {
                request.url.endsWith("/status") -> HttpResponse(200, statusBody)
                else -> HttpResponse(200, telemetryBody)
            }
        }

        val view = MonitoringClient(platform, "http://test").loadDashboard()

        assertEquals(
            listOf("http://test/api/v1/devices/MCU001/status", "http://test/api/v1/devices/MCU001/telemetry/latest"),
            platform.requests.map { it.url },
        )
        assertEquals(listOf("GET", "GET"), platform.requests.map { it.method })
        assertTrue(view.hasData)
        assertEquals("火情预警", view.riskText)
        assertEquals(50, view.temperaturePercent)
        assertEquals("报警中", view.localAlarmText)
    }

    @Test
    fun aDeviceWithNoSampleYetIsAnEmptyDashboardNotAnError() = runTest {
        // The contract answers 404 for "latest" instead of a zero-filled sample.
        val platform = FakePlatform { request ->
            if (request.url.endsWith("/status")) HttpResponse(200, statusBody) else HttpResponse(404, "")
        }

        val view = MonitoringClient(platform, "http://test").loadDashboard()

        assertFalse(view.hasData)
        assertEquals("--", view.temperatureText)
        assertEquals("火情预警", view.riskText, "alarm state is still known without a sample")
    }

    @Test
    fun aMissingDeviceStillFailsTheDashboard() = runTest {
        val platform = FakePlatform { HttpResponse(404, """{"error":{"code":"device_not_found","message":"no device"}}""") }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadDashboard() }

        assertEquals("device_not_found", error.code)
        assertEquals(404, error.statusCode)
    }

    @Test
    fun anUnknownAlarmCauseIsIgnoredRatherThanFailingTheWholePage() = runTest {
        val platform = FakePlatform { request ->
            if (request.url.endsWith("/status")) {
                HttpResponse(200, statusBody)
            } else {
                HttpResponse(
                    200,
                    """{"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:00Z","temperatureC":20,"humidityRh":40,""" +
                        """"gasAdcRaw":1,"gasAdcFiltered":1,"localAlarm":false,"alarmCauses":["future_cause"]}""",
                )
            }
        }

        // Forward compatibility: an added cause must not take down the page.
        val view = MonitoringClient(platform, "http://test").loadDashboard()

        assertTrue(view.hasData)
    }

    // --- trends ------------------------------------------------------------------------

    @Test
    fun trendsAreBoundedByTheWindowAndTheLimitIsClamped() = runTest {
        val platform = FakePlatform { HttpResponse(200, """{"items":[]}""") }
        val client = MonitoringClient(platform, "http://test")

        client.loadTrends()
        client.loadTrends(limit = 5_000)
        client.loadTrends(limit = 0)
        client.loadTrends(window = TrendWindow.LAST_DAY)
        client.loadTrends(window = TrendWindow.LAST_SIX_HOURS)

        // The window becomes absolute bounds against the pinned clock, and the
        // page is requested newest-first so the rows that survive the limit are
        // the ones nearest now. A bare `limit` would return the oldest rows in
        // the range and present them as the trend of the whole window.
        assertEquals(
            listOf(
                "http://test/api/v1/devices/MCU001/telemetry" +
                    "?from=2026-09-22T00:30:45Z&to=2026-09-22T01:30:45Z&limit=200&order=desc",
                "http://test/api/v1/devices/MCU001/telemetry" +
                    "?from=2026-09-22T00:30:45Z&to=2026-09-22T01:30:45Z&limit=1000&order=desc",
                "http://test/api/v1/devices/MCU001/telemetry" +
                    "?from=2026-09-22T00:30:45Z&to=2026-09-22T01:30:45Z&limit=1&order=desc",
                "http://test/api/v1/devices/MCU001/telemetry" +
                    "?from=2026-09-21T01:30:45Z&to=2026-09-22T01:30:45Z&limit=200&order=desc",
                "http://test/api/v1/devices/MCU001/telemetry" +
                    "?from=2026-09-21T19:30:45Z&to=2026-09-22T01:30:45Z&limit=200&order=desc",
            ),
            platform.requests.map { it.url },
        )
    }

    @Test
    fun aNewestFirstPageIsReversedIntoAscendingOrder() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"items":[
                    {"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:02Z","temperatureC":22,"humidityRh":42,
                     "gasAdcRaw":102,"gasAdcFiltered":100,"gasPpm":14,"localAlarm":false},
                    {"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:01Z","temperatureC":21,"humidityRh":41,
                     "gasAdcRaw":101,"gasAdcFiltered":99,"gasPpm":13,"localAlarm":false}
                ]}""",
            )
        }

        val view = MonitoringClient(platform, "http://test").loadTrends()

        // The query asks for the newest rows, so the presentation layer must put
        // them back in event-time order before any statistic reads them.
        assertEquals(listOf("10:00:01", "10:00:02"), view.series.map { it.timeText })
        assertEquals("21", view.temperature.minimum)
        assertEquals("22", view.temperature.maximum)
    }

    @Test
    fun anEmptyHistoryDecodesWithoutInventingStatistics() = runTest {
        val platform = FakePlatform { HttpResponse(200, """{"items":[]}""") }

        val view = MonitoringClient(platform, "http://test").loadTrends()

        assertEquals(0, view.sampleCount)
        assertFalse(view.hasData)
        assertEquals("--", view.temperature.average)
    }

    @Test
    fun historySamplesWithoutGasDoNotBreakDecoding() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"items":[{"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:00Z","temperatureC":20,""" +
                    """"humidityRh":40,"gasAdcRaw":1,"gasAdcFiltered":1,"localAlarm":false}]}""",
            )
        }

        val view = MonitoringClient(platform, "http://test").loadTrends()

        assertEquals(1, view.sampleCount)
        assertEquals(0, view.gasSampleCount)
        assertEquals("--", view.series.single().gasText)
    }

    // --- alerts ------------------------------------------------------------------------

    @Test
    fun alertsRequestALimitAndMapToDisplayModels() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"items":[{"id":"a1","deviceId":"MCU001","state":"fire_warning",""" +
                    """"startedAt":"2026-09-21T10:00:00Z","endedAt":null,""" +
                    """"evidence":{"gasAdcRise":240,"gasAdcRiseThreshold":200,""" +
                    """"temperatureRateCPerMinute":2.5,"temperatureRateThresholdCPerMinute":1.5,""" +
                    """"sampleCount":12,"windowSeconds":60}}]}""",
            )
        }

        val view = MonitoringClient(platform, "http://test").loadAlerts()

        assertEquals("http://test/api/v1/devices/MCU001/alerts?limit=50", platform.requests.single().url)
        assertEquals(1, view.count)
        assertEquals("火情预警", view.items.single().stateText)
        assertTrue(view.items.single().active)
        assertEquals("2.5 °C/min", view.items.single().temperatureRateText)
    }

    @Test
    fun anEmptyAlertListIsAnEmptyViewNotAnError() = runTest {
        val platform = FakePlatform { HttpResponse(200, """{"items":[]}""") }

        val view = MonitoringClient(platform, "http://test").loadAlerts()

        assertEquals(0, view.count)
        assertTrue(view.items.isEmpty())
    }

    @Test
    fun anAlertWithoutEndedAtDecodesAsAnOpenEvent() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"items":[{"id":"a1","deviceId":"MCU001","state":"suspect",""" +
                    """"startedAt":"2026-09-21T10:00:00Z","evidence":{"gasAdcRise":10,""" +
                    """"temperatureRateCPerMinute":1.0,"sampleCount":3}}]}""",
            )
        }

        val view = MonitoringClient(platform, "http://test").loadAlerts()

        // `endedAt`, `gasAdcRiseThreshold` and `windowSeconds` are absent entirely.
        assertEquals("--", view.items.single().endedAt)
        assertEquals("--", view.items.single().windowSecondsText)
        assertTrue(view.items.single().active)
    }

    // --- settings ----------------------------------------------------------------------

    @Test
    fun settingsDecodeTheConfirmationLifecycle() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"desiredVersion":4,"confirmedVersion":3,"temperatureHighC":30,""" +
                    """"humidityHighRh":80,"gasHighPpm":20,"updatedAt":"2026-09-21T10:00:00Z",""" +
                    """"confirmationState":"pending"}""",
            )
        }

        val view = MonitoringClient(platform, "http://test").loadSettings()

        assertEquals("http://test/api/v1/devices/MCU001/thresholds", platform.requests.single().url)
        assertEquals("等待设备确认", view.confirmationText)
        assertFalse(view.confirmed)
        assertTrue(view.awaitingDevice)
        assertEquals(4, view.desiredVersion)
        assertEquals(3, view.confirmedVersion)
    }

    @Test
    fun aThresholdResponseWithoutAConfirmationStateDefaultsToPending() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"desiredVersion":1,"temperatureHighC":30,"humidityHighRh":80,"gasHighPpm":20}""",
            )
        }

        val view = MonitoringClient(platform, "http://test").loadSettings()

        assertEquals("等待设备确认", view.confirmationText)
        assertFalse(view.confirmed, "an absent confirmation state is not a confirmation")
    }

    // --- commands ----------------------------------------------------------------------

    @Test
    fun everyControlRequestCarriesAUniqueIdempotencyKey() = runTest {
        val platform = FakePlatform { HttpResponse(202, """{"requestId":"cmd-1","status":"pending"}""") }
        val client = MonitoringClient(platform, "http://test")

        client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))
        client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))
        client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))

        val keys = platform.requests.map { it.headers["Idempotency-Key"] }
        assertEquals(3, keys.toSet().size, "keys were reused: $keys")
    }

    @Test
    fun controlRequestsDeclareTheirContentTypeAndQueriesDoNotNeedAKey() = runTest {
        val platform = FakePlatform { request ->
            if (request.method == "GET") HttpResponse(200, """{"items":[]}""") else HttpResponse(202, """{"requestId":"c","status":"pending"}""")
        }
        val client = MonitoringClient(platform, "http://test")

        client.loadTrends()
        client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))

        val read = platform.requests.first()
        assertNull(read.headers["Idempotency-Key"], "a query must not carry a control key")
        val write = platform.requests.last()
        assertEquals("application/json; charset=utf-8", write.headers["Content-Type"])
    }

    @Test
    fun aThresholdUpdateSendsAllThreeFieldsAndRejectsBeforeSendingWhenOutOfRange() = runTest {
        val platform = FakePlatform { HttpResponse(202, """{"requestId":"cmd-2","status":"pending"}""") }
        val client = MonitoringClient(platform, "http://test")

        client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))

        val request = platform.requests.single()
        assertEquals("PUT", request.method)
        assertEquals("http://test/api/v1/devices/MCU001/thresholds", request.url)
        // Parsed rather than string-compared: Kotlin/JS renders 30.0 as `30` and
        // the JVM as `30.0`. Both are the same JSON number, and the contract's
        // `additionalProperties: false` is asserted by the size check.
        val body = Json.parseToJsonElement(request.body!!).jsonObject
        assertEquals(setOf("temperatureHighC", "humidityHighRh", "gasHighPpm"), body.keys)
        assertEquals(30.0, body.getValue("temperatureHighC").jsonPrimitive.double)
        assertEquals(80.0, body.getValue("humidityHighRh").jsonPrimitive.double)
        assertEquals(20.0, body.getValue("gasHighPpm").jsonPrimitive.double)

        val before = platform.requests.size
        assertFailsWith<IllegalArgumentException> {
            client.updateThresholds(ThresholdUpdate(30.0, 80.0, 5_000.0))
        }
        assertEquals(before, platform.requests.size, "an invalid payload must not reach the network")
    }

    @Test
    fun thresholdConfirmationIsReadBackFromTheCommandStatus() = runTest {
        val platform = FakePlatform { request ->
            when {
                request.url.endsWith("/thresholds") && request.method == "PUT" ->
                    HttpResponse(202, """{"requestId":"cmd-9","status":"pending","desiredVersion":5}""")
                else -> HttpResponse(
                    200,
                    """{"requestId":"cmd-9","deviceId":"MCU001","type":"set_thresholds","state":"applied",""" +
                        """"acceptedAt":"2026-09-21T10:00:00Z","confirmedVersion":5}""",
                )
            }
        }
        val client = MonitoringClient(platform, "http://test")

        val accepted = client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))
        assertFalse(accepted.confirmed)

        val settled = client.awaitCommandOutcome(accepted.requestId)

        assertEquals("设备已确认", settled?.stateText)
        assertTrue(settled?.confirmed == true)
    }

    // --- control loop ------------------------------------------------------------------

    @Test
    fun anAcknowledgementThatNeverArrivesIsReportedAsStillPending() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"requestId":"cmd-1","deviceId":"MCU001","type":"set_thresholds","state":"published",""" +
                    """"acceptedAt":"2026-09-21T10:00:00Z"}""",
            )
        }

        val outcome = MonitoringClient(platform, "http://test").awaitCommandOutcome("cmd-1")

        assertNull(outcome, "an unanswered command is not a failure, it is unresolved")
        assertEquals(MonitoringClient.DEFAULT_COMMAND_ATTEMPTS, platform.requests.size)
    }

    @Test
    fun aStatusReadFailureIsRetriedRatherThanAbortingTheWait() = runTest {
        var attempt = 0
        val platform = FakePlatform {
            attempt += 1
            if (attempt == 1) {
                HttpResponse(503, """{"error":{"code":"broker_unavailable","message":"down"}}""")
            } else {
                HttpResponse(
                    200,
                    """{"requestId":"cmd-1","deviceId":"MCU001","type":"set_thresholds","state":"applied",""" +
                        """"acceptedAt":"2026-09-21T10:00:00Z"}""",
                )
            }
        }

        val outcome = MonitoringClient(platform, "http://test").awaitCommandOutcome("cmd-1")

        assertEquals("设备已确认", outcome?.stateText)
        assertEquals(2, platform.requests.size)
    }

    @Test
    fun aPermanentStatusReadFailureIsNotMisreportedAsStillPending() = runTest {
        val platform = FakePlatform {
            HttpResponse(404, """{"error":{"code":"command_not_found","message":"unknown command"}}""")
        }

        val error = assertFailsWith<MonitoringException> {
            MonitoringClient(platform, "http://test").awaitCommandOutcome("missing")
        }

        assertEquals("command_not_found", error.code)
        assertEquals(404, error.statusCode)
        assertEquals(1, platform.requests.size)
    }

    @Test
    fun aRejectedCommandSettlesAsAFailure() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"requestId":"cmd-1","deviceId":"MCU001","type":"set_thresholds","state":"rejected",""" +
                    """"acceptedAt":"2026-09-21T10:00:00Z","errorCode":"invalid_threshold"}""",
            )
        }

        val settled = MonitoringClient(platform, "http://test").awaitCommandOutcome("cmd-1")
            ?: fail("a terminal state was recorded but the wait reported unresolved")

        assertTrue(settled.failed)
        assertEquals("invalid_threshold", settled.errorText)
        assertFalse(settled.confirmed)
    }

    // --- error mapping -----------------------------------------------------------------

    @Test
    fun aFourHundredErrorKeepsTheBackendCodeAndStatus() = runTest {
        val platform = FakePlatform {
            HttpResponse(422, """{"error":{"code":"invalid_threshold","message":"气体阈值超出范围"}}""")
        }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadSettings() }

        assertEquals("invalid_threshold", error.code)
        assertEquals("气体阈值超出范围", error.message)
        assertEquals(422, error.statusCode)
    }

    @Test
    fun aFiveHundredErrorKeepsItsStatusAndCode() = runTest {
        val platform = FakePlatform {
            HttpResponse(503, """{"error":{"code":"broker_unavailable","message":"broker down"}}""")
        }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadSettings() }

        assertEquals("broker_unavailable", error.code)
        assertEquals(503, error.statusCode)
    }

    @Test
    fun aConflictKeepsTheVersionConflictCode() = runTest {
        val platform = FakePlatform {
            HttpResponse(409, """{"error":{"code":"version_conflict","message":"key reuse"}}""")
        }

        val error = assertFailsWith<MonitoringException> {
            MonitoringClient(platform, "http://test").updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))
        }

        assertEquals("version_conflict", error.code)
        assertEquals(409, error.statusCode)
    }

    @Test
    fun aNonEnvelopeErrorBodyStillReportsTheHttpStatus() = runTest {
        val platform = FakePlatform { HttpResponse(502, "<html>bad gateway</html>") }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadSettings() }

        assertEquals("http_502", error.code)
        assertEquals(502, error.statusCode)
        assertTrue(error.message.contains("502"), "actual: ${error.message}")
    }

    @Test
    fun aMalformedSuccessBodySurfacesAsAParseFailure() = runTest {
        val platform = FakePlatform { HttpResponse(200, "{ not json") }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadSettings() }

        assertEquals("malformed_response", error.code)
        assertEquals(200, error.statusCode)
    }

    @Test
    fun anInvalidTelemetryValueIsAParseFailureRatherThanASilentDefault() = runTest {
        val platform = FakePlatform { request ->
            if (request.url.endsWith("/status")) {
                HttpResponse(200, statusBody)
            } else {
                HttpResponse(
                    200,
                    """{"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:00Z","temperatureC":"warm",""" +
                        """"humidityRh":50,"gasAdcRaw":1,"gasAdcFiltered":1,"localAlarm":false}""",
                )
            }
        }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadDashboard() }

        assertEquals("malformed_response", error.code)
        assertEquals(200, error.statusCode)
    }

    @Test
    fun anUnknownConnectivityValueIsRejectedInsteadOfGuesswork() = runTest {
        val platform = FakePlatform {
            HttpResponse(
                200,
                """{"deviceId":"MCU001","connectivity":"sometimes","alarmState":"normal","lastSeenAt":null}""",
            )
        }

        val error = assertFailsWith<MonitoringException> { MonitoringClient(platform, "http://test").loadDashboard() }

        assertEquals("malformed_response", error.code)
    }

    // --- configuration -----------------------------------------------------------------

    @Test
    fun aTrailingSlashOnTheBaseUrlDoesNotDoubleUpThePath() = runTest {
        val platform = FakePlatform { HttpResponse(200, """{"items":[]}""") }

        MonitoringClient(platform, "http://test/").loadTrends()

        assertEquals(
            "http://test/api/v1/devices/MCU001/telemetry" +
                "?from=2026-09-22T00:30:45Z&to=2026-09-22T01:30:45Z&limit=200&order=desc",
            platform.requests.single().url,
        )
    }

    @Test
    fun theDeviceIdIsConfigurableAndAppearsInEveryPath() = runTest {
        val platform = FakePlatform { HttpResponse(200, """{"items":[]}""") }

        MonitoringClient(platform, "http://test", deviceId = "LAB-2").loadAlerts()

        assertEquals("http://test/api/v1/devices/LAB-2/alerts?limit=50", platform.requests.single().url)
    }

    // --- encoding for the MiniApp boundary ---------------------------------------------

    @Test
    fun viewsEncodeToJsonThatAHostCanParse() = runTest {
        val platform = FakePlatform { request ->
            when {
                request.url.endsWith("/status") -> HttpResponse(200, statusBody)
                request.url.contains("/telemetry/latest") -> HttpResponse(200, telemetryBody)
                else -> HttpResponse(200, """{"items":[]}""")
            }
        }
        val client = MonitoringClient(platform, "http://test")

        val dashboard = client.encodeDashboard(client.loadDashboard())
        val trends = client.encodeTrends(client.loadTrends())
        val alerts = client.encodeAlerts(client.loadAlerts())

        assertTrue(dashboard.contains("\"riskTone\""))
        assertTrue(dashboard.contains("\"hasData\":true"))
        assertTrue(trends.contains("\"series\""))
        assertTrue(alerts.contains("\"count\":0"))
    }

    @Test
    fun everyViewHasAnEncoderSoNoHostHasToBuildJsonItself() = runTest {
        val platform = FakePlatform { request ->
            when {
                request.method == "GET" && request.url.endsWith("/thresholds") ->
                    HttpResponse(
                        200,
                        """{"desiredVersion":2,"temperatureHighC":30,"humidityHighRh":80,""" +
                            """"gasHighPpm":20,"confirmedVersion":2,"confirmationState":"confirmed"}""",
                    )
                request.method == "GET" -> HttpResponse(200, """{"items":[]}""")
                else -> HttpResponse(202, """{"requestId":"cmd-1","status":"pending"}""")
            }
        }
        val client = MonitoringClient(platform, "http://test")

        val settings = client.encodeSettings(client.loadSettings())
        val command = client.encodeCommandStatus(client.updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0)))

        assertTrue(settings.contains("\"confirmationText\":\"设备已确认\""), "actual: $settings")
        assertTrue(settings.contains("\"confirmed\":true"))
        // The enqueue acknowledgement must never encode as a device confirmation.
        assertTrue(command.contains("\"stateText\":\"等待设备确认\""), "actual: $command")
        assertTrue(command.contains("\"confirmed\":false"))
    }

    @Test
    fun anAbsentGasReadingIsOmittedRatherThanEncodedAsNull() = runTest {
        val platform = FakePlatform { request ->
            if (request.url.endsWith("/status")) {
                HttpResponse(200, statusBody)
            } else {
                HttpResponse(
                    200,
                    """{"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:00Z","temperatureC":20,""" +
                        """"humidityRh":40,"gasAdcRaw":1,"gasAdcFiltered":1,"gasPpm":null,"localAlarm":false}""",
                )
            }
        }

        val encoded = MonitoringClient(platform, "http://test").let { it.encodeDashboard(it.loadDashboard()) }

        // explicitNulls=false: the page sees `undefined`, and the shared view has
        // already resolved it to the "--" placeholder.
        assertTrue(encoded.contains("\"gasText\":\"--\""), "actual: $encoded")
        assertFalse(encoded.contains("\"gasPpm\":null"))
    }
}

/**
 * Platform stub that answers from a lambda and records every request.
 *
 * Idempotency keys are sequential so a test can assert both uniqueness and the
 * exact header a request carried.
 */
private class FakePlatform(
    /**
     * Pinned so a window query produces exactly assertable `from`/`to` bounds
     * instead of a timestamp that changes with the wall clock. Declared before
     * the handler so the usual `FakePlatform { ... }` call still passes the
     * lambda as the trailing argument.
     */
    private val now: Long = FIXED_NOW,
    private val handler: (HttpRequest) -> HttpResponse,
) : MonitoringPlatform {
    val requests = mutableListOf<HttpRequest>()
    private var key = 0

    override suspend fun request(request: HttpRequest): HttpResponse {
        requests += request
        return handler(request)
    }

    override fun newIdempotencyKey(): String = "key-${++key}"

    override fun nowMillis(): Long = now
}

/**
 * 2026-09-22T01:30:45Z. The exact bounds the window queries are asserted against
 * are derived from it: 近1小时 starts at 2026-09-22T00:30:45Z, 近6小时 at
 * 2026-09-21T19:30:45Z and 近24小时 at 2026-09-21T01:30:45Z.
 */
private const val FIXED_NOW = 1_790_040_645_000L
