const test = require('node:test')
const assert = require('node:assert/strict')
const presentation = require('../utils/presentation.js')
const { validateThresholds } = require('../utils/threshold-limits.js')

test('dashboard derives proper risk status and tone for all 4 contract alert states', () => {
  const statusNormal = { alarmState: 'normal', connectivity: 'online', localAlarm: false }
  const viewNormal = presentation.dashboard(statusNormal, null)
  assert.equal(viewNormal.riskLevel, 'normal')
  assert.equal(viewNormal.riskTone, 'mint')
  assert.equal(viewNormal.riskText, '环境正常')

  const statusSuspect = { alarmState: 'suspect', connectivity: 'online', localAlarm: false }
  const viewSuspect = presentation.dashboard(statusSuspect, null)
  assert.equal(viewSuspect.riskLevel, 'suspect')
  assert.equal(viewSuspect.riskTone, 'warning')
  assert.equal(viewSuspect.riskText, '疑似异常')

  const statusFire = { alarmState: 'fire_warning', connectivity: 'online', localAlarm: true }
  const viewFire = presentation.dashboard(statusFire, null)
  assert.equal(viewFire.riskLevel, 'fire')
  assert.equal(viewFire.riskTone, 'danger')
  assert.equal(viewFire.riskText, '火情预警')

  const statusRecovered = { alarmState: 'recovered', connectivity: 'offline', localAlarm: false }
  const viewRecovered = presentation.dashboard(statusRecovered, null)
  assert.equal(viewRecovered.riskLevel, 'recovered')
  assert.equal(viewRecovered.riskTone, 'info')
  assert.equal(viewRecovered.riskText, '指标已恢复')
  assert.equal(viewRecovered.connectivityText, '离线')
})

test('dashboard handles null telemetry as empty data without crashing', () => {
  const status = { deviceId: 'MCU001', alarmState: 'normal', connectivity: 'online', localAlarm: false }
  const view = presentation.dashboard(status, null)
  assert.equal(view.hasData, false)
  assert.equal(view.temperatureText, '--')
  assert.equal(view.humidityText, '--')
  assert.equal(view.gasText, '--')
  assert.equal(view.gasAvailable, false)
  assert.equal(view.temperaturePercent, 0)
  assert.equal(view.humidityPercent, 0)
  assert.equal(view.gasPercent, 0)
  assert.equal(view.buzzerText, '待机')
})

test('dashboard renders integer readings, contract percentages and gasPpm:null as unmeasured', () => {
  const status = { deviceId: 'MCU001', alarmState: 'normal', connectivity: 'online', localAlarm: false }
  const tel1 = {
    receivedAt: '2026-09-23T14:30:00Z',
    temperatureC: 26.6,
    humidityRh: 59.4,
    gasPpm: 25.4,
    localAlarm: false,
  }
  const v1 = presentation.dashboard(status, tel1)
  assert.equal(v1.hasData, true)
  assert.equal(v1.temperatureText, '27') // 26.6 -> 27
  assert.equal(v1.humidityText, '59') // 59.4 -> 59
  assert.equal(v1.gasText, '25') // 25.4 -> 25
  assert.equal(v1.gasAvailable, true)
  assert.equal(v1.temperaturePercent, 33) // 26.6 / 80 * 100 = 33.25 -> 33
  assert.equal(v1.humidityPercent, 59) // 59.4 / 100 * 100 = 59.4 -> 59
  assert.equal(v1.gasPercent, 3) // 25.4 / 999 * 100 = 2.54 -> 3
  assert.equal(v1.buzzerText, '待机')

  // gasPpm 为 null 时表示未测量，必须为 '--'，绝不能显示成 0
  const telUncalibrated = {
    receivedAt: '2026-09-23T14:30:00Z',
    temperatureC: 30.0,
    humidityRh: 60.0,
    gasPpm: null,
    localAlarm: true,
  }
  const v2 = presentation.dashboard(status, telUncalibrated)
  assert.equal(v2.gasText, '--')
  assert.equal(v2.gasAvailable, false)
  assert.equal(v2.gasPercent, 0)
  assert.equal(v2.localAlarm, true)
  assert.equal(v2.localAlarmText, '报警中')
  assert.equal(v2.buzzerText, '报警策略生效')
})

