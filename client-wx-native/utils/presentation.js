/**
 * 纯展示层派生规则（Side-effect free）。
 * 严格对齐 client-kmp MonitoringPresentation，字段名与 Kotlin @Serializable data class 一致。
 */

const { reading, percent, decimal, clockText, parseEpochMillis } = require('./format.js')
const { ThresholdLimits } = require('./threshold-limits.js')

const SAVE_HINT = '下发后需设备确认，确认前仍按旧规则报警'
const CURVE_STATUS_TEXT = '各指标按独立量程展示'
const CURVE_MASK_TITLE = '三条曲线按各自量程展示'
const CURVE_MASK_SUB = '用于观察变化趋势，不用于直接比较曲线高度'
const CURVE_AXIS_START = '区间起点'
const CURVE_AXIS_END = '此刻'
const TRENDS_FOOTER_HINT = '三条曲线按各自量程展示，用于观察变化趋势，不用于直接比较曲线高度；数据受最近一页最多200条限制'

const Tone = {
  MINT: 'mint',
  WARNING: 'warning',
  DANGER: 'danger',
  INFO: 'info',
}

const TrendWindows = [
  { key: 'LAST_HOUR', label: '近1小时', hours: 1 },
  { key: 'LAST_SIX_HOURS', label: '近6小时', hours: 6 },
  { key: 'LAST_DAY', label: '近24小时', hours: 24 },
]

const AlertFilters = [
  { key: 'all', label: '全部' },
  { key: 'fire_warning', label: '火情' },
  { key: 'suspect', label: '疑似' },
  { key: 'recovered', label: '已恢复' },
]

function trendWindowOptions() {
  return TrendWindows.map((w) => ({ key: w.key, label: w.label }))
}

function alertFilterOptions() {
  return AlertFilters.map((f) => ({ key: f.key, label: f.label }))
}

function selectors() {
  return {
    windows: trendWindowOptions(),
    filters: alertFilterOptions(),
  }
}

/**
 * 仪表盘视图模型派生。
 * 注意：远程静音已移除，buzzerText 严格为 报警策略生效 / 待机。
 */
function dashboard(status = {}, telemetry = null) {
  const alarmState = status.alarmState || 'normal'
  let risk
  switch (alarmState) {
    case 'suspect':
      risk = { level: 'suspect', tone: Tone.WARNING, text: '疑似异常', detail: '部分条件异常，系统确认中' }
      break
    case 'fire_warning':
      risk = { level: 'fire', tone: Tone.DANGER, text: '火情预警', detail: '气体突增与温升速率同时超限' }
      break
    case 'recovered':
      risk = { level: 'recovered', tone: Tone.INFO, text: '指标已恢复', detail: '事件归档中' }
      break
    case 'normal':
    default:
      risk = { level: 'normal', tone: Tone.MINT, text: '环境正常', detail: '各项指标处于安全范围' }
      break
  }

  const localAlarm = Boolean(telemetry && telemetry.localAlarm != null ? telemetry.localAlarm : status.localAlarm)
  const connectivity = status.connectivity || 'unknown'
  const online = connectivity === 'online'
  const connectivityText = online ? '在线' : (connectivity === 'offline' ? '离线' : '未知')

  const hasData = telemetry != null
  const tempVal = telemetry ? telemetry.temperatureC : null
  const humVal = telemetry ? telemetry.humidityRh : null
  const gasVal = telemetry ? telemetry.gasPpm : null

  return {
    deviceId: status.deviceId || 'MCU001',
    hasData,
    online,
    riskLevel: risk.level,
    riskTone: risk.tone,
    riskText: risk.text,
    riskDetail: risk.detail,
    connectivityText,
    temperatureText: reading(tempVal),
    humidityText: reading(humVal),
    gasText: reading(gasVal),
    gasAvailable: telemetry != null && telemetry.gasPpm != null,
    temperaturePercent: percent(tempVal, ThresholdLimits.TEMPERATURE_MAX_C),
    humidityPercent: percent(humVal, ThresholdLimits.HUMIDITY_MAX_RH),
    gasPercent: percent(gasVal, ThresholdLimits.GAS_MAX_PPM),
    localAlarm,
    localAlarmText: localAlarm ? '报警中' : '正常',
    buzzerText: localAlarm ? '报警策略生效' : '待机',
    updatedAt: (telemetry && telemetry.receivedAt) || status.lastSeenAt || '--',
  }
}

/** 针对单一指标聚合统计，跳过未测量/非法样本 */
function summarizeMetric(points, selector) {
  const measured = []
  for (let i = 0; i < points.length; i++) {
    const val = selector(points[i])
    if (val != null && Number.isFinite(Number(val))) {
      measured.push({ point: points[i], value: Number(val) })
    }
  }
  if (!measured.length) {
    return { minimum: '--', average: '--', maximum: '--', peakAt: '--' }
  }

  let min = measured[0].value
  let max = measured[0].value
  let sum = 0
  let peak = measured[0]

  for (let i = 0; i < measured.length; i++) {
    const v = measured[i].value
    sum += v
    if (v < min) min = v
    if (v > max) {
      max = v
      peak = measured[i]
    }
  }

  return {
    minimum: reading(min),
    average: reading(sum / measured.length),
    maximum: reading(max),
    peakAt: clockText(peak.point.receivedAt),
  }
}

