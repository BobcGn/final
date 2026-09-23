/**
 * 实时监控首页。
 *
 * 刷新策略（与 client-kmp 严格对齐）：
 * - 固定 3000 ms 轮询一次「status + latest」原子快照；
 * - 上一轮请求尚未返回时自动跳过本轮（防并发重叠）；
 * - 页面在 onShow 启动轮询，onHide / onUnload 停止轮询；
 * - 刷新失败写入 error 提示，但不会打断定时器循环；
 * - 不依赖 WebSocket 订阅。
 */
const monitoringService = require('../../services/monitoring.js')

const POLL_INTERVAL_MS = 3000

Page({
  data: {
    deviceId: 'MCU001',
    loading: true,
    error: '',
    view: null,
  },

  _timer: null,
  _fetching: false,
  _unloaded: false,

  onLoad() {
    this._unloaded = false
  },

  onShow() {
    this._unloaded = false
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 0 })
    }
    this.fetchDashboard()
    this.startPolling()
  },

  onHide() {
    this.stopPolling()
  },

  onUnload() {
    this._unloaded = true
    this.stopPolling()
  },

  startPolling() {
    this.stopPolling()
    this._timer = setInterval(() => {
      this.fetchDashboard()
    }, POLL_INTERVAL_MS)
  },

  stopPolling() {
    if (this._timer) {
      clearInterval(this._timer)
      this._timer = null
    }
  },

  /** 下拉刷新 */
  async onPullDownRefresh() {
    await this.fetchDashboard()
    wx.stopPullDownRefresh()
  },

  /** 拉取原子快照并刷新视图模型 */
  async fetchDashboard() {
    if (this._fetching) return
    this._fetching = true
    try {
      const view = await monitoringService.loadDashboard(this.data.deviceId)
      if (this._unloaded) return
      this.setData({
        view,
        loading: false,
        error: '',
      })
    } catch (e) {
      if (this._unloaded) return
      this.setData({
        loading: false,
        error: (e && e.message) || '数据加载失败',
      })
    } finally {
      this._fetching = false
    }
  },
})
