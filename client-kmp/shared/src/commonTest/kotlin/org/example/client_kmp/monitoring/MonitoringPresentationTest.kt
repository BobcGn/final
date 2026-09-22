package org.example.client_kmp.monitoring

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertFailsWith
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * Covers the pure rules both hosts render from.
 *
 * These are the assertions that make "Android and the MiniApp show the same
 * thing" checkable rather than aspirational: the labels, tones and numbers
 * asserted here are the only source either UI uses.
 */
class MonitoringPresentationTest {

    // --- dashboard: alert state mapping -------------------------------------------------

    @Test
    fun everyAlertStateMapsToItsOwnLabelAndTone() {
        val cases = listOf(
            AlertState.normal to Triple("环境正常", "各项指标处于安全范围", Tone.MINT),
            AlertState.suspect to Triple("疑似异常", "部分条件异常，系统确认中", Tone.WARNING),
            AlertState.fire_warning to Triple("火情预警", "气体突增与温升速率同时超限", Tone.DANGER),
            AlertState.recovered to Triple("指标已恢复", "事件归档中", Tone.INFO),
        )
        for ((state, expected) in cases) {
            val view = MonitoringPresentation.dashboard(status(alarmState = state), telemetry())
            assertEquals(expected.first, view.riskText, "riskText for $state")
            assertEquals(expected.second, view.riskDetail, "riskDetail for $state")
            assertEquals(expected.third, view.riskTone, "tone for $state")
        }
    }

    @Test
    fun riskLevelNamesStayStableForHostStyleClasses() {
        // The MiniApp keys WXSS classes off these exact strings.
        assertEquals("normal", MonitoringPresentation.dashboard(status(), telemetry()).riskLevel)
        assertEquals(
            "suspect",
            MonitoringPresentation.dashboard(status(alarmState = AlertState.suspect), telemetry()).riskLevel,
        )
        assertEquals(
            "fire",
            MonitoringPresentation.dashboard(status(alarmState = AlertState.fire_warning), telemetry()).riskLevel,
        )
        assertEquals(
            "recovered",
            MonitoringPresentation.dashboard(status(alarmState = AlertState.recovered), telemetry()).riskLevel,
        )
    }

    // --- dashboard: connectivity --------------------------------------------------------

    @Test
    fun everyConnectivityValueHasWordingAndAnOnlineFlag() {
        assertEquals("在线" to true, connectivityOf(Connectivity.online))
        assertEquals("离线" to false, connectivityOf(Connectivity.offline))
        assertEquals("未知" to false, connectivityOf(Connectivity.unknown))
    }

    private fun connectivityOf(value: Connectivity): Pair<String, Boolean> {
        val view = MonitoringPresentation.dashboard(status(connectivity = value), telemetry())
        return view.connectivityText to view.online
    }

    // --- dashboard: no telemetry yet ----------------------------------------------------

    @Test
    fun aDeviceWithoutTelemetryShowsPlaceholdersInsteadOfZeroes() {
        val view = MonitoringPresentation.dashboard(status(alarmState = AlertState.fire_warning), null)

        assertFalse(view.hasData)
        assertEquals("--", view.temperatureText)
        assertEquals("--", view.humidityText)
        assertEquals("--", view.gasText)
        assertFalse(view.gasAvailable)
        assertEquals(0, view.temperaturePercent)
        assertEquals(0, view.humidityPercent)
        assertEquals(0, view.gasPercent)
        // Alarm state is still real without a sample, so it must not be blanked.
        assertEquals("火情预警", view.riskText)
        assertEquals("2026-09-21T09:00:00Z", view.updatedAt)
    }

    @Test
    fun alarmFallsBackToDeviceStatusWhenTelemetryIsMissing() {
        val offlineAlarm = MonitoringPresentation.dashboard(
            status(connectivity = Connectivity.offline, localAlarm = true),
            null,
        )

        assertTrue(offlineAlarm.localAlarm)
        assertEquals("报警中", offlineAlarm.localAlarmText)
        assertEquals("报警策略生效", offlineAlarm.buzzerText)
    }

    // --- dashboard: gas --------------------------------------------------------------------

    @Test
    fun anUncalibratedSampleNeverRendersGasAsZero() {
        val view = MonitoringPresentation.dashboard(status(), telemetry(gasPpm = null))

        assertTrue(view.hasData)
        assertEquals("--", view.gasText)
        assertFalse(view.gasAvailable)
        assertEquals(0, view.gasPercent)
    }

