/**
 * 展示层：把后端模型派生为「已经格式化好」的视图对象。
 *
 * 本文件是 client-kmp 共享层 `MonitoringPresentation` 的等价实现。
 * 字段名与 Kotlin 的 @Serializable data class 逐一对应，因此 Android、KMP 微信宿主
 * 与本端对同一份后端数据渲染出的文字、取整、空态完全一致。
 * 依据：client-kmp/shared/src/commonMain/.../monitoring/Presentation.kt
 *
 * 设计约束（与 KMP 相同）：
 * - 全部为纯函数，无副作用、不依赖微信 API，可直接单测；
 * - 文案集中在共享层，避免两端对同一个状态给出不同说法；
 * - `validate` 是唯一会抛错的入口。
 */
const { reading, percent, decimal, clockText } = require('./format.js')
const { THRESHOLD_LIMITS, validateThresholdUpdate } = require('./threshold-limits.js')

/* ---------- 固定文案（与 KMP 冻结基线一致） ---------- */
const MUTE_HINT = '静音不影响环境检测与告警上报'
const SAVE_HINT = '下发后需设备确认，确认前仍按旧规则报警'
const CURVE_STATUS_TEXT = '折线图下一步接入'
const CURVE_MASK_TITLE = '趋势曲线即将上线'
const CURVE_MASK_SUB = '三指标同屏对比'
const CURVE_AXIS_START = '区间起点'
const CURVE_AXIS_END = '此刻'
const TRENDS_FOOTER_HINT = '统计基于所选区间内的真实历史样本计算'

/** 共享色调令牌；各宿主自行映射为配色 */
const TONE = {
  MINT: 'mint',
  WARNING: 'warning',
  DANGER: 'danger',
  INFO: 'info',
}

/** 趋势窗口：label 与 from 边界的换算依据 */
const TREND_WINDOWS = [
  { key: 'LAST_HOUR', label: '近1小时', hours: 1 },
  { key: 'LAST_SIX_HOURS', label: '近6小时', hours: 6 },
  { key: 'LAST_DAY', label: '近24小时', hours: 24 },
]

/**
 * 告警筛选标签。
 *
 * 契约的告警状态只有 normal / suspect / fire_warning / recovered，
 * 没有 acknowledged，因此第三项用「疑似」——否则该标签永远筛不出数据。
 */
const ALERT_FILTERS = [
  { key: 'all', label: '全部' },
  { key: 'fire_warning', label: '火情' },
  { key: 'suspect', label: '疑似' },
  { key: 'recovered', label: '已恢复' },
]

/* ---------- 内部工具 ---------- */

/** 告警状态 -> 文案与色调 */
function alertStateMeta(state) {
  if (state === 'fire_warning') return { text: '火情预警', tone: TONE.DANGER }
  if (state === 'suspect') return { text: '疑似异常', tone: TONE.WARNING }
  if (state === 'recovered') return { text: '已恢复', tone: TONE.INFO }
  return { text: '正常', tone: TONE.MINT }
}

/**
 * 汇总单个指标的最低/平均/最高与峰值时刻。
 * 缺读数的样本（未标定的气体、传感器故障的非有限值）被跳过而不是当成 0，
 * 因此数据缺口只会降低样本数，不会拉低平均值。
 * @param {Object[]} points 遥测样本（升序）
 * @param {Function} selector 取值函数，返回 number|null
 * @returns {{minimum: string, average: string, maximum: string, peakAt: string}}
 */
function summarize(points, selector) {
  const measured = []
  points.forEach((point) => {
    const value = selector(point)
    if (value === null || value === undefined) return
    const n = Number(value)
    if (!isFinite(n)) return
    measured.push({ point, value: n })
  })
  if (!measured.length) return { minimum: '--', average: '--', maximum: '--', peakAt: '--' }

  let peak = measured[0]
  let min = measured[0].value
  let sum = 0
  measured.forEach((item) => {
    if (item.value > peak.value) peak = item
    if (item.value < min) min = item.value
    sum += item.value
  })
  return {
    minimum: reading(min),
    average: reading(sum / measured.length),
    maximum: reading(peak.value),
    peakAt: clockText(peak.point.receivedAt),
  }
}

