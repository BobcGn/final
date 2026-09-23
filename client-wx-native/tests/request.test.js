const test = require('node:test')
const assert = require('node:assert/strict')
const { rawRequest, ApiError } = require('../services/request.js')

test('ApiError holds code, message, and statusCode', () => {
  const err = new ApiError('invalid_threshold', 'Out of range', 422)
  assert.equal(err.code, 'invalid_threshold')
  assert.equal(err.message, 'Out of range')
  assert.equal(err.statusCode, 422)
  assert.ok(err instanceof Error)
})

test('rawRequest resolves on 2xx status', async () => {
  const origWx = global.wx
  global.wx = {
    request(opts) {
      opts.success({
        statusCode: 200,
        data: { ok: true },
      })
    },
  }

  try {
    const res = await rawRequest('/api/v1/test', { query: { a: 1, b: 'hello' } })
    assert.deepEqual(res, { ok: true })
  } finally {
    global.wx = origWx
  }
})

test('rawRequest parses backend error envelope on non-2xx status', async () => {
  const origWx = global.wx
  global.wx = {
    request(opts) {
      opts.success({
        statusCode: 422,
        data: {
          error: {
            code: 'invalid_threshold',
            message: 'temperatureHighC must be between 0 and 80',
          },
        },
      })
    },
  }

  try {
    await assert.rejects(
      async () => {
        await rawRequest('/api/v1/devices/MCU001/thresholds', { method: 'PUT' })
      },
      (err) => {
        assert.equal(err.code, 'invalid_threshold')
        assert.equal(err.statusCode, 422)
        assert.equal(err.message, 'temperatureHighC must be between 0 and 80')
        return true
      }
    )
  } finally {
    global.wx = origWx
  }
})

test('rawRequest falls back gracefully when error body has no envelope', async () => {
  const origWx = global.wx
  global.wx = {
    request(opts) {
      opts.success({
        statusCode: 502,
        data: '<html>Bad Gateway</html>',
      })
    },
  }

  try {
    await assert.rejects(
      async () => {
        await rawRequest('/api/v1/test')
      },
      (err) => {
        assert.equal(err.code, 'http_502')
        assert.equal(err.statusCode, 502)
        return true
      }
    )
  } finally {
    global.wx = origWx
  }
})

test('rawRequest rejects with network_error when wx.request fails', async () => {
  const origWx = global.wx
  global.wx = {
    request(opts) {
      opts.fail({ errMsg: 'request:fail connection timeout' })
    },
  }

  try {
    await assert.rejects(
      async () => {
        await rawRequest('/api/v1/test')
      },
      (err) => {
        assert.equal(err.code, 'network_error')
        assert.equal(err.statusCode, 0)
        assert.ok(err.message.includes('timeout'))
        return true
      }
    )
  } finally {
    global.wx = origWx
  }
})
