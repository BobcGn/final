/**
 * Mock 数据层。
 *
 * 后端除 /healthz 外所有路由当前返回 501 not_implemented，前端在真实后端
 * 就绪前使用本模块开发。所有响应字段严格对齐接口契约（api.md draft-v1），
 * 切换真实后端时页面代码零改动，只需把 config/env.js 的 useMock 置为 false。
 *
 * 行为模拟：
 * - 遥测数据按随机游走演进，每次请求 latest 相当于一次新采样；
 * - 阈值下发后 3 秒模拟设备确认（desiredVersion 追上 confirmedVersion）；
 * - 模拟网络延迟 150-400ms。
 */
const { ApiError } = require('../request.js')

const state = {
  seq: 41,
  temperatureC: 27.6,
  humidityRh: 60.5,
  gasPpm: 25.0,
  thresholds: { temperatureHighC: 30.0, gasHighPpm: 80.0 },
  desiredVersion: 4,
  confirmedVersion: 4,
  confirmationState: 'confirmed',
  updatedAt: '2026-09-18T11:10:00Z',
}

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
function minutesAgoIso(min) {
  return new Date(Date.now() - min * 60 * 1000).toISOString()
}

/** 模拟一次新采样：温湿度/气体随机游走（范围控制在安全区间附近） */
function advance() {
  state.seq += 1
  state.temperatureC = clamp(state.temperatureC + rand(-0.15, 0.15), 24.5, 30.5)
  state.humidityRh = clamp(state.humidityRh + rand(-0.8, 0.8), 48, 72)
  state.gasPpm = clamp(state.gasPpm + rand(-1.2, 1.2), 14, 42)
}

/** 计算当前告警原因（与阈值比较） */
function computeCauses() {
  const causes = []
  if (round1(state.gasPpm) > state.thresholds.gasHighPpm) causes.push('gas_high')
  if (round1(state.temperatureC) > state.thresholds.temperatureHighC) causes.push('temperature_high')
  return causes
}

/** 构造一条符合契约的遥测对象 */
function buildTelemetry(deviceId, advanceSample) {
  if (advanceSample) advance()
  const causes = computeCauses()
  const ts = nowIso()
  return {
    deviceId,
    sequence: state.seq,
    timestamp: ts,
    receivedAt: ts,
    temperatureC: round1(state.temperatureC),
    humidityRh: round1(state.humidityRh),
    gasAdcRaw: Math.round(state.gasPpm * 52),
    gasAdcFiltered: Math.round(state.gasPpm * 52) - 12,
    gasPpm: round1(state.gasPpm),
    localAlarm: causes.length > 0,
    alarmCauses: causes,
    network: 'online',
  }
}

/* ---------- 各路由处理 ---------- */

function getStatus(deviceId) {
  const causes = computeCauses()
  return {
    deviceId,
    connectivity: 'online',
    alarmState: causes.length > 0 ? 'suspect' : 'normal',
    localAlarm: causes.length > 0,
    lastSeenAt: nowIso(),
    offlineAfterSeconds: 15,
    thresholdVersion: {
      desired: state.desiredVersion,
      confirmed: state.confirmedVersion,
    },
  }
}

function getLatest(deviceId) {
  return buildTelemetry(deviceId, true)
}

function getHistory(deviceId, query = {}) {
  const limit = Math.min(Number(query.limit) || 60, 200)
  const stepMs = 30 * 1000
  const base = Date.now()
  const items = []
  let temp = state.temperatureC
  let hum = state.humidityRh
  let gas = state.gasPpm
  for (let i = limit; i >= 1; i--) {
    temp = clamp(temp + rand(-0.2, 0.2), 24.5, 30.5)
    hum = clamp(hum + rand(-1, 1), 48, 72)
    gas = clamp(gas + rand(-1.5, 1.5), 14, 42)
    const ts = new Date(base - i * stepMs).toISOString()
    items.push({
      deviceId,
      sequence: state.seq - i,
      timestamp: ts,
      receivedAt: ts,
      temperatureC: round1(temp),
      humidityRh: round1(hum),
      gasAdcRaw: Math.round(gas * 52),
      gasAdcFiltered: Math.round(gas * 52) - 12,
      gasPpm: round1(gas),
      localAlarm: false,
      alarmCauses: [],
    })
  }
  if (query.order === 'desc') {
    items.reverse()
  }
  return { items, nextCursor: null }
}