/* ---------- 各页面视图 ---------- */

/**
 * 仪表盘视图。
 * telemetry 为 null 表示后端尚无有效样本（契约用 404 表达），此时指标显示 `--`，
 * 设备与告警状态仍取自 status，页面呈现空态而不是报错或假读数。
 * @param {Object} status GET /status 响应
 * @param {Object|null} telemetry GET /telemetry/latest 响应
 * @returns {Object} DashboardView
 */
function dashboard(status, telemetry) {
  const RISK = {
    normal: { level: 'normal', tone: TONE.MINT, text: '环境正常', detail: '各项指标处于安全范围' },
    suspect: { level: 'suspect', tone: TONE.WARNING, text: '疑似异常', detail: '部分条件异常，系统确认中' },
    fire_warning: { level: 'fire', tone: TONE.DANGER, text: '火情预警', detail: '气体突增与温升速率同时超限' },
    recovered: { level: 'recovered', tone: TONE.INFO, text: '指标已恢复', detail: '事件归档中' },
  }
  const risk = RISK[status.alarmState] || RISK.normal
  const localAlarm = telemetry ? !!telemetry.localAlarm : !!status.localAlarm
  const muted = telemetry ? !!telemetry.buzzerMuted : !!status.buzzerMuted
  const gasPpm = telemetry ? telemetry.gasPpm : null

  return {
    deviceId: status.deviceId,
    hasData: !!telemetry,
    online: status.connectivity === 'online',
    riskLevel: risk.level,
    riskTone: risk.tone,
    riskText: risk.text,
    riskDetail: risk.detail,
    connectivityText: status.connectivity === 'online' ? '在线' : status.connectivity === 'offline' ? '离线' : '未知',
    temperatureText: telemetry ? reading(telemetry.temperatureC) : '--',
    humidityText: telemetry ? reading(telemetry.humidityRh) : '--',
    gasText: gasPpm === null || gasPpm === undefined ? '--' : reading(gasPpm),
    gasAvailable: gasPpm !== null && gasPpm !== undefined,
    temperaturePercent: percent(telemetry ? telemetry.temperatureC : null, THRESHOLD_LIMITS.TEMPERATURE_MAX_C),
    humidityPercent: percent(telemetry ? telemetry.humidityRh : null, THRESHOLD_LIMITS.HUMIDITY_MAX_RH),
    gasPercent: percent(gasPpm, THRESHOLD_LIMITS.GAS_MAX_PPM),
    localAlarm,
    localAlarmText: localAlarm ? '报警中' : '正常',
    buzzerText: muted ? '已静音' : localAlarm ? '报警策略生效' : '待机',
    buzzerMuted: muted,
    updatedAt: (telemetry && telemetry.receivedAt) || status.lastSeenAt || '--',
    muteHint: MUTE_HINT,
  }
}

/** 单条趋势点视图 */
function trendPoint(point, index) {
  return {
    key: point.sequence === null || point.sequence === undefined ? 'idx-' + index : (point.bootId || 'boot') + '-' + point.sequence,
    receivedAt: point.receivedAt,
    timeText: clockText(point.receivedAt),
    temperatureText: reading(point.temperatureC),
    humidityText: reading(point.humidityRh),
    gasText: point.gasPpm === null || point.gasPpm === undefined ? '--' : reading(point.gasPpm),
    localAlarm: !!point.localAlarm,
  }
}

/**
 * 趋势视图。
 * window 只决定哪个选项高亮：样本已由服务端按 from/to 收窄，这里不再二次过滤，
 * 否则会把一个服务端已经遵守的窗口再缩小，让样本数与用户选的范围对不上。
 * @param {Object[]} points 升序样本
 * @param {string} [windowKey] 当前窗口 key
 * @returns {Object} TrendsView
 */
