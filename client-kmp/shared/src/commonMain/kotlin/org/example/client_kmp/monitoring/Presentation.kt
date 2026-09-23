package org.example.client_kmp.monitoring

import kotlinx.serialization.Serializable
import kotlin.math.round
import kotlin.math.roundToInt

/**
 * Copy shared by both hosts.
 *
 * These strings are the frozen baseline's wording. They live here rather than in
 * each host because a promise about what a control does, or a note about how the
 * curve is scaled, must not be able to differ between Android and the MiniApp
 * for the same backend state.
 */
private const val MUTE_HINT = "静音不影响环境检测与告警上报"
private const val SAVE_HINT = "下发后需设备确认，确认前仍按旧规则报警"
private const val CURVE_STATUS_TEXT = "各指标按独立量程展示"
private const val CURVE_MASK_TITLE = "三条曲线按各自量程展示"
private const val CURVE_MASK_SUB = "用于观察变化趋势，不用于直接比较曲线高度"
private const val CURVE_AXIS_START = "区间起点"
private const val CURVE_AXIS_END = "此刻"
private const val TRENDS_FOOTER_HINT = "三条曲线按各自量程展示，用于观察变化趋势，不用于直接比较曲线高度；数据受最近一页最多200条限制"

/**
 * Threshold ranges frozen by `docs/api/openapi.yaml`.
 *
 * They are used both for validating an outgoing update and for scaling the
 * dashboard meters, so a meter and its alarm boundary can never disagree.
 */
object ThresholdLimits {
    const val TEMPERATURE_MIN_C = 0.0
    const val TEMPERATURE_MAX_C = 80.0
    const val HUMIDITY_MIN_RH = 0.0
    const val HUMIDITY_MAX_RH = 100.0
    const val GAS_MIN_PPM = 1.0
    const val GAS_MAX_PPM = 999.0
}

/** Shared presentation tone tokens; hosts map them to their own colour system. */
object Tone {
    const val MINT = "mint"
    const val WARNING = "warning"
    const val DANGER = "danger"
    const val INFO = "info"
}

/**
 * History windows offered by the trends page.
 *
 * The labels are the frozen baseline's copy (`client-wx-native/pages/trends`):
 * both hosts render them verbatim, so keeping them here is what stops Android
 * and the MiniApp from drifting into different window names for the same span.
 * [hours] is what turns a selection into the contract's absolute `from` bound.
 */
enum class TrendWindow(val label: String, val hours: Int) {
    LAST_HOUR("近1小时", 1),
    LAST_SIX_HOURS("近6小时", 6),
    LAST_DAY("近24小时", 24),
}

/**
 * Filters offered by the alerts page.
 *
 * The baseline's third pill is `已确认`, which this client cannot offer: the
 * backend's alert vocabulary has no acknowledged state (the contract models
 * `normal`, `suspect`, `fire_warning` and `recovered` only), so offering the pill
 * would filter every list down to nothing. `疑似` takes its place, which keeps
 * four pills in the baseline's order and every pill able to return rows.
 */
enum class AlertFilter(val key: String, val label: String) {
    ALL("all", "全部"),
    FIRE_WARNING("fire_warning", "火情"),
    SUSPECT("suspect", "疑似"),
    RECOVERED("recovered", "已恢复"),
}

/**
 * One entry of a segmented selector.
 *
 * It carries no selected state on purpose: which option is active is host state,
 * and a view that mirrored it would have two sources for one fact. Hosts compare
 * [key] against the key on the view they are rendering.
 */
@Serializable
data class SelectOption(val key: String, val label: String)

/**
 * Every selector option the UI can offer, without needing a fetched view.
 *
 * The MiniApp has to draw the trends window selector before its first history
 * response arrives, and it cannot reach a Kotlin enum from WXML. Exposing the
 * option lists here keeps the labels in the shared layer, so the selector copy
 * exists once instead of being restated in the page script.
 */
