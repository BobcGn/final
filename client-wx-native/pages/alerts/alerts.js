/**
 * 告警记录页。
 *
 * 数据源：GET /api/v1/devices/{id}/alerts。
 * 契约规范与 KMP 对齐：
 * 1. 契约无 acknowledged 状态，仅有 normal / suspect / fire_warning / recovered；
 * 2. 筛选胶囊第三项对齐为「疑似」；
 * 3. 告警证据采用 ADC 码与温升速率，不显示 ppm 单位；
 * 4. 底部只展示已恢复时间（endedAt），无 acknowledgedAt；
 * 5. 一次拉取后在本地切标签筛选，避免频繁请求。
 */
const deviceService = require('../../services/device.js')
const presentation = require('../../utils/presentation.js')

Page({
  data: {
    deviceId: 'MCU001',
    loading: true,
    error: '',
    activeFilter: 'all',
    filters: presentation.alertFilterOptions(),
    alertsView: null,
  },

  _rawAlerts: [],

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 2 })
    }
    this.fetch()
  },

  async fetch() {
    this.setData({ loading: true, error: '' })
    try {
      const res = await deviceService.getAlerts(this.data.deviceId, { limit: 50 })
      this._rawAlerts = (res && res.items) || []
      const alertsView = presentation.alerts(this._rawAlerts, this.data.activeFilter)
      this.setData({
        alertsView,
        loading: false,
        error: '',
      })
    } catch (e) {
      this.setData({
        loading: false,
        error: (e && e.message) || '加载失败',
      })
    }
  },

  onFilterTap(e) {
    const key = e.currentTarget.dataset.key
    if (key === this.data.activeFilter) return
    this.setData({ activeFilter: key })
    const alertsView = presentation.alerts(this._rawAlerts, key)
    this.setData({ alertsView })
  },
})
