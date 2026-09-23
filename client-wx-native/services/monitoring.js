/**
 * 业务监控数据服务层。
 * 对齐 client-kmp MonitoringClient 的职责与策略：
 * - 聚合 status 与 latest（404 视为空态而非异常）；
 * - 历史遥测以 order=desc 倒序拉取后在本地反转为升序；
 * - 告警列表统一拉取后由展示层做本地标签筛选；
 * - 阈值下发前先做本地范围校验；
 * - 控制命令通过 GET /commands/{requestId} 轮询终态（支持 429/5xx 重试与 401/404 立即中断）。
 */

const deviceService = require('./device.js')
const presentation = require('../utils/presentation.js')
const { validateThresholds } = require('../utils/threshold-limits.js')

const DEFAULT_TREND_LIMIT = 200
const DEFAULT_ALERT_LIMIT = 50
const MAX_SAMPLE_LIMIT = 1000
const DEFAULT_COMMAND_ATTEMPTS = 10
const DEFAULT_COMMAND_INTERVAL_MS = 1500

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function isRetryableError(err) {
  if (!err) return true
  if (err.code === 'network_error') return true
  if (err.statusCode === 429 || (err.statusCode >= 500 && err.statusCode <= 599)) return true
  return false
}

/**
 * 加载仪表盘快照：并发获取设备状态与最新遥测。
 * 若最新遥测返回 404，视为空态（telemetry = null），正常构建 hasData = false 的视图。
 */
async function loadDashboard(deviceId = 'MCU001') {
  const statusPromise = deviceService.getStatus(deviceId)
  const latestPromise = deviceService.getLatestTelemetry(deviceId).catch((err) => {
    if (err && err.statusCode === 404) return null
    throw err
  })

  const [status, latest] = await Promise.all([statusPromise, latestPromise])
  return presentation.dashboard(status, latest)
}

/**
 * 加载趋势统计与序列数据。
 * 查询参数采用绝对 from/to 时间戳，并显式指定 order=desc。
 * 收到数据后反转为升序，供统计与 Canvas 绘制共用。
 */
async function loadTrends(arg1 = 'MCU001', arg2 = 'LAST_HOUR', arg3 = DEFAULT_TREND_LIMIT) {
  const deviceId = typeof arg1 === 'string' && !presentation.TrendWindows.some((w) => w.key === arg1) ? arg1 : 'MCU001'
  const windowKey = typeof arg1 === 'string' && presentation.TrendWindows.some((w) => w.key === arg1) ? arg1 : (typeof arg2 === 'string' ? arg2 : 'LAST_HOUR')
  const limit = typeof arg2 === 'number' ? arg2 : (typeof arg3 === 'number' ? arg3 : DEFAULT_TREND_LIMIT)

  const safeLimit = Math.min(MAX_SAMPLE_LIMIT, Math.max(1, Number(limit) || DEFAULT_TREND_LIMIT))
  const win = presentation.TrendWindows.find((w) => w.key === windowKey) || presentation.TrendWindows[0]
  const now = Date.now()
  const to = new Date(now).toISOString()
  const from = new Date(now - win.hours * 3600 * 1000).toISOString()

  const res = await deviceService.getTelemetryHistory(deviceId, {
    from,
    to,
    limit: safeLimit,
    order: 'desc',
  })

  const rawItems = (res && res.items) || []
  // 倒序请求后本地反转为升序
  const points = rawItems.slice().reverse()
  return presentation.trends(points, windowKey)
}

/**
 * 统一提供给趋势页面的采样序列与展示模型。
 * 确保图表绘制与统计卡片消费同一批升序数据。
 */