function trends(points, windowKey) {
  const list = points || []
  const window = TREND_WINDOWS.filter((w) => w.key === windowKey)[0] || TREND_WINDOWS[0]
  let gasSampleCount = 0
  list.forEach((p) => {
    if (p.gasPpm !== null && p.gasPpm !== undefined && isFinite(Number(p.gasPpm))) gasSampleCount += 1
  })
  return {
    sampleCount: list.length,
    hasData: list.length > 0,
    temperature: summarize(list, (p) => p.temperatureC),
    humidity: summarize(list, (p) => p.humidityRh),
    gas: summarize(list, (p) => p.gasPpm),
    gasSampleCount,
    windowKey: window.key,
    windowLabel: window.label,
    windowOptions: trendWindowOptions(),
    curveStatusText: CURVE_STATUS_TEXT,
    curveMaskTitle: CURVE_MASK_TITLE,
    curveMaskSub: CURVE_MASK_SUB,
    curveAxisStart: CURVE_AXIS_START,
    curveAxisEnd: CURVE_AXIS_END,
    // 两端都还没画曲线，占位提示是用户实际看到的内容
    curveReady: false,
    curveLegend: [
      { label: '温度', tone: TONE.DANGER },
      { label: '湿度', tone: TONE.INFO },
      { label: '气体', tone: TONE.MINT },
    ],
    footerHint: TRENDS_FOOTER_HINT,
    series: list.map(trendPoint),
  }
}

/**
 * 单条告警视图。
 * 证据读自后端持久化的 evidence，绝不用最新值反推历史原因。
 * 气体项是 ADC 码的上升量（ppm 未标定期间仍然有意义）。
 * @param {Object} event 告警事件
 * @returns {Object} AlertItemView
 */
function alert(event) {
  const meta = alertStateMeta(event.state)
  const evidence = event.evidence || {}
  const rate = evidence.temperatureRateCPerMinute
  const rateThreshold = evidence.temperatureRateThresholdCPerMinute
  return {
    id: event.id,
    state: event.state,
    stateText: meta.text,
    tone: meta.tone,
    startedAt: event.startedAt,
    endedAt: event.endedAt || '--',
    active: event.endedAt === null || event.endedAt === undefined,
    gasAdcRiseText: evidence.gasAdcRise === null || evidence.gasAdcRise === undefined ? '--' : String(evidence.gasAdcRise),
    gasAdcRiseThresholdText:
      evidence.gasAdcRiseThreshold === null || evidence.gasAdcRiseThreshold === undefined ? '--' : String(evidence.gasAdcRiseThreshold),
    temperatureRateText: rate === null || rate === undefined ? '--' : decimal(rate) + ' °C/min',
    temperatureRateThresholdText:
      rateThreshold === null || rateThreshold === undefined ? '--' : decimal(rateThreshold) + ' °C/min',
    sampleCountText: evidence.sampleCount === null || evidence.sampleCount === undefined ? '--' : String(evidence.sampleCount),
    windowSecondsText: evidence.windowSeconds === null || evidence.windowSeconds === undefined ? '--' : String(evidence.windowSeconds),
  }
}

/**
 * 告警列表视图。
 * 过滤在客户端做：契约的 alerts 查询没有 state 参数，一次请求 + 本地切标签
 * 可以避免每切一次标签就打一次网络请求。
 * @param {Object[]} events 告警事件
 * @param {string} [filterKey] 当前筛选 key
 * @returns {Object} AlertsView
 */
function alerts(events, filterKey) {
  const list = events || []
  const key = filterKey || 'all'
  const shown = key === 'all' ? list : list.filter((e) => e.state === key)
  return {
    count: list.length,
    visibleCount: shown.length,
    filterKey: key,
    filters: alertFilterOptions(),
    items: shown.map(alert),
  }
}

/**
 * 阈值设置视图。
 *
 * confirmed 的判定要求 confirmedVersion 存在且不小于 desiredVersion：
 * 契约明确「不得把期望值当作设备已确认」，因此版本落后时一律显示等待中。
 * @param {Object} value GET /thresholds 响应
 * @returns {Object} SettingsView
 */