@Serializable
data class SelectorOptions(
    val windows: List<SelectOption>,
    val filters: List<SelectOption>,
)

/**
 * One legend entry above the trends curve.
 *
 * The tone is shared so both hosts pick the same colour for the same metric; the
 * baseline maps temperature to its danger colour, humidity to its info colour and
 * gas to the mint accent, and the same mapping is used by the dashboard meters.
 */
@Serializable
data class CurveLegend(val label: String, val tone: String, val rangeText: String = "")

/**
 * Everything the dashboard needs, already formatted and scaled.
 *
 * `hasData` is false when the backend has no valid telemetry yet (the contract
 * answers 404 for "latest" rather than a zero-filled sample). Metrics are then
 * `--` while device and alarm state stay real, so a newly provisioned device
 * shows an empty state instead of an error or a fake reading.
 */
@Serializable
data class DashboardView(
    val deviceId: String,
    val hasData: Boolean,
    val online: Boolean,
    val riskLevel: String,
    val riskTone: String,
    val riskText: String,
    val riskDetail: String,
    val connectivityText: String,
    val temperatureText: String,
    val humidityText: String,
    val gasText: String,
    val gasAvailable: Boolean,
    val temperaturePercent: Int,
    val humidityPercent: Int,
    val gasPercent: Int,
    val localAlarm: Boolean,
    val localAlarmText: String,
    val buzzerText: String,
    val buzzerMuted: Boolean,
    val updatedAt: String,
    /**
     * Copy for the mute control, worded the way the frozen baseline words it.
     * Both hosts render it, so the promise a user reads before tapping the button
     * cannot differ between them.
     */
    val muteHint: String,
)

/**
 * Minimum / average / maximum of one metric, pre-formatted for display, plus the
 * time the maximum was recorded.
 *
 * `peakAt` answers "when was it worst", which a min/avg/max triple alone cannot:
 * a peak at 03:00 and a peak at 09:00 describe very different rooms. It is an
 * empty cell when there are no samples rather than a fabricated timestamp.
 */
@Serializable
data class MetricSummary(
    val minimum: String,
    val average: String,
    val maximum: String,
    val peakAt: String,
)

/**
 * One trend row, pre-formatted for list rendering.
 *
 * `key` is always present so a host list can key on a real field: the device
 * ordering tuple when the firmware reports it, and the sample position
 * otherwise. Gas is `--` when the estimate is unavailable, which is different
 * from a measured zero.
 *
 * [timestampEpochMs] is null when `receivedAt` is not a parseable RFC 3339
 * instant. Chart geometry reads that null as "this sample has no reliable event
 * time" and degrades the whole series to uniform spacing rather than inventing
 * an epoch-0 position. `timeText` still shows the raw string so an unreadable
 * stamp is visible in the list instead of silently rewritten.
 */
@Serializable
data class TrendPointView(
    val key: String,
    val receivedAt: String,
    val timeText: String,
    val temperatureText: String,
    val humidityText: String,
    val gasText: String,
    val localAlarm: Boolean,
    val timestampEpochMs: Long? = null,
    val temperatureC: Double = 0.0,
    val humidityRh: Double = 0.0,
    val gasPpm: Double? = null,
)

/**
 * Historical statistics plus the ordered rows they were computed from.
 *
 * `series` is what the chart draws: each row carries the raw metric values and
 * a nullable event time so [TrendChartGeometry] can map X coordinates. Hosts
 * must not render it as a list in the curve's place — a table of samples is not
 * a trend curve. [curveReady] is the single flag that says whether there is
 * currently drawable history.
 */
