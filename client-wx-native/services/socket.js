/**
 * WebSocket 实时层：GET /ws/v1/devices/{deviceId}/telemetry（契约 §10）。
 *
 * 契约要点与对应实现：
 * - 事件 Envelope：{ type, eventId, occurredAt, deviceId, data }；
 * - 服务端发 ping、客户端回 pong（handleMessage 内处理）；
 * - 同一 eventId 必须去重（isDuplicate，保留最近 200 个）；
 * - WebSocket 只做实时增量、不补历史：重连后先回调 onResync 让页面走 REST 补数；
 * - 慢客户端会被服务端断开，因此断开即重连，退避 1s→15s。
 *
 * Mock 模式（env.useMock = true）不会真的建连，而是由本地定时器按同样的
 * Envelope 格式投递事件，保证页面代码在两种模式下完全一致。
 */
const { env } = require('../config/env.js')
const mock = require('./mock/mock.js')
const { genIdempotencyKey } = require('../utils/helpers.js')

/** 服务端事件类型（契约 §10） */
const EVENT_TYPES = [
  'telemetry.updated',
  'device.status_changed',
  'alert.state_changed',
  'command.status_changed',
  'thresholds.confirmed',
]

/** 本地连接状态事件（非契约字段，仅供界面提示） */
const CONNECTION_EVENT = 'connection.changed'

const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 15000
const IDLE_TIMEOUT_MS = 40000
const SEEN_EVENT_LIMIT = 200
/** 断线期间 REST 兜底轮询间隔（真机弱网下避免界面长时间不刷新） */
const FALLBACK_POLL_MS = 20000

const listeners = {}
const seenEventIds = []

let task = null
let currentDeviceId = ''
let connectionStatus = 'closed'
let retryCount = 0
let reconnectTimer = null
let idleTimer = null
let fallbackTimer = null
let mockTimers = []
let mockConfirmedVersion = null
let mockAlertState = null
let resyncHandler = null

/* ---------- 事件订阅 ---------- */

/**
 * 订阅事件。
 * @param {string} type 事件类型，或 CONNECTION_EVENT
 * @param {Function} handler 处理函数，入参为 Envelope
 * @returns {Function} 取消订阅
 */
function on(type, handler) {
  if (!listeners[type]) listeners[type] = []
  listeners[type].push(handler)
  return function off() {
    listeners[type] = (listeners[type] || []).filter((fn) => fn !== handler)
  }
}

function emit(event) {
  const fns = listeners[event.type] || []
  fns.forEach((fn) => {
    try {
      fn(event)
    } catch (e) {
      // 单个订阅者异常不影响其他订阅者
    }
  })
}

/** 更新连接状态并通过本地事件通知界面 */
function setStatus(next) {
  if (connectionStatus === next) return
  connectionStatus = next
  emit({ type: CONNECTION_EVENT, status: next, occurredAt: new Date().toISOString() })
}

/** 订阅方可通过本函数自行广播（Mock 与测试使用） */
function publish(event) {
  emit(event)
}

/** eventId 去重：契约要求客户端不能假定事件不重复 */
function isDuplicate(eventId) {
  if (!eventId) return false
  if (seenEventIds.indexOf(eventId) !== -1) return true
  seenEventIds.push(eventId)
  if (seenEventIds.length > SEEN_EVENT_LIMIT) seenEventIds.shift()
  return false
}

/** 包装成契约 §10 的 Envelope */
function envelope(type, data) {
  return {
    type,
    eventId: genIdempotencyKey(),
    occurredAt: new Date().toISOString(),
    deviceId: currentDeviceId,
    data,
  }
}

/* ---------- 心跳与看门狗 ---------- */

function resetIdleTimer() {
  if (idleTimer) clearTimeout(idleTimer)
  idleTimer = setTimeout(() => {
    // 超过 IDLE_TIMEOUT_MS 没有任何消息，认为连接已死，主动重连
    closeSocket()
    scheduleReconnect()
  }, IDLE_TIMEOUT_MS)
}

function clearIdleTimer() {
  if (idleTimer) {
    clearTimeout(idleTimer)
    idleTimer = null
  }
}

/* ---------- 真实 WebSocket ---------- */

function buildWsUrl(deviceId) {
  return env.wsUrl + '/ws/v1/devices/' + encodeURIComponent(deviceId) + '/telemetry'
}

function handleMessage(raw) {
  resetIdleTimer()
  const text = typeof raw === 'string' ? raw : ''
  // 契约：服务端发 ping，客户端回 pong
  if (text === 'ping') {
    try {
      if (task) task.send({ data: 'pong' })
    } catch (e) {
      // 发送失败交由 onError/onClose 处理
    }
    return
  }
  let payload = null
  try {
    payload = JSON.parse(text)
  } catch (e) {
    return
  }
  if (!payload || !payload.type) return
  if (EVENT_TYPES.indexOf(payload.type) === -1) return
  if (isDuplicate(payload.eventId)) return
  emit(payload)
}

