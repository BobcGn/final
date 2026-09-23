package org.example.client_kmp

import com.sun.net.httpserver.HttpExchange
import com.sun.net.httpserver.HttpServer
import kotlinx.coroutines.test.runTest
import org.example.client_kmp.monitoring.MonitoringClient
import org.example.client_kmp.monitoring.MonitoringException
import org.example.client_kmp.monitoring.ThresholdUpdate
import java.net.InetSocketAddress
import java.util.UUID
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

/**
 * Android platform-adapter tests.
 *
 * The round-trip cases run against a real loopback HTTP server so the adapter's
 * `HttpURLConnection` handling — channels, the error stream on 4xx/5xx, header
 * propagation and body writing — is exercised for real rather than mocked. A
 * stub would not catch the difference between `inputStream` and `errorStream`,
 * which is exactly where a control client silently loses the backend's error
 * envelope.
 */
class AndroidMonitoringPlatformTest {

    private var server: HttpServer? = null

    @AfterTest
    fun stopServer() {
        server?.stop(0)
        server = null
    }

    // --- idempotency keys ---------------------------------------------------------------

    @Test
    fun everyIdempotencyKeyIsUnique() {
        val platform = AndroidMonitoringPlatform()

        val keys = List(500) { platform.newIdempotencyKey() }

        assertEquals(500, keys.toSet().size, "a repeated key would make the backend treat a new command as a duplicate")
    }

    @Test
    fun anIdempotencyKeyFitsTheContractLengthAndCharacterSet() {
        val platform = AndroidMonitoringPlatform()

        repeat(50) {
            val key = platform.newIdempotencyKey()
            // Contract: opaque string, 1..64 characters.
            assertTrue(key.length in 1..64, "key out of contract length: $key")
            assertEquals(key, UUID.fromString(key).toString(), "key is not a canonical UUID: $key")
        }
    }

    // --- real HTTP round trips -----------------------------------------------------------

    @Test
    fun aSuccessfulReadIsDecodedFromTheRealResponse() = runTest {
        val requests = mutableListOf<RecordedRequest>()
        val baseUrl = start(
            requests,
            """{"items":[{"deviceId":"MCU001","receivedAt":"2026-09-21T10:00:00Z","temperatureC":21.5,""" +
                """"humidityRh":44,"gasAdcRaw":120,"gasAdcFiltered":118,"gasPpm":9,"localAlarm":false}]}""",
        )

        val view = MonitoringClient(AndroidMonitoringPlatform(), baseUrl).loadTrends()

        assertEquals(1, view.sampleCount)
        // 21.5 rounds to 22: the DHT11 resolves whole degrees, so the displayed
        // figure is a whole number even when the wire carries a fraction.
        assertEquals("22", view.temperature.average)
        val request = requests.single()
        assertEquals("GET", request.method)
        assertEquals("/api/v1/devices/MCU001/telemetry", request.path)
        // The bounds come from the real clock through this platform's own
        // `nowMillis`, so the shape is asserted rather than the exact instants.
        assertTrue(request.query.startsWith("from=20"))
        assertTrue(request.query.contains("&to=20"))
        assertTrue(request.query.endsWith("&limit=200&order=desc"))
    }

    @Test
    fun aControlCommandReachesTheServerWithItsBodyIdempotencyKeyAndVerb() = runTest {
        val requests = mutableListOf<RecordedRequest>()
        val baseUrl = start(requests, """{"requestId":"cmd-7","status":"pending"}""", status = 202)

        val accepted = MonitoringClient(AndroidMonitoringPlatform(), baseUrl)
            .updateThresholds(ThresholdUpdate(30.0, 80.0, 20.0))

        val request = requests.single()
        assertEquals("PUT", request.method)
        assertEquals("/api/v1/devices/MCU001/thresholds", request.path)
        assertEquals("""{"temperatureHighC":30.0,"humidityHighRh":80.0,"gasHighPpm":20.0}""", request.body)
        assertTrue(request.contentType.orEmpty().startsWith("application/json"))
        assertTrue(!request.idempotencyKey.isNullOrBlank(), "control request carried no Idempotency-Key")
        // 202 means accepted, and the shared view must say so rather than claim confirmation.
        assertEquals("等待设备确认", accepted.stateText)
        assertFalse(accepted.confirmed)
    }