@Serializable
data class TrendsView(
    val sampleCount: Int,
    val hasData: Boolean,
    val temperature: MetricSummary,
    val humidity: MetricSummary,
    val gas: MetricSummary,
    val gasSampleCount: Int,
    val windowKey: String,
    val windowLabel: String,
    val windowOptions: List<SelectOption>,
    /** Section tip on the curve block. */
    val curveStatusText: String,
    val curveMaskTitle: String,
    val curveMaskSub: String,
    val curveAxisStart: String,
    val curveAxisEnd: String,
    /**
     * True when there is currently drawable history — that is, `series` holds at
     * least one sample with the values a chart needs. It is not a statement about
     * whether a chart library is wired up: both hosts render the shared geometry
     * on a canvas. Empty success (no samples in range) leaves this false so the
     * host can show "no data" rather than a blank frame.
     */
    val curveReady: Boolean,
    val curveLegend: List<CurveLegend>,
    val footerHint: String,
    val series: List<TrendPointView>,
)

/**
 * One alert rendered for a list.
 *
 * Text lives here rather than in each host so Android and the MiniApp cannot
 * drift into different labels for the same backend state.
 */
@Serializable
data class AlertItemView(
    val id: String,
    val state: String,
    val stateText: String,
    val tone: String,
    val startedAt: String,
    val endedAt: String,
    val active: Boolean,
    val gasAdcRiseText: String,
    val gasAdcRiseThresholdText: String,
    val temperatureRateText: String,
    val temperatureRateThresholdText: String,
    val sampleCountText: String,
    val windowSecondsText: String,
)

/**
 * Alert-list payload shared by Android and MiniApp hosts.
 *
 * `count` is how many events the backend returned and `visibleCount` how many of
 * them the active filter keeps, so a host can tell "this device has no alerts"
 * from "this filter has no alerts" without re-deriving the filter itself.
 */
@Serializable
data class AlertsView(
    val count: Int,
    val visibleCount: Int,
    /** Which filter produced [items]; hosts compare it to mark the active pill. */
    val filterKey: String,
    val filters: List<SelectOption>,
    val items: List<AlertItemView>,
)

/** Threshold settings with device-confirmation wording resolved. */
@Serializable
data class SettingsView(
    val temperatureHighC: Double,
    /**
     * The humidity limit is carried but not exposed as a control.
     *
     * The frozen baseline's settings page offers temperature and gas only, and
     * this client follows it. The value still has to travel: the contract's
     * threshold update requires all three fields, so the host sends the limit the
     * device already reports rather than inventing one. Exposing a humidity
     * slider is recorded as outstanding work rather than dropped silently.
     */
    val humidityHighRh: Double,
    val gasHighPpm: Double,
    val desiredVersion: Int,
    val confirmedVersion: Int?,
    val confirmationState: String,
    val confirmationText: String,
    val confirmationTone: String,
    val confirmed: Boolean,
    val awaitingDevice: Boolean,
    val updatedAt: String,
    /** Copy under the save button; the baseline's wording. */
    val saveHint: String,
)

/** Lifecycle wording for a control command; `confirmed` is true only after the device said so. */
@Serializable
data class CommandStatusView(
    val requestId: String,
    val state: String,
    val stateText: String,
    val tone: String,
    val settled: Boolean,
    val confirmed: Boolean,
    val failed: Boolean,
    val versionText: String,
    val errorText: String,
)

/**
 * Pure presentation rules consumed by both platform UIs.
 *
 * Every function is total and side-effect free so it can be unit tested without
 * a platform host. `validate` is the only throwing entry point and reports the
 * contract range it rejected.
 */
object MonitoringPresentation {

