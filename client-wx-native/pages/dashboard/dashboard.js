/**
 * 机房环境总览页（仪表盘）。
 *
 * 与 KMP 方案严格对齐（client-kmp/miniApp/pages/monitor/monitor.js）：
 * - 每 3 秒重取一次「status + latest」组成原子快照，指标与风险状态同帧更新；
 * - 只在本页可见时轮询，隐藏或卸载立即停止；
 * - 刷新失败写入 error 但不中断轮询，下一轮自然重试；
 * - 示数、百分比、空态、文案全部由共享展示层派生（utils/presentation.js），
 *   本页不做任何数值换算，避免与 KMP 出现精度或文案差异。
 */
const monitoring = require('../../services/monitoring.js')

/** 仪表盘刷新节奏；与 KMP 宿主保持一致 */
const POLL_INTERVAL_MS = 3000

Page({
  data: {
    /** DashboardView（已格式化） */
    dashboard: null,
    loading: true,
    error: '',
    muting: false,
    /** 控制命令的结果提示（等待/已确认/失败） */
    commandHint: '',
    commandTone: 'warning',
  },

  onLoad() {
    this.loadDashboard(true)
  },

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 0 })
    }
    this.startPolling()
  },

  onHide() {
    this.stopPolling()
  },

  onUnload() {
    this.stopPolling()
  },

  /** 下拉刷新：立即取一次，并结束下拉动画 */
  async onPullDownRefresh() {
    await this.loadDashboard(false)
    wx.stopPullDownRefresh()
  },

  startPolling() {
    this.stopPolling()
    this._timer = setInterval(() => {
      this.loadDashboard(false)
    }, POLL_INTERVAL_MS)
  },

  stopPolling() {
    if (this._timer) clearInterval(this._timer)
    this._timer = null
  },

  /**
   * 刷新仪表盘。
   *
   * 失败的刷新只写入 error，不抛出、不停止轮询：一次网络抖动不应该让页面永久停更。
   * 上一轮未返回时跳过本轮，避免弱网下请求叠加把页面挤得更慢。
   * @param {boolean} showLoading 是否显示整页加载态（首屏显示，轮询不显示）
   */
  async loadDashboard(showLoading) {
    if (this._inflight) return
    this._inflight = true
    if (showLoading) this.setData({ loading: true })
    try {
      const view = await monitoring.loadDashboard()
      this.setData({ dashboard: view, loading: false, error: '' })
    } catch (e) {
      this.setData({ loading: false, error: (e && e.message) || '数据加载失败' })
    } finally {
      this._inflight = false
    }
  },

  /**
   * 蜂鸣器静音/恢复。
   *
   * 入队响应只代表后端已接受命令，因此这里不下结论：轮询
   * `GET /commands/{requestId}` 拿到设备终态后，只有 `applied` 才算成功；
   * 超时不得提示成功（契约 §9 与 KMP `awaitCommandOutcome` 的语义）。
   */
  async onMuteTap() {
    if (this.data.muting || !this.data.dashboard) return
    const muted = !this.data.dashboard.buzzerMuted
    this.setData({ muting: true, commandHint: '' })
    wx.showLoading({ title: '下发中', mask: true })
    try {
      const accepted = await monitoring.setMuted(muted)
      this.setData({ commandHint: accepted.stateText, commandTone: accepted.tone })
      const outcome = await monitoring.awaitCommandOutcome(accepted.requestId)
      this.setData({
        commandHint: outcome ? outcome.stateText : '等待设备确认',
        commandTone: outcome ? outcome.tone : 'warning',
      })
      await this.loadDashboard(false)
      wx.showToast({
        title: outcome ? outcome.stateText : '等待设备确认',
        // 只有设备明确 applied 才是成功；pending/duplicate/failed 不能给绿勾
        icon: outcome && outcome.confirmed ? 'success' : 'none',
      })
    } catch (e) {
      wx.showToast({ title: (e && e.message) || '下发失败', icon: 'none' })
    } finally {
      wx.hideLoading()
      this.setData({ muting: false })
    }
  },
})
