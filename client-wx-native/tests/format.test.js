/**
 * 格式化规则测试（utils/format.js）。
 *
 * 对齐 KMP `MonitoringPresentation` 的私有格式化函数：
 * 读数为整数（half-up）、百分比按契约量程、非有限值一律 `--`。
 */
const test = require('node:test')
const assert = require('node:assert')
const { reading, percent, decimal, clockText, rfc3339Utc, hoursToMillis } = require('../utils/format.js')

test('reading：读数为整数，不出现小数点', () => {
  assert.strictEqual(reading(26.1), '26')
  assert.strictEqual(reading(26.4), '26')
  assert.strictEqual(reading(26.0), '26')
  assert.strictEqual(reading(0), '0')
  assert.strictEqual(reading(61.9), '62')
})

test('reading：四舍五入为 half-up（与固件取整规则一致）', () => {
  assert.strictEqual(reading(29.5), '30')
  assert.strictEqual(reading(30.5), '31')
  assert.strictEqual(reading(0.5), '1')
})

test('reading：非有限值与空值返回 --（不能显示 0）', () => {
  assert.strictEqual(reading(NaN), '--')
  assert.strictEqual(reading(Infinity), '--')
  assert.strictEqual(reading(-Infinity), '--')
  assert.strictEqual(reading(null), '--')
  assert.strictEqual(reading(undefined), '--')
})

test('percent：按契约量程换算并输出整数', () => {
  assert.strictEqual(percent(26.1, 80), 33)
  assert.strictEqual(percent(40, 80), 50)
  assert.strictEqual(percent(61, 100), 61)
  assert.strictEqual(percent(25, 999), 3)
})

test('percent：超量程夹到 0-100，无值返回 0', () => {
  assert.strictEqual(percent(999, 80), 100)
  assert.strictEqual(percent(-5, 80), 0)
  assert.strictEqual(percent(null, 80), 0)
  assert.strictEqual(percent(NaN, 80), 0)
})

test('decimal：保留 1 位并去掉末尾 .0（仅用于确实带小数的派生量）', () => {
  assert.strictEqual(decimal(4.2), '4.2')
  assert.strictEqual(decimal(3.0), '3')
  assert.strictEqual(decimal(1.25), '1.3')
  assert.strictEqual(decimal(NaN), '--')
  assert.strictEqual(decimal(null), '--')
})

test('clockText：从 RFC 3339 取 HH:mm:ss，无时间部分原样返回', () => {
  assert.strictEqual(clockText('2026-09-23T07:11:41Z'), '07:11:41')
  assert.strictEqual(clockText('2026-09-23T07:11:41.123Z'), '07:11:41')
  assert.strictEqual(clockText('no-time-part'), 'no-time-part')
  assert.strictEqual(clockText(null), '--')
})

test('rfc3339Utc：输出不带毫秒的 UTC 形式（与 KMP Rfc3339 一致）', () => {
  const ms = Date.UTC(2026, 8, 23, 7, 11, 41, 456)
  assert.strictEqual(rfc3339Utc(ms), '2026-09-23T07:11:41Z')
})

test('hoursToMillis：窗口小时 -> 毫秒', () => {
  assert.strictEqual(hoursToMillis(1), 3600000)
  assert.strictEqual(hoursToMillis(24), 86400000)
})
