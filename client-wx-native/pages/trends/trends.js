/**
 * 历史趋势页（真实折线图与统计摘要版）。
 *
 * 数据源：GET /api/v1/devices/{id}/telemetry（from/to/limit/cursor/order 契约参数）。
 * 核心特性：
 * 1. 独立 Y 轴缩放：温度 (°C, #FB7185)、湿度 (%RH, #7DD3FC)、气体 (ppm, #3FE8C3)
 *    各自独立计算极值并保留 10% 上下内边距（padding）。
 * 2. 气体缺失分段：当 gasPpm 为 null 时打断连续线段，绝不显示为 0；有效 0.0 正常显示。
 * 3. 统计口径与图表严格一致：均消费同一批经过倒序拉取、本地反转为升序的样本。
 * 4. 示数取整对齐 KMP：三指标平均/最低/最高使用 reading() 整数取整（half-up）。
 * 5. 网络防竞态：自增 requestId 避免旧请求覆盖新请求。
 * 6. 绘制防竞态：renderGate 绑定绘制版本，selector 异步回调内再次校验
 *    「页面未卸载 / 版本仍最新 / 时间窗未变」，过期回调不得清空或重绘 Canvas。
 * 7. 失败语义：网络失败 ≠ 空数据。失败时清空当前曲线并展示可重试的错误态。
 */
const monitoringService = require('../../services/monitoring.js')
const {
  buildChartGeometry,
  drawTrendChart,
  createRenderGate,
} = require('../../utils/trend-chart.js')

const RANGES = [
  { key: 'LAST_HOUR', label: '近1小时', hours: 1 },
  { key: 'LAST_SIX_HOURS', label: '近6小时', hours: 6 },
  { key: 'LAST_DAY', label: '近24小时', hours: 24 },
]

/** 网络失败时的统一提示语；与空数据文案严格区分。 */
const LOAD_ERROR_TEXT = '历史数据加载失败，请重试'

Page({
  data: {
    ranges: RANGES.map((r) => r.label),
    windows: RANGES,
    activeIndex: 0,
    loading: true,
    /** 网络失败时的错误文案；空字符串表示无错误。与「暂无历史数据」严格区分。 */
    error: '',
    trends: null,
    stats: null,
    sampleCount: 0,
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
    this.requestId += 1
    if (this.renderGate) this.renderGate.dispose()
    this.chartPoints = []
    this.renderGate = null
  },

  async onRangeTap(e) {
    const index = Number(e.currentTarget.dataset.index)
    if (index === this.data.activeIndex) return
    // 切窗时立刻清空旧曲线，避免旧时间窗的曲线被误当成新时间窗展示。
    this.setData({
      activeIndex: index,
      loading: true,
      error: '',
      hasChartData: false,
      sampleCount: 0,
      trends: null,
      stats: null,
      chartRanges: { temp: '--', hum: '--', gas: '--' },
      axisTimes: { start: '--', end: '--' },
    })
    this.chartPoints = []
    this.fetch()
  },

  /** 点击错误提示后重试当前时间窗。 */
  async onRetryTap() {
    this.fetch()
  },

  /** 拉取历史遥测并计算三项指标统计与折线图几何 */
  async fetch() {
    const reqId = ++this.requestId
    const windowKey = RANGES[this.data.activeIndex].key
    if (this.renderGate) {
      this.renderGate.nextVersion(String(this.data.activeIndex))
    }
    this.setData({ loading: true, error: '' })
    try {
      const { trendsView, rawPoints } = await monitoringService.loadTrendSamples('MCU001', windowKey, 200)
      if (this._unloaded || reqId !== this.requestId) return

      this.chartPoints = rawPoints

      this.setData(
        {
          loading: false,
          error: '',
          sampleCount: trendsView.sampleCount,
          hasChartData: trendsView.hasData,
          trends: trendsView,
          stats: trendsView.hasData ? trendsView : null,
        },
        () => {
          this.renderChart()
        }
      )
    } catch (e) {
      if (this._unloaded || reqId !== this.requestId) return
      this.chartPoints = []
      this.setData({
        loading: false,
        error: LOAD_ERROR_TEXT,
        sampleCount: 0,
        hasChartData: false,
        trends: null,
        stats: null,
        chartRanges: { temp: '--', hum: '--', gas: '--' },
        axisTimes: { start: '--', end: '--' },
      })
    }
  },

  /**
   * 在 Canvas 2D 上绘制折线图。
   */
  renderChart() {
    if (this._unloaded || !this.renderGate) return
    const gate = this.renderGate
    const drawVersion = gate.nextVersion(String(this.data.activeIndex))
    const drawWindowKey = String(this.data.activeIndex)
    const pointsSnapshot = (this.chartPoints || []).slice()

    wx.createSelectorQuery()
      .in(this)
      .select('#trendsCanvas')
      .fields({ node: true, size: true })
      .exec((res) => {
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
