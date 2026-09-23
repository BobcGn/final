/**
 * 设备 API 门面：真实后端与 Mock 自动切换。
 *
 * 切换逻辑：config/env.js 的 useMock 为 true 时全部走本地 Mock；
 * 为 false 时走真实后端（services/request.js 的 rawRequest）。
 * 页面只依赖本文件导出的函数，不感知数据来源。
 */
const { env } = require('../config/env.js')
const { rawRequest } = require('./request.js')
const mock = require('./mock/mock.js')
const { genIdempotencyKey } = require('../utils/helpers.js')

/**
 * 统一调用入口。
 * @param {string} path 以 /api/v1 开头的路径
 * @param {Object} [options] { method, data, header, query }
 */
async function call(path, options = {}) {
  if (env.useMock) return mock.handle(path, options)
  return rawRequest(path, options)
}

/** 设备在线与告警状态：GET /api/v1/devices/{id}/status */
function getStatus(deviceId) {
  return call('/api/v1/devices/' + deviceId + '/status')
}

/** 最新有效遥测：GET /api/v1/devices/{id}/telemetry/latest */
function getLatestTelemetry(deviceId) {
  return call('/api/v1/devices/' + deviceId + '/telemetry/latest')
}

/** 历史遥测分页：GET /api/v1/devices/{id}/telemetry */
function getTelemetryHistory(deviceId, query) {
  return call('/api/v1/devices/' + deviceId + '/telemetry', { query })
}

/** 历史告警分页：GET /api/v1/devices/{id}/alerts */
function getAlerts(deviceId, query) {
  return call('/api/v1/devices/' + deviceId + '/alerts', { query })
}

/** 当前阈值：GET /api/v1/devices/{id}/thresholds */
function getThresholds(deviceId) {
  return call('/api/v1/devices/' + deviceId + '/thresholds')
}

/**
 * 下发阈值：PUT /api/v1/devices/{id}/thresholds
 * 契约要求携带 Idempotency-Key；202 只表示命令已被接受。
 */
function putThresholds(deviceId, payload) {
  return call('/api/v1/devices/' + deviceId + '/thresholds', {
    method: 'PUT',
    data: payload,
    header: { 'Idempotency-Key': genIdempotencyKey() },
  })
}

module.exports = {
  getStatus,
  getLatestTelemetry,
  getTelemetryHistory,
  getAlerts,
  getThresholds,
  putThresholds,
}
