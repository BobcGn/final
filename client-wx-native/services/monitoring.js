/**
 * 监测数据层：契约路径、查询构造、幂等键与命令轮询。
 *
 * 本文件是 client-kmp 共享层 `MonitoringClient` 的等价实现：
 * 端点路径、查询参数、校验时机、幂等键要求与命令终态轮询策略全部保持一致，
 * 页面只消费它返回的「已格式化视图」，不再自己拼字段或做换算。
 * 依据：client-kmp/shared/src/commonMain/.../monitoring/MonitoringClient.kt
 */
const { rawRequest, ApiError } = require('./request.js')
const { env } = require('../config/env.js')
const mock = require('./mock/mock.js')
const presentation = require('../utils/presentation.js')
const { genIdempotencyKey } = require('../utils/helpers.js')
const { rfc3339Utc, hoursToMillis } = require('../utils/format.js')

/** 趋势分页默认值：与后端默认页大小一致，兼顾"看得到近期变化"与流量预算 */
const DEFAULT_TREND_LIMIT = 200
/** 告警默认页大小 */
const DEFAULT_ALERT_LIMIT = 50
/** 契约允许的最大页大小 */
const MAX_SAMPLE_LIMIT = 1000
/** 控制回路预算：10 × 1.5s ≈ 15s，足够覆盖一个设备上报周期 */
const DEFAULT_COMMAND_ATTEMPTS = 10
const DEFAULT_COMMAND_INTERVAL_MS = 1500

let deviceId = 'MCU001'

/**
 * 切换目标设备。
 * @param {string} id 设备编号，须匹配 ^[A-Za-z0-9_-]{1,32}$
 */
function setDeviceId(id) {
  deviceId = id
}

