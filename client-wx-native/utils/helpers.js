/**
 * 通用工具：时间格式化与幂等键生成。
 */

/** RFC 3339 时间转 'MM-DD HH:mm'，用于列表展示 */
function formatRfc3339(iso) {
  if (!iso) return '--'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '--'
  const p = (n) => (n < 10 ? '0' + n : '' + n)
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes())
}

/** RFC 3339 时间转 'HH:mm:ss'，用于实时页面的更新时间展示 */
function formatTime(iso) {
  if (!iso) return '--:--:--'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '--:--:--'
  const p = (n) => (n < 10 ? '0' + n : '' + n)
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds())
}

/**
 * 生成 UUID v4 字符串，作为控制类请求（阈值下发）的 Idempotency-Key。
 * 契约要求：相同用户、设备、路由和 key 的重复请求必须返回同一命令结果。
 */
function genIdempotencyKey() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    const v = c === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}

module.exports = { formatRfc3339, formatTime, genIdempotencyKey }