    /**
     * Derives the dashboard from device state plus an optional latest sample.
     *
     * `telemetry` is null when the device has no valid sample yet; alarm and
     * mute state then fall back to [DeviceStatus] so the page stays truthful.
     */
    fun dashboard(status: DeviceStatus, telemetry: TelemetryPoint?): DashboardView {
        val risk = when (status.alarmState) {
            AlertState.normal -> Risk("normal", Tone.MINT, "环境正常", "各项指标处于安全范围")
            AlertState.suspect -> Risk("suspect", Tone.WARNING, "疑似异常", "部分条件异常，系统确认中")
            AlertState.fire_warning -> Risk("fire", Tone.DANGER, "火情预警", "气体突增与温升速率同时超限")
            AlertState.recovered -> Risk("recovered", Tone.INFO, "指标已恢复", "事件归档中")
        }
        val localAlarm = telemetry?.localAlarm ?: status.localAlarm
        val muted = telemetry?.buzzerMuted ?: status.buzzerMuted
        return DashboardView(
            deviceId = status.deviceId,
            hasData = telemetry != null,
            online = status.connectivity == Connectivity.online,
            riskLevel = risk.level,
            riskTone = risk.tone,
            riskText = risk.text,
            riskDetail = risk.detail,
            connectivityText = when (status.connectivity) {
                Connectivity.online -> "在线"
                Connectivity.offline -> "离线"
                Connectivity.unknown -> "未知"
            },
            temperatureText = telemetry?.let { reading(it.temperatureC) } ?: "--",
            humidityText = telemetry?.let { reading(it.humidityRh) } ?: "--",
            gasText = telemetry?.gasPpm?.let(::reading) ?: "--",
            gasAvailable = telemetry?.gasPpm != null,
            temperaturePercent = percent(telemetry?.temperatureC, ThresholdLimits.TEMPERATURE_MAX_C),
            humidityPercent = percent(telemetry?.humidityRh, ThresholdLimits.HUMIDITY_MAX_RH),
            gasPercent = percent(telemetry?.gasPpm, ThresholdLimits.GAS_MAX_PPM),
            localAlarm = localAlarm,
            localAlarmText = if (localAlarm) "报警中" else "正常",
            buzzerText = when {
                muted -> "已静音"
                localAlarm -> "报警策略生效"
                else -> "待机"
            },
            buzzerMuted = muted,
            updatedAt = telemetry?.receivedAt ?: status.lastSeenAt ?: "--",
            muteHint = MUTE_HINT,
        )
    }

    /**
     * Summarises samples in the order the backend returned them (ascending).
     *
     * Samples without a gas reading are excluded from the gas statistics so an
     * uncalibrated device cannot drag the average toward zero.
     *
     * `window` only selects which of the three options is marked active; the
     * samples themselves were already narrowed to that window by the query, so no
     * second filter is applied here. Filtering twice would silently shrink a
     * window the server had already honoured and make the sample count disagree
     * with the range the user picked.
     */
    fun trends(points: List<TelemetryPoint>, window: TrendWindow = TrendWindow.LAST_HOUR): TrendsView {
        val gasReadings = points.mapNotNull { it.gasPpm }
        val tempSummary = summarize(points) { it.temperatureC }
        val humSummary = summarize(points) { it.humidityRh }
        val gasSummary = summarize(points) { it.gasPpm }

        val tempRange = if (points.isNotEmpty()) "${tempSummary.minimum}~${tempSummary.maximum}°C" else ""
        val humRange = if (points.isNotEmpty()) "${humSummary.minimum}~${humSummary.maximum}%" else ""
        val gasRange = if (gasReadings.isNotEmpty()) "${gasSummary.minimum}~${gasSummary.maximum}ppm" else ""

        // Axis endpoints show a clock only when that sample's stamp is real;
        // an unreliable endpoint reads `--` rather than a made-up time.
        val axisStart = if (points.isNotEmpty() && Rfc3339.parseEpochMillis(points.first().receivedAt) != null) {
            clockText(points.first().receivedAt)
        } else {
            "--"
        }
        val axisEnd = if (points.isNotEmpty() && Rfc3339.parseEpochMillis(points.last().receivedAt) != null) {
            clockText(points.last().receivedAt)
        } else {
            "--"
        }
        val statusText = if (points.isNotEmpty()) CURVE_STATUS_TEXT else "暂无数据"

        return TrendsView(
            sampleCount = points.size,
            hasData = points.isNotEmpty(),
            temperature = tempSummary,
            humidity = humSummary,
            gas = gasSummary,
            gasSampleCount = gasReadings.size,
            windowKey = window.name,
            windowLabel = window.label,
            windowOptions = trendWindowOptions(),
            curveStatusText = statusText,
            curveMaskTitle = CURVE_MASK_TITLE,
            curveMaskSub = CURVE_MASK_SUB,
            curveAxisStart = axisStart,
            curveAxisEnd = axisEnd,
            curveReady = points.isNotEmpty(),
            curveLegend = listOf(
                CurveLegend("温度", Tone.DANGER, tempRange),
                CurveLegend("湿度", Tone.INFO, humRange),
                CurveLegend("气体", Tone.MINT, gasRange),
            ),
            footerHint = TRENDS_FOOTER_HINT,
            series = points.mapIndexed { index, point -> trendPoint(point, index) },
        )
    }