    @Test
    fun aMeasuredGasValueIsRenderedEvenWhenItIsZero() {
        val view = MonitoringPresentation.dashboard(status(), telemetry(gasPpm = 0.0))

        assertEquals("0", view.gasText)
        assertTrue(view.gasAvailable)
        assertEquals(0, view.gasPercent)
    }

    // --- dashboard: meter scaling ----------------------------------------------------------

    @Test
    fun metersScaleAgainstTheAlarmBoundary() {
        val view = MonitoringPresentation.dashboard(
            status(),
            telemetry(temperatureC = 40.0, humidityRh = 25.0, gasPpm = 99.9),
        )

        assertEquals(50, view.temperaturePercent, "40 of 80 °C")
        assertEquals(25, view.humidityPercent, "25 of 100 %RH")
        assertEquals(10, view.gasPercent, "99.9 of 999 ppm")
    }

    @Test
    fun meterPercentagesAreClampedToTheBarRange() {
        val over = MonitoringPresentation.dashboard(
            status(),
            telemetry(temperatureC = 500.0, humidityRh = 500.0, gasPpm = 5_000.0),
        )
        assertEquals(100, over.temperaturePercent)
        assertEquals(100, over.humidityPercent)
        assertEquals(100, over.gasPercent)

        val under = MonitoringPresentation.dashboard(
            status(),
            telemetry(temperatureC = -50.0, humidityRh = -50.0, gasPpm = -50.0),
        )
        assertEquals(0, under.temperaturePercent)
        assertEquals(0, under.humidityPercent)
        assertEquals(0, under.gasPercent)
    }

    @Test
    fun buzzerWordingDistinguishesAlarmingAndIdle() {
        assertEquals("待机", MonitoringPresentation.dashboard(status(), telemetry()).buzzerText)
        assertEquals(
            "报警策略生效",
            MonitoringPresentation.dashboard(status(), telemetry(localAlarm = true)).buzzerText,
        )
    }

    // --- trends ---------------------------------------------------------------------------

    @Test
    fun anEmptyHistoryReportsNoDataRatherThanZeroStatistics() {
        val view = MonitoringPresentation.trends(emptyList())

        assertEquals(0, view.sampleCount)
        assertFalse(view.hasData)
        assertTrue(view.series.isEmpty())
        assertEquals("--", view.temperature.minimum)
        assertEquals("--", view.temperature.average)
        assertEquals("--", view.temperature.maximum)
        assertEquals("--", view.gas.average)
        assertEquals(0, view.gasSampleCount)
    }

    @Test
    fun aSingleSampleIsItsOwnMinimumAverageAndMaximum() {
        val view = MonitoringPresentation.trends(listOf(point(temperatureC = 25.0, humidityRh = 60.0, gasPpm = 12.0)))

        assertEquals(1, view.sampleCount)
        assertEquals(MetricSummary("25", "25", "25", "10:00:00"), view.temperature)
        assertEquals(MetricSummary("60", "60", "60", "10:00:00"), view.humidity)
        assertEquals(MetricSummary("12", "12", "12", "10:00:00"), view.gas)
        assertEquals(1, view.gasSampleCount)
    }

    @Test
    fun multipleSamplesSummariseMinAverageAndMax() {
        val view = MonitoringPresentation.trends(
            listOf(
                point(temperatureC = 20.0, humidityRh = 40.0, gasPpm = 10.0),
                point(temperatureC = 30.0, humidityRh = 60.0, gasPpm = 20.0),
                point(temperatureC = 40.0, humidityRh = 80.0, gasPpm = 30.0),
            ),
        )

        assertEquals(3, view.sampleCount)
        assertEquals("20", view.temperature.minimum)
        assertEquals("30", view.temperature.average)
        assertEquals("40", view.temperature.maximum)
        assertEquals("60", view.humidity.average)
        assertEquals("20", view.gas.average)
    }

    @Test
    fun samplesWithoutGasAreExcludedFromGasStatisticsButStillCounted() {
        val missing = point(gasPpm = null)
        val view = MonitoringPresentation.trends(
            listOf(missing, point(gasPpm = 30.0, temperatureC = 10.0)),
        )

        assertEquals(2, view.sampleCount, "every sample counts toward the window")
        assertEquals(1, view.gasSampleCount)
        assertEquals("30", view.gas.average, "an absent reading must not drag the average toward zero")
    }