function openSocket() {
  setStatus(retryCount > 0 ? 'reconnecting' : 'connecting')
  try {
    task = wx.connectSocket({ url: buildWsUrl(currentDeviceId) })
  } catch (e) {
    task = null
    scheduleReconnect()
    return
  }
  if (!task) return

  task.onOpen(() => {
    const isReconnect = retryCount > 0
    retryCount = 0
    setStatus('open')
    resetIdleTimer()
    startFallbackPoll()
    // 契约：WebSocket 不补历史，重连后先 REST 补数再接收增量
    if (isReconnect && typeof resyncHandler === 'function') resyncHandler()
  })

  task.onMessage((res) => handleMessage(res && res.data))

  task.onError(() => {
    closeSocket()
    scheduleReconnect()
  })

  task.onClose(() => {
    clearIdleTimer()
    stopFallbackPoll()
    closeSocket()
    scheduleReconnect()
  })
}

function closeSocket() {
  if (!task) return
  const t = task
  task = null
  try {
    t.close({ code: 1000, reason: 'client close' })
  } catch (e) {
    // 已关闭时忽略
  }
}

function scheduleReconnect() {
  if (!currentDeviceId) return
  if (reconnectTimer) return
  setStatus('reconnecting')
  const delay = Math.min(RECONNECT_MAX_MS, RECONNECT_BASE_MS * Math.pow(2, retryCount))
  retryCount += 1
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null
    openSocket()
  }, delay)
}

/** 断线期间的兜底轮询：仅用于提示与补数，不替代实时流 */
function startFallbackPoll() {
  if (fallbackTimer || typeof resyncHandler !== 'function') return
  fallbackTimer = setInterval(resyncHandler, FALLBACK_POLL_MS)
}

function stopFallbackPoll() {
  if (fallbackTimer) {
    clearInterval(fallbackTimer)
    fallbackTimer = null
  }
}

/* ---------- Mock 实时流 ---------- */

function stopMockFeed() {
  mockTimers.forEach((t) => clearInterval(t))
  mockTimers = []
  mockConfirmedVersion = null
  mockAlertState = null
}

/**
 * Mock 模式下的本地事件投递：
 * 1) 每 2 秒投递 telemetry.updated；
 * 2) 每 1 秒比对阈值版本，设备确认后投递 thresholds.confirmed + command.status_changed；
 * 3) 告警状态变化时投递 alert.state_changed。
 */
function startMockFeed(deviceId) {
  stopMockFeed()
  setStatus('open')

  mockTimers.push(
    setInterval(async () => {
      try {
        const latest = await mock.handle('/api/v1/devices/' + deviceId + '/telemetry/latest')
        publish(
          envelope('telemetry.updated', {
            sequence: latest.sequence,
            temperatureC: latest.temperatureC,
            humidityRh: latest.humidityRh,
            gasAdcFiltered: latest.gasAdcFiltered,
            gasPpm: latest.gasPpm,
            localAlarm: latest.localAlarm,
          })
        )

        const causes = (latest.alarmCauses || []).join(',')
        if (causes !== mockAlertState) {
          mockAlertState = causes
          publish(
            envelope('alert.state_changed', {
              alarmState: latest.localAlarm ? 'suspect' : 'normal',
              alarmCauses: latest.alarmCauses || [],
            })
          )
        }
      } catch (e) {
        // Mock 出错不影响页面
      }
    }, 2000)
  )

  mockTimers.push(
    setInterval(async () => {
      try {
        const t = await mock.handle('/api/v1/devices/' + deviceId + '/thresholds')
        if (mockConfirmedVersion === null) {
          mockConfirmedVersion = t.confirmedVersion
          return
        }
        if (t.confirmedVersion !== mockConfirmedVersion) {
          mockConfirmedVersion = t.confirmedVersion
          publish(
            envelope('thresholds.confirmed', {
              desiredVersion: t.desiredVersion,
              confirmedVersion: t.confirmedVersion,
            })
          )
          publish(envelope('command.status_changed', { status: 'applied', desiredVersion: t.desiredVersion }))
        }
      } catch (e) {
        // 忽略
      }
    }, 1000)
  )
}

/* ---------- 对外 API ---------- */

/**
 * 建立实时连接。
 * @param {string} deviceId 设备编号
 * @param {Object} [options] { onResync } 重连/兜底时回调，供页面走 REST 补数
 */
function connect(deviceId, options = {}) {
  resyncHandler = options.onResync || resyncHandler
  if (currentDeviceId === deviceId && (connectionStatus === 'open' || connectionStatus === 'connecting')) return
  disconnect()
  currentDeviceId = deviceId
  retryCount = 0
  if (env.useMock) {
    startMockFeed(deviceId)
    return
  }
  openSocket()
}

/** 断开连接并清理所有定时器（页面卸载时调用） */
function disconnect() {
  currentDeviceId = ''
  clearIdleTimer()
  stopFallbackPoll()
  stopMockFeed()
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
  closeSocket()
  setStatus('closed')
  retryCount = 0
}

function getConnectionStatus() {
  return connectionStatus
}

module.exports = {
  EVENT_TYPES,
  CONNECTION_EVENT,
  connect,
  disconnect,
  on,
  publish,
  getConnectionStatus,
}
