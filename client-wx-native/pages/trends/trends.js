/**
 * 历史趋势页。
 *
 * 与 KMP 方案对齐（MonitoringClient.loadTrends）：
 * - 窗口换算成绝对 from/to 边界，按 order=desc 取最近一页后本地反转升序，
 *   避免"只描述区间开头几分钟却当成整段走势"；
 * - 统计、峰值时刻、图例与全部文案来自共享展示层，本页不做数值格式化。
 */
const monitoring = require('../../services/monitoring.js')
const presentation = require('../../utils/presentation.js')

Page({
  data: {
    /** 选择器选项来自共享层，首个响应到达前即可绘制 */
    windowOptions: presentation.trendWindowOptions(),
    trendWindow: 'LAST_HOUR',
    trends: null,
    loading: true,
    error: '',
  },

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 1 })
    }
    this.loadTrends()
  },

  async onPullDownRefresh() {
    await this.loadTrends()
    wx.stopPullDownRefresh()
  },

  /** 切换时间窗口：范围由服务端按 from/to 决定，本地不过滤样本 */
  onRange(e) {
    const key = e.currentTarget.dataset.key
    if (!key || key === this.data.trendWindow) return
    this.setData({ trendWindow: key })
    this.loadTrends()
  },

  async loadTrends() {
    this.setData({ loading: true })
    try {
      const trends = await monitoring.loadTrends(this.data.trendWindow)
      this.setData({ trends, loading: false, error: '' })
    } catch (e) {
      this.setData({ loading: false, error: (e && e.message) || '数据加载失败' })
    }
  },
})