    @Test
    fun historyRowsKeepAscendingBackendOrderAndCarryADisplayableKey() {
        val first = point(receivedAt = "2026-09-21T10:00:05Z", bootId = "b1", sequence = 7)
        val second = point(receivedAt = "2026-09-21T10:01:05Z", bootId = "b1", sequence = 8)

        val view = MonitoringPresentation.trends(listOf(first, second))

        assertEquals(listOf("2026-09-21T10:00:05Z", "2026-09-21T10:01:05Z"), view.series.map { it.receivedAt })
        assertEquals(listOf("10:00:05", "10:01:05"), view.series.map { it.timeText })
        assertEquals(listOf("b1-7", "b1-8"), view.series.map { it.key })
    }

    @Test
    fun historyRowsWithoutADeviceSequenceStillGetDistinctKeys() {
        val view = MonitoringPresentation.trends(listOf(point(), point(), point()))

        assertEquals(listOf("idx-0", "idx-1", "idx-2"), view.series.map { it.key })
    }

    @Test
    fun theRowClockIsTheTimePartWhateverTheOffsetSuffix() {
        val view = MonitoringPresentation.trends(
            listOf(
                point(receivedAt = "2026-09-21T10:00:05Z"),
                point(receivedAt = "2026-09-21T10:00:06+08:00"),
                point(receivedAt = "2026-09-21T10:00:07.250Z"),
            ),
        )

        assertEquals(listOf("10:00:05", "10:00:06", "10:00:07"), view.series.map { it.timeText })
    }

    @Test
    fun anUnparseableTimestampStaysVisibleInsteadOfBecomingAWrongClock() {
        val view = MonitoringPresentation.trends(listOf(point(receivedAt = "not-a-timestamp")))

        assertEquals("not-a-timestamp", view.series.single().timeText)
    }

    @Test
    fun anUnparseableTimestampYieldsANullEpochRatherThanAForgedZero() {
        // 0 would be a real instant (the Unix epoch). An unparseable timestamp
        // has no instant at all, so the model must say so explicitly.
        val view = MonitoringPresentation.trends(listOf(point(receivedAt = "not-a-timestamp")))
        assertEquals(null, view.series.single().timestampEpochMs)

        val good = MonitoringPresentation.trends(listOf(point(receivedAt = "2026-09-21T10:00:00Z")))
        assertEquals(
            Rfc3339.parseEpochMillis("2026-09-21T10:00:00Z"),
            good.series.single().timestampEpochMs,
        )
    }

    @Test
    fun anUnreliableAxisEndpointReadsDoubleDashInsteadOfInventingATime() {
        // Mixed reliability: the end keeps its clock, the start degrades to `--`.
        val mixed = MonitoringPresentation.trends(
            listOf(
                point(receivedAt = "not-a-timestamp"),
                point(receivedAt = "2026-09-21T11:00:00Z"),
            ),
        )
        assertEquals("--", mixed.curveAxisStart)
        assertEquals("11:00:00", mixed.curveAxisEnd)

        // No reliable endpoint at all: both ends degrade to `--`.
        val allBad = MonitoringPresentation.trends(
            listOf(
                point(receivedAt = "bad-1"),
                point(receivedAt = "bad-2"),
            ),
        )
        assertEquals("--", allBad.curveAxisStart)
        assertEquals("--", allBad.curveAxisEnd)
    }

    @Test
    fun historyRowRendersGasAsPlaceholderOnlyWhenItIsAbsent() {
        val view = MonitoringPresentation.trends(listOf(point(gasPpm = null), point(gasPpm = 0.0)))

        assertEquals("--", view.series[0].gasText)
        assertEquals("0", view.series[1].gasText)
    }

    // --- alerts ---------------------------------------------------------------------------

    @Test
    fun alertStatesMapToLabelsAndTones() {
        val cases = listOf(
            AlertState.fire_warning to ("火情预警" to Tone.DANGER),
            AlertState.suspect to ("疑似异常" to Tone.WARNING),
            AlertState.recovered to ("已恢复" to Tone.INFO),
            AlertState.normal to ("正常" to Tone.MINT),
        )
        for ((state, expected) in cases) {
            val item = MonitoringPresentation.alert(alert(state = state))
            assertEquals(expected.first, item.stateText, "label for $state")
            assertEquals(expected.second, item.tone, "tone for $state")
            assertEquals(state.name, item.state, "raw state for $state")
        }
    }

