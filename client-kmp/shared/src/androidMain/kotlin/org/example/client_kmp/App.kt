package org.example.client_kmp

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlin.coroutines.cancellation.CancellationException
import kotlin.math.roundToInt
import org.example.client_kmp.monitoring.AlertFilter
import org.example.client_kmp.monitoring.AlertItemView
import org.example.client_kmp.monitoring.AlertsView
import org.example.client_kmp.monitoring.ChartSeries
import org.example.client_kmp.monitoring.CurveLegend
import org.example.client_kmp.monitoring.DashboardView
import org.example.client_kmp.monitoring.MetricSummary
import org.example.client_kmp.monitoring.MonitoringClient
import org.example.client_kmp.monitoring.MonitoringPresentation
import org.example.client_kmp.monitoring.SelectOption
import org.example.client_kmp.monitoring.SettingsView
import org.example.client_kmp.monitoring.ThresholdLimits
import org.example.client_kmp.monitoring.ThresholdUpdate
import org.example.client_kmp.monitoring.Tone
import org.example.client_kmp.monitoring.TrendChartGeometry
import org.example.client_kmp.monitoring.TrendWindow
import org.example.client_kmp.monitoring.TrendsView

private val Background = Color(0xFF071310)
private val Surface = Color(0xFF10231E)
private val SurfaceLight = Color(0xFF17302A)
private val Mint = Color(0xFF3FE8C3)
private val TextPrimary = Color(0xFFECF5F2)
private val TextSecondary = Color(0xFF8FA8A2)
private val Danger = Color(0xFFFB7185)
private val Warning = Color(0xFFF5C96B)
private val Info = Color(0xFF7DD3FC)

// The one accent gradient the baseline reuses for every selected control and
// primary button, with the on-colour it pairs with.
private val ActiveStart = Color(0xFF45F0CB)
private val ActiveEnd = Color(0xFF16B98B)

/**
 * Failure copy for a history fetch that did not produce data. Kept constant and
 * distinct from the empty-success wording so a network error is never read as
 * "this window has no samples".
 */
private const val HISTORY_LOAD_ERROR = "历史数据加载失败，点击时间窗可重试"

/** Backend address reachable from the Android emulator; the host is `10.0.2.2`. */
private const val EMULATOR_BASE_URL = "http://10.0.2.2:8080"

private const val DASHBOARD_REFRESH_MS = 3_000L

private enum class MonitorTab(val title: String, val glyph: String) {
    Dashboard("监控", "◉"), Trends("趋势", "⌁"), Alerts("告警", "!"), Settings("设置", "⚙")
}

/**
 * Android host UI.
 *
 * Every value, label and colour token comes from the shared
 * [MonitoringClient]; this file only arranges them. The client is remembered so
 * recomposition never builds a second one, and each screen's polling lives in a
 * `LaunchedEffect`, so switching tabs or destroying the Activity cancels it
 * instead of leaking a coroutine.
 */
@Composable
fun App() {
    val client = remember { MonitoringClient(AndroidMonitoringPlatform(), EMULATOR_BASE_URL) }
    var tab by remember { mutableStateOf(MonitorTab.Dashboard) }
    MaterialTheme {
        Scaffold(
            containerColor = Background,
            bottomBar = {
                NavigationBar(containerColor = Color(0xFF0D1E1A)) {
                    MonitorTab.entries.forEach { item ->
                        NavigationBarItem(
                            selected = tab == item,
                            onClick = { tab = item },
                            icon = { Text(item.glyph) },
                            label = { Text(item.title) },
                            // The baseline's selected tab is a filled accent pill
                            // with the dark on-colour; the Material default would
                            // tint it instead, which reads as a different palette.
                            colors = NavigationBarItemDefaults.colors(
                                indicatorColor = ActiveEnd,
                                selectedIconColor = Background,
                                selectedTextColor = Mint,
                                unselectedIconColor = TextSecondary,
                                unselectedTextColor = TextSecondary,
                            ),
                        )
                    }
                }
            },
        ) { padding ->
            Box(Modifier.fillMaxSize().padding(padding).background(Background)) {
                when (tab) {
                    MonitorTab.Dashboard -> DashboardScreen(client)
                    MonitorTab.Trends -> TrendsScreen(client)
                    MonitorTab.Alerts -> AlertsScreen(client)
                    MonitorTab.Settings -> SettingsScreen(client)
                }
            }
        }
    }
}

