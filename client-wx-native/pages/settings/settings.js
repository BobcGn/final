/**
 * 预警阈值设置页。
 *
 * 与 client-kmp 严格对齐：
 * 1. 契约要求下发三项完整阈值（temperatureHighC / humidityHighRh / gasHighPpm）；
 * 2. 湿度不可编辑但在保存时保持回传，杜绝丢失；
 * 3. 温度与气体步长为 1（固件按整数摄氏度执行）；
 * 4. 下发前执行本地范围校验，提示文案完全一致；
 * 5. 下发 202 仅代表在途，通过轮询 GET /commands/{requestId} 等待设备 applied 终态；
 * 6. 移除 WebSocket thresholds.confirmed 依赖。
 */
const monitoring = require('../../services/monitoring.js')

Page({
  data: {
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
    this.fetch()
  },

  async onPullDownRefresh() {
    await this.fetch()
    wx.stopPullDownRefresh()
  },

  async fetch() {
    this.setData({ loading: true })
    try {
      const view = await monitoring.loadSettings('MCU001')
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

  async loadSettings() {
    return this.fetch()
  },

  onTempChange(e) {
    this.setData({ temperatureHighC: e.detail.value })
  },
  onTemp(e) {
    this.onTempChange(e)
  },

  onGasChange(e) {
    this.setData({ gasHighPpm: e.detail.value })
  },
  onGas(e) {
    this.onGasChange(e)
  },

  async onSave() {
    if (this.data.saving) return
    this.setData({ saving: true, commandHint: '' })
    if (typeof wx !== 'undefined' && wx.showLoading) {
      wx.showLoading({ title: '下发中', mask: true })
    }
    try {
      const accepted = await monitoring.updateThresholds('MCU001', {
        temperatureHighC: Number(this.data.temperatureHighC),
        humidityHighRh: Number(this.data.humidityHighRh),
        gasHighPpm: Number(this.data.gasHighPpm),
      })
      this.setData({ commandHint: accepted.stateText, commandTone: accepted.tone })
      const outcome = await monitoring.awaitCommandOutcome('MCU001', accepted.requestId)
      this.setData({
        commandHint: outcome ? outcome.stateText : '等待设备确认',
        commandTone: outcome ? outcome.tone : 'warning',
      })
      if (typeof wx !== 'undefined' && wx.showToast) {
        wx.showToast({
          title: outcome ? outcome.stateText : '等待设备确认',
          icon: outcome && outcome.confirmed ? 'success' : 'none',
        })
      }
      await this.fetch()
    } catch (e) {
      if (typeof wx !== 'undefined' && wx.showToast) {
        wx.showToast({ title: (e && e.message) || '下发失败', icon: 'none' })
      }
    } finally {
      if (typeof wx !== 'undefined' && wx.hideLoading) {
        wx.hideLoading()
      }
      this.setData({ saving: false })
    }
  },
})