    @Test
    fun alertEvidenceIsRenderedFromTheStoredEventNotRecomputed() {
        val item = MonitoringPresentation.alert(
            alert(
                evidence = AlertEvidence(
                    gasAdcRise = 240,
                    temperatureRateCPerMinute = 2.5,
                    sampleCount = 12,
                    gasAdcRiseThreshold = 200,
                    temperatureRateThresholdCPerMinute = 1.5,
                    windowSeconds = 60,
                ),
            ),
        )

        assertEquals("240", item.gasAdcRiseText)
        assertEquals("200", item.gasAdcRiseThresholdText)
        assertEquals("2.5 °C/min", item.temperatureRateText)
        assertEquals("1.5 °C/min", item.temperatureRateThresholdText)
        assertEquals("12", item.sampleCountText)
        assertEquals("60", item.windowSecondsText)
    }

    @Test
    fun optionalAlertEvidenceRendersAsAPlaceholder() {
        val item = MonitoringPresentation.alert(
            alert(evidence = AlertEvidence(gasAdcRise = 10, temperatureRateCPerMinute = 1.0, sampleCount = 3)),
        )

        assertEquals("--", item.gasAdcRiseThresholdText)
        assertEquals("--", item.temperatureRateThresholdText)
        assertEquals("--", item.windowSecondsText)
    }

    @Test
    fun anOpenAlertIsActiveAndAnEndedOneIsNot() {
        assertTrue(MonitoringPresentation.alert(alert(endedAt = null)).active)
        assertEquals("--", MonitoringPresentation.alert(alert(endedAt = null)).endedAt)

        val closed = MonitoringPresentation.alert(alert(endedAt = "2026-09-21T11:00:00Z"))
        assertFalse(closed.active)
        assertEquals("2026-09-21T11:00:00Z", closed.endedAt)
    }

    @Test
    fun anEmptyAlertListReportsZeroRatherThanBeingIndistinguishableFromLoading() {
        val view = MonitoringPresentation.alerts(emptyList())

        assertEquals(0, view.count)
        assertTrue(view.items.isEmpty())
    }

    @Test
    fun alertCountMatchesTheRenderedItems() {
        val view = MonitoringPresentation.alerts(listOf(alert(), alert(), alert()))

        assertEquals(3, view.count)
        assertEquals(3, view.items.size)
    }

    // --- settings -------------------------------------------------------------------------

    @Test
    fun everyConfirmationStateHasWordingAndATone() {
        val cases = listOf(
            ConfirmationState.confirmed to ("设备已确认" to Tone.MINT),
            ConfirmationState.pending to ("等待设备确认" to Tone.WARNING),
            ConfirmationState.rejected to ("设备已拒绝" to Tone.DANGER),
            ConfirmationState.timed_out to ("确认超时，请重试" to Tone.DANGER),
        )
        for ((state, expected) in cases) {
            val view = MonitoringPresentation.settings(thresholds(state))
            assertEquals(expected.first, view.confirmationText, "text for $state")
            assertEquals(expected.second, view.confirmationTone, "tone for $state")
            assertEquals(state.name, view.confirmationState)
        }
    }

    @Test
    fun desiredThresholdsAreNeverPresentedAsDeviceConfirmed() {
        val pending = MonitoringPresentation.settings(
            thresholds(ConfirmationState.pending, desired = 4, confirmed = 3),
        )
        assertFalse(pending.confirmed)
        assertTrue(pending.awaitingDevice)

        val staleConfirmation = MonitoringPresentation.settings(
            thresholds(ConfirmationState.confirmed, desired = 5, confirmed = 3),
        )
        assertFalse(staleConfirmation.confirmed, "confirmedVersion below desiredVersion is not a confirmation")
        assertTrue(staleConfirmation.awaitingDevice)

        val noVersion = MonitoringPresentation.settings(
            thresholds(ConfirmationState.confirmed, desired = 5, confirmed = null),
        )
        assertFalse(noVersion.confirmed, "a confirmation state without a version proves nothing")
    }

