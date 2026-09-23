/**
 * Mock 数据层。
 *
 * 字段与枚举严格对齐后端契约（backend/docs/api.md + docs/api/openapi.yaml），
 * 并与 client-kmp 共享层的 `Models.kt` 保持同一形状：
 * - 遥测的 `gasPpm` 可为 null（未标定），客户端必须显示 `--` 而不是 0；
 * - 告警证据用 ADC 码（`gasAdcRise` / `gasAdcRiseThreshold`），不是 ppm；
 * - 阈值有三个字段（温度/湿度/气体）；
 * - 控制命令入队只返回 pending，设备确认要通过 `GET /commands/{requestId}` 观察。
 *
 * 后端除 `/healthz` 外仍返回 501，因此前端在后端就绪前使用本模块开发；
 * 切到真实后端只需把 config/env.js 的 useMock 置为 false，页面零改动。
 */
const { ApiError } = require('../request.js')

const state = {
  seq: 41,
  bootId: 'boot-a1',
  temperatureC: 27.6,
  humidityRh: 60.5,
  gasPpm: 25.0,
  gasCalibrated: true,
  sensorFault: false,
  buzzerMuted: false,
  network: 'online',
  thresholds: { temperatureHighC: 30.0, humidityHighRh: 80.0, gasHighPpm: 20.0 },
  desiredVersion: 4,
  confirmedVersion: 4,
  confirmationState: 'confirmed',
  updatedAt: '2026-09-23T07:10:00Z',
}

/** 命令生命周期：requestId -> CommandStatus */
const commands = {}

/* ---------- 内部工具 ---------- */

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
function rand(min, max) {
  return min + Math.random() * (max - min)
}
function clamp(v, min, max) {
  return Math.min(max, Math.max(min, v))
}
function round1(v) {
  return Math.round(v * 10) / 10
}
function nowIso() {
  return new Date().toISOString()
}
function newRequestId() {
  return 'mock-' + Date.now().toString(36) + '-' + Math.random().toString(16).slice(2, 8)
}

/** 模拟一次新采样：温湿度/气体随机游走（范围控制在安全区间附近） */
function advance() {
  state.seq += 1
  state.temperatureC = clamp(state.temperatureC + rand(-0.15, 0.15), 24.5, 30.5)
  state.humidityRh = clamp(state.humidityRh + rand(-0.8, 0.8), 48, 72)
  state.gasPpm = clamp(state.gasPpm + rand(-1.2, 1.2), 14, 42)
}

/** 与阈值比较得出本地报警原因（ADC 码上升量是告警证据的口径） */
function computeCauses() {
  const causes = []
  if (round1(state.gasPpm) > state.thresholds.gasHighPpm) causes.push('gas_high')
  if (round1(state.temperatureC) > state.thresholds.temperatureHighC) causes.push('temperature_high')
  if (round1(state.humidityRh) > state.thresholds.humidityHighRh) causes.push('humidity_high')
  return causes
}

/** 构造一条符合契约的遥测对象 */
function buildTelemetry(advanceSample) {
  if (advanceSample) advance()
  const causes = computeCauses()
  const ts = nowIso()
  return {
    deviceId: 'MCU001',
    sequence: state.seq,
    bootId: state.bootId,
    timestamp: ts,
    receivedAt: ts,
    temperatureC: round1(state.temperatureC),
    humidityRh: round1(state.humidityRh),
    gasAdcRaw: Math.round(state.gasPpm * 52),
    gasAdcFiltered: Math.round(state.gasPpm * 52) - 12,
    gasPpm: state.gasCalibrated ? round1(state.gasPpm) : null,
    gasCalibrated: state.gasCalibrated,
    localAlarm: causes.length > 0,
    alarmCauses: causes,
    buzzerMuted: state.buzzerMuted,
    network: state.network,
    sensorFault: state.sensorFault,
  }
}