@Composable
private fun Page(title: String, subtitle: String, content: @Composable ColumnScope.() -> Unit) {
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 18.dp, vertical = 20.dp),
    ) {
        Text(title, color = TextPrimary, fontSize = 28.sp, fontWeight = FontWeight.SemiBold)
        Text(subtitle, color = TextSecondary, fontSize = 13.sp, modifier = Modifier.padding(top = 4.dp, bottom = 18.dp))
        content()
        Spacer(Modifier.height(24.dp))
    }
}

@Composable
private fun GlassCard(content: @Composable ColumnScope.() -> Unit) {
    Column(
        Modifier.fillMaxWidth().padding(vertical = 7.dp)
            .background(Brush.linearGradient(listOf(SurfaceLight, Surface)), RoundedCornerShape(20.dp))
            .padding(18.dp),
        content = content,
    )
}

@Composable
private fun SectionTitle(title: String, tip: String = "") {
    Row(Modifier.fillMaxWidth().padding(top = 18.dp, bottom = 7.dp), horizontalArrangement = Arrangement.SpaceBetween) {
        Text(title, color = TextPrimary, fontWeight = FontWeight.SemiBold, fontSize = 19.sp)
        Text(tip, color = TextSecondary, fontSize = 12.sp)
    }
}

@Composable
private fun Hint(text: String, color: Color = TextSecondary) {
    Text(text, color = color, fontSize = 13.sp, modifier = Modifier.padding(vertical = 12.dp))
}

@Composable
private fun DashboardScreen(client: MonitoringClient) {
    var view by remember { mutableStateOf<DashboardView?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    // The hint carries the shared tone so a rejection, a timeout and a device
    // confirmation are visually distinct rather than three shades of one line.
    var commandHint by remember { mutableStateOf("") }
    var commandTone by remember { mutableStateOf(Tone.WARNING) }
    val scope = rememberCoroutineScope()

    // Refreshes once per interval. Failures are captured into `error`, so a
    // network blip can never terminate the loop: the next tick retries.
    LaunchedEffect(Unit) {
        while (true) {
            runCatching { client.loadDashboard() }
                .onSuccess { view = it; error = null }
                .onFailure { error = it.message }
            delay(DASHBOARD_REFRESH_MS)
        }
    }

    // Disabled while in flight, so a double tap cannot enqueue two commands.
    fun toggleMute(currentlyMuted: Boolean) {
        if (busy) return
        busy = true
        scope.launch {
            runCatching {
                val accepted = client.setMuted(!currentlyMuted)
                commandHint = accepted.stateText
                commandTone = accepted.tone
                // The enqueue response is only an acknowledgement; wait for the device.
                val settled = client.awaitCommandOutcome(accepted.requestId)
                // Null means the device had not answered inside the polling budget.
                // That is "still awaiting", so it must not read as a failure either.
                commandHint = settled?.stateText ?: "等待设备确认"
                commandTone = settled?.tone ?: Tone.WARNING
            }.onFailure { error = it.message }
            runCatching { client.loadDashboard() }.onSuccess { view = it }
            busy = false
        }
    }

    Page("机房环境总览", "智慧机房 · 实时动环监测") {
        if (view == null && error == null) Hint("数据加载中…")
        error?.let { Hint(it, Danger) }
        view?.let { data ->
            GlassCard {
                Text("系统风险状态", color = TextSecondary, fontSize = 13.sp)
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 8.dp)) {
                    Box(Modifier.size(10.dp).background(toneColor(data.riskTone), CircleShape))
                    Text(
                        data.riskText,
                        color = toneColor(data.riskTone),
                        fontSize = 25.sp,
                        fontWeight = FontWeight.SemiBold,
                        modifier = Modifier.padding(start = 10.dp),
                    )
                    Spacer(Modifier.weight(1f))
                    Pill(data.connectivityText, if (data.online) Mint else TextSecondary)
                }
                Text(data.riskDetail, color = TextSecondary, fontSize = 13.sp, modifier = Modifier.padding(top = 6.dp))
            }
            if (!data.hasData) Hint("该设备尚未上报有效遥测数据", Warning)
            SectionTitle("实时数据", "每 3 秒同步")
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Meter("T", "温度", data.temperatureText, "°C", data.temperaturePercent, Color(0xFFFFA183), Modifier.weight(1f))
                Meter("H", "湿度", data.humidityText, "%", data.humidityPercent, Color(0xFF8ED6FF), Modifier.weight(1f))
                Meter("G", "气体", data.gasText, "ppm", data.gasPercent, Mint, Modifier.weight(1f))
            }
            SectionTitle("设备状态", "本地采集终端")
            GlassCard {
                Text(data.deviceId, color = TextPrimary, fontSize = 22.sp, fontWeight = FontWeight.SemiBold)
                KeyValue("本地报警", data.localAlarmText, if (data.localAlarm) Danger else Mint)
                KeyValue("声光提示", data.buzzerText, if (data.buzzerMuted) Warning else TextPrimary)
                KeyValue("更新时间", data.updatedAt, TextPrimary)
            }
            SectionTitle("远程控制", "静音不影响检测与上报")
            GlassCard {
                Text("蜂鸣器控制", color = TextPrimary, fontSize = 18.sp, fontWeight = FontWeight.SemiBold)
                Text(data.muteHint, color = TextSecondary, fontSize = 12.sp)
                if (commandHint.isNotEmpty()) Hint(commandHint, toneColor(commandTone))
                Button(
                    enabled = !busy,
                    onClick = { toggleMute(data.buzzerMuted) },
                    colors = ButtonDefaults.buttonColors(containerColor = Mint, contentColor = Background),
                    modifier = Modifier.fillMaxWidth().padding(top = 14.dp),
                ) { Text(if (busy) "下发中…" else if (data.buzzerMuted) "恢复鸣叫" else "远程静音") }
            }
        }
    }
}