function trendPoint(point, index) {
  const seqKey = point.sequence != null ? `${point.bootId || 'boot'}-${point.sequence}` : `idx-${index}`
  return {
    key: seqKey,
    receivedAt: point.receivedAt || '',
    timeText: clockText(point.receivedAt),
    temperatureText: reading(point.temperatureC),
    humidityText: reading(point.humidityRh),
    gasText: reading(point.gasPpm),
    localAlarm: Boolean(point.localAlarm),
    timestampEpochMs: parseEpochMillis(point.receivedAt),
    temperatureC: Number.isFinite(Number(point.temperatureC)) ? Number(point.temperatureC) : 0.0,
    humidityRh: Number.isFinite(Number(point.humidityRh)) ? Number(point.humidityRh) : 0.0,
    gasPpm: point.gasPpm != null && Number.isFinite(Number(point.gasPpm)) ? Number(point.gasPpm) : null,
  }
}

/**
 * 历史趋势视图模型派生。
 * points 必须按时间升序排列。
 */
function trends(points = [], windowKey = 'LAST_HOUR') {
  const win = TrendWindows.find((w) => w.key === windowKey) || TrendWindows[0]
  const gasReadings = points.filter((p) => p.gasPpm != null && Number.isFinite(Number(p.gasPpm)))

  const tempSummary = summarizeMetric(points, (p) => p.temperatureC)
  const humSummary = summarizeMetric(points, (p) => p.humidityRh)
  const gasSummary = summarizeMetric(points, (p) => p.gasPpm)

  const tempRange = points.length > 0 ? `${tempSummary.minimum}~${tempSummary.maximum}°C` : ''
  const humRange = points.length > 0 ? `${humSummary.minimum}~${humSummary.maximum}%` : ''
  const gasRange = gasReadings.length > 0 ? `${gasSummary.minimum}~${gasSummary.maximum}ppm` : ''

  const firstStamp = points.length > 0 ? points[0].receivedAt : null
  const lastStamp = points.length > 0 ? points[points.length - 1].receivedAt : null

  const axisStart = points.length > 0 && parseEpochMillis(firstStamp) != null ? clockText(firstStamp) : '--'
  const axisEnd = points.length > 0 && parseEpochMillis(lastStamp) != null ? clockText(lastStamp) : '--'
  const curveStatusText = points.length > 0 ? CURVE_STATUS_TEXT : '暂无数据'

  return {
    sampleCount: points.length,
    hasData: points.length > 0,
    temperature: tempSummary,
    humidity: humSummary,
    gas: gasSummary,
    gasSampleCount: gasReadings.length,
    windowKey: win.key,
    windowLabel: win.label,
    windowOptions: trendWindowOptions(),
    curveStatusText,
    curveMaskTitle: CURVE_MASK_TITLE,
    curveMaskSub: CURVE_MASK_SUB,
    curveAxisStart: axisStart,
    curveAxisEnd: axisEnd,
    curveReady: points.length > 0,
    curveLegend: [
      { label: '温度', tone: Tone.DANGER, rangeText: tempRange },
      { label: '湿度', tone: Tone.INFO, rangeText: humRange },
      { label: '气体', tone: Tone.MINT, rangeText: gasRange },
    ],
    footerHint: TRENDS_FOOTER_HINT,
    series: points.map((p, i) => trendPoint(p, i)),
  }
}

function alertItem(e) {
  let stateText
  let tone
  switch (e.state) {
    case 'fire_warning':
      stateText = '火情预警'
      tone = Tone.DANGER
      break
    case 'suspect':
      stateText = '疑似异常'
      tone = Tone.WARNING
      break
    case 'recovered':
      stateText = '已恢复'
      tone = Tone.INFO
      break
    case 'normal':
    default:
      stateText = '正常'
      tone = Tone.MINT
      break
  }

  const ev = e.evidence || {}
  return {
    id: e.id || '',
    state: e.state || 'normal',
    stateText,
    tone,
    startedAt: clockText(e.startedAt),
    endedAt: e.endedAt ? clockText(e.endedAt) : '--',
    active: !e.endedAt,
    gasAdcRiseText: ev.gasAdcRise != null ? String(ev.gasAdcRise) : '--',
    gasAdcRiseThresholdText: ev.gasAdcRiseThreshold != null ? String(ev.gasAdcRiseThreshold) : '--',
    temperatureRateText:
      ev.temperatureRateCPerMinute != null ? `${decimal(ev.temperatureRateCPerMinute)}°C/min` : '--',
    temperatureRateThresholdText:
      ev.temperatureRateThresholdCPerMinute != null
        ? `${decimal(ev.temperatureRateThresholdCPerMinute)}°C/min`
        : '--',
    sampleCountText: ev.sampleCount != null ? String(ev.sampleCount) : '--',
    windowSecondsText: ev.windowSeconds != null ? `${ev.windowSeconds}s` : '--',
  }
}