    @Test
    fun aConfirmedVersionAtOrAboveTheDesiredVersionIsConfirmed() {
        val view = MonitoringPresentation.settings(
            thresholds(ConfirmationState.confirmed, desired = 5, confirmed = 5),
        )

        assertTrue(view.confirmed)
        assertFalse(view.awaitingDevice)
    }

    @Test
    fun settingsCarryTheEditableValuesAndFallBackForAMissingTimestamp() {
        val view = MonitoringPresentation.settings(thresholds(ConfirmationState.pending).copy(updatedAt = null))

        assertEquals(30.0, view.temperatureHighC)
        assertEquals(80.0, view.humidityHighRh)
        assertEquals(20.0, view.gasHighPpm)
        assertEquals("--", view.updatedAt)
    }

    // --- command lifecycle ----------------------------------------------------------------

    @Test
    fun anEnqueuedCommandIsNeverReportedAsDeviceConfirmed() {
        // The contract only ever emits `pending` here, and 202 means "accepted".
        val view = MonitoringPresentation.commandAccepted(
            CommandAccepted(requestId = "cmd-1", status = "pending", desiredVersion = 7),
        )

        assertEquals("等待设备确认", view.stateText)
        assertEquals(Tone.WARNING, view.tone)
        assertFalse(view.settled)
        assertFalse(view.confirmed)
        assertFalse(view.failed)
        assertEquals("cmd-1", view.requestId)
        assertEquals("7", view.versionText)
    }

    @Test
    fun everyCommandStateHasWordingSettlementAndOutcome() {
        val cases = listOf(
            // state, text, settled, confirmed, failed, tone
            CommandState.accepted to listOf("命令已接受", false, false, false, Tone.WARNING),
            CommandState.published to listOf("已下发，等待设备确认", false, false, false, Tone.WARNING),
            CommandState.applied to listOf("设备已确认", true, true, false, Tone.MINT),
            CommandState.rejected to listOf("设备已拒绝", true, false, true, Tone.DANGER),
            CommandState.expired to listOf("命令已过期", true, false, true, Tone.DANGER),
            CommandState.duplicate to listOf("重复命令，已忽略", true, false, false, Tone.INFO),
            CommandState.failed to listOf("设备执行失败", true, false, true, Tone.DANGER),
            CommandState.timed_out to listOf("设备确认超时", true, false, true, Tone.DANGER),
            CommandState.publish_failed to listOf("下发失败", true, false, true, Tone.DANGER),
        )
        for ((state, expected) in cases) {
            val view = MonitoringPresentation.commandStatus(commandStatus(state))
            assertEquals(expected[0], view.stateText, "text for $state")
            assertEquals(expected[1], view.settled, "settled for $state")
            assertEquals(expected[2], view.confirmed, "confirmed for $state")
            assertEquals(expected[3], view.failed, "failed for $state")
            assertEquals(expected[4], view.tone, "tone for $state")
        }
    }

    @Test
    fun aSettledCommandReportsItsDeviceVersionAndError() {
        val view = MonitoringPresentation.commandStatus(
            commandStatus(CommandState.rejected, confirmedVersion = 3, errorCode = "invalid_threshold"),
        )

        assertEquals("3", view.versionText)
        assertEquals("invalid_threshold", view.errorText)
    }

    @Test
    fun anUnsettledCommandHasNoVersionOrErrorToShow() {
        val view = MonitoringPresentation.commandStatus(commandStatus(CommandState.published))

        assertEquals("--", view.versionText)
        assertEquals("--", view.errorText)
    }

    // --- threshold validation -------------------------------------------------------------

    @Test
    fun thresholdValidationAcceptsTheContractBoundaries() {
        MonitoringPresentation.validate(ThresholdUpdate(0.0, 0.0, 1.0))
        MonitoringPresentation.validate(ThresholdUpdate(80.0, 100.0, 999.0))
        MonitoringPresentation.validate(ThresholdUpdate(30.0, 80.0, 20.0))
    }

    @Test
    fun thresholdValidationRejectsValuesOutsideEveryRange() {
        val rejected = listOf(
            ThresholdUpdate(-0.1, 50.0, 20.0),
            ThresholdUpdate(80.1, 50.0, 20.0),
            ThresholdUpdate(30.0, -0.1, 20.0),
            ThresholdUpdate(30.0, 100.1, 20.0),
            ThresholdUpdate(30.0, 50.0, 0.9),
            ThresholdUpdate(30.0, 50.0, 999.1),
        )
        for (update in rejected) {
            assertFailsWith<IllegalArgumentException>(update.toString()) {
                MonitoringPresentation.validate(update)
            }
        }
    }