function getAlerts(deviceId) {
  return {
    items: [
      {
        id: 'mock-alert-003',
        deviceId,
        state: 'recovered',
        startedAt: minutesAgoIso(52),
        acknowledgedAt: minutesAgoIso(50),
        endedAt: minutesAgoIso(47),
        evidence: {
          gasRise: 96.0,
          gasRiseThreshold: 150.0,
          temperatureRateCPerMinute: 1.1,
          temperatureRateThresholdCPerMinute: 3.0,
          sampleCount: 6,
        },
      },
      {
        id: 'mock-alert-002',
        deviceId,
        state: 'acknowledged',
        startedAt: minutesAgoIso(180),
        acknowledgedAt: minutesAgoIso(175),
        endedAt: null,
        evidence: {
          gasRise: 165.0,
          gasRiseThreshold: 150.0,
          temperatureRateCPerMinute: 2.4,
          temperatureRateThresholdCPerMinute: 3.0,
          sampleCount: 7,
        },
      },
      {
        id: 'mock-alert-001',
        deviceId,
        state: 'fire_warning',
        startedAt: minutesAgoIso(1440),
        acknowledgedAt: null,
        endedAt: null,
        evidence: {
          gasRise: 187.0,
          gasRiseThreshold: 150.0,
          temperatureRateCPerMinute: 4.2,
          temperatureRateThresholdCPerMinute: 3.0,
          sampleCount: 8,
        },
      },
    ],
    nextCursor: null,
  }
}

function getThresholds(deviceId) {
  return {
    desiredVersion: state.desiredVersion,
    confirmedVersion: state.confirmedVersion,
    temperatureHighC: state.thresholds.temperatureHighC,
    gasHighPpm: state.thresholds.gasHighPpm,
    updatedAt: state.updatedAt,
    confirmationState: state.confirmationState,
  }
}

function putThresholds(deviceId, data = {}) {
  const { temperatureHighC, gasHighPpm } = data
  if (typeof temperatureHighC !== 'number' || temperatureHighC < 0 || temperatureHighC > 80) {
    throw new ApiError('invalid_threshold', 'temperatureHighC must be between 0 and 80', 422)
  }
  if (typeof gasHighPpm !== 'number' || gasHighPpm < 1 || gasHighPpm > 999) {
    throw new ApiError('invalid_threshold', 'gasHighPpm must be between 1 and 999', 422)
  }
  state.thresholds = { temperatureHighC, gasHighPpm }
  state.desiredVersion += 1
  state.confirmationState = 'pending'
  state.updatedAt = nowIso()
  // 模拟设备 3 秒后回 ack（真实场景由 thresholds.confirmed WebSocket 事件驱动）
  setTimeout(() => {
    state.confirmedVersion = state.desiredVersion
    state.confirmationState = 'confirmed'
  }, 3000)
  return {
    requestId: 'mock-req-' + state.desiredVersion,
    status: 'pending',
    desiredVersion: state.desiredVersion,
    expiresAt: new Date(Date.now() + 30000).toISOString(),
  }
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
  const deviceId = m[1]
  const sub = m[2]

  if (sub === 'status' && method === 'GET') return getStatus(deviceId)
  if (sub === 'telemetry/latest' && method === 'GET') return getLatest(deviceId)
  if (sub === 'telemetry' && method === 'GET') return getHistory(deviceId, options.query)
  if (sub === 'alerts' && method === 'GET') return getAlerts(deviceId)
  if (sub === 'thresholds' && method === 'GET') return getThresholds(deviceId)
  if (sub === 'thresholds' && method === 'PUT') return putThresholds(deviceId, options.data)
  throw new ApiError('not_implemented', 'Mock 未覆盖该路由: ' + method + ' ' + path, 501)
}

module.exports = { handle }