/** 记录一条命令并按设备处理时延推进其生命周期 */
function enqueueCommand(type, extra) {
  const requestId = newRequestId()
  const acceptedAt = nowIso()
  const record = {
    requestId,
    deviceId: 'MCU001',
    type,
    state: 'published',
    acceptedAt,
    completedAt: null,
    expiresAt: new Date(Date.now() + 30000).toISOString(),
    desiredVersion: null,
    confirmedVersion: null,
    errorCode: null,
  }
  Object.assign(record, extra || {})
  commands[requestId] = record

  // 模拟设备执行：~2.4 秒后 applied（>1.5s 轮询间隔，页面能看到一次等待）
  setTimeout(() => {
    const current = commands[requestId]
    if (!current) return
    current.state = 'applied'
    current.completedAt = nowIso()
    if (type === 'set_thresholds') {
      state.confirmedVersion = state.desiredVersion
      state.confirmationState = 'confirmed'
      current.confirmedVersion = state.confirmedVersion
    }
    if (type === 'set_mute') {
      current.confirmedVersion = state.confirmedVersion
    }
  }, 2400)

  return record
}

/* ---------- 各路由处理 ---------- */

function getStatus() {
  const causes = computeCauses()
  return {
    deviceId: 'MCU001',
    connectivity: 'online',
    // 契约的告警状态没有 acknowledged：只有设备本地判断与复合预警
    alarmState: causes.length > 0 ? 'suspect' : 'normal',
    localAlarm: causes.length > 0,
    buzzerMuted: state.buzzerMuted,
    lastSeenAt: nowIso(),
    offlineAfterSeconds: 15,
    thresholdVersion: {
      desired: state.desiredVersion,
      confirmed: state.confirmedVersion,
    },
  }
}

function getLatest() {
  return buildTelemetry(true)
}

/**
 * 历史遥测：支持契约的 from/to/limit/order。
 * order=desc 返回最近一页（客户端会反转为升序供统计使用）。
 */
function getHistory(query) {
  const q = query || {}
  const limit = Math.min(Number(q.limit) || 200, 1000)
  const desc = q.order === 'desc'
  const stepMs = 30 * 1000
  const to = q.to ? Date.parse(q.to) : Date.now()
  const from = q.from ? Date.parse(q.from) : to - 60 * 60 * 1000

  const items = []
  let temp = state.temperatureC
  let hum = state.humidityRh
  let gas = state.gasPpm
  for (let i = 0; i < limit; i++) {
    // 从 to 往前每 30 秒一个样本，越旧的值波动越大（确定性随机游走）
    const ts = to - i * stepMs
    if (ts < from) break
    temp = clamp(temp + rand(-0.2, 0.2), 24.5, 30.5)
    hum = clamp(hum + rand(-1, 1), 48, 72)
    gas = clamp(gas + rand(-1.5, 1.5), 12, 45)
    items.push({
      deviceId: 'MCU001',
      sequence: state.seq - i,
      bootId: state.bootId,
      timestamp: new Date(ts).toISOString(),
      receivedAt: new Date(ts + 800).toISOString(),
      temperatureC: round1(temp),
      humidityRh: round1(hum),
      gasAdcRaw: Math.round(gas * 52),
      gasAdcFiltered: Math.round(gas * 52) - 12,
      gasPpm: round1(gas),
      localAlarm: false,
      alarmCauses: [],
      buzzerMuted: state.buzzerMuted,
      network: 'online',
      sensorFault: false,
    })
  }
  // 循环内是按 to 往前生成，天然为降序；只有请求升序时才反转
  if (!desc) items.reverse()
  return { items, nextCursor: null }
}

function getAlerts(query) {
  const limit = Math.min(Number((query && query.limit) || 50), 1000)
  const all = [
    {
      id: '01K5H7T7T4J2MYE7Y0BR1ZBQ0Q',
      deviceId: 'MCU001',
      state: 'fire_warning',
      startedAt: new Date(Date.now() - 42 * 60 * 1000).toISOString(),
      endedAt: null,
      evidence: {
        gasAdcRise: 1870,
        gasAdcRiseThreshold: 1500,
        temperatureRateCPerMinute: 4.2,
        temperatureRateThresholdCPerMinute: 3.0,
        sampleCount: 8,
        windowSeconds: 60,
      },
    },
    {
      id: '01K5H6Q2M9X4T8ZP3V7C1NDQKR',
      deviceId: 'MCU001',
      state: 'suspect',
      startedAt: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
      endedAt: new Date(Date.now() - 2.6 * 60 * 60 * 1000).toISOString(),
      evidence: {
        gasAdcRise: 980,
        gasAdcRiseThreshold: 1500,
        temperatureRateCPerMinute: 1.6,
        temperatureRateThresholdCPerMinute: 3.0,
        sampleCount: 6,
        windowSeconds: 60,
      },
    },
    {
      id: '01K5H4B8C1D2E3F4G5H6J7K8L9',
      deviceId: 'MCU001',
      state: 'recovered',
      startedAt: new Date(Date.now() - 26 * 60 * 60 * 1000).toISOString(),
      endedAt: new Date(Date.now() - 25 * 60 * 60 * 1000).toISOString(),
      evidence: {
        gasAdcRise: 1620,
        gasAdcRiseThreshold: 1500,
        temperatureRateCPerMinute: 3.4,
        temperatureRateThresholdCPerMinute: 3.0,
        sampleCount: 9,
        windowSeconds: 60,
      },
    },
  ]
  return { items: all.slice(0, limit), nextCursor: null }
}

