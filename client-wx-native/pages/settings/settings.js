/**
 * 预警阈值页。
 *
 * 与 KMP 方案对齐（MonitoringClient.loadSettings / updateThresholds / awaitCommandOutcome）：
 * - 契约要求一次下发三个字段（温度/湿度/气体），界面上湿度不可编辑但必须一起发送；
 * - 超范围在本地校验并抛出契约范围文案，不发无效请求；
 * - 入队只代表"后端已接受"，必须轮询 `GET /commands/{requestId}` 观察设备终态，
 *   且只有 `applied` 才算成功，超时不得提示成功。
 */
const monitoring = require('../../services/monitoring.js')

Page({
  data: {
    /** SettingsView（已格式化） */
    settings: null,
    temperatureHighC: 30,
    humidityHighRh: 80,
    gasHighPpm: 20,
    loading: true,
    error: '',
    saving: false,
    commandHint: '',
    commandTone: 'warning',
  },

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 3 })
    }
    this.loadSettings()
  },

  async onPullDownRefresh() {
    await this.loadSettings()
    wx.stopPullDownRefresh()
  },

  async loadSettings() {
    this.setData({ loading: true })
    try {
      const view = await monitoring.loadSettings()
      this.setData({
        settings: view,
        temperatureHighC: view.temperatureHighC,
        humidityHighRh: view.humidityHighRh,
        gasHighPpm: view.gasHighPpm,
        loading: false,
        error: '',
      })
    } catch (e) {
      this.setData({ loading: false, error: (e && e.message) || '数据加载失败' })
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
      const accepted = await monitoring.updateThresholds({
        temperatureHighC: Number(this.data.temperatureHighC),
        humidityHighRh: Number(this.data.humidityHighRh),
        gasHighPpm: Number(this.data.gasHighPpm),
      })
      this.setData({ commandHint: accepted.stateText, commandTone: accepted.tone })
      const outcome = await monitoring.awaitCommandOutcome(accepted.requestId)
      this.setData({
        commandHint: outcome ? outcome.stateText : '等待设备确认',
        commandTone: outcome ? outcome.tone : 'warning',
      })
      wx.showToast({
        title: outcome ? outcome.stateText : '等待设备确认',
        icon: outcome && outcome.confirmed ? 'success' : 'none',
      })
      await this.loadSettings()
    } catch (e) {
      wx.showToast({ title: (e && e.message) || '下发失败', icon: 'none' })
    } finally {
      wx.hideLoading()
      this.setData({ saving: false })
    }
  },
})