    @Test
    fun aThresholdUpdateSendsAllThreeFieldsOverTheWire() = runTest {
        val requests = mutableListOf<RecordedRequest>()
        val baseUrl = start(requests, """{"requestId":"cmd-8","status":"pending"}""", status = 202)

        MonitoringClient(AndroidMonitoringPlatform(), baseUrl).updateThresholds(ThresholdUpdate(31.5, 77.0, 42.0))

        val request = requests.single()
        assertEquals("PUT", request.method)
        assertEquals("/api/v1/devices/MCU001/thresholds", request.path)
        assertEquals("""{"temperatureHighC":31.5,"humidityHighRh":77.0,"gasHighPpm":42.0}""", request.body)
    }

    @Test
    fun aBackendErrorEnvelopeIsRecoveredFromTheErrorStream() = runTest {
        val requests = mutableListOf<RecordedRequest>()
        val baseUrl = start(
            requests,
            """{"error":{"code":"invalid_threshold","message":"气体阈值超出范围"}}""",
            status = 422,
        )

        val error = assertFailsWith<MonitoringException> {
            MonitoringClient(AndroidMonitoringPlatform(), baseUrl).updateThresholds(ThresholdUpdate(30.0, 70.0, 20.0))
        }

        // Reading errorStream rather than the body is what makes this survive; a
        // null stream here would collapse the code into a bare HTTP status.
        assertEquals("invalid_threshold", error.code)
        assertEquals("气体阈值超出范围", error.message)
        assertEquals(422, error.statusCode)
    }

    @Test
    fun aServerErrorWithoutABodyStillReportsItsStatus() = runTest {
        val requests = mutableListOf<RecordedRequest>()
        val baseUrl = start(requests, "", status = 500)

        val error = assertFailsWith<MonitoringException> {
            MonitoringClient(AndroidMonitoringPlatform(), baseUrl).loadSettings()
        }

        assertEquals(500, error.statusCode)
        assertNotEquals("", error.code)
    }

    @Test
    fun aConnectionFailureIsReportedAndDoesNotHangTheCaller() = runTest {
        // Port 1 on loopback refuses immediately; the adapter must surface an I/O
        // failure rather than swallow it into an empty success.
        val platform = AndroidMonitoringPlatform()

        assertFailsWith<Exception> {
            MonitoringClient(platform, "http://127.0.0.1:1").loadTrends()
        }
    }

    // --- helpers -------------------------------------------------------------------------

    private data class RecordedRequest(
        val method: String,
        val path: String,
        val query: String,
        val body: String,
        val contentType: String?,
        val idempotencyKey: String?,
    )

    /**
     * Starts a loopback server that answers every request with [body] and
     * [status], recording what arrived.
     *
     * @return the base URL to hand to the client.
     */
    private fun start(
        recorded: MutableList<RecordedRequest>,
        body: String,
        status: Int = 200,
    ): String {
        val instance = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        instance.createContext("/") { exchange: HttpExchange ->
            recorded += RecordedRequest(
                method = exchange.requestMethod,
                path = exchange.requestURI.path,
                query = exchange.requestURI.query.orEmpty(),
                body = exchange.requestBody.bufferedReader(Charsets.UTF_8).use { it.readText() },
                contentType = exchange.requestHeaders.getFirst("Content-Type"),
                idempotencyKey = exchange.requestHeaders.getFirst("Idempotency-Key"),
            )
            val bytes = body.toByteArray(Charsets.UTF_8)
            exchange.responseHeaders.add("Content-Type", "application/json; charset=utf-8")
            exchange.sendResponseHeaders(status, bytes.size.toLong())
            exchange.responseBody.use { it.write(bytes) }
        }
        instance.start()
        server = instance
        return "http://127.0.0.1:${instance.address.port}"
    }
}
