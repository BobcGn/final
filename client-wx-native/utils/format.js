/**
 * 数值与时间格式化规则。
 *
 * 本文件是 `client-kmp` 共享层 `MonitoringPresentation` 私有格式化函数的等价实现，
 * 目的是让微信端与 KMP 方案对同一个后端值渲染出完全相同的字符串。
 * 规则来源：client-kmp/shared/src/commonMain/.../monitoring/Presentation.kt
 *
 * 关键约定（与 KMP 注释中的理由一致）：
 * - 读数一律输出整数：DHT11 分辨率是 1 °C / 1 %RH，气体估计精度 1 ppm，
 *   小数是设备产不出的精度；打印 `31.0` 会让读者相信一个从未被测量的数字。
 * - 取整用四舍五入（half-up），与固件对子单位阈值的取整规则一致。
 * - 进度条按契约量程缩放并夹在 0–100，输出整数。
 */

/**
 * 读数值 -> 整数字符串。
 * @param {number|null|undefined} value 原始读数
 * @returns {string} 整数字符串；非有限值（NaN/Infinity/null）返回 '--'
 */
function reading(value) {
  if (value === null || value === undefined) return '--'
  const n = Number(value)
  if (!isFinite(n)) return '--'
  return String(Math.round(n))
}

/**
 * 按量程把读数换算成 0–100 的整数百分比（用于仪表条）。
 * @param {number|null|undefined} value 原始读数
 * @param {number} maximum 契约量程上限（如 80 °C / 100 %RH / 999 ppm）
 * @returns {number} 0–100 的整数；无值时返回 0
 */
function percent(value, maximum) {
  if (value === null || value === undefined) return 0
  const n = Number(value)
  if (!isFinite(n)) return 0
  const scaled = Math.round((n / maximum) * 100)
  return Math.min(100, Math.max(0, scaled))
}

/**
 * 小数展示：保留 1 位并去掉末尾的 .0（用于温升速率等确实带小数的派生量）。
 * @param {number|null|undefined} value 原始值
 * @returns {string} 如 '1.8' / '2'；非有限值返回 '--'
 */
function decimal(value) {
  if (value === null || value === undefined) return '--'
  const n = Number(value)
  if (!isFinite(n)) return '--'
  const rounded = Math.round(n * 10) / 10
  return Number.isInteger(rounded) ? String(rounded) : String(rounded)
}

/**
 * 从 RFC 3339 时间戳中取 `HH:mm:ss`。
 * 没有时间部分的字符串原样返回，避免猜出一个错误的时间。
 * @param {string} timestamp 如 2026-09-23T07:11:41Z
 * @returns {string} HH:mm:ss，或原字符串，或 '--'
 */
function clockText(timestamp) {
  if (!timestamp) return '--'
  const text = String(timestamp)
  const idx = text.indexOf('T')
  if (idx === -1) return text
  return text.substr(idx + 1, 8)
}

/**
 * 把毫秒时间戳渲染为契约要求的 UTC RFC 3339（不带毫秒）。
 * 与 KMP `Rfc3339.utcFromEpochMillis` 输出一致，用于历史查询的 from/to 边界。
 * @param {number} millis 毫秒时间戳
 * @returns {string} 如 2026-09-23T07:11:41Z
 */
function rfc3339Utc(millis) {
  return new Date(millis).toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/**
 * 小时数转毫秒（趋势窗口 -> from 边界）。
 * @param {number} hours 小时
 * @returns {number} 毫秒
 */
function hoursToMillis(hours) {
  return Number(hours) * 60 * 60 * 1000
}

module.exports = { reading, percent, decimal, clockText, rfc3339Utc, hoursToMillis }
