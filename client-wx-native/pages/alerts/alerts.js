/**
 * 告警记录页。
 *
 * 与 KMP 方案对齐（MonitoringClient.loadAlerts + MonitoringPresentation.alerts）：
 * - 契约的 alerts 查询没有 state 参数，因此拉一页后本地切标签，避免每次切标签都发请求；
 * - 证据（气体上升量、温升速率、样本数、窗口）全部读自后端持久化的 evidence，
 *   不用最新值反推历史告警原因；
 * - 告警状态枚举没有 acknowledged，筛选标签第三项是「疑似」。
 */
const monitoring = require('../../services/monitoring.js')
const presentation = require('../../utils/presentation.js')

Page({
  data: {
    filters: presentation.alertFilterOptions(),
    filterKey: 'all',
    items: [],
    count: 0,
    visibleCount: 0,
    loading: true,
    error: '',
  },

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 2 })
    }
    this.loadAlerts()
  },

  async onPullDownRefresh() {
    await this.loadAlerts()
    wx.stopPullDownRefresh()
  },

  onFilter(e) {
    const key = e.currentTarget.dataset.key
    if (!key || key === this.data.filterKey) return
    this.setData({ filterKey: key })
    this.loadAlerts()
  },

  async loadAlerts() {
    this.setData({ loading: true })
    try {
      const view = await monitoring.loadAlerts(this.data.filterKey)
      this.setData({
        filters: view.filters,
        filterKey: view.filterKey,
        items: view.items,
        count: view.count,
        visibleCount: view.visibleCount,
        loading: false,
        error: '',
      })
    } catch (e) {
      this.setData({ loading: false, error: (e && e.message) || '数据加载失败' })
    }
  },
})
