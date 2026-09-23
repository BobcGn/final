/**
 * 趋势请求竞态门闩（纯逻辑，可注入、可单测）。
 *
 * MiniApp 的 `loadTab()` 可能在上一次趋势请求尚未返回时再次发起请求
 * （例如用户在「近1小时」与「近6小时」之间快速切换）。若请求 A 后发、
 * B 先回，A 的迟到响应不得覆盖 B 已写入的趋势状态。
 *
 * 页面在每次趋势请求前调用 `beginTrend` 拿到版本号；响应回来时用
 * `isTrendCurrent` 判断是否仍是最新意图，再决定是否 setData / 绘制。
 * 页面卸载时调用 `invalidateAll`，作废所有在途请求（包括非趋势标签页）。
 */

/**
 * @returns {{
 *   beginTrend: (windowKey: string) => number,
 *   isTrendCurrent: (version: number, windowKey?: string) => boolean,
 *   beginLoad: (tab: number) => number,
 *   isLoadCurrent: (version: number, tab?: number) => boolean,
 *   invalidateAll: () => void,
 *   isDisposed: () => boolean,
 * }}
 */
function createRequestGate() {
  let trendVersion = 0
  let trendWindowKey = null
  let loadVersion = 0
  let loadTab = null
  let disposed = false

  return {
    /** 开始一次趋势请求；返回本次的版本号。同时作废任何在途的非趋势请求。 */
    beginTrend(windowKey) {
      trendVersion += 1
      loadVersion += 1
      trendWindowKey = windowKey == null ? null : String(windowKey)
      loadTab = null
      return trendVersion
    },
    /**
     * 判断 version 对应的趋势请求是否仍是最新意图。
     * @param {number} version beginTrend 返回的版本号
     * @param {string} [windowKey] 发起时的时间窗；传入时会一并校验时间窗未变
     */
    isTrendCurrent(version, windowKey) {
      if (disposed) return false
      if (version !== trendVersion) return false
      if (windowKey == null) return true
      return String(windowKey) === trendWindowKey
    },
    /** 开始一次非趋势标签页请求；返回本次的版本号。同时作废任何在途的趋势请求。 */
    beginLoad(tab) {
      loadVersion += 1
      trendVersion += 1
      loadTab = tab == null ? null : Number(tab)
      trendWindowKey = null
      return loadVersion
    },
    /** 判断 version 对应的非趋势请求是否仍是最新意图。 */
    isLoadCurrent(version, tab) {
      if (disposed) return false
      if (version !== loadVersion) return false
      if (tab == null) return true
      return Number(tab) === loadTab
    },
    /** 页面卸载时调用；使所有在途回调失效，无论趋势还是非趋势。 */
    invalidateAll() {
      disposed = true
      trendVersion += 1
      loadVersion += 1
      trendWindowKey = null
      loadTab = null
    },
    isDisposed() {
      return disposed
    },
  }
}

module.exports = { createRequestGate }