@Composable
private fun Meter(letter: String, label: String, value: String, unit: String, percent: Int, color: Color, modifier: Modifier) {
    Column(modifier.background(Surface, RoundedCornerShape(16.dp)).padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(letter, color = color, fontWeight = FontWeight.Bold)
            Text(label, color = TextSecondary, fontSize = 12.sp, modifier = Modifier.padding(start = 6.dp))
        }
        Text(value, color = TextPrimary, fontSize = 25.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.padding(top = 8.dp))
        Text(unit, color = TextSecondary, fontSize = 10.sp)
        Box(Modifier.fillMaxWidth().height(4.dp).background(Color.White.copy(alpha = .08f), CircleShape)) {
            Box(Modifier.fillMaxWidth(percent / 100f).height(4.dp).background(color, CircleShape))
        }
    }
}

/**
 * The trends page.
 *
 * The page's shape is the frozen baseline's: a window selector, three statistic
 * cards, then the curve block. The curve block is a chart, not a list of
 * samples — a table of readings is not a trend curve.
 */
@Composable
private fun TrendsScreen(client: MonitoringClient) {
    var view by remember { mutableStateOf<TrendsView?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var window by remember { mutableStateOf(TrendWindow.LAST_HOUR) }
    // Re-tapping the active window does not change `window`, so the effect key
    // alone would never refetch. This tick bumps on every tap to force a reload.
    var retryTick by remember { mutableStateOf(0) }
    // The window is the effect key, so tapping a range cancels the in-flight fetch
    // for the previous one instead of letting two responses race for one state.
    LaunchedEffect(window, retryTick) {
        // Clear immediately when the window changed so the previous window's
        // curve is never left on screen masquerading as the newly selected one.
        // A same-window refresh (retryTick bump) keeps the old content as a
        // loading transition only; failure still clears it below.
        if (view?.windowKey != window.name) {
            view = null
        }
        error = null
        try {
            view = client.loadTrends(window)
            error = null
        } catch (e: CancellationException) {
            // Propagate structured-concurrency cancellation. Swallowing it here
            // would let a cancelled fetch write `error` over the new window's state.
            throw e
        } catch (e: Exception) {
            // Failure strategy (identical on Android / MiniApp / wx-native): do
            // not keep the old curve looking like the new window. Clear it and
            // show an explicit failure message the user can act on. Empty success
            // stays a separate state ("所选区间内没有遥测样本").
            view = null
            error = HISTORY_LOAD_ERROR
        }
    }
    Page("历史趋势", "数据统计与曲线") {
        // The selector renders before the data arrives: it is the control the user
        // needs in order to fetch anything, so hiding it behind the result would
        // leave a page with nothing to do while the first request is in flight.
        Segmented(
            options = view?.windowOptions ?: MonitoringPresentation.trendWindowOptions(),
            activeKey = window.name,
            onSelect = { key ->
                TrendWindow.entries.firstOrNull { it.name == key }?.let { selected ->
                    if (selected == window) {
                        // Re-tap the active window: retry the same window.
                        retryTick += 1
                    } else {
                        window = selected
                    }
                }
            },
        )
        if (view == null && error == null) Hint("数据加载中…")
        error?.let { message ->
            Hint(message, Danger)
            // Retry re-runs the same window without changing the selection.
            Text(
                "点击上方时间窗可重试",
                color = TextSecondary,
                fontSize = 12.sp,
                modifier = Modifier.padding(top = 4.dp),
            )
        }
        view?.let { data ->
            if (!data.hasData) {
                Hint("所选区间内没有遥测样本")
            } else {
                SectionTitle("统计摘要", "共 ${data.sampleCount} 条样本")
                Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    SummaryCard("温度", "°C", data.temperature)
                    SummaryCard("湿度", "%RH", data.humidity)
                    SummaryCard(
                        "气体",
                        "ppm",
                        // A window with no calibrated gas reading has no gas
                        // statistics at all, which is not the same as a measured
                        // zero, so the card keeps its empty cells.
                        if (data.gasSampleCount > 0) data.gas else EMPTY_SUMMARY,
                    )
                }
                if (data.gasSampleCount < data.sampleCount) {
                    Hint("${data.sampleCount - data.gasSampleCount} 条样本没有已校准气体读数，未计入气体统计")
                }
                SectionTitle("曲线视图", data.curveStatusText)
                TrendChart(data)
                Hint(data.footerHint)
            }
        }
    }
}