function settings(value) {
  const confirmed =
    value.confirmationState === 'confirmed' &&
    value.confirmedVersion !== null &&
    value.confirmedVersion !== undefined &&
    value.confirmedVersion >= value.desiredVersion
  const text = {
    confirmed: '设备已确认',
    pending: '等待设备确认',
    rejected: '设备已拒绝',
    timed_out: '确认超时，请重试',
  }[value.confirmationState] || value.confirmationState
  const tone =
    value.confirmationState === 'confirmed'
      ? TONE.MINT
      : value.confirmationState === 'pending'
        ? TONE.WARNING
        : TONE.DANGER

  return {
    temperatureHighC: value.temperatureHighC,
    humidityHighRh: value.humidityHighRh,
    gasHighPpm: value.gasHighPpm,
    desiredVersion: value.desiredVersion,
    confirmedVersion: value.confirmedVersion,
    confirmationState: value.confirmationState,
    confirmationText: text,
    confirmationTone: tone,
    confirmed,
    awaitingDevice: !confirmed,
    updatedAt: value.updatedAt || '--',
    saveHint: SAVE_HINT,
  }
}

/**
 * 已入队命令视图。
 * 入队响应的状态永远是 pending：broker 已发布、设备尚未答复，
 * 因此这里绝不呈现为「已确认」——宿主必须轮询 commandStatus 拿终态。
 * @param {Object} accepted POST/PUT 的 202 响应
 * @returns {Object} CommandStatusView
 */
function commandAccepted(accepted) {
  return {
    requestId: accepted.requestId,
    state: accepted.status,
    stateText: '等待设备确认',
    tone: TONE.WARNING,
    settled: false,
    confirmed: false,
    failed: false,
    versionText: accepted.desiredVersion === null || accepted.desiredVersion === undefined ? '--' : String(accepted.desiredVersion),
    errorText: '--',
  }
}

/** 命令生命周期状态 -> 宿主可用的语义 */
function commandStatus(value) {
  const settled = value.state !== 'accepted' && value.state !== 'published'
  const confirmed = value.state === 'applied'
  const failed = settled && !confirmed && value.state !== 'duplicate'
  const text = {
    accepted: '命令已接受',
    published: '已下发，等待设备确认',
    applied: '设备已确认',
    rejected: '设备已拒绝',
    expired: '命令已过期',
    duplicate: '重复命令，已忽略',
    failed: '设备执行失败',
    timed_out: '设备确认超时',
    publish_failed: '下发失败',
  }[value.state] || value.state
  const tone = confirmed
    ? TONE.MINT
    : !settled
      ? TONE.WARNING
      : value.state === 'duplicate'
        ? TONE.INFO
        : TONE.DANGER

  return {
    requestId: value.requestId,
    state: value.state,
    stateText: text,
    tone,
    settled,
    confirmed,
    failed,
    versionText: value.confirmedVersion === null || value.confirmedVersion === undefined ? '--' : String(value.confirmedVersion),
    errorText: value.errorCode || '--',
  }
}

/** 趋势窗口选项（顺序即选择器顺序） */
function trendWindowOptions() {
  return TREND_WINDOWS.map((w) => ({ key: w.key, label: w.label }))
}

/** 告警筛选选项 */
function alertFilterOptions() {
  return ALERT_FILTERS.map((f) => ({ key: f.key, label: f.label }))
}

/**
 * 两份选择器选项。
 * 页面在首个数据响应到达前就要能画出选择器，标签只能来自共享层，
 * 否则同一组选项会在页面脚本里被重复定义并逐渐分叉。
 */
function selectors() {
  return { windows: trendWindowOptions(), filters: alertFilterOptions() }
}

/** 窗口 key -> 小时数（用于换算 from 边界） */
function windowHours(key) {
  const hit = TREND_WINDOWS.filter((w) => w.key === key)[0]
  return (hit || TREND_WINDOWS[0]).hours
}

module.exports = {
  TONE,
  MUTE_HINT,
  SAVE_HINT,
  TREND_WINDOWS,
  ALERT_FILTERS,
  dashboard,
  trends,
  alerts,
  alert,
  settings,
  commandAccepted,
  commandStatus,
  trendWindowOptions,
  alertFilterOptions,
  selectors,
  windowHours,
  validate: validateThresholdUpdate,
}
