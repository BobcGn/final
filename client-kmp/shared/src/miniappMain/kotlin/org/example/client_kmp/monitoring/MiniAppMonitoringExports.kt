@file:OptIn(ExperimentalJsExport::class)

package org.example.client_kmp.monitoring

import io.github.bobcgn.miniapp.export.MiniAppExports
import kotlin.js.Date
import kotlin.js.ExperimentalJsExport
import kotlin.js.JsExport

/**
 * WeChat transport adapter.
 *
 * The request is delegated to the MiniApp SDK host binding because the WeChat
 * runtime has no `fetch` or `XMLHttpRequest`. The SDK call is synchronous from
 * Kotlin's point of view, so suspending here only yields the coroutine
 * dispatcher, it does not block a thread the way Android's adapter does.
 */
private class MiniAppMonitoringPlatform : MonitoringPlatform {
    private var sequence = 0

    override suspend fun request(request: HttpRequest): HttpResponse {
        val headers = request.headers.flatMap { (name, value) -> listOf(name, value) }.toTypedArray()
        val result = MiniAppExports.networkRequest(request.url, request.method, headers, request.body, REQUEST_TIMEOUT_MS)
        return HttpResponse(result.statusCode, result.body)
    }

    /**
     * Built from the clock plus a per-session counter.
     *
     * `crypto.randomUUID` is not available in every WeChat base library, so the
     * pair of "milliseconds plus monotonic sequence" is used instead: it is
     * unique within and across sessions without needing a host API.
     */
    override fun newIdempotencyKey(): String = "miniapp-${Date.now().toLong()}-${++sequence}"

    /**
     * The WeChat runtime's clock. `Date.now()` is the only epoch source the base
     * library guarantees, so it is what the shared window bound is built from.
     */
    override fun nowMillis(): Long = Date.now().toLong()

    private companion object {
        const val REQUEST_TIMEOUT_MS = 8_000
    }
}

/**
 * Stable CommonJS boundary consumed by `client-kmp/miniApp`.
 *
 * The bundle is resolved by the WeChat host at
 * `require('./kotlin/client-kmp-shared-miniapp.js')` and this object lives at
 * `org.example.client_kmp.monitoring.LabMonitorExports`. `runtime.js` owns that
 * path so page code never touches the generated bundle layout.
 *
 * Every accessor returns a JSON string rather than a Kotlin object graph, so
 * page data stays plain JavaScript and no presentation rule is re-implemented
 * in WXML or page scripts. Suspending functions surface in JavaScript as
 * Promises that reject with the [MonitoringException] message and `code`.
 */
@JsExport
object LabMonitorExports {
    private var client = MonitoringClient(MiniAppMonitoringPlatform(), DEFAULT_BASE_URL)

    /**
     * Points the runtime at a backend.
     *
     * Called once from `app.js` on launch. A WeChat mini program cannot reach
     * `localhost`, so [baseUrl] must be the development machine's LAN address
     * when running on a real phone.
     */
    fun configure(baseUrl: String, deviceId: String = DEFAULT_DEVICE_ID) {
        client = MonitoringClient(MiniAppMonitoringPlatform(), baseUrl, deviceId)
    }

    suspend fun dashboard(): String = client.encodeDashboard(client.loadDashboard())

    /**
     * Loads the trends view for one window.
     *
     * [window] is a [TrendWindow] key (`LAST_HOUR`, `LAST_SIX_HOURS`,
     * `LAST_DAY`). An unrecognised key falls back to the shortest window instead
     * of throwing: the key arrives from a WXML `data-` attribute, and a stale one
     * should render the default range rather than blank the page.
     */
    suspend fun trends(
        window: String = DEFAULT_WINDOW_KEY,
        limit: Int = MonitoringClient.DEFAULT_TREND_LIMIT,
    ): String = client.encodeTrends(client.loadTrends(windowFromKey(window), limit))

    /**
     * Loads the alert page narrowed by [filter], an [AlertFilter] key
     * (`all` selects everything the backend returned).
     */
    suspend fun alerts(
        filter: String = AlertFilter.ALL.key,
        limit: Int = MonitoringClient.DEFAULT_ALERT_LIMIT,
    ): String = client.encodeAlerts(client.loadAlerts(limit, filterFromKey(filter)))

    suspend fun settings(): String = client.encodeSettings(client.loadSettings())

    /**
     * The selector option lists, as JSON.
     *
     * The page draws the trends window selector before it has fetched any history,
     * and WXML cannot enumerate a Kotlin enum, so the labels have to arrive from
     * here. Exposing them keeps the selector copy in the shared layer instead of
     * restating it in the page script where it could drift from Android's.
     */
    fun selectors(): String = client.encodeSelectors(MonitoringPresentation.selectors())

    /** Reads the recorded outcome of an enqueued command; the only way to see a device acknowledgement. */
    suspend fun commandStatus(requestId: String): String =
        client.encodeCommandStatus(client.loadCommandStatus(requestId))

    /**
     * Waits for the device acknowledgement that settles a command.
     *
     * @return the settled view as JSON, or null when the device did not answer
     *   within the shared polling budget. Null means "still awaiting", which a
     *   host must not present as success or as failure.
     */
    suspend fun awaitCommandOutcome(requestId: String): String? =
        client.awaitCommandOutcome(requestId)?.let { client.encodeCommandStatus(it) }

    /** Validates and enqueues thresholds. Rejects before any network call when a value is out of range. */
    suspend fun updateThresholds(
        temperatureHighC: Double,
        humidityHighRh: Double,
        gasHighPpm: Double,
    ): String = client.encodeCommandStatus(
        client.updateThresholds(ThresholdUpdate(temperatureHighC, humidityHighRh, gasHighPpm)),
    )

    private const val DEFAULT_BASE_URL = "http://127.0.0.1:8080"
    private const val DEFAULT_DEVICE_ID = "MCU001"
    private const val DEFAULT_WINDOW_KEY = "LAST_HOUR"

    private fun windowFromKey(key: String): TrendWindow =
        TrendWindow.entries.firstOrNull { it.name == key } ?: TrendWindow.LAST_HOUR

    private fun filterFromKey(key: String): AlertFilter =
        AlertFilter.entries.firstOrNull { it.key == key } ?: AlertFilter.ALL
}