test('trends aggregates points into integer summaries and handles uncalibrated gas gaps', () => {
  const points = [
    { receivedAt: '2026-09-23T14:00:00Z', temperatureC: 20.4, humidityRh: 50.0, gasPpm: 10.0, sequence: 1 },
    { receivedAt: '2026-09-23T14:05:00Z', temperatureC: 25.6, humidityRh: 55.0, gasPpm: null, sequence: 2 }, // 缺失气体
    { receivedAt: '2026-09-23T14:10:00Z', temperatureC: 30.0, humidityRh: 60.0, gasPpm: 30.0, sequence: 3 },
  ]

  const view = presentation.trends(points, 'LAST_HOUR')
  assert.equal(view.sampleCount, 3)
  assert.equal(view.hasData, true)

  // 温度：min 20.4 -> 20, max 30.0 -> 30, avg (20.4+25.6+30)/3 = 25.33 -> 25
  assert.equal(view.temperature.minimum, '20')
  assert.equal(view.temperature.average, '25')
  assert.equal(view.temperature.maximum, '30')
  assert.equal(view.temperature.peakAt, '14:10:00')

  // 气体：仅有 2 个有效读数，缺失点不拉低平均值
  assert.equal(view.gasSampleCount, 2)
  assert.equal(view.gas.minimum, '10')
  assert.equal(view.gas.maximum, '30')
  assert.equal(view.gas.average, '20') // (10+30)/2 = 20
  assert.equal(view.gas.peakAt, '14:10:00')

  // 坐标端点
  assert.equal(view.curveAxisStart, '14:00:00')
  assert.equal(view.curveAxisEnd, '14:10:00')
})

test('trends handles empty points gracefully', () => {
  const view = presentation.trends([], 'LAST_HOUR')
  assert.equal(view.hasData, false)
  assert.equal(view.sampleCount, 0)
  assert.equal(view.temperature.minimum, '--')
  assert.equal(view.curveStatusText, '暂无数据')
  assert.equal(view.curveAxisStart, '--')
  assert.equal(view.curveAxisEnd, '--')
})

test('alerts maps events with ADC evidence and local filtering without acknowledged state', () => {
  const events = [
    {
      id: 'a1',
      state: 'fire_warning',
      startedAt: '2026-09-23T10:00:00Z',
      endedAt: null,
      evidence: { gasAdcRise: 180, gasAdcRiseThreshold: 150, temperatureRateCPerMinute: 4.25, sampleCount: 8 },
    },
    {
      id: 'a2',
      state: 'suspect',
      startedAt: '2026-09-23T11:00:00Z',
      endedAt: null,
      evidence: { gasAdcRise: 120, gasAdcRiseThreshold: 150, temperatureRateCPerMinute: 1.5, sampleCount: 5 },
    },
    {
      id: 'a3',
      state: 'recovered',
      startedAt: '2026-09-23T09:00:00Z',
      endedAt: '2026-09-23T09:30:00Z',
      evidence: { gasAdcRise: 90, gasAdcRiseThreshold: 150, temperatureRateCPerMinute: 0.8, sampleCount: 6 },
    },
    {
      id: 'a4',
      state: 'normal',
      startedAt: '2026-09-23T08:00:00Z',
      endedAt: '2026-09-23T08:30:00Z',
    },
  ]

  const viewAll = presentation.alerts(events, 'all')
  assert.equal(viewAll.count, 4)
  assert.equal(viewAll.visibleCount, 4)
  assert.equal(viewAll.items[0].gasAdcRiseText, '180')
  assert.equal(viewAll.items[0].temperatureRateText, '4.3°C/min')
  assert.equal(viewAll.items[0].active, true)
  assert.equal(viewAll.items[2].active, false)
  assert.equal(viewAll.items[2].endedAt, '09:30:00')
  assert.equal(viewAll.items[3].stateText, '正常')

  const viewSuspect = presentation.alerts(events, 'suspect')
  assert.equal(viewSuspect.visibleCount, 1)
  assert.equal(viewSuspect.items[0].id, 'a2')
})

test('settings resolves confirmation wording, version matching and save hint', () => {
  const pendingThresholds = {
    desiredVersion: 5,
    confirmedVersion: 4,
    confirmationState: 'pending',
    temperatureHighC: 30,
    humidityHighRh: 70,
    gasHighPpm: 50,
  }
  const pendingView = presentation.settings(pendingThresholds)
  assert.equal(pendingView.confirmed, false)
  assert.equal(pendingView.awaitingDevice, true)
  assert.equal(pendingView.confirmationText, '等待设备确认')
  assert.equal(pendingView.confirmationTone, 'warning')
  assert.equal(pendingView.saveHint, '下发后需设备确认，确认前仍按旧规则报警')

  const confirmedThresholds = {
    desiredVersion: 5,
    confirmedVersion: 5,
    confirmationState: 'confirmed',
    temperatureHighC: 30,
    humidityHighRh: 70,
    gasHighPpm: 50,
  }
  const confirmedView = presentation.settings(confirmedThresholds)
  assert.equal(confirmedView.confirmed, true)
  assert.equal(confirmedView.awaitingDevice, false)
  assert.equal(confirmedView.confirmationText, '设备已确认')
  assert.equal(confirmedView.confirmationTone, 'mint')

  const rejectedView = presentation.settings({ confirmationState: 'rejected' })
  assert.equal(rejectedView.confirmationText, '设备已拒绝')
  assert.equal(rejectedView.confirmationTone, 'danger')

  const timedOutView = presentation.settings({ confirmationState: 'timed_out' })
  assert.equal(timedOutView.confirmationText, '确认超时，请重试')
  assert.equal(timedOutView.confirmationTone, 'danger')

  const otherView = presentation.settings({ confirmationState: 'unknown_state' })
  assert.equal(otherView.confirmationText, 'unknown_state')
  assert.equal(otherView.confirmationTone, 'info')
})