async function loadTrendSamples(arg1 = 'MCU001', arg2 = 'LAST_HOUR', arg3 = DEFAULT_TREND_LIMIT) {
  const deviceId = typeof arg1 === 'string' && !presentation.TrendWindows.some((w) => w.key === arg1) ? arg1 : 'MCU001'
  const windowKey = typeof arg1 === 'string' && presentation.TrendWindows.some((w) => w.key === arg1) ? arg1 : (typeof arg2 === 'string' ? arg2 : 'LAST_HOUR')
  const limit = typeof arg2 === 'number' ? arg2 : (typeof arg3 === 'number' ? arg3 : DEFAULT_TREND_LIMIT)

  const safeLimit = Math.min(MAX_SAMPLE_LIMIT, Math.max(1, Number(limit) || DEFAULT_TREND_LIMIT))
  const win = presentation.TrendWindows.find((w) => w.key === windowKey) || presentation.TrendWindows[0]
  const now = Date.now()
  const to = new Date(now).toISOString()
  const from = new Date(now - win.hours * 3600 * 1000).toISOString()

  const res = await deviceService.getTelemetryHistory(deviceId, {
    from,
    to,
    limit: safeLimit,
    order: 'desc',
  })

  const rawItems = (res && res.items) || []
  const points = rawItems.slice().reverse()
  const trendsView = presentation.trends(points, windowKey)
  return {
    trendsView,
    rawPoints: points,
  }
}

/**
 * 加载告警列表并应用本地筛选。
 */
async function loadAlerts(deviceId = 'MCU001', limit = DEFAULT_ALERT_LIMIT, filterKey = 'all') {
  const safeLimit = Math.min(MAX_SAMPLE_LIMIT, Math.max(1, Number(limit) || DEFAULT_ALERT_LIMIT))
  const res = await deviceService.getAlerts(deviceId, { limit: safeLimit })
  const items = (res && res.items) || []
  return presentation.alerts(items, filterKey)
}

/**
 * 加载当前阈值与同步状态。
 */
async function loadSettings(deviceId = 'MCU001') {
  const thresholds = await deviceService.getThresholds(deviceId)
  return presentation.settings(thresholds)
}

/**
 * 查询指定命令的生命周期状态。
 */
async function loadCommandStatus(arg1 = 'MCU001', arg2) {
  const deviceId = arg2 ? arg1 : 'MCU001'
  const requestId = arg2 || arg1
  const status = await deviceService.getCommandStatus(deviceId, requestId)
  return presentation.commandStatus(status)
}

/**
 * 校验并下发阈值更新命令。
 */
async function updateThresholds(arg1, arg2) {
  const deviceId = typeof arg1 === 'string' ? arg1 : 'MCU001'
  const update = typeof arg1 === 'string' ? arg2 : arg1
  validateThresholds(update)
  const accepted = await deviceService.putThresholds(deviceId, {
    temperatureHighC: Number(update.temperatureHighC),
    humidityHighRh: Number(update.humidityHighRh),
    gasHighPpm: Number(update.gasHighPpm),
  })
  return presentation.commandAccepted(accepted)
}

/**
 * 轮询等待命令终态。
 * 只有 applied 算成功；429/5xx/网络错误重试；401/404/破坏性错误直接抛出。
 */
async function awaitCommandOutcome(
  arg1,
  arg2,
  attempts = DEFAULT_COMMAND_ATTEMPTS,
  intervalMs = DEFAULT_COMMAND_INTERVAL_MS
) {
  const deviceId = typeof arg1 === 'string' && typeof arg2 === 'string' ? arg1 : 'MCU001'
  const requestId = typeof arg1 === 'string' && typeof arg2 === 'string' ? arg2 : arg1

  for (let i = 0; i < attempts; i++) {
    await delay(intervalMs)
    let view = null
    try {
      view = await loadCommandStatus(deviceId, requestId)
    } catch (err) {
      if (!isRetryableError(err)) {
        throw err
      }
      view = null
    }
    if (view && view.settled) {
      return view
    }
  }
  return null
}

module.exports = {
  DEFAULT_TREND_LIMIT,
  DEFAULT_ALERT_LIMIT,
  MAX_SAMPLE_LIMIT,
  DEFAULT_COMMAND_ATTEMPTS,
  DEFAULT_COMMAND_INTERVAL_MS,
  loadDashboard,
  loadTrends,
  loadTrendSamples,
  loadAlerts,
  loadSettings,
  loadCommandStatus,
  updateThresholds,
  awaitCommandOutcome,
}