private val EMPTY_SUMMARY = MetricSummary("--", "--", "--", "--")

/**
 * Draws the real telemetry trend chart using Compose Canvas.
 *
 * Each metric is mapped to its own valid range (with 10% padding),
 * and X coordinates follow actual sampling timestamps. Missing gas readings
 * break the line rather than connecting across gaps or dropping to zero.
 */
@Composable
private fun TrendChart(data: TrendsView) {
    GlassCard {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(14.dp)) {
            data.curveLegend.forEach { LegendDot(it) }
        }
        Box(
            Modifier.fillMaxWidth().padding(top = 14.dp).height(180.dp)
                .background(Color.Black.copy(alpha = 0.18f), RoundedCornerShape(14.dp))
                .padding(horizontal = 8.dp, vertical = 10.dp),
        ) {
            Canvas(Modifier.fillMaxSize()) {
                val layout = TrendChartGeometry.compute(
                    points = data.series,
                    width = size.width,
                    height = size.height,
                    padLeft = 4.dp.toPx(),
                    padRight = 4.dp.toPx(),
                    padTop = 8.dp.toPx(),
                    padBottom = 8.dp.toPx(),
                )

                // 1. Grid lines
                layout.gridLinesY.forEach { y ->
                    drawLine(
                        color = Color.White.copy(alpha = 0.06f),
                        start = Offset(0f, y),
                        end = Offset(size.width, y),
                        strokeWidth = 1.dp.toPx(),
                    )
                }

                // 2. Draw each metric series
                fun drawSeries(series: ChartSeries, color: Color) {
                    val strokeWidth = 2.dp.toPx()
                    val pointRadius = 3.dp.toPx()

                    series.segments.forEach { segment ->
                        if (segment.size >= 2) {
                            val path = Path().apply {
                                moveTo(segment[0].x, segment[0].y)
                                for (i in 1 until segment.size) {
                                    lineTo(segment[i].x, segment[i].y)
                                }
                            }
                            drawPath(
                                path = path,
                                color = color,
                                style = Stroke(width = strokeWidth, cap = StrokeCap.Round),
                            )
                        }
                        segment.forEach { pt ->
                            drawCircle(
                                color = color,
                                radius = pointRadius,
                                center = Offset(pt.x, pt.y),
                            )
                        }
                    }

                    series.singlePoints.forEach { pt ->
                        drawCircle(
                            color = color,
                            radius = pointRadius + 1.dp.toPx(),
                            center = Offset(pt.x, pt.y),
                        )
                    }
                }

                drawSeries(layout.temperatureSeries, Danger)
                drawSeries(layout.humiditySeries, Info)
                drawSeries(layout.gasSeries, Mint)
            }
        }
        Row(Modifier.fillMaxWidth().padding(top = 8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(data.curveAxisStart, color = TextSecondary, fontSize = 11.sp)
            Text(data.curveAxisEnd, color = TextSecondary, fontSize = 11.sp)
        }
    }
}

