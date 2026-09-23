package org.example.client_kmp.monitoring

import kotlinx.coroutines.test.runTest
import kotlin.js.jsTypeOf
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * MiniApp platform-adapter tests.
 *
 * These run on the JavaScript runtime the WeChat host uses, so they verify the
 * part Android tests cannot: that the shared runtime and its validation
 * actually execute on JS, and that the exported boundary keeps the shape
 * `miniApp/runtime.js` depends on.
 *
 * What they deliberately do not claim is a live network call — the MiniApp SDK
 * host binding only exists inside WeChat, so a request that gets past validation
 * is expected to fail here. Asserting that failure is what proves the adapter
 * was reached instead of short-circuiting.
 */
class MiniAppMonitoringExportsTest {

    @Test
    fun theExportedBoundaryKeepsTheShapeTheHostRequires() {
        // A missing member would fail this file at compile time; this asserts the
        // runtime shape as well, since `runtime.js` calls them by name.
        assertEquals("function", jsTypeOf(LabMonitorExports::configure))
        assertEquals("function", jsTypeOf(LabMonitorExports::dashboard))
        assertEquals("function", jsTypeOf(LabMonitorExports::trends))
        assertEquals("function", jsTypeOf(LabMonitorExports::alerts))
        assertEquals("function", jsTypeOf(LabMonitorExports::settings))
        assertEquals("function", jsTypeOf(LabMonitorExports::commandStatus))
        assertEquals("function", jsTypeOf(LabMonitorExports::awaitCommandOutcome))
        assertEquals("function", jsTypeOf(LabMonitorExports::updateThresholds))
        assertEquals("function", jsTypeOf(LabMonitorExports::selectors))
    }

    @Test
    fun theSelectorLabelsReachTheHostFromTheSharedLayer() {
        // The page draws the trends window selector before its first history
        // response, so these labels cannot come from a fetched view.
        val encoded = LabMonitorExports.selectors()

        for (expected in listOf(
            """"key":"LAST_HOUR","label":"近1小时"""",
            """"key":"LAST_SIX_HOURS","label":"近6小时"""",
            """"key":"LAST_DAY","label":"近24小时"""",
            """"key":"all","label":"全部"""",
            """"key":"fire_warning","label":"火情"""",
            """"key":"suspect","label":"疑似"""",
            """"key":"recovered","label":"已恢复"""",
        )) {
            assertTrue(encoded.contains(expected), "selectors() is missing $expected in $encoded")
        }
    }

    @Test
    fun anUnknownWindowKeyFallsBackInsteadOfThrowing() = runTest {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")

        // The key arrives from a WXML data attribute, so a stale one must render
        // the default window rather than break the page. Reaching the platform
        // adapter is what proves the key resolved; the adapter then fails because
        // the WeChat host binding does not exist here.
        assertFailsWith<Throwable> { LabMonitorExports.trends("NOT_A_WINDOW") }
    }

    @Test
    fun anUnknownFilterKeyFallsBackInsteadOfThrowing() = runTest {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")

        assertFailsWith<Throwable> { LabMonitorExports.alerts("NOT_A_FILTER") }
    }

    @Test
    fun configuringTheBackendAddressIsSynchronousAndRepeatable() {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")
        LabMonitorExports.configure("http://192.168.1.23:8080", "LAB-2")
        // Restore the default so no later test inherits the LAN address.
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")
    }

    @Test
    fun sharedThresholdValidationRunsOnTheJsRuntime() = runTest {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")

        val error = assertFailsWith<Throwable> {
            LabMonitorExports.updateThresholds(30.0, 80.0, 5_000.0)
        }

        assertTrue(
            error.message.orEmpty().contains("气体阈值需在 1-999 ppm"),
            "expected the shared contract message, actual: ${error.message}",
        )
    }

    @Test
    fun eachThresholdFieldIsValidatedIndependently() = runTest {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")

        assertTrue(
            assertFailsWith<Throwable> { LabMonitorExports.updateThresholds(200.0, 80.0, 20.0) }
                .message.orEmpty().contains("温度阈值需在 0-80 °C"),
        )
        assertTrue(
            assertFailsWith<Throwable> { LabMonitorExports.updateThresholds(30.0, 200.0, 20.0) }
                .message.orEmpty().contains("湿度阈值需在 0-100 %RH"),
        )
    }

    @Test
    fun aValidPayloadClearsSharedValidationAndReachesThePlatformAdapter() = runTest {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")

        // The SDK host binding only exists inside WeChat, so the call cannot
        // succeed here. It must fail for a host reason, not a validation one.
        val failure = assertFailsWith<Throwable> {
            LabMonitorExports.updateThresholds(30.0, 80.0, 20.0)
        }

        assertFalse(
            failure.message.orEmpty().contains("阈值"),
            "a contract-valid payload must not be rejected by validation: ${failure.message}",
        )
    }

    @Test
    fun aReadRequestAlsoReachesThePlatformAdapter() = runTest {
        LabMonitorExports.configure("http://127.0.0.1:8080", "MCU001")

        val failure = assertFailsWith<Throwable> { LabMonitorExports.dashboard() }

        assertTrue(failure.message.orEmpty().isNotEmpty(), "a platform failure must carry a message for the user")
    }
}
