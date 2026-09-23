/**
 * 历史趋势页（真实折线图 + 统计摘要）。
 *
 * 数据源与统计口径与 KMP 方案对齐（services/monitoring.js + utils/presentation.js）：
 * - 用绝对 from/to 边界 + order=desc 取最近一页，本地反转为升序；
 * - 统计、峰值时刻、图例与文案来自共享展示层，读数取整（不做小数展示）。
 *
 * 折线图沿用团队实现（utils/trend-chart.js），保留其既有语义：
 * 1. 独立 Y 轴缩放：温度 (°C, #FB7185)、湿度 (%RH, #7DD3FC)、气体 (ppm, #3FE8C3)
 *    各自计算极值并保留 10% 上下内边距。
 * 2. 气体缺失分段：gasPpm 为 null 时打断线段，绝不显示为 0；有效 0.0 正常绘制。
 * 3. 时间比例 X 轴（all-or-nothing）：仅当全部时间戳有效且跨度为正时按时间比例分布，
 *    否则整条序列统一降级为按索引均匀分布。
 * 4. 网络防竞态：自增 requestId 避免旧请求覆盖新请求。
 * 5. 绘制防竞态：renderGate 版本校验，过期 selector 回调不清空也不重绘 Canvas。
 * 6. 失败语义：网络失败 ≠ 空数据；失败清空当前曲线并给出可重试的错误态。
 */
const monitoring = require('../../services/monitoring.js')
const presentation = require('../../utils/presentation.js')
const {
  buildChartGeometry,
  drawTrendChart,
  createRenderGate,
} = require('../../utils/trend-chart.js')

/** 网络失败时的统一提示语；与空数据文案严格区分 */
const LOAD_ERROR_TEXT = '历史数据加载失败，请重试'

Page({
  data: {
    /** 选择器选项来自共享层，首个响应到达前即可绘制 */
    windowOptions: presentation.trendWindowOptions(),
    trendWindow: 'LAST_HOUR',
    /** TrendsView（已格式化） */
    trends: null,
    loading: true,
    /** 网络失败时的错误文案；空字符串表示无错误。与「没有样本」严格区分 */
    error: '',
    hasChartData: false,
    chartRanges: {
      temp: '--',
      hum: '--',
      gas: '--',
    },
    axisTimes: {
      start: '--',
      end: '--',
    },
  },

  requestId: 0,
  chartPoints: [],
  // 绘制门闩：网络 requestId 管请求竞态，renderGate 管 selector 回调竞态
  renderGate: null,

  onLoad() {
    this.renderGate = createRenderGate()
  },

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 1 })
    }
    this.fetch()
  },

  onUnload() {
    this._unloaded = true
    // 递增网络版本，作废所有在途请求
    this.requestId += 1
    // 递增绘制版本并标记 disposed，作废所有在途 selector 回调
    if (this.renderGate) this.renderGate.dispose()
    this.chartPoints = []
    this.renderGate = null
  },

  /** 切换时间窗口：范围由服务端按 from/to 决定，本地不过滤样本 */
  onRange(e) {
    const key = e.currentTarget.dataset.key
    if (!key || key === this.data.trendWindow) return
    // 切窗时立刻清空旧曲线，避免旧时间窗的曲线被误当成新时间窗展示
    this.setData({
      trendWindow: key,
      loading: true,
      error: '',
      hasChartData: false,
      trends: null,
      chartRanges: { temp: '--', hum: '--', gas: '--' },
      axisTimes: { start: '--', end: '--' },
    })
    this.chartPoints = []
    this.fetch()
  },

  /** 点击错误提示后重试当前时间窗 */
  onRetryTap() {
    this.fetch()
  },

  /** 拉取历史样本：先算统计视图，再交给 Canvas 绘制 */
  async fetch() {
    const reqId = ++this.requestId
    const windowKey = this.data.trendWindow
    if (this.renderGate) {
      // 开启新的绘制意图；旧的 selector 回调会因版本过期而被拒绝
      this.renderGate.nextVersion(windowKey)
    }
    this.setData({ loading: true, error: '' })
    try {
      const points = await monitoring.loadTrendSamples(windowKey)
      if (this._unloaded || reqId !== this.requestId) return

      // 同一批样本同时喂给统计与折线图，两者永远基于同一份数据
      this.chartPoints = points
      const trends = presentation.trends(points, windowKey)

      // 空成功：无样本 + 无错误，与网络失败严格区分
      this.setData(
        {
          loading: false,
          error: '',
          trends,
          hasChartData: trends.hasData,
        },
        () => {
          this.renderChart()
        }
      )
    } catch (e) {
      if (this._unloaded || reqId !== this.requestId) return
      // 失败策略：清空当前曲线，展示明确错误态，不把旧曲线伪装成新时间窗
      this.chartPoints = []
      this.setData({
        loading: false,
        error: LOAD_ERROR_TEXT,
        trends: null,
        hasChartData: false,
        chartRanges: { temp: '--', hum: '--', gas: '--' },
        axisTimes: { start: '--', end: '--' },
      })
    }
  },

  /**
   * 在 Canvas 2D 上绘制折线图。
   *
   * selector 回调是网络之外的第二个异步边界：回调触发时本次请求可能已过期
   * （页面卸载、更新的请求已发出、或时间窗已切换）。过期回调不得清空/重绘
   * Canvas，也不得更新 chartRanges / axisTimes。
   */
  renderChart() {
    if (this._unloaded || !this.renderGate) return
    const gate = this.renderGate
    const drawWindowKey = String(this.data.trendWindow)
    const drawVersion = gate.nextVersion(drawWindowKey)
    const pointsSnapshot = (this.chartPoints || []).slice()

    wx.createSelectorQuery()
      .in(this)
      .select('#trendsCanvas')
      .fields({ node: true, size: true })
      .exec((res) => {
        // 异步边界重入：页面已卸载、版本已过期、或时间窗已变 → 直接放弃
        if (!gate.isStillWanted(drawVersion, drawWindowKey)) return
        if (this._unloaded) return
        if (!res || !res[0] || !res[0].node) return
        const canvas = res[0].node
        const ctx = canvas.getContext('2d')
        const dpr = (wx.getSystemInfoSync && wx.getSystemInfoSync().pixelRatio) || 1
        const width = res[0].width || 300
        const height = res[0].height || 180

        canvas.width = width * dpr
        canvas.height = height * dpr
        ctx.scale(dpr, dpr)

        const geometry = buildChartGeometry(pointsSnapshot, {
          width,
          height,
          padding: { left: 16, right: 16, top: 16, bottom: 16 },
        })

        drawTrendChart(ctx, geometry)

        // setData 前再校验一次：绘制过程中状态可能已变化
        if (!gate.isStillWanted(drawVersion, drawWindowKey) || this._unloaded) return

        this.setData({
          chartRanges: {
            temp: geometry.tempRangeText,
            hum: geometry.humRangeText,
            gas: geometry.gasRangeText,
          },
          axisTimes: {
            start: geometry.xStartText,
            end: geometry.xEndText,
          },
        })
      })
  },
})