@Composable
private fun LegendDot(item: CurveLegend) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(8.dp).background(toneColor(item.tone), CircleShape))
        val label = if (item.rangeText.isNotEmpty() && item.rangeText != "--") {
            "${item.label} (${item.rangeText})"
        } else {
            item.label
        }
        Text(label, color = TextSecondary, fontSize = 12.sp, modifier = Modifier.padding(start = 5.dp))
    }
}

/**
 * One segmented selector row.
 *
 * The trends windows and the alert filters are both lists of [SelectOption], so
 * one control renders both and the baseline's "the active option is a filled mint
 * pill" behaviour appears twice without being written twice. Which option is
 * active is decided here by comparing [activeKey], because the option list itself
 * deliberately carries no selected state.
 */
@Composable
private fun Segmented(options: List<SelectOption>, activeKey: String, onSelect: (String) -> Unit) {
    val shape = RoundedCornerShape(999.dp)
    Row(
        Modifier.fillMaxWidth().padding(top = 8.dp)
            .background(Surface, shape)
            .padding(4.dp),
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        options.forEach { option ->
            val selected = option.key == activeKey
            val base = Modifier
                .weight(1f)
                // Tapping the active option is a no-op, which keeps a stray tap
                // from refetching the range already on screen.
                .clickable(enabled = !selected) { onSelect(option.key) }
                .padding(vertical = 9.dp)
            val styled = if (selected) {
                base.background(Brush.linearGradient(listOf(ActiveStart, ActiveEnd)), shape)
            } else {
                base
            }
            Box(styled, contentAlignment = Alignment.Center) {
                Text(
                    option.label,
                    color = if (selected) Background else TextSecondary,
                    fontSize = 13.sp,
                    fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Normal,
                )
            }
        }
    }
}

@Composable
private fun SummaryCard(name: String, unit: String, summary: MetricSummary) {
    Column(Modifier.width(190.dp).background(Surface, RoundedCornerShape(18.dp)).padding(16.dp)) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(name, color = TextSecondary, fontSize = 13.sp)
            Text(unit, color = TextSecondary, fontSize = 11.sp)
        }
        Text(summary.average, color = TextPrimary, fontSize = 32.sp, fontWeight = FontWeight.SemiBold)
        Row(Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            Column {
                Text("最低", color = TextSecondary, fontSize = 11.sp)
                Text(summary.minimum, color = TextPrimary, fontSize = 15.sp, fontWeight = FontWeight.SemiBold)
            }
            Column {
                Text("最高", color = TextSecondary, fontSize = 11.sp)
                Text(summary.maximum, color = TextPrimary, fontSize = 15.sp, fontWeight = FontWeight.SemiBold)
            }
        }
        // The peak time is what a min/avg/max triple cannot say on its own: when
        // the worst reading happened.
        Text("峰值 ${summary.peakAt}", color = TextSecondary, fontSize = 11.sp, modifier = Modifier.padding(top = 10.dp))
    }
}