    private fun trendPoint(point: TelemetryPoint, index: Int): TrendPointView = TrendPointView(
        // The device ordering tuple is preferred; the index keeps the key unique
        // when the firmware has not reported bootId/sequence yet.
        key = point.sequence?.let { "${point.bootId ?: "boot"}-$it" } ?: "idx-$index",
        receivedAt = point.receivedAt,
        timeText = clockText(point.receivedAt),
        temperatureText = reading(point.temperatureC),
        humidityText = reading(point.humidityRh),
        gasText = point.gasPpm?.let(::reading) ?: "--",
        localAlarm = point.localAlarm,
        timestampEpochMs = Rfc3339.parseEpochMillis(point.receivedAt),
        temperatureC = point.temperatureC,
        humidityRh = point.humidityRh,
        gasPpm = point.gasPpm,
    )

    /**
     * Maps persisted alert events without recomputing their trigger evidence.
     *
     * Filtering happens here rather than on the server because the backend's
     * alerts query has no state parameter: the page is fetched once and the pills
     * narrow it in place, which is also what keeps switching a pill from costing a
     * request on a phone hotspot.
     */
    fun alerts(
        events: List<AlertEvent>,
        filter: AlertFilter = AlertFilter.ALL,
    ): AlertsView {
        val shown = if (filter == AlertFilter.ALL) {
            events
        } else {
            events.filter { it.state.name == filter.key }
        }
        return AlertsView(
            count = events.size,
            visibleCount = shown.size,
            filterKey = filter.key,
            filters = alertFilterOptions(),
            items = shown.map(::alert),
        )
    }

    /** Maps one persisted event to host-ready labels and formatted evidence. */
    fun alert(event: AlertEvent): AlertItemView {
        val tone = when (event.state) {
            AlertState.fire_warning -> Tone.DANGER
            AlertState.suspect -> Tone.WARNING
            AlertState.recovered -> Tone.INFO
            AlertState.normal -> Tone.MINT
        }
        return AlertItemView(
            id = event.id,
            state = event.state.name,
            stateText = when (event.state) {
                AlertState.fire_warning -> "火情预警"
                AlertState.suspect -> "疑似异常"
                AlertState.recovered -> "已恢复"
                AlertState.normal -> "正常"
            },
            tone = tone,
            startedAt = event.startedAt,
            endedAt = event.endedAt ?: "--",
            active = event.endedAt == null,
            gasAdcRiseText = event.evidence.gasAdcRise.toString(),
            gasAdcRiseThresholdText = event.evidence.gasAdcRiseThreshold?.toString() ?: "--",
            temperatureRateText = "${decimal(event.evidence.temperatureRateCPerMinute)} °C/min",
            temperatureRateThresholdText =
                event.evidence.temperatureRateThresholdCPerMinute?.let { "${decimal(it)} °C/min" } ?: "--",
            sampleCountText = event.evidence.sampleCount.toString(),
            windowSecondsText = event.evidence.windowSeconds?.toString() ?: "--",
        )
    }