function getThresholds() {
  return {
    desiredVersion: state.desiredVersion,
    confirmedVersion: state.confirmedVersion,
    temperatureHighC: state.thresholds.temperatureHighC,
    humidityHighRh: state.thresholds.humidityHighRh,
    gasHighPpm: state.thresholds.gasHighPpm,
    updatedAt: state.updatedAt,
    confirmationState: state.confirmationState,
  }
}

function putThresholds(data) {
  const { temperatureHighC, humidityHighRh, gasHighPpm } = data || {}
  if (typeof temperatureHighC !== 'number' || temperatureHighC < 0 || temperatureHighC > 80) {
    throw new ApiError('invalid_threshold', 'temperatureHighC must be between 0 and 80', 422)
  }
  if (typeof humidityHighRh !== 'number' || humidityHighRh < 0 || humidityHighRh > 100) {
    throw new ApiError('invalid_threshold', 'humidityHighRh must be between 0 and 100', 422)
  }
  if (typeof gasHighPpm !== 'number' || gasHighPpm < 1 || gasHighPpm > 999) {
    throw new ApiError('invalid_threshold', 'gasHighPpm must be between 1 and 999', 422)
  }
  state.thresholds = { temperatureHighC, humidityHighRh, gasHighPpm }
  state.desiredVersion += 1
  state.confirmationState = 'pending'
  state.updatedAt = nowIso()

  const record = enqueueCommand('set_thresholds', { desiredVersion: state.desiredVersion })
  return {
    requestId: record.requestId,
    status: 'pending',
    desiredVersion: state.desiredVersion,
    expiresAt: record.expiresAt,
  }
}

function mute(data) {
  state.buzzerMuted = !!(data && data.muted)
  const record = enqueueCommand('set_mute', { desiredVersion: state.desiredVersion })
  return { requestId: record.requestId, status: 'pending', expiresAt: record.expiresAt }
}

function getCommandStatus(requestId) {
  const record = commands[requestId]
  if (!record) {
    throw new ApiError('not_found', 'command not found: ' + requestId, 404)
  }
  return record
}

/* ---------- 路由分发 ---------- */

/**
 * 按 REST 路径与方法分发到对应 Mock 处理器。
 * @param {string} path 如 /api/v1/devices/MCU001/telemetry/latest
 * @param {Object} [options] { method, data, query }
 */
async function handle(path, options = {}) {
  await delay(150 + Math.random() * 250)
  const method = (options.method || 'GET').toUpperCase()
  const m = path.match(/^\/api\/v1\/devices\/([^/]+)\/(.+)$/)
  if (!m) throw new ApiError('not_implemented', 'Mock 未覆盖该路由: ' + path, 501)
  const sub = m[2]

  if (sub === 'status' && method === 'GET') return getStatus()
  if (sub === 'telemetry/latest' && method === 'GET') return getLatest()
  if (sub === 'telemetry' && method === 'GET') return getHistory(options.query)
  if (sub === 'alerts' && method === 'GET') return getAlerts(options.query)
  if (sub === 'thresholds' && method === 'GET') return getThresholds()
  if (sub === 'thresholds' && method === 'PUT') return putThresholds(options.data)
  if (sub === 'commands/mute' && method === 'POST') return mute(options.data)
  if (sub.indexOf('commands/') === 0 && method === 'GET') return getCommandStatus(sub.slice('commands/'.length))
  throw new ApiError('not_implemented', 'Mock 未覆盖该路由: ' + method + ' ' + path, 501)
}

module.exports = { handle }