/**
 * 告警列表视图模型派生。
 * @param {Array} events
 * @param {string} filterKey 'all' | 'fire_warning' | 'suspect' | 'recovered'
 */
function alerts(events = [], filterKey = 'all') {
  const shown = filterKey === 'all' ? events : events.filter((e) => e.state === filterKey)
  const items = shown.map(alertItem)
  return {
    count: events.length,
    visibleCount: items.length,
    filterKey,
    filters: alertFilterOptions(),
    items,
  }
}

/**
 * 阈值设置视图模型派生。
 */
function settings(thresholds = {}) {
  const confirmed =
    thresholds.confirmationState === 'confirmed' &&
    thresholds.confirmedVersion != null &&
    thresholds.confirmedVersion >= (thresholds.desiredVersion || 0)

  let confirmationText
  let confirmationTone
  switch (thresholds.confirmationState) {
    case 'confirmed':
      confirmationText = '设备已确认'
      confirmationTone = Tone.MINT
      break
    case 'pending':
      confirmationText = '等待设备确认'
      confirmationTone = Tone.WARNING
      break
    case 'rejected':
      confirmationText = '设备已拒绝'
      confirmationTone = Tone.DANGER
      break
    case 'timed_out':
      confirmationText = '确认超时，请重试'
      confirmationTone = Tone.DANGER
      break
    default:
      confirmationText = thresholds.confirmationState || '未知'
      confirmationTone = Tone.INFO
      break
  }

  return {
    temperatureHighC: Number(thresholds.temperatureHighC || 0),
    humidityHighRh: Number(thresholds.humidityHighRh || 0),
    gasHighPpm: Number(thresholds.gasHighPpm || 0),
    desiredVersion: Number(thresholds.desiredVersion || 0),
    confirmedVersion: thresholds.confirmedVersion != null ? Number(thresholds.confirmedVersion) : null,
    confirmationState: thresholds.confirmationState || 'pending',
    confirmationText,
    confirmationTone,
    confirmed,
    awaitingDevice: !confirmed,
    updatedAt: thresholds.updatedAt || '--',
    saveHint: SAVE_HINT,
  }
}

/**
 * 阈值下发 202 接受视图派生。
 */
function commandAccepted(value = {}) {
  return {
    requestId: value.requestId || '',
    state: value.status || 'pending',
    stateText: '等待设备确认',
    tone: Tone.WARNING,
    settled: false,
    confirmed: false,
    failed: false,
    versionText: value.desiredVersion != null ? String(value.desiredVersion) : '--',
    errorText: '--',
  }
}

/**
 * 命令生命周期状态视图派生。
 */
function commandStatus(value = {}) {
  const state = value.state
  const settled = state !== 'accepted' && state !== 'published'
  const confirmed = state === 'applied'
  const failed = settled && !confirmed && state !== 'duplicate'

  let stateText
  switch (state) {
    case 'accepted':
      stateText = '命令已接受'
      break
    case 'published':
      stateText = '已下发，等待设备确认'
      break
    case 'applied':
      stateText = '设备已确认'
      break
    case 'rejected':
      stateText = '设备已拒绝'
      break
    case 'expired':
      stateText = '命令已过期'
      break
    case 'duplicate':
      stateText = '重复命令，已忽略'
      break
    case 'failed':
      stateText = '设备执行失败'
      break
    case 'timed_out':
      stateText = '设备确认超时'
      break
    case 'publish_failed':
      stateText = '下发失败'
      break
    default:
      stateText = state || '未知'
      break
  }

  let tone
  if (confirmed) {
    tone = Tone.MINT
  } else if (!settled) {
    tone = Tone.WARNING
  } else if (state === 'duplicate') {
    tone = Tone.INFO
  } else {
    tone = Tone.DANGER
  }

  return {
    requestId: value.requestId || '',
    state: state || '',
    stateText,
    tone,
    settled,
    confirmed,
    failed,
    versionText: value.confirmedVersion != null ? String(value.confirmedVersion) : '--',
    errorText: value.errorCode || '--',
  }
}

module.exports = {
  Tone,
  SAVE_HINT,
  CURVE_STATUS_TEXT,
  CURVE_MASK_TITLE,
  CURVE_MASK_SUB,
  CURVE_AXIS_START,
  CURVE_AXIS_END,
  TRENDS_FOOTER_HINT,
  TrendWindows,
  AlertFilters,
  trendWindowOptions,
  alertFilterOptions,
  selectors,
  dashboard,
  trends,
  alerts,
  settings,
  commandAccepted,
  commandStatus,
}
