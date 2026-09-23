/**
 * 通用工具：幂等键生成。
 *
 * 时间展示统一走 utils/format.js 的 clockText（与 KMP 共享层同规则），
 * 因此原先的本地时间格式化函数已移除，避免两端出现不同的时间口径。
 */

/**
 * 生成 UUID v4 字符串，作为控制类请求（静音/阈值下发）的 Idempotency-Key。
 * 契约要求：相同用户、设备、路由和 key 的重复请求必须返回同一命令结果。
 */
function genIdempotencyKey() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    const v = c === 'x' ? r : (r & 0x3) | 0x8
    return v.toString(16)
  })
}

module.exports = { genIdempotencyKey }