    @Test
    fun validationMessagesNameTheSupportedRange() {
        val error = assertFailsWith<IllegalArgumentException> {
            MonitoringPresentation.validate(ThresholdUpdate(200.0, 50.0, 20.0))
        }
        assertTrue(error.message!!.contains("0-80"), "actual: ${error.message}")
    }

    @Test
    fun theSliderRangesAndTheValidatedRangesAreTheSameConstants() {
        // Guards against a host offering a value the shared validator rejects.
        MonitoringPresentation.validate(
            ThresholdUpdate(
                temperatureHighC = ThresholdLimits.TEMPERATURE_MAX_C,
                humidityHighRh = ThresholdLimits.HUMIDITY_MAX_RH,
                gasHighPpm = ThresholdLimits.GAS_MAX_PPM,
            ),
        )
        MonitoringPresentation.validate(
            ThresholdUpdate(
                temperatureHighC = ThresholdLimits.TEMPERATURE_MIN_C,
                humidityHighRh = ThresholdLimits.HUMIDITY_MIN_RH,
                gasHighPpm = ThresholdLimits.GAS_MIN_PPM,
            ),
        )
    }

    // --- formatting edge cases -------------------------------------------------------------

    @Test
    fun readingsAreWholeNumbersBecauseTheSensorResolvesWholeUnits() {
        // The DHT11 resolves one degree and one percent, and the gas estimate one
        // ppm. A fractional reading would be precision the device cannot produce,
        // so a value that arrives with a fraction is rounded, not printed as is.
        val view = MonitoringPresentation.dashboard(
            status(),
            telemetry(temperatureC = 25.5, humidityRh = 60.0, gasPpm = 12.34),
        )

        assertEquals("26", view.temperatureText)
        assertEquals("60", view.humidityText)
        assertEquals("12", view.gasText)
    }

    @Test
    fun aNonFiniteReadingIsNotRenderedAsANumber() {
        val view = MonitoringPresentation.dashboard(
            status(),
            telemetry(temperatureC = Double.NaN, humidityRh = Double.POSITIVE_INFINITY),
        )

        assertEquals("--", view.temperatureText)
        assertEquals("--", view.humidityText)
        assertEquals(0, view.temperaturePercent)
        assertEquals(0, view.humidityPercent)
    }

    // --- fixtures ---------------------------------------------------------------------------

    // --- trends: window selector, peak time, curve readiness -------------------------

    @Test
    fun theWindowSelectorOffersAllThreeBaselineWindowsAndMarksTheActiveOne() {
        val view = MonitoringPresentation.trends(listOf(point()), TrendWindow.LAST_SIX_HOURS)

        assertEquals("LAST_SIX_HOURS", view.windowKey)
        assertEquals("近6小时", view.windowLabel)
        assertEquals(
            listOf("LAST_HOUR" to "近1小时", "LAST_SIX_HOURS" to "近6小时", "LAST_DAY" to "近24小时"),
            view.windowOptions.map { it.key to it.label },
        )
    }

    @Test
    fun theCurveBlockIsRealTrendCurveCarryingIndependentScaleCopy() {
        val view = MonitoringPresentation.trends(listOf(point()))

        assertTrue(view.curveReady)
        assertEquals("各指标按独立量程展示", view.curveStatusText)
        assertEquals("三条曲线按各自量程展示", view.curveMaskTitle)
        assertEquals("用于观察变化趋势，不用于直接比较曲线高度", view.curveMaskSub)
        assertEquals("10:00:00", view.curveAxisStart)
        assertEquals("10:00:00", view.curveAxisEnd)
        assertEquals("三条曲线按各自量程展示，用于观察变化趋势，不用于直接比较曲线高度；数据受最近一页最多200条限制", view.footerHint)
        assertEquals(
            listOf("温度" to Tone.DANGER, "湿度" to Tone.INFO, "气体" to Tone.MINT),
            view.curveLegend.map { it.label to it.tone },
        )

        val emptyView = MonitoringPresentation.trends(emptyList())
        assertFalse(emptyView.curveReady)
        assertEquals("暂无数据", emptyView.curveStatusText)
        assertEquals("--", emptyView.curveAxisStart)
        assertEquals("--", emptyView.curveAxisEnd)
    }