    /**
     * Resolves confirmation wording.
     *
     * A desired version above the confirmed version is never reported as
     * confirmed, matching the contract's "never present desired as confirmed".
     */
    fun settings(value: Thresholds): SettingsView {
        val confirmed = value.confirmationState == ConfirmationState.confirmed &&
            value.confirmedVersion != null &&
            value.confirmedVersion >= value.desiredVersion
        return SettingsView(
            temperatureHighC = value.temperatureHighC,
            humidityHighRh = value.humidityHighRh,
            gasHighPpm = value.gasHighPpm,
            desiredVersion = value.desiredVersion,
            confirmedVersion = value.confirmedVersion,
            confirmationState = value.confirmationState.name,
            confirmationText = when (value.confirmationState) {
                ConfirmationState.confirmed -> "设备已确认"
                ConfirmationState.pending -> "等待设备确认"
                ConfirmationState.rejected -> "设备已拒绝"
                ConfirmationState.timed_out -> "确认超时，请重试"
            },
            confirmationTone = when (value.confirmationState) {
                ConfirmationState.confirmed -> Tone.MINT
                ConfirmationState.pending -> Tone.WARNING
                ConfirmationState.rejected, ConfirmationState.timed_out -> Tone.DANGER
            },
            confirmed = confirmed,
            awaitingDevice = !confirmed,
            updatedAt = value.updatedAt ?: "--",
            saveHint = SAVE_HINT,
        )
    }

    /**
     * Describes an enqueued command.
     *
     * The request body of `CommandAccepted` is always `pending`, which means the
     * broker published the command and the device has not answered yet. Callers
     * must not render this as a device confirmation; poll [commandStatus] for
     * the terminal outcome.
     */
    fun commandAccepted(value: CommandAccepted): CommandStatusView = CommandStatusView(
        requestId = value.requestId,
        state = value.status,
        stateText = "等待设备确认",
        tone = Tone.WARNING,
        settled = false,
        confirmed = false,
        failed = false,
        versionText = value.desiredVersion?.toString() ?: "--",
        errorText = "--",
    )

    /** Maps a backend command lifecycle state to host-ready semantics. */
    fun commandStatus(value: CommandStatus): CommandStatusView {
        val settled = when (value.state) {
            CommandState.accepted, CommandState.published -> false
            else -> true
        }
        val confirmed = value.state == CommandState.applied
        val failed = settled && !confirmed && value.state != CommandState.duplicate
        return CommandStatusView(
            requestId = value.requestId,
            state = value.state.name,
            stateText = when (value.state) {
                CommandState.accepted -> "命令已接受"
                CommandState.published -> "已下发，等待设备确认"
                CommandState.applied -> "设备已确认"
                CommandState.rejected -> "设备已拒绝"
                CommandState.expired -> "命令已过期"
                CommandState.duplicate -> "重复命令，已忽略"
                CommandState.failed -> "设备执行失败"
                CommandState.timed_out -> "设备确认超时"
                CommandState.publish_failed -> "下发失败"
            },
            tone = when {
                confirmed -> Tone.MINT
                !settled -> Tone.WARNING
                value.state == CommandState.duplicate -> Tone.INFO
                else -> Tone.DANGER
            },
            settled = settled,
            confirmed = confirmed,
            failed = failed,
            versionText = value.confirmedVersion?.toString() ?: "--",
            errorText = value.errorCode ?: "--",
        )
    }

    /** The trends window options, in the order the selector shows them. */
    fun trendWindowOptions(): List<SelectOption> =
        TrendWindow.entries.map { SelectOption(key = it.name, label = it.label) }

    /** The alert filter options, in the order the filter bar shows them. */
    fun alertFilterOptions(): List<SelectOption> =
        AlertFilter.entries.map { SelectOption(key = it.key, label = it.label) }

    /**
     * Both selector lists in one payload.
     *
     * This exists for the MiniApp, which must draw a selector before any view has
     * been fetched and has no way to enumerate a Kotlin enum from WXML. Returning
     * the same lists the views carry keeps one source for every option label.
     */
    fun selectors(): SelectorOptions =
        SelectorOptions(windows = trendWindowOptions(), filters = alertFilterOptions())