@Composable
private fun AlertsScreen(client: MonitoringClient) {
    var view by remember { mutableStateOf<AlertsView?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var filter by remember { mutableStateOf(AlertFilter.ALL) }
    // Switching a pill refetches rather than re-filtering what is already on
    // screen. The filtering rule lives in the shared layer, and re-deriving it
    // here would be a second implementation of the same rule — the kind of
    // duplication that lets Android and the MiniApp drift.
    LaunchedEffect(filter) {
        error = null
        runCatching { client.loadAlerts(filter = filter) }
            .onSuccess { view = it }
            .onFailure { error = it.message }
    }
    Page("告警记录", "早期火情预警事件") {
        Segmented(
            options = view?.filters ?: MonitoringPresentation.alertFilterOptions(),
            activeKey = filter.key,
            onSelect = { key -> AlertFilter.entries.firstOrNull { it.key == key }?.let { filter = it } },
        )
        if (view == null && error == null) Hint("数据加载中…")
        error?.let { Hint(it, Danger) }
        view?.let { data ->
            if (data.visibleCount == 0) Hint("🛡  该分类下暂无告警记录")
            data.items.forEach { AlertCard(it) }
        }
    }
}

/**
 * One alert, laid out as the frozen baseline lays it out: a state header, a
 * bordered evidence panel of four fields, then the recovery line.
 *
 * The evidence labels name the quantity this backend actually returns. The
 * baseline's first cell is a gas rise in ppm because its contract exposes that
 * field; this client's contract exposes an ADC-code rise, so the label says ADC
 * rather than borrowing a unit the number is not in.
 */
@Composable
private fun AlertCard(item: AlertItemView) {
    GlassCard {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(8.dp).background(toneColor(item.tone), CircleShape))
                Text(
                    item.stateText,
                    color = toneColor(item.tone),
                    fontSize = 12.sp,
                    modifier = Modifier.padding(start = 8.dp)
                        .background(toneColor(item.tone).copy(alpha = 0.14f), CircleShape)
                        .padding(horizontal = 10.dp, vertical = 4.dp),
                )
            }
            Text(item.startedAt, color = TextSecondary, fontSize = 11.sp)
        }
        Column(
            Modifier.fillMaxWidth().padding(top = 14.dp)
                .background(Color.Black.copy(alpha = 0.18f), RoundedCornerShape(14.dp))
                .padding(14.dp),
        ) {
            Text("触发证据", color = TextSecondary, fontSize = 12.sp)
            Row(Modifier.fillMaxWidth().padding(top = 10.dp)) {
                EvidenceCell("气体 ADC 上升", item.gasAdcRiseText, Modifier.weight(1f))
                EvidenceCell("触发阈值", item.gasAdcRiseThresholdText, Modifier.weight(1f))
            }
            Row(Modifier.fillMaxWidth().padding(top = 10.dp)) {
                EvidenceCell("温升速率", item.temperatureRateText, Modifier.weight(1f))
                EvidenceCell("样本数", item.sampleCountText, Modifier.weight(1f))
            }
        }
        if (!item.active) {
            Text("已恢复 ${item.endedAt}", color = TextSecondary, fontSize = 11.sp, modifier = Modifier.padding(top = 12.dp))
        }
    }
}

@Composable
private fun EvidenceCell(label: String, value: String, modifier: Modifier) {
    Column(modifier) {
        Text(label, color = TextSecondary, fontSize = 11.sp)
        Text(value, color = TextPrimary, fontSize = 16.sp, fontWeight = FontWeight.SemiBold, modifier = Modifier.padding(top = 2.dp))
    }
}

@Composable
private fun SettingsScreen(client: MonitoringClient) {
    var view by remember { mutableStateOf<SettingsView?>(null) }
    var temperature by remember { mutableStateOf(30f) }
    // The humidity limit is not editable here, matching the frozen baseline's
    // settings page, which offers temperature and gas only. The value is still
    // sent: the contract requires all three fields on every update, so dropping it
    // would make every save a rejection.
    var humidity by remember { mutableStateOf(80f) }
    var gas by remember { mutableStateOf(20f) }
    var error by remember { mutableStateOf<String?>(null) }
    var saving by remember { mutableStateOf(false) }
    var commandHint by remember { mutableStateOf("") }
    var commandTone by remember { mutableStateOf(Tone.WARNING) }
    val scope = rememberCoroutineScope()

    suspend fun refresh() {
        runCatching { client.loadSettings() }.onSuccess {
            view = it
            temperature = it.temperatureHighC.toFloat()
            humidity = it.humidityHighRh.toFloat()
            gas = it.gasHighPpm.toFloat()
            error = null
        }.onFailure { error = it.message }
    }
    LaunchedEffect(Unit) { refresh() }

    fun save() {
        if (saving) return
        saving = true
        scope.launch {
            runCatching {
                // The shared client rejects an out-of-range value before any
                // network call, so the message names the contract range.
                val accepted = client.updateThresholds(
                    ThresholdUpdate(temperature.toDouble(), humidity.toDouble(), gas.toDouble()),
                )
                commandHint = accepted.stateText
                commandTone = accepted.tone
                val settled = client.awaitCommandOutcome(accepted.requestId)
                // Null is "the device has not answered yet", not a failure.
                commandHint = settled?.stateText ?: "等待设备确认"
                commandTone = settled?.tone ?: Tone.WARNING
            }.onFailure { error = it.message }
            refresh()
            saving = false
        }
    }

    Page("预警阈值", "设置设备本地报警的安全边界") {
        if (view == null && error == null) Hint("数据加载中…")
        error?.let { Hint(it, Danger) }
        view?.let { data ->
            SectionTitle("报警规则", "修改后需下发设备")
            // Ranges come from the shared contract constants, so a slider can
            // never offer a value the backend would reject with 422.
            ThresholdSlider("温度上限", temperature, "°C", temperatureRange()) { temperature = it }
            ThresholdSlider("气体浓度上限", gas, "ppm", gasRange()) { gas = it }
            SectionTitle("设备确认", "202 仅表示命令已接受")
            GlassCard {
                KeyValue("规则同步状态", data.confirmationText, toneColor(data.confirmationTone))
                KeyValue("期望版本", data.desiredVersion.toString(), TextPrimary)
                KeyValue("设备确认版本", data.confirmedVersion?.toString() ?: "--", TextPrimary)
                KeyValue("更新时间", data.updatedAt, TextPrimary)
            }
            if (commandHint.isNotEmpty()) Hint(commandHint, toneColor(commandTone))
            Button(
                enabled = !saving,
                onClick = { save() },
                colors = ButtonDefaults.buttonColors(containerColor = Mint, contentColor = Background),
                modifier = Modifier.fillMaxWidth().padding(top = 20.dp),
            ) { Text(if (saving) "正在下发…" else "保存并下发到设备") }
            Hint(data.saveHint)
        }
    }
}