function currentDeviceId() {
  return deviceId
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/**
 * 宿主传输层（对应 KMP 的 `MonitoringPlatform.request`）。
 * useMock 为真时由本地 Mock 响应，否则走真实后端；调用方不感知差异。
 * @param {string} path 以 /api/v1 开头的路径
 * @param {Object} [options] { method, data, query }
 */
function request(path, options) {
  if (env.useMock) return mock.handle(path, options || {})
  return rawRequest(path, options || {})
}

/** GET，成功返回解析后的对象；2xx 但结构不是对象视为响应损坏 */
async function get(path, options) {
  const data = await request(path, options)
  if (data === null || typeof data !== 'object') {
    throw new ApiError('malformed_response', '响应解析失败 (HTTP 200)', 200)
  }
  return data
}

/**
 * GET，遇到指定状态码返回 null。
 * 仪表盘用它对 `telemetry/latest` 的 404 表达空态：设备尚无有效样本不是错误。
 * @param {string} path 路径
 * @param {number} on 视为空态的状态码
 */
async function getOrNull(path, on) {
  try {
    return await get(path)
  } catch (e) {
    if (e instanceof ApiError && e.statusCode === on) return null
    throw e
  }
}

/** 控制类请求：统一带幂等键（契约要求重复请求返回同一结果） */
function send(path, method, body) {
  return request(path, {
    method,
    data: body,
    header: { 'Idempotency-Key': genIdempotencyKey() },
  })
}

/**
 * 加载仪表盘：一次取 status + latest，组成原子快照。
 *
 * 两者必须一起取：分开取会出现「新的读数配旧的报警状态」，
 * 页面就会显示一个后端从未处在过的状态组合。
 * @returns {Promise<Object>} DashboardView
 */
async function loadDashboard() {
  const status = await get('/api/v1/devices/' + deviceId + '/status')
  const latest = await getOrNull('/api/v1/devices/' + deviceId + '/telemetry/latest', 404)
  return presentation.dashboard(status, latest)
}

/**
 * 加载趋势样本（升序返回）。
 *
 * 查询用绝对 from/to 边界而不是只给 limit：只给 limit 时后端返回的是区间内
 * 最旧的一页，24 小时窗口会变成"只描述开头几分钟"，却被当成一整天的走势。
 * 因此按 order=desc 取最近一页，再在本地反转为升序，保证下游统计口径一致。
 *
 * 折线图需要原始数值（不是格式化文本），因此样本与视图分开暴露：
 * 同一批样本喂给 presentation.trends() 得到统计视图，同时喂给
 * utils/trend-chart.js 绘制曲线，两者永远基于同一份数据。
 * @param {string} [windowKey] LAST_HOUR | LAST_SIX_HOURS | LAST_DAY
 * @param {number} [limit] 页大小，1..1000
 * @returns {Promise<Object[]>} 升序的原始遥测样本
 */
async function loadTrendSamples(windowKey, limit) {
  const safeLimit = Math.min(Math.max(Number(limit) || DEFAULT_TREND_LIMIT, 1), MAX_SAMPLE_LIMIT)
  const to = Date.now()
  const from = to - hoursToMillis(presentation.windowHours(windowKey || 'LAST_HOUR'))
  const query = {
    from: rfc3339Utc(from),
    to: rfc3339Utc(to),
    limit: safeLimit,
    order: 'desc',
  }
  const page = await get('/api/v1/devices/' + deviceId + '/telemetry', { query })
  return (page.items || []).slice().reverse()
}

/**
 * 加载趋势视图（统计 + 图例 + 文案）。
 * @param {string} [windowKey] 窗口 key
 * @param {number} [limit] 页大小
 * @returns {Promise<Object>} TrendsView
 */
async function loadTrends(windowKey, limit) {
  const points = await loadTrendSamples(windowKey, limit)
  return presentation.trends(points, windowKey || 'LAST_HOUR')
}

/**
 * 加载告警列表。
 * 筛选在客户端做：契约的 alerts 查询没有 state 参数，一次请求 + 本地切标签
 * 可以避免每切一次标签就多打一次请求。
 * @param {string} [filterKey] all | fire_warning | suspect | recovered
 * @param {number} [limit] 页大小
 * @returns {Promise<Object>} AlertsView
 */
async function loadAlerts(filterKey, limit) {
  const safeLimit = Math.min(Math.max(Number(limit) || DEFAULT_ALERT_LIMIT, 1), MAX_SAMPLE_LIMIT)
  const page = await get('/api/v1/devices/' + deviceId + '/alerts', { query: { limit: safeLimit } })
  return presentation.alerts(page.items || [], filterKey || 'all')
}

/** 加载阈值与设备确认状态 */
async function loadSettings() {
  const thresholds = await get('/api/v1/devices/' + deviceId + '/thresholds')
  return presentation.settings(thresholds)
}

/**
 * 读取命令生命周期。
 * 入队响应永远是 pending，这是唯一能观测到设备 ack 的途径。
 * @param {string} requestId 入队返回的命令 ID
 */
async function loadCommandStatus(requestId) {
  const status = await get('/api/v1/devices/' + deviceId + '/commands/' + encodeURIComponent(requestId))
  return presentation.commandStatus(status)
}

/** 下发静音/恢复：静音不清除 localAlarm，也不停止采样 */
async function setMuted(muted) {
  const accepted = await send('/api/v1/devices/' + deviceId + '/commands/mute', 'POST', { muted: !!muted })
  return presentation.commandAccepted(accepted)
}

/**
 * 校验并下发阈值。
 * 超范围在本地就抛错，用户看到的是契约范围而不是一次服务端 422。
 * @param {{temperatureHighC: number, humidityHighRh: number, gasHighPpm: number}} update 三个字段均为契约必填
 */
async function updateThresholds(update) {
  presentation.validate(update)
  const accepted = await send('/api/v1/devices/' + deviceId + '/thresholds', 'PUT', update)
  return presentation.commandAccepted(accepted)
}

/** 429 / 5xx / 网络中断属于可重试：命令可能仍会落地 */
function isRetryable(e) {
  if (!(e instanceof ApiError)) return true
  return e.statusCode === 0 || e.statusCode === 429 || e.statusCode >= 500
}

/**
 * 轮询命令终态。
 *
 * 契约/客户端类错误（401/404/结构损坏）立即抛出：把配置错误当成"还在等待"
 * 会把一个坏配置藏起来。可重试错误在预算内继续轮询。
 * @param {string} requestId 命令 ID
 * @param {number} [attempts] 轮询次数
 * @param {number} [intervalMillis] 轮询间隔
 * @returns {Promise<Object|null>} 终态视图；预算内未见终态返回 null（不得当作成功）
 */
async function awaitCommandOutcome(requestId, attempts, intervalMillis) {
  const max = attempts || DEFAULT_COMMAND_ATTEMPTS
  const interval = intervalMillis || DEFAULT_COMMAND_INTERVAL_MS
  for (let i = 0; i < max; i++) {
    await sleep(interval)
    let view = null
    try {
      view = await loadCommandStatus(requestId)
    } catch (e) {
      if (!isRetryable(e)) throw e
    }
    if (view && view.settled) return view
  }
  return null
}

module.exports = {
  DEFAULT_TREND_LIMIT,
  DEFAULT_ALERT_LIMIT,
  MAX_SAMPLE_LIMIT,
  DEFAULT_COMMAND_ATTEMPTS,
  DEFAULT_COMMAND_INTERVAL_MS,
  setDeviceId,
  currentDeviceId,
  loadDashboard,
  loadTrendSamples,
  loadTrends,
  loadAlerts,
  loadSettings,
  loadCommandStatus,
  setMuted,
  updateThresholds,
  awaitCommandOutcome,
}