    /**
     * Validates an outgoing threshold update against the contract ranges.
     *
     * @throws IllegalArgumentException when a value is outside the device-supported range.
     */
    fun validate(update: ThresholdUpdate) {
        require(update.temperatureHighC in ThresholdLimits.TEMPERATURE_MIN_C..ThresholdLimits.TEMPERATURE_MAX_C) {
            "温度阈值需在 0-80 °C"
        }
        require(update.humidityHighRh in ThresholdLimits.HUMIDITY_MIN_RH..ThresholdLimits.HUMIDITY_MAX_RH) {
            "湿度阈值需在 0-100 %RH"
        }
        require(update.gasHighPpm in ThresholdLimits.GAS_MIN_PPM..ThresholdLimits.GAS_MAX_PPM) {
            "气体阈值需在 1-999 ppm"
        }
    }

    private data class Risk(val level: String, val tone: String, val text: String, val detail: String)

    /**
     * Extracts `HH:mm:ss` from an RFC 3339 timestamp for a compact list row.
     *
     * A value that carries no time part is returned as-is rather than guessed at,
     * so an unexpected format stays visible instead of turning into a wrong time.
     */
    private fun clockText(timestamp: String): String {
        val time = timestamp.substringAfter('T', missingDelimiterValue = "")
        return if (time.isEmpty()) timestamp else time.take(8)
    }

    /**
     * Summarises one metric over the samples that actually carry it.
     *
     * A sample without a reading — an uncalibrated gas estimate, or a value that
     * arrived non-finite from a sensor fault — is skipped rather than counted as
     * zero, so a gap lowers the sample count instead of the average.
     */
    private fun summarize(
        points: List<TelemetryPoint>,
        selector: (TelemetryPoint) -> Double?,
    ): MetricSummary {
        val measured = points.mapNotNull { point ->
            selector(point)?.takeIf { it.isFinite() }?.let { value -> point to value }
        }
        if (measured.isEmpty()) return MetricSummary("--", "--", "--", "--")
        val peak = measured.maxBy { it.second }
        return MetricSummary(
            minimum = reading(measured.minOf { it.second }),
            average = reading(measured.sumOf { it.second } / measured.size),
            maximum = reading(peak.second),
            peakAt = clockText(peak.first.receivedAt),
        )
    }

    /**
     * Renders a temperature, humidity or gas reading as a whole number.
     *
     * The DHT11 resolves one degree and one percent, and the gas estimate one
     * ppm, so a fractional reading would be precision the device cannot produce.
     * This deliberately differs from the frozen baseline, which formats one
     * decimal place: those decimals are formatting over integers, and printing
     * `31.0` invites a reader to trust a digit that was never measured. The
     * average is rounded for the same reason — it is displayed beside the two
     * other figures, and a fractional average would put the digit straight back.
     *
     * Rounding is half-up via [roundToInt] rather than `kotlin.math.round`, which
     * breaks ties toward the even neighbour and would show an average of 20 from
     * 20 and 21 while the peak beside it read 21. Half-up is also the rule the
     * device applies to a sub-unit threshold (`docs/device-protocol.md` §4.3), so
     * both ends of the link round the same way.
     */
    private fun reading(value: Double): String {
        if (!value.isFinite()) return "--"
        return value.roundToInt().toString()
    }

    /** Scales a reading onto its alarm boundary, clamped to the 0-100 bar range. */
    private fun percent(value: Double?, maximum: Double): Int {
        if (value == null || !value.isFinite()) return 0
        return ((value / maximum) * 100.0).roundToInt().coerceIn(0, 100)
    }

    /** One decimal place, trailing `.0` dropped; non-finite input renders as an empty cell. */
    private fun decimal(value: Double): String {
        if (!value.isFinite()) return "--"
        val rounded = round(value * 10.0) / 10.0
        return if (rounded == rounded.toLong().toDouble()) rounded.toLong().toString() else rounded.toString()
    }
}
