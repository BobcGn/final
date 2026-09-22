package org.example.client_kmp.monitoring

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.jsonObject
import kotlin.test.Test
import kotlin.test.assertTrue

/**
 * Pins the field names `miniApp/pages/monitor/monitor.wxml` binds.
 *
 * The WeChat host reads plain JSON and WXML cannot fail loudly: a renamed field
 * simply renders blank. This test fails instead, so a shared-model rename cannot
 * silently blank a MiniApp screen.
 *
 * When WXML starts binding a new field, add it here.
 */
class MiniAppWxmlBindingTest {

    private val client = MonitoringClient(NeverCalledPlatform, "http://test")

    @Test
    fun theDashboardJsonCarriesEveryFieldTheMonitorTabBinds() {
        val view = MonitoringPresentation.dashboard(
            DeviceStatus(
                deviceId = "MCU001",
                connectivity = Connectivity.online,
                alarmState = AlertState.fire_warning,
                lastSeenAt = "2026-09-21T09:00:00Z",
                localAlarm = true,
            ),
            TelemetryPoint(
                deviceId = "MCU001",
                receivedAt = "2026-09-21T10:00:00Z",
                temperatureC = 40.0,
                humidityRh = 55.0,
                gasAdcRaw = 900,
                gasAdcFiltered = 880,
                gasPpm = 180.0,
                localAlarm = true,
            ),
        )

        assertHasAll(
            keysOf(client.encodeDashboard(view)),
            // risk block, the three meters and the device card
            "riskTone", "riskText", "riskDetail", "connectivityText", "online", "hasData",
            "temperatureText", "temperaturePercent",
            "humidityText", "humidityPercent",
            "gasText", "gasPercent",
            "deviceId", "localAlarm", "localAlarmText", "buzzerText", "updatedAt",
        )
    }

    @Test
    fun theTrendsJsonCarriesEveryFieldTheTrendTabBinds() {
        val json = client.encodeTrends(
            MonitoringPresentation.trends(
                listOf(
                    TelemetryPoint(
                        deviceId = "MCU001",
                        receivedAt = "2026-09-21T10:00:00Z",
                        temperatureC = 20.0,
                        humidityRh = 40.0,
                        gasAdcRaw = 100,
                        gasAdcFiltered = 98,
                        gasPpm = 12.0,
                        localAlarm = false,
                        bootId = "b1",
                        sequence = 3,
                    ),
                ),
            ),
        )

        assertHasAll(
            keysOf(json),
            "hasData", "sampleCount", "gasSampleCount", "temperature", "humidity", "gas", "series",
            // The window selector and the curve frame are rendered by the host, so
            // a rename here would blank them without failing anything else.
            "windowKey", "windowLabel", "windowOptions",
            "curveStatusText", "curveMaskTitle", "curveMaskSub",
            "curveAxisStart", "curveAxisEnd", "curveReady", "curveLegend", "footerHint",
        )
        assertHasAll(
            keysOfField(json, "temperature"),
            // `peakAt` is the peak-time line on every statistic card.
            "minimum", "average", "maximum", "peakAt",
        )
        assertHasAll(
            keysOfRow(json, "curveLegend"),
            "label", "tone", "rangeText",
        )
        assertHasAll(
            keysOfRow(json, "windowOptions"),
            "key", "label",
        )
        // `key` is what the list uses for `wx:key`; it must always be emitted.
        assertHasAll(
            keysOfRow(json, "series"),
            "key", "receivedAt", "timeText", "temperatureText", "humidityText", "gasText", "localAlarm",
            "timestampEpochMs", "temperatureC", "humidityRh", "gasPpm",
        )
    }

    @Test
    fun theAlertsJsonCarriesEveryFieldTheAlertTabBinds() {
        val json = client.encodeAlerts(
            MonitoringPresentation.alerts(
                listOf(
                    AlertEvent(
                        id = "a1",
                        deviceId = "MCU001",
                        state = AlertState.fire_warning,
                        startedAt = "2026-09-21T10:00:00Z",
                        evidence = AlertEvidence(
                            gasAdcRise = 240,
                            temperatureRateCPerMinute = 2.5,
                            sampleCount = 12,
                            gasAdcRiseThreshold = 200,
                            temperatureRateThresholdCPerMinute = 1.5,
                            windowSeconds = 60,
                        ),
                    ),
                ),
            ),
        )

        assertHasAll(
            keysOf(json),
            // `visibleCount` decides the empty-state line and `filters` draws the
            // filter bar; `filterKey` marks the active pill.
            "count", "visibleCount", "filterKey", "filters", "items",
        )
        assertHasAll(
            keysOfRow(json, "filters"),
            "key", "label",
        )
        assertHasAll(
            keysOfRow(json, "items"),
            // `id` is the wx:key; the rest are rendered in the card.
            "id", "state", "stateText", "tone", "startedAt", "endedAt", "active",
            "gasAdcRiseText", "gasAdcRiseThresholdText",
            "temperatureRateText", "temperatureRateThresholdText",
            "sampleCountText", "windowSecondsText",
        )
    }

    @Test
    fun theSettingsJsonCarriesEveryFieldTheSettingsTabBinds() {
        val json = client.encodeSettings(
            MonitoringPresentation.settings(
                Thresholds(
                    desiredVersion = 4,
                    temperatureHighC = 30.0,
                    humidityHighRh = 80.0,
                    gasHighPpm = 20.0,
                    confirmedVersion = 3,
                    updatedAt = "2026-09-21T10:00:00Z",
                    confirmationState = ConfirmationState.pending,
                ),
            ),
        )

        assertHasAll(
            keysOf(json),
            "temperatureHighC", "humidityHighRh", "gasHighPpm",
            "desiredVersion", "confirmedVersion",
            "confirmationState", "confirmationText", "confirmationTone", "updatedAt",
        )
    }

    @Test
    fun theCommandStatusJsonCarriesEveryFieldTheControlButtonsBind() {
        val json = client.encodeCommandStatus(
            MonitoringPresentation.commandAccepted(
                CommandAccepted(requestId = "cmd-1", status = "pending", desiredVersion = 5),
            ),
        )

        assertHasAll(
            keysOf(json),
            "requestId", "state", "stateText", "tone", "settled", "confirmed", "failed", "versionText", "errorText",
        )
    }

    // --- helpers -------------------------------------------------------------------------

    private fun keysOf(json: String): Set<String> = Json.parseToJsonElement(json).jsonObject.keys

    private fun keysOfField(json: String, field: String): Set<String> =
        Json.parseToJsonElement(json).jsonObject.getValue(field).jsonObject.keys

    /** Keys of the first element of the named array; the fixtures always have one. */
    private fun keysOfRow(json: String, array: String): Set<String> =
        (Json.parseToJsonElement(json).jsonObject.getValue(array) as JsonArray).first().jsonObject.keys

    private fun assertHasAll(actual: Set<String>, vararg bound: String) {
        val missing = bound.filterNot { it in actual }
        assertTrue(missing.isEmpty(), "the Host UI binds fields the runtime no longer emits: $missing")
    }

    /** The encoders under test never reach the network, so this must never run. */
    private object NeverCalledPlatform : MonitoringPlatform {
        override suspend fun request(request: HttpRequest): HttpResponse =
            error("the binding test must not perform I/O")

        override fun newIdempotencyKey(): String = error("the binding test must not perform I/O")

        // These tests only call the encoders, so the clock is never read; it is
        // implemented to keep the stub honest rather than to be meaningful.
        override fun nowMillis(): Long = 0L
    }
}
