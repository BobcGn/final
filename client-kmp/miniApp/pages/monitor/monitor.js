const runtime = require('../../runtime.js')
const { createRequestGate } = require('./trend-request.js')

/**
 * Page copy for the four tabs. Titles live here because they are pure host UI
 * chrome; every data label comes from the shared runtime so Android and the
 * MiniApp cannot drift apart. The alert subtitle is the frozen baseline's.
 */
const TITLES = [
  ['机房环境总览', '智慧机房 · 实时动环监测'],
  ['历史趋势', '数据统计与曲线'],
  ['告警记录', '早期火情预警事件'],
  ['预警阈值', '设置设备本地报警的安全边界'],
]

/** Dashboard refresh cadence; the backend is the source of truth for values. */
const POLL_INTERVAL_MS = 3000

/** Trends page size. Matches the shared client's default page. */
const TREND_LIMIT = 200

/** Alert page size, matching the shared client's default page. */
const ALERT_LIMIT = 50

const DEFAULT_WINDOW = 'LAST_HOUR'
const DEFAULT_FILTER = 'all'

/** Network-failure copy. Strictly distinct from the empty-success wording. */
const LOAD_ERROR_TEXT = '历史数据加载失败，下拉可重试'

Page({
  data: {
    tab: 0,
    title: TITLES[0][0],
    subtitle: TITLES[0][1],
    loading: true,
    error: '',
    dashboard: null,
    trends: null,
    // The selector option lists come from the shared runtime so the labels exist
    // once. They are filled before the first fetch, so the trends window control
    // is usable even while the history request is still in flight.
    windowOptions: [],
    alertFilters: [],
    trendWindow: DEFAULT_WINDOW,
    alertFilter: DEFAULT_FILTER,
    alerts: [],
    alertCount: 0,
    settings: null,
    temperatureHighC: 30,
    // Not editable on this page, matching the frozen baseline's two controls.
    // It is still sent: the contract requires all three threshold fields.
    humidityHighRh: 80,
    gasHighPpm: 20,
    saving: false,
    muting: false,
    commandHint: '',
    commandTone: 'warning',
  },

  onLoad() {
    // One gate guards both the network boundary and the selector-callback
    // boundary. A fresh version is taken per request; stale responses and
    // stale canvas callbacks are rejected before they can setData or draw.
    this._gate = createRequestGate()
    try {
      const options = JSON.parse(runtime.selectors())
      this.setData({ windowOptions: options.windows, alertFilters: options.filters })
    } catch (e) {
      // A missing option list only costs the selector's labels; the data calls
      // below still work, so the page shows its error rather than failing to load.
      this.setData({ error: (e && e.message) || '选项加载失败' })
    }
    this.loadTab()
  },

  onShow() {
    this.startPolling()
    if (this.data.tab === 1 && this.data.trends && this.data.trends.hasData) {
      this.drawTrendChart(this.data.trends)
    }
  },

  onHide() {
    this.stopPolling()
  },

  // A hidden or unloaded page must not keep a timer alive, otherwise it would
  // keep issuing requests and calling setData on a destroyed page. Unloading
  // also invalidates every in-flight request and every pending canvas callback.
  onUnload() {
    this._unloaded = true
    this.stopPolling()
    if (this._gate) this._gate.invalidateAll()
  },

  async onPullDownRefresh() {
    await this.loadTab()
    wx.stopPullDownRefresh()
  },

  onTab(e) {
    const tab = Number(e.currentTarget.dataset.tab)
    if (tab === this.data.tab) return
    // Switching away from a tab must not let a late trend response repaint it.
    // The request gate's version bump in loadTab is what rejects those.
    this.setData({ tab, title: TITLES[tab][0], subtitle: TITLES[tab][1], error: '', commandHint: '' })
    this.loadTab()
  },

  /**
   * Switches the trends window and refetches.
   *
   * The window is an absolute `from`/`to` bound in the shared client, so the
   * server decides the range; nothing here filters the samples it returns.
   */
  onRange(e) {
    const key = e.currentTarget.dataset.key
    if (!key || key === this.data.trendWindow) return
    // Clear the previous window's curve immediately so a slow or failed fetch
    // cannot present the old window's curve as the newly selected one.
    this.setData({
      trendWindow: key,
      trends: null,
      error: '',
    })
    this.loadTab()
  },

  /** Switches the alert filter and refetches, so the filter rule stays shared. */
  onFilter(e) {
    const key = e.currentTarget.dataset.key
    if (!key || key === this.data.alertFilter) return
    this.setData({ alertFilter: key })
    this.loadTab()
  },

  startPolling() {
    this.stopPolling()
    this._timer = setInterval(() => {
      // Only the dashboard is live; the other tabs are fetched on demand so an
      // idle page does not burn quota on unchanged history.
      if (this.data.tab === 0) this.loadDashboard(false)
    }, POLL_INTERVAL_MS)
  },

  stopPolling() {
    if (this._timer) clearInterval(this._timer)
    this._timer = null
  },

  async loadTab() {
    const tab = this.data.tab
    const gate = this._gate
    if (!gate || gate.isDisposed()) return

    this.setData({ loading: true })
    try {
      if (tab === 0) {
        const loadVersion = gate.beginLoad(0)
        await this.loadDashboard(false, loadVersion)
        return
      }
      if (tab === 1) {
        const windowKey = this.data.trendWindow
        // Monotonic version per trend request; stale responses are dropped
        // before they can touch trends / loading / error / legend / axis / canvas.
        const trendVersion = gate.beginTrend(windowKey)
        const trends = JSON.parse(await runtime.trends(windowKey, TREND_LIMIT))
        if (this._unloaded || !gate.isTrendCurrent(trendVersion, windowKey) || this.data.tab !== 1) {
          return
        }
        this.setData({ trends, loading: false, error: '' }, () => {
          this.drawTrendChart(trends, trendVersion, windowKey)
        })
        return
      }
      if (tab === 2) {
        const loadVersion = gate.beginLoad(2)
        const page = JSON.parse(await runtime.alerts(this.data.alertFilter, ALERT_LIMIT))
        if (this._unloaded || !gate.isLoadCurrent(loadVersion, 2) || this.data.tab !== 2) return
        this.setData({ alerts: page.items, alertCount: page.visibleCount, loading: false, error: '' })
        return
      }
      if (tab === 3) {
        const loadVersion = gate.beginLoad(3)
        const value = JSON.parse(await runtime.settings())
        if (this._unloaded || !gate.isLoadCurrent(loadVersion, 3) || this.data.tab !== 3) return
        this.setData({
          settings: value,
          temperatureHighC: value.temperatureHighC,
          humidityHighRh: value.humidityHighRh,
          gasHighPpm: value.gasHighPpm,
          loading: false,
          error: '',
        })
      }
    } catch (e) {
      if (this._unloaded) return
      // Trend failure must clear the curve and show the failure state, never
      // leave the previous window's data looking like the current window.
      if (tab === 1) {
        const windowKey = this.data.trendWindow
        // Only apply the failure if this tab is still showing the trend view.
        if (this.data.tab !== 1) return
        this.setData({
          trends: null,
          loading: false,
          error: LOAD_ERROR_TEXT,
        })
        return
      }
      // Non-trend failures never carry trend state, so there is nothing to clear.
      this.setData({ loading: false, error: LOAD_ERROR_TEXT })
    }
  },

  /**
   * Draws the trend chart on the MiniApp canvas.
   *
   * `trendVersion` / `windowKey` identify which request this draw belongs to.
   * The selector callback is a second async boundary after the network call, so
   * it re-checks the gate before touching the canvas or setData: a stale draw
   * must not clear or repaint the canvas, nor update legend ranges or axis labels.
   */
  drawTrendChart(trends, trendVersion, windowKey) {
    if (this._unloaded) return
    if (!trends || !trends.hasData || !trends.series || !trends.series.length) return
    const gate = this._gate
    if (!gate) return
    // When called without a version (e.g. onShow redraw), treat it as a fresh
    // intent against the current window so it is still gated against unload and
    // later window changes.
    const version = trendVersion != null ? trendVersion : gate.beginTrend(windowKey != null ? windowKey : this.data.trendWindow)
    const key = windowKey != null ? windowKey : this.data.trendWindow

    const query = wx.createSelectorQuery().in(this)
    query
      .select('#trendCanvas')
      .fields({ node: true, size: true })
      .exec((res) => {
        // Re-check at the async boundary: page gone, version stale, or window
        // changed means this callback must not draw or setData at all.
        if (this._unloaded || !gate.isTrendCurrent(version, key)) return
        if (this.data.tab !== 1 || this.data.trendWindow !== key) return
        if (!res || !res[0] || !res[0].node) return
        const canvas = res[0].node
        const ctx = canvas.getContext('2d')
        const width = res[0].width
        const height = res[0].height
        if (!width || !height) return

        const dpr = (wx.getWindowInfo && wx.getWindowInfo().pixelRatio) ||
                    (wx.getSystemInfoSync && wx.getSystemInfoSync().pixelRatio) || 1
        canvas.width = width * dpr
        canvas.height = height * dpr
        ctx.scale(dpr, dpr)

        ctx.clearRect(0, 0, width, height)

        const padLeft = 12
        const padRight = 12
        const padTop = 16
        const padBottom = 16
        const plotWidth = width - padLeft - padRight
        const plotHeight = height - padTop - padBottom

        // 1. Draw 4 horizontal grid lines
        ctx.strokeStyle = 'rgba(255, 255, 255, 0.06)'
        ctx.lineWidth = 1
        for (let i = 0; i < 4; i++) {
          const y = padTop + (i / 3) * plotHeight
          ctx.beginPath()
          ctx.moveTo(padLeft, y)
          ctx.lineTo(width - padRight, y)
          ctx.stroke()
        }

        const points = trends.series
        // X mapping is all-or-nothing: time scale only when every timestamp is
        // valid and the span is positive; otherwise the whole series is uniform.
        // A null timestamp is never treated as epoch 0.
        let xCoords = []
        let usesTimeScale = false
        if (points.length === 1) {
          xCoords = [padLeft + plotWidth / 2]
        } else {
          const timestamps = points.map((p) =>
            p.timestampEpochMs == null ? null : p.timestampEpochMs
          )
          const allValid = timestamps.every((t) => t != null)
          const positiveSpan = allValid &&
            timestamps[timestamps.length - 1] - timestamps[0] > 0
          if (allValid && positiveSpan) {
            usesTimeScale = true
            const tMin = timestamps[0]
            const tMax = timestamps[timestamps.length - 1]
            const span = tMax - tMin
            xCoords = timestamps.map((t) => {
              const fraction = Math.max(0, Math.min(1, (t - tMin) / span))
              return padLeft + fraction * plotWidth
            })
          } else {
            // Single sample is handled above; here: any invalid timestamp, or a
            // zero span (all equal). Uniform by index for the entire series.
            const step = plotWidth / (points.length - 1)
            xCoords = points.map((_, idx) => padLeft + idx * step)
          }
        }
        void usesTimeScale

        const drawSeries = (values, color) => {
          const valid = values.filter((v) => v !== null && v !== undefined && !isNaN(v))
          if (!valid.length) return
          const minVal = Math.min(...valid)
          const maxVal = Math.max(...valid)

          const computeY = (v) => {
            if (maxVal <= minVal) return padTop + plotHeight / 2
            const span = maxVal - minVal
            const pMin = minVal - span * 0.1
            const pMax = maxVal + span * 0.1
            const frac = Math.max(0, Math.min(1, (v - pMin) / (pMax - pMin)))
            return height - padBottom - frac * plotHeight
          }

          let segments = []
          let curSeg = []
          let singles = []

          for (let i = 0; i < values.length; i++) {
            const v = values[i]
            if (v === null || v === undefined || isNaN(v)) {
              if (curSeg.length === 1) singles.push(curSeg[0])
              else if (curSeg.length > 1) segments.push(curSeg)
              curSeg = []
            } else {
              curSeg.push({ x: xCoords[i], y: computeY(v) })
            }
          }
          if (curSeg.length === 1) singles.push(curSeg[0])
          else if (curSeg.length > 1) segments.push(curSeg)

          ctx.strokeStyle = color
          ctx.lineWidth = 2
          ctx.lineCap = 'round'
          ctx.lineJoin = 'round'

          segments.forEach((seg) => {
            ctx.beginPath()
            ctx.moveTo(seg[0].x, seg[0].y)
            for (let i = 1; i < seg.length; i++) {
              ctx.lineTo(seg[i].x, seg[i].y)
            }
            ctx.stroke()

            ctx.fillStyle = color
            seg.forEach((pt) => {
              ctx.beginPath()
              ctx.arc(pt.x, pt.y, 2.5, 0, Math.PI * 2)
              ctx.fill()
            })
          })

          ctx.fillStyle = color
          singles.forEach((pt) => {
            ctx.beginPath()
            ctx.arc(pt.x, pt.y, 3.5, 0, Math.PI * 2)
            ctx.fill()
          })
        }

        drawSeries(points.map((p) => p.temperatureC), '#FB7185')
        drawSeries(points.map((p) => p.humidityRh), '#7DD3FC')
        drawSeries(points.map((p) => p.gasPpm), '#3FE8C3')
      })
  },

  /**
   * Refreshes the dashboard.
   *
   * Failures are swallowed into `error` instead of being thrown, so a network
   * blip can never break the polling loop: the next tick simply retries.
   * `loadVersion` is the request gate's token for this call; a stale response
   * must not overwrite a newer tab's state.
   */
  async loadDashboard(showLoading = true, loadVersion) {
    const gate = this._gate
    if (showLoading) this.setData({ loading: true })
    try {
      const dash = JSON.parse(await runtime.dashboard())
      if (this._unloaded) return
      if (gate && loadVersion != null && !gate.isLoadCurrent(loadVersion, 0)) return
      if (this.data.tab !== 0) return
      this.setData({ dashboard: dash, loading: false, error: '' })
    } catch (e) {
      if (this._unloaded) return
      if (gate && loadVersion != null && !gate.isLoadCurrent(loadVersion, 0)) return
      if (this.data.tab !== 0) return
      this.setData({ loading: false, error: LOAD_ERROR_TEXT })
    }
  },

  /**
   * Waits for the device acknowledgement that closes the control loop.
   *
   * The polling budget and the "still awaiting" decision live in the shared
   * runtime, so the MiniApp and Android cannot disagree about what counts as
   * settled. A null result means the device had not answered in time and must
   * not be reported as success.
   *
   * @param {string} requestId command id returned by the enqueue call.
   * @returns {Promise<Object|null>} the settled view, or null on timeout.
   */
  async awaitCommandOutcome(requestId) {
    const settled = await runtime.awaitCommandOutcome(requestId)
    return settled ? JSON.parse(settled) : null
  },

  async onMute() {
    if (this.data.muting || !this.data.dashboard) return
    this.setData({ muting: true, commandHint: '' })
    wx.showLoading({ title: '下发中', mask: true })
    try {
      const accepted = JSON.parse(await runtime.mute(!this.data.dashboard.buzzerMuted))
      this.setData({ commandHint: accepted.stateText, commandTone: accepted.tone })
      const outcome = await this.awaitCommandOutcome(accepted.requestId)
      // The hint keeps the terminal tone, so a rejection or a timeout reads
      // differently from a pending acknowledgement.
      this.setData({
        commandHint: outcome ? outcome.stateText : '等待设备确认',
        commandTone: outcome ? outcome.tone : 'warning',
      })
      await this.loadDashboard(false)
      wx.showToast({
        title: outcome ? outcome.stateText : '等待设备确认',
        // Only an explicit device `applied` acknowledgement is success.
        // Pending, duplicate and failed outcomes must not get a green tick.
        icon: outcome && outcome.confirmed ? 'success' : 'none',
      })
    } catch (e) {
      wx.showToast({ title: (e && e.message) || '下发失败', icon: 'none' })
    } finally {
      wx.hideLoading()
      this.setData({ muting: false })
    }
  },

  onTemp(e) {
    this.setData({ temperatureHighC: e.detail.value })
  },
  onGas(e) {
    this.setData({ gasHighPpm: e.detail.value })
  },

  async onSave() {
    if (this.data.saving) return
    this.setData({ saving: true, commandHint: '' })
    wx.showLoading({ title: '下发中', mask: true })
    try {
      // The shared runtime rejects an out-of-range value before any network
      // call, so the user sees the contract range instead of a server error.
      const accepted = JSON.parse(
        await runtime.updateThresholds(
          Number(this.data.temperatureHighC),
          Number(this.data.humidityHighRh),
          Number(this.data.gasHighPpm)
        )
      )
      this.setData({ commandHint: accepted.stateText, commandTone: accepted.tone })
      const outcome = await this.awaitCommandOutcome(accepted.requestId)
      this.setData({
        commandHint: outcome ? outcome.stateText : '等待设备确认',
        commandTone: outcome ? outcome.tone : 'warning',
      })
      wx.showToast({
        title: outcome ? outcome.stateText : '等待设备确认',
        icon: outcome && outcome.confirmed ? 'success' : 'none',
      })
      await this.loadTab()
    } catch (e) {
      wx.showToast({ title: (e && e.message) || '下发失败', icon: 'none' })
    } finally {
      wx.hideLoading()
      this.setData({ saving: false })
    }
  },
})
