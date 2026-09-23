/**
 * REST 封装测试（services/request.js）。
 *
 * 覆盖请求失败路径：业务错误信封、无信封回退、网络中断，
 * 以及 query 拼接与幂等键请求头（控制请求必须携带）。
 */
const test = require('node:test')
const assert = require('node:assert')
const { rawRequest, ApiError } = require('../services/request.js')

test('2xx 返回响应体', async () => {
  global.wx = {
    request(opts) {
      opts.success({ statusCode: 200, data: { deviceId: 'MCU001', connectivity: 'online' } })
    },
  }
  const res = await rawRequest('/api/v1/devices/MCU001/status')
  assert.strictEqual(res.connectivity, 'online')
})

test('202 视为成功（控制命令已入队，不代表设备确认）', async () => {
  global.wx = {
    request(opts) {
      opts.success({ statusCode: 202, data: { requestId: 'r1', status: 'pending' } })
    },
  }
  const res = await rawRequest('/api/v1/devices/MCU001/commands/mute', { method: 'POST', data: { muted: true } })
  assert.strictEqual(res.status, 'pending')
})

test('业务错误按信封解析为 ApiError', async () => {
  global.wx = {
    request(opts) {
      opts.success({
        statusCode: 422,
        data: { error: { code: 'invalid_threshold', message: 'gasHighPpm must be between 1 and 999' } },
      })
    },
  }
  await assert.rejects(
    () => rawRequest('/api/v1/devices/MCU001/thresholds', { method: 'PUT', data: {} }),
    (e) => {
      assert.ok(e instanceof ApiError)
      assert.strictEqual(e.code, 'invalid_threshold')
      assert.strictEqual(e.statusCode, 422)
      return true
    }
  )
})

test('404 保留状态码，供"空态"判定使用', async () => {
  global.wx = {
    request(opts) {
      opts.success({ statusCode: 404, data: { error: { code: 'device_not_found', message: 'no sample' } } })
    },
  }
  await assert.rejects(
    () => rawRequest('/api/v1/devices/MCU001/telemetry/latest'),
    (e) => e.statusCode === 404 && e.code === 'device_not_found'
  )
})

test('缺少信封时回退 http_<status>', async () => {
  global.wx = {
    request(opts) {
      opts.success({ statusCode: 502, data: 'bad gateway' })
    },
  }
  await assert.rejects(
    () => rawRequest('/api/v1/devices/MCU001/status'),
    (e) => e.code === 'http_502' && e.statusCode === 502
  )
})

test('网络中断转为 network_error（statusCode 0，可重试）', async () => {
  global.wx = {
    request(opts) {
      opts.fail({ errMsg: 'request:fail timeout' })
    },
  }
  await assert.rejects(
    () => rawRequest('/api/v1/devices/MCU001/status'),
    (e) => e.code === 'network_error' && e.statusCode === 0
  )
})

test('query 被编码拼接，幂等键与 Content-Type 合并到请求头', async () => {
  let captured = null
  global.wx = {
    request(opts) {
      captured = opts
      opts.success({ statusCode: 200, data: {} })
    },
  }
  await rawRequest('/api/v1/devices/MCU001/telemetry', {
    query: { from: '2026-09-23T06:00:00Z', to: '2026-09-23T07:00:00Z', limit: 200, order: 'desc' },
    header: { 'Idempotency-Key': 'test-key' },
  })
  assert.ok(captured.url.indexOf('/api/v1/devices/MCU001/telemetry?from=') !== -1)
  assert.ok(captured.url.indexOf('order=desc') !== -1)
  assert.ok(captured.url.indexOf('limit=200') !== -1)
  assert.strictEqual(captured.header['Idempotency-Key'], 'test-key')
  assert.strictEqual(captured.header['Content-Type'], 'application/json; charset=utf-8')
  assert.strictEqual(captured.method, 'GET')
})
