const test = require('node:test')
const assert = require('node:assert/strict')
const { env } = require('../config/env.js')

test('real backend is the default data source for hardware telemetry', () => {
  assert.equal(env.useMock, false)
})
