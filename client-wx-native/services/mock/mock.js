/**
 * Mock 数据层。
 *
 * 响应字段严格对齐接口契约（docs/api/openapi.yaml v2.0.0）。
 * 切换真实后端时页面代码零改动，只需把 config/env.js 的 useMock 置为 false。
 *
 * 行为模拟：
 * - 遥测数据按随机游走演进，每次请求 latest 相当于一次新采样；
 * - 历史遥测每隔 9 个点模拟一次 gasPpm: null（未标定），用于检验折线图分段与空态统计；
 * - 告警证据采用 ADC 码与温升速率，无 acknowledged 伪状态；
 * - 阈值下发支持三字段（含 humidityHighRh）；
 * - 支持 GET /commands/{requestId} 轮询，模拟设备延时确认（published -> applied）；
 * - 严格无远程静音能力。
 */
const { ApiError } = require('../request.js')

const state = {
  seq: 41,
  temperatureC: 27.6,
  humidityRh: 60.5,
  gasPpm: 25.0,
  thresholds: {
    temperatureHighC: 30.0,
    humidityHighRh: 60.0,
    gasHighPpm: 80.0,
  },
  desiredVersion: 4,
  confirmedVersion: 4,
  confirmationState: 'confirmed',
  updatedAt: '2026-09-18T11:10:00Z',
  commands: {},
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
    // 每 9 条样本模拟一次未标定气体（gasPpm 为 null）
    const isUncalibrated = i % 9 === 0
    items.push({
      deviceId,
      sequence: state.seq - i,
      timestamp: ts,
      receivedAt: ts,
      temperatureC: round1(temp),
      humidityRh: round1(hum),
      gasAdcRaw: Math.round(gas * 52),
      gasAdcFiltered: Math.round(gas * 52) - 12,
      gasPpm: isUncalibrated ? null : round1(gas),
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
        endedAt: minutesAgoIso(47),
        evidence: {
          gasAdcRise: 96,
          gasAdcRiseThreshold: 150,
          temperatureRateCPerMinute: 1.1,
          temperatureRateThresholdCPerMinute: 3.0,
          sampleCount: 6,
          windowSeconds: 30,
        },
      },
      {
        id: 'mock-alert-002',
        deviceId,
        state: 'suspect',
        startedAt: minutesAgoIso(180),
        endedAt: null,
        evidence: {
          gasAdcRise: 165,
          gasAdcRiseThreshold: 150,
          temperatureRateCPerMinute: 2.4,
          temperatureRateThresholdCPerMinute: 3.0,
          sampleCount: 7,
          windowSeconds: 30,
        },
      },
      {
        id: 'mock-alert-001',
        deviceId,
        state: 'fire_warning',
        startedAt: minutesAgoIso(1440),
        endedAt: null,
        evidence: {
          gasAdcRise: 187,
          gasAdcRiseThreshold: 150,
          temperatureRateCPerMinute: 4.2,
          temperatureRateThresholdCPerMinute: 3.0,
          sampleCount: 8,
          windowSeconds: 30,
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
    humidityHighRh: state.thresholds.humidityHighRh,
    gasHighPpm: state.thresholds.gasHighPpm,
    updatedAt: state.updatedAt,
    confirmationState: state.confirmationState,
  }
}

function putThresholds(deviceId, data = {}) {
  const { temperatureHighC, humidityHighRh, gasHighPpm } = data
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

  const requestId = 'mock-req-' + state.desiredVersion
  state.commands[requestId] = {
    requestId,
    deviceId,
    type: 'set_thresholds',
    state: 'published',
    acceptedAt: nowIso(),
    completedAt: null,
    desiredVersion: state.desiredVersion,
    confirmedVersion: state.confirmedVersion,
    errorCode: null,
  }

  // 模拟设备 1.5 秒后回 ack applied
  setTimeout(() => {
    if (state.commands[requestId]) {
      state.commands[requestId].state = 'applied'
      state.commands[requestId].completedAt = nowIso()
      state.commands[requestId].confirmedVersion = state.desiredVersion
      state.confirmedVersion = state.desiredVersion
      state.confirmationState = 'confirmed'
    }
  }, 1500)

  return {
    requestId,
    status: 'pending',
    desiredVersion: state.desiredVersion,
    expiresAt: new Date(Date.now() + 30000).toISOString(),
  }
}

function getCommandStatus(deviceId, requestId) {
  const cmd = state.commands[requestId]
  if (!cmd) {
    throw new ApiError('not_found', '命令未找到: ' + requestId, 404)
  }
  return cmd
}

/* ---------- 路由分发 ---------- */

/**
 * 按 REST 路径与方法分发到对应 Mock 处理器。
 * @param {string} path 如 /api/v1/devices/MCU001/telemetry/latest
 * @param {Object} [options] { method, data, query }
 */
async function handle(path, options = {}) {
  await delay(100 + Math.random() * 150)
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

  const cmdMatch = sub.match(/^commands\/([^/]+)$/)
  if (cmdMatch && method === 'GET') return getCommandStatus(deviceId, cmdMatch[1])

  throw new ApiError('not_implemented', 'Mock 未覆盖该路由: ' + method + ' ' + path, 501)
}

module.exports = { handle }