/**
 * One threshold control.
 *
 * The slider is stepped so it only produces whole units. The device resolves a
 * whole degree and a whole ppm and rounds anything finer (`docs/device-protocol.md`
 * §4.3), so a continuous slider would let an operator set 30.5, show 30.5 and get
 * a device enforcing 31 — a control whose displayed value is not the value that
 * takes effect. The frozen baseline's temperature slider steps by 0.5 and is the
 * one place this client deliberately does not follow it.
 */
@Composable
private fun ThresholdSlider(
    label: String,
    value: Float,
    unit: String,
    range: ClosedFloatingPointRange<Float>,
    onChange: (Float) -> Unit,
) {
    GlassCard {
        Text(label, color = TextSecondary)
        Text("${value.roundToInt()} $unit", color = TextPrimary, fontSize = 34.sp, fontWeight = FontWeight.SemiBold)
        Slider(
            value = value,
            onValueChange = { onChange(it.roundToInt().toFloat()) },
            valueRange = range,
            steps = ((range.endInclusive - range.start).roundToInt() - 1).coerceAtLeast(0),
            // The baseline paints the slider with the accent, not the Material
            // default, which would otherwise be the only purple on the screen.
            colors = SliderDefaults.colors(
                thumbColor = Mint,
                activeTrackColor = Mint,
                inactiveTrackColor = Color.White.copy(alpha = 0.12f),
                activeTickColor = Background,
                inactiveTickColor = TextSecondary,
            ),
        )
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(range.start.roundToInt().toString(), color = TextSecondary, fontSize = 11.sp)
            Text(range.endInclusive.roundToInt().toString(), color = TextSecondary, fontSize = 11.sp)
        }
    }
}

@Composable
private fun KeyValue(label: String, value: String, color: Color) {
    Row(Modifier.fillMaxWidth().padding(top = 12.dp), horizontalArrangement = Arrangement.SpaceBetween) {
        Text(label, color = TextSecondary, fontSize = 13.sp)
        Text(value, color = color, fontSize = 13.sp, fontWeight = FontWeight.SemiBold)
    }
}

@Composable
private fun Pill(text: String, color: Color) {
    Text(text, color = color, fontSize = 12.sp, modifier = Modifier.background(color.copy(alpha = .14f), CircleShape).padding(horizontal = 10.dp, vertical = 4.dp))
}

/** Maps a shared tone token onto this host's palette. */
private fun toneColor(tone: String): Color = when (tone) {
    Tone.DANGER -> Danger
    Tone.WARNING -> Warning
    Tone.INFO -> Info
    else -> Mint
}

private fun temperatureRange() =
    ThresholdLimits.TEMPERATURE_MIN_C.toFloat()..ThresholdLimits.TEMPERATURE_MAX_C.toFloat()

private fun gasRange() =
    ThresholdLimits.GAS_MIN_PPM.toFloat()..ThresholdLimits.GAS_MAX_PPM.toFloat()
