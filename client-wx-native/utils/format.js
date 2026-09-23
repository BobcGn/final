/**
 * 展示层基础格式化工具。
 *
 * 示数取整规则说明（与 client-kmp 严格一致）：
 * - DHT11 传感器物理分辨率为 1 °C / 1 %RH，气体估计有效精度为 1 ppm。
 * - 小数位并非设备能产出的精度，输出 26.1 会误导读者相信未测量的数字。
 * - 取整采用 half-up 规则，与固件对子单位阈值的取整规则一致（docs/device-protocol.md §4.3）。
 */

/**
 * 读数取整：将数值以 half-up 规则格式化为整数。
 * 非有限值或 null 返回 '--'。
 * @param {number|null|undefined} value
 * @returns {string}
 */
function reading(value) {
  if (value == null || !Number.isFinite(Number(value))) return '--'
  return String(Math.floor(Number(value) + 0.5))
}

/**
 * 百分比换算：将数值缩放到最大量程，按 half-up 取整并夹在 0-100 区间。
 * 非有限值或 null 返回 0。
 * @param {number|null|undefined} value
 * @param {number} maximum
 * @returns {number}
 */
function percent(value, maximum) {
  if (value == null || !Number.isFinite(Number(value)) || !maximum) return 0
  const ratio = (Number(value) / Number(maximum)) * 100
  const rounded = Math.floor(ratio + 0.5)
  return Math.min(100, Math.max(0, rounded))
}

/**
 * 保留最多 1 位小数，若末尾为 .0 则省略。
 * 非有限值返回 '--'。
 * @param {number|null|undefined} value
 * @returns {string}
 */
function decimal(value) {
  if (value == null || !Number.isFinite(Number(value))) return '--'
  const rounded = Math.round(Number(value) * 10) / 10
  return String(rounded)
}

/**
 * 从 RFC 3339 时间戳中提取 HH:mm:ss 紧凑时钟文本。
 * 若无时间部分或格式异常则原样返回或兜底。
 * @param {string|null|undefined} timestamp
 * @returns {string}
 */
function clockText(timestamp) {
  if (!timestamp) return '--'
  const str = String(timestamp)
  const idx = str.indexOf('T')
  if (idx === -1) return str
  const time = str.slice(idx + 1)
  return time.length >= 8 ? time.slice(0, 8) : time
}

/**
 * 将毫秒时间戳转换为 UTC ISO 8601 (RFC 3339) 格式字符串。
 * @param {number} ms
 * @returns {string}
 */
function rfc3339Utc(ms) {
  return new Date(ms).toISOString()
}

/**
 * 解析 RFC 3339 时间戳为毫秒时间戳，解析失败返回 null。
 * @param {string|null|undefined} timestamp
 * @returns {number|null}
 */
function parseEpochMillis(timestamp) {
  if (!timestamp) return null
  const ms = Date.parse(timestamp)
  return Number.isNaN(ms) ? null : ms
}

module.exports = {
  reading,
  percent,
  decimal,
  clockText,
  rfc3339Utc,
  parseEpochMillis,
}