test('commandStatus maps lifecycle states accurately', () => {
  const accepted = presentation.commandStatus({ state: 'accepted' })
  assert.equal(accepted.settled, false)
  assert.equal(accepted.stateText, '命令已接受')
  assert.equal(accepted.tone, 'warning')

  const published = presentation.commandStatus({ state: 'published' })
  assert.equal(published.settled, false)
  assert.equal(published.confirmed, false)
  assert.equal(published.stateText, '已下发，等待设备确认')

  const applied = presentation.commandStatus({ state: 'applied', confirmedVersion: 6 })
  assert.equal(applied.settled, true)
  assert.equal(applied.confirmed, true)
  assert.equal(applied.failed, false)
  assert.equal(applied.stateText, '设备已确认')
  assert.equal(applied.tone, 'mint')

  const rejected = presentation.commandStatus({ state: 'rejected' })
  assert.equal(rejected.settled, true)
  assert.equal(rejected.stateText, '设备已拒绝')

  const expired = presentation.commandStatus({ state: 'expired' })
  assert.equal(expired.settled, true)
  assert.equal(expired.stateText, '命令已过期')

  const duplicate = presentation.commandStatus({ state: 'duplicate' })
  assert.equal(duplicate.settled, true)
  assert.equal(duplicate.stateText, '重复命令，已忽略')
  assert.equal(duplicate.tone, 'info')

  const failed = presentation.commandStatus({ state: 'failed' })
  assert.equal(failed.settled, true)
  assert.equal(failed.stateText, '设备执行失败')

  const timedOut = presentation.commandStatus({ state: 'timed_out', errorCode: 'timeout' })
  assert.equal(timedOut.settled, true)
  assert.equal(timedOut.confirmed, false)
  assert.equal(timedOut.failed, true)
  assert.equal(timedOut.stateText, '设备确认超时')
  assert.equal(timedOut.tone, 'danger')

  const pubFailed = presentation.commandStatus({ state: 'publish_failed' })
  assert.equal(pubFailed.settled, true)
  assert.equal(pubFailed.stateText, '下发失败')

  const otherState = presentation.commandStatus({ state: 'other' })
  assert.equal(otherState.stateText, 'other')
})

test('validateThresholds enforces contract boundaries with identical KMP copy', () => {
  assert.doesNotThrow(() => {
    validateThresholds({ temperatureHighC: 0, humidityHighRh: 0, gasHighPpm: 1 })
    validateThresholds({ temperatureHighC: 80, humidityHighRh: 100, gasHighPpm: 999 })
  })

  assert.throws(() => validateThresholds(null), /阈值参数不能为空/)
  assert.throws(() => validateThresholds({ temperatureHighC: -1, humidityHighRh: 50, gasHighPpm: 50 }), /温度阈值需在 0-80 °C/)
  assert.throws(() => validateThresholds({ temperatureHighC: 81, humidityHighRh: 50, gasHighPpm: 50 }), /温度阈值需在 0-80 °C/)
  assert.throws(() => validateThresholds({ temperatureHighC: 25, humidityHighRh: -1, gasHighPpm: 50 }), /湿度阈值需在 0-100 %RH/)
  assert.throws(() => validateThresholds({ temperatureHighC: 25, humidityHighRh: 101, gasHighPpm: 50 }), /湿度阈值需在 0-100 %RH/)
  assert.throws(() => validateThresholds({ temperatureHighC: 25, humidityHighRh: 50, gasHighPpm: 0 }), /气体阈值需在 1-999 ppm/)
  assert.throws(() => validateThresholds({ temperatureHighC: 25, humidityHighRh: 50, gasHighPpm: 1000 }), /气体阈值需在 1-999 ppm/)
})