    @Test
    fun everyWindowKeepsTheSameSelectorAndCurveCopy() {
        // The selector and the curve frame are chrome, so a window switch must not
        // change them; only the key and the samples may differ.
        val perWindow = TrendWindow.entries.map { MonitoringPresentation.trends(listOf(point()), it) }

        assertEquals(1, perWindow.map { it.windowOptions }.distinct().size)
        assertEquals(1, perWindow.map { it.curveMaskTitle }.distinct().size)
        assertEquals(3, perWindow.map { it.windowKey }.distinct().size)
    }

    @Test
    fun thePeakTimeNamesWhenTheHighestReadingWasTaken() {
        val view = MonitoringPresentation.trends(
            listOf(
                point(receivedAt = "2026-09-21T10:00:00Z", temperatureC = 25.0),
                point(receivedAt = "2026-09-21T11:00:00Z", temperatureC = 30.0),
                point(receivedAt = "2026-09-21T12:00:00Z", temperatureC = 27.0),
            ),
        )

        assertEquals("30", view.temperature.maximum)
        assertEquals("11:00:00", view.temperature.peakAt)
        // Each metric reports its own peak, not the sample that peaked overall.
        assertEquals("10:00:00", view.humidity.peakAt)
    }

    @Test
    fun aWindowWithNoSamplesReportsEmptyCellsRatherThanZeroes() {
        val view = MonitoringPresentation.trends(emptyList())

        assertEquals("--", view.temperature.peakAt)
        assertEquals("--", view.gas.peakAt)
        assertFalse(view.hasData)
    }

    @Test
    fun aWindowWithNoCalibratedGasReadingLeavesTheGasPeakEmpty() {
        val view = MonitoringPresentation.trends(listOf(point(gasPpm = null), point(gasPpm = null)))

        assertEquals(0, view.gasSampleCount)
        assertEquals("--", view.gas.peakAt)
        assertEquals("--", view.gas.average)
    }

    @Test
    fun statisticsAreWholeNumbersEvenWhenTheAverageIsNot() {
        // 20 and 21 average to 20.5, which must not reach the screen as a decimal.
        val view = MonitoringPresentation.trends(
            listOf(
                point(receivedAt = "2026-09-21T10:00:00Z", temperatureC = 20.0),
                point(receivedAt = "2026-09-21T10:00:01Z", temperatureC = 21.0),
            ),
        )

        assertEquals("20", view.temperature.minimum)
        assertEquals("21", view.temperature.average)
        assertEquals("21", view.temperature.maximum)
    }

    @Test
    fun aNonFiniteSampleIsLeftOutOfTheStatisticsInsteadOfBreakingThem() {
        val view = MonitoringPresentation.trends(
            listOf(
                point(receivedAt = "2026-09-21T10:00:00Z", temperatureC = 22.0),
                point(receivedAt = "2026-09-21T10:00:01Z", temperatureC = Double.NaN),
            ),
        )

        // The summary describes the sample that exists; the count still reports
        // both rows, because the count is what was fetched.
        assertEquals("22", view.temperature.maximum)
        assertEquals("10:00:00", view.temperature.peakAt)
        assertEquals(2, view.sampleCount)
    }

    // --- alerts: filter bar -----------------------------------------------------------

    @Test
    fun theFilterBarOffersFourStablePillsInTheBaselinesOrder() {
        assertEquals(
            listOf("all", "fire_warning", "suspect", "recovered"),
            MonitoringPresentation.alertFilterOptions().map { it.key },
        )
        assertEquals(
            listOf("全部", "火情", "疑似", "已恢复"),
            MonitoringPresentation.alertFilterOptions().map { it.label },
        )
    }

    @Test
    fun aFilterKeepsOnlyItsOwnStateAndReportsBothTotals() {
        val events = listOf(
            alert(state = AlertState.fire_warning),
            alert(state = AlertState.suspect),
            alert(state = AlertState.recovered),
        )

        val all = MonitoringPresentation.alerts(events, AlertFilter.ALL)
        assertEquals(3, all.count)
        assertEquals(3, all.visibleCount)
        assertEquals("all", all.filterKey)

        val fire = MonitoringPresentation.alerts(events, AlertFilter.FIRE_WARNING)
        assertEquals(3, fire.count)
        assertEquals(1, fire.visibleCount)
        assertEquals("fire_warning", fire.filterKey)
        assertEquals(listOf("火情预警"), fire.items.map { it.stateText })
    }

