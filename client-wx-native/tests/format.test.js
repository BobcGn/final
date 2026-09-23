const test = require('node:test')
const assert = require('node:assert/strict')
const {
  reading,
  percent,
  decimal,
  clockText,
  rfc3339Utc,
  parseEpochMillis,
} = require('../utils/format.js')

test('reading formats numbers with half-up rounding into integer strings', () => {
  assert.equal(reading(26.1), '26')
  assert.equal(reading(26.49), '26')
  assert.equal(reading(26.5), '27')
  assert.equal(reading(26.9), '27')
  assert.equal(reading(0.0), '0')
  assert.equal(reading(0), '0')
})

test('reading returns dash for non-finite or empty values', () => {
  assert.equal(reading(null), '--')
  assert.equal(reading(undefined), '--')
  assert.equal(reading(NaN), '--')
  assert.equal(reading(Infinity), '--')
  assert.equal(reading(-Infinity), '--')
  assert.equal(reading('not-a-number'), '--')
})

test('percent scales values to 0-100 range with clamping and half-up rounding', () => {
  assert.equal(percent(0, 80), 0)
  assert.equal(percent(20, 80), 25)
  assert.equal(percent(40, 80), 50)
  assert.equal(percent(80, 80), 100)
  assert.equal(percent(120, 80), 100) // 超出夹持
  assert.equal(percent(-5, 80), 0) // 负数夹持
  assert.equal(percent(500, 999), 50)
})

test('percent returns 0 for invalid inputs', () => {
  assert.equal(percent(null, 80), 0)
  assert.equal(percent(undefined, 80), 0)
  assert.equal(percent(NaN, 80), 0)
  assert.equal(percent(10, 0), 0)
})

test('decimal formats up to one decimal place with trailing .0 removed', () => {
  assert.equal(decimal(26.14), '26.1')
  assert.equal(decimal(26.16), '26.2')
  assert.equal(decimal(26.0), '26')
  assert.equal(decimal(0), '0')
  assert.equal(decimal(null), '--')
  assert.equal(decimal(NaN), '--')
})

test('clockText extracts HH:mm:ss from RFC 3339 timestamps', () => {
  assert.equal(clockText('2026-09-23T14:32:05Z'), '14:32:05')
  assert.equal(clockText('2026-09-23T08:15:30.123+08:00'), '08:15:30')
  assert.equal(clockText('14:32:05'), '14:32:05')
  assert.equal(clockText(null), '--')
  assert.equal(clockText(''), '--')
})

test('rfc3339Utc and parseEpochMillis roundtrip', () => {
  const now = 1790172000000 // 2026-09-23T14:00:00.000Z
  const iso = rfc3339Utc(now)
  assert.ok(iso.includes('Z'))
  const parsed = parseEpochMillis(iso)
  assert.equal(parsed, now)
  assert.equal(parseEpochMillis(null), null)
  assert.equal(parseEpochMillis('invalid-time'), null)
})
