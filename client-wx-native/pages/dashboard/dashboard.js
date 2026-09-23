/**
 * 实时监控首页。
 *
 * 数据流（契约 §10 推荐做法）：
 * 1) 进入页面先走 REST 补数：GET status + GET telemetry/latest；
 * 2) 随后订阅 WebSocket 增量：telemetry.updated / device.status_changed /
 *    alert.state_changed / command.status_changed；
 * 3) 断线重连后由 socket 回调 onResync 再走一次 REST 补数，避免丢失增量。
 *
 * Mock 模式下 socket 由本地定时器投递同格式事件，页面代码无需区分。
 */
const deviceService = require('../../services/device.js')
const socket = require('../../services/socket.js')
const { formatTime } = require('../../utils/helpers.js')

/** 复合预警状态 -> 文案与配色（枚举值来自契约 §4） */
const ALARM_STATE = {
  normal: { level: 'normal', text: '环境正常', sub: '各项指标处于安全范围' },
  suspect: { level: 'suspect', text: '疑似异常', sub: '部分条件异常，系统确认中' },
  fire_warning: { level: 'fire', text: '火情预警', sub: '气体突增与温升速率同时超限' },
  acknowledged: { level: 'suspect', text: '告警已确认', sub: '人员已确认，等待环境恢复' },
  recovered: { level: 'recovered', text: '指标已恢复', sub: '事件归档中' },
}

/** 设备连接状态 -> 标签配色 */
const CONNECTIVITY = {
  online: { level: 'green', text: '在线' },
  offline: { level: 'red', text: '离线' },
  unknown: { level: 'gray', text: '未知' },
}

Page({
  data: {
    deviceId: 'MCU001',
    loading: true,
    error: '',
    banner: ALARM_STATE.normal,
    connectivity: CONNECTIVITY.unknown,
    latest: null,
    view: null,
    updatedAt: '--:--:--',
    /** 实时通道状态文案（来自 services/socket.js 的本地连接事件） */
    streamText: '连接中…',
  },

  onLoad() {
    this.fetchInitial()
  },

  onShow() {
    if (typeof this.getTabBar === 'function' && this.getTabBar()) {
      this.getTabBar().setData({ selected: 0 })
    }
    this.subscribeStream()
  },

  onHide() {
    this.unsubscribeStream()
  },

  onUnload() {
    this.unsubscribeStream()
  },

  /* ---------- 实时订阅 ---------- */

  /** 订阅 WebSocket 事件；重连后回调 onResync 走 REST 补数 */
  subscribeStream() {
    if (this._offs) return
    socket.connect(this.data.deviceId, { onResync: () => this.resync() })

    this._offs = [
      socket.on(socket.CONNECTION_EVENT, (evt) => {
        this.setData({ streamText: this.describeStream(evt.status) })
      }),
      socket.on('telemetry.updated', (evt) => {
        this.applyTelemetry(Object.assign({}, evt.data, { deviceId: this.data.deviceId, receivedAt: evt.occurredAt }))
      }),
      socket.on('device.status_changed', () => this.fetchStatus()),
      socket.on('alert.state_changed', () => this.fetchStatus()),
      socket.on('command.status_changed', () => {
        this.fetchStatus()
        this.fetchLatest()
      }),
    ]
    this.setData({ streamText: this.describeStream(socket.getConnectionStatus()) })
  },

  unsubscribeStream() {
    if (this._offs) {
      this._offs.forEach((off) => off())
      this._offs = null
    }
    socket.disconnect()
  },

  describeStream(status) {
    if (status === 'open') return '实时推送'
    if (status === 'connecting') return '连接中…'
    if (status === 'reconnecting') return '重连中，已切 REST 兜底'
    return '未连接'
  },

  /** 断线重连后的 REST 补数（只拉最新值，不整页 loading） */
  async resync() {
    await Promise.all([this.fetchStatus(), this.fetchLatest()])
  },

  /* ---------- REST 补数 ---------- */

  /** 下拉刷新 */
  async onPullDownRefresh() {
    await this.fetchInitial()
    wx.stopPullDownRefresh()
  },

  /** 初始化：并行拉取设备状态与最新遥测 */
  async fetchInitial() {
    try {
      const [status, latest] = await Promise.all([
        deviceService.getStatus(this.data.deviceId),
        deviceService.getLatestTelemetry(this.data.deviceId),
      ])
      this.applyStatus(status)
      this.applyTelemetry(latest)
      this.setData({ loading: false, error: '' })
    } catch (e) {
      this.setData({ loading: false, error: (e && e.message) || '数据加载失败' })
    }
  },

  async fetchStatus() {
    try {
      this.applyStatus(await deviceService.getStatus(this.data.deviceId))
    } catch (e) {
      // 单次补数失败不打断页面，等待下次事件或兜底轮询
    }
  },

  async fetchLatest() {
    try {
      this.applyTelemetry(await deviceService.getLatestTelemetry(this.data.deviceId))
    } catch (e) {
      // 同上
    }
  },

  applyStatus(status) {
    this.setData({
      banner: ALARM_STATE[status.alarmState] || ALARM_STATE.normal,
      connectivity: CONNECTIVITY[status.connectivity] || CONNECTIVITY.unknown,
    })
  },

  /** 将遥测转换为视图模型：字符串数值 + 进度条百分比 */
  applyTelemetry(latest) {
    this.setData({
      latest,
      updatedAt: formatTime(latest.receivedAt || latest.timestamp),
      view: {
        temperatureC: latest.temperatureC.toFixed(1),
        humidityRh: latest.humidityRh.toFixed(1),
        gasPpm: latest.gasPpm.toFixed(1),
        tempPercent: Math.min(100, (latest.temperatureC / 40) * 100),
        humPercent: Math.min(100, latest.humidityRh),
        gasPercent: Math.min(100, (latest.gasPpm / 100) * 100),
      },
    })
  },
})