    @Test
    fun aFilterWithNoMatchesStillNamesItselfSoThePillStaysSelected() {
        val view = MonitoringPresentation.alerts(listOf(alert(state = AlertState.recovered)), AlertFilter.FIRE_WARNING)

        assertEquals(1, view.count)
        assertEquals(0, view.visibleCount)
        assertEquals("fire_warning", view.filterKey)
        assertTrue(view.items.isEmpty())
    }

    @Test
    fun theSelectorPayloadCarriesBothListsForTheMiniApp() {
        val selectors = MonitoringPresentation.selectors()

        assertEquals(MonitoringPresentation.trendWindowOptions(), selectors.windows)
        assertEquals(MonitoringPresentation.alertFilterOptions(), selectors.filters)
    }

    // --- settings and dashboard: shared copy ------------------------------------------

    @Test
    fun theSettingsViewCarriesTheHumidityLimitEvenThoughItIsNotEditable() {
        // The contract requires all three fields on every threshold update, so the
        // value has to survive to the host even though the baseline exposes only
        // two controls.
        val view = MonitoringPresentation.settings(thresholds(ConfirmationState.confirmed))

        assertEquals(80.0, view.humidityHighRh)
        assertEquals("下发后需设备确认，确认前仍按旧规则报警", view.saveHint)
    }

    private fun status(
        deviceId: String = "MCU001",
        connectivity: Connectivity = Connectivity.online,
        alarmState: AlertState = AlertState.normal,
        localAlarm: Boolean = false,
    ) = DeviceStatus(
        deviceId = deviceId,
        connectivity = connectivity,
        alarmState = alarmState,
        lastSeenAt = "2026-09-21T09:00:00Z",
        localAlarm = localAlarm,
    )

    private fun telemetry(
        temperatureC: Double = 25.0,
        humidityRh: Double = 50.0,
        gasPpm: Double? = 100.0,
        localAlarm: Boolean = false,
    ) = point(
        temperatureC = temperatureC,
        humidityRh = humidityRh,
        gasPpm = gasPpm,
        localAlarm = localAlarm,
    )

    private fun point(
        deviceId: String = "MCU001",
        receivedAt: String = "2026-09-21T10:00:00Z",
        temperatureC: Double = 25.0,
        humidityRh: Double = 50.0,
        gasPpm: Double? = 100.0,
        localAlarm: Boolean = false,
        bootId: String? = null,
        sequence: Long? = null,
    ) = TelemetryPoint(
        deviceId = deviceId,
        receivedAt = receivedAt,
        temperatureC = temperatureC,
        humidityRh = humidityRh,
        gasAdcRaw = 500,
        gasAdcFiltered = 480,
        localAlarm = localAlarm,
        gasPpm = gasPpm,
        bootId = bootId,
        sequence = sequence,
    )

    private fun alert(
        state: AlertState = AlertState.fire_warning,
        endedAt: String? = null,
        evidence: AlertEvidence = AlertEvidence(gasAdcRise = 240, temperatureRateCPerMinute = 2.5, sampleCount = 12),
    ) = AlertEvent(
        id = "alert-1",
        deviceId = "MCU001",
        state = state,
        startedAt = "2026-09-21T10:00:00Z",
        endedAt = endedAt,
        evidence = evidence,
    )

    private fun thresholds(
        state: ConfirmationState,
        desired: Int = 3,
        confirmed: Int? = 3,
    ) = Thresholds(
        desiredVersion = desired,
        temperatureHighC = 30.0,
        humidityHighRh = 80.0,
        gasHighPpm = 20.0,
        confirmedVersion = confirmed,
        updatedAt = "2026-09-21T10:00:00Z",
        confirmationState = state,
    )

    private fun commandStatus(
        state: CommandState,
        confirmedVersion: Int? = null,
        errorCode: String? = null,
    ) = CommandStatus(
        requestId = "cmd-1",
        deviceId = "MCU001",
        type = "set_thresholds",
        state = state,
        acceptedAt = "2026-09-21T10:00:00Z",
        confirmedVersion = confirmedVersion,
        errorCode = errorCode,
    )
}
